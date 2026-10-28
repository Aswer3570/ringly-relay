// Package publisher — the auto-publishing module: registers the daemon on the control server and
// sends heartbeats.
//
// Implements runner.Module. Only active in publish=auto mode. Registration retries in the background
// (control being briefly unavailable must not stop the bridge/TURN from serving clients they already
// have); a heartbeat once a minute.
//
// Registration happens ONCE per installation: the token is kept in the state file (cfg.StatePath) and
// survives restarts. The token used to live only in memory and the daemon unpublished itself on
// stop — every restart (an update, a VPS reboot, an automatic restart after a crash) was a new
// registration with the shared secret, so rotating the secret on the server gradually knocked out
// every bridge. Now only new installations need the secret. On startup the daemon registers WITH its
// token — the server updates its record (the address or keys may have changed) instead of creating a
// new one. If the server doesn't recognize the token (the record was deleted), it registers again
// rather than silently staying out of distribution.
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

// Variables rather than constants — so tests don't wait a minute.
var (
	heartbeatInterval = 60 * time.Second
	registerRetry     = 30 * time.Second
	httpTimeout       = 15 * time.Second
)

// state — what the daemon writes itself (not a person): who we are in the pool. Control — which server
// the token belongs to: change the server in the configuration and the old token means nothing there.
type state struct {
	RelayID    string `json:"relayId"`
	RelayToken string `json:"relayToken"`
	Control    string `json:"control"`
}

// httpError — a non-2xx server response; the status tells "token not recognized" apart from other failures.
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

// Module publishes the daemon on the control server.
type Module struct {
	cfg      config.Config
	out      io.Writer
	client   *http.Client
	counters *stats.Counters // traffic and connections — sent with every heartbeat (nil — without them)

	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu    sync.Mutex
	token string // the per-relay token received at registration
}

// New creates the publisher module. counters — the daemon's shared counters (may be nil).
func New(cfg config.Config, out io.Writer, counters *stats.Counters) *Module {
	return &Module{cfg: cfg, out: out, client: &http.Client{Timeout: httpTimeout}, counters: counters}
}

// Name — the module name for runner logs.
func (m *Module) Name() string { return "publisher" }

// Start launches background registration + heartbeat. Returns right away: control being unavailable
// must not bring the daemon down (the bridge/TURN keep serving the clients they have).
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

// Stop stops the background work. It does NOT unpublish: a stop is almost always a restart, and the
// server judges liveness by the heartbeat anyway (no signal for 3 minutes — the bridge isn't handed out).
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
		// Registration with retries. With our token it updates the record, without it — a new one.
		for {
			err := m.register(ctx)
			if err == nil {
				break
			}
			fmt.Fprintf(m.out, "[publisher] registration failed: %v — retrying in %s\n", err, registerRetry)
			if !sleepCtx(ctx, registerRetry) {
				return
			}
		}

		// heartbeat — as long as the server recognizes the token
		if !m.heartbeatLoop(ctx) {
			return
		}
		fmt.Fprintln(m.out, "[publisher] the server did not recognize the token — registering again")
		m.forgetToken()
	}
}

// heartbeatLoop sends a heartbeat every heartbeatInterval. false — the context was cancelled, true —
// the server stopped recognizing the token and a new registration is needed.
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
				fmt.Fprintf(m.out, "[publisher] heartbeat failed: %v\n", err)
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
		// the server doesn't know the token and rejected the secret — the saved token is useless, start clean
		m.forgetToken()
	}
	if err != nil {
		return err
	}
	if resp.RelayToken == "" {
		return fmt.Errorf("the server did not return relayToken")
	}
	m.mu.Lock()
	m.token = resp.RelayToken
	m.mu.Unlock()
	if err := m.saveState(state{RelayID: resp.RelayID, RelayToken: resp.RelayToken, Control: m.cfg.Origin.Control}); err != nil {
		// not fatal: we keep running, but after a restart we'll have to register again
		fmt.Fprintf(m.out, "[publisher] ⚠️ could not save state (%v) — after a restart the daemon will register again\n", err)
	}
	if token == resp.RelayToken {
		fmt.Fprintln(m.out, "[publisher] registration updated on the control server")
	} else {
		fmt.Fprintln(m.out, "[publisher] registered on the control server")
	}
	return nil
}

func (m *Module) heartbeat(ctx context.Context) error {
	var resp struct {
		Disabled bool `json:"disabled"`
		Blocked  bool `json:"blocked"`
	}
	// Bytes since the last successful report + how many are connected now. Numbers only: the server
	// never learns who is connected. A failed report — the increase goes with the next one (Commit only after 2xx).
	report := m.counters.Snapshot()
	body := map[string]any{
		"bridgeBytes": report.BridgeBytes,
		"callsBytes":  report.CallsBytes,
		"callsActive": report.CallsActive,
	}
	if report.BridgeKnown { // without sing-box statistics the client count is unknown — don't claim zero
		body["bridgeClients"] = report.BridgeClients
	}
	if err := m.post(ctx, "/relays/heartbeat", body, "", m.currentToken(), &resp); err != nil {
		return err
	}
	m.counters.Commit(report)
	if resp.Blocked {
		fmt.Fprintln(m.out, "⚠️ [publisher] the bridge is blocked by the Ringly administrators and is not handed out to users")
	} else if resp.Disabled {
		fmt.Fprintln(m.out, "⚠️ [publisher] the server took the daemon out of distribution (probably the traffic limit was exceeded)")
	}
	return nil
}

// registerPayload builds the registration body from the configuration (matches the backend's RegisterRelaySchema).
func (m *Module) registerPayload() map[string]any {
	body := map[string]any{
		"offersBridge": m.cfg.Bridge.Enabled,
		"offersCalls":  m.cfg.Calls.Enabled,
	}
	// The address is ALWAYS sent. Without it the server takes the request's IP, and Nginx sits in front
	// of the API — the server would see the proxy's address and hand out a bridge nobody can reach.
	host := m.cfg.Publish.PublicHost
	if host == "" {
		if ip, err := netcheck.PublicIP(); err == nil {
			host = ip
		} else {
			fmt.Fprintf(m.out, "[publisher] ⚠️ the public address could not be detected (%v) — set it in ringly-relay setup\n", err)
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

// saveState writes atomically (temporary file + rename) and owner-only: the token is the pass into the pool.
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

// post sends JSON and parses the backend's response envelope { success, data, error }, decoding data into out.
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
		return fmt.Errorf("parsing the response of %s: %w", path, err)
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

// sleepCtx sleeps for d or returns false if ctx is cancelled first.
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
