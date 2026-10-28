package bridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/ringlymessenger/ringly-relay/internal/singbox"
	"github.com/ringlymessenger/ringly-relay/internal/stats"
)

// Как часто спрашивать у sing-box трафик и подключения. Heartbeat уходит раз в минуту — 15 с с запасом.
var statsInterval = 15 * time.Second

// newStatsAPI — адрес и секрет API статистики для этого запуска sing-box: свободный порт только на
// 127.0.0.1 и случайный секрет (на диск не пишется, живёт до перезапуска).
func newStatsAPI() (*singbox.StatsAPI, error) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	addr := l.Addr().String()
	_ = l.Close()

	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	return &singbox.StatsAPI{Listen: addr, Secret: hex.EncodeToString(secret)}, nil
}

// snapshot — нужная нам часть ответа GET /connections (Clash API sing-box).
type snapshot struct {
	DownloadTotal int64 `json:"downloadTotal"`
	UploadTotal   int64 `json:"uploadTotal"`
	Connections   []struct {
		Metadata struct {
			SourceIP string `json:"sourceIP"`
		} `json:"metadata"`
	} `json:"connections"`
}

// pollStats до завершения процесса (done) переносит из sing-box в счётчики демона прирост трафика
// и число РАЗНЫХ подключённых клиентов. Адреса клиентов дальше этой функции не уходят — только счёт.
func pollStats(api *singbox.StatsAPI, counters *stats.Counters, done <-chan struct{}, log func(string)) {
	client := &http.Client{Timeout: 3 * time.Second}
	var lastTotal int64
	failing := false

	poll := func() {
		snap, err := fetchSnapshot(client, api)
		if err != nil {
			if !failing { // одна строка на серию сбоев, а не каждые 15 с
				log(fmt.Sprintf("статистика мостика недоступна: %v", err))
				failing = true
			}
			return
		}
		failing = false

		total := snap.DownloadTotal + snap.UploadTotal
		if total < lastTotal { // sing-box перезапускался — счёт начался заново
			lastTotal = 0
		}
		counters.AddBridgeBytes(total - lastTotal)
		lastTotal = total

		clients := map[string]struct{}{}
		for _, c := range snap.Connections {
			if ip := c.Metadata.SourceIP; ip != "" {
				clients[ip] = struct{}{}
			}
		}
		counters.SetBridgeClients(int64(len(clients)))
	}

	t := time.NewTicker(statsInterval)
	defer t.Stop()
	for {
		select {
		case <-done:
			counters.SetBridgeClients(0)
			return
		case <-t.C:
			poll()
		}
	}
}

func fetchSnapshot(client *http.Client, api *singbox.StatsAPI) (snapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+api.Listen+"/connections", nil)
	if err != nil {
		return snapshot{}, err
	}
	req.Header.Set("Authorization", "Bearer "+api.Secret)
	resp, err := client.Do(req)
	if err != nil {
		return snapshot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return snapshot{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var snap snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return snapshot{}, err
	}
	return snap, nil
}
