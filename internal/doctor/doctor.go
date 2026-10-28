// Package doctor — `ringly-relay doctor`: checks the whole installation step by step, explaining what
// to do when a step fails.
//
// Why: on a real installation the bridge "worked" (sing-box listened on the port), but it was unclear
// whether a client would get through: is sing-box there, does it accept the configuration, are the
// cover domain and the Ringly server reachable from this VPS. Each of these took manual digging. The
// last step is a real test connection through the bridge, the way the app makes it.
//
// Client addresses are never shown — only the number of active connections.
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

// Run prints the report and returns the number of failed checks.
func Run(c config.Config, out io.Writer) int {
	r := &report{out: out}
	fmt.Fprintln(out, "Ringly Relay check")

	if err := c.Validate(); err != nil {
		r.fail("configuration: %v\n    → run ringly-relay setup: it fills in the current values, fixes and checks them", err)
		return r.failures // nothing more to check
	}
	r.ok("configuration is valid")

	if c.Bridge.Enabled {
		checkBridge(c, r)
	}
	if c.Calls.Enabled {
		checkCalls(c, r)
	}
	checkPublish(c, r)

	fmt.Fprintln(out)
	if r.failures == 0 {
		fmt.Fprintln(out, "All good.")
	} else {
		fmt.Fprintf(out, "Problems: %d — see the ✗ items.\n", r.failures)
	}
	return r.failures
}

func checkBridge(c config.Config, r *report) {
	// without sing-box check the rest: the domain and the Ringly server don't depend on it
	singboxOK := false
	if bin, err := bridge.Binary(c); err != nil {
		r.fail("%v", err)
	} else {
		r.ok("sing-box: %s", version(bin))
		if err := bridge.Check(c); err != nil {
			r.fail("%v", err)
		} else {
			r.ok("sing-box accepts the bridge configuration")
			singboxOK = true
		}
	}

	port, _ := config.PortFromListen(c.Bridge.Listen)
	listening := netcheck.Listening(port)
	if listening {
		if n, ok := netcheck.EstablishedOn(port); ok {
			r.ok("the bridge accepts connections on port %d (connections now: %d)", port, n)
		} else {
			r.ok("the bridge accepts connections on port %d", port)
		}
	} else {
		r.fail("nothing listens on port %d — is the daemon running? Docker: docker compose up -d; systemd: systemctl start ringly-relay", port)
	}

	if d, err := netcheck.RealityTarget(c.Reality.SNI, 5*time.Second); err != nil {
		r.fail("cover domain: %v", err)
	} else {
		r.ok("cover domain %s is reachable over TLS 1.3 (%d ms)", c.Reality.SNI, d.Milliseconds())
	}

	originOK := false
	if d, err := netcheck.Origin(context.Background(), c.Origin.URL, 10*time.Second); err != nil {
		r.fail("%v — without this the bridge can't get clients to Ringly", err)
	} else {
		r.ok("Ringly server reachable: %s (%d ms)", c.Origin.URL, d.Milliseconds())
		originOK = true
	}

	// The checks above can all pass while no client gets through (that is what happened with
	// www.microsoft.com as the cover domain), so finish with a real connection through the bridge.
	// It only makes sense when everything it depends on is in place — otherwise the cause is above.
	if singboxOK && listening && originOK {
		if d, err := bridge.SelfTest(context.Background(), c, 20*time.Second); err != nil {
			r.fail("test connection through the bridge: %v\n"+
				"    → the cover domain %s most likely doesn't work with Reality: choose another one in ringly-relay setup "+
				"(e.g. %s) and restart the daemon", err, c.Reality.SNI, config.DefaultSNI)
		} else {
			r.ok("test connection through the bridge reached the Ringly server (%d ms)", d.Milliseconds())
		}
	}

	r.warn("port %d can't be checked from the outside here: if your provider has its own firewall (security group), open %d/tcp there", port, port)
}

func checkCalls(c config.Config, r *report) {
	ip, err := netcheck.PublicIP()
	switch {
	case err != nil:
		r.warn("the public address could not be detected (%v) — make sure calls.public_ip is right: %s", err, c.Calls.PublicIP)
	case ip != c.Calls.PublicIP:
		r.fail("calls.public_ip = %s, but the server goes online from %s — calls through this TURN won't connect. Run ringly-relay setup", c.Calls.PublicIP, ip)
	default:
		r.ok("public address for calls: %s", ip)
	}
}

func checkPublish(c config.Config, r *report) {
	switch c.Publish.Mode {
	case config.ModeAuto:
		if _, err := netcheck.Origin(context.Background(), c.Origin.Control, 10*time.Second); err != nil {
			r.fail("registration server: %v", err)
		} else {
			r.ok("registration server reachable — the bridge is handed out to users automatically")
		}
	case config.ModePrivate:
		r.ok("private mode — the link for users is printed by: ringly-relay link")
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
