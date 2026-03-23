package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
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
	FromState string    `json:"from_state"`
	ToState   string    `json:"to_state"`
	Message   string    `json:"message,omitempty"`
	At        time.Time `json:"at"`
}

// ToolRouter executes a named tool with resolved inputs.
type ToolRouter interface {
	Execute(ctx context.Context, toolName string, inputs map[string]string) (string, error)
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
}

func NewExecutor(toolRouter ToolRouter, traceEmitter TraceEmitter) *Executor {
	return &Executor{
		ToolRouter:   toolRouter,
		TraceEmitter: traceEmitter,
		NodeTimeout:  defaultNodeTimeout,
		MaxRetries:   defaultMaxRetries,
	}
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
				FromState: string(prev),
				ToState:   string(next),
				Message:   message,
				At:        time.Now().UTC(),
			})
		}
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

	resolveInputs := func(node DAGNode) (map[string]string, error) {
		resolved := make(map[string]string, len(node.Inputs))
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

				for attempt := 0; attempt <= maxRetries; attempt++ {
					if attempt > 0 {
						setState(node.ID, NodeStatusRetrying, "", nil, attempt, "retrying")
					}

					setState(node.ID, NodeStatusRunning, "", nil, attempt, "running")

					inputs, err := resolveInputs(node)
					if err != nil {
						if attempt < maxRetries {
							continue
						}
						setState(node.ID, NodeStatusFailed, "", err, attempt, "input_resolution_failed")
						return
					}

					nodeCtx, cancel := context.WithTimeout(ctx, nodeTimeout)
					output, err := e.ToolRouter.Execute(nodeCtx, node.Tool, inputs)
					cancel()
					if err == nil {
						setState(node.ID, NodeStatusSuccess, output, nil, attempt, "success")
						return
					}

					if attempt < maxRetries {
						continue
					}

					setState(node.ID, NodeStatusFailed, "", err, attempt, "failed")
					return
				}
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
