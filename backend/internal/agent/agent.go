package agent

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Martian-dev/agentops/internal/db"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const queryTimeout = 3 * time.Second

// Envelope is the common API response shape.
type Envelope struct {
	Data  interface{} `json:"data"`
	Error interface{} `json:"error"`
}

// ErrorBody is the structured error payload.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type createAgentRequest struct {
	Name        string          `json:"name"`
	ToolIDs     []string        `json:"tool_ids"`
	ModelConfig json.RawMessage `json:"model_config"`
}

type runAgentRequest struct {
	Goal         string      `json:"goal"`
	DatasetInput interface{} `json:"dataset_input"`
}

type Agent struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	ToolIDs       []string        `json:"tool_ids"`
	ModelConfig   json.RawMessage `json:"model_config"`
	ActiveVersion int             `json:"active_version"`
	CreatedAt     time.Time       `json:"created_at"`
}

type Tool struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Description   *string         `json:"description"`
	InputSchema   json.RawMessage `json:"input_schema"`
	OutputSchema  json.RawMessage `json:"output_schema"`
	HandlerType   *string         `json:"handler_type"`
	HandlerConfig json.RawMessage `json:"handler_config"`
	CreatedAt     time.Time       `json:"created_at"`
}

type AgentDetail struct {
	Agent
	Tools          []Tool        `json:"tools"`
	PromptVersions []interface{} `json:"prompt_versions"`
}

type Handler struct {
	pool *pgxpool.Pool
}

func NewHandler() *Handler {
	return &Handler{pool: db.Pool}
}

func (h *Handler) CreateAgent(c *fiber.Ctx) error {
	if h.pool == nil {
		return writeError(c, fiber.StatusServiceUnavailable, "db_unavailable", "database connection not initialized")
	}

	var req createAgentRequest
	if err := c.BodyParser(&req); err != nil {
		return writeError(c, fiber.StatusBadRequest, "bad_request", "invalid JSON request body")
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return writeError(c, fiber.StatusBadRequest, "validation_error", "name is required")
	}

	if req.ModelConfig == nil {
		req.ModelConfig = json.RawMessage(`{}`)
	}
	if !json.Valid(req.ModelConfig) {
		return writeError(c, fiber.StatusBadRequest, "validation_error", "model_config must be valid JSON")
	}

	for _, id := range req.ToolIDs {
		if _, err := uuid.Parse(id); err != nil {
			return writeError(c, fiber.StatusBadRequest, "validation_error", "tool_ids must contain valid UUID values")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	row := h.pool.QueryRow(ctx, `
		INSERT INTO agents (name, tool_ids, model_config)
		VALUES ($1, $2::uuid[], $3::jsonb)
		RETURNING
			id::text,
			name,
			COALESCE((SELECT array_agg(t::text) FROM unnest(tool_ids) AS t), '{}'::text[]),
			COALESCE(model_config, '{}'::jsonb)::text,
			active_version,
			created_at
	`, req.Name, req.ToolIDs, req.ModelConfig)

	var agent Agent
	var modelConfigText string
	if err := row.Scan(&agent.ID, &agent.Name, &agent.ToolIDs, &modelConfigText, &agent.ActiveVersion, &agent.CreatedAt); err != nil {
		return writeError(c, fiber.StatusInternalServerError, "internal_error", "failed to create agent")
	}
	agent.ModelConfig = json.RawMessage(modelConfigText)

	return writeSuccess(c, fiber.StatusOK, agent)
}

func (h *Handler) ListAgents(c *fiber.Ctx) error {
	if h.pool == nil {
		return writeError(c, fiber.StatusServiceUnavailable, "db_unavailable", "database connection not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	rows, err := h.pool.Query(ctx, `
		SELECT
			id::text,
			name,
			COALESCE((SELECT array_agg(t::text) FROM unnest(tool_ids) AS t), '{}'::text[]),
			COALESCE(model_config, '{}'::jsonb)::text,
			active_version,
			created_at
		FROM agents
		ORDER BY created_at DESC
	`)
	if err != nil {
		return writeError(c, fiber.StatusInternalServerError, "internal_error", "failed to list agents")
	}
	defer rows.Close()

	agents := make([]Agent, 0)
	for rows.Next() {
		var agent Agent
		var modelConfigText string
		if err := rows.Scan(&agent.ID, &agent.Name, &agent.ToolIDs, &modelConfigText, &agent.ActiveVersion, &agent.CreatedAt); err != nil {
			return writeError(c, fiber.StatusInternalServerError, "internal_error", "failed to read agents")
		}
		agent.ModelConfig = json.RawMessage(modelConfigText)
		agents = append(agents, agent)
	}

	if err := rows.Err(); err != nil {
		return writeError(c, fiber.StatusInternalServerError, "internal_error", "failed to read agents")
	}

	return writeSuccess(c, fiber.StatusOK, agents)
}

func (h *Handler) GetAgent(c *fiber.Ctx) error {
	if h.pool == nil {
		return writeError(c, fiber.StatusServiceUnavailable, "db_unavailable", "database connection not initialized")
	}

	agentID := strings.TrimSpace(c.Params("id"))
	if _, err := uuid.Parse(agentID); err != nil {
		return writeError(c, fiber.StatusBadRequest, "validation_error", "id must be a valid UUID")
	}

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	var detail AgentDetail
	var modelConfigText string
	err := h.pool.QueryRow(ctx, `
		SELECT
			id::text,
			name,
			COALESCE((SELECT array_agg(t::text) FROM unnest(tool_ids) AS t), '{}'::text[]),
			COALESCE(model_config, '{}'::jsonb)::text,
			active_version,
			created_at
		FROM agents
		WHERE id = $1::uuid
	`, agentID).Scan(
		&detail.ID,
		&detail.Name,
		&detail.ToolIDs,
		&modelConfigText,
		&detail.ActiveVersion,
		&detail.CreatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return writeError(c, fiber.StatusNotFound, "not_found", "agent not found")
		}
		return writeError(c, fiber.StatusInternalServerError, "internal_error", "failed to fetch agent")
	}
	detail.ModelConfig = json.RawMessage(modelConfigText)

	tools, err := h.getToolsByIDs(ctx, detail.ToolIDs)
	if err != nil {
		return writeError(c, fiber.StatusInternalServerError, "internal_error", "failed to fetch agent tools")
	}
	detail.Tools = tools
	detail.PromptVersions = make([]interface{}, 0)

	return writeSuccess(c, fiber.StatusOK, detail)
}

func (h *Handler) RunAgent(c *fiber.Ctx) error {
	if h.pool == nil {
		return writeError(c, fiber.StatusServiceUnavailable, "db_unavailable", "database connection not initialized")
	}

	agentID := strings.TrimSpace(c.Params("id"))
	if _, err := uuid.Parse(agentID); err != nil {
		return writeError(c, fiber.StatusBadRequest, "validation_error", "id must be a valid UUID")
	}

	var req runAgentRequest
	if err := c.BodyParser(&req); err != nil {
		return writeError(c, fiber.StatusBadRequest, "bad_request", "invalid JSON request body")
	}
	if strings.TrimSpace(req.Goal) == "" {
		return writeError(c, fiber.StatusBadRequest, "validation_error", "goal is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	var exists bool
	if err := h.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id = $1::uuid)`, agentID).Scan(&exists); err != nil {
		return writeError(c, fiber.StatusInternalServerError, "internal_error", "failed to validate agent")
	}
	if !exists {
		return writeError(c, fiber.StatusNotFound, "not_found", "agent not found")
	}

	data := fiber.Map{
		"run_id": uuid.NewString(),
		"status": "pending",
	}

	return writeSuccess(c, fiber.StatusOK, data)
}

func (h *Handler) getToolsByIDs(ctx context.Context, toolIDs []string) ([]Tool, error) {
	if len(toolIDs) == 0 {
		return make([]Tool, 0), nil
	}

	rows, err := h.pool.Query(ctx, `
		SELECT
			id::text,
			name,
			description,
			COALESCE(input_schema, '{}'::jsonb)::text,
			COALESCE(output_schema, '{}'::jsonb)::text,
			handler_type,
			COALESCE(handler_config, '{}'::jsonb)::text,
			created_at
		FROM tools
		WHERE id = ANY($1::uuid[])
		ORDER BY name ASC
	`, toolIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tools := make([]Tool, 0, len(toolIDs))
	for rows.Next() {
		var t Tool
		var inputSchemaText string
		var outputSchemaText string
		var handlerConfigText string
		if err := rows.Scan(
			&t.ID,
			&t.Name,
			&t.Description,
			&inputSchemaText,
			&outputSchemaText,
			&t.HandlerType,
			&handlerConfigText,
			&t.CreatedAt,
		); err != nil {
			return nil, err
		}
		t.InputSchema = json.RawMessage(inputSchemaText)
		t.OutputSchema = json.RawMessage(outputSchemaText)
		t.HandlerConfig = json.RawMessage(handlerConfigText)
		tools = append(tools, t)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return tools, nil
}

func writeSuccess(c *fiber.Ctx, status int, data interface{}) error {
	return c.Status(status).JSON(Envelope{
		Data:  data,
		Error: nil,
	})
}

func writeError(c *fiber.Ctx, status int, code, message string) error {
	return c.Status(status).JSON(Envelope{
		Data: nil,
		Error: ErrorBody{
			Code:    code,
			Message: message,
		},
	})
}
