package config

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// FORMAT checks for fields. "Not empty" guarantees nothing: on a real installation the configuration
// got an SNI with Markdown escaping (`www\.ozon\.ru`) and an address together with its markup
// (`[https://…](https://…)`), and sing-box failed only at startup with an unclear error. We catch it
// here and explain what is wrong.

var (
	uuidRe    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	shortIDRe = regexp.MustCompile(`^([0-9a-f]{2}){1,8}$`) // Reality: hex, even length, up to 16 characters
	labelRe   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// NormalizeHost fixes typical copy-paste leftovers: Markdown escaping (`www\.site\.com`), a scheme
// (`https://site.com/`), a path and spaces. Returns what ValidateHost should check.
func NormalizeHost(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, `\`, "")
	s = strings.Trim(s, "`'\"<>")
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(strings.TrimSuffix(s, "."))
}

// NormalizeURL extracts the address from a Markdown link `[text](https://…)` and drops quotes and a
// trailing "/".
func NormalizeURL(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "`'\"<>")
	if i := strings.Index(s, "]("); i >= 0 && strings.HasSuffix(s, ")") {
		s = s[i+2 : len(s)-1]
	}
	s = strings.Trim(s, "[]")
	return strings.TrimSuffix(s, "/")
}

// ValidateHost — a plain domain name (for the Reality SNI): no scheme, port, path or escaping.
func ValidateHost(s string) error {
	if s == "" {
		return fmt.Errorf("empty")
	}
	if strings.ContainsAny(s, `\/:[]() `) {
		return fmt.Errorf("%q — a single domain name is required, e.g. dl.google.com (no https://, slashes or backslashes)", s)
	}
	if net.ParseIP(s) != nil {
		return fmt.Errorf("%q — a domain is required, not an IP address: Reality disguises itself as a real website", s)
	}
	labels := strings.Split(strings.ToLower(s), ".")
	if len(labels) < 2 || len(s) > 253 {
		return fmt.Errorf("%q doesn't look like a domain name", s)
	}
	for _, l := range labels {
		if !labelRe.MatchString(l) {
			return fmt.Errorf("%q — invalid characters in the domain name", s)
		}
	}
	return nil
}

// ValidateURL — a server address: http(s), with a host, without markup.
func ValidateURL(s string) error {
	if strings.ContainsAny(s, "[]() ") {
		return fmt.Errorf("%q — looks like the address was copied with its markup; just https://… is needed", s)
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("%q — a full address like https://api.ringlyapp.net is required", s)
	}
	return nil
}

func validatePrivateKey(s string) error {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(raw) != 32 {
		return fmt.Errorf("reality.private_key is corrupted (an X25519 key is expected, 32 bytes in base64url) — remove the line and run `ringly-relay setup`, it will generate a new one")
	}
	return nil
}

func validateListen(field, s string) error {
	port, err := PortFromListen(s)
	if err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("%s: port %d is outside 1–65535", field, port)
	}
	return nil
}
