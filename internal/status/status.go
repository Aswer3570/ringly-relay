// Package status печатает текущее состояние демона по конфигу.
//
// Этап 1: читает конфиг и показывает, что настроено. Живую статистику (аптайм, объём трафика, связь
// с control-сервером) добавит этап 3, когда появится runtime-состояние.
package status

import (
	"fmt"
	"io"

	"github.com/ringlymessenger/ringly-relay/internal/bridgelink"
	"github.com/ringlymessenger/ringly-relay/internal/config"
)

// Print выводит сводку конфигурации демона.
func Print(c config.Config, out io.Writer) {
	fmt.Fprintln(out, "Ringly Relay — состояние")
	fmt.Fprintf(out, "  origin:      %s\n", c.Origin.URL)
	fmt.Fprintf(out, "  публикация:  %s\n", c.Publish.Mode)

	fmt.Fprintf(out, "  мостик:      %s\n", onOff(c.Bridge.Enabled))
	if c.Bridge.Enabled {
		fmt.Fprintf(out, "               вход %s, SNI %s\n", c.Bridge.Listen, c.Reality.SNI)
		if pub, err := bridgelink.PublicFromPrivate(c.Reality.PrivateKey); err == nil {
			fmt.Fprintf(out, "               Reality pubkey %s\n", pub)
		}
	}

	fmt.Fprintf(out, "  звонки:      %s\n", onOff(c.Calls.Enabled))
	if c.Calls.Enabled {
		fmt.Fprintf(out, "               TURN %s / TLS %s, публичный IP %s\n",
			c.Calls.TurnListen, c.Calls.TurnTLSListen, c.Calls.PublicIP)
		tlsMode := "self-signed"
		if c.Calls.TLSCert != "" {
			tlsMode = "свой сертификат"
		}
		fmt.Fprintf(out, "               TLS-сертификат: %s\n", tlsMode)
	}

	if err := c.Validate(); err != nil {
		fmt.Fprintf(out, "  ⚠️ конфиг:   %v\n", err)
	} else {
		fmt.Fprintln(out, "  конфиг:      корректен")
	}
}

func onOff(b bool) string {
	if b {
		return "включён"
	}
	return "выключен"
}
