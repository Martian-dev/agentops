package agent

import (
	"context"
	"encoding/json"
	"testing"
)

func TestCheckTokenBudget_UnderLimit(t *testing.T) {
	var counter int64
	if err := checkTokenBudget(&counter, 100, 50000); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if counter != 100 {
		t.Fatalf("expected counter=100, got %d", counter)
	}
}

func TestCheckTokenBudget_OverLimit(t *testing.T) {
	var counter int64
	counter = 49990
	err := checkTokenBudget(&counter, 20, 50000)
	if err == nil {
		t.Fatal("expected error when exceeding budget")
	}
}

func TestCheckTokenBudget_ExactLimit(t *testing.T) {
	var counter int64
	if err := checkTokenBudget(&counter, 50000, 50000); err != nil {
		t.Fatalf("expected no error at exact limit, got: %v", err)
	}
}

func TestRecursionDepth_ContextRoundtrip(t *testing.T) {
	ctx := context.Background()
	if d := GetRecursionDepth(ctx); d != 0 {
		t.Fatalf("expected default depth 0, got %d", d)
	}

	ctx = WithRecursionDepth(ctx, 2)
	if d := GetRecursionDepth(ctx); d != 2 {
		t.Fatalf("expected depth 2, got %d", d)
	}

	ctx = WithRecursionDepth(ctx, 4)
	if d := GetRecursionDepth(ctx); d != 4 {
		t.Fatalf("expected depth 4, got %d", d)
	}
}

func TestFilterPII_RedactsEmail(t *testing.T) {
	input := "Contact john.doe@example.com for details"
	got := filterPII(input)
	expected := "Contact [REDACTED] for details"
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestFilterPII_RedactsPhone(t *testing.T) {
	input := "Call 555-123-4567 now"
	got := filterPII(input)
	expected := "Call [REDACTED] now"
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestFilterPII_RedactsSSN(t *testing.T) {
	input := "SSN is 123-45-6789"
	got := filterPII(input)
	expected := "SSN is [REDACTED]"
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestFilterPII_PreservesCleanText(t *testing.T) {
	input := "Hello world, this is a normal message"
	got := filterPII(input)
	if got != input {
		t.Fatalf("expected no changes, got %q", got)
	}
}

func TestFilterPII_MultiplePatterns(t *testing.T) {
	input := "Email: a@b.com Phone: 555.123.4567 SSN: 111-22-3333"
	got := filterPII(input)
	expected := "Email: [REDACTED] Phone: [REDACTED] SSN: [REDACTED]"
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestParseModelConfig_Defaults(t *testing.T) {
	cfg := ParseModelConfig(json.RawMessage(`{}`))
	if cfg.MaxTokenBudget != defaultMaxTokenBudget {
		t.Fatalf("expected default budget %d, got %d", defaultMaxTokenBudget, cfg.MaxTokenBudget)
	}
	if cfg.PIIFilterEnabled {
		t.Fatal("expected PII filter disabled by default")
	}
}

func TestParseModelConfig_CustomValues(t *testing.T) {
	raw := json.RawMessage(`{"max_token_budget": 10000, "pii_filter_enabled": true}`)
	cfg := ParseModelConfig(raw)
	if cfg.MaxTokenBudget != 10000 {
		t.Fatalf("expected budget 10000, got %d", cfg.MaxTokenBudget)
	}
	if !cfg.PIIFilterEnabled {
		t.Fatal("expected PII filter enabled")
	}
}

func TestParseModelConfig_NilInput(t *testing.T) {
	cfg := ParseModelConfig(nil)
	if cfg.MaxTokenBudget != defaultMaxTokenBudget {
		t.Fatalf("expected default budget, got %d", cfg.MaxTokenBudget)
	}
}
