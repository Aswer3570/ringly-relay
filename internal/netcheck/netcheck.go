// Package netcheck — сетевые проверки, общие для setup, doctor и link: публичный адрес сервера,
// пригодность домена для Reality и доступность сервера Ringly.
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

// PublicIP — адрес, с которого сервер выходит в интернет, без обращения к сторонним сервисам:
// UDP-«соединение» ничего не отправляет, но ядро выбирает исходящий интерфейс. На обычном VPS
// публичный адрес висит прямо на интерфейсе. Если адрес внутренний (сервер за NAT), честно
// возвращаем ошибку — угадывать нельзя: ссылка с чужим адресом не заработает.
func PublicIP() (string, error) {
	conn, err := net.Dial("udp", "1.1.1.1:53")
	if err != nil {
		return "", fmt.Errorf("нет выхода в интернет: %w", err)
	}
	defer conn.Close()
	ip := conn.LocalAddr().(*net.UDPAddr).IP
	if ip.IsPrivate() || ip.IsLoopback() || isCGNAT(ip) {
		return "", fmt.Errorf("сервер за NAT (адрес интерфейса %s внутренний) — укажите публичный адрес вручную", ip)
	}
	return ip.String(), nil
}

func isCGNAT(ip net.IP) bool {
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	return cgnat.Contains(ip)
}

// RealityTarget проверяет, годится ли домен для маскировки: Reality проксирует незнакомцев на
// настоящий сайт, поэтому он обязан быть доступен С ЭТОГО сервера и уметь TLS 1.3. Иначе мостик
// поднимется, но клиенты не подключатся — и узнать причину будет неоткуда.
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
			return 0, fmt.Errorf("%s не поддерживает TLS 1.3 — для Reality нужен другой домен", host)
		}
		return 0, fmt.Errorf("%s недоступен с этого сервера по TLS 1.3: %w", host, err)
	}
	conn.Close()
	return time.Since(start), nil
}

// Origin проверяет, что сервер Ringly отвечает (GET /health). Любой HTTP-ответ означает, что сеть
// до него есть; не-2xx отдельно — сервер жив, но нездоров.
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
		return 0, fmt.Errorf("сервер Ringly недоступен с этого VPS: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return time.Since(start), fmt.Errorf("сервер Ringly ответил %d", resp.StatusCode)
	}
	return time.Since(start), nil
}

// Listening — принимает ли кто-то соединения на локальном порту.
//
// На Linux смотрим таблицу сокетов ядра, а не подключаемся: для Reality пустое подключение —
// «чужак», и sing-box писал бы в журнал ERROR «REALITY: processed invalid connection» при каждом
// старте и каждом doctor — пугающе для волонтёра, хотя всё в порядке.
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
