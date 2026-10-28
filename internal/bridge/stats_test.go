package bridge

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ringlymessenger/ringly-relay/internal/singbox"
	"github.com/ringlymessenger/ringly-relay/internal/stats"
)

// A fake Clash API: returns the given totals and connections, checks the secret.
func fakeAPI(t *testing.T, secret string, total *atomic.Int64, ips []string) *singbox.StatsAPI {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret || r.URL.Path != "/connections" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var conns []string
		for _, ip := range ips {
			conns = append(conns, fmt.Sprintf(`{"metadata":{"sourceIP":%q}}`, ip))
		}
		fmt.Fprintf(w, `{"downloadTotal":%d,"uploadTotal":0,"connections":[%s]}`, total.Load(), strings.Join(conns, ","))
	}))
	t.Cleanup(srv.Close)
	return &singbox.StatsAPI{Listen: strings.TrimPrefix(srv.URL, "http://"), Secret: secret}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPollStatsCountsDeltaAndDistinctClients(t *testing.T) {
	old := statsInterval
	statsInterval = 10 * time.Millisecond
	defer func() { statsInterval = old }()

	var total atomic.Int64
	total.Store(1000)
	// three connections from two clients — two clients
	api := fakeAPI(t, "s3cret", &total, []string{"10.0.0.1", "10.0.0.1", "10.0.0.2"})
	c := stats.New()
	done := make(chan struct{})
	go pollStats(api, c, done, func(string) {})

	waitFor(t, func() bool { return c.Snapshot().BridgeBytes == 1000 })
	if got := c.Snapshot().BridgeClients; got != 2 {
		t.Fatalf("clients: expected 2, got %d", got)
	}

	// the report went out; then sing-box restarted — its count started from zero
	c.Commit(c.Snapshot())
	total.Store(300)
	waitFor(t, func() bool { return c.Snapshot().BridgeBytes == 300 })

	close(done)
	waitFor(t, func() bool { return c.Snapshot().BridgeClients == 0 })
}

func TestPollStatsWrongSecretCountsNothing(t *testing.T) {
	old := statsInterval
	statsInterval = 10 * time.Millisecond
	defer func() { statsInterval = old }()

	var total atomic.Int64
	total.Store(500)
	api := fakeAPI(t, "right", &total, nil)
	api.Secret = "wrong"
	c := stats.New()
	done := make(chan struct{})
	var logged atomic.Int32
	go pollStats(api, c, done, func(string) { logged.Add(1) })

	time.Sleep(80 * time.Millisecond)
	close(done)
	if c.Snapshot().BridgeBytes != 0 || c.Snapshot().BridgeKnown {
		t.Fatal("nothing must be counted without API access")
	}
	if logged.Load() != 1 {
		t.Fatalf("a series of failures must be logged as one line, not %d", logged.Load())
	}
}
