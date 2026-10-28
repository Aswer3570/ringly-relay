package bridge

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ringlymessenger/ringly-relay/internal/config"
	"github.com/ringlymessenger/ringly-relay/internal/singbox"
)

// SelfTest goes through the RUNNING bridge the way the app does: it starts a separate sing-box client
// (VLESS + Reality) next to the daemon and fetches the Ringly server's /health through it. It checks
// the whole path — Reality with the chosen cover domain, the destination rules, the way out to Ringly —
// but not the provider's firewall: the connection comes from this machine.
func SelfTest(ctx context.Context, c config.Config, timeout time.Duration) (time.Duration, error) {
	bin, err := Binary(c)
	if err != nil {
		return 0, err
	}
	proxyPort, err := freeLocalPort()
	if err != nil {
		return 0, err
	}
	conf, err := singbox.SelfTestConfig(c, proxyPort)
	if err != nil {
		return 0, err
	}
	path, err := writeTempConfig(conf)
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(filepath.Dir(path))

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "run", "-c", path)
	tail := &tailBuffer{max: 10}
	cmd.Stdout, cmd.Stderr = tail, tail
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("starting the sing-box test client: %w", err)
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	defer func() { cancel(); <-done }()

	if err := waitReady(proxyPort, done); err != nil {
		return 0, explain(fmt.Errorf("the sing-box test client: %w", err), tail.String(), proxyPort)
	}

	proxy, _ := url.Parse(singbox.ProxyURL(proxyPort))
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(c.Origin.URL, "/")+"/health", nil)
	if err != nil {
		return 0, err
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("no answer from the Ringly server through the bridge: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return 0, fmt.Errorf("the Ringly server answered %d through the bridge", resp.StatusCode)
	}
	return time.Since(start), nil
}

func freeLocalPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("finding a free local port: %w", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
