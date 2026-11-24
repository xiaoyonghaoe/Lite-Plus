package agent

import (
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/metricstore"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
)

func resetV2EventQueuesForTest(t *testing.T) {
	t.Helper()
	v2EventMu.Lock()
	original := v2EventQueues
	v2EventQueues = make(map[string]*v2EventQueue)
	v2EventMu.Unlock()
	t.Cleanup(func() {
		v2EventMu.Lock()
		v2EventQueues = original
		v2EventMu.Unlock()
	})
}

func TestTakeV2EventsWithMoreAndAck(t *testing.T) {
	resetV2EventQueuesForTest(t)

	for i := 0; i < 5; i++ {
		EnqueueV2Event("node-a", v2.MethodAgentMessage, map[string]any{"idx": i})
	}
	if !HasPendingV2Events("node-a") {
		t.Fatal("expected pending events for node-a")
	}

	batch1, hasMore1 := TakeV2EventsWithMore("node-a", nil, 3)
	if len(batch1) != 3 || !hasMore1 {
		t.Fatalf("batch1 len=%d hasMore=%v, want 3, true", len(batch1), hasMore1)
	}

	ackIDs := []string{batch1[0].ID, batch1[1].ID, batch1[2].ID}
	batch2, hasMore2 := TakeV2EventsWithMore("node-a", ackIDs, 3)
	if len(batch2) != 2 || hasMore2 {
		t.Fatalf("batch2 len=%d hasMore=%v, want 2, false", len(batch2), hasMore2)
	}

	ackIDs2 := []string{batch2[0].ID, batch2[1].ID}
	batch3, hasMore3 := TakeV2EventsWithMore("node-a", ackIDs2, 3)
	if len(batch3) != 0 || hasMore3 {
		t.Fatalf("batch3 len=%d hasMore=%v, want 0, false", len(batch3), hasMore3)
	}
	if HasPendingV2Events("node-a") {
		t.Fatal("expected no pending events after all ACKs")
	}
}

func TestRemoveAllV2EventsByMethodsClearsEveryQueue(t *testing.T) {
	resetV2EventQueuesForTest(t)

	EnqueueV2Event("node-a", v2.MethodAgentMessage, map[string]any{"msg": "a"})
	EnqueueV2Event("node-b", v2.MethodAgentMessage, map[string]any{"msg": "b"})
	EnqueueV2Event("node-b", v2.MethodAgentPing, v2.PingParams{TaskID: 3})

	RemoveAllV2EventsByMethods(v2.MethodAgentMessage)
	if events := TakeV2Events("node-a", nil, 16); len(events) != 0 {
		t.Fatalf("node-a message events remained: %#v", events)
	}
	if events := TakeV2Events("node-b", nil, 16); len(events) != 1 || events[0].Method != v2.MethodAgentPing {
		t.Fatalf("node-b events = %#v", events)
	}
}

func TestRemoveV2EventQueueClearsOnlyDeletedClient(t *testing.T) {
	resetV2EventQueuesForTest(t)

	EnqueueV2Event("node-a", v2.MethodAgentPing, v2.PingParams{TaskID: 1})
	EnqueueV2Event("node-b", v2.MethodAgentPing, v2.PingParams{TaskID: 2})
	RemoveV2EventQueue("node-a")
	metricstore.BlockEntityWrites("node-a")
	t.Cleanup(func() { metricstore.UnblockEntityWrites("node-a") })
	if event := EnqueueV2Event("node-a", v2.MethodAgentPing, v2.PingParams{TaskID: 3}); event.ID != "" {
		t.Fatalf("blocked node accepted late event: %#v", event)
	}
	if events := TakeV2Events("node-b", nil, 16); len(events) != 1 {
		t.Fatalf("node-b events = %#v", events)
	}
}

func TestCoalesceV2ConfigAndFirewallEvents(t *testing.T) {
	resetV2EventQueuesForTest(t)

	EnqueueV2Event("node-a", v2.MethodAgentConfig, v2.ConfigParams{Revision: 1})
	EnqueueV2Event("node-a", v2.MethodAgentConfig, v2.ConfigParams{Revision: 2})
	EnqueueV2Event("node-a", v2.MethodAgentFirewallApply, v2.FirewallApplyParams{RequestID: "req-1"})
	EnqueueV2Event("node-a", v2.MethodAgentFirewallApply, v2.FirewallApplyParams{RequestID: "req-2"})

	events := TakeV2Events("node-a", nil, 16)
	if len(events) != 2 {
		t.Fatalf("expected 2 coalesced events, got %d: %#v", len(events), events)
	}
}

func TestSweepExpiredV2EventsPrunesOldEvents(t *testing.T) {
	resetV2EventQueuesForTest(t)

	EnqueueV2Event("node-a", v2.MethodAgentMessage, map[string]any{"msg": "old"})
	v2EventMu.Lock()
	v2EventQueues["node-a"].events[0].ExpiresAt = time.Now().UTC().Add(-time.Second)
	v2EventMu.Unlock()

	SweepExpiredV2Events()
	if HasPendingV2Events("node-a") {
		t.Fatal("expired event should have been pruned")
	}
}
