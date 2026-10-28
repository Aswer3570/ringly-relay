// Package doctor — `ringly-relay doctor`: проверка установки целиком, по пунктам, с объяснением,
// что делать, если пункт не прошёл.
//
// Зачем: на живой установке мостик «работал» (sing-box слушал порт), но было непонятно, дойдёт ли
// до него клиент: на месте ли sing-box, принимает ли он конфиг, доступен ли домен маскировки
// и сервер Ringly с этого VPS. Каждый такой вопрос стоил ручного расследования.
//
// Адреса клиентов не показываются — только число активных подключений.
package doctor

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/ringlymessenger/ringly-relay/internal/bridge"
	"github.com/ringlymessenger/ringly-relay/internal/config"
	"github.com/ringlymessenger/ringly-relay/internal/netcheck"
)

type report struct {
	out      io.Writer
	failures int
}

func (r *report) ok(format string, a ...any) {
	fmt.Fprintf(r.out, "  ✓ "+format+"\n", a...)
}

func (r *report) warn(format string, a ...any) {
	fmt.Fprintf(r.out, "  ⚠️ "+format+"\n", a...)
}

func (r *report) fail(format string, a ...any) {
	r.failures++
	fmt.Fprintf(r.out, "  ✗ "+format+"\n", a...)
}

// Run печатает отчёт и возвращает число непройденных проверок.
func Run(c config.Config, out io.Writer) int {
	r := &report{out: out}
	fmt.Fprintln(out, "Проверка Ringly Relay")

	if err := c.Validate(); err != nil {
		r.fail("конфиг: %v\n    → запустите ringly-relay setup: он подставит текущие значения, исправит их и проверит", err)
		return r.failures // дальше проверять нечего
	}
	r.ok("конфиг корректен")

	if c.Bridge.Enabled {
		checkBridge(c, r)
	}
	if c.Calls.Enabled {
		checkCalls(c, r)
	}
	checkPublish(c, r)

	fmt.Fprintln(out)
	if r.failures == 0 {
		fmt.Fprintln(out, "Всё в порядке.")
	} else {
		fmt.Fprintf(out, "Проблем: %d — см. пункты с ✗.\n", r.failures)
	}
	return r.failures
}

func checkBridge(c config.Config, r *report) {
	// без sing-box проверяем остальное: домен и сервер Ringly от него не зависят
	if bin, err := bridge.Binary(c); err != nil {
		r.fail("%v", err)
	} else {
		r.ok("sing-box: %s", version(bin))
		if err := bridge.Check(c); err != nil {
			r.fail("%v", err)
		} else {
			r.ok("sing-box принимает конфиг мостика")
		}
	}

	port, _ := config.PortFromListen(c.Bridge.Listen)
	if netcheck.Listening(port) {
		if n, ok := netcheck.EstablishedOn(port); ok {
			r.ok("мостик принимает соединения на порту %d (сейчас подключений: %d)", port, n)
		} else {
			r.ok("мостик принимает соединения на порту %d", port)
		}
	} else {
		r.fail("порт %d никто не слушает — демон запущен? Docker: docker compose up -d; systemd: systemctl start ringly-relay", port)
	}

	if d, err := netcheck.RealityTarget(c.Reality.SNI, 5*time.Second); err != nil {
		r.fail("домен маскировки: %v", err)
	} else {
		r.ok("домен маскировки %s доступен по TLS 1.3 (%d мс)", c.Reality.SNI, d.Milliseconds())
	}

	if d, err := netcheck.Origin(context.Background(), c.Origin.URL, 10*time.Second); err != nil {
		r.fail("%v — без этого мостик не доведёт клиентов до Ringly", err)
	} else {
		r.ok("сервер Ringly доступен: %s (%d мс)", c.Origin.URL, d.Milliseconds())
	}

	r.warn("снаружи порт %d отсюда не проверить: если у хостера есть свой фаервол (security group), откройте в нём %d/tcp", port, port)
}

func checkCalls(c config.Config, r *report) {
	ip, err := netcheck.PublicIP()
	switch {
	case err != nil:
		r.warn("публичный адрес не определить автоматически (%v) — убедитесь, что calls.public_ip верный: %s", err, c.Calls.PublicIP)
	case ip != c.Calls.PublicIP:
		r.fail("calls.public_ip = %s, а сервер выходит в интернет с %s — звонки через этот TURN не соединятся. Запустите ringly-relay setup", c.Calls.PublicIP, ip)
	default:
		r.ok("публичный адрес для звонков: %s", ip)
	}
}

func checkPublish(c config.Config, r *report) {
	switch c.Publish.Mode {
	case config.ModeAuto:
		if _, err := netcheck.Origin(context.Background(), c.Origin.Control, 10*time.Second); err != nil {
			r.fail("сервер регистрации: %v", err)
		} else {
			r.ok("сервер регистрации доступен — мостик раздаётся пользователям автоматически")
		}
	case config.ModePrivate:
		r.ok("приватный режим — ссылку для пользователей печатает: ringly-relay link")
	}
}

func version(bin string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").Output()
	if err != nil {
		return bin
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return first
}
