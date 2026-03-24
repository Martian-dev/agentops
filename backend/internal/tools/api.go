package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const toolQueryTimeout = 3 * time.Second

type apiEnvelope struct {
	Data  interface{} `json:"data"`
	Error interface{} `json:"error"`
}

type apiErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type CreateToolRequest struct {
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	InputSchema   json.RawMessage `json:"input_schema"`
	OutputSchema  json.RawMessage `json:"output_schema"`
	HandlerType   string          `json:"handler_type"`
	HandlerConfig json.RawMessage `json:"handler_config"`
}

type UpdateToolRequest struct {
	Name          *string         `json:"name"`
	Description   *string         `json:"description"`
	InputSchema   json.RawMessage `json:"input_schema"`
	OutputSchema  json.RawMessage `json:"output_schema"`
	HandlerType   *string         `json:"handler_type"`
	HandlerConfig json.RawMessage `json:"handler_config"`
}

type TestToolRequest struct {
	Input json.RawMessage `json:"input"`
}

type APIHandler struct {
	pool       *pgxpool.Pool
	toolRouter *Router
}

func NewAPIHandler(pool *pgxpool.Pool, toolRouter *Router) *APIHandler {
	return &APIHandler{pool: pool, toolRouter: toolRouter}
}

func (h *APIHandler) CreateTool(c *fiber.Ctx) error {
	if h.pool == nil {
		return writeAPIError(c, fiber.StatusServiceUnavailable, "db_unavailable", "database connection not initialized")
	}

	var req CreateToolRequest
	if err := decodeStrictJSON(c.Body(), &req); err != nil {
		return writeAPIError(c, fiber.StatusBadRequest, "bad_request", "invalid JSON request body")
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return writeAPIError(c, fiber.StatusBadRequest, "validation_error", "name is required")
	}

	req.Description = strings.TrimSpace(req.Description)
	req.HandlerType = strings.TrimSpace(req.HandlerType)
	if !isAllowedHandlerType(req.HandlerType) {
		return writeAPIError(c, fiber.StatusBadRequest, "validation_error", "handler_type must be one of: http, internal, llm")
	}

	if _, err := requireJSONObject(req.InputSchema, "input_schema"); err != nil {
		return writeAPIError(c, fiber.StatusBadRequest, "validation_error", err.Error())
	}
	if _, err := requireJSONObject(req.OutputSchema, "output_schema"); err != nil {
		return writeAPIError(c, fiber.StatusBadRequest, "validation_error", err.Error())
	}
	if len(req.HandlerConfig) == 0 {
		req.HandlerConfig = json.RawMessage(`{}`)
	}
	if _, err := requireJSONObject(req.HandlerConfig, "handler_config"); err != nil {
		return writeAPIError(c, fiber.StatusBadRequest, "validation_error", err.Error())
	}

	ctx, cancel := context.WithTimeout(context.Background(), toolQueryTimeout)
	defer cancel()

	var existing bool
	if err := h.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tools WHERE name = $1)`, req.Name).Scan(&existing); err != nil {
		return writeAPIError(c, fiber.StatusInternalServerError, "internal_error", "failed to validate tool name")
	}
	if existing {
		return writeAPIError(c, fiber.StatusConflict, "already_exists", "tool name already exists")
	}

	row := h.pool.QueryRow(ctx, `
		INSERT INTO tools (name, description, input_schema, output_schema, handler_type, handler_config)
		VALUES ($1, $2, $3::jsonb, $4::jsonb, $5, $6::jsonb)
		RETURNING
			id::text,
			name,
			description,
			COALESCE(input_schema, '{}'::jsonb)::text,
			COALESCE(output_schema, '{}'::jsonb)::text,
			COALESCE(handler_type, ''),
			COALESCE(handler_config, '{}'::jsonb)::text,
			created_at
	`, req.Name, req.Description, req.InputSchema, req.OutputSchema, req.HandlerType, req.HandlerConfig)

	tool, err := scanToolRow(row)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return writeAPIError(c, fiber.StatusConflict, "already_exists", "tool name already exists")
		}
		return writeAPIError(c, fiber.StatusInternalServerError, "internal_error", "failed to create tool")
	}

	return writeAPISuccess(c, fiber.StatusOK, tool)
}

func (h *APIHandler) ListTools(c *fiber.Ctx) error {
	if h.pool == nil {
		return writeAPIError(c, fiber.StatusServiceUnavailable, "db_unavailable", "database connection not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), toolQueryTimeout)
	defer cancel()

	rows, err := h.pool.Query(ctx, `
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
		ORDER BY created_at DESC
	`)
	if err != nil {
		return writeAPIError(c, fiber.StatusInternalServerError, "internal_error", "failed to list tools")
	}
	defer rows.Close()

	tools := make([]Tool, 0)
	for rows.Next() {
		tool, err := scanToolRows(rows)
		if err != nil {
			return writeAPIError(c, fiber.StatusInternalServerError, "internal_error", "failed to read tools")
		}
		tools = append(tools, tool)
	}
	if err := rows.Err(); err != nil {
		return writeAPIError(c, fiber.StatusInternalServerError, "internal_error", "failed to read tools")
	}

	return writeAPISuccess(c, fiber.StatusOK, tools)
}

func (h *APIHandler) GetTool(c *fiber.Ctx) error {
	if h.pool == nil {
		return writeAPIError(c, fiber.StatusServiceUnavailable, "db_unavailable", "database connection not initialized")
	}

	toolID := strings.TrimSpace(c.Params("id"))
	if _, err := uuid.Parse(toolID); err != nil {
		return writeAPIError(c, fiber.StatusBadRequest, "validation_error", "id must be a valid UUID")
	}

	ctx, cancel := context.WithTimeout(context.Background(), toolQueryTimeout)
	defer cancel()

	row := h.pool.QueryRow(ctx, `
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
		WHERE id = $1::uuid
		LIMIT 1
	`, toolID)

	tool, err := scanToolRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return writeAPIError(c, fiber.StatusNotFound, "not_found", "tool not found")
		}
		return writeAPIError(c, fiber.StatusInternalServerError, "internal_error", "failed to fetch tool")
	}

	return writeAPISuccess(c, fiber.StatusOK, tool)
}

func (h *APIHandler) UpdateTool(c *fiber.Ctx) error {
	if h.pool == nil {
		return writeAPIError(c, fiber.StatusServiceUnavailable, "db_unavailable", "database connection not initialized")
	}

	toolID := strings.TrimSpace(c.Params("id"))
	if _, err := uuid.Parse(toolID); err != nil {
		return writeAPIError(c, fiber.StatusBadRequest, "validation_error", "id must be a valid UUID")
	}

	var req UpdateToolRequest
	if err := decodeStrictJSON(c.Body(), &req); err != nil {
		return writeAPIError(c, fiber.StatusBadRequest, "bad_request", "invalid JSON request body")
	}

	if req.Name == nil && req.Description == nil && len(req.InputSchema) == 0 && len(req.OutputSchema) == 0 && req.HandlerType == nil && len(req.HandlerConfig) == 0 {
		return writeAPIError(c, fiber.StatusBadRequest, "validation_error", "at least one field must be provided")
	}

	ctx, cancel := context.WithTimeout(context.Background(), toolQueryTimeout)
	defer cancel()

	row := h.pool.QueryRow(ctx, `
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
		WHERE id = $1::uuid
		LIMIT 1
	`, toolID)

	tool, err := scanToolRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return writeAPIError(c, fiber.StatusNotFound, "not_found", "tool not found")
		}
		return writeAPIError(c, fiber.StatusInternalServerError, "internal_error", "failed to fetch tool")
	}

	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		if trimmed == "" {
			return writeAPIError(c, fiber.StatusBadRequest, "validation_error", "name cannot be empty")
		}
		tool.Name = trimmed
	}

	if req.Description != nil {
		trimmed := strings.TrimSpace(*req.Description)
		tool.Description = &trimmed
	}

	if len(req.InputSchema) > 0 {
		if _, err := requireJSONObject(req.InputSchema, "input_schema"); err != nil {
			return writeAPIError(c, fiber.StatusBadRequest, "validation_error", err.Error())
		}
		tool.InputSchema = req.InputSchema
	}

	if len(req.OutputSchema) > 0 {
		if _, err := requireJSONObject(req.OutputSchema, "output_schema"); err != nil {
			return writeAPIError(c, fiber.StatusBadRequest, "validation_error", err.Error())
		}
		tool.OutputSchema = req.OutputSchema
	}

	if req.HandlerType != nil {
		trimmed := strings.TrimSpace(*req.HandlerType)
		if !isAllowedHandlerType(trimmed) {
			return writeAPIError(c, fiber.StatusBadRequest, "validation_error", "handler_type must be one of: http, internal, llm")
		}
		tool.HandlerType = trimmed
	}

	if len(req.HandlerConfig) > 0 {
		if _, err := requireJSONObject(req.HandlerConfig, "handler_config"); err != nil {
			return writeAPIError(c, fiber.StatusBadRequest, "validation_error", err.Error())
		}
		tool.HandlerConfig = req.HandlerConfig
	}

	updatedRow := h.pool.QueryRow(ctx, `
		UPDATE tools
		SET
			name = $2,
			description = $3,
			input_schema = $4::jsonb,
			output_schema = $5::jsonb,
			handler_type = $6,
			handler_config = $7::jsonb
		WHERE id = $1::uuid
		RETURNING
			id::text,
			name,
			description,
			COALESCE(input_schema, '{}'::jsonb)::text,
			COALESCE(output_schema, '{}'::jsonb)::text,
			COALESCE(handler_type, ''),
			COALESCE(handler_config, '{}'::jsonb)::text,
			created_at
	`, toolID, tool.Name, tool.Description, tool.InputSchema, tool.OutputSchema, tool.HandlerType, tool.HandlerConfig)

	updatedTool, err := scanToolRow(updatedRow)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return writeAPIError(c, fiber.StatusConflict, "already_exists", "tool name already exists")
		}
		return writeAPIError(c, fiber.StatusInternalServerError, "internal_error", "failed to update tool")
	}

	return writeAPISuccess(c, fiber.StatusOK, updatedTool)
}

func (h *APIHandler) DeleteTool(c *fiber.Ctx) error {
	if h.pool == nil {
		return writeAPIError(c, fiber.StatusServiceUnavailable, "db_unavailable", "database connection not initialized")
	}

	toolID := strings.TrimSpace(c.Params("id"))
	if _, err := uuid.Parse(toolID); err != nil {
		return writeAPIError(c, fiber.StatusBadRequest, "validation_error", "id must be a valid UUID")
	}

	ctx, cancel := context.WithTimeout(context.Background(), toolQueryTimeout)
	defer cancel()

	tag, err := h.pool.Exec(ctx, `DELETE FROM tools WHERE id = $1::uuid`, toolID)
	if err != nil {
		return writeAPIError(c, fiber.StatusInternalServerError, "internal_error", "failed to delete tool")
	}

	if tag.RowsAffected() == 0 {
		return writeAPIError(c, fiber.StatusNotFound, "not_found", "tool not found")
	}

	return writeAPISuccess(c, fiber.StatusOK, fiber.Map{
		"id":      toolID,
		"deleted": true,
	})
}

func (h *APIHandler) TestTool(c *fiber.Ctx) error {
	if h.pool == nil {
		return writeAPIError(c, fiber.StatusServiceUnavailable, "db_unavailable", "database connection not initialized")
	}
	if h.toolRouter == nil {
		return writeAPIError(c, fiber.StatusServiceUnavailable, "tool_router_unavailable", "tool router is not initialized")
	}

	toolID := strings.TrimSpace(c.Params("id"))
	if _, err := uuid.Parse(toolID); err != nil {
		return writeAPIError(c, fiber.StatusBadRequest, "validation_error", "id must be a valid UUID")
	}

	ctx, cancel := context.WithTimeout(context.Background(), toolQueryTimeout)
	defer cancel()

	row := h.pool.QueryRow(ctx, `
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
		WHERE id = $1::uuid
		LIMIT 1
	`, toolID)

	tool, err := scanToolRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return writeAPIError(c, fiber.StatusNotFound, "not_found", "tool not found")
		}
		return writeAPIError(c, fiber.StatusInternalServerError, "internal_error", "failed to fetch tool")
	}

	var req TestToolRequest
	if err := decodeStrictJSON(c.Body(), &req); err != nil {
		return writeAPIError(c, fiber.StatusBadRequest, "bad_request", "invalid JSON request body")
	}

	inputObj, err := requireJSONObject(req.Input, "input")
	if err != nil {
		return writeAPIError(c, fiber.StatusBadRequest, "validation_error", err.Error())
	}

	output, err := h.toolRouter.ExecuteRaw(ctx, tool.Name, inputObj)
	if err != nil {
		var invalidIn *ErrInvalidInput
		if errors.As(err, &invalidIn) {
			return writeAPIError(c, fiber.StatusBadRequest, "validation_error", invalidIn.Error())
		}

		var invalidOut *ErrInvalidOutput
		if errors.As(err, &invalidOut) {
			return writeAPIError(c, fiber.StatusBadRequest, "validation_error", invalidOut.Error())
		}

		return writeAPIError(c, fiber.StatusBadRequest, "tool_test_failed", err.Error())
	}

	return writeAPISuccess(c, fiber.StatusOK, fiber.Map{
		"tool_id":   tool.ID,
		"tool_name": tool.Name,
		"valid":     true,
		"output":    output,
	})
}

func decodeStrictJSON(body []byte, target interface{}) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}

	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("request body must contain exactly one JSON object")
	}

	return nil
}

func requireJSONObject(raw json.RawMessage, field string) (map[string]interface{}, error) {
	if len(raw) == 0 {
		return nil, errors.New(field + " is required")
	}

	var parsed interface{}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, errors.New(field + " must be valid JSON")
	}

	obj, ok := parsed.(map[string]interface{})
	if !ok {
		return nil, errors.New(field + " must be a JSON object")
	}

	return obj, nil
}

func isAllowedHandlerType(v string) bool {
	switch v {
	case "http", "internal", "llm":
		return true
	default:
		return false
	}
}

func scanToolRow(row pgx.Row) (Tool, error) {
	var tool Tool
	var inputSchemaText string
	var outputSchemaText string
	var handlerConfigText string

	err := row.Scan(
		&tool.ID,
		&tool.Name,
		&tool.Description,
		&inputSchemaText,
		&outputSchemaText,
		&tool.HandlerType,
		&handlerConfigText,
		&tool.CreatedAt,
	)
	if err != nil {
		return Tool{}, err
	}

	tool.InputSchema = json.RawMessage(inputSchemaText)
	tool.OutputSchema = json.RawMessage(outputSchemaText)
	tool.HandlerConfig = json.RawMessage(handlerConfigText)

	return tool, nil
}

func scanToolRows(rows pgx.Rows) (Tool, error) {
	return scanToolRow(rows)
}

func writeAPISuccess(c *fiber.Ctx, status int, data interface{}) error {
	return c.Status(status).JSON(apiEnvelope{Data: data, Error: nil})
}

func writeAPIError(c *fiber.Ctx, status int, code, message string) error {
	return c.Status(status).JSON(apiEnvelope{
		Data: nil,
		Error: apiErrorBody{
			Code:    code,
			Message: message,
		},
	})
}
