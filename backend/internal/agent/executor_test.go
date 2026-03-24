package agent

import (
	"context"
	"errors"
	"fmt"
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
