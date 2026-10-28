// Package netcheck — network checks shared by setup, doctor and link: the server's public address,
// whether a domain suits Reality and whether the Ringly server is reachable.
package netcheck

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// PublicIP — the address the server goes online from, without asking third-party services: a UDP
// "connection" sends nothing, but the kernel picks the outgoing interface. On an ordinary VPS the public
// address sits right on the interface. If the address is internal (the server is behind NAT), return an
// honest error — guessing is not an option: a link with the wrong address won't work.
func PublicIP() (string, error) {
	conn, err := net.Dial("udp", "1.1.1.1:53")
	if err != nil {
		return "", fmt.Errorf("no internet access: %w", err)
	}
	defer conn.Close()
	ip := conn.LocalAddr().(*net.UDPAddr).IP
	if ip.IsPrivate() || ip.IsLoopback() || isCGNAT(ip) {
		return "", fmt.Errorf("the server is behind NAT (interface address %s is internal) — enter the public address manually", ip)
	}
	return ip.String(), nil
}

func isCGNAT(ip net.IP) bool {
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	return cgnat.Contains(ip)
}

// RealityTarget checks whether a domain suits the disguise: Reality proxies strangers to the real site,
// so it must be reachable FROM THIS server and support TLS 1.3. Otherwise the bridge starts but clients
// can't connect — with no way to find out why.
func RealityTarget(host string, timeout time.Duration) (time.Duration, error) {
	start := time.Now()
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", net.JoinHostPort(host, "443"), &tls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS13,
		MaxVersion: tls.VersionTLS13,
	})
	if err != nil {
		if strings.Contains(err.Error(), "protocol version") {
			return 0, fmt.Errorf("%s doesn't support TLS 1.3 — Reality needs another domain", host)
		}
		return 0, fmt.Errorf("%s is unreachable from this server over TLS 1.3: %w", host, err)
	}
	conn.Close()
	return time.Since(start), nil
}

// Origin checks that the Ringly server responds (GET /health). Any HTTP response means the network path
// exists; a non-2xx is reported separately — the server is alive but unhealthy.
func Origin(ctx context.Context, origin string, timeout time.Duration) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(origin, "/")+"/health", nil)
	if err != nil {
		return 0, err
	}
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("the Ringly server is unreachable from this VPS: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return time.Since(start), fmt.Errorf("the Ringly server answered %d", resp.StatusCode)
	}
	return time.Since(start), nil
}

// Listening — whether anything accepts connections on a local port.
//
// On Linux we read the kernel socket table instead of connecting: for Reality an empty connection is a
// "stranger", and sing-box would log ERROR "REALITY: processed invalid connection" on every start and
// every doctor run — scary for a volunteer, although everything is fine.
func Listening(port int) bool {
	if listening, ok := listeningInKernel(port); ok {
		return listening
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)), 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
