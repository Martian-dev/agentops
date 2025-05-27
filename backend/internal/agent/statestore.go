package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

// StateDelta is an immutable record of a single state change for a node.
// The state store is append-only: every Apply() produces a new delta rather
// than overwriting previous state in place.
type StateDelta struct {
	NodeID         string            `json:"node_id"`
	Version        int               `json:"version"`
	Status         NodeStatus        `json:"status"`
	Output         string            `json:"output,omitempty"`
	Error          string            `json:"error,omitempty"`
	Attempt        int               `json:"attempt"`
	Timestamp      time.Time         `json:"timestamp"`
	InputsUsed     map[string]string `json:"inputs_used,omitempty"`
	DurationMs     int64             `json:"duration_ms,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	PrevStatus     NodeStatus        `json:"prev_status,omitempty"`
}

// StateStore is an immutable, versioned state container for DAG execution.
// All state transitions are recorded as StateDelta entries. The store never
// overwrites data — it only appends. This design prevents the class of bugs
// where retries overwrite outputs or parallel nodes read partially applied
// state.
type StateStore struct {
	mu      sync.RWMutex
	deltas  []StateDelta
	version int

	// latest caches the most recent delta index per node for fast Snapshot().
	latest map[string]int
}

// NewStateStore creates an empty state store.
func NewStateStore() *StateStore {
	return &StateStore{
		deltas: make([]StateDelta, 0, 64),
		latest: make(map[string]int),
	}
}

// NewStateStoreFromDeltas reconstructs a StateStore from a list of deltas
// (e.g. loaded from a checkpoint).
func NewStateStoreFromDeltas(deltas []StateDelta) *StateStore {
	store := &StateStore{
		deltas: make([]StateDelta, len(deltas)),
		latest: make(map[string]int, len(deltas)),
	}
	copy(store.deltas, deltas)

	for i, d := range store.deltas {
		store.latest[d.NodeID] = i
		if d.Version > store.version {
			store.version = d.Version
		}
	}
	return store
}

// Apply records a new state transition for a node. It returns the version
// number assigned to this delta.
func (s *StateStore) Apply(delta StateDelta) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.version++
	delta.Version = s.version

	if delta.Timestamp.IsZero() {
		delta.Timestamp = time.Now().UTC()
	}

	// Record previous status for audit trail
	if idx, ok := s.latest[delta.NodeID]; ok {
		delta.PrevStatus = s.deltas[idx].Status
	}

	s.deltas = append(s.deltas, delta)
	s.latest[delta.NodeID] = len(s.deltas) - 1

	return s.version
}

// Snapshot returns a consistent point-in-time view of all node states.
// The returned map is a fresh copy — callers cannot corrupt the store.
func (s *StateStore) Snapshot() map[string]*NodeState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.snapshotLocked()
}

// snapshotLocked builds a snapshot without acquiring the lock (caller must hold it).
func (s *StateStore) snapshotLocked() map[string]*NodeState {
	result := make(map[string]*NodeState, len(s.latest))
	for nodeID, idx := range s.latest {
		d := s.deltas[idx]
		ns := &NodeState{
			Status:     d.Status,
			Output:     d.Output,
			RetryCount: d.Attempt,
		}
		if d.Error != "" {
			ns.Err = fmt.Errorf("%s", d.Error)
		}
		result[nodeID] = ns
	}
	return result
}

// SnapshotAt returns the state as it existed at a specific version number.
// This enables point-in-time debugging of execution state.
func (s *StateStore) SnapshotAt(version int) map[string]*NodeState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	latest := make(map[string]int)
	for i, d := range s.deltas {
		if d.Version > version {
			break
		}
		latest[d.NodeID] = i
	}

	result := make(map[string]*NodeState, len(latest))
	for nodeID, idx := range latest {
		d := s.deltas[idx]
		ns := &NodeState{
			Status:     d.Status,
			Output:     d.Output,
			RetryCount: d.Attempt,
		}
		if d.Error != "" {
			ns.Err = fmt.Errorf("%s", d.Error)
		}
		result[nodeID] = ns
	}
	return result
}

// History returns the full delta chain for a given node, ordered by version.
func (s *StateStore) History(nodeID string) []StateDelta {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var history []StateDelta
	for _, d := range s.deltas {
		if d.NodeID == nodeID {
			history = append(history, d)
		}
	}
	return history
}

// AllDeltas returns a copy of all deltas in the store.
func (s *StateStore) AllDeltas() []StateDelta {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]StateDelta, len(s.deltas))
	copy(result, s.deltas)
	return result
}

// Version returns the current global version number.
func (s *StateStore) Version() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.version
}

// NodeStatus returns the latest status for a node, or NodeStatusPending if unknown.
func (s *StateStore) NodeLatestStatus(nodeID string) NodeStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	idx, ok := s.latest[nodeID]
	if !ok {
		return NodeStatusPending
	}
	return s.deltas[idx].Status
}

// NodeOutput returns the latest output for a node. Returns empty string if none.
func (s *StateStore) NodeOutput(nodeID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	idx, ok := s.latest[nodeID]
	if !ok {
		return ""
	}
	return s.deltas[idx].Output
}

// CompletedNodes returns the set of node IDs that are in a terminal state
// (success or failed).
func (s *StateStore) CompletedNodes() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var completed []string
	for nodeID, idx := range s.latest {
		status := s.deltas[idx].Status
		if status == NodeStatusSuccess || status == NodeStatusFailed {
			completed = append(completed, nodeID)
		}
	}
	sort.Strings(completed)
	return completed
}

// SuccessfulNodes returns node IDs that completed successfully.
func (s *StateStore) SuccessfulNodes() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var nodes []string
	for nodeID, idx := range s.latest {
		if s.deltas[idx].Status == NodeStatusSuccess {
			nodes = append(nodes, nodeID)
		}
	}
	sort.Strings(nodes)
	return nodes
}

// StatusSnapshot returns a map of nodeID -> status string for all known nodes.
func (s *StateStore) StatusSnapshot() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]string, len(s.latest))
	for nodeID, idx := range s.latest {
		result[nodeID] = string(s.deltas[idx].Status)
	}
	return result
}

// ComputeIdempotencyKey generates a deterministic key from node ID, attempt,
// and resolved inputs. If a retry produces the same key as a prior successful
// execution, the cached output can be reused.
func ComputeIdempotencyKey(nodeID string, attempt int, inputs map[string]string) string {
	h := sha256.New()
	h.Write([]byte(nodeID))
	h.Write([]byte(fmt.Sprintf(":%d:", attempt)))

	// Sort keys for deterministic hashing
	keys := make([]string, 0, len(inputs))
	for k := range inputs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte("="))
		h.Write([]byte(inputs[k]))
		h.Write([]byte(";"))
	}

	return hex.EncodeToString(h.Sum(nil))[:16]
}

// MarshalDeltas serializes all deltas to JSON (for checkpoint persistence).
func (s *StateStore) MarshalDeltas() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return json.Marshal(s.deltas)
}
