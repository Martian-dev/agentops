package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Martian-dev/agentops/internal/llm/tracectx"
	"github.com/Martian-dev/agentops/internal/tools"
)

const (
	defaultNodeTimeout = 30 * time.Second
	defaultMaxRetries  = 2
)

type NodeStatus string

const (
	NodeStatusPending  NodeStatus = "pending"
	NodeStatusRunning  NodeStatus = "running"
	NodeStatusSuccess  NodeStatus = "success"
	NodeStatusFailed   NodeStatus = "failed"
	NodeStatusRetrying NodeStatus = "retrying"
	NodeStatusSkipped  NodeStatus = "skipped"
)

type NodeState struct {
	Status     NodeStatus
	Output     string
	Err        error
	RetryCount int
}

type TraceEvent struct {
	NodeID         string            `json:"node_id"`
	EventType      string            `json:"event_type,omitempty"`
	FromState      string            `json:"from_state"`
	ToState        string            `json:"to_state"`
	Attempt        int               `json:"attempt,omitempty"`
	Message        string            `json:"message,omitempty"`
	At             time.Time         `json:"at"`
	StateSnapshot  map[string]string `json:"state_snapshot,omitempty"`
	OutputPreview  string            `json:"output_preview,omitempty"`
	InputsUsed     map[string]string `json:"inputs_used,omitempty"`
	DurationMs     int64             `json:"duration_ms,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
}

// ToolRouter executes a named tool with resolved inputs.
type ToolRouter interface {
	Execute(ctx context.Context, toolName string, inputs map[string]interface{}) (string, error)
}

// TraceEmitter records node transition events.
type TraceEmitter interface {
	Emit(ctx context.Context, runID string, event TraceEvent) error
}

type Executor struct {
	ToolRouter   ToolRouter
	TraceEmitter TraceEmitter
	NodeTimeout  time.Duration
	MaxRetries   int

	// Guardrails
	tokenCount       int64 // accessed atomically
	maxTokenBudget   int64
	piiFilterEnabled bool
	cancelRun        context.CancelFunc
}

func NewExecutor(toolRouter ToolRouter, traceEmitter TraceEmitter) *Executor {
	return &Executor{
		ToolRouter:     toolRouter,
		TraceEmitter:   traceEmitter,
		NodeTimeout:    defaultNodeTimeout,
		MaxRetries:     defaultMaxRetries,
		maxTokenBudget: int64(defaultMaxTokenBudget),
	}
}

// NewExecutorWithConfig creates an executor with guardrail settings from model config.
func NewExecutorWithConfig(toolRouter ToolRouter, traceEmitter TraceEmitter, cfg ModelConfig) *Executor {
	return &Executor{
		ToolRouter:       toolRouter,
		TraceEmitter:     traceEmitter,
		NodeTimeout:      defaultNodeTimeout,
		MaxRetries:       defaultMaxRetries,
		maxTokenBudget:   int64(cfg.MaxTokenBudget),
		piiFilterEnabled: cfg.PIIFilterEnabled,
	}
}

// TokensUsed returns the cumulative token count for this executor run.
func (e *Executor) TokensUsed() int64 {
	if e == nil {
		return 0
	}
	return atomic.LoadInt64(&e.tokenCount)
}

// Execute runs a DAG plan using the immutable StateStore. State transitions
// are recorded as StateDelta entries rather than direct map mutations.
// Returns the legacy map[string]*NodeState for backward compatibility plus
// the enriched ExecutionResult via ExecuteWithResult.
func (e *Executor) Execute(ctx context.Context, runID string, plan *DAGPlan) (map[string]*NodeState, error) {
	result, err := e.ExecuteWithResult(ctx, runID, plan, nil)
	if result == nil {
		return nil, err
	}
	return result.FinalStates, err
}

// ExecuteWithResult runs a DAG plan and returns the full ExecutionResult
// including state deltas, snapshots, and checkpoint data.
func (e *Executor) ExecuteWithResult(ctx context.Context, runID string, plan *DAGPlan, checkpoint *Checkpoint) (*ExecutionResult, error) {
	if e == nil {
		return nil, fmt.Errorf("executor is nil")
	}
	if e.ToolRouter == nil {
		return nil, fmt.Errorf("tool router is required")
	}
	if plan == nil {
		return nil, fmt.Errorf("plan is nil")
	}

	// Guardrail 9c: recursion depth check
	if GetRecursionDepth(ctx) > maxRecursionDepth {
		return nil, fmt.Errorf("recursion depth exceeded: depth %d exceeds maximum %d", GetRecursionDepth(ctx), maxRecursionDepth)
	}

	tiers, err := topoSort(plan.Nodes)
	if err != nil {
		return nil, err
	}

	nodeTimeout := e.NodeTimeout
	if nodeTimeout <= 0 {
		nodeTimeout = defaultNodeTimeout
	}
	maxRetries := e.MaxRetries
	if maxRetries < 0 {
		maxRetries = defaultMaxRetries
	}

	// Initialize the immutable state store
	var store *StateStore
	startTier := 0
	if checkpoint != nil {
		store = checkpoint.RestoreStateStore()
		startTier = checkpoint.CompletedTiers
	} else {
		store = NewStateStore()
	}

	// Initialize all nodes to pending (if not already in the store from checkpoint)
	for _, node := range plan.Nodes {
		if checkpoint == nil || !checkpoint.IsNodeCompleted(node.ID) {
			// Only set to pending if the node hasn't already been processed
			if store.NodeLatestStatus(node.ID) == NodeStatusPending {
				store.Apply(StateDelta{
					NodeID: node.ID,
					Status: NodeStatusPending,
				})
			}
		}
	}

	// Create a cancellable context for token budget enforcement
	runCtx, cancelRun := context.WithCancel(ctx)
	e.cancelRun = cancelRun
	defer cancelRun()

	applyState := func(nodeID string, status NodeStatus, output string, nodeErr error, attempt int, message string, inputsUsed map[string]string, durationMs int64, idempotencyKey string) {
		errStr := ""
		if nodeErr != nil {
			errStr = nodeErr.Error()
		}

		prevStatus := store.NodeLatestStatus(nodeID)

		store.Apply(StateDelta{
			NodeID:         nodeID,
			Status:         status,
			Output:         output,
			Error:          errStr,
			Attempt:        attempt,
			InputsUsed:     inputsUsed,
			DurationMs:     durationMs,
			IdempotencyKey: idempotencyKey,
		})

		traceMsg := message
		if errStr != "" {
			traceMsg = errStr
		}

		if e.TraceEmitter != nil {
			// Build output preview (first 256 chars)
			preview := output
			if len(preview) > 256 {
				preview = preview[:256] + "..."
			}

			_ = e.TraceEmitter.Emit(ctx, runID, TraceEvent{
				NodeID:         nodeID,
				EventType:      "",
				FromState:      string(prevStatus),
				ToState:        string(status),
				Attempt:        attempt,
				Message:        traceMsg,
				At:             time.Now().UTC(),
				StateSnapshot:  store.StatusSnapshot(),
				OutputPreview:  preview,
				InputsUsed:     inputsUsed,
				DurationMs:     durationMs,
				IdempotencyKey: idempotencyKey,
			})
		}
	}

	emitCustomEvent := func(nodeID, eventType string, attempt int, message string) {
		if e.TraceEmitter == nil {
			return
		}
		_ = e.TraceEmitter.Emit(ctx, runID, TraceEvent{
			NodeID:        nodeID,
			EventType:     eventType,
			FromState:     "",
			ToState:       "",
			Attempt:       attempt,
			Message:       message,
			At:            time.Now().UTC(),
			StateSnapshot: store.StatusSnapshot(),
		})
	}

	hasFailedDependency := func(node DAGNode) bool {
		for _, depID := range node.DependsOn {
			status := store.NodeLatestStatus(depID)
			if status == NodeStatusFailed || status == NodeStatusSkipped {
				return true
			}
		}
		return false
	}

	resolveInputs := func(node DAGNode) (map[string]interface{}, map[string]string, error) {
		resolved := make(map[string]interface{}, len(node.Inputs))
		rawInputs := make(map[string]string, len(node.Inputs))
		for k, v := range node.Inputs {
			rawInputs[k] = v
			if strings.HasPrefix(v, "$") && strings.HasSuffix(v, ".output") {
				refID := strings.TrimSuffix(strings.TrimPrefix(v, "$"), ".output")
				status := store.NodeLatestStatus(refID)
				if status != NodeStatusSuccess {
					return nil, nil, fmt.Errorf("node_id=%s references non-success dependency output dependency=%s status=%s", node.ID, refID, status)
				}
				output := store.NodeOutput(refID)
				resolved[k] = output
				rawInputs[k] = v + " -> (resolved)"
				continue
			}
			resolved[k] = v
		}
		return resolved, rawInputs, nil
	}

	var lastCheckpoint *Checkpoint

	for tierIdx, tier := range tiers {
		// Skip tiers that were already completed in a prior checkpoint
		if tierIdx < startTier {
			continue
		}

		var wg sync.WaitGroup
		for _, node := range tier {
			// Skip nodes already completed from checkpoint
			if checkpoint != nil && checkpoint.IsNodeCompleted(node.ID) {
				continue
			}

			if hasFailedDependency(node) {
				applyState(node.ID, NodeStatusFailed, "", fmt.Errorf("skipped due to failed dependency"), 0, "dependency_failed", nil, 0, "")
				continue
			}

			node := node
			wg.Add(1)
			go func() {
				defer wg.Done()

				inputs, rawInputs, err := resolveInputs(node)
				if err != nil {
					applyState(node.ID, NodeStatusFailed, "", err, 0, "input_resolution_failed", rawInputs, 0, "")
					return
				}

				// Compute idempotency key for this execution
				idemKey := ComputeIdempotencyKey(node.ID, 0, node.Inputs)

				// Check if we have a cached result from checkpoint
				if checkpoint != nil {
					if cachedOutput, ok := checkpoint.LookupIdempotent(idemKey); ok {
						applyState(node.ID, NodeStatusSuccess, cachedOutput, nil, 0, "idempotent_cache_hit", rawInputs, 0, idemKey)
						return
					}
				}

				nodeCtx := tracectx.WithProviderFallbackHook(runCtx, func(providerErr error) {
					emitCustomEvent(node.ID, "provider_fallback", 0, providerErr.Error())
				})

				startTime := time.Now()
				output, attempts, err := e.runNode(nodeCtx, node, inputs, maxRetries, nodeTimeout, emitCustomEvent)
				durationMs := time.Since(startTime).Milliseconds()

				if err != nil {
					applyState(node.ID, NodeStatusFailed, "", err, attempts, "failed", rawInputs, durationMs, idemKey)
					return
				}

				// Guardrail 9d: PII filter
				if e.piiFilterEnabled {
					output = filterPII(output)
				}

				applyState(node.ID, NodeStatusSuccess, output, nil, attempts, "success", rawInputs, durationMs, idemKey)
			}()
		}

		wg.Wait()

		// Checkpoint after each tier completes
		tierCheckpoint, cpErr := NewCheckpoint(runID, plan, store, tierIdx+1)
		if cpErr == nil {
			lastCheckpoint = tierCheckpoint
		}
	}

	// Build final result
	result := &ExecutionResult{
		FinalStates: store.Snapshot(),
		Deltas:      store.AllDeltas(),
		Checkpoint:  lastCheckpoint,
		TotalTokens: e.TokensUsed(),
	}

	var runErr error
	for nodeID, state := range result.FinalStates {
		if state.Status == NodeStatusFailed {
			runErr = fmt.Errorf("execution finished with failures; first_failed_node=%s", nodeID)
			break
		}
	}

	return result, runErr
}

func (e *Executor) runNode(
	ctx context.Context,
	node DAGNode,
	resolvedInputs map[string]interface{},
	retryLimit int,
	nodeTimeout time.Duration,
	emitCustomEvent func(nodeID, eventType string, attempt int, message string),
) (output string, lastAttempt int, err error) {
	backoff := 500 * time.Millisecond

	for attempt := 0; attempt <= retryLimit; attempt++ {
		lastAttempt = attempt
		if attempt > 0 {
			emitCustomEvent(node.ID, "node_retrying", attempt, "retrying")
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return "", attempt, ctx.Err()
			}
			backoff *= 2
		}

		nodeCtx, cancel := context.WithTimeout(ctx, nodeTimeout)
		output, err = e.ToolRouter.Execute(nodeCtx, node.Tool, resolvedInputs)
		cancel()
		if err == nil {
			// Guardrail 9b: token budget check
			// Estimate tokens from output length (rough heuristic: 1 token ≈ 4 chars)
			tokenEstimate := len(output) / 4
			if tokenEstimate < 1 {
				tokenEstimate = 1
			}
			if budgetErr := checkTokenBudget(&e.tokenCount, tokenEstimate, e.maxTokenBudget); budgetErr != nil {
				emitCustomEvent(node.ID, "token_budget_exceeded", attempt, budgetErr.Error())
				if e.cancelRun != nil {
					e.cancelRun()
				}
				return "", attempt, budgetErr
			}
			return output, attempt, nil
		}

		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return "", attempt, err
		}
		var invalidInput *tools.ErrInvalidInput
		if errors.As(err, &invalidInput) {
			return "", attempt, err
		}
	}

	return "", lastAttempt, fmt.Errorf("node %s failed after %d attempts: %w", node.ID, retryLimit+1, err)
}

func topoSort(nodes []DAGNode) ([][]DAGNode, error) {
	if len(nodes) == 0 {
		return make([][]DAGNode, 0), nil
	}

	nodeByID := make(map[string]DAGNode, len(nodes))
	indegree := make(map[string]int, len(nodes))
	adj := make(map[string][]string, len(nodes))

	for _, node := range nodes {
		if _, exists := nodeByID[node.ID]; exists {
			return nil, fmt.Errorf("topo sort failed: duplicate node id=%s", node.ID)
		}
		nodeByID[node.ID] = node
		indegree[node.ID] = 0
	}

	for _, node := range nodes {
		for _, depID := range node.DependsOn {
			if _, exists := nodeByID[depID]; !exists {
				return nil, fmt.Errorf("topo sort failed: node_id=%s missing dependency=%s", node.ID, depID)
			}
			adj[depID] = append(adj[depID], node.ID)
			indegree[node.ID]++
		}
	}

	queue := make([]string, 0)
	for _, node := range nodes {
		if indegree[node.ID] == 0 {
			queue = append(queue, node.ID)
		}
	}

	processed := 0
	tiers := make([][]DAGNode, 0)
	for len(queue) > 0 {
		levelSize := len(queue)
		current := queue[:levelSize]
		queue = queue[levelSize:]

		tier := make([]DAGNode, 0, levelSize)
		for _, id := range current {
			processed++
			tier = append(tier, nodeByID[id])
			for _, next := range adj[id] {
				indegree[next]--
				if indegree[next] == 0 {
					queue = append(queue, next)
				}
			}
		}

		tiers = append(tiers, tier)
	}

	if processed != len(nodes) {
		return nil, fmt.Errorf("topo sort failed: cycle detected")
	}

	return tiers, nil
}
