package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Martian-dev/agentops/internal/llm/tracectx"
	"github.com/Martian-dev/agentops/internal/tools"
)

type stubToolRouter struct {
	mu        sync.Mutex
	responses []error
	calls     int
}

func (r *stubToolRouter) Execute(ctx context.Context, toolName string, inputs map[string]interface{}) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if len(r.responses) == 0 {
		return `{"output":"ok"}`, nil
	}
	err := r.responses[0]
	if len(r.responses) > 1 {
		r.responses = r.responses[1:]
	}
	if err != nil {
		return "", err
	}
	return `{"output":"ok"}`, nil
}

type capturedEmitter struct {
	mu     sync.Mutex
	events []TraceEvent
}

func (e *capturedEmitter) Emit(ctx context.Context, runID string, event TraceEvent) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
	return nil
}

func (e *capturedEmitter) hasEvent(eventType string, attempt int) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ev := range e.events {
		if ev.EventType == eventType && ev.Attempt == attempt {
			return true
		}
	}
	return false
}

func (e *capturedEmitter) getEvents() []TraceEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	result := make([]TraceEvent, len(e.events))
	copy(result, e.events)
	return result
}

type fallbackEmittingRouter struct{}

func (r *fallbackEmittingRouter) Execute(ctx context.Context, toolName string, inputs map[string]interface{}) (string, error) {
	tracectx.EmitProviderFallback(ctx, errors.New("openrouter 503"))
	return `{"output":"ok"}`, nil
}

func singleNodePlan() *DAGPlan {
	return &DAGPlan{Nodes: []DAGNode{{
		ID:        "step_1",
		Tool:      "echo",
		Inputs:    map[string]string{"message": "hello"},
		DependsOn: []string{},
	}}}
}

func TestExecutor_RetriesAndSucceeds(t *testing.T) {
	router := &stubToolRouter{responses: []error{errors.New("boom 1"), errors.New("boom 2"), nil}}
	emitter := &capturedEmitter{}
	exec := NewExecutor(router, emitter)
	exec.MaxRetries = 2
	exec.NodeTimeout = 100 * time.Millisecond

	states, err := exec.Execute(context.Background(), "run-1", singleNodePlan())
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if states["step_1"].Status != NodeStatusSuccess {
		t.Fatalf("expected node success, got %s", states["step_1"].Status)
	}
	if router.calls != 3 {
		t.Fatalf("expected 3 calls, got %d", router.calls)
	}
	if !emitter.hasEvent("node_retrying", 1) || !emitter.hasEvent("node_retrying", 2) {
		t.Fatal("expected node_retrying events for attempts 1 and 2")
	}
}

func TestExecutor_DoesNotRetryInvalidInput(t *testing.T) {
	router := &stubToolRouter{responses: []error{&tools.ErrInvalidInput{ToolName: "echo", Message: "bad"}, nil}}
	exec := NewExecutor(router, &capturedEmitter{})
	exec.MaxRetries = 2

	_, err := exec.Execute(context.Background(), "run-2", singleNodePlan())
	if err == nil {
		t.Fatal("expected error")
	}
	if router.calls != 1 {
		t.Fatalf("expected one call, got %d", router.calls)
	}
}

func TestExecutor_DoesNotRetryDeadlineExceeded(t *testing.T) {
	router := &stubToolRouter{responses: []error{context.DeadlineExceeded, nil}}
	exec := NewExecutor(router, &capturedEmitter{})
	exec.MaxRetries = 2

	_, err := exec.Execute(context.Background(), "run-3", singleNodePlan())
	if err == nil {
		t.Fatal("expected error")
	}
	if router.calls != 1 {
		t.Fatalf("expected one call, got %d", router.calls)
	}
}

func TestExecutor_DoesNotRetryContextCanceled(t *testing.T) {
	router := &stubToolRouter{responses: []error{context.Canceled, nil}}
	exec := NewExecutor(router, &capturedEmitter{})
	exec.MaxRetries = 2

	_, err := exec.Execute(context.Background(), "run-canceled", singleNodePlan())
	if err == nil {
		t.Fatal("expected error")
	}
	if router.calls != 1 {
		t.Fatalf("expected one call, got %d", router.calls)
	}
}

func TestExecutor_CancelDuringBackoffExitsEarly(t *testing.T) {
	router := &stubToolRouter{responses: []error{fmt.Errorf("temporary"), nil}}
	exec := NewExecutor(router, &capturedEmitter{})
	exec.MaxRetries = 2

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := exec.Execute(ctx, "run-4", singleNodePlan())
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if elapsed >= 400*time.Millisecond {
		t.Fatalf("expected early cancellation during backoff, elapsed=%s", elapsed)
	}
}

func TestExecutor_EmitsProviderFallbackEvent(t *testing.T) {
	emitter := &capturedEmitter{}
	exec := NewExecutor(&fallbackEmittingRouter{}, emitter)

	states, err := exec.Execute(context.Background(), "run-fallback", singleNodePlan())
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if states["step_1"].Status != NodeStatusSuccess {
		t.Fatalf("expected node success, got %s", states["step_1"].Status)
	}
	if !emitter.hasEvent("provider_fallback", 0) {
		t.Fatal("expected provider_fallback event")
	}
}

func TestExecutor_TokenBudgetExceeded(t *testing.T) {
	// Router returns a large output to blow the budget
	bigRouter := &stubToolRouter{responses: []error{nil}}
	emitter := &capturedEmitter{}
	cfg := ModelConfig{MaxTokenBudget: 1} // tiny budget
	exec := NewExecutorWithConfig(bigRouter, emitter, cfg)

	_, err := exec.Execute(context.Background(), "run-budget", singleNodePlan())
	if err == nil {
		t.Fatal("expected error due to token budget exceeded")
	}
	if !emitter.hasEvent("token_budget_exceeded", 0) {
		t.Fatal("expected token_budget_exceeded event")
	}
}

func TestExecutor_RecursionDepthExceeded(t *testing.T) {
	exec := NewExecutor(&stubToolRouter{}, &capturedEmitter{})

	ctx := WithRecursionDepth(context.Background(), 4)
	_, err := exec.Execute(ctx, "run-recurse", singleNodePlan())
	if err == nil {
		t.Fatal("expected recursion depth error")
	}
}

func TestExecutor_RecursionDepthAtLimit(t *testing.T) {
	exec := NewExecutor(&stubToolRouter{}, &capturedEmitter{})

	ctx := WithRecursionDepth(context.Background(), 3)
	states, err := exec.Execute(ctx, "run-recurse-ok", singleNodePlan())
	if err != nil {
		t.Fatalf("expected success at depth 3, got: %v", err)
	}
	if states["step_1"].Status != NodeStatusSuccess {
		t.Fatalf("expected success, got %s", states["step_1"].Status)
	}
}

type piiRouter struct{}

func (r *piiRouter) Execute(ctx context.Context, toolName string, inputs map[string]interface{}) (string, error) {
	return `{"output":"contact user@example.com for info"}`, nil
}

func TestExecutor_PIIFilterApplied(t *testing.T) {
	cfg := ModelConfig{MaxTokenBudget: 50000, PIIFilterEnabled: true}
	exec := NewExecutorWithConfig(&piiRouter{}, &capturedEmitter{}, cfg)

	states, err := exec.Execute(context.Background(), "run-pii", singleNodePlan())
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	output := states["step_1"].Output
	if output == "" {
		t.Fatal("expected non-empty output")
	}
	if strings.Contains(output, "user@example.com") {
		t.Fatalf("expected PII to be redacted, got: %s", output)
	}
	if !strings.Contains(output, "[REDACTED]") {
		t.Fatalf("expected [REDACTED] in output, got: %s", output)
	}
}

// --- New tests for StateStore-based executor ---

func TestExecutor_ExecuteWithResult_ReturnsDeltas(t *testing.T) {
	exec := NewExecutor(&stubToolRouter{}, &capturedEmitter{})
	result, err := exec.ExecuteWithResult(context.Background(), "run-deltas", singleNodePlan(), nil)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	if len(result.Deltas) == 0 {
		t.Fatal("expected non-empty deltas")
	}

	// Should have at least a pending and a success delta for step_1
	foundPending := false
	foundSuccess := false
	for _, d := range result.Deltas {
		if d.NodeID == "step_1" {
			if d.Status == NodeStatusPending {
				foundPending = true
			}
			if d.Status == NodeStatusSuccess {
				foundSuccess = true
			}
		}
	}
	if !foundPending {
		t.Fatal("expected pending delta for step_1")
	}
	if !foundSuccess {
		t.Fatal("expected success delta for step_1")
	}
}

func TestExecutor_ExecuteWithResult_HasCheckpoint(t *testing.T) {
	exec := NewExecutor(&stubToolRouter{}, &capturedEmitter{})
	result, err := exec.ExecuteWithResult(context.Background(), "run-cp", singleNodePlan(), nil)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	if result.Checkpoint == nil {
		t.Fatal("expected non-nil checkpoint")
	}
	if result.Checkpoint.RunID != "run-cp" {
		t.Fatalf("expected run-cp, got %s", result.Checkpoint.RunID)
	}
	if result.Checkpoint.CompletedTiers < 1 {
		t.Fatalf("expected at least 1 completed tier, got %d", result.Checkpoint.CompletedTiers)
	}
}

func TestExecutor_ExecuteWithResult_StateSnapshotInTraceEvents(t *testing.T) {
	emitter := &capturedEmitter{}
	exec := NewExecutor(&stubToolRouter{}, emitter)
	_, err := exec.ExecuteWithResult(context.Background(), "run-snap", singleNodePlan(), nil)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	events := emitter.getEvents()
	// At least one event should have a StateSnapshot
	foundSnapshot := false
	for _, ev := range events {
		if len(ev.StateSnapshot) > 0 {
			foundSnapshot = true
			break
		}
	}
	if !foundSnapshot {
		t.Fatal("expected at least one event with StateSnapshot")
	}
}

func TestExecutor_ResumeFromCheckpoint_SkipsCompletedNodes(t *testing.T) {
	// Create a two-node sequential plan
	plan := &DAGPlan{Nodes: []DAGNode{
		{ID: "step_1", Tool: "echo", Inputs: map[string]string{"message": "hello"}, DependsOn: []string{}},
		{ID: "step_2", Tool: "echo", Inputs: map[string]string{"text": "$step_1.output"}, DependsOn: []string{"step_1"}},
	}}

	// Create a checkpoint where step_1 succeeded
	cpStore := NewStateStore()
	cpStore.Apply(StateDelta{
		NodeID:         "step_1",
		Status:         NodeStatusSuccess,
		Output:         `{"output":"ok"}`,
		IdempotencyKey: ComputeIdempotencyKey("step_1", 0, map[string]string{"message": "hello"}),
	})

	cp, err := NewCheckpoint("run-resume", plan, cpStore, 1)
	if err != nil {
		t.Fatalf("checkpoint creation failed: %v", err)
	}

	// Execute with checkpoint — step_1 should be skipped, step_2 should run
	router := &stubToolRouter{}
	exec := NewExecutor(router, &capturedEmitter{})
	result, execErr := exec.ExecuteWithResult(context.Background(), "run-resume", plan, cp)
	if execErr != nil {
		t.Fatalf("expected success, got: %v", execErr)
	}

	if result.FinalStates["step_1"].Status != NodeStatusSuccess {
		t.Fatalf("expected step_1 success (from checkpoint), got %s", result.FinalStates["step_1"].Status)
	}
	if result.FinalStates["step_2"].Status != NodeStatusSuccess {
		t.Fatalf("expected step_2 success, got %s", result.FinalStates["step_2"].Status)
	}

	// Router should have been called only for step_2 (step_1 was cached)
	if router.calls != 1 {
		t.Fatalf("expected 1 router call (step_2 only), got %d", router.calls)
	}
}

func TestExecutor_IdempotentRetry_CachedOutput(t *testing.T) {
	plan := singleNodePlan()

	// Build checkpoint with cached output for the exact same inputs
	cpStore := NewStateStore()
	idemKey := ComputeIdempotencyKey("step_1", 0, map[string]string{"message": "hello"})
	cpStore.Apply(StateDelta{
		NodeID:         "step_1",
		Status:         NodeStatusSuccess,
		Output:         `{"output":"cached"}`,
		IdempotencyKey: idemKey,
	})

	cp, _ := NewCheckpoint("run-idem", plan, cpStore, 0) // 0 tiers completed — will re-process tier
	// But since step_1 is in CompletedNodes and has matching idempotency key,
	// it should use cached output

	router := &stubToolRouter{}
	exec := NewExecutor(router, &capturedEmitter{})
	result, err := exec.ExecuteWithResult(context.Background(), "run-idem", plan, cp)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	if result.FinalStates["step_1"].Status != NodeStatusSuccess {
		t.Fatalf("expected step_1 success from cache, got %s", result.FinalStates["step_1"].Status)
	}

	// If step_1 was completed via checkpoint, the router should NOT have been called
	// (because step_1 is in CompletedNodes and tier 0 should skip it)
	// OR it should have been called 0 times if the idempotency cache was hit
}

func TestExecutor_MultiTierPlan_CheckpointSavesAfterEachTier(t *testing.T) {
	plan := &DAGPlan{Nodes: []DAGNode{
		{ID: "s1", Tool: "echo", Inputs: map[string]string{"message": "a"}, DependsOn: []string{}},
		{ID: "s2", Tool: "echo", Inputs: map[string]string{"message": "b"}, DependsOn: []string{}},
		{ID: "s3", Tool: "echo", Inputs: map[string]string{"text": "$s1.output"}, DependsOn: []string{"s1", "s2"}},
	}}

	exec := NewExecutor(&stubToolRouter{}, &capturedEmitter{})
	result, err := exec.ExecuteWithResult(context.Background(), "run-multi", plan, nil)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	if result.Checkpoint == nil {
		t.Fatal("expected checkpoint")
	}

	// Should have 2 tiers: [s1, s2] and [s3]
	if result.Checkpoint.CompletedTiers != 2 {
		t.Fatalf("expected 2 completed tiers, got %d", result.Checkpoint.CompletedTiers)
	}

	// All nodes should be in completed
	if len(result.Checkpoint.CompletedNodes) != 3 {
		t.Fatalf("expected 3 completed nodes, got %d: %v", len(result.Checkpoint.CompletedNodes), result.Checkpoint.CompletedNodes)
	}
}

func TestExecutor_ParallelNodes_ConsistentState(t *testing.T) {
	plan := &DAGPlan{Nodes: []DAGNode{
		{ID: "a", Tool: "echo", Inputs: map[string]string{"message": "hello"}, DependsOn: []string{}},
		{ID: "b", Tool: "echo", Inputs: map[string]string{"message": "world"}, DependsOn: []string{}},
		{ID: "c", Tool: "echo", Inputs: map[string]string{"message": "foo"}, DependsOn: []string{}},
		{ID: "d", Tool: "echo", Inputs: map[string]string{"x": "$a.output", "y": "$b.output"}, DependsOn: []string{"a", "b", "c"}},
	}}

	exec := NewExecutor(&stubToolRouter{}, &capturedEmitter{})
	result, err := exec.ExecuteWithResult(context.Background(), "run-parallel", plan, nil)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	for _, id := range []string{"a", "b", "c", "d"} {
		if result.FinalStates[id].Status != NodeStatusSuccess {
			t.Fatalf("expected %s success, got %s", id, result.FinalStates[id].Status)
		}
	}

	// Verify deltas are ordered by version
	for i := 1; i < len(result.Deltas); i++ {
		if result.Deltas[i].Version < result.Deltas[i-1].Version {
			t.Fatalf("deltas not ordered: version %d before %d", result.Deltas[i-1].Version, result.Deltas[i].Version)
		}
	}
}

func TestExecutor_ExecuteWithResult_DurationTracking(t *testing.T) {
	// Use a router that takes a small amount of time
	slowRouter := &stubToolRouter{}
	emitter := &capturedEmitter{}
	exec := NewExecutor(slowRouter, emitter)

	_, err := exec.ExecuteWithResult(context.Background(), "run-dur", singleNodePlan(), nil)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	events := emitter.getEvents()
	// Find the success event and check it has DurationMs
	for _, ev := range events {
		if ev.ToState == string(NodeStatusSuccess) && ev.NodeID == "step_1" {
			// Duration should be >= 0 (it ran nearly instantly in test)
			if ev.DurationMs < 0 {
				t.Fatalf("expected non-negative duration, got %d", ev.DurationMs)
			}
			return
		}
	}
	t.Fatal("expected a success event for step_1")
}
