// Package bridge — модуль мостика сообщений: супервизор процесса sing-box (VLESS + Reality).
//
// Реализует runner.Module. Start генерирует конфиг, проверяет его `sing-box check`, запускает процесс
// и возвращается, только когда порт реально принимает соединения. Раньше «готов» печаталось сразу
// после cmd.Start(): sing-box мог тут же упасть (нет прав на 443, кривой конфиг), а демон продолжал
// жить с мёртвым портом — и Docker его не перезапускал, ведь основной процесс не завершился.
// Теперь неожиданный выход sing-box отдаётся в Failed(), runner завершает демон с ошибкой, а
// перезапуск делает супервизор (Docker restart / systemd Restart=on-failure).
package bridge

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ringlymessenger/ringly-relay/internal/config"
	"github.com/ringlymessenger/ringly-relay/internal/netcheck"
	"github.com/ringlymessenger/ringly-relay/internal/singbox"
	"github.com/ringlymessenger/ringly-relay/internal/stats"
)

// Сколько ждать, пока sing-box откроет порт. Обычно это доли секунды.
var readyTimeout = 10 * time.Second

// Module супервизирует sing-box для мостика сообщений.
type Module struct {
	cfg      config.Config
	out      io.Writer
	counters *stats.Counters // трафик и подключения для отчёта на сервер (nil — не считаем)

	mu       sync.Mutex
	cmd      *exec.Cmd
	confPath string
	stopped  bool          // Stop вызван — неожиданный выход не логируем как аварию
	done     chan struct{} // закрывается в watch(), когда процесс завершён
	tail     *tailBuffer   // последние строки вывода sing-box — для внятной ошибки
	failed   chan error    // неожиданный выход sing-box после старта (см. Failed)
}

// New создаёт модуль мостика. counters — общие счётчики демона (может быть nil).
func New(cfg config.Config, out io.Writer, counters *stats.Counters) *Module {
	return &Module{cfg: cfg, out: out, counters: counters}
}

// Name — имя модуля для логов runner.
func (m *Module) Name() string { return "bridge" }

// Failed отдаёт ошибку, если sing-box завершился сам после успешного старта.
func (m *Module) Failed() <-chan error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.failed
}

// Binary — путь к sing-box из конфига (или из PATH).
func Binary(c config.Config) (string, error) {
	bin := c.Bridge.SingboxPath
	if bin == "" {
		bin = "sing-box"
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return "", fmt.Errorf("не найден sing-box (%q). В Docker-образе он уже есть; при ручной установке "+
			"поставьте sing-box 1.12+ (https://sing-box.sagernet.org) или укажите bridge.singbox_path", bin)
	}
	return resolved, nil
}

// Check прогоняет сгенерированный конфиг через `sing-box check` — точная причина вместо падения
// при запуске.
func Check(c config.Config) error {
	bin, err := Binary(c)
	if err != nil {
		return err
	}
	conf, err := singbox.BridgeConfig(c)
	if err != nil {
		return err
	}
	return checkConf(bin, conf)
}

func checkConf(bin string, conf []byte) error {
	confPath, err := writeTempConfig(conf)
	if err != nil {
		return err
	}
	defer os.RemoveAll(filepath.Dir(confPath))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "check", "-c", confPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("sing-box не принял конфиг: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// Start проверяет конфиг, запускает sing-box и ждёт, пока порт начнёт принимать соединения.
func (m *Module) Start(ctx context.Context) error {
	if err := Check(m.cfg); err != nil {
		return err
	}
	resolved, err := Binary(m.cfg)
	if err != nil {
		return err
	}
	port, err := config.PortFromListen(m.cfg.Bridge.Listen)
	if err != nil {
		return err
	}
	if netcheck.Listening(port) {
		return fmt.Errorf("порт %d уже занят другой программой (часто это веб-сервер). "+
			"Освободите его или выберите другой порт в `ringly-relay setup`", port)
	}

	conf, err := singbox.BridgeConfig(m.cfg)
	if err != nil {
		return err
	}
	// Статистика (трафик, число клиентов) — необязательная надстройка: если sing-box её не принял
	// (сборка без Clash API), мостик всё равно работает, просто без цифр в админке.
	api, withStats := m.statsConfig(resolved)
	if withStats != nil {
		conf = withStats
	}
	confPath, err := writeTempConfig(conf)
	if err != nil {
		return err
	}

	tail := &tailBuffer{max: 20}
	// ctx НЕ передаём в CommandContext: остановкой управляет Stop (graceful SIGTERM),
	// иначе отмена ctx убила бы процесс жёстко, минуя корректное завершение.
	cmd := exec.Command(resolved, "run", "-c", confPath)
	cmd.Stdout = io.MultiWriter(prefixWriter{w: m.out, prefix: "[sing-box] "}, tail)
	cmd.Stderr = io.MultiWriter(prefixWriter{w: m.out, prefix: "[sing-box] "}, tail)

	if err := cmd.Start(); err != nil {
		os.RemoveAll(filepath.Dir(confPath))
		return fmt.Errorf("запуск sing-box: %w", err)
	}

	done := make(chan struct{})
	failed := make(chan error, 1)
	m.mu.Lock()
	m.cmd = cmd
	m.confPath = confPath
	m.stopped = false
	m.done = done
	m.tail = tail
	m.failed = failed
	m.mu.Unlock()

	go m.watch(cmd, done, failed)

	if err := waitReady(port, done); err != nil {
		_ = m.Stop(context.Background())
		return explain(err, tail.String(), port)
	}
	if api != nil {
		go pollStats(api, m.counters, done, func(s string) { fmt.Fprintf(m.out, "[bridge] %s\n", s) })
	}
	return nil
}

// statsConfig — конфиг с API статистики, если его некому читать (нет счётчиков) или sing-box его
// не принимает, — nil, и мостик запускается с обычным конфигом.
func (m *Module) statsConfig(bin string) (*singbox.StatsAPI, []byte) {
	if m.counters == nil {
		return nil, nil
	}
	api, err := newStatsAPI()
	if err != nil {
		fmt.Fprintf(m.out, "[bridge] статистика мостика отключена: %v\n", err)
		return nil, nil
	}
	conf, err := singbox.BridgeConfigWithStats(m.cfg, api)
	if err == nil {
		err = checkConf(bin, conf)
	}
	if err != nil {
		fmt.Fprintf(m.out, "[bridge] статистика мостика отключена (sing-box не принял API статистики): %v\n", err)
		return nil, nil
	}
	return api, conf
}

// waitReady ждёт, пока порт начнёт принимать соединения, или пока процесс не умрёт.
func waitReady(port int, done <-chan struct{}) error {
	deadline := time.Now().Add(readyTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			return fmt.Errorf("sing-box завершился при запуске")
		default:
		}
		if netcheck.Listening(port) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("sing-box не открыл порт %d за %s", port, readyTimeout)
}

// explain добавляет к ошибке вывод sing-box и подсказку для самой частой причины.
func explain(err error, output string, port int) error {
	msg := err.Error()
	if output != "" {
		msg += ":\n" + output
	}
	if strings.Contains(output, "permission denied") && port < 1024 {
		msg += fmt.Sprintf("\n\nНет права слушать порт %d. В Docker-образе это уже настроено — пересоберите образ. "+
			"При ручной установке: sudo setcap cap_net_bind_service=+ep $(command -v sing-box)", port)
	}
	if strings.Contains(output, "address already in use") {
		msg += fmt.Sprintf("\n\nПорт %d занят другой программой. Освободите его или выберите другой порт в `ringly-relay setup`.", port)
	}
	return fmt.Errorf("%s", msg)
}

// watch ждёт завершения процесса, закрывает done и сообщает о неожиданном выходе.
func (m *Module) watch(cmd *exec.Cmd, done chan struct{}, failed chan<- error) {
	err := cmd.Wait()
	close(done)

	m.mu.Lock()
	stopped := m.stopped
	tail := m.tail
	m.mu.Unlock()

	if stopped {
		return // штатная остановка через Stop
	}
	reason := "сам по себе"
	if err != nil {
		reason = err.Error()
	}
	msg := fmt.Sprintf("sing-box неожиданно завершился (%s)", reason)
	if out := tail.String(); out != "" {
		msg += ". Последний вывод:\n" + out
	}
	failed <- fmt.Errorf("%s", msg)
}

// Stop гасит процесс: SIGTERM, ждём немного, при необходимости — kill; убираем временный конфиг.
func (m *Module) Stop(ctx context.Context) error {
	m.mu.Lock()
	cmd := m.cmd
	confPath := m.confPath
	done := m.done
	m.stopped = true
	m.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return nil
	}
	defer func() {
		if confPath != "" {
			os.RemoveAll(filepath.Dir(confPath))
		}
	}()

	_ = cmd.Process.Signal(os.Interrupt) // дать sing-box закрыться корректно

	deadline := 5 * time.Second
	if dl, ok := ctx.Deadline(); ok {
		if d := time.Until(dl); d < deadline {
			deadline = d
		}
	}
	select {
	case <-done:
		return nil
	case <-time.After(deadline):
		_ = cmd.Process.Kill() // не закрылся вовремя — принудительно
		<-done                 // дождаться, пока watch зафиксирует завершение
		return fmt.Errorf("sing-box не завершился за %s, отправлен kill", deadline)
	}
}

func writeTempConfig(conf []byte) (string, error) {
	dir, err := os.MkdirTemp("", "ringly-relay-singbox")
	if err != nil {
		return "", fmt.Errorf("временный каталог для конфига sing-box: %w", err)
	}
	path := filepath.Join(dir, "singbox.json")
	if err := os.WriteFile(path, conf, 0o600); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("запись конфига sing-box: %w", err)
	}
	return path, nil
}

// tailBuffer хранит последние строки вывода — чтобы ошибка запуска объясняла сама себя.
type tailBuffer struct {
	mu    sync.Mutex
	max   int
	lines []string
	part  []byte
}

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.part = append(t.part, b...)
	for {
		i := bytes.IndexByte(t.part, '\n')
		if i < 0 {
			break
		}
		t.lines = append(t.lines, string(t.part[:i]))
		t.part = t.part[i+1:]
		if len(t.lines) > t.max {
			t.lines = t.lines[len(t.lines)-t.max:]
		}
	}
	return len(b), nil
}

func (t *tailBuffer) String() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := t.lines
	if len(t.part) > 0 {
		lines = append(lines[:len(lines):len(lines)], string(t.part))
	}
	return strings.Join(lines, "\n")
}

// prefixWriter добавляет префикс к выводу дочернего процесса.
type prefixWriter struct {
	w      io.Writer
	prefix string
}

func (p prefixWriter) Write(b []byte) (int, error) {
	if _, err := fmt.Fprintf(p.w, "%s%s", p.prefix, b); err != nil {
		return 0, err
	}
	return len(b), nil
}
