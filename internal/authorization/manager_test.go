package authorization

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/userInner/kern/internal/approval"
	"github.com/userInner/kern/internal/operation"
	"github.com/userInner/kern/internal/policy"
	"github.com/userInner/kern/internal/store/sqlite"
	"github.com/userInner/kern/internal/task"
)

func TestManagerWaitsForDurableApprovalDecision(t *testing.T) {
	tests := []struct {
		name         string
		decision     approval.Decision
		wantPrepared bool
	}{
		{name: "approved", decision: approval.DecisionApproved, wantPrepared: true},
		{name: "denied", decision: approval.DecisionDenied, wantPrepared: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
			if err != nil {
				t.Fatalf("sqlite.Open() error = %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			item, err := store.CreateTask(t.Context(), "authorize", "change a file")
			if err != nil {
				t.Fatalf("CreateTask() error = %v", err)
			}
			if err := store.Transition(t.Context(), item.ID, task.StatusPlanning, "", ""); err != nil {
				t.Fatalf("Transition(planning) error = %v", err)
			}
			if err := store.Transition(t.Context(), item.ID, task.StatusRunning, "", ""); err != nil {
				t.Fatalf("Transition(running) error = %v", err)
			}
			item, err = store.GetTask(t.Context(), item.ID)
			if err != nil {
				t.Fatalf("GetTask() error = %v", err)
			}
			op, _, err := store.CreateOperation(
				t.Context(),
				item.ID,
				"change",
				"change-1",
				operation.EffectLocalWrite,
				json.RawMessage(`{"action":"write_file","path":"note.txt","content":"ok"}`),
			)
			if err != nil {
				t.Fatalf("CreateOperation() error = %v", err)
			}
			manager, err := New(store, policy.New(), Config{
				ApprovalTTL: time.Second,
				PollPeriod:  time.Millisecond,
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			type result struct {
				op  operation.Operation
				err error
			}
			resultCh := make(chan result, 1)
			go func() {
				prepared, authorizeErr := manager.Authorize(t.Context(), item, op)
				resultCh <- result{op: prepared, err: authorizeErr}
			}()

			request := waitForApproval(t, store, item.ID)
			waiting, err := store.GetTask(t.Context(), item.ID)
			if err != nil || waiting.Status != task.StatusWaitingApproval {
				t.Fatalf("waiting task = %#v, %v", waiting, err)
			}
			if _, err := store.DecideApproval(t.Context(), request.ID, tt.decision, "test-user"); err != nil {
				t.Fatalf("DecideApproval() error = %v", err)
			}
			select {
			case authorized := <-resultCh:
				if tt.wantPrepared {
					if authorized.err != nil || authorized.op.Status != operation.StatusPrepared {
						t.Fatalf("Authorize() = %#v, %v", authorized.op, authorized.err)
					}
				} else if !errors.Is(authorized.err, ErrDenied) || authorized.op.Status != operation.StatusCancelled {
					t.Fatalf("Authorize() = %#v, %v", authorized.op, authorized.err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Authorize() did not observe approval decision")
			}
			resumed, err := store.GetTask(t.Context(), item.ID)
			if err != nil || resumed.Status != task.StatusRunning {
				t.Fatalf("resumed task = %#v, %v", resumed, err)
			}
		})
	}
}

func TestManagerCancelsApprovedOperationWhenExecutionContextStops(t *testing.T) {
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	item, err := store.CreateTask(t.Context(), "authorize", "change a file")
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	if err := store.Transition(t.Context(), item.ID, task.StatusPlanning, "", ""); err != nil {
		t.Fatalf("Transition(planning) error = %v", err)
	}
	if err := store.Transition(t.Context(), item.ID, task.StatusRunning, "", ""); err != nil {
		t.Fatalf("Transition(running) error = %v", err)
	}
	item, err = store.GetTask(t.Context(), item.ID)
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	op, _, err := store.CreateOperation(
		t.Context(),
		item.ID,
		"change",
		"change-interrupted",
		operation.EffectLocalWrite,
		json.RawMessage(`{"action":"write_file","path":"note.txt","content":"ok"}`),
	)
	if err != nil {
		t.Fatalf("CreateOperation() error = %v", err)
	}
	manager, err := New(store, policy.New(), Config{
		ApprovalTTL: time.Minute,
		PollPeriod:  time.Hour,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	authorizeCtx, cancelAuthorize := context.WithCancel(t.Context())
	t.Cleanup(cancelAuthorize)
	resultCh := make(chan error, 1)
	go func() {
		_, authorizeErr := manager.Authorize(authorizeCtx, item, op)
		resultCh <- authorizeErr
	}()

	request := waitForApproval(t, store, item.ID)
	if _, err := store.DecideApproval(
		t.Context(),
		request.ID,
		approval.DecisionApproved,
		"test-user",
	); err != nil {
		t.Fatalf("DecideApproval() error = %v", err)
	}
	prepared, err := store.GetOperation(t.Context(), op.ID)
	if err != nil || prepared.Status != operation.StatusPrepared {
		t.Fatalf("prepared operation = %#v, %v", prepared, err)
	}

	cancelAuthorize()
	select {
	case authorizeErr := <-resultCh:
		if !errors.Is(authorizeErr, context.Canceled) {
			t.Fatalf("Authorize() error = %v", authorizeErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Authorize() did not stop after context cancellation")
	}

	cancelled, err := store.GetOperation(t.Context(), op.ID)
	if err != nil {
		t.Fatalf("GetOperation() error = %v", err)
	}
	if cancelled.Status != operation.StatusCancelled || cancelled.ErrorCode != "authorization_interrupted" {
		t.Fatalf("cancelled operation = %#v", cancelled)
	}
	resumed, err := store.GetTask(t.Context(), item.ID)
	if err != nil || resumed.Status != task.StatusRunning {
		t.Fatalf("resumed task = %#v, %v", resumed, err)
	}
}

func TestManagerCleansPendingApprovalWhenOperationReadIsCancelled(t *testing.T) {
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	item, err := store.CreateTask(t.Context(), "authorize", "change a file")
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	if err := store.Transition(t.Context(), item.ID, task.StatusPlanning, "", ""); err != nil {
		t.Fatalf("Transition(planning) error = %v", err)
	}
	if err := store.Transition(t.Context(), item.ID, task.StatusRunning, "", ""); err != nil {
		t.Fatalf("Transition(running) error = %v", err)
	}
	item, err = store.GetTask(t.Context(), item.ID)
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	op, _, err := store.CreateOperation(
		t.Context(),
		item.ID,
		"change",
		"change-cancelled-read",
		operation.EffectLocalWrite,
		json.RawMessage(`{"action":"write_file","path":"note.txt","content":"ok"}`),
	)
	if err != nil {
		t.Fatalf("CreateOperation() error = %v", err)
	}
	blockingStore := &cancelledOperationReadStore{
		Store:   store,
		entered: make(chan struct{}),
	}
	manager, err := New(blockingStore, policy.New(), Config{
		ApprovalTTL: time.Minute,
		PollPeriod:  time.Hour,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	authorizeCtx, cancelAuthorize := context.WithCancel(t.Context())
	resultCh := make(chan error, 1)
	go func() {
		_, authorizeErr := manager.Authorize(authorizeCtx, item, op)
		resultCh <- authorizeErr
	}()
	select {
	case <-blockingStore.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Authorize() did not start the operation status read")
	}
	cancelAuthorize()

	select {
	case authorizeErr := <-resultCh:
		if !errors.Is(authorizeErr, context.Canceled) {
			t.Fatalf("Authorize() error = %v", authorizeErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Authorize() did not stop after context cancellation")
	}

	resumed, err := store.GetTask(t.Context(), item.ID)
	if err != nil || resumed.Status != task.StatusRunning {
		t.Fatalf("resumed task = %#v, %v", resumed, err)
	}
	pending, err := store.PendingApprovals(t.Context(), item.ID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("PendingApprovals() = %#v, %v", pending, err)
	}
	cancelled, err := store.GetOperation(t.Context(), op.ID)
	if err != nil || cancelled.Status != operation.StatusCancelled {
		t.Fatalf("cancelled operation = %#v, %v", cancelled, err)
	}
}

type cancelledOperationReadStore struct {
	*sqlite.Store
	entered chan struct{}
	once    sync.Once
}

func (s *cancelledOperationReadStore) GetOperation(
	ctx context.Context,
	_ string,
) (operation.Operation, error) {
	s.once.Do(func() { close(s.entered) })
	<-ctx.Done()
	return operation.Operation{}, ctx.Err()
}

func waitForApproval(t *testing.T, store *sqlite.Store, taskID string) approval.Request {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		requests, err := store.PendingApprovals(t.Context(), taskID)
		if err != nil {
			t.Fatalf("PendingApprovals() error = %v", err)
		}
		if len(requests) == 1 {
			return requests[0]
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("approval request was not persisted")
	return approval.Request{}
}
