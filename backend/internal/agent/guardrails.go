package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sync/atomic"
)

// --- Token budget guardrail (Step 9b) ---

const defaultMaxTokenBudget = 50000

// checkTokenBudget atomically adds newTokens and returns an error if the
// cumulative total exceeds maxBudget.
func checkTokenBudget(counter *int64, newTokens int, maxBudget int64) error {
	used := atomic.AddInt64(counter, int64(newTokens))
	if used > maxBudget {
		return fmt.Errorf("token budget exceeded: used %d of %d", used, maxBudget)
	}
	return nil
}

// --- Recursion depth guardrail (Step 9c) ---

const maxRecursionDepth = 3

type contextKey string

const recursionDepthKey contextKey = "recursion_depth"

// WithRecursionDepth returns a child context carrying the given recursion depth.
func WithRecursionDepth(ctx context.Context, depth int) context.Context {
	return context.WithValue(ctx, recursionDepthKey, depth)
}

// GetRecursionDepth extracts the current recursion depth from the context.
func GetRecursionDepth(ctx context.Context) int {
	if d, ok := ctx.Value(recursionDepthKey).(int); ok {
		return d
	}
	return 0
}

// --- PII response filter (Step 9d) ---

var piiPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`), // email
	regexp.MustCompile(`\b\d{3}[-.]?\d{3}[-.]?\d{4}\b`),                          // phone
	regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`),                                  // SSN
}

// filterPII replaces common PII patterns (email, phone, SSN) with [REDACTED].
func filterPII(s string) string {
	for _, p := range piiPatterns {
		s = p.ReplaceAllString(s, "[REDACTED]")
	}
	return s
}

// --- Model config parser ---

// ModelConfig holds guardrail-related settings extracted from the agent's
// model_config JSONB column.
type ModelConfig struct {
	MaxTokenBudget  int  `json:"max_token_budget"`
	PIIFilterEnabled bool `json:"pii_filter_enabled"`
}

// ParseModelConfig parses a raw JSON blob into guardrail settings, applying
// sane defaults for missing fields.
func ParseModelConfig(raw json.RawMessage) ModelConfig {
	var cfg ModelConfig
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &cfg)
	}
	if cfg.MaxTokenBudget <= 0 {
		cfg.MaxTokenBudget = defaultMaxTokenBudget
	}
	return cfg
}
