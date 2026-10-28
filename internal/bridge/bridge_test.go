package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ringlymessenger/ringly-relay/internal/config"
)

// Вместо настоящего sing-box тесты запускают САМ тестовый бинарник в роли sing-box (переменная
// FAKE_SINGBOX): он понимает `check` и `run`, слушает порт из конфига и умеет падать по сценарию.
// Так проверяется ровно то, что ломалось на живой установке: готовность порта, падение при
// запуске и падение уже после старта.
func TestMain(m *testing.M) {
	if mode := os.Getenv("FAKE_SINGBOX"); mode != "" {
		os.Exit(fakeSingbox(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeSingbox(mode string, args []string) int {
	if len(args) == 0 {
		return 2
	}
	if args[0] == "check" {
		if mode == "badconfig" {
			fmt.Fprintln(os.Stderr, "FATAL decode config: unknown field")
			return 1
		}
		return 0
	}
	if mode == "denied" {
		fmt.Fprintln(os.Stderr, "FATAL start service: listen tcp 0.0.0.0:443: bind: permission denied")
		return 1
	}
	var conf struct {
		Inbounds []struct {
			ListenPort int `json:"listen_port"`
		} `json:"inbounds"`
	}
	raw, err := os.ReadFile(args[len(args)-1])
	if err != nil || json.Unmarshal(raw, &conf) != nil || len(conf.Inbounds) == 0 {
		return 3
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", conf.Inbounds[0].ListenPort))
	if err != nil {
		return 4
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if mode == "dielater" {
		time.Sleep(300 * time.Millisecond)
		fmt.Fprintln(os.Stderr, "FATAL inbound crashed")
		return 1
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
	<-sig
	return 0
}

// writeStub — исполняемый «sing-box», который перезапускает тестовый бинарник в нужном режиме.
func writeStub(t *testing.T, mode string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "sing-box")
	script := fmt.Sprintf("#!/bin/sh\nFAKE_SINGBOX=%s exec %q \"$@\"\n", mode, self)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("запись заглушки: %v", err)
	}
	return path
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// stubConfig — валидный конфиг мостика на свободном порту, где вместо sing-box подставлена заглушка.
func stubConfig(t *testing.T, singboxPath string) config.Config {
	t.Helper()
	c := config.Default()
	c.Bridge.SingboxPath = singboxPath
	c.Bridge.Listen = fmt.Sprintf(":%d", freePort(t))
	c.Reality.SNI = "www.microsoft.com"
	c.Reality.PrivateKey = "26d5WDpy5OtXi5Z3Dos1me-Z5Zk0ScUxtsitvV4As64"
	c.Reality.ShortID = "08b992eec8cf3b84"
	c.Reality.UUID = "11111111-2222-4333-8444-555555555555"
	return c
}

func TestStartStopLifecycle(t *testing.T) {
	c := stubConfig(t, writeStub(t, "ok"))
	m := New(c, io.Discard, nil)

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Start вернулся — значит порт уже принимает соединения
	port, _ := config.PortFromListen(c.Bridge.Listen)
	if conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err != nil {
		t.Fatalf("после Start порт не слушается: %v", err)
	} else {
		conn.Close()
	}

	m.mu.Lock()
	confPath := m.confPath
	m.mu.Unlock()

	start := time.Now()
	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("Stop не уложился в ожидаемое время")
	}
	if _, err := os.Stat(filepath.Dir(confPath)); !os.IsNotExist(err) {
		t.Fatalf("временный каталог конфига не удалён после Stop: %v", err)
	}
	select {
	case err := <-m.Failed():
		t.Fatalf("штатная остановка не должна считаться аварией: %v", err)
	default:
	}
}

// Падение при запуске (нет прав на 443) — ошибка Start с выводом sing-box и подсказкой, а не «готов».
func TestStartFailsWhenSingboxDies(t *testing.T) {
	c := stubConfig(t, writeStub(t, "denied"))
	c.Bridge.Listen = ":443" // подсказка про права даётся для привилегированных портов
	err := New(c, io.Discard, nil).Start(context.Background())
	if err == nil {
		t.Fatal("ожидалась ошибка: sing-box упал при запуске")
	}
	for _, want := range []string{"permission denied", "setcap"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("в ошибке нет %q:\n%v", want, err)
		}
	}
}

func TestStartFailsOnBadConfig(t *testing.T) {
	err := New(stubConfig(t, writeStub(t, "badconfig")), io.Discard, nil).Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "не принял конфиг") {
		t.Fatalf("ожидалась ошибка sing-box check, получено: %v", err)
	}
}

// Падение уже после старта уходит в Failed() — runner по нему завершит демон.
func TestFailedAfterStart(t *testing.T) {
	m := New(stubConfig(t, writeStub(t, "dielater")), io.Discard, nil)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case err := <-m.Failed():
		if !strings.Contains(err.Error(), "inbound crashed") {
			t.Errorf("в ошибке нет вывода sing-box: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("падение sing-box не дошло до Failed()")
	}
}

func TestStartMissingBinary(t *testing.T) {
	m := New(stubConfig(t, "/nonexistent/sing-box-xyz"), io.Discard, nil)
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("ожидалась ошибка при отсутствующем бинарнике sing-box")
	}
}

func TestStopWithoutStart(t *testing.T) {
	m := New(stubConfig(t, writeStub(t, "ok")), io.Discard, nil)
	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("Stop без Start должен быть безопасен, получено: %v", err)
	}
}
