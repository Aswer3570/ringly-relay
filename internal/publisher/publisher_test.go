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

// fakeControl эмулирует control-сервер с конвертом { success, data }.
type fakeControl struct {
	mu            sync.Mutex
	registerBody  map[string]any
	enrollHeader  string
	tokenHeaders  []string // X-Relay-Token каждой регистрации ("" — без токена)
	heartbeats    int
	gotDelete     bool
	rejectTokenHB bool // heartbeat отвечает 401 — «сервер не узнал токен»
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
		if tok == "" { // новая запись — новый токен; со своим токеном — тот же
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
			t.Fatalf("не дождались: %s", what)
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
	waitFor(t, "регистрация", func() bool { return m.currentToken() != "" })

	fc.mu.Lock()
	body := fc.registerBody
	enroll := fc.enrollHeader
	fc.mu.Unlock()

	if enroll != "enroll-123" {
		t.Fatalf("enrollment-заголовок = %q, ожидалось enroll-123", enroll)
	}
	if body["offersBridge"] != true {
		t.Errorf("offersBridge = %v, ожидалось true", body["offersBridge"])
	}
	if body["host"] != "203.0.113.7" {
		t.Errorf("host = %v", body["host"])
	}
	br, ok := body["bridge"].(map[string]any)
	if !ok {
		t.Fatalf("нет секции bridge в теле регистрации: %v", body)
	}
	if br["sni"] != "www.apple.com" || br["port"].(float64) != 443 {
		t.Errorf("bridge = %v", br)
	}
	// publicKey должен быть выведен из приватного ключа, а не пустой
	if pk, _ := br["realityPublicKey"].(string); pk == "" {
		t.Error("realityPublicKey пуст")
	}
}

// Перезапуск не должен быть новой регистрацией: токен лежит на диске, вторая регистрация идёт С НИМ
// (сервер обновит запись), а остановка демона не снимает его с публикации.
func TestPublisherKeepsTokenAcrossRestarts(t *testing.T) {
	fc := &fakeControl{}
	srv := httptest.NewServer(fc.handler())
	defer srv.Close()

	cfg := autoConfig(srv.URL)
	cfg.StatePath = filepath.Join(t.TempDir(), "state.json")

	first := New(cfg, io.Discard, nil)
	_ = first.Start(context.Background())
	waitFor(t, "первая регистрация", func() bool { return first.currentToken() != "" })
	_ = first.Stop(context.Background())

	fc.mu.Lock()
	if fc.gotDelete {
		t.Error("остановка сняла демон с публикации — после перезапуска это была бы новая регистрация")
	}
	fc.mu.Unlock()
	if info, err := os.Stat(cfg.StatePath); err != nil {
		t.Fatalf("состояние не сохранено: %v", err)
	} else if info.Mode().Perm() != 0o600 {
		t.Errorf("права на состояние %v, ожидалось 0600 (там токен)", info.Mode().Perm())
	}

	second := New(cfg, io.Discard, nil)
	_ = second.Start(context.Background())
	defer second.Stop(context.Background())
	waitFor(t, "повторная регистрация", func() bool {
		fc.mu.Lock()
		defer fc.mu.Unlock()
		return len(fc.tokenHeaders) == 2
	})
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if fc.tokenHeaders[0] != "" || fc.tokenHeaders[1] != "tok-1" {
		t.Fatalf("токены регистраций = %q, ожидалось [\"\" \"tok-1\"] — перезапуск должен прийти со своим токеном", fc.tokenHeaders)
	}
	if fc.issued != 1 {
		t.Errorf("выдано токенов %d, ожидался 1 — перезапуск завёл новую запись", fc.issued)
	}
}

// Сервер перестал узнавать токен (запись удалили) — демон регистрируется заново сам, а не висит вне выдачи.
func TestPublisherReRegistersWhenTokenRejected(t *testing.T) {
	heartbeatInterval = 20 * time.Millisecond
	defer func() { heartbeatInterval = 60 * time.Second }()

	fc := &fakeControl{}
	srv := httptest.NewServer(fc.handler())
	defer srv.Close()

	m := New(autoConfig(srv.URL), io.Discard, nil)
	_ = m.Start(context.Background())
	defer m.Stop(context.Background())
	waitFor(t, "регистрация", func() bool { return m.currentToken() == "tok-1" })

	fc.mu.Lock()
	fc.rejectTokenHB = true
	fc.mu.Unlock()

	waitFor(t, "регистрация заново", func() bool { return m.currentToken() == "tok-2" })
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if last := fc.tokenHeaders[len(fc.tokenHeaders)-1]; last != "" {
		t.Errorf("повторная регистрация пришла с отвергнутым токеном %q", last)
	}
}
