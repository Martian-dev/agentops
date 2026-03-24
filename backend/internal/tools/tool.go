package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Tool represents a tool that can be used by agents
type Tool struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Description   *string         `json:"description"`
	InputSchema   json.RawMessage `json:"input_schema"`
	OutputSchema  json.RawMessage `json:"output_schema"`
	HandlerType   string          `json:"handler_type"`
	HandlerConfig json.RawMessage `json:"handler_config"`
	CreatedAt     time.Time       `json:"created_at"`
}

// ToolHandlerFunc executes an internal (in-process) tool implementation.
type ToolHandlerFunc func(ctx context.Context, inputs map[string]interface{}) (interface{}, error)

// ErrToolNotFound indicates that a tool is not present in the registry table.
type ErrToolNotFound struct {
	ToolName string
}

func (e *ErrToolNotFound) Error() string {
	return fmt.Sprintf("tool not found: %s", e.ToolName)
}

// ErrInvalidInput indicates that provided inputs failed JSON Schema validation.
type ErrInvalidInput struct {
	ToolName string
	Message  string
}

func (e *ErrInvalidInput) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("invalid input for tool: %s", e.ToolName)
	}
	return fmt.Sprintf("invalid input for tool %s: %s", e.ToolName, e.Message)
}

// ErrInvalidOutput indicates that a tool response failed JSON Schema validation.
type ErrInvalidOutput struct {
	ToolName string
	Message  string
}

func (e *ErrInvalidOutput) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("invalid output for tool: %s", e.ToolName)
	}
	return fmt.Sprintf("invalid output for tool %s: %s", e.ToolName, e.Message)
}
