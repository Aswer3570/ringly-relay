package singbox

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"

	"github.com/ringlymessenger/ringly-relay/internal/bridgelink"
	"github.com/ringlymessenger/ringly-relay/internal/config"
)

// SelfTestConfig builds a sing-box CLIENT configuration that connects to this daemon's own bridge
// exactly the way the app does (VLESS + Reality, uTLS chrome) and exposes it as a local HTTP proxy on
// 127.0.0.1:proxyPort.
//
// Why a real client: "the port listens and the cover domain opens over TLS 1.3" did not mean a client
// could get through — with www.microsoft.com every connection failed inside the Reality handshake,
// while every other check stayed green.
func SelfTestConfig(c config.Config, proxyPort int) ([]byte, error) {
	if !c.Bridge.Enabled {
		return nil, fmt.Errorf("the message bridge is disabled")
	}
	port, err := config.PortFromListen(c.Bridge.Listen)
	if err != nil {
		return nil, err
	}
	pub, err := bridgelink.PublicFromPrivate(c.Reality.PrivateKey)
	if err != nil {
		return nil, err
	}

	conf := clientConf{
		Log: logConf{Level: "error"},
		Inbounds: []clientInbound{{
			Type:       "mixed",
			Tag:        "selftest-in",
			Listen:     "127.0.0.1",
			ListenPort: proxyPort,
		}},
		Outbounds: []clientOutbound{{
			Type:       "vless",
			Tag:        "bridge",
			Server:     localHost(c.Bridge.Listen),
			ServerPort: port,
			UUID:       c.Reality.UUID,
			Flow:       bridgelink.Flow,
			TLS: clientTLS{
				Enabled:    true,
				ServerName: c.Reality.SNI,
				UTLS:       utlsConf{Enabled: true, Fingerprint: "chrome"},
				Reality:    clientReality{Enabled: true, PublicKey: pub, ShortID: c.Reality.ShortID},
			},
		}},
	}
	return json.MarshalIndent(conf, "", "  ")
}

// localHost — where to reach the bridge from this machine: the listen address itself, or loopback when
// the bridge listens on all interfaces.
func localHost(listen string) string {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return "127.0.0.1"
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		return "127.0.0.1"
	}
	return host
}

// ProxyURL — the local proxy address of the self-test client.
func ProxyURL(proxyPort int) string {
	return "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(proxyPort))
}

// --- the client side of the sing-box schema ---

type clientConf struct {
	Log       logConf          `json:"log"`
	Inbounds  []clientInbound  `json:"inbounds"`
	Outbounds []clientOutbound `json:"outbounds"`
}

type clientInbound struct {
	Type       string `json:"type"`
	Tag        string `json:"tag"`
	Listen     string `json:"listen"`
	ListenPort int    `json:"listen_port"`
}

type clientOutbound struct {
	Type       string    `json:"type"`
	Tag        string    `json:"tag"`
	Server     string    `json:"server"`
	ServerPort int       `json:"server_port"`
	UUID       string    `json:"uuid"`
	Flow       string    `json:"flow"`
	TLS        clientTLS `json:"tls"`
}

type clientTLS struct {
	Enabled    bool          `json:"enabled"`
	ServerName string        `json:"server_name"`
	UTLS       utlsConf      `json:"utls"`
	Reality    clientReality `json:"reality"`
}

type utlsConf struct {
	Enabled     bool   `json:"enabled"`
	Fingerprint string `json:"fingerprint"`
}

type clientReality struct {
	Enabled   bool   `json:"enabled"`
	PublicKey string `json:"public_key"`
	ShortID   string `json:"short_id"`
}
