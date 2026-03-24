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
)

type NodeState struct {
	Status     NodeStatus
	Output     string
	Err        error
	RetryCount int
}

type TraceEvent struct {
	NodeID    string    `json:"node_id"`
	EventType string    `json:"event_type,omitempty"`
	FromState string    `json:"from_state"`
	ToState   string    `json:"to_state"`
	Attempt   int       `json:"attempt,omitempty"`
	Message   string    `json:"message,omitempty"`
	At        time.Time `json:"at"`
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
	tokenCount      int64 // accessed atomically
	maxTokenBudget  int64
	piiFilterEnabled bool
	cancelRun       context.CancelFunc
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

func (e *Executor) Execute(ctx context.Context, runID string, plan *DAGPlan) (map[string]*NodeState, error) {
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

	states := make(map[string]*NodeState, len(plan.Nodes))
	for _, node := range plan.Nodes {
		states[node.ID] = &NodeState{Status: NodeStatusPending}
	}

	var mu sync.RWMutex

	// Create a cancellable context for token budget enforcement
	runCtx, cancelRun := context.WithCancel(ctx)
	e.cancelRun = cancelRun
	defer cancelRun()

	setState := func(nodeID string, next NodeStatus, output string, nodeErr error, retries int, message string) {
		mu.Lock()
		state := states[nodeID]
		prev := state.Status
		state.Status = next
		if output != "" {
			state.Output = output
		}
		state.Err = nodeErr
		state.RetryCount = retries
		mu.Unlock()

		if e.TraceEmitter != nil {
			_ = e.TraceEmitter.Emit(ctx, runID, TraceEvent{
				NodeID:    nodeID,
				EventType: "",
				FromState: string(prev),
				ToState:   string(next),
				Attempt:   retries,
				Message:   message,
				At:        time.Now().UTC(),
			})
		}
	}

	emitCustomEvent := func(nodeID, eventType string, attempt int, message string) {
		if e.TraceEmitter == nil {
			return
		}
		_ = e.TraceEmitter.Emit(ctx, runID, TraceEvent{
			NodeID:    nodeID,
			EventType: eventType,
			FromState: "",
			ToState:   "",
			Attempt:   attempt,
			Message:   message,
			At:        time.Now().UTC(),
		})
	}

	hasFailedDependency := func(node DAGNode) bool {
		mu.RLock()
		defer mu.RUnlock()
		for _, depID := range node.DependsOn {
			depState, ok := states[depID]
			if !ok {
				return true
			}
			if depState.Status == NodeStatusFailed {
				return true
			}
		}
		return false
	}

	resolveInputs := func(node DAGNode) (map[string]interface{}, error) {
		resolved := make(map[string]interface{}, len(node.Inputs))
		for k, v := range node.Inputs {
			if strings.HasPrefix(v, "$") && strings.HasSuffix(v, ".output") {
				refID := strings.TrimSuffix(strings.TrimPrefix(v, "$"), ".output")
				mu.RLock()
				refState, ok := states[refID]
				mu.RUnlock()
				if !ok {
					return nil, fmt.Errorf("node_id=%s has unknown output reference=%s", node.ID, v)
				}
				if refState.Status != NodeStatusSuccess {
					return nil, fmt.Errorf("node_id=%s references non-success dependency output dependency=%s status=%s", node.ID, refID, refState.Status)
				}
				resolved[k] = refState.Output
				continue
			}
			resolved[k] = v
		}
		return resolved, nil
	}

	for _, tier := range tiers {
		var wg sync.WaitGroup
		for _, node := range tier {
			if hasFailedDependency(node) {
				setState(node.ID, NodeStatusFailed, "", fmt.Errorf("skipped due to failed dependency"), 0, "dependency_failed")
				continue
			}

			node := node
			wg.Add(1)
			go func() {
				defer wg.Done()

				inputs, err := resolveInputs(node)
				if err != nil {
					setState(node.ID, NodeStatusFailed, "", err, 0, "input_resolution_failed")
					return
				}

				nodeCtx := tracectx.WithProviderFallbackHook(runCtx, func(providerErr error) {
					emitCustomEvent(node.ID, "provider_fallback", 0, providerErr.Error())
				})

				output, attempts, err := e.runNode(nodeCtx, node, inputs, maxRetries, nodeTimeout, emitCustomEvent)
				if err != nil {
					setState(node.ID, NodeStatusFailed, "", err, attempts, "failed")
					return
				}

				// Guardrail 9d: PII filter
				if e.piiFilterEnabled {
					output = filterPII(output)
				}

				setState(node.ID, NodeStatusSuccess, output, nil, attempts, "success")
			}()
		}

		wg.Wait()
	}

	var runErr error
	mu.RLock()
	for nodeID, state := range states {
		if state.Status == NodeStatusFailed {
			runErr = fmt.Errorf("execution finished with failures; first_failed_node=%s", nodeID)
			break
		}
	}
	mu.RUnlock()

	return states, runErr
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
