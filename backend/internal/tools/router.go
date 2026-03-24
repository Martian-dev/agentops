package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Martian-dev/agentops/internal/llm"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

const (
	defaultRouterHTTPTimeout = 25 * time.Second
)

// Router resolves tools from the registry and dispatches execution.
type Router struct {
	pool             *pgxpool.Pool
	internalHandlers map[string]ToolHandlerFunc
	httpClient       *http.Client
	llmClient        llm.LLMClient
}

func NewRouter(pool *pgxpool.Pool, internalHandlers map[string]ToolHandlerFunc) *Router {
	handlerCopy := make(map[string]ToolHandlerFunc, len(internalHandlers))
	for name, fn := range internalHandlers {
		handlerCopy[name] = fn
	}

	return &Router{
		pool:             pool,
		internalHandlers: handlerCopy,
		httpClient:       &http.Client{Timeout: defaultRouterHTTPTimeout},
		llmClient:        llm.NewFallbackClientFromEnv(),
	}
}

// SetLLMClient overrides the default fallback client, primarily for tests.
func (r *Router) SetLLMClient(client llm.LLMClient) {
	if r == nil {
		return
	}
	r.llmClient = client
}

// Register adds or replaces an internal tool handler by tool name.
func (r *Router) Register(toolName string, handler ToolHandlerFunc) {
	if r == nil {
		return
	}
	toolName = strings.TrimSpace(toolName)
	if toolName == "" || handler == nil {
		return
	}
	if r.internalHandlers == nil {
		r.internalHandlers = make(map[string]ToolHandlerFunc)
	}
	r.internalHandlers[toolName] = handler
}

// Lookup resolves a tool row by name.
func (r *Router) Lookup(ctx context.Context, toolName string) (*Tool, error) {
	if r == nil {
		return nil, fmt.Errorf("tool router is nil")
	}
	if r.pool == nil {
		return nil, fmt.Errorf("database pool not initialized")
	}
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return nil, fmt.Errorf("tool name is required")
	}

	row := r.pool.QueryRow(ctx, `
		SELECT
			id::text,
			name,
			description,
			COALESCE(input_schema, '{}'::jsonb)::text,
			COALESCE(output_schema, '{}'::jsonb)::text,
			COALESCE(handler_type, ''),
			COALESCE(handler_config, '{}'::jsonb)::text,
			created_at
		FROM tools
		WHERE name = $1
		LIMIT 1
	`, toolName)

	var tool Tool
	var inputSchemaText string
	var outputSchemaText string
	var handlerConfigText string
	if err := row.Scan(
		&tool.ID,
		&tool.Name,
		&tool.Description,
		&inputSchemaText,
		&outputSchemaText,
		&tool.HandlerType,
		&handlerConfigText,
		&tool.CreatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &ErrToolNotFound{ToolName: toolName}
		}
		return nil, fmt.Errorf("lookup tool %s: %w", toolName, err)
	}

	tool.InputSchema = json.RawMessage(inputSchemaText)
	tool.OutputSchema = json.RawMessage(outputSchemaText)
	tool.HandlerConfig = json.RawMessage(handlerConfigText)

	return &tool, nil
}

// Execute resolves, validates, and dispatches a tool call.
func (r *Router) Execute(ctx context.Context, toolName string, inputs map[string]interface{}) (string, error) {
	result, err := r.ExecuteRaw(ctx, toolName, inputs)
	if err != nil {
		return "", err
	}

	resultBytes, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshal tool output for %s: %w", toolName, err)
	}

	return string(resultBytes), nil
}

// ExecuteRaw resolves, validates, and dispatches a tool call, returning typed output.
func (r *Router) ExecuteRaw(ctx context.Context, toolName string, inputs map[string]interface{}) (interface{}, error) {
	tool, err := r.Lookup(ctx, toolName)
	if err != nil {
		return nil, err
	}

	if inputs == nil {
		inputs = make(map[string]interface{})
	}

	if err := validateInput(tool, inputs); err != nil {
		return nil, err
	}

	result, err := r.dispatch(ctx, tool, inputs)
	if err != nil {
		return nil, err
	}

	if err := validateOutput(tool, result); err != nil {
		return nil, err
	}

	return result, nil
}

func (r *Router) dispatch(ctx context.Context, tool *Tool, inputs map[string]interface{}) (interface{}, error) {
	switch tool.HandlerType {
	case "internal":
		return r.dispatchInternal(ctx, tool, inputs)
	case "http":
		return r.dispatchHTTP(ctx, tool, inputs)
	case "llm":
		return r.dispatchLLM(ctx, tool, inputs)
	default:
		return nil, fmt.Errorf("unsupported handler_type=%q for tool=%s", tool.HandlerType, tool.Name)
	}
}

func (r *Router) dispatchInternal(ctx context.Context, tool *Tool, inputs map[string]interface{}) (interface{}, error) {
	handler, ok := r.internalHandlers[tool.Name]
	if !ok {
		return nil, fmt.Errorf("missing internal handler for tool=%s", tool.Name)
	}
	result, err := handler(ctx, inputs)
	if err != nil {
		return nil, fmt.Errorf("internal handler failed for tool=%s: %w", tool.Name, err)
	}
	return result, nil
}

func (r *Router) dispatchHTTP(ctx context.Context, tool *Tool, inputs map[string]interface{}) (interface{}, error) {
	var cfg struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(tool.HandlerConfig, &cfg); err != nil {
		return nil, fmt.Errorf("invalid handler_config for tool=%s: %w", tool.Name, err)
	}
	cfg.URL = strings.TrimSpace(cfg.URL)
	if cfg.URL == "" {
		return nil, fmt.Errorf("handler_config.url is required for tool=%s", tool.Name)
	}

	body, err := json.Marshal(inputs)
	if err != nil {
		return nil, fmt.Errorf("marshal HTTP inputs for tool=%s: %w", tool.Name, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build HTTP request for tool=%s: %w", tool.Name, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP handler failed for tool=%s: %w", tool.Name, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read HTTP response for tool=%s: %w", tool.Name, err)
	}

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP handler returned status=%d for tool=%s body=%s", resp.StatusCode, tool.Name, strings.TrimSpace(string(respBody)))
	}

	var parsed interface{}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("HTTP handler returned non-JSON response for tool=%s: %w", tool.Name, err)
	}

	return parsed, nil
}

func (r *Router) dispatchLLM(ctx context.Context, tool *Tool, inputs map[string]interface{}) (interface{}, error) {
	var cfg struct {
		SystemPrompt string `json:"system_prompt"`
	}
	if err := json.Unmarshal(tool.HandlerConfig, &cfg); err != nil {
		return nil, fmt.Errorf("invalid handler_config for tool=%s: %w", tool.Name, err)
	}
	cfg.SystemPrompt = strings.TrimSpace(cfg.SystemPrompt)
	if cfg.SystemPrompt == "" {
		return nil, fmt.Errorf("handler_config.system_prompt is required for tool=%s", tool.Name)
	}
	if r.llmClient == nil {
		return nil, fmt.Errorf("llm client is required for tool=%s", tool.Name)
	}

	inputJSON, err := json.Marshal(inputs)
	if err != nil {
		return nil, fmt.Errorf("marshal llm inputs for tool=%s: %w", tool.Name, err)
	}

	output, _, _, err := r.llmClient.Complete(ctx, cfg.SystemPrompt, string(inputJSON), 0)
	if err != nil {
		return nil, fmt.Errorf("llm handler failed for tool=%s: %w", tool.Name, err)
	}

	return map[string]string{"output": output}, nil
}

func validateInput(tool *Tool, inputs map[string]interface{}) error {
	schemaBytes := tool.InputSchema
	if len(schemaBytes) == 0 {
		schemaBytes = json.RawMessage(`{}`)
	}

	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("tool-input.json", strings.NewReader(string(schemaBytes))); err != nil {
		return fmt.Errorf("invalid input_schema for tool=%s: %w", tool.Name, err)
	}

	schema, err := compiler.Compile("tool-input.json")
	if err != nil {
		return fmt.Errorf("invalid input_schema for tool=%s: %w", tool.Name, err)
	}

	if err := schema.Validate(inputs); err != nil {
		return &ErrInvalidInput{
			ToolName: tool.Name,
			Message:  formatValidationError(err),
		}
	}

	return nil
}

func validateOutput(tool *Tool, output interface{}) error {
	schemaBytes := tool.OutputSchema
	if len(schemaBytes) == 0 {
		schemaBytes = json.RawMessage(`{}`)
	}

	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("tool-output.json", strings.NewReader(string(schemaBytes))); err != nil {
		return fmt.Errorf("invalid output_schema for tool=%s: %w", tool.Name, err)
	}

	schema, err := compiler.Compile("tool-output.json")
	if err != nil {
		return fmt.Errorf("invalid output_schema for tool=%s: %w", tool.Name, err)
	}

	if err := schema.Validate(output); err != nil {
		return &ErrInvalidOutput{
			ToolName: tool.Name,
			Message:  formatValidationError(err),
		}
	}

	return nil
}

func formatValidationError(err error) string {
	var vErr *jsonschema.ValidationError
	if !errors.As(err, &vErr) {
		return err.Error()
	}

	messages := flattenValidationErrors(vErr)
	if len(messages) == 0 {
		return vErr.Error()
	}
	return strings.Join(messages, "; ")
}

func flattenValidationErrors(vErr *jsonschema.ValidationError) []string {
	if vErr == nil {
		return nil
	}

	if len(vErr.Causes) == 0 {
		field := strings.TrimSpace(vErr.InstanceLocation)
		if field == "" {
			field = "$"
		}
		return []string{fmt.Sprintf("%s: %s", field, vErr.Message)}
	}

	out := make([]string, 0)
	for _, cause := range vErr.Causes {
		out = append(out, flattenValidationErrors(cause)...)
	}
	return out
}
