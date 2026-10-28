// Package stats — traffic and connection counters the daemon sends to the control server.
//
// Totals and counts only — no client addresses or identifiers: the server learns "how many", never
// "who". Bytes accumulate as the increase since the last successful heartbeat: once sent, the sent part
// is subtracted (Commit); if a heartbeat fails, the increase goes with the next one — nothing is lost.
//
// All methods are safe on a nil pointer: a module without counters (tests, a manual run without
// publishing) simply counts nothing.
package stats

import "sync/atomic"

// Counters are shared by the bridge, calls and publisher modules.
type Counters struct {
	bridgeBytes   atomic.Int64 // message bridge traffic since the last report
	callsBytes    atomic.Int64 // call (TURN) traffic since the last report
	bridgeClients atomic.Int64 // connected to the bridge now (distinct clients)
	callsActive   atomic.Int64 // calls through TURN right now (allocations)
	bridgeKnown   atomic.Bool  // bridge statistics are available at all (sing-box provides them)
}

// New creates empty counters.
func New() *Counters { return &Counters{} }

// Report — what goes into the heartbeat.
type Report struct {
	BridgeBytes   int64
	CallsBytes    int64
	BridgeClients int64
	CallsActive   int64
	BridgeKnown   bool
}

func (c *Counters) AddBridgeBytes(n int64) {
	if c != nil && n > 0 {
		c.bridgeBytes.Add(n)
	}
}

func (c *Counters) AddCallsBytes(n int64) {
	if c != nil && n > 0 {
		c.callsBytes.Add(n)
	}
}

func (c *Counters) SetBridgeClients(n int64) {
	if c != nil {
		c.bridgeClients.Store(n)
		c.bridgeKnown.Store(true)
	}
}

func (c *Counters) SetCallsActive(n int64) {
	if c != nil {
		c.callsActive.Store(n)
	}
}

// Snapshot — the current values for a report (the byte increase is not reset).
func (c *Counters) Snapshot() Report {
	if c == nil {
		return Report{}
	}
	return Report{
		BridgeBytes:   c.bridgeBytes.Load(),
		CallsBytes:    c.callsBytes.Load(),
		BridgeClients: c.bridgeClients.Load(),
		CallsActive:   c.callsActive.Load(),
		BridgeKnown:   c.bridgeKnown.Load(),
	}
}

// Commit subtracts the increase already delivered to the server. Subtracting rather than resetting:
// bytes accumulated while the request was in flight stay for the next report.
func (c *Counters) Commit(sent Report) {
	if c == nil {
		return
	}
	c.bridgeBytes.Add(-sent.BridgeBytes)
	c.callsBytes.Add(-sent.CallsBytes)
}
