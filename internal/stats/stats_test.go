package stats

import "testing"

func TestCommitKeepsBytesAccumulatedDuringRequest(t *testing.T) {
	c := New()
	c.AddBridgeBytes(100)
	c.AddCallsBytes(40)
	sent := c.Snapshot()

	// пока heartbeat летел, пришло ещё
	c.AddBridgeBytes(7)
	c.Commit(sent)

	got := c.Snapshot()
	if got.BridgeBytes != 7 || got.CallsBytes != 0 {
		t.Fatalf("после Commit ожидали остаток 7/0, получили %d/%d", got.BridgeBytes, got.CallsBytes)
	}
}

func TestFailedHeartbeatLosesNothing(t *testing.T) {
	c := New()
	c.AddCallsBytes(10)
	_ = c.Snapshot() // отчёт не дошёл — Commit не вызывали
	c.AddCallsBytes(5)
	if got := c.Snapshot().CallsBytes; got != 15 {
		t.Fatalf("ожидали 15, получили %d", got)
	}
}

func TestNilCountersAreNoop(t *testing.T) {
	var c *Counters
	c.AddBridgeBytes(1)
	c.SetCallsActive(3)
	c.Commit(Report{BridgeBytes: 1})
	if (c.Snapshot() != Report{}) {
		t.Fatal("nil-счётчики должны отдавать пустой отчёт")
	}
}

func TestNegativeOrZeroIgnored(t *testing.T) {
	c := New()
	c.AddBridgeBytes(-5)
	c.AddCallsBytes(0)
	if r := c.Snapshot(); r.BridgeBytes != 0 || r.CallsBytes != 0 {
		t.Fatalf("отрицательный прирост не должен учитываться: %+v", r)
	}
}
