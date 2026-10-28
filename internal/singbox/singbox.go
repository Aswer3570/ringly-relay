// Package singbox generates the sing-box configuration for the message bridge (VLESS + Reality).
//
// sing-box is not embedded as a library (a huge dependency tree and a fragile API) — the daemon
// supervises the reference binary instead (see internal/bridge). This package only turns our
// config.Config into a valid sing-box JSON configuration.
//
// Layout: a VLESS inbound with TLS/Reality accepts the client; a direct outbound connects to the
// destination the client asked for. A Reality probe knocking on the port is proxied to the real cover
// domain and sees its genuine certificate.
//
// Destinations are restricted: the bridge exists for the Ringly app only, so connections are allowed
// to Ringly's own hosts and rejected otherwise. Without this rule anyone holding a bridge link could
// use the volunteer's server as a general-purpose proxy.
package singbox

import (
	"encoding/json"
	"fmt"

	"github.com/ringlymessenger/ringly-relay/internal/bridgelink"
	"github.com/ringlymessenger/ringly-relay/internal/config"
)

// AllowedDomainSuffixes — where the app is allowed to go through a bridge: the API and WebSocket,
// media links and the central TURN server for calls all live under these domains. A domain suffix
// matches the domain itself and all its subdomains.
var AllowedDomainSuffixes = []string{"ringlyapp.net"}

const inboundTag = "vless-in"

// StatsAPI — sing-box's built-in statistics API (Clash API), where the daemon reads the bridge
// traffic and the number of connected clients. It listens ONLY on 127.0.0.1 and requires a secret,
// so it is invisible from the outside. The official sing-box build includes it (tag with_clash_api).
type StatsAPI struct {
	Listen string // "127.0.0.1:<port>"
	Secret string
}

// BridgeConfig builds the sing-box JSON configuration for an enabled message bridge.
func BridgeConfig(c config.Config) ([]byte, error) { return BridgeConfigWithStats(c, nil) }

// BridgeConfigWithStats — the same plus the statistics API (nil — without it).
func BridgeConfigWithStats(c config.Config, api *StatsAPI) ([]byte, error) {
	if !c.Bridge.Enabled {
		return nil, fmt.Errorf("the message bridge is disabled")
	}
	port, err := config.PortFromListen(c.Bridge.Listen)
	if err != nil {
		return nil, err
	}

	conf := singboxConf{
		Log: logConf{Level: "warn"},
		Inbounds: []inbound{{
			Type:       "vless",
			Tag:        inboundTag,
			Listen:     "::",
			ListenPort: port,
			Users: []vlessUser{{
				UUID: c.Reality.UUID,
				Flow: bridgelink.Flow,
			}},
			TLS: &tlsConf{
				Enabled:    true,
				ServerName: c.Reality.SNI,
				Reality: &realityConf{
					Enabled: true,
					Handshake: handshake{
						Server:     c.Reality.SNI,
						ServerPort: 443,
					},
					PrivateKey: c.Reality.PrivateKey,
					ShortID:    []string{c.Reality.ShortID},
				},
			},
		}},
		Outbounds: []outbound{{Type: "direct", Tag: "direct"}},
		Route: route{
			Rules: []rule{
				// The app names the host (HTTP CONNECT through the tunnel), so the domain is usually
				// known; sniffing recovers it from TLS SNI when a client connects by IP.
				{Inbound: []string{inboundTag}, Action: "sniff"},
				{DomainSuffix: AllowedDomainSuffixes, Action: "route", Outbound: "direct"},
				// everything else that came through the bridge is refused
				{Inbound: []string{inboundTag}, Action: "reject"},
			},
			Final: "direct",
		},
	}
	if api != nil {
		conf.Experimental = &experimental{ClashAPI: clashAPI{ExternalController: api.Listen, Secret: api.Secret}}
	}

	return json.MarshalIndent(conf, "", "  ")
}

// --- the subset of the sing-box configuration schema we set ---

type singboxConf struct {
	Log          logConf       `json:"log"`
	Inbounds     []inbound     `json:"inbounds"`
	Outbounds    []outbound    `json:"outbounds"`
	Route        route         `json:"route"`
	Experimental *experimental `json:"experimental,omitempty"`
}

type route struct {
	Rules []rule `json:"rules"`
	Final string `json:"final"`
}

type rule struct {
	Inbound      []string `json:"inbound,omitempty"`
	DomainSuffix []string `json:"domain_suffix,omitempty"`
	Action       string   `json:"action"`
	Outbound     string   `json:"outbound,omitempty"`
}

type experimental struct {
	ClashAPI clashAPI `json:"clash_api"`
}

type clashAPI struct {
	ExternalController string `json:"external_controller"`
	Secret             string `json:"secret"`
}

type logConf struct {
	Level string `json:"level"`
}

type inbound struct {
	Type       string      `json:"type"`
	Tag        string      `json:"tag"`
	Listen     string      `json:"listen"`
	ListenPort int         `json:"listen_port"`
	Users      []vlessUser `json:"users"`
	TLS        *tlsConf    `json:"tls,omitempty"`
}

type vlessUser struct {
	UUID string `json:"uuid"`
	Flow string `json:"flow"`
}

type tlsConf struct {
	Enabled    bool         `json:"enabled"`
	ServerName string       `json:"server_name"`
	Reality    *realityConf `json:"reality,omitempty"`
}

type realityConf struct {
	Enabled    bool      `json:"enabled"`
	Handshake  handshake `json:"handshake"`
	PrivateKey string    `json:"private_key"`
	ShortID    []string  `json:"short_id"`
}

type handshake struct {
	Server     string `json:"server"`
	ServerPort int    `json:"server_port"`
}

type outbound struct {
	Type string `json:"type"`
	Tag  string `json:"tag"`
}
