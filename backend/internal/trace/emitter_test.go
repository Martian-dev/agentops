package trace

import (
	"testing"
	"time"
)

func TestEmitterEmit_DropsWhenChannelFull(t *testing.T) {
	e := &Emitter{
		runID: "run-1",
		ch:    make(chan TraceEvent, 1),
		// No db needed for this unit test; we only validate non-blocking enqueue/drop.
	}

	first := TraceEvent{EventType: "node_start", Timestamp: time.Now().UTC()}
	second := TraceEvent{EventType: "node_complete", Timestamp: time.Now().UTC()}

	e.Emit(first)
	e.Emit(second)

	if got := len(e.ch); got != 1 {
		t.Fatalf("expected channel length 1 after overflow drop, got %d", got)
	}

	popped := <-e.ch
	if popped.EventType != "node_start" {
		t.Fatalf("expected first event retained, got %s", popped.EventType)
	}
}

func TestMapTransitionEventType(t *testing.T) {
	cases := []struct {
		toState string
		want    string
	}{
		{toState: "running", want: "node_start"},
		{toState: "success", want: "node_complete"},
		{toState: "failed", want: "node_failed"},
		{toState: "retrying", want: "tool_called"},
	}

	for _, tc := range cases {
		got := mapTransitionEventType(tc.toState)
		if got != tc.want {
			t.Fatalf("state=%s expected=%s got=%s", tc.toState, tc.want, got)
		}
	}
}
