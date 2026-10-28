package setup

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ringlymessenger/ringly-relay/internal/config"
)

// A case from a real installation: the domain pasted with Markdown escaping, the port as a bare
// number. The wizard must clean everything up and produce a working configuration without hand edits.
func TestRunCleansPastedInput(t *testing.T) {
	checkRealityTarget = func(string) error { return nil }
	detectPublicIP = func() (string, error) { return "203.0.113.7", nil }

	answers := strings.Join([]string{
		"",              // bridge: yes (default)
		"443",           // port
		`www\.ozon\.ru`, // domain with escaping
		"",              // calls: no
		"private",       // mode
		"",              // public address: the detected one
	}, "\n") + "\n"

	c, err := Run(strings.NewReader(answers), io.Discard, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if c.Bridge.Listen != ":443" {
		t.Errorf("listen = %q, want :443", c.Bridge.Listen)
	}
	if c.Reality.SNI != "www.ozon.ru" {
		t.Errorf("sni = %q, want www.ozon.ru", c.Reality.SNI)
	}
	if c.Publish.PublicHost != "203.0.113.7" {
		t.Errorf("public_host = %q, want 203.0.113.7", c.Publish.PublicHost)
	}
	if c.Reality.PrivateKey == "" || c.Reality.UUID == "" {
		t.Error("the Reality keys were not generated")
	}
}

// A domain unreachable from the server — a warning and the question again, not a silently broken bridge.
func TestRunRejectsUnreachableDomain(t *testing.T) {
	checkRealityTarget = func(host string) error {
		if host == "bad.example.com" {
			return errors.New("unreachable")
		}
		return nil
	}
	detectPublicIP = func() (string, error) { return "203.0.113.7", nil }

	answers := strings.Join([]string{
		"", "", // bridge yes, default port
		"bad.example.com", "n", // unreachable domain → don't keep
		"www.apple.com", // second answer
		"", "private", "",
	}, "\n") + "\n"

	c, err := Run(strings.NewReader(answers), io.Discard, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if c.Reality.SNI != "www.apple.com" {
		t.Errorf("sni = %q, expected the second answer", c.Reality.SNI)
	}
}

// Auto mode doesn't ask for the registration secret: it's built into the daemon, a volunteer can't know it.
func TestRunAutoModeNeedsNoSecret(t *testing.T) {
	checkRealityTarget = func(string) error { return nil }
	detectPublicIP = func() (string, error) { return "203.0.113.7", nil }

	answers := strings.Join([]string{
		"", "", "", // bridge yes, default port and domain
		"",     // calls: no
		"auto", // mode
		"",     // public address: the detected one
	}, "\n") + "\n"

	c, err := Run(strings.NewReader(answers), io.Discard, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if c.Publish.EnrollSecret != config.DefaultEnrollSecret {
		t.Errorf("enroll_secret = %q, expected the built-in one", c.Publish.EnrollSecret)
	}
	// the address must be in the configuration: behind Nginx the server can't learn it itself
	if c.Publish.PublicHost != "203.0.113.7" {
		t.Errorf("public_host = %q, want 203.0.113.7", c.Publish.PublicHost)
	}
}
