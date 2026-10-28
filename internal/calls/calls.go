// Package calls — модуль ретранслятора звонков (TURN) на базе pion/turn.
//
// Реализует runner.Module. Поднимает TURN на UDP+TCP и, отдельно, TURN-over-TLS (фолбэк, когда
// DPI душит UDP). Аутентификация — coturn-совместимая REST-схема (см. turnauth.go), так что креды,
// выданные бэкендом Ringly, здесь принимаются.
//
// ⚠️ Приватность звонков (два TURN от разных операторов, чтобы ни один не видел оба IP) обеспечивается
// на стороне выдачи пар (сервер/клиент), а не здесь: один демон — один хоп. См. relay-daemon.md §5.
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

// Module супервизирует TURN-сервер.
type Module struct {
	cfg      config.Config
	out      io.Writer
	counters *stats.Counters // трафик и число звонков для отчёта на сервер (nil — не считаем)
	server   *turn.Server
	conns    []io.Closer   // слушатели, которые надо закрыть при остановке
	stopTick chan struct{} // остановить опрос числа звонков
}

// New создаёт модуль звонков. counters — общие счётчики демона (может быть nil).
func New(cfg config.Config, out io.Writer, counters *stats.Counters) *Module {
	return &Module{cfg: cfg, out: out, counters: counters}
}

// Name — имя модуля для логов runner.
func (m *Module) Name() string { return "calls" }

// Start открывает слушатели и запускает TURN-сервер.
func (m *Module) Start(ctx context.Context) error {
	publicIP := net.ParseIP(m.cfg.Calls.PublicIP)
	if publicIP == nil {
		return fmt.Errorf("calls.public_ip = %q не является IP-адресом", m.cfg.Calls.PublicIP)
	}
	gen := countingGenerator{
		RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{
			RelayAddress: publicIP,  // адрес, который TURN раздаёт пирам (обязан быть публичным)
			Address:      "0.0.0.0", // на каком интерфейсе реально слушать relay-порты
		},
		counters: m.counters,
	}
	auth := restAuthHandler(m.cfg.Calls.Secret, realm, time.Now)

	var (
		packetConns []turn.PacketConnConfig
		listeners   []turn.ListenerConfig
	)

	// UDP — основной путь.
	udp, err := net.ListenPacket("udp4", m.cfg.Calls.TurnListen)
	if err != nil {
		return fmt.Errorf("TURN UDP %s: %w", m.cfg.Calls.TurnListen, err)
	}
	m.conns = append(m.conns, udp)
	packetConns = append(packetConns, turn.PacketConnConfig{PacketConn: udp, RelayAddressGenerator: gen})

	// TCP — на том же порту (TURN over TCP).
	tcp, err := net.Listen("tcp4", m.cfg.Calls.TurnListen)
	if err != nil {
		m.closeConns()
		return fmt.Errorf("TURN TCP %s: %w", m.cfg.Calls.TurnListen, err)
	}
	m.conns = append(m.conns, tcp)
	listeners = append(listeners, turn.ListenerConfig{Listener: tcp, RelayAddressGenerator: gen})

	// TLS — фолбэк под DPI на UDP.
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
		return fmt.Errorf("запуск TURN-сервера: %w", err)
	}
	m.server = server
	m.stopTick = make(chan struct{})
	go m.trackActive(server, m.stopTick)

	if m.cfg.Calls.TrafficCapGB > 0 {
		// v1: лимит носит информативный характер (сервер выдачи снимает демон с ротации по отчёту),
		// жёсткого побайтового учёта в relay пока нет. См. relay-daemon.md §5.
		fmt.Fprintf(m.out, "[calls] лимит трафика %d ГБ/мес (учёт на стороне выдачи, не жёсткий)\n", m.cfg.Calls.TrafficCapGB)
	}
	return nil
}

// Stop останавливает TURN-сервер и закрывает слушатели.
func (m *Module) Stop(ctx context.Context) error {
	if m.stopTick != nil {
		close(m.stopTick)
		m.stopTick = nil
	}
	if m.server != nil {
		if err := m.server.Close(); err != nil {
			return fmt.Errorf("остановка TURN-сервера: %w", err)
		}
		m.server = nil
	}
	m.closeConns()
	return nil
}

// trackActive раз в 15 с переписывает число идущих звонков (аллокаций TURN) — для отчёта.
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

// tlsCertificate берёт сертификат из конфига (маскировка), иначе генерирует самоподписанный.
func (m *Module) tlsCertificate() (tls.Certificate, error) {
	if m.cfg.Calls.TLSCert != "" && m.cfg.Calls.TLSKey != "" {
		cert, err := tls.LoadX509KeyPair(m.cfg.Calls.TLSCert, m.cfg.Calls.TLSKey)
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("загрузка TLS-сертификата: %w", err)
		}
		return cert, nil
	}
	fmt.Fprintln(m.out, "[calls] TURN-over-TLS с самоподписанным сертификатом "+
		"(шифрует, но не маскирует под сайт; для маскировки задайте tls_cert/tls_key)")
	return selfSignedCert(m.cfg.Reality.SNI)
}
