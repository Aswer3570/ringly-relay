// Package calls — the call relay module (TURN) built on pion/turn.
//
// Implements runner.Module. Runs TURN on UDP+TCP and, separately, TURN over TLS (a fallback when DPI
// throttles UDP). Authentication is the coturn-compatible REST scheme (see turnauth.go), so credentials
// issued by the Ringly backend are accepted here.
//
// ⚠️ Call privacy (two TURN relays from different operators, so neither sees both IPs) is provided where
// pairs are handed out (server/client), not here: one daemon is one hop.
package calls

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/pion/turn/v4"

	"github.com/ringlymessenger/ringly-relay/internal/config"
	"github.com/ringlymessenger/ringly-relay/internal/stats"
)

const realm = "ringly"

// Module supervises the TURN server.
type Module struct {
	cfg      config.Config
	out      io.Writer
	counters *stats.Counters // traffic and number of calls for the server report (nil — not counted)
	server   *turn.Server
	conns    []io.Closer   // listeners to close on stop
	stopTick chan struct{} // stops polling the number of calls
}

// New creates the calls module. counters — the daemon's shared counters (may be nil).
func New(cfg config.Config, out io.Writer, counters *stats.Counters) *Module {
	return &Module{cfg: cfg, out: out, counters: counters}
}

// Name — the module name for runner logs.
func (m *Module) Name() string { return "calls" }

// Start opens the listeners and starts the TURN server.
func (m *Module) Start(ctx context.Context) error {
	publicIP := net.ParseIP(m.cfg.Calls.PublicIP)
	if publicIP == nil {
		return fmt.Errorf("calls.public_ip = %q is not an IP address", m.cfg.Calls.PublicIP)
	}
	gen := countingGenerator{
		RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{
			RelayAddress: publicIP,  // the address TURN hands to peers (must be public)
			Address:      "0.0.0.0", // the interface relay ports actually listen on
		},
		counters: m.counters,
	}
	auth := restAuthHandler(m.cfg.Calls.Secret, realm, time.Now)

	var (
		packetConns []turn.PacketConnConfig
		listeners   []turn.ListenerConfig
	)

	// UDP — the main path.
	udp, err := net.ListenPacket("udp4", m.cfg.Calls.TurnListen)
	if err != nil {
		return fmt.Errorf("TURN UDP %s: %w", m.cfg.Calls.TurnListen, err)
	}
	m.conns = append(m.conns, udp)
	packetConns = append(packetConns, turn.PacketConnConfig{PacketConn: udp, RelayAddressGenerator: gen})

	// TCP — on the same port (TURN over TCP).
	tcp, err := net.Listen("tcp4", m.cfg.Calls.TurnListen)
	if err != nil {
		m.closeConns()
		return fmt.Errorf("TURN TCP %s: %w", m.cfg.Calls.TurnListen, err)
	}
	m.conns = append(m.conns, tcp)
	listeners = append(listeners, turn.ListenerConfig{Listener: tcp, RelayAddressGenerator: gen})

	// TLS — the fallback when DPI throttles UDP.
	if m.cfg.Calls.TurnTLSListen != "" {
		cert, err := m.tlsCertificate()
		if err != nil {
			m.closeConns()
			return err
		}
		tlsL, err := tls.Listen("tcp4", m.cfg.Calls.TurnTLSListen, &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		})
		if err != nil {
			m.closeConns()
			return fmt.Errorf("TURN TLS %s: %w", m.cfg.Calls.TurnTLSListen, err)
		}
		m.conns = append(m.conns, tlsL)
		listeners = append(listeners, turn.ListenerConfig{Listener: tlsL, RelayAddressGenerator: gen})
	}

	server, err := turn.NewServer(turn.ServerConfig{
		Realm:             realm,
		AuthHandler:       auth,
		PacketConnConfigs: packetConns,
		ListenerConfigs:   listeners,
	})
	if err != nil {
		m.closeConns()
		return fmt.Errorf("starting the TURN server: %w", err)
	}
	m.server = server
	m.stopTick = make(chan struct{})
	go m.trackActive(server, m.stopTick)

	if m.cfg.Calls.TrafficCapGB > 0 {
		// v1: the limit is informational (the distribution server takes the daemon out of rotation based on
		// the report); the relay doesn't enforce a hard byte cap yet.
		fmt.Fprintf(m.out, "[calls] traffic limit %d GB/month (enforced by distribution, not a hard cap)\n", m.cfg.Calls.TrafficCapGB)
	}
	return nil
}

// Stop stops the TURN server and closes the listeners.
func (m *Module) Stop(ctx context.Context) error {
	if m.stopTick != nil {
		close(m.stopTick)
		m.stopTick = nil
	}
	if m.server != nil {
		if err := m.server.Close(); err != nil {
			return fmt.Errorf("stopping the TURN server: %w", err)
		}
		m.server = nil
	}
	m.closeConns()
	return nil
}

// trackActive refreshes the number of ongoing calls (TURN allocations) every 15 s — for the report.
func (m *Module) trackActive(server *turn.Server, stop <-chan struct{}) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	m.counters.SetCallsActive(int64(server.AllocationCount()))
	for {
		select {
		case <-stop:
			m.counters.SetCallsActive(0)
			return
		case <-t.C:
			m.counters.SetCallsActive(int64(server.AllocationCount()))
		}
	}
}

func (m *Module) closeConns() {
	for i := len(m.conns) - 1; i >= 0; i-- {
		_ = m.conns[i].Close()
	}
	m.conns = nil
}

// tlsCertificate takes the certificate from the configuration (disguise), otherwise generates a self-signed one.
func (m *Module) tlsCertificate() (tls.Certificate, error) {
	if m.cfg.Calls.TLSCert != "" && m.cfg.Calls.TLSKey != "" {
		cert, err := tls.LoadX509KeyPair(m.cfg.Calls.TLSCert, m.cfg.Calls.TLSKey)
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("loading the TLS certificate: %w", err)
		}
		return cert, nil
	}
	fmt.Fprintln(m.out, "[calls] TURN over TLS with a self-signed certificate "+
		"(encrypts but doesn't disguise as a website; set tls_cert/tls_key for that)")
	return selfSignedCert(m.cfg.Reality.SNI)
}
