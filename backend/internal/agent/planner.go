package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Martian-dev/agentops/internal/llm"
)

const (
	plannerSystemPrompt = "You are a task planning engine. Given a goal and a list of available tools,\noutput ONLY a valid JSON object matching this schema - no explanation, no\nmarkdown, no preamble:\n\n{\n  \"nodes\": [\n    {\n      \"id\": \"step_1\",\n      \"tool\": \"<tool_name from the list>\",\n      \"inputs\": { \"<key>\": \"<value or $step_N.output>\" },\n      \"depends_on\": []\n    }\n  ]\n}\n\nRules:\n- Use only tools from the provided list.\n- Reference prior step output with \"$step_N.output\" in an input value.\n- A node may only depend on nodes with a lower index.\n- Maximum 10 nodes.\n- If no tools are needed, return {\"nodes\": []}."
)

// DAGNode is one executable node in a DAG plan.
type DAGNode struct {
	ID        string            `json:"id"`
	Tool      string            `json:"tool"`
	Inputs    map[string]string `json:"inputs"`
	DependsOn []string          `json:"depends_on"`
}

// DAGPlan is the planner output.
type DAGPlan struct {
	Nodes []DAGNode `json:"nodes"`
}

// Planner asks an LLM to produce a DAG plan from a goal and tools.
type Planner struct {
	LLMClient llm.LLMClient
}

// PlannerParseError captures JSON parse failures and preserves raw model output.
type PlannerParseError struct {
	Err         error
	RawResponse string
}

func (e *PlannerParseError) Error() string {
	return fmt.Sprintf("failed to parse planner response as DAG JSON: %v; raw_response=%q", e.Err, e.RawResponse)
}

func (e *PlannerParseError) Unwrap() error {
	return e.Err
}

// NewPlannerFromEnv builds a planner from OpenRouter environment variables.
func NewPlannerFromEnv() *Planner {
	return &Planner{
		LLMClient: llm.NewFallbackClientFromEnv(),
	}
}

func (p *Planner) Plan(ctx context.Context, goal string, tools []Tool) (*DAGPlan, error) {
	if p == nil {
		return nil, fmt.Errorf("planner is nil")
	}
	if strings.TrimSpace(goal) == "" {
		return nil, fmt.Errorf("goal is required")
	}
	if p.LLMClient == nil {
		return nil, fmt.Errorf("planner llm client is required")
	}

	type toolPrompt struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema,omitempty"`
	}
	promptTools := make([]toolPrompt, 0, len(tools))
	for _, t := range tools {
		description := ""
		if t.Description != nil {
			description = *t.Description
		}
		promptTools = append(promptTools, toolPrompt{
			Name:        t.Name, 
			Description: description,
			InputSchema: t.InputSchema,
		})
	}

	toolJSON, err := json.Marshal(promptTools)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal available tools: %w", err)
	}

	userContent := fmt.Sprintf("Goal:\n%s\n\nAvailable tools (JSON array):\n%s", goal, string(toolJSON))

	raw, _, _, err := p.LLMClient.Complete(ctx, plannerSystemPrompt, userContent, 0)
	if err != nil {
		return nil, fmt.Errorf("planner request failed: %w", err)
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("planner provider returned empty content")
	}
	raw = stripMarkdownFences(raw)

	var plan DAGPlan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		return nil, &PlannerParseError{Err: err, RawResponse: raw}
	}

	return &plan, nil
}

// stripMarkdownFences removes ```json / ``` wrappers that LLMs sometimes emit
// despite being instructed to return raw JSON.
func stripMarkdownFences(s string) string {
	// Remove opening fence: ```json or ```
	for _, prefix := range []string{"```json", "```"} {
		if strings.HasPrefix(s, prefix) {
			s = strings.TrimPrefix(s, prefix)
			break
		}
	}
	// Remove closing fence
	if idx := strings.LastIndex(s, "```"); idx != -1 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}
