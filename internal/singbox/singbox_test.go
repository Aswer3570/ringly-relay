package singbox

import (
	"encoding/json"
	"testing"

	"github.com/ringlymessenger/ringly-relay/internal/config"
)

func testConfig() config.Config {
	c := config.Default()
	c.Bridge.Listen = ":8443"
	c.Reality.SNI = "dl.google.com"
	c.Reality.PrivateKey = "26d5WDpy5OtXi5Z3Dos1me-Z5Zk0ScUxtsitvV4As64"
	c.Reality.ShortID = "08b992eec8cf3b84"
	c.Reality.UUID = "11111111-2222-4333-8444-555555555555"
	return c
}

func TestBridgeConfigShape(t *testing.T) {
	raw, err := BridgeConfig(testConfig())
	if err != nil {
		t.Fatalf("BridgeConfig: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("generated invalid JSON: %v", err)
	}

	inbounds, ok := m["inbounds"].([]any)
	if !ok || len(inbounds) != 1 {
		t.Fatalf("expected exactly one inbound, got: %v", m["inbounds"])
	}
	in := inbounds[0].(map[string]any)

	if in["type"] != "vless" {
		t.Errorf("inbound type = %v, expected vless", in["type"])
	}
	if in["listen_port"].(float64) != 8443 {
		t.Errorf("listen_port = %v, expected 8443", in["listen_port"])
	}

	users := in["users"].([]any)
	u := users[0].(map[string]any)
	if u["uuid"] != "11111111-2222-4333-8444-555555555555" {
		t.Errorf("uuid = %v", u["uuid"])
	}
	if u["flow"] != "xtls-rprx-vision" {
		t.Errorf("flow = %v, expected xtls-rprx-vision", u["flow"])
	}

	tls := in["tls"].(map[string]any)
	if tls["enabled"] != true {
		t.Error("tls.enabled must be true")
	}
	if tls["server_name"] != "dl.google.com" {
		t.Errorf("server_name = %v", tls["server_name"])
	}
	reality := tls["reality"].(map[string]any)
	if reality["enabled"] != true {
		t.Error("reality.enabled must be true")
	}
	if reality["private_key"] != "26d5WDpy5OtXi5Z3Dos1me-Z5Zk0ScUxtsitvV4As64" {
		t.Errorf("private_key = %v", reality["private_key"])
	}
	sids := reality["short_id"].([]any)
	if len(sids) != 1 || sids[0] != "08b992eec8cf3b84" {
		t.Errorf("short_id = %v", sids)
	}
	hs := reality["handshake"].(map[string]any)
	if hs["server"] != "dl.google.com" || hs["server_port"].(float64) != 443 {
		t.Errorf("handshake = %v", hs)
	}
}

// The bridge must not be an open proxy: only Ringly hosts are routed, everything else from the
// bridge inbound is rejected, and the reject rule is the last one (sing-box applies rules in order).
func TestBridgeConfigRestrictsDestinations(t *testing.T) {
	raw, err := BridgeConfig(testConfig())
	if err != nil {
		t.Fatalf("BridgeConfig: %v", err)
	}
	var m struct {
		Route struct {
			Rules []map[string]any `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	rules := m.Route.Rules
	if len(rules) < 2 {
		t.Fatalf("expected routing rules, got %v", rules)
	}

	var allow map[string]any
	for _, r := range rules {
		if r["action"] == "route" {
			allow = r
		}
	}
	if allow == nil || allow["outbound"] != "direct" {
		t.Fatalf("no allow rule routing to direct: %v", rules)
	}
	suffixes, _ := allow["domain_suffix"].([]any)
	if len(suffixes) != 1 || suffixes[0] != "ringlyapp.net" {
		t.Errorf("allowed domains = %v, expected [ringlyapp.net]", suffixes)
	}

	last := rules[len(rules)-1]
	if last["action"] != "reject" {
		t.Errorf("last rule must reject, got %v", last)
	}
	if in, _ := last["inbound"].([]any); len(in) != 1 || in[0] != inboundTag {
		t.Errorf("reject rule must cover the bridge inbound, got %v", last["inbound"])
	}
}

func TestBridgeConfigDisabled(t *testing.T) {
	c := testConfig()
	c.Bridge.Enabled = false
	if _, err := BridgeConfig(c); err == nil {
		t.Fatal("expected an error with the bridge disabled")
	}
}

func TestSelfTestConfigTargetsOwnBridge(t *testing.T) {
	c := testConfig()
	conf, err := SelfTestConfig(c, 18080)
	if err != nil {
		t.Fatalf("SelfTestConfig: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(conf, &m); err != nil {
		t.Fatal(err)
	}
	in := m["inbounds"].([]any)[0].(map[string]any)
	if in["listen"] != "127.0.0.1" || in["listen_port"].(float64) != 18080 {
		t.Errorf("the local proxy must listen on 127.0.0.1 only: %v", in)
	}
	out := m["outbounds"].([]any)[0].(map[string]any)
	if out["server"] != "127.0.0.1" || out["uuid"] != c.Reality.UUID {
		t.Errorf("the client must target this daemon's own bridge: %v", out)
	}
	reality := out["tls"].(map[string]any)["reality"].(map[string]any)
	if reality["public_key"] == "" || reality["short_id"] != c.Reality.ShortID {
		t.Errorf("Reality parameters don't match the bridge: %v", reality)
	}
}

func TestLocalHost(t *testing.T) {
	for listen, want := range map[string]string{":443": "127.0.0.1", "0.0.0.0:443": "127.0.0.1", "[::]:443": "127.0.0.1", "10.0.0.5:443": "10.0.0.5"} {
		if got := localHost(listen); got != want {
			t.Errorf("localHost(%q) = %q, want %q", listen, got, want)
		}
	}
}
