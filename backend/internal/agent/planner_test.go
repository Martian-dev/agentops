package agent

import (
	"context"
	"errors"
	"testing"
)

type plannerLLMStub struct {
	response string
	err      error
	calls    int
}

func (s *plannerLLMStub) Complete(ctx context.Context, systemPrompt, userMessage string, temp float32) (string, int, int, error) {
	s.calls++
	return s.response, 0, 0, s.err
}

func TestPlanner_PlanUsesLLMClient(t *testing.T) {
	stub := &plannerLLMStub{response: `{"nodes":[{"id":"step_1","tool":"echo","inputs":{"message":"hello"},"depends_on":[]}]}`}
	p := &Planner{LLMClient: stub}

	plan, err := p.Plan(context.Background(), "say hello", []Tool{{Name: "echo"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stub.calls != 1 {
		t.Fatalf("expected one llm call, got %d", stub.calls)
	}
	if len(plan.Nodes) != 1 || plan.Nodes[0].Tool != "echo" {
		t.Fatalf("unexpected plan: %+v", plan)
	}
}

func TestPlanner_PlanParseErrorPreservesRaw(t *testing.T) {
	stub := &plannerLLMStub{response: `not-json`}
	p := &Planner{LLMClient: stub}

	_, err := p.Plan(context.Background(), "say hello", []Tool{{Name: "echo"}})
	if err == nil {
		t.Fatal("expected parse error")
	}
	var parseErr *PlannerParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected PlannerParseError, got %T", err)
	}
	if parseErr.RawResponse != "not-json" {
		t.Fatalf("expected raw response to be preserved, got %q", parseErr.RawResponse)
	}
}
