package config

import (
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	in := Default()
	in.Reality.PrivateKey = "priv"
	in.Reality.ShortID = "sid"
	in.Operator.Contact = "op@example.com"

	if err := Save(path, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out != in {
		t.Fatalf("round trip mismatch:\n  got  %+v\n  want %+v", out, in)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"empty — no modules and keys", func(c *Config) { *c = Config{} }, true},
		{"bridge without Reality keys", func(c *Config) {}, true}, // Default() has no keys
		{"valid bridge", func(c *Config) {
			withKeys(c)
			c.Publish.EnrollSecret = "e" // auto mode requires a registration secret
		}, false},
		{"unknown publishing mode", func(c *Config) {
			withKeys(c)
			c.Publish.Mode = "wat"
		}, true},
		{"private mode without control — fine", func(c *Config) {
			withKeys(c)
			c.Publish.Mode = ModePrivate
			c.Origin.Control = ""
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Default()
			tt.mutate(&c)
			err := c.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// withKeys — Reality keys in a real format (dummy "p"/"s"/"u" would not pass the format checks).
func withKeys(c *Config) {
	c.Reality.PrivateKey = "26d5WDpy5OtXi5Z3Dos1me-Z5Zk0ScUxtsitvV4As64"
	c.Reality.ShortID = "08b992eec8cf3b84"
	c.Reality.UUID = "11111111-2222-4333-8444-555555555555"
}

// Markdown copy-paste leftovers that broke a real installation are fixed by normalization and,
// without it, rejected by validation with a clear reason.
func TestFormatChecks(t *testing.T) {
	if got := NormalizeHost(`www\.ozon\.ru`); got != "www.ozon.ru" {
		t.Errorf("NormalizeHost(escaping) = %q", got)
	}
	if got := NormalizeHost("https://dl.google.com/"); got != "dl.google.com" {
		t.Errorf("NormalizeHost(scheme) = %q", got)
	}
	if got := NormalizeURL("[https://api.ringlyapp.net](https://api.ringlyapp.net)"); got != "https://api.ringlyapp.net" {
		t.Errorf("NormalizeURL(markdown) = %q", got)
	}

	bad := []func(*Config){
		func(c *Config) { c.Reality.SNI = `www\.ozon\.ru` },
		func(c *Config) { c.Reality.SNI = "1.2.3.4" },
		func(c *Config) { c.Origin.URL = "[https://api.ringlyapp.net](https://api.ringlyapp.net)" },
		func(c *Config) { c.Origin.URL = "api.ringlyapp.net" },
		func(c *Config) { c.Reality.PrivateKey = "priv" },
		func(c *Config) { c.Reality.ShortID = "xyz" },
		func(c *Config) { c.Reality.UUID = "not-a-uuid" },
		func(c *Config) { c.Bridge.Listen = ":99999" },
	}
	for i, mutate := range bad {
		c := Default()
		withKeys(&c)
		c.Publish.EnrollSecret = "e"
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("case %d: expected a format error", i)
		}
	}
}
