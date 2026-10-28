package singbox

import (
	"encoding/json"
	"testing"

	"github.com/ringlymessenger/ringly-relay/internal/config"
)

func testConfig() config.Config {
	c := config.Default()
	c.Bridge.Listen = ":8443"
	c.Reality.SNI = "www.microsoft.com"
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
		t.Fatalf("сгенерирован невалидный JSON: %v", err)
	}

	inbounds, ok := m["inbounds"].([]any)
	if !ok || len(inbounds) != 1 {
		t.Fatalf("ожидался ровно один inbound, получено: %v", m["inbounds"])
	}
	in := inbounds[0].(map[string]any)

	if in["type"] != "vless" {
		t.Errorf("type inbound = %v, ожидалось vless", in["type"])
	}
	if in["listen_port"].(float64) != 8443 {
		t.Errorf("listen_port = %v, ожидалось 8443", in["listen_port"])
	}

	users := in["users"].([]any)
	u := users[0].(map[string]any)
	if u["uuid"] != "11111111-2222-4333-8444-555555555555" {
		t.Errorf("uuid = %v", u["uuid"])
	}
	if u["flow"] != "xtls-rprx-vision" {
		t.Errorf("flow = %v, ожидалось xtls-rprx-vision", u["flow"])
	}

	tls := in["tls"].(map[string]any)
	if tls["enabled"] != true {
		t.Error("tls.enabled должно быть true")
	}
	if tls["server_name"] != "www.microsoft.com" {
		t.Errorf("server_name = %v", tls["server_name"])
	}
	reality := tls["reality"].(map[string]any)
	if reality["enabled"] != true {
		t.Error("reality.enabled должно быть true")
	}
	if reality["private_key"] != "26d5WDpy5OtXi5Z3Dos1me-Z5Zk0ScUxtsitvV4As64" {
		t.Errorf("private_key = %v", reality["private_key"])
	}
	sids := reality["short_id"].([]any)
	if len(sids) != 1 || sids[0] != "08b992eec8cf3b84" {
		t.Errorf("short_id = %v", sids)
	}
	hs := reality["handshake"].(map[string]any)
	if hs["server"] != "www.microsoft.com" || hs["server_port"].(float64) != 443 {
		t.Errorf("handshake = %v", hs)
	}
}

func TestBridgeConfigDisabled(t *testing.T) {
	c := testConfig()
	c.Bridge.Enabled = false
	if _, err := BridgeConfig(c); err == nil {
		t.Fatal("ожидалась ошибка при выключенном мостике")
	}
}
