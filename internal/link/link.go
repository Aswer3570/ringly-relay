// Package link prints the bridge link for private mode (shared by hand).
//
// In private mode the control server doesn't know about the daemon; this link is the only way to hand
// out the bridge. The public address comes from --host, otherwise from the configuration
// (publish.public_host, calls.public_ip), otherwise from the outgoing interface. Behind NAT it can't be
// detected — then --host is required.
package link

import (
	"fmt"
	"io"

	"github.com/ringlymessenger/ringly-relay/internal/bridgelink"
	"github.com/ringlymessenger/ringly-relay/internal/config"
	"github.com/ringlymessenger/ringly-relay/internal/netcheck"
)

// Print builds and prints the bridge link. host — the daemon's public address (IP or domain).
func Print(c config.Config, host string, out io.Writer) error {
	if !c.Bridge.Enabled {
		return fmt.Errorf("the message bridge is off — there is nothing to link to")
	}
	if host == "" {
		host = firstNonEmpty(c.Publish.PublicHost, c.Calls.PublicIP)
	}
	if host == "" {
		ip, err := netcheck.PublicIP()
		if err != nil {
			return fmt.Errorf("%v\nset the address: ringly-relay link --host <ip-or-domain>", err)
		}
		host = ip
	}
	pub, err := bridgelink.PublicFromPrivate(c.Reality.PrivateKey)
	if err != nil {
		return err
	}
	port, err := config.PortFromListen(c.Bridge.Listen)
	if err != nil {
		return err
	}
	d := bridgelink.Descriptor{
		Host:      host,
		Port:      port,
		PublicKey: pub,
		ShortID:   c.Reality.ShortID,
		SNI:       c.Reality.SNI,
		UUID:      c.Reality.UUID,
		Msg:       true,
		Calls:     c.Calls.Enabled,
	}
	s, err := bridgelink.Encode(d)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Bridge link (give it to the user):")
	fmt.Fprintln(out, s)
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
