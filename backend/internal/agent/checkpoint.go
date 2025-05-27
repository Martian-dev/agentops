package agent

import (
	"encoding/json"
	"fmt"
	"time"
)

// Checkpoint is a serializable snapshot of execution progress. It stores
// enough information to resume a failed run from the last completed tier,
// skipping nodes that already succeeded and reusing their cached outputs.
type Checkpoint struct {
	RunID          string        `json:"run_id"`
	PlanJSON       json.RawMessage `json:"plan_json"`
	Deltas         []StateDelta  `json:"deltas"`
	CompletedNodes []string      `json:"completed_nodes"`
	CompletedTiers int           `json:"completed_tiers"`
	CreatedAt      time.Time     `json:"created_at"`
	IdempotencyMap map[string]string `json:"idempotency_map"` // idempotency_key -> output
}

// NewCheckpoint creates a checkpoint from the current StateStore and plan.
func NewCheckpoint(runID string, plan *DAGPlan, store *StateStore, completedTiers int) (*Checkpoint, error) {
	if store == nil {
		return nil, fmt.Errorf("state store is required for checkpoint")
	}

	planJSON, err := json.Marshal(plan)
	if err != nil {
		return nil, fmt.Errorf("marshal plan for checkpoint: %w", err)
	}

	deltas := store.AllDeltas()

	// Build idempotency map from successful executions
	idempotencyMap := make(map[string]string)
	for _, d := range deltas {
		if d.Status == NodeStatusSuccess && d.IdempotencyKey != "" {
			idempotencyMap[d.IdempotencyKey] = d.Output
		}
	}

	return &Checkpoint{
		RunID:          runID,
		PlanJSON:       planJSON,
		Deltas:         deltas,
		CompletedNodes: store.SuccessfulNodes(),
		CompletedTiers: completedTiers,
		CreatedAt:      time.Now().UTC(),
		IdempotencyMap: idempotencyMap,
	}, nil
}

// Marshal serializes the checkpoint to JSON for database storage.
func (cp *Checkpoint) Marshal() ([]byte, error) {
	if cp == nil {
		return nil, fmt.Errorf("checkpoint is nil")
	}
	return json.Marshal(cp)
}

// UnmarshalCheckpoint deserializes a checkpoint from JSON.
func UnmarshalCheckpoint(data []byte) (*Checkpoint, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty checkpoint data")
	}

	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("unmarshal checkpoint: %w", err)
	}

	if cp.RunID == "" {
		return nil, fmt.Errorf("checkpoint missing run_id")
	}

	return &cp, nil
}

// RestoreStateStore reconstructs a StateStore from checkpoint deltas.
func (cp *Checkpoint) RestoreStateStore() *StateStore {
	if cp == nil || len(cp.Deltas) == 0 {
		return NewStateStore()
	}
	return NewStateStoreFromDeltas(cp.Deltas)
}

// RestorePlan deserializes the DAG plan from the checkpoint.
func (cp *Checkpoint) RestorePlan() (*DAGPlan, error) {
	if cp == nil {
		return nil, fmt.Errorf("checkpoint is nil")
	}
	var plan DAGPlan
	if err := json.Unmarshal(cp.PlanJSON, &plan); err != nil {
		return nil, fmt.Errorf("unmarshal plan from checkpoint: %w", err)
	}
	return &plan, nil
}

// IsNodeCompleted checks whether a node was already completed in this checkpoint.
func (cp *Checkpoint) IsNodeCompleted(nodeID string) bool {
	if cp == nil {
		return false
	}
	for _, id := range cp.CompletedNodes {
		if id == nodeID {
			return true
		}
	}
	return false
}

// LookupIdempotent checks if there is a cached output for the given
// idempotency key. Returns the output and true if found.
func (cp *Checkpoint) LookupIdempotent(key string) (string, bool) {
	if cp == nil || cp.IdempotencyMap == nil {
		return "", false
	}
	output, ok := cp.IdempotencyMap[key]
	return output, ok
}

// ExecutionResult is the enriched return type from the executor, containing
// the final state snapshot, full delta history, and optional checkpoint data.
type ExecutionResult struct {
	// FinalStates is the resolved state for each node at execution end.
	FinalStates map[string]*NodeState `json:"final_states"`

	// Deltas is the complete ordered history of all state transitions.
	Deltas []StateDelta `json:"deltas"`

	// Checkpoint is the last checkpoint saved (can be used for resume).
	Checkpoint *Checkpoint `json:"checkpoint,omitempty"`

	// TotalTokens is the cumulative token count.
	TotalTokens int64 `json:"total_tokens"`
}
