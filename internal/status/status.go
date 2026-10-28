// Package status prints the daemon's current configuration.
//
// It reads the configuration and shows what is set up. Live numbers (traffic, connected clients) are
// reported to the control server in auto mode and checked by `ringly-relay doctor`.
package status

import (
	"fmt"
	"io"

	"github.com/ringlymessenger/ringly-relay/internal/bridgelink"
	"github.com/ringlymessenger/ringly-relay/internal/config"
)

// Print prints a summary of the daemon's configuration.
func Print(c config.Config, out io.Writer) {
	fmt.Fprintln(out, "Ringly Relay — status")
	fmt.Fprintf(out, "  origin:      %s\n", c.Origin.URL)
	fmt.Fprintf(out, "  publishing:  %s\n", c.Publish.Mode)

	fmt.Fprintf(out, "  bridge:      %s\n", onOff(c.Bridge.Enabled))
	if c.Bridge.Enabled {
		fmt.Fprintf(out, "               entry %s, SNI %s\n", c.Bridge.Listen, c.Reality.SNI)
		if pub, err := bridgelink.PublicFromPrivate(c.Reality.PrivateKey); err == nil {
			fmt.Fprintf(out, "               Reality pubkey %s\n", pub)
		}
	}

	fmt.Fprintf(out, "  calls:       %s\n", onOff(c.Calls.Enabled))
	if c.Calls.Enabled {
		fmt.Fprintf(out, "               TURN %s / TLS %s, public IP %s\n",
			c.Calls.TurnListen, c.Calls.TurnTLSListen, c.Calls.PublicIP)
		tlsMode := "self-signed"
		if c.Calls.TLSCert != "" {
			tlsMode = "own certificate"
		}
		fmt.Fprintf(out, "               TLS certificate: %s\n", tlsMode)
	}

	if err := c.Validate(); err != nil {
		fmt.Fprintf(out, "  ⚠️ config:   %v\n", err)
	} else {
		fmt.Fprintln(out, "  config:      valid")
	}
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
