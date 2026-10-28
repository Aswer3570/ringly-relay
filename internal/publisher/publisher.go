// Package publisher — модуль автопубликации: регистрирует демон на control-сервере и шлёт heartbeat.
//
// Реализует runner.Module. Работает только в режиме publish=auto. Регистрация с ретраями в фоне
// (транзиентная недоступность control не мешает мостику/TURN обслуживать уже известных клиентов);
// heartbeat раз в минуту.
//
// Регистрация — ОДИН раз за жизнь установки: токен лежит в файле состояния (cfg.StatePath) и
// переживает перезапуски. Раньше токен жил только в памяти, а при остановке демон снимал себя с
// публикации — каждый перезапуск (обновление, перезагрузка VPS, автоперезапуск после падения) был
// новой регистрацией по общему секрету. Смена секрета на сервере тогда постепенно выбивала все
// мостики. Теперь секрет нужен только новым установкам. На старте демон шлёт регистрацию СО своим
// токеном — сервер обновляет его запись (адрес, ключи могли поменяться), а не заводит новую.
// Сервер токен не узнал (запись удалили) — регистрация заново, а не молчаливое висение вне выдачи.
package publisher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ringlymessenger/ringly-relay/internal/bridgelink"
	"github.com/ringlymessenger/ringly-relay/internal/config"
	"github.com/ringlymessenger/ringly-relay/internal/netcheck"
	"github.com/ringlymessenger/ringly-relay/internal/stats"
)

// Переменные, а не константы, — чтобы тесты не ждали минуту.
var (
	heartbeatInterval = 60 * time.Second
	registerRetry     = 30 * time.Second
	httpTimeout       = 15 * time.Second
)

// state — то, что демон пишет сам (не человек): кто мы в пуле. Control — к какому серверу относится
// токен: сменили сервер в конфиге — старый токен там ничего не значит.
type state struct {
	RelayID    string `json:"relayId"`
	RelayToken string `json:"relayToken"`
	Control    string `json:"control"`
}

// httpError — ответ сервера не 2xx; по коду отличаем «не узнал токен» от прочих сбоев.
type httpError struct {
	path   string
	status int
	body   string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("%s → HTTP %d: %s", e.path, e.status, e.body)
}

func isUnauthorized(err error) bool {
	var he *httpError
	return errors.As(err, &he) && he.status == http.StatusUnauthorized
}

// Module публикует демон на control-сервере.
type Module struct {
	cfg      config.Config
	out      io.Writer
	client   *http.Client
	counters *stats.Counters // трафик и подключения — едут в каждом heartbeat (nil — без них)

	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu    sync.Mutex
	token string // per-relay токен, полученный при регистрации
}

// New создаёт модуль публикатора. counters — общие счётчики демона (может быть nil).
func New(cfg config.Config, out io.Writer, counters *stats.Counters) *Module {
	return &Module{cfg: cfg, out: out, client: &http.Client{Timeout: httpTimeout}, counters: counters}
}

// Name — имя модуля для логов runner.
func (m *Module) Name() string { return "publisher" }

// Start запускает фоновую регистрацию + heartbeat. Возвращается сразу: недоступность control не
// должна валить демон (мостик/TURN продолжают обслуживать уже известных клиентов).
func (m *Module) Start(_ context.Context) error {
	if st, ok := m.loadState(); ok {
		m.mu.Lock()
		m.token = st.RelayToken
		m.mu.Unlock()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.wg.Add(1)
	go m.loop(ctx)
	return nil
}

// Stop останавливает фон. С публикации НЕ снимаемся: остановка почти всегда — перезапуск, а
// «живость» сервер и так считает по heartbeat (нет сигнала 3 минуты — мостик не выдаётся).
func (m *Module) Stop(_ context.Context) error {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
	return nil
}

func (m *Module) loop(ctx context.Context) {
	defer m.wg.Done()

	for {
		// Регистрация с ретраями. Со своим токеном — это обновление записи, без него — новая.
		for {
			err := m.register(ctx)
			if err == nil {
				break
			}
			fmt.Fprintf(m.out, "[publisher] регистрация не удалась: %v — повтор через %s\n", err, registerRetry)
			if !sleepCtx(ctx, registerRetry) {
				return
			}
		}

		// heartbeat — до тех пор, пока сервер узнаёт токен
		if !m.heartbeatLoop(ctx) {
			return
		}
		fmt.Fprintln(m.out, "[publisher] сервер не узнал токен — регистрируюсь заново")
		m.forgetToken()
	}
}

// heartbeatLoop шлёт heartbeat раз в heartbeatInterval. false — контекст отменён, true — сервер
// перестал узнавать токен и нужна регистрация заново.
func (m *Module) heartbeatLoop(ctx context.Context) bool {
	t := time.NewTicker(heartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			err := m.heartbeat(ctx)
			if isUnauthorized(err) {
				return true
			}
			if err != nil {
				fmt.Fprintf(m.out, "[publisher] heartbeat не прошёл: %v\n", err)
			}
		}
	}
}

func (m *Module) register(ctx context.Context) error {
	body := m.registerPayload()
	var resp struct {
		RelayID    string `json:"relayId"`
		RelayToken string `json:"relayToken"`
	}
	token := m.currentToken()
	err := m.post(ctx, "/relays/register", body, m.cfg.Publish.EnrollSecret, token, &resp)
	if err != nil && token != "" && isUnauthorized(err) {
		// токен сервер не знает и секрет не принял — сохранённый токен бесполезен, начнём с чистого
		m.forgetToken()
	}
	if err != nil {
		return err
	}
	if resp.RelayToken == "" {
		return fmt.Errorf("сервер не вернул relayToken")
	}
	m.mu.Lock()
	m.token = resp.RelayToken
	m.mu.Unlock()
	if err := m.saveState(state{RelayID: resp.RelayID, RelayToken: resp.RelayToken, Control: m.cfg.Origin.Control}); err != nil {
		// не фатально: работаем, но после перезапуска придётся регистрироваться заново
		fmt.Fprintf(m.out, "[publisher] ⚠️ не сохранить состояние (%v) — после перезапуска демон зарегистрируется заново\n", err)
	}
	if token == resp.RelayToken {
		fmt.Fprintln(m.out, "[publisher] регистрация обновлена на control-сервере")
	} else {
		fmt.Fprintln(m.out, "[publisher] зарегистрирован на control-сервере")
	}
	return nil
}

func (m *Module) heartbeat(ctx context.Context) error {
	var resp struct {
		Disabled bool `json:"disabled"`
		Blocked  bool `json:"blocked"`
	}
	// Прирост байтов с прошлого успешного отчёта + сколько подключено сейчас. Только числа: кто
	// подключён, сервер не узнаёт. Не дошёл отчёт — прирост уедет следующим (Commit только после 2xx).
	report := m.counters.Snapshot()
	body := map[string]any{
		"bridgeBytes": report.BridgeBytes,
		"callsBytes":  report.CallsBytes,
		"callsActive": report.CallsActive,
	}
	if report.BridgeKnown { // без статистики sing-box число клиентов неизвестно — не врём нулём
		body["bridgeClients"] = report.BridgeClients
	}
	if err := m.post(ctx, "/relays/heartbeat", body, "", m.currentToken(), &resp); err != nil {
		return err
	}
	m.counters.Commit(report)
	if resp.Blocked {
		fmt.Fprintln(m.out, "⚠️ [publisher] мостик заблокирован администратором Ringly и пользователям не выдаётся")
	} else if resp.Disabled {
		fmt.Fprintln(m.out, "⚠️ [publisher] сервер снял демон с выдачи (вероятно, превышен лимит трафика)")
	}
	return nil
}

// registerPayload собирает тело регистрации из конфига (соответствует RegisterRelaySchema бэкенда).
func (m *Module) registerPayload() map[string]any {
	body := map[string]any{
		"offersBridge": m.cfg.Bridge.Enabled,
		"offersCalls":  m.cfg.Calls.Enabled,
	}
	// Адрес отправляем ВСЕГДА. Если его нет, сервер берёт IP запроса, а перед API стоит Nginx —
	// сервер увидел бы адрес прокси и раздавал клиентам мостик, до которого не достучаться.
	host := m.cfg.Publish.PublicHost
	if host == "" {
		if ip, err := netcheck.PublicIP(); err == nil {
			host = ip
		} else {
			fmt.Fprintf(m.out, "[publisher] ⚠️ публичный адрес не определён (%v) — задайте его в ringly-relay setup\n", err)
		}
	}
	if host != "" {
		body["host"] = host
	}
	if m.cfg.Bridge.Enabled {
		if port, err := config.PortFromListen(m.cfg.Bridge.Listen); err == nil {
			if pub, err := bridgelink.PublicFromPrivate(m.cfg.Reality.PrivateKey); err == nil {
				body["bridge"] = map[string]any{
					"port":             port,
					"realityPublicKey": pub,
					"shortId":          m.cfg.Reality.ShortID,
					"sni":              m.cfg.Reality.SNI,
					"vlessUuid":        m.cfg.Reality.UUID,
				}
			}
		}
	}
	if m.cfg.Calls.Enabled {
		turnPort, err1 := config.PortFromListen(m.cfg.Calls.TurnListen)
		turnTLS, err2 := config.PortFromListen(m.cfg.Calls.TurnTLSListen)
		if err1 == nil && err2 == nil {
			body["calls"] = map[string]any{
				"turnPort":     turnPort,
				"turnTlsPort":  turnTLS,
				"secret":       m.cfg.Calls.Secret,
				"trafficCapGB": m.cfg.Calls.TrafficCapGB,
			}
		}
	}
	return body
}

func (m *Module) forgetToken() {
	m.mu.Lock()
	m.token = ""
	m.mu.Unlock()
	if m.cfg.StatePath != "" {
		_ = os.Remove(m.cfg.StatePath)
	}
}

func (m *Module) loadState() (state, bool) {
	if m.cfg.StatePath == "" {
		return state{}, false
	}
	raw, err := os.ReadFile(m.cfg.StatePath)
	if err != nil {
		return state{}, false
	}
	var st state
	if json.Unmarshal(raw, &st) != nil || st.RelayToken == "" || st.Control != m.cfg.Origin.Control {
		return state{}, false
	}
	return st, true
}

// saveState пишет атомарно (временный файл + rename) и только для владельца: токен — пропуск в пул.
func (m *Module) saveState(st state) error {
	if m.cfg.StatePath == "" {
		return nil
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(m.cfg.StatePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), m.cfg.StatePath)
}

func (m *Module) currentToken() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.token
}

// post отправляет JSON и разбирает ответ-конверт бэкенда { success, data, error }, декодируя data в out.
func (m *Module) post(ctx context.Context, path string, body any, enroll, token string, out any) error {
	req, err := m.newRequest(ctx, http.MethodPost, path, body, enroll, token)
	if err != nil {
		return err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &httpError{path: path, status: resp.StatusCode, body: strings.TrimSpace(string(raw))}
	}
	var env struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
		Error   string          `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("разбор ответа %s: %w", path, err)
	}
	if !env.Success {
		return fmt.Errorf("%s: %s", path, env.Error)
	}
	if out != nil && len(env.Data) > 0 {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

func (m *Module) newRequest(ctx context.Context, method, path string, body any, enroll, token string) (*http.Request, error) {
	var buf io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		buf = bytes.NewReader(b)
	}
	url := strings.TrimRight(m.cfg.Origin.Control, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, url, buf)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if enroll != "" {
		req.Header.Set("X-Relay-Enroll", enroll)
	}
	if token != "" {
		req.Header.Set("X-Relay-Token", token)
	}
	return req, nil
}

// sleepCtx спит d или возвращает false, если ctx отменён раньше.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
