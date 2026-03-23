package trace

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Martian-dev/agentops/internal/agent"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultEmitterBufferSize = 256
	defaultWriteTimeout      = 2 * time.Second
)

// TraceEvent is the JSON payload appended to agent_traces.events.
type TraceEvent struct {
	Timestamp  time.Time `json:"timestamp"`
	EventType  string    `json:"event_type"`
	NodeID     string    `json:"node_id,omitempty"`
	ToolName   string    `json:"tool_name,omitempty"`
	DurationMs int64     `json:"duration_ms,omitempty"`
	TokenIn    int       `json:"token_in,omitempty"`
	TokenOut   int       `json:"token_out,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// Emitter decouples trace emission from trace persistence via a buffered channel.
type Emitter struct {
	runID string
	ch    chan TraceEvent
	db    *pgxpool.Pool

	done chan struct{}
}

// NewEmitter creates the trace row (if missing), starts the background writer,
// and returns a non-blocking emitter.
func NewEmitter(ctx context.Context, db *pgxpool.Pool, runID string, bufferSize int) (*Emitter, error) {
	if db == nil {
		return nil, fmt.Errorf("trace emitter requires a database pool")
	}
	if runID == "" {
		return nil, fmt.Errorf("trace emitter requires runID")
	}
	if bufferSize <= 0 {
		bufferSize = defaultEmitterBufferSize
	}

	// Pre-create an empty events array once per run to avoid append-time row races.
	if _, err := db.Exec(ctx, `
		INSERT INTO agent_traces (run_id, events)
		SELECT $1::uuid, '[]'::jsonb
		WHERE NOT EXISTS (
			SELECT 1 FROM agent_traces WHERE run_id = $1::uuid
		)
	`, runID); err != nil {
		return nil, fmt.Errorf("create trace row for run_id=%s: %w", runID, err)
	}

	e := &Emitter{
		runID: runID,
		ch:    make(chan TraceEvent, bufferSize),
		db:    db,
		done:  make(chan struct{}),
	}
	go e.drain()
	return e, nil
}

// Emit is intentionally non-blocking; if the buffer is full, the event is dropped.
func (e *Emitter) Emit(event TraceEvent) {
	if e == nil {
		return
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}

	select {
	case e.ch <- event:
	default:
		// Channel full: drop rather than stall executor throughput.
	}
}

// Close drains already-queued events and stops the background worker.
func (e *Emitter) Close(ctx context.Context) error {
	if e == nil {
		return nil
	}

	close(e.ch)
	select {
	case <-e.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Emitter) drain() {
	defer close(e.done)

	for event := range e.ch {
		if err := e.appendEvent(event); err != nil {
			// Swallow persistence errors to preserve non-blocking semantics.
			continue
		}
	}
}

func (e *Emitter) appendEvent(event TraceEvent) error {
	payload, err := json.Marshal([]TraceEvent{event})
	if err != nil {
		return fmt.Errorf("marshal trace event: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultWriteTimeout)
	defer cancel()

	_, err = e.db.Exec(ctx, `
		UPDATE agent_traces
		SET events = events || $1::jsonb
		WHERE run_id = $2::uuid
	`, payload, e.runID)
	if err != nil {
		return fmt.Errorf("append trace event for run_id=%s: %w", e.runID, err)
	}
	return nil
}

// ExecutorEmitter adapts this package to the current executor TraceEmitter interface.
type ExecutorEmitter struct {
	db         *pgxpool.Pool
	bufferSize int

	mu       sync.Mutex
	emitters map[string]*Emitter
}

func NewExecutorEmitter(db *pgxpool.Pool, bufferSize int) *ExecutorEmitter {
	if bufferSize <= 0 {
		bufferSize = defaultEmitterBufferSize
	}
	return &ExecutorEmitter{
		db:         db,
		bufferSize: bufferSize,
		emitters:   make(map[string]*Emitter),
	}
}

// Emit is non-blocking in steady state: it maps executor transitions to TraceEvent
// and enqueues them on a run-specific emitter.
func (e *ExecutorEmitter) Emit(ctx context.Context, runID string, ev agent.TraceEvent) error {
	emitter, err := e.getOrCreateEmitter(ctx, runID)
	if err != nil {
		return err
	}

	eventType := mapTransitionEventType(ev.ToState)
	traceEvent := TraceEvent{
		Timestamp: ev.At,
		EventType: eventType,
		NodeID:    ev.NodeID,
	}
	if ev.ToState == string(agent.NodeStatusFailed) {
		traceEvent.Error = ev.Message
	}

	emitter.Emit(traceEvent)
	return nil
}

func (e *ExecutorEmitter) CloseRun(ctx context.Context, runID string) error {
	e.mu.Lock()
	emitter := e.emitters[runID]
	delete(e.emitters, runID)
	e.mu.Unlock()

	if emitter == nil {
		return nil
	}
	return emitter.Close(ctx)
}

func (e *ExecutorEmitter) getOrCreateEmitter(ctx context.Context, runID string) (*Emitter, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if existing := e.emitters[runID]; existing != nil {
		return existing, nil
	}

	emitter, err := NewEmitter(ctx, e.db, runID, e.bufferSize)
	if err != nil {
		return nil, err
	}
	e.emitters[runID] = emitter
	return emitter, nil
}

func mapTransitionEventType(toState string) string {
	switch toState {
	case string(agent.NodeStatusRunning):
		return "node_start"
	case string(agent.NodeStatusSuccess):
		return "node_complete"
	case string(agent.NodeStatusFailed):
		return "node_failed"
	default:
		return "tool_called"
	}
}
