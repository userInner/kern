package engine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/userInner/kern/internal/plan"
	"github.com/userInner/kern/internal/store/sqlite"
	"github.com/userInner/kern/internal/task"
	"github.com/userInner/kern/internal/verification"
)

func TestEnginePersistsCheckpointsAndReleasesLease(t *testing.T) {
	store := openEngineTestStore(t)
	created, err := store.CreateTask(
		t.Context(),
		"test",
		"Inspect this repository, implement the requested file change, then run tests and verify the diff.",
	)
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	engine := New(
		t.Context(),
		store,
		processorFunc(func(context.Context, task.Task) (string, error) { return "done", nil }),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		1,
	)
	t.Cleanup(engine.Close)
	if err := engine.Enqueue(t.Context(), created.ID); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	completed := waitForTaskStatus(t, store, created.ID, task.StatusCompleted)
	if completed.LeaseOwner != "" || completed.LeaseExpiresAt != nil {
		t.Fatalf("completed task retained lease: %#v", completed)
	}
	checkpoint, err := store.LatestCheckpoint(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("LatestCheckpoint() error = %v", err)
	}
	if checkpoint.Ordinal != 3 || checkpoint.Reason != "verification completed" {
		t.Fatalf("LatestCheckpoint() = %#v", checkpoint)
	}
	currentPlan, err := store.CurrentPlan(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("CurrentPlan() error = %v", err)
	}
	if currentPlan.Status != plan.StatusCompleted || len(currentPlan.Steps) != 3 {
		t.Fatalf("CurrentPlan() = %#v", currentPlan)
	}
	for _, step := range currentPlan.Steps {
		if step.Status != plan.StepStatusCompleted {
			t.Fatalf("plan step = %#v", step)
		}
	}
}

func TestEngineMarksExecutePlanStepFailed(t *testing.T) {
	store := openEngineTestStore(t)
	created, err := store.CreateTask(
		t.Context(),
		"test",
		"Inspect this repository, implement the requested change, and run tests.",
	)
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	engine := New(
		t.Context(),
		store,
		processorFunc(func(context.Context, task.Task) (string, error) {
			return "", errors.New("compiler failed")
		}),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		1,
	)
	t.Cleanup(engine.Close)
	if err := engine.Enqueue(t.Context(), created.ID); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	waitForTaskStatus(t, store, created.ID, task.StatusFailed)
	currentPlan, err := store.CurrentPlan(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("CurrentPlan() error = %v", err)
	}
	if currentPlan.Status != plan.StatusFailed ||
		currentPlan.Steps[0].Status != plan.StepStatusCompleted ||
		currentPlan.Steps[1].Status != plan.StepStatusFailed ||
		currentPlan.Steps[1].Failure != "compiler failed" ||
		currentPlan.Steps[2].Status != plan.StepStatusPending {
		t.Fatalf("failed plan = %#v", currentPlan)
	}
}

func TestEngineMapsPartialVerificationToPartialCompletion(t *testing.T) {
	store := openEngineTestStore(t)
	created, err := store.CreateTask(t.Context(), "test", "return evidence")
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	engine := NewWithVerifier(
		t.Context(),
		store,
		processorFunc(func(context.Context, task.Task) (string, error) { return "done", nil }),
		verifierFunc(func(context.Context, task.Task, string) (verification.Report, error) {
			return verification.Report{Checks: []verification.CheckResult{{
				Verifier: "test.required_command",
				Status:   verification.StatusNotRun,
				Required: true,
				Summary:  "required command was not run",
				Evidence: []verification.Evidence{{
					Kind: "operation_ledger", Ref: "attempt:test", Summary: "no command operation",
				}},
			}}}, nil
		}),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		1,
	)
	t.Cleanup(engine.Close)
	if err := engine.Enqueue(t.Context(), created.ID); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	completed := waitForTaskStatus(t, store, created.ID, task.StatusPartiallyCompleted)
	if completed.Result != "done" || completed.ErrorMessage != "" {
		t.Fatalf("partially completed task = %#v", completed)
	}
	items, err := store.ListVerifications(t.Context(), created.ID, completed.ActiveAttemptID)
	if err != nil || len(items) != 2 || items[1].Verifier != "core.aggregate" ||
		items[1].Status != string(verification.StatusPartial) {
		t.Fatalf("ListVerifications() = %#v, %v", items, err)
	}
}

func TestEnginePauseDoesNotOverwriteDurableState(t *testing.T) {
	store := openEngineTestStore(t)
	created, err := store.CreateTask(t.Context(), "test", "pause a running task")
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	started := make(chan struct{})
	var calls atomic.Int32
	processor := processorFunc(func(ctx context.Context, _ task.Task) (string, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		}
		return "resumed", nil
	})
	engine := New(
		t.Context(),
		store,
		processor,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		1,
	)
	t.Cleanup(engine.Close)
	if err := engine.Enqueue(t.Context(), created.ID); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("processor did not start")
	}
	paused, err := store.PauseTask(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("PauseTask() error = %v", err)
	}
	engine.CancelExecution(created.ID, context.Canceled)
	time.Sleep(20 * time.Millisecond)
	loaded, err := store.GetTask(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if loaded.Status != task.StatusWaitingInput || loaded.ActiveAttemptID != paused.ActiveAttemptID {
		t.Fatalf("paused task overwritten: %#v", loaded)
	}

	resumed, err := store.CreateAttempt(t.Context(), created.ID, "resume test")
	if err != nil {
		t.Fatalf("CreateAttempt() error = %v", err)
	}
	if err := engine.Enqueue(t.Context(), resumed.ID); err != nil {
		t.Fatalf("Enqueue(resume) error = %v", err)
	}
	waitForTaskStatus(t, store, created.ID, task.StatusCompleted)
}

type processorFunc func(context.Context, task.Task) (string, error)

func (f processorFunc) Process(ctx context.Context, item task.Task) (string, error) {
	return f(ctx, item)
}

type verifierFunc func(context.Context, task.Task, string) (verification.Report, error)

func (f verifierFunc) Verify(
	ctx context.Context,
	item task.Task,
	result string,
) (verification.Report, error) {
	return f(ctx, item, result)
}

func openEngineTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return store
}

func waitForTaskStatus(
	t *testing.T,
	store *sqlite.Store,
	taskID string,
	want task.Status,
) task.Task {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		item, err := store.GetTask(t.Context(), taskID)
		if err != nil {
			t.Fatalf("GetTask() error = %v", err)
		}
		if item.Status == want {
			return item
		}
		time.Sleep(10 * time.Millisecond)
	}
	item, err := store.GetTask(t.Context(), taskID)
	if err != nil {
		t.Fatalf("GetTask(final) error = %v", err)
	}
	t.Fatalf("task status = %q, want %q", item.Status, want)
	return task.Task{}
}
