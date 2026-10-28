package publisher

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ringlymessenger/ringly-relay/internal/config"
)

func autoConfig(control string) config.Config {
	c := config.Default()
	c.Origin.Control = control
	c.Publish.Mode = config.ModeAuto
	c.Publish.EnrollSecret = "enroll-123"
	c.Publish.PublicHost = "203.0.113.7"
	c.Bridge.Enabled = true
	c.Bridge.Listen = ":443"
	c.Reality.SNI = "www.apple.com"
	c.Reality.PrivateKey = "26d5WDpy5OtXi5Z3Dos1me-Z5Zk0ScUxtsitvV4As64"
	c.Reality.ShortID = "08b992eec8cf3b84"
	c.Reality.UUID = "11111111-2222-4333-8444-555555555555"
	c.Calls.Enabled = false
	return c
}

// fakeControl emulates the control server with the { success, data } envelope.
type fakeControl struct {
	mu            sync.Mutex
	registerBody  map[string]any
	enrollHeader  string
	tokenHeaders  []string // X-Relay-Token of every registration ("" — without a token)
	heartbeats    int
	gotDelete     bool
	rejectTokenHB bool // the heartbeat answers 401 — "the server didn't recognize the token"
	issued        int
}

func (f *fakeControl) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/relays/register", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		f.mu.Lock()
		f.registerBody = parsed
		f.enrollHeader = r.Header.Get("X-Relay-Enroll")
		tok := r.Header.Get("X-Relay-Token")
		f.tokenHeaders = append(f.tokenHeaders, tok)
		if tok == "" { // a new record — a new token; with its own token — the same one
			f.issued++
			tok = "tok-" + string(rune('0'+f.issued))
		}
		f.rejectTokenHB = false
		f.mu.Unlock()
		writeEnvelope(w, map[string]any{"relayId": "r1", "relayToken": tok})
	})
	mux.HandleFunc("/relays/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.heartbeats++
		reject := f.rejectTokenHB
		f.mu.Unlock()
		if reject {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"success":false,"error":"Invalid relay token"}`))
			return
		}
		writeEnvelope(w, map[string]any{"disabled": false})
	})
	mux.HandleFunc("/relays", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			f.mu.Lock()
			f.gotDelete = true
			f.mu.Unlock()
		}
		writeEnvelope(w, map[string]any{"ok": true})
	})
	return mux
}

func writeEnvelope(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data})
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPublisherRegisterPayload(t *testing.T) {
	fc := &fakeControl{}
	srv := httptest.NewServer(fc.handler())
	defer srv.Close()

	m := New(autoConfig(srv.URL), io.Discard, nil)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop(context.Background())
	waitFor(t, "registration", func() bool { return m.currentToken() != "" })

	fc.mu.Lock()
	body := fc.registerBody
	enroll := fc.enrollHeader
	fc.mu.Unlock()

	if enroll != "enroll-123" {
		t.Fatalf("enrollment header = %q, expected enroll-123", enroll)
	}
	if body["offersBridge"] != true {
		t.Errorf("offersBridge = %v, expected true", body["offersBridge"])
	}
	if body["host"] != "203.0.113.7" {
		t.Errorf("host = %v", body["host"])
	}
	br, ok := body["bridge"].(map[string]any)
	if !ok {
		t.Fatalf("no bridge section in the registration body: %v", body)
	}
	if br["sni"] != "www.apple.com" || br["port"].(float64) != 443 {
		t.Errorf("bridge = %v", br)
	}
	// publicKey must be derived from the private key, not empty
	if pk, _ := br["realityPublicKey"].(string); pk == "" {
		t.Error("realityPublicKey is empty")
	}
}

// A restart must not be a new registration: the token is on disk, the second registration comes WITH
// it (the server updates the record), and stopping the daemon doesn't unpublish it.
func TestPublisherKeepsTokenAcrossRestarts(t *testing.T) {
	fc := &fakeControl{}
	srv := httptest.NewServer(fc.handler())
	defer srv.Close()

	cfg := autoConfig(srv.URL)
	cfg.StatePath = filepath.Join(t.TempDir(), "state.json")

	first := New(cfg, io.Discard, nil)
	_ = first.Start(context.Background())
	waitFor(t, "first registration", func() bool { return first.currentToken() != "" })
	_ = first.Stop(context.Background())

	fc.mu.Lock()
	if fc.gotDelete {
		t.Error("stopping unpublished the daemon — after a restart that would be a new registration")
	}
	fc.mu.Unlock()
	if info, err := os.Stat(cfg.StatePath); err != nil {
		t.Fatalf("state was not saved: %v", err)
	} else if info.Mode().Perm() != 0o600 {
		t.Errorf("state permissions %v, expected 0600 (it holds the token)", info.Mode().Perm())
	}

	second := New(cfg, io.Discard, nil)
	_ = second.Start(context.Background())
	defer second.Stop(context.Background())
	waitFor(t, "second registration", func() bool {
		fc.mu.Lock()
		defer fc.mu.Unlock()
		return len(fc.tokenHeaders) == 2
	})
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if fc.tokenHeaders[0] != "" || fc.tokenHeaders[1] != "tok-1" {
		t.Fatalf("registration tokens = %q, expected [\"\" \"tok-1\"] — a restart must come with its own token", fc.tokenHeaders)
	}
	if fc.issued != 1 {
		t.Errorf("tokens issued %d, expected 1 — the restart created a new record", fc.issued)
	}
}

// The server stopped recognizing the token (the record was deleted) — the daemon registers again by itself instead of staying out of distribution.
func TestPublisherReRegistersWhenTokenRejected(t *testing.T) {
	heartbeatInterval = 20 * time.Millisecond
	defer func() { heartbeatInterval = 60 * time.Second }()

	fc := &fakeControl{}
	srv := httptest.NewServer(fc.handler())
	defer srv.Close()

	m := New(autoConfig(srv.URL), io.Discard, nil)
	_ = m.Start(context.Background())
	defer m.Stop(context.Background())
	waitFor(t, "registration", func() bool { return m.currentToken() == "tok-1" })

	fc.mu.Lock()
	fc.rejectTokenHB = true
	fc.mu.Unlock()

	waitFor(t, "registration again", func() bool { return m.currentToken() == "tok-2" })
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if last := fc.tokenHeaders[len(fc.tokenHeaders)-1]; last != "" {
		t.Errorf("the new registration came with the rejected token %q", last)
	}
}
