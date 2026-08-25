package sqlite

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/userInner/kern/internal/operation"
	"github.com/userInner/kern/internal/task"
)

func TestObservabilitySnapshotUsesDurableFacts(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	created, err := store.CreateTask(t.Context(), "metrics", "exercise durable metrics")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendEvent(t.Context(), created.ID, created.ActiveAttemptID, "model.call_completed", map[string]any{
		"status": "succeeded", "duration_ms": 125,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendEvent(t.Context(), created.ID, created.ActiveAttemptID, "model.call_started", map[string]any{
		"call_id": "1:0", "retry": 0,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendEvent(t.Context(), created.ID, created.ActiveAttemptID, "model.usage", map[string]any{
		"input_tokens": 10, "output_tokens": 5, "reasoning_tokens": 3,
		"cached_tokens": 2, "cost_micros": 25,
	}); err != nil {
		t.Fatal(err)
	}
	op, _, err := store.CreateOperation(
		t.Context(), created.ID, "inspect", "metrics-operation", operation.EffectRead, map[string]string{"path": "."},
	)
	if err != nil {
		t.Fatal(err)
	}
	op, err = store.TransitionOperation(t.Context(), op.ID, operation.StatusPrepared, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	op, err = store.TransitionOperation(t.Context(), op.ID, operation.StatusExecuting, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.TransitionOperation(t.Context(), op.ID, operation.StatusSucceeded, &operation.Result{
		Timing: json.RawMessage(`{"duration_ms":75}`),
	}, "ok", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []task.Status{task.StatusPlanning, task.StatusRunning, task.StatusVerifying, task.StatusCompleted} {
		if err := store.Transition(t.Context(), created.ID, status, "done", ""); err != nil {
			t.Fatal(err)
		}
	}

	snapshot, err := store.ObservabilitySnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.TasksCreated != 1 || snapshot.TaskCurrent["completed"] != 1 ||
		snapshot.Attempts["completed"] != 1 || snapshot.TaskDuration.Count != 1 {
		t.Fatalf("task metrics = %#v", snapshot)
	}
	if snapshot.ModelCallsStarted != 1 || snapshot.ModelCalls["succeeded"] != 1 || snapshot.ModelTokens["input"] != 10 ||
		snapshot.ModelCostMicros != 25 || snapshot.ModelCallDuration.Count != 1 {
		t.Fatalf("model metrics = %#v", snapshot)
	}
	if snapshot.Operations["inspect\x00read\x00succeeded"] != 1 || snapshot.OperationDuration.Count != 1 {
		t.Fatalf("operation metrics = %#v", snapshot)
	}
	events, err := store.EventsAfter(t.Context(), created.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if len(event.TraceID) != 32 || len(event.SpanID) != 16 {
			t.Fatalf("event trace identity = %#v", event)
		}
	}
}
