package agent

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCheckpoint_MarshalUnmarshalRoundTrip(t *testing.T) {
	plan := &DAGPlan{Nodes: []DAGNode{
		{ID: "step_1", Tool: "echo", Inputs: map[string]string{"message": "hello"}, DependsOn: []string{}},
		{ID: "step_2", Tool: "concat", Inputs: map[string]string{"text": "$step_1.output"}, DependsOn: []string{"step_1"}},
	}}

	store := NewStateStore()
	store.Apply(StateDelta{NodeID: "step_1", Status: NodeStatusSuccess, Output: `{"output":"hello"}`, IdempotencyKey: "key1"})
	store.Apply(StateDelta{NodeID: "step_2", Status: NodeStatusFailed, Error: "boom"})

	cp, err := NewCheckpoint("run-123", plan, store, 1)
	if err != nil {
		t.Fatalf("failed to create checkpoint: %v", err)
	}

	data, err := cp.Marshal()
	if err != nil {
		t.Fatalf("failed to marshal checkpoint: %v", err)
	}

	restored, err := UnmarshalCheckpoint(data)
	if err != nil {
		t.Fatalf("failed to unmarshal checkpoint: %v", err)
	}

	if restored.RunID != "run-123" {
		t.Fatalf("expected run-123, got %s", restored.RunID)
	}
	if restored.CompletedTiers != 1 {
		t.Fatalf("expected 1 completed tier, got %d", restored.CompletedTiers)
	}
	if len(restored.CompletedNodes) != 1 || restored.CompletedNodes[0] != "step_1" {
		t.Fatalf("expected ['step_1'] completed, got %v", restored.CompletedNodes)
	}
}

func TestCheckpoint_RestoreStateStore(t *testing.T) {
	store := NewStateStore()
	store.Apply(StateDelta{NodeID: "a", Status: NodeStatusSuccess, Output: "done"})
	store.Apply(StateDelta{NodeID: "b", Status: NodeStatusFailed, Error: "oops"})

	plan := &DAGPlan{Nodes: []DAGNode{
		{ID: "a", Tool: "echo"},
		{ID: "b", Tool: "echo"},
	}}

	cp, err := NewCheckpoint("run-1", plan, store, 1)
	if err != nil {
		t.Fatalf("checkpoint creation failed: %v", err)
	}

	restored := cp.RestoreStateStore()
	snap := restored.Snapshot()

	if snap["a"].Status != NodeStatusSuccess {
		t.Fatalf("expected a=success, got %s", snap["a"].Status)
	}
	if snap["a"].Output != "done" {
		t.Fatalf("expected a output='done', got %s", snap["a"].Output)
	}
	if snap["b"].Status != NodeStatusFailed {
		t.Fatalf("expected b=failed, got %s", snap["b"].Status)
	}
}

func TestCheckpoint_RestorePlan(t *testing.T) {
	plan := &DAGPlan{Nodes: []DAGNode{
		{ID: "step_1", Tool: "echo", Inputs: map[string]string{"message": "hello"}},
		{ID: "step_2", Tool: "concat", DependsOn: []string{"step_1"}},
	}}

	store := NewStateStore()
	cp, err := NewCheckpoint("run-1", plan, store, 0)
	if err != nil {
		t.Fatalf("checkpoint creation failed: %v", err)
	}

	restoredPlan, err := cp.RestorePlan()
	if err != nil {
		t.Fatalf("restore plan failed: %v", err)
	}

	if len(restoredPlan.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(restoredPlan.Nodes))
	}
	if restoredPlan.Nodes[0].ID != "step_1" {
		t.Fatalf("expected step_1, got %s", restoredPlan.Nodes[0].ID)
	}
}

func TestCheckpoint_IsNodeCompleted(t *testing.T) {
	cp := &Checkpoint{
		RunID:          "run-1",
		CompletedNodes: []string{"a", "b"},
	}

	if !cp.IsNodeCompleted("a") {
		t.Fatal("expected a to be completed")
	}
	if !cp.IsNodeCompleted("b") {
		t.Fatal("expected b to be completed")
	}
	if cp.IsNodeCompleted("c") {
		t.Fatal("expected c to not be completed")
	}

	// nil checkpoint
	var nilCP *Checkpoint
	if nilCP.IsNodeCompleted("a") {
		t.Fatal("nil checkpoint should return false")
	}
}

func TestCheckpoint_IdempotencyLookup(t *testing.T) {
	store := NewStateStore()
	store.Apply(StateDelta{
		NodeID:         "step_1",
		Status:         NodeStatusSuccess,
		Output:         `{"output":"cached_result"}`,
		IdempotencyKey: "key_abc",
	})
	store.Apply(StateDelta{
		NodeID:         "step_2",
		Status:         NodeStatusFailed,
		Error:          "failed",
		IdempotencyKey: "key_def",
	})

	plan := &DAGPlan{Nodes: []DAGNode{
		{ID: "step_1", Tool: "echo"},
		{ID: "step_2", Tool: "echo"},
	}}

	cp, _ := NewCheckpoint("run-1", plan, store, 1)

	// Should find the successful result
	output, ok := cp.LookupIdempotent("key_abc")
	if !ok {
		t.Fatal("expected key_abc to be found")
	}
	if output != `{"output":"cached_result"}` {
		t.Fatalf("expected cached_result, got %s", output)
	}

	// Failed nodes should not be in idempotency map
	_, ok = cp.LookupIdempotent("key_def")
	if ok {
		t.Fatal("failed node key should not be in idempotency map")
	}

	// Unknown key
	_, ok = cp.LookupIdempotent("nonexistent")
	if !ok {
		// This is expected behavior
	}

	// nil checkpoint
	var nilCP *Checkpoint
	_, ok = nilCP.LookupIdempotent("any")
	if ok {
		t.Fatal("nil checkpoint should return false")
	}
}

func TestCheckpoint_EmptyCheckpoint(t *testing.T) {
	_, err := UnmarshalCheckpoint(nil)
	if err == nil {
		t.Fatal("expected error for nil data")
	}

	_, err = UnmarshalCheckpoint([]byte{})
	if err == nil {
		t.Fatal("expected error for empty data")
	}

	_, err = UnmarshalCheckpoint([]byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}

	_, err = UnmarshalCheckpoint([]byte(`{"completed_nodes":[]}`))
	if err == nil {
		t.Fatal("expected error for missing run_id")
	}
}

func TestCheckpoint_NilStoreHandling(t *testing.T) {
	_, err := NewCheckpoint("run-1", &DAGPlan{}, nil, 0)
	if err == nil {
		t.Fatal("expected error for nil store")
	}
}

func TestCheckpoint_NilRestoreStateStore(t *testing.T) {
	var nilCP *Checkpoint
	store := nilCP.RestoreStateStore()
	if store == nil {
		t.Fatal("nil checkpoint should return empty store")
	}
	if store.Version() != 0 {
		t.Fatal("empty store should have version 0")
	}
}

func TestCheckpoint_CreatedAtIsSet(t *testing.T) {
	store := NewStateStore()
	before := time.Now().UTC()
	cp, _ := NewCheckpoint("run-1", &DAGPlan{}, store, 0)
	after := time.Now().UTC()

	if cp.CreatedAt.Before(before) || cp.CreatedAt.After(after) {
		t.Fatalf("created_at %v not in expected range [%v, %v]", cp.CreatedAt, before, after)
	}
}

func TestExecutionResult_Structure(t *testing.T) {
	result := &ExecutionResult{
		FinalStates: map[string]*NodeState{
			"a": {Status: NodeStatusSuccess, Output: "done"},
		},
		Deltas: []StateDelta{
			{NodeID: "a", Status: NodeStatusPending, Version: 1},
			{NodeID: "a", Status: NodeStatusSuccess, Version: 2, Output: "done"},
		},
		TotalTokens: 42,
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty JSON")
	}
}
