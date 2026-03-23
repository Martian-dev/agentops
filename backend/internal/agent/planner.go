package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	defaultPlannerModel  = "google/gemini-2.0-flash-001"
	defaultOpenRouterURL = "https://openrouter.ai/api/v1/chat/completions"
	plannerSystemPrompt  = "You are a task planning engine. Given a goal and a list of available tools,\noutput ONLY a valid JSON object matching this schema - no explanation, no\nmarkdown, no preamble:\n\n{\n  \"nodes\": [\n    {\n      \"id\": \"step_1\",\n      \"tool\": \"<tool_name from the list>\",\n      \"inputs\": { \"<key>\": \"<value or $step_N.output>\" },\n      \"depends_on\": []\n    }\n  ]\n}\n\nRules:\n- Use only tools from the provided list.\n- Reference prior step output with \"$step_N.output\" in an input value.\n- A node may only depend on nodes with a lower index.\n- Maximum 10 nodes.\n- If no tools are needed, return {\"nodes\": []}."
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
	APIKey     string
	Model      string
	BaseURL    string
	HTTPClient *http.Client
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
		APIKey:  os.Getenv("OPENROUTER_API_KEY"),
		Model:   os.Getenv("OPENROUTER_MODEL"),
		BaseURL: os.Getenv("OPENROUTER_BASE_URL"),
		HTTPClient: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

func (p *Planner) Plan(ctx context.Context, goal string, tools []Tool) (*DAGPlan, error) {
	if p == nil {
		return nil, fmt.Errorf("planner is nil")
	}
	if strings.TrimSpace(goal) == "" {
		return nil, fmt.Errorf("goal is required")
	}
	if strings.TrimSpace(p.APIKey) == "" {
		return nil, fmt.Errorf("OPENROUTER_API_KEY is required")
	}

	model := strings.TrimSpace(p.Model)
	if model == "" {
		model = defaultPlannerModel
	}
	baseURL := strings.TrimSpace(p.BaseURL)
	if baseURL == "" {
		baseURL = defaultOpenRouterURL
	}
	httpClient := p.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}

	type toolPrompt struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	promptTools := make([]toolPrompt, 0, len(tools))
	for _, t := range tools {
		description := ""
		if t.Description != nil {
			description = *t.Description
		}
		promptTools = append(promptTools, toolPrompt{Name: t.Name, Description: description})
	}

	toolJSON, err := json.Marshal(promptTools)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal available tools: %w", err)
	}

	userContent := fmt.Sprintf("Goal:\n%s\n\nAvailable tools (JSON array):\n%s", goal, string(toolJSON))

	type chatMessage struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type requestBody struct {
		Model       string        `json:"model"`
		Temperature float64       `json:"temperature"`
		Messages    []chatMessage `json:"messages"`
	}
	body, err := json.Marshal(requestBody{
		Model:       model,
		Temperature: 0,
		Messages: []chatMessage{
			{Role: "system", Content: plannerSystemPrompt},
			{Role: "user", Content: userContent},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build planner request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create planner HTTP request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("planner request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read planner response: %w", err)
	}

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("planner request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBytes)))
	}

	type chatResponse struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	var parsed chatResponse
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse planner provider response envelope: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("planner provider returned no choices")
	}

	raw := strings.TrimSpace(parsed.Choices[0].Message.Content)
	if raw == "" {
		return nil, fmt.Errorf("planner provider returned empty content")
	}

	var plan DAGPlan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		return nil, &PlannerParseError{Err: err, RawResponse: raw}
	}

	return &plan, nil
}
