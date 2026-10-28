// Package bridge — the message bridge module: a supervisor for the sing-box process (VLESS + Reality).
//
// Implements runner.Module. Start generates the configuration, checks it with `sing-box check`,
// starts the process and returns only once the port actually accepts connections. "Ready" used to be
// printed right after cmd.Start(): sing-box could crash immediately (no permission for 443, a broken
// configuration) while the daemon kept living with a dead port — and Docker didn't restart it, since
// the main process hadn't exited. Now an unexpected sing-box exit is reported through Failed(), the
// runner exits the daemon with an error and the supervisor restarts it (Docker restart / systemd
// Restart=on-failure).
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

// How long to wait for sing-box to open its port. Usually a fraction of a second.
var readyTimeout = 10 * time.Second

// Module supervises sing-box for the message bridge.
type Module struct {
	cfg      config.Config
	out      io.Writer
	counters *stats.Counters // traffic and connections for the server report (nil — not counted)

	mu       sync.Mutex
	cmd      *exec.Cmd
	confPath string
	stopped  bool          // Stop was called — the exit is not logged as a crash
	done     chan struct{} // closed in watch() once the process has exited
	tail     *tailBuffer   // last lines of sing-box output — for a meaningful error
	failed   chan error    // unexpected sing-box exit after startup (see Failed)
}

// New creates the bridge module. counters — the daemon's shared counters (may be nil).
func New(cfg config.Config, out io.Writer, counters *stats.Counters) *Module {
	return &Module{cfg: cfg, out: out, counters: counters}
}

// Name — the module name for runner logs.
func (m *Module) Name() string { return "bridge" }

// Failed delivers an error if sing-box exited on its own after a successful start.
func (m *Module) Failed() <-chan error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.failed
}

// Binary — the sing-box path from the configuration (or from PATH).
func Binary(c config.Config) (string, error) {
	bin := c.Bridge.SingboxPath
	if bin == "" {
		bin = "sing-box"
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return "", fmt.Errorf("sing-box not found (%q). The Docker image already has it; for a manual installation "+
			"install sing-box 1.12+ (https://sing-box.sagernet.org) or set bridge.singbox_path", bin)
	}
	return resolved, nil
}

// Check runs the generated configuration through `sing-box check` — an exact reason instead of a
// crash at startup.
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
		return fmt.Errorf("sing-box rejected the configuration: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// Start checks the configuration, starts sing-box and waits until the port accepts connections.
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
		return fmt.Errorf("port %d is already in use by another program (often a web server). "+
			"Free it or choose another port in `ringly-relay setup`", port)
	}

	conf, err := singbox.BridgeConfig(m.cfg)
	if err != nil {
		return err
	}
	// Statistics (traffic, number of clients) are optional: if sing-box doesn't accept them (a build
	// without the Clash API), the bridge still works, just without the numbers.
	api, withStats := m.statsConfig(resolved)
	if withStats != nil {
		conf = withStats
	}
	confPath, err := writeTempConfig(conf)
	if err != nil {
		return err
	}

	tail := &tailBuffer{max: 20}
	// ctx is NOT passed to CommandContext: Stop controls shutdown (graceful SIGTERM); otherwise
	// cancelling ctx would kill the process hard, skipping a clean exit.
	cmd := exec.Command(resolved, "run", "-c", confPath)
	cmd.Stdout = io.MultiWriter(prefixWriter{w: m.out, prefix: "[sing-box] "}, tail)
	cmd.Stderr = io.MultiWriter(prefixWriter{w: m.out, prefix: "[sing-box] "}, tail)

	if err := cmd.Start(); err != nil {
		os.RemoveAll(filepath.Dir(confPath))
		return fmt.Errorf("starting sing-box: %w", err)
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

// statsConfig — the configuration with the statistics API; nil when nobody reads it (no counters) or
// sing-box doesn't accept it, and the bridge then starts with the plain configuration.
func (m *Module) statsConfig(bin string) (*singbox.StatsAPI, []byte) {
	if m.counters == nil {
		return nil, nil
	}
	api, err := newStatsAPI()
	if err != nil {
		fmt.Fprintf(m.out, "[bridge] bridge statistics disabled: %v\n", err)
		return nil, nil
	}
	conf, err := singbox.BridgeConfigWithStats(m.cfg, api)
	if err == nil {
		err = checkConf(bin, conf)
	}
	if err != nil {
		fmt.Fprintf(m.out, "[bridge] bridge statistics disabled (sing-box rejected the statistics API): %v\n", err)
		return nil, nil
	}
	return api, conf
}

// waitReady waits until the port accepts connections or the process dies.
func waitReady(port int, done <-chan struct{}) error {
	deadline := time.Now().Add(readyTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			return fmt.Errorf("sing-box exited during startup")
		default:
		}
		if netcheck.Listening(port) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("sing-box did not open port %d within %s", port, readyTimeout)
}

// explain adds the sing-box output and a hint for the most common cause to the error.
func explain(err error, output string, port int) error {
	msg := err.Error()
	if output != "" {
		msg += ":\n" + output
	}
	if strings.Contains(output, "permission denied") && port < 1024 {
		msg += fmt.Sprintf("\n\nNo permission to listen on port %d. The Docker image already handles this — rebuild the image. "+
			"For a manual installation: sudo setcap cap_net_bind_service=+ep $(command -v sing-box)", port)
	}
	if strings.Contains(output, "address already in use") {
		msg += fmt.Sprintf("\n\nPort %d is in use by another program. Free it or choose another port in `ringly-relay setup`.", port)
	}
	return fmt.Errorf("%s", msg)
}

// watch waits for the process to exit, closes done and reports an unexpected exit.
func (m *Module) watch(cmd *exec.Cmd, done chan struct{}, failed chan<- error) {
	err := cmd.Wait()
	close(done)

	m.mu.Lock()
	stopped := m.stopped
	tail := m.tail
	m.mu.Unlock()

	if stopped {
		return // a normal stop via Stop
	}
	reason := "on its own"
	if err != nil {
		reason = err.Error()
	}
	msg := fmt.Sprintf("sing-box exited unexpectedly (%s)", reason)
	if out := tail.String(); out != "" {
		msg += ". Last output:\n" + out
	}
	failed <- fmt.Errorf("%s", msg)
}

// Stop shuts the process down: SIGTERM, a short wait, kill if needed; removes the temporary configuration.
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

	_ = cmd.Process.Signal(os.Interrupt) // let sing-box shut down cleanly

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
		_ = cmd.Process.Kill() // didn't exit in time — force it
		<-done                 // wait for watch to record the exit
		return fmt.Errorf("sing-box did not exit within %s, sent kill", deadline)
	}
}

func writeTempConfig(conf []byte) (string, error) {
	dir, err := os.MkdirTemp("", "ringly-relay-singbox")
	if err != nil {
		return "", fmt.Errorf("temporary directory for the sing-box configuration: %w", err)
	}
	path := filepath.Join(dir, "singbox.json")
	if err := os.WriteFile(path, conf, 0o600); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("writing the sing-box configuration: %w", err)
	}
	return path, nil
}

// tailBuffer keeps the last output lines — so a startup error explains itself.
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

// prefixWriter prefixes the child process output.
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
