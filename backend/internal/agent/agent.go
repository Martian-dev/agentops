package agent

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Martian-dev/agentops/internal/db"
	"github.com/Martian-dev/agentops/internal/tools"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RunTraceEmitter extends TraceEmitter with the ability to close a run's trace stream.
type RunTraceEmitter interface {
	TraceEmitter
	CloseRun(ctx context.Context, runID string) error
}

const queryTimeout = 3 * time.Second
const maxRunDuration = 5 * time.Minute

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
	Goal            string `json:"goal"`
	PromptVersionID string `json:"prompt_version_id"`
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

// RunSummary is the shape returned by GET /agents/:id/runs.
type RunSummary struct {
	ID          string     `json:"id"`
	Status      string     `json:"status"`
	Goal        *string    `json:"goal"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	TokenTotal  int        `json:"token_total"`
}

// RunStatus is the shape returned by GET /runs/:id/status.
type RunStatus struct {
	Status        string                 `json:"status"`
	CompletionPct int                    `json:"completion_pct"`
	NodeStates    map[string]string      `json:"node_states"`
	NodeOutputs   map[string]interface{} `json:"node_outputs,omitempty"`
	TokenTotal    int                    `json:"token_total"`
	ElapsedMs     int64                  `json:"elapsed_ms"`
}

// liveRunState tracks in-flight run state for status polling.
type liveRunState struct {
	nodeStates  map[string]string
	nodeOutputs map[string]interface{}
	tokenCount  *int64
	startedAt   time.Time
	totalNodes  int
}

type Handler struct {
	pool          *pgxpool.Pool
	toolRouter    *tools.Router
	traceEmitter  RunTraceEmitter
	planner       *Planner
	activeCancels sync.Map // run_id → context.CancelFunc
	runStates     sync.Map // run_id → *liveRunState
}

func NewHandler() *Handler {
	return &Handler{pool: db.Pool}
}

// NewHandlerWithDeps creates a handler with all dependencies needed for run execution.
func NewHandlerWithDeps(pool *pgxpool.Pool, toolRouter *tools.Router, traceEmitter RunTraceEmitter) *Handler {
	return &Handler{
		pool:         pool,
		toolRouter:   toolRouter,
		traceEmitter: traceEmitter,
		planner:      NewPlannerFromEnv(),
	}
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

	agentTools, err := h.getToolsByIDs(ctx, detail.ToolIDs)
	if err != nil {
		return writeError(c, fiber.StatusInternalServerError, "internal_error", "failed to fetch agent tools")
	}
	detail.Tools = agentTools
	detail.PromptVersions = make([]interface{}, 0)

	return writeSuccess(c, fiber.StatusOK, detail)
}

// RunAgent handles POST /agents/:id/run — Step 10a full lifecycle.
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

	// Load agent
	var agentObj Agent
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
		&agentObj.ID, &agentObj.Name, &agentObj.ToolIDs,
		&modelConfigText, &agentObj.ActiveVersion, &agentObj.CreatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return writeError(c, fiber.StatusNotFound, "not_found", "agent not found")
		}
		return writeError(c, fiber.StatusInternalServerError, "internal_error", "failed to load agent")
	}
	agentObj.ModelConfig = json.RawMessage(modelConfigText)

	// Load tools
	agentTools, err := h.getToolsByIDs(ctx, agentObj.ToolIDs)
	if err != nil {
		return writeError(c, fiber.StatusInternalServerError, "internal_error", "failed to load agent tools")
	}

	// INSERT agent_runs row
	var runID string
	err = h.pool.QueryRow(ctx, `
		INSERT INTO agent_runs (agent_id, status, goal, started_at)
		VALUES ($1::uuid, 'running', $2, now())
		RETURNING id::text
	`, agentID, req.Goal).Scan(&runID)
	if err != nil {
		return writeError(c, fiber.StatusInternalServerError, "internal_error", "failed to create run")
	}

	// INSERT agent_traces row
	_, err = h.pool.Exec(ctx, `
		INSERT INTO agent_traces (run_id, events)
		VALUES ($1::uuid, '[]'::jsonb)
	`, runID)
	if err != nil {
		return writeError(c, fiber.StatusInternalServerError, "internal_error", "failed to create trace")
	}

	// Return 202 Accepted immediately
	c.Status(fiber.StatusAccepted)

	// Step 10b: context with wall-clock timeout + cancellation
	runCtx, runCancel := context.WithTimeout(context.Background(), maxRunDuration)
	h.activeCancels.Store(runID, runCancel)

	// Convert agent tools to planner-compatible format
	plannerTools := make([]Tool, len(agentTools))
	copy(plannerTools, agentTools)

	modelCfg := ParseModelConfig(agentObj.ModelConfig)

	// Launch async goroutine for planning + execution
	go h.executeRun(runCtx, runCancel, runID, req.Goal, plannerTools, modelCfg)

	return writeSuccess(c, fiber.StatusAccepted, fiber.Map{
		"run_id": runID,
	})
}

// executeRun is the background goroutine for the full plan→execute lifecycle.
func (h *Handler) executeRun(ctx context.Context, cancel context.CancelFunc, runID, goal string, agentTools []Tool, modelCfg ModelConfig) {
	defer func() {
		h.activeCancels.Delete(runID)
		h.runStates.Delete(runID)
		cancel()
	}()

	finalStatus := "failed"
	var tokenTotal int64

	defer func() {
		updateCtx, updateCancel := context.WithTimeout(context.Background(), queryTimeout)
		defer updateCancel()
		
		var nodeOutputsJSON []byte
		if v, ok := h.runStates.Load(runID); ok {
			live := v.(*liveRunState)
			if len(live.nodeOutputs) > 0 {
				nodeOutputsJSON, _ = json.Marshal(live.nodeOutputs)
			}
		}

		_, err := h.pool.Exec(updateCtx, `
			UPDATE agent_runs
			SET status = $1, completed_at = now(), token_total = $2, node_outputs = $4::jsonb
			WHERE id = $3::uuid
		`, finalStatus, tokenTotal, runID, nodeOutputsJSON)
		if err != nil {
			log.Printf("ERROR: failed to update run %s status: %v", runID, err)
		}
	}()

	// Planner
	if h.planner == nil {
		log.Printf("ERROR: run %s failed: planner not configured", runID)
		return
	}
	plan, err := h.planner.Plan(ctx, goal, agentTools)
	if err != nil {
		log.Printf("ERROR: run %s planning failed: %v", runID, err)
		return
	}

	// Save DAG plan to DB
	planJSON, err := json.Marshal(plan)
	if err == nil {
		updateCtx, updateCancel := context.WithTimeout(context.Background(), queryTimeout)
		_, _ = h.pool.Exec(updateCtx, `
			UPDATE agent_runs SET dag_plan = $1::jsonb WHERE id = $2::uuid
		`, planJSON, runID)
		updateCancel()
	}

	// Build tool names for validation
	toolNames := make([]string, len(agentTools))
	for i, t := range agentTools {
		toolNames[i] = t.Name
	}

	// Validate
	if err := ValidateDAG(plan, toolNames); err != nil {
		log.Printf("ERROR: run %s DAG validation failed: %v", runID, err)
		return
	}

	// Create executor with guardrails
	var execToolRouter ToolRouter
	if h.toolRouter != nil {
		execToolRouter = h.toolRouter
	}
	var execTraceEmitter TraceEmitter
	if h.traceEmitter != nil {
		execTraceEmitter = h.traceEmitter
	}
	executor := NewExecutorWithConfig(execToolRouter, execTraceEmitter, modelCfg)

	// Initialize live run state for status polling
	nodeStates := make(map[string]string, len(plan.Nodes))
	for _, node := range plan.Nodes {
		nodeStates[node.ID] = string(NodeStatusPending)
	}
	live := &liveRunState{
		nodeStates:  nodeStates,
		nodeOutputs: make(map[string]interface{}),
		tokenCount:  &executor.tokenCount,
		startedAt:   time.Now(),
		totalNodes:  len(plan.Nodes),
	}
	h.runStates.Store(runID, live)

	// Execute
	states, execErr := executor.Execute(ctx, runID, plan)

	// Update live state with final node states and extract outputs
	outMap := make(map[string]interface{})
	if states != nil {
		for id, state := range states {
			live.nodeStates[id] = string(state.Status)
			if len(state.Output) > 0 {
				var parsed interface{}
				if err := json.Unmarshal([]byte(state.Output), &parsed); err == nil {
					live.nodeOutputs[id] = parsed
					outMap[id] = parsed
				}
			}
		}
	}

	tokenTotal = executor.TokensUsed()

	// Close trace emitter for this run
	if h.traceEmitter != nil {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), queryTimeout)
		_ = h.traceEmitter.CloseRun(closeCtx, runID)
		closeCancel()
	}

	if execErr != nil {
		log.Printf("ERROR: run %s execution failed: %v", runID, execErr)
		return
	}

	finalStatus = "success"
}

// GetRunStatus handles GET /runs/:id/status — Step 10c.
func (h *Handler) GetRunStatus(c *fiber.Ctx) error {
	if h.pool == nil {
		return writeError(c, fiber.StatusServiceUnavailable, "db_unavailable", "database connection not initialized")
	}

	runID := strings.TrimSpace(c.Params("id"))
	if _, err := uuid.Parse(runID); err != nil {
		return writeError(c, fiber.StatusBadRequest, "validation_error", "id must be a valid UUID")
	}

	// Check live state first (in-flight run)
	if v, ok := h.runStates.Load(runID); ok {
		live := v.(*liveRunState)
		completed := 0
		for _, s := range live.nodeStates {
			if s == string(NodeStatusSuccess) || s == string(NodeStatusFailed) {
				completed++
			}
		}
		pct := 0
		if live.totalNodes > 0 {
			pct = (completed * 100) / live.totalNodes
		}
		return writeSuccess(c, fiber.StatusOK, RunStatus{
			Status:        "running",
			CompletionPct: pct,
			NodeStates:    live.nodeStates,
			NodeOutputs:   live.nodeOutputs,
			TokenTotal:    int(atomic.LoadInt64(live.tokenCount)),
			ElapsedMs:     time.Since(live.startedAt).Milliseconds(),
		})
	}

	// Completed run — read from DB
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	var status string
	var tokenTotal int
	var startedAt, completedAt *time.Time
	var dagPlanJSON []byte
	var nodeOutputsJSON []byte
	err := h.pool.QueryRow(ctx, `
		SELECT status, token_total, started_at, completed_at, dag_plan, node_outputs
		FROM agent_runs WHERE id = $1::uuid
	`, runID).Scan(&status, &tokenTotal, &startedAt, &completedAt, &dagPlanJSON, &nodeOutputsJSON)
	if err != nil {
		if err == pgx.ErrNoRows {
			return writeError(c, fiber.StatusNotFound, "not_found", "run not found")
		}
		return writeError(c, fiber.StatusInternalServerError, "internal_error", "failed to fetch run status")
	}

	// Parse outputs
	var nodeOutputs map[string]interface{}
	if len(nodeOutputsJSON) > 0 {
		_ = json.Unmarshal(nodeOutputsJSON, &nodeOutputs)
	}

	// Reconstruct node states from saved DAG plan
	nodeStates := make(map[string]string)
	if len(dagPlanJSON) > 0 {
		var plan DAGPlan
		if json.Unmarshal(dagPlanJSON, &plan) == nil {
			for _, node := range plan.Nodes {
				if status == "success" {
					nodeStates[node.ID] = string(NodeStatusSuccess)
				} else {
					nodeStates[node.ID] = string(NodeStatusFailed)
				}
			}
		}
	}

	var elapsedMs int64
	if startedAt != nil {
		end := time.Now()
		if completedAt != nil {
			end = *completedAt
		}
		elapsedMs = end.Sub(*startedAt).Milliseconds()
	}

	return writeSuccess(c, fiber.StatusOK, RunStatus{
		Status:        status,
		CompletionPct: 100,
		NodeStates:    nodeStates,
		TokenTotal:    tokenTotal,
		ElapsedMs:     elapsedMs,
	})
}

// GetRunTrace handles GET /runs/:id/trace — Step 10d.
func (h *Handler) GetRunTrace(c *fiber.Ctx) error {
	if h.pool == nil {
		return writeError(c, fiber.StatusServiceUnavailable, "db_unavailable", "database connection not initialized")
	}

	runID := strings.TrimSpace(c.Params("id"))
	if _, err := uuid.Parse(runID); err != nil {
		return writeError(c, fiber.StatusBadRequest, "validation_error", "id must be a valid UUID")
	}

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	var eventsJSON []byte
	err := h.pool.QueryRow(ctx, `
		SELECT events FROM agent_traces WHERE run_id = $1::uuid
	`, runID).Scan(&eventsJSON)
	if err != nil {
		if err == pgx.ErrNoRows {
			return writeError(c, fiber.StatusNotFound, "not_found", "trace not found")
		}
		return writeError(c, fiber.StatusInternalServerError, "internal_error", "failed to fetch trace")
	}

	var events interface{}
	if err := json.Unmarshal(eventsJSON, &events); err != nil {
		return writeError(c, fiber.StatusInternalServerError, "internal_error", "failed to parse trace events")
	}

	return writeSuccess(c, fiber.StatusOK, events)
}

// ListAgentRuns handles GET /agents/:id/runs — Step 10e.
func (h *Handler) ListAgentRuns(c *fiber.Ctx) error {
	if h.pool == nil {
		return writeError(c, fiber.StatusServiceUnavailable, "db_unavailable", "database connection not initialized")
	}

	agentID := strings.TrimSpace(c.Params("id"))
	if _, err := uuid.Parse(agentID); err != nil {
		return writeError(c, fiber.StatusBadRequest, "validation_error", "id must be a valid UUID")
	}

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	rows, err := h.pool.Query(ctx, `
		SELECT id::text, status, goal, started_at, completed_at, token_total
		FROM agent_runs
		WHERE agent_id = $1::uuid
		ORDER BY started_at DESC
		LIMIT 50
	`, agentID)
	if err != nil {
		return writeError(c, fiber.StatusInternalServerError, "internal_error", "failed to list runs")
	}
	defer rows.Close()

	runs := make([]RunSummary, 0)
	for rows.Next() {
		var r RunSummary
		if err := rows.Scan(&r.ID, &r.Status, &r.Goal, &r.StartedAt, &r.CompletedAt, &r.TokenTotal); err != nil {
			return writeError(c, fiber.StatusInternalServerError, "internal_error", "failed to read runs")
		}
		runs = append(runs, r)
	}
	if err := rows.Err(); err != nil {
		return writeError(c, fiber.StatusInternalServerError, "internal_error", "failed to read runs")
	}

	return writeSuccess(c, fiber.StatusOK, runs)
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

	agentTools := make([]Tool, 0, len(toolIDs))
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
		agentTools = append(agentTools, t)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return agentTools, nil
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
