package stats

import "testing"

func TestCommitKeepsBytesAccumulatedDuringRequest(t *testing.T) {
	c := New()
	c.AddBridgeBytes(100)
	c.AddCallsBytes(40)
	sent := c.Snapshot()

	// more arrived while the heartbeat was in flight
	c.AddBridgeBytes(7)
	c.Commit(sent)

	got := c.Snapshot()
	if got.BridgeBytes != 7 || got.CallsBytes != 0 {
		t.Fatalf("after Commit expected the remainder 7/0, got %d/%d", got.BridgeBytes, got.CallsBytes)
	}
}

func TestFailedHeartbeatLosesNothing(t *testing.T) {
	c := New()
	c.AddCallsBytes(10)
	_ = c.Snapshot() // the report failed — Commit was not called
	c.AddCallsBytes(5)
	if got := c.Snapshot().CallsBytes; got != 15 {
		t.Fatalf("expected 15, got %d", got)
	}
}

func TestNilCountersAreNoop(t *testing.T) {
	var c *Counters
	c.AddBridgeBytes(1)
	c.SetCallsActive(3)
	c.Commit(Report{BridgeBytes: 1})
	if (c.Snapshot() != Report{}) {
		t.Fatal("nil counters must give an empty report")
	}
}

func TestNegativeOrZeroIgnored(t *testing.T) {
	c := New()
	c.AddBridgeBytes(-5)
	c.AddCallsBytes(0)
	if r := c.Snapshot(); r.BridgeBytes != 0 || r.CallsBytes != 0 {
		t.Fatalf("a negative increase must be ignored: %+v", r)
	}
}
