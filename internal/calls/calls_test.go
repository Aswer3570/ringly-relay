package calls

import (
	"context"
	"crypto/x509"
	"net"
	"testing"
	"time"

	"github.com/pion/turn/v4"

	"github.com/ringlymessenger/ringly-relay/internal/config"
)

func TestSelfSignedCert(t *testing.T) {
	cert, err := selfSignedCert("www.example.com")
	if err != nil {
		t.Fatalf("selfSignedCert: %v", err)
	}
	if len(cert.Certificate) == 0 {
		t.Fatal("empty certificate chain")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("the certificate does not parse: %v", err)
	}
	if leaf.Subject.CommonName != "www.example.com" {
		t.Fatalf("CN = %q, expected www.example.com", leaf.Subject.CommonName)
	}
	if cert.PrivateKey == nil {
		t.Fatal("the certificate has no private key — useless for a TLS server")
	}
}

// TestTURNAllocateIntegration runs a real TURN server with our REST auth and checks that a client with
// valid credentials gets an allocation, and with a wrong secret it doesn't.
func TestTURNAllocateIntegration(t *testing.T) {
	const secret = "integration-secret"

	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("server UDP: %v", err)
	}
	defer udp.Close()
	serverAddr := udp.LocalAddr().String()

	server, err := turn.NewServer(turn.ServerConfig{
		Realm:       realm,
		AuthHandler: restAuthHandler(secret, realm, time.Now),
		PacketConnConfigs: []turn.PacketConnConfig{{
			PacketConn: udp,
			RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{
				RelayAddress: net.ParseIP("127.0.0.1"),
				Address:      "127.0.0.1",
			},
		}},
	})
	if err != nil {
		t.Fatalf("TURN server: %v", err)
	}
	defer server.Close()

	t.Run("valid credentials → allocation", func(t *testing.T) {
		user, pass := makeCred(secret, time.Now().Add(time.Hour))
		relay, closeClient := allocate(t, serverAddr, user, pass)
		defer closeClient()
		if relay == nil {
			t.Fatal("no allocation")
		}
		if !relay.LocalAddr().(*net.UDPAddr).IP.Equal(net.ParseIP("127.0.0.1")) {
			t.Fatalf("relay address %v, expected the public 127.0.0.1", relay.LocalAddr())
		}
	})

	t.Run("wrong secret → refused", func(t *testing.T) {
		user, pass := makeCred("wrong-secret", time.Now().Add(time.Hour))
		relay, closeClient := allocateExpectFail(t, serverAddr, user, pass)
		defer closeClient()
		if relay != nil {
			t.Fatal("allocation granted with a wrong secret")
		}
	})
}

func TestModuleStartStop(t *testing.T) {
	c := config.Default()
	c.Bridge.Enabled = false
	c.Calls.Enabled = true
	c.Calls.TurnListen = "127.0.0.1:0"
	c.Calls.TurnTLSListen = "" // no TLS in the smoke test
	c.Calls.Secret = "s"
	c.Calls.PublicIP = "127.0.0.1"

	m := New(c, testWriter{t}, nil)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// --- helpers ---

func allocate(t *testing.T, serverAddr, user, pass string) (net.PacketConn, func()) {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("client UDP: %v", err)
	}
	client, err := turn.NewClient(&turn.ClientConfig{
		TURNServerAddr: serverAddr,
		Conn:           conn,
		Username:       user,
		Password:       pass,
		Realm:          realm,
	})
	if err != nil {
		conn.Close()
		t.Fatalf("TURN client: %v", err)
	}
	if err := client.Listen(); err != nil {
		client.Close()
		conn.Close()
		t.Fatalf("client.Listen: %v", err)
	}
	relay, err := client.Allocate()
	if err != nil {
		client.Close()
		conn.Close()
		t.Fatalf("Allocate: %v", err)
	}
	return relay, func() {
		relay.Close()
		client.Close()
		conn.Close()
	}
}

func allocateExpectFail(t *testing.T, serverAddr, user, pass string) (net.PacketConn, func()) {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("client UDP: %v", err)
	}
	client, err := turn.NewClient(&turn.ClientConfig{
		TURNServerAddr: serverAddr,
		Conn:           conn,
		Username:       user,
		Password:       pass,
		Realm:          realm,
	})
	if err != nil {
		conn.Close()
		t.Fatalf("TURN client: %v", err)
	}
	if err := client.Listen(); err != nil {
		client.Close()
		conn.Close()
		t.Fatalf("client.Listen: %v", err)
	}
	relay, err := client.Allocate()
	if err == nil {
		return relay, func() { relay.Close(); client.Close(); conn.Close() }
	}
	return nil, func() { client.Close(); conn.Close() }
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(b []byte) (int, error) { w.t.Logf("%s", b); return len(b), nil }
