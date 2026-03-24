package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeLLMClient struct {
	response string
	err      error
}

func (f *fakeLLMClient) Complete(ctx context.Context, systemPrompt, userMessage string, temp float32) (string, int, int, error) {
	return f.response, 0, 0, f.err
}

func TestValidateInput_ErrorIncludesFieldPath(t *testing.T) {
	tool := &Tool{
		Name: "sum",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"a": {"type": "number"}
			},
			"required": ["a"]
		}`),
	}

	err := validateInput(tool, map[string]interface{}{"a": "not-a-number"})
	if err == nil {
		t.Fatal("expected validation error")
	}

	inv, ok := err.(*ErrInvalidInput)
	if !ok {
		t.Fatalf("expected ErrInvalidInput, got %T", err)
	}
	if !strings.Contains(inv.Error(), "/a") {
		t.Fatalf("expected field path in error, got: %s", inv.Error())
	}
}

func TestDispatchInternal_CallsRegisteredHandler(t *testing.T) {
	r := NewRouter(nil, map[string]ToolHandlerFunc{
		"echo": func(ctx context.Context, inputs map[string]interface{}) (interface{}, error) {
			return map[string]interface{}{"ok": inputs["msg"]}, nil
		},
	})

	tool := &Tool{Name: "echo", HandlerType: "internal"}
	out, err := r.dispatchInternal(context.Background(), tool, map[string]interface{}{"msg": "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m, ok := out.(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected output type: %T", out)
	}
	if m["ok"] != "hello" {
		t.Fatalf("unexpected output value: %#v", m)
	}
}

func TestDispatchHTTP_PostsJSONAndParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", req.Method)
		}
		if got := req.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("expected json content type, got %s", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	r := NewRouter(nil, nil)
	tool := &Tool{
		Name:          "remote_tool",
		HandlerType:   "http",
		HandlerConfig: json.RawMessage(`{"url":"` + srv.URL + `"}`),
	}

	out, err := r.dispatchHTTP(context.Background(), tool, map[string]interface{}{"a": "b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m, ok := out.(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected output type: %T", out)
	}
	if m["status"] != "ok" {
		t.Fatalf("unexpected output payload: %#v", m)
	}
}

func TestDispatchLLM_ReturnsOutputEnvelope(t *testing.T) {
	r := NewRouter(nil, nil)
	r.SetLLMClient(&fakeLLMClient{response: "hello from llm"})
	tool := &Tool{
		Name:        "llm_tool",
		HandlerType: "llm",
		HandlerConfig: json.RawMessage(`{
			"system_prompt": "You are a formatter"
		}`),
	}

	out, err := r.dispatchLLM(context.Background(), tool, map[string]interface{}{"x": 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m, ok := out.(map[string]string)
	if !ok {
		t.Fatalf("unexpected output type: %T", out)
	}
	if m["output"] != "hello from llm" {
		t.Fatalf("unexpected llm output: %#v", m)
	}
}

func TestDispatchLLM_PropagatesClientError(t *testing.T) {
	r := NewRouter(nil, nil)
	r.SetLLMClient(&fakeLLMClient{err: errors.New("provider down")})
	tool := &Tool{
		Name:        "llm_tool",
		HandlerType: "llm",
		HandlerConfig: json.RawMessage(`{
			"system_prompt": "You are a formatter"
		}`),
	}

	_, err := r.dispatchLLM(context.Background(), tool, map[string]interface{}{"x": 1})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "provider down") {
		t.Fatalf("expected wrapped provider error, got: %v", err)
	}
}
