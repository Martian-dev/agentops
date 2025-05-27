package agent

import (
	"sync"
	"testing"
	"time"
)

func TestStateStore_ApplyAndSnapshot(t *testing.T) {
	store := NewStateStore()

	store.Apply(StateDelta{NodeID: "a", Status: NodeStatusPending})
	store.Apply(StateDelta{NodeID: "b", Status: NodeStatusPending})
	store.Apply(StateDelta{NodeID: "a", Status: NodeStatusRunning})
	store.Apply(StateDelta{NodeID: "a", Status: NodeStatusSuccess, Output: `{"result":"hello"}`})

	snap := store.Snapshot()
	if snap["a"].Status != NodeStatusSuccess {
		t.Fatalf("expected node 'a' to be success, got %s", snap["a"].Status)
	}
	if snap["a"].Output != `{"result":"hello"}` {
		t.Fatalf("expected node 'a' output, got %s", snap["a"].Output)
	}
	if snap["b"].Status != NodeStatusPending {
		t.Fatalf("expected node 'b' to be pending, got %s", snap["b"].Status)
	}
}

func TestStateStore_SnapshotIsIsolated(t *testing.T) {
	store := NewStateStore()
	store.Apply(StateDelta{NodeID: "x", Status: NodeStatusSuccess, Output: "out1"})

	snap1 := store.Snapshot()

	// Mutate snapshot — should not affect store
	snap1["x"].Output = "MUTATED"

	snap2 := store.Snapshot()
	if snap2["x"].Output == "MUTATED" {
		t.Fatal("snapshot mutation leaked into store")
	}
	if snap2["x"].Output != "out1" {
		t.Fatalf("expected original output, got %s", snap2["x"].Output)
	}
}

func TestStateStore_SnapshotAt(t *testing.T) {
	store := NewStateStore()

	v1 := store.Apply(StateDelta{NodeID: "a", Status: NodeStatusPending})
	v2 := store.Apply(StateDelta{NodeID: "a", Status: NodeStatusRunning})
	v3 := store.Apply(StateDelta{NodeID: "a", Status: NodeStatusSuccess, Output: "done"})

	snapV1 := store.SnapshotAt(v1)
	if snapV1["a"].Status != NodeStatusPending {
		t.Fatalf("at v%d expected pending, got %s", v1, snapV1["a"].Status)
	}

	snapV2 := store.SnapshotAt(v2)
	if snapV2["a"].Status != NodeStatusRunning {
		t.Fatalf("at v%d expected running, got %s", v2, snapV2["a"].Status)
	}

	snapV3 := store.SnapshotAt(v3)
	if snapV3["a"].Status != NodeStatusSuccess {
		t.Fatalf("at v%d expected success, got %s", v3, snapV3["a"].Status)
	}
	if snapV3["a"].Output != "done" {
		t.Fatalf("at v%d expected output 'done', got %s", v3, snapV3["a"].Output)
	}
}

func TestStateStore_History(t *testing.T) {
	store := NewStateStore()

	store.Apply(StateDelta{NodeID: "a", Status: NodeStatusPending})
	store.Apply(StateDelta{NodeID: "b", Status: NodeStatusPending})
	store.Apply(StateDelta{NodeID: "a", Status: NodeStatusRunning})
	store.Apply(StateDelta{NodeID: "a", Status: NodeStatusSuccess})

	history := store.History("a")
	if len(history) != 3 {
		t.Fatalf("expected 3 deltas for node 'a', got %d", len(history))
	}
	if history[0].Status != NodeStatusPending {
		t.Fatalf("first delta should be pending, got %s", history[0].Status)
	}
	if history[1].Status != NodeStatusRunning {
		t.Fatalf("second delta should be running, got %s", history[1].Status)
	}
	if history[2].Status != NodeStatusSuccess {
		t.Fatalf("third delta should be success, got %s", history[2].Status)
	}

	historyB := store.History("b")
	if len(historyB) != 1 {
		t.Fatalf("expected 1 delta for node 'b', got %d", len(historyB))
	}

	historyC := store.History("nonexistent")
	if len(historyC) != 0 {
		t.Fatalf("expected 0 deltas for nonexistent, got %d", len(historyC))
	}
}

func TestStateStore_PrevStatus(t *testing.T) {
	store := NewStateStore()

	store.Apply(StateDelta{NodeID: "a", Status: NodeStatusPending})
	store.Apply(StateDelta{NodeID: "a", Status: NodeStatusRunning})
	store.Apply(StateDelta{NodeID: "a", Status: NodeStatusSuccess})

	history := store.History("a")
	if history[0].PrevStatus != "" {
		t.Fatalf("first delta should have no prev status, got %s", history[0].PrevStatus)
	}
	if history[1].PrevStatus != NodeStatusPending {
		t.Fatalf("second delta prev should be pending, got %s", history[1].PrevStatus)
	}
	if history[2].PrevStatus != NodeStatusRunning {
		t.Fatalf("third delta prev should be running, got %s", history[2].PrevStatus)
	}
}

func TestStateStore_CompletedAndSuccessfulNodes(t *testing.T) {
	store := NewStateStore()
	store.Apply(StateDelta{NodeID: "a", Status: NodeStatusSuccess})
	store.Apply(StateDelta{NodeID: "b", Status: NodeStatusFailed, Error: "boom"})
	store.Apply(StateDelta{NodeID: "c", Status: NodeStatusRunning})

	completed := store.CompletedNodes()
	if len(completed) != 2 {
		t.Fatalf("expected 2 completed, got %d", len(completed))
	}

	successful := store.SuccessfulNodes()
	if len(successful) != 1 || successful[0] != "a" {
		t.Fatalf("expected ['a'] successful, got %v", successful)
	}
}

func TestStateStore_Version(t *testing.T) {
	store := NewStateStore()
	if store.Version() != 0 {
		t.Fatalf("expected version 0, got %d", store.Version())
	}

	v := store.Apply(StateDelta{NodeID: "a", Status: NodeStatusPending})
	if v != 1 {
		t.Fatalf("expected version 1, got %d", v)
	}

	v = store.Apply(StateDelta{NodeID: "a", Status: NodeStatusRunning})
	if v != 2 {
		t.Fatalf("expected version 2, got %d", v)
	}

	if store.Version() != 2 {
		t.Fatalf("expected store version 2, got %d", store.Version())
	}
}

func TestStateStore_NodeLatestStatus(t *testing.T) {
	store := NewStateStore()

	if store.NodeLatestStatus("x") != NodeStatusPending {
		t.Fatal("unknown node should return pending")
	}

	store.Apply(StateDelta{NodeID: "x", Status: NodeStatusRunning})
	if store.NodeLatestStatus("x") != NodeStatusRunning {
		t.Fatal("expected running")
	}

	store.Apply(StateDelta{NodeID: "x", Status: NodeStatusSuccess})
	if store.NodeLatestStatus("x") != NodeStatusSuccess {
		t.Fatal("expected success")
	}
}

func TestStateStore_ConcurrentApplyAndSnapshot(t *testing.T) {
	store := NewStateStore()
	const goroutines = 50
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Writers
	for g := 0; g < goroutines; g++ {
		g := g
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				nodeID := string(rune('a'+g%26)) + "_writer"
				store.Apply(StateDelta{
					NodeID: nodeID,
					Status: NodeStatusRunning,
					Output: "some output",
				})
			}
		}()
	}

	// Readers
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				snap := store.Snapshot()
				_ = snap
				_ = store.StatusSnapshot()
				_ = store.CompletedNodes()
			}
		}()
	}

	wg.Wait()

	// Verify the store is consistent after all concurrent operations
	if store.Version() != goroutines*iterations {
		t.Fatalf("expected version %d, got %d", goroutines*iterations, store.Version())
	}
}

func TestStateStore_NewFromDeltas(t *testing.T) {
	deltas := []StateDelta{
		{NodeID: "a", Version: 1, Status: NodeStatusPending, Timestamp: time.Now()},
		{NodeID: "a", Version: 2, Status: NodeStatusSuccess, Output: "done", Timestamp: time.Now()},
		{NodeID: "b", Version: 3, Status: NodeStatusFailed, Error: "oops", Timestamp: time.Now()},
	}

	store := NewStateStoreFromDeltas(deltas)
	if store.Version() != 3 {
		t.Fatalf("expected version 3, got %d", store.Version())
	}

	snap := store.Snapshot()
	if snap["a"].Status != NodeStatusSuccess {
		t.Fatalf("expected a=success, got %s", snap["a"].Status)
	}
	if snap["b"].Status != NodeStatusFailed {
		t.Fatalf("expected b=failed, got %s", snap["b"].Status)
	}
}

func TestStateStore_StatusSnapshot(t *testing.T) {
	store := NewStateStore()
	store.Apply(StateDelta{NodeID: "a", Status: NodeStatusSuccess})
	store.Apply(StateDelta{NodeID: "b", Status: NodeStatusFailed})

	ss := store.StatusSnapshot()
	if ss["a"] != "success" {
		t.Fatalf("expected 'success', got %s", ss["a"])
	}
	if ss["b"] != "failed" {
		t.Fatalf("expected 'failed', got %s", ss["b"])
	}
}

func TestComputeIdempotencyKey(t *testing.T) {
	inputs := map[string]string{"query": "hello", "limit": "10"}

	k1 := ComputeIdempotencyKey("step_1", 0, inputs)
	k2 := ComputeIdempotencyKey("step_1", 0, inputs)
	if k1 != k2 {
		t.Fatalf("same inputs should produce same key: %s != %s", k1, k2)
	}

	k3 := ComputeIdempotencyKey("step_2", 0, inputs)
	if k1 == k3 {
		t.Fatal("different node IDs should produce different keys")
	}

	k4 := ComputeIdempotencyKey("step_1", 1, inputs)
	if k1 == k4 {
		t.Fatal("different attempts should produce different keys")
	}

	different := map[string]string{"query": "world", "limit": "10"}
	k5 := ComputeIdempotencyKey("step_1", 0, different)
	if k1 == k5 {
		t.Fatal("different inputs should produce different keys")
	}
}

func TestStateStore_MarshalDeltas(t *testing.T) {
	store := NewStateStore()
	store.Apply(StateDelta{NodeID: "a", Status: NodeStatusSuccess, Output: "hello"})
	store.Apply(StateDelta{NodeID: "b", Status: NodeStatusFailed, Error: "boom"})

	data, err := store.MarshalDeltas()
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty marshal output")
	}
}
