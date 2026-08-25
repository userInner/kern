package tracecontext

import (
	"encoding/json"
	"testing"
)

func TestAttemptTraceIDIsStableAndOpaque(t *testing.T) {
	first := AttemptTraceID("attempt-1")
	if len(first) != 32 || first != AttemptTraceID("attempt-1") {
		t.Fatalf("AttemptTraceID() = %q", first)
	}
	if first == AttemptTraceID("attempt-2") || first == "attempt-1" {
		t.Fatal("AttemptTraceID must be unique and opaque")
	}
}

func TestNewRequestContinuesValidTraceparent(t *testing.T) {
	parent := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	identity := NewRequest(parent)
	if identity.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || len(identity.SpanID) != 16 {
		t.Fatalf("NewRequest() = %#v", identity)
	}
	if got := identity.Traceparent(); len(got) != 55 {
		t.Fatalf("Traceparent() = %q", got)
	}
}

func TestNewRequestRejectsInvalidOrZeroParent(t *testing.T) {
	for _, parent := range []string{"invalid", "00-00000000000000000000000000000000-00f067aa0ba902b7-01"} {
		identity := NewRequest(parent)
		if len(identity.TraceID) != 32 || identity.TraceID == "00000000000000000000000000000000" {
			t.Fatalf("NewRequest(%q) = %#v", parent, identity)
		}
	}
}

func TestEventSpanCorrelatesModelBoundaries(t *testing.T) {
	started, parent := EventSpan("attempt-1", "model.call_started", json.RawMessage(`{"call_id":"2:0"}`))
	completed, completedParent := EventSpan("attempt-1", "model.call_completed", json.RawMessage(`{"call_id":"2:0"}`))
	if started != completed || parent != AttemptSpanID("attempt-1") || completedParent != parent {
		t.Fatalf("model spans started=%q completed=%q parent=%q/%q", started, completed, parent, completedParent)
	}
	root, rootParent := EventSpan("attempt-1", "task.completed", nil)
	if root != AttemptSpanID("attempt-1") || rootParent != "" {
		t.Fatalf("task root span = %q parent=%q", root, rootParent)
	}
}
