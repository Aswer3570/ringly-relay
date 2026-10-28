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

// Instead of the real sing-box the tests run THE TEST BINARY ITSELF as sing-box (the FAKE_SINGBOX
// variable): it understands `check` and `run`, listens on the configured port and can fail on cue.
// This checks exactly what broke on a real installation: port readiness, a crash at startup and a
// crash after startup.
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

// writeStub — an executable "sing-box" that re-runs the test binary in the given mode.
func writeStub(t *testing.T, mode string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "sing-box")
	script := fmt.Sprintf("#!/bin/sh\nFAKE_SINGBOX=%s exec %q \"$@\"\n", mode, self)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing the stub: %v", err)
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

// stubConfig — a valid bridge configuration on a free port with the stub in place of sing-box.
func stubConfig(t *testing.T, singboxPath string) config.Config {
	t.Helper()
	c := config.Default()
	c.Bridge.SingboxPath = singboxPath
	c.Bridge.Listen = fmt.Sprintf(":%d", freePort(t))
	c.Reality.SNI = "dl.google.com"
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
	// Start returned — so the port already accepts connections
	port, _ := config.PortFromListen(c.Bridge.Listen)
	if conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err != nil {
		t.Fatalf("the port is not listening after Start: %v", err)
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
		t.Fatal("Stop took longer than expected")
	}
	if _, err := os.Stat(filepath.Dir(confPath)); !os.IsNotExist(err) {
		t.Fatalf("the temporary configuration directory was not removed after Stop: %v", err)
	}
	select {
	case err := <-m.Failed():
		t.Fatalf("a normal stop must not count as a crash: %v", err)
	default:
	}
}

// A crash at startup (no permission for 443) — a Start error with the sing-box output and a hint, not "ready".
func TestStartFailsWhenSingboxDies(t *testing.T) {
	c := stubConfig(t, writeStub(t, "denied"))
	c.Bridge.Listen = ":443" // the permission hint is given for privileged ports
	err := New(c, io.Discard, nil).Start(context.Background())
	if err == nil {
		t.Fatal("expected an error: sing-box crashed at startup")
	}
	for _, want := range []string{"permission denied", "setcap"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error lacks %q:\n%v", want, err)
		}
	}
}

func TestStartFailsOnBadConfig(t *testing.T) {
	err := New(stubConfig(t, writeStub(t, "badconfig")), io.Discard, nil).Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "rejected the configuration") {
		t.Fatalf("expected a sing-box check error, got: %v", err)
	}
}

// A crash after startup goes to Failed() — the runner exits the daemon on it.
func TestFailedAfterStart(t *testing.T) {
	m := New(stubConfig(t, writeStub(t, "dielater")), io.Discard, nil)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case err := <-m.Failed():
		if !strings.Contains(err.Error(), "inbound crashed") {
			t.Errorf("the error lacks the sing-box output: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the sing-box crash did not reach Failed()")
	}
}

func TestStartMissingBinary(t *testing.T) {
	m := New(stubConfig(t, "/nonexistent/sing-box-xyz"), io.Discard, nil)
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("expected an error with the sing-box binary missing")
	}
}

func TestStopWithoutStart(t *testing.T) {
	m := New(stubConfig(t, writeStub(t, "ok")), io.Discard, nil)
	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("Stop without Start must be safe, got: %v", err)
	}
}
