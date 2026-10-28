package calls

import (
	"net"

	"github.com/pion/turn/v4"

	"github.com/ringlymessenger/ringly-relay/internal/stats"
)

// countingGenerator — обёртка над генератором relay-адресов pion: каждый выданный звонку relay-порт
// считает прошедшие через него байты. Это сторона «relay ↔ собеседник», то есть весь медиапоток
// звонка в одну ногу — именно столько трафика звонок стоит волонтёру (вторая нога «клиент ↔ TURN»
// такая же по объёму). Считаются только размеры пакетов, их содержимое не трогаем — оно и так
// зашифровано (SRTP).
type countingGenerator struct {
	turn.RelayAddressGenerator
	counters *stats.Counters
}

func (g countingGenerator) AllocatePacketConn(network string, requestedPort int) (net.PacketConn, net.Addr, error) {
	conn, addr, err := g.RelayAddressGenerator.AllocatePacketConn(network, requestedPort)
	if err != nil || g.counters == nil {
		return conn, addr, err
	}
	return countingPacketConn{PacketConn: conn, counters: g.counters}, addr, nil
}

type countingPacketConn struct {
	net.PacketConn
	counters *stats.Counters
}

func (c countingPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, addr, err := c.PacketConn.ReadFrom(p)
	c.counters.AddCallsBytes(int64(n))
	return n, addr, err
}

func (c countingPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	n, err := c.PacketConn.WriteTo(p, addr)
	c.counters.AddCallsBytes(int64(n))
	return n, err
}
