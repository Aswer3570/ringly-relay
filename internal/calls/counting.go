package calls

import (
	"net"

	"github.com/pion/turn/v4"

	"github.com/ringlymessenger/ringly-relay/internal/stats"
)

// countingGenerator — a wrapper around pion's relay address generator: every relay port handed to a
// call counts the bytes passing through it. This is the "relay ↔ peer" side, i.e. the call's whole media
// stream in one direction — exactly what a call costs the volunteer (the "client ↔ TURN" leg is the
// same size). Only packet sizes are counted; the content is not touched — it is encrypted anyway (SRTP).
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
