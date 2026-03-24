package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/Martian-dev/agentops/internal/llm/tracectx"
)

type stubClient struct {
	responses []stubResponse
	calls     int
}

type stubResponse struct {
	text string
	in   int
	out  int
	err  error
}

func (s *stubClient) Complete(ctx context.Context, systemPrompt, userMessage string, temp float32) (string, int, int, error) {
	s.calls++
	if len(s.responses) == 0 {
		return "", 0, 0, errors.New("no response configured")
	}
	resp := s.responses[0]
	if len(s.responses) > 1 {
		s.responses = s.responses[1:]
	}
	return resp.text, resp.in, resp.out, resp.err
}

func TestFallbackClient_PrimarySuccess(t *testing.T) {
	primary := &stubClient{responses: []stubResponse{{text: "ok", in: 10, out: 20}}}
	secondary := &stubClient{responses: []stubResponse{{text: "secondary", in: 1, out: 1}}}
	client := &FallbackClient{Primary: primary, Secondary: secondary}

	text, in, out, err := client.Complete(context.Background(), "sys", "user", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "ok" || in != 10 || out != 20 {
		t.Fatalf("unexpected completion result text=%q in=%d out=%d", text, in, out)
	}
	if primary.calls != 1 {
		t.Fatalf("expected one primary call, got %d", primary.calls)
	}
	if secondary.calls != 0 {
		t.Fatalf("expected zero secondary calls, got %d", secondary.calls)
	}
}

func TestFallbackClient_ServerErrorRetriesThenFallsBack(t *testing.T) {
	primary := &stubClient{responses: []stubResponse{
		{err: &HTTPStatusError{Provider: "openrouter", StatusCode: 500, Body: "internal"}},
		{err: &HTTPStatusError{Provider: "openrouter", StatusCode: 503, Body: "unavailable"}},
	}}
	secondary := &stubClient{responses: []stubResponse{{text: "from-gemini", in: 8, out: 9}}}
	client := &FallbackClient{Primary: primary, Secondary: secondary}

	hookCalled := false
	ctx := tracectx.WithProviderFallbackHook(context.Background(), func(err error) {
		hookCalled = true
	})

	text, _, _, err := client.Complete(ctx, "sys", "user", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "from-gemini" {
		t.Fatalf("unexpected secondary response: %q", text)
	}
	if primary.calls != 2 {
		t.Fatalf("expected two primary calls, got %d", primary.calls)
	}
	if secondary.calls != 1 {
		t.Fatalf("expected one secondary call, got %d", secondary.calls)
	}
	if !hookCalled {
		t.Fatal("expected provider fallback hook to be called")
	}
}

func TestFallbackClient_ClientErrorDoesNotFallback(t *testing.T) {
	primary := &stubClient{responses: []stubResponse{
		{err: &HTTPStatusError{Provider: "openrouter", StatusCode: 400, Body: "bad request"}},
	}}
	secondary := &stubClient{responses: []stubResponse{{text: "from-gemini"}}}
	client := &FallbackClient{Primary: primary, Secondary: secondary}

	_, _, _, err := client.Complete(context.Background(), "sys", "user", 0)
	if err == nil {
		t.Fatal("expected error")
	}
	if primary.calls != 1 {
		t.Fatalf("expected one primary call, got %d", primary.calls)
	}
	if secondary.calls != 0 {
		t.Fatalf("expected zero secondary calls, got %d", secondary.calls)
	}
}

func TestFallbackClient_TooManyRequestsDoesNotFallback(t *testing.T) {
	primary := &stubClient{responses: []stubResponse{
		{err: &HTTPStatusError{Provider: "openrouter", StatusCode: 429, Body: "rate limit"}},
	}}
	secondary := &stubClient{responses: []stubResponse{{text: "from-gemini"}}}
	client := &FallbackClient{Primary: primary, Secondary: secondary}

	_, _, _, err := client.Complete(context.Background(), "sys", "user", 0)
	if err == nil {
		t.Fatal("expected error")
	}
	if primary.calls != 1 {
		t.Fatalf("expected one primary call, got %d", primary.calls)
	}
	if secondary.calls != 0 {
		t.Fatalf("expected zero secondary calls, got %d", secondary.calls)
	}
}

func TestFallbackClient_ServerErrorRetryThenPrimarySuccess(t *testing.T) {
	primary := &stubClient{responses: []stubResponse{
		{err: &HTTPStatusError{Provider: "openrouter", StatusCode: 500, Body: "internal"}},
		{text: "recovered", in: 4, out: 5},
	}}
	secondary := &stubClient{responses: []stubResponse{{text: "from-gemini"}}}
	client := &FallbackClient{Primary: primary, Secondary: secondary}

	text, _, _, err := client.Complete(context.Background(), "sys", "user", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "recovered" {
		t.Fatalf("expected recovered primary response, got %q", text)
	}
	if primary.calls != 2 {
		t.Fatalf("expected two primary calls, got %d", primary.calls)
	}
	if secondary.calls != 0 {
		t.Fatalf("expected zero secondary calls, got %d", secondary.calls)
	}
}

func TestIsServerError_StatusFiltering(t *testing.T) {
	cases := []struct {
		status int
		want   bool
	}{
		{status: 500, want: true},
		{status: 502, want: true},
		{status: 503, want: true},
		{status: 504, want: true},
		{status: 400, want: false},
		{status: 429, want: false},
		{status: 401, want: false},
	}

	for _, tc := range cases {
		err := &HTTPStatusError{Provider: "openrouter", StatusCode: tc.status, Body: "x"}
		if got := IsServerError(err); got != tc.want {
			t.Fatalf("status=%d expected=%t got=%t", tc.status, tc.want, got)
		}
	}
}
