// Package setup — the interactive first-time setup wizard.
//
// It asks one toggle at a time, explaining the consequences, generates the Reality keys and writes
// the configuration. The call relay is off by default on purpose: it carries all voice and video,
// while the message bridge uses almost nothing — a volunteer should enable calls deliberately.
//
// Principle: nobody should have to edit the configuration by hand. So the wizard doesn't ask what a
// newcomer has no answer to (the origin/control addresses have correct defaults), fills in the public
// IP itself, cleans up copy-paste leftovers (Markdown escaping and markup) and validates every field
// right away — asking again instead of failing at the end or crashing sing-box at startup.
package setup

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/ringlymessenger/ringly-relay/internal/bridgelink"
	"github.com/ringlymessenger/ringly-relay/internal/config"
	"github.com/ringlymessenger/ringly-relay/internal/netcheck"
)

// Checking the Reality domain from the server. A variable so the wizard's tests don't use the network.
var checkRealityTarget = func(host string) error {
	_, err := netcheck.RealityTarget(host, 5*time.Second)
	return err
}

// Public address detection. A variable for the same reason.
var detectPublicIP = netcheck.PublicIP

// Run walks through the wizard, reading answers from in and printing to out, and returns the
// finished configuration.
func Run(in io.Reader, out io.Writer, existing *config.Config) (config.Config, error) {
	r := bufio.NewReader(in)
	p := &prompt{r: r, out: out}

	c := config.Default()
	if existing != nil {
		c = *existing // running again edits the existing configuration
	}

	fmt.Fprintln(out, "Ringly Relay setup")
	fmt.Fprintln(out, "A volunteer relay helps people get around blocking.")
	fmt.Fprintln(out, "Traffic through you is encrypted — it can't be read.")
	fmt.Fprintln(out, strings.Repeat("─", 60))

	// The Ringly server addresses are not asked: they have correct defaults and a newcomer doesn't
	// know them. If copy-paste broke them in an old configuration, fix them silently.
	c.Origin.URL = config.NormalizeURL(c.Origin.URL)
	c.Origin.Control = config.NormalizeURL(firstNonEmpty(c.Origin.Control, c.Origin.URL))

	publicIP, ipErr := detectPublicIP()

	// --- Message bridge ---
	fmt.Fprintln(out, "\n[1/3] Message bridge — forwards the app's encrypted traffic.")
	fmt.Fprintln(out, "      Uses almost no traffic. Recommended.")
	c.Bridge.Enabled = p.yesNo("Enable the message bridge?", c.Bridge.Enabled)
	if c.Bridge.Enabled {
		c.Bridge.Listen = p.valid("  Entry port (443 = looks like HTTPS)", c.Bridge.Listen, normalizeListen, validListen)
		fmt.Fprintln(out, "  Cover domain: the bridge pretends to be this website. A large site hosted")
		fmt.Fprintln(out, "  close to your server works best. It must open from this server.")
		for {
			c.Reality.SNI = p.valid("  Domain", config.NormalizeHost(c.Reality.SNI), config.NormalizeHost, config.ValidateHost)
			fmt.Fprintf(out, "  checking %s from this server…\n", c.Reality.SNI)
			if err := checkRealityTarget(c.Reality.SNI); err != nil {
				fmt.Fprintf(out, "  ⚠️ %v\n", err)
				if !p.yesNo("  Keep this domain anyway?", false) {
					continue
				}
			} else {
				fmt.Fprintln(out, "  ✓ the domain is suitable")
			}
			break
		}
	}

	// --- Call relay ---
	fmt.Fprintln(out, "\n[2/3] Call relay (TURN) — carries voice and video through your server.")
	fmt.Fprintln(out, "      ⚠️ This is REAL traffic (tens to hundreds of MB per call). Off by default.")
	c.Calls.Enabled = p.yesNo("Enable the call relay?", c.Calls.Enabled)
	if c.Calls.Enabled {
		c.Calls.TurnListen = p.text("  TURN port (UDP+TCP)", c.Calls.TurnListen)
		c.Calls.TurnTLSListen = p.text("  TURN over TLS port (fallback when UDP is throttled)", c.Calls.TurnTLSListen)
		c.Calls.TrafficCapGB = p.intVal("  Monthly traffic limit, GB (0 = unlimited)", c.Calls.TrafficCapGB)
		fmt.Fprintln(out, "  The public IP is required: without it the relay hands out an internal address.")
		if ipErr != nil {
			fmt.Fprintf(out, "  ⚠️ %v\n", ipErr)
		}
		c.Calls.PublicIP = p.valid("  Public IP of this server", firstNonEmpty(c.Calls.PublicIP, publicIP), strings.TrimSpace, validIP)
	}

	// --- Publishing mode ---
	fmt.Fprintln(out, "\n[3/3] Publishing mode:")
	fmt.Fprintln(out, "      auto    — the server knows about you and hands your bridge out to users")
	fmt.Fprintln(out, "      private — the server doesn't know about you; you share the link yourself (`ringly-relay link`)")
	c.Publish.Mode = p.choice("Publishing mode", []string{config.ModeAuto, config.ModePrivate}, c.Publish.Mode)
	// the registration secret is built into the daemon (config.DefaultEnrollSecret) — not asked
	if c.Publish.Mode == config.ModeAuto && c.Publish.EnrollSecret == "" {
		c.Publish.EnrollSecret = config.DefaultEnrollSecret
	}
	// The address is needed in both modes: in private it goes into the link, in auto into the
	// registration. "Let the server figure it out" doesn't work: behind Nginx the server sees the
	// proxy's address, not this VPS's.
	if ipErr != nil {
		fmt.Fprintf(out, "  ⚠️ %v\n", ipErr)
	}
	c.Publish.PublicHost = p.valid("  Public address of this server (IP or domain)",
		firstNonEmpty(c.Publish.PublicHost, publicIP), config.NormalizeHost, validHostOrIP)

	// --- Reality keys ---
	if c.Bridge.Enabled && c.Reality.PrivateKey == "" {
		keys, err := bridgelink.GenerateKeys()
		if err != nil {
			return config.Config{}, err
		}
		c.Reality.PrivateKey = keys.PrivateKey
		c.Reality.ShortID = keys.ShortID
		c.Reality.UUID = keys.UUID
		fmt.Fprintln(out, "\nGenerated the Reality keys and UUID.")
	}
	if c.Calls.Enabled && c.Calls.Secret == "" {
		secret, err := bridgelink.GenerateSecret()
		if err != nil {
			return config.Config{}, err
		}
		c.Calls.Secret = secret
		fmt.Fprintln(out, "Generated the TURN secret.")
	}

	if err := c.Validate(); err != nil {
		return config.Config{}, fmt.Errorf("the resulting configuration is invalid: %w", err)
	}
	return c, nil
}

// prompt — a small helper for reading answers with defaults.
type prompt struct {
	r   *bufio.Reader
	out io.Writer
}

func (p *prompt) line(label, def string) string {
	if def != "" {
		fmt.Fprintf(p.out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(p.out, "%s: ", label)
	}
	s, _ := p.r.ReadString('\n')
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	return s
}

func (p *prompt) text(label, def string) string { return p.line(label, def) }

// valid asks, normalizes the answer and asks again until the check passes.
func (p *prompt) valid(label, def string, normalize func(string) string, check func(string) error) string {
	for {
		s := normalize(p.line(label, def))
		if err := check(s); err != nil {
			fmt.Fprintf(p.out, "  %v\n", err)
			continue
		}
		return s
	}
}

func normalizeListen(s string) string {
	s = strings.TrimSpace(s)
	if _, err := strconv.Atoi(s); err == nil {
		return ":" + s // "443" → ":443"
	}
	return s
}

func validListen(s string) error {
	port, err := config.PortFromListen(s)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("a port number is required, e.g. 443")
	}
	return nil
}

func validIP(s string) error {
	if net.ParseIP(s) == nil {
		return fmt.Errorf("an IP address is required, e.g. 203.0.113.10")
	}
	return nil
}

func validHostOrIP(s string) error {
	if net.ParseIP(s) != nil {
		return nil
	}
	return config.ValidateHost(s)
}

func (p *prompt) yesNo(label string, def bool) bool {
	d := "n"
	if def {
		d = "y"
	}
	for {
		s := strings.ToLower(p.line(label+" (y/n)", d))
		switch s {
		case "y", "yes":
			return true
		case "n", "no":
			return false
		}
		fmt.Fprintln(p.out, "  answer y or n")
	}
}

func (p *prompt) intVal(label string, def int) int {
	for {
		s := p.line(label, strconv.Itoa(def))
		n, err := strconv.Atoi(s)
		if err == nil && n >= 0 {
			return n
		}
		fmt.Fprintln(p.out, "  enter a non-negative number")
	}
}

func (p *prompt) choice(label string, opts []string, def string) string {
	for {
		s := strings.ToLower(p.line(fmt.Sprintf("%s (%s)", label, strings.Join(opts, "/")), def))
		for _, o := range opts {
			if s == o {
				return o
			}
		}
		fmt.Fprintf(p.out, "  choose one of: %s\n", strings.Join(opts, ", "))
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
