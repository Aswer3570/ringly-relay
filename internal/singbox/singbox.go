// Package singbox генерирует конфиг sing-box для мостика сообщений (VLESS + Reality).
//
// Мы не встраиваем sing-box библиотекой (огромное дерево зависимостей, хрупкий API), а супервизируем
// эталонный бинарник (см. internal/bridge). Здесь — только генерация корректного JSON-конфига из
// нашего config.Config.
//
// Схема: VLESS-inbound с TLS/Reality принимает клиента; direct-outbound дозванивается до адреса,
// который запросил клиент (в нашем случае — origin приложения). Reality-зонд, постучавшийся на порт,
// проксируется на реальный SNI-домен и видит его настоящий сертификат.
package singbox

import (
	"encoding/json"
	"fmt"

	"github.com/ringlymessenger/ringly-relay/internal/bridgelink"
	"github.com/ringlymessenger/ringly-relay/internal/config"
)

// StatsAPI — встроенный API статистики sing-box (Clash API), откуда демон берёт трафик мостика и
// число подключённых клиентов. Слушает ТОЛЬКО 127.0.0.1 и требует секрет: снаружи он не виден.
// Есть в официальной сборке sing-box (тег with_clash_api).
type StatsAPI struct {
	Listen string // "127.0.0.1:<порт>"
	Secret string
}

// BridgeConfig собирает JSON-конфиг sing-box для включённого мостика сообщений.
func BridgeConfig(c config.Config) ([]byte, error) { return BridgeConfigWithStats(c, nil) }

// BridgeConfigWithStats — то же, плюс API статистики (nil — без него).
func BridgeConfigWithStats(c config.Config, api *StatsAPI) ([]byte, error) {
	if !c.Bridge.Enabled {
		return nil, fmt.Errorf("мостик сообщений выключен")
	}
	port, err := config.PortFromListen(c.Bridge.Listen)
	if err != nil {
		return nil, err
	}

	conf := singboxConf{
		Log: logConf{Level: "warn"},
		Inbounds: []inbound{{
			Type:       "vless",
			Tag:        "vless-in",
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
	}
	if api != nil {
		conf.Experimental = &experimental{ClashAPI: clashAPI{ExternalController: api.Listen, Secret: api.Secret}}
	}

	return json.MarshalIndent(conf, "", "  ")
}

// --- подмножество схемы конфига sing-box, которое мы задаём ---

type singboxConf struct {
	Log          logConf       `json:"log"`
	Inbounds     []inbound     `json:"inbounds"`
	Outbounds    []outbound    `json:"outbounds"`
	Experimental *experimental `json:"experimental,omitempty"`
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
