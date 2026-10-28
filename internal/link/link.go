// Package link печатает ссылку-мостик для приватного режима (раздаётся из рук в руки).
//
// В приватном режиме control-сервер о демоне не знает; единственный способ передать мостик — эта
// ссылка. Публичный адрес берётся из --host, иначе из конфига (publish.public_host, calls.public_ip),
// иначе определяется по исходящему интерфейсу. За NAT определить его нельзя — тогда просим --host.
package link

import (
	"fmt"
	"io"

	"github.com/ringlymessenger/ringly-relay/internal/bridgelink"
	"github.com/ringlymessenger/ringly-relay/internal/config"
	"github.com/ringlymessenger/ringly-relay/internal/netcheck"
)

// Print формирует и печатает ссылку-мостик. host — публичный адрес демона (IP или домен).
func Print(c config.Config, host string, out io.Writer) error {
	if !c.Bridge.Enabled {
		return fmt.Errorf("мостик сообщений выключен — ссылку выдавать нечему")
	}
	if host == "" {
		host = firstNonEmpty(c.Publish.PublicHost, c.Calls.PublicIP)
	}
	if host == "" {
		ip, err := netcheck.PublicIP()
		if err != nil {
			return fmt.Errorf("%v\nукажите адрес: ringly-relay link --host <ip-или-домен>", err)
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
	fmt.Fprintln(out, "Ссылка-мостик (передайте пользователю):")
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
