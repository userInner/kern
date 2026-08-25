package sqlite

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/userInner/kern/internal/approval"
	"github.com/userInner/kern/internal/operation"
	"github.com/userInner/kern/internal/task"
)

func TestStoreOperationIdempotencyIsConcurrentSafe(t *testing.T) {
	store := openTestStore(t)
	createdTask := createTestTask(t, store)
	const callers = 8
	ids := make(chan string, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			created, _, err := store.CreateOperation(
				t.Context(),
				createdTask.ID,
				"inspect.read",
				"concurrent-read",
				operation.EffectRead,
				map[string]string{"path": "README.md"},
			)
			if err != nil {
				errs <- err
				return
			}
			ids <- created.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatalf("CreateOperation() concurrent error = %v", err)
	}
	var wantID string
	count := 0
	for itemID := range ids {
		count++
		if wantID == "" {
			wantID = itemID
		}
		if itemID != wantID {
			t.Fatalf("operation IDs differ: %q and %q", wantID, itemID)
		}
	}
	if count != callers {
		t.Fatalf("successful callers = %d, want %d", count, callers)
	}
}

func TestStoreOperationIdempotencyAndRecovery(t *testing.T) {
	store := openTestStore(t)
	createdTask := createTestTask(t, store)
	input := map[string]string{"path": "README.md"}

	created, wasCreated, err := store.CreateOperation(
		t.Context(),
		createdTask.ID,
		"inspect.read",
		"read-readme",
		operation.EffectRead,
		input,
	)
	if err != nil {
		t.Fatalf("CreateOperation() error = %v", err)
	}
	if !wasCreated {
		t.Fatal("CreateOperation() created = false, want true")
	}
	replayed, wasCreated, err := store.CreateOperation(
		t.Context(),
		createdTask.ID,
		"inspect.read",
		"read-readme",
		operation.EffectRead,
		input,
	)
	if err != nil {
		t.Fatalf("CreateOperation(replay) error = %v", err)
	}
	if wasCreated || replayed.ID != created.ID {
		t.Fatalf("CreateOperation(replay) = %#v, created %v", replayed, wasCreated)
	}
	if _, _, err := store.CreateOperation(
		t.Context(),
		createdTask.ID,
		"inspect.read",
		"read-readme",
		operation.EffectRead,
		map[string]string{"path": "go.mod"},
	); !errors.Is(err, operation.ErrDuplicate) {
		t.Fatalf("CreateOperation(conflict) error = %v, want ErrDuplicate", err)
	}

	prepared, err := store.TransitionOperation(
		t.Context(),
		created.ID,
		operation.StatusPrepared,
		nil,
		"",
		"",
	)
	if err != nil {
		t.Fatalf("TransitionOperation(prepared) error = %v", err)
	}
	recovery := []byte(`{"path":"README.md","before_sha256":"before","after_sha256":"after"}`)
	prepared, err = store.SetOperationRecovery(t.Context(), prepared.ID, recovery)
	if err != nil || string(prepared.Recovery) != string(recovery) {
		t.Fatalf("SetOperationRecovery() = %#v, %v", prepared, err)
	}
	replayedRecovery, err := store.SetOperationRecovery(t.Context(), prepared.ID, recovery)
	if err != nil || string(replayedRecovery.Recovery) != string(recovery) {
		t.Fatalf("SetOperationRecovery(replay) = %#v, %v", replayedRecovery, err)
	}
	if _, err := store.SetOperationRecovery(
		t.Context(),
		prepared.ID,
		[]byte(`{"path":"other"}`),
	); !errors.Is(err, operation.ErrDuplicate) {
		t.Fatalf("SetOperationRecovery(conflict) error = %v", err)
	}
	if _, err := store.TransitionOperation(
		t.Context(),
		prepared.ID,
		operation.StatusExecuting,
		nil,
		"",
		"",
	); err != nil {
		t.Fatalf("TransitionOperation(executing) error = %v", err)
	}
	count, err := store.MarkInterruptedOperationsUnknown(t.Context(), createdTask.ID)
	if err != nil {
		t.Fatalf("MarkInterruptedOperationsUnknown() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("MarkInterruptedOperationsUnknown() count = %d, want 1", count)
	}
	unknown, err := store.GetOperation(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetOperation() error = %v", err)
	}
	if unknown.Status != operation.StatusUncertain {
		t.Fatalf("GetOperation() status = %q, want unknown", unknown.Status)
	}
	uncertain, err := store.ListUncertainOperations(t.Context(), createdTask.ID)
	if err != nil || len(uncertain) != 1 || uncertain[0].ID != created.ID {
		t.Fatalf("ListUncertainOperations() = %#v, %v", uncertain, err)
	}
	receipt, err := store.ResolveUncertainOperation(
		t.Context(),
		created.ID,
		operation.ResolutionSucceeded,
		"local-user",
	)
	if err != nil {
		t.Fatalf("ResolveUncertainOperation() error = %v", err)
	}
	if receipt.OperationID != created.ID || receipt.Resolution != operation.ResolutionSucceeded {
		t.Fatalf("ResolveUncertainOperation() = %#v", receipt)
	}
	resolved, err := store.GetOperation(t.Context(), created.ID)
	if err != nil || resolved.Status != operation.StatusSucceeded {
		t.Fatalf("resolved operation = %#v, %v", resolved, err)
	}
	uncertain, err = store.ListUncertainOperations(t.Context(), createdTask.ID)
	if err != nil || len(uncertain) != 0 {
		t.Fatalf("ListUncertainOperations(resolved) = %#v, %v", uncertain, err)
	}
	replayedReceipt, err := store.ResolveUncertainOperation(
		t.Context(),
		created.ID,
		operation.ResolutionSucceeded,
		"local-user",
	)
	if err != nil || replayedReceipt.ID != receipt.ID {
		t.Fatalf("ResolveUncertainOperation(replay) = %#v, %v", replayedReceipt, err)
	}
	if _, err := store.ResolveUncertainOperation(
		t.Context(),
		created.ID,
		operation.ResolutionNotExecuted,
		"local-user",
	); !errors.Is(err, operation.ErrAlreadyResolved) {
		t.Fatalf("ResolveUncertainOperation(conflict) error = %v", err)
	}
}

func TestResolveUncertainOperationAsNotExecuted(t *testing.T) {
	store := openTestStore(t)
	createdTask := createTestTask(t, store)
	op, _, err := store.CreateOperation(
		t.Context(),
		createdTask.ID,
		"network.request",
		"uncertain-network-write",
		operation.EffectNetworkWrite,
		map[string]string{"url": "https://example.invalid/action"},
	)
	if err != nil {
		t.Fatalf("CreateOperation() error = %v", err)
	}
	for _, status := range []operation.Status{operation.StatusPrepared, operation.StatusExecuting} {
		if _, err := store.TransitionOperation(t.Context(), op.ID, status, nil, "", ""); err != nil {
			t.Fatalf("TransitionOperation(%s) error = %v", status, err)
		}
	}
	if _, err := store.MarkInterruptedOperationsUnknown(t.Context(), createdTask.ID); err != nil {
		t.Fatalf("MarkInterruptedOperationsUnknown() error = %v", err)
	}
	if _, err := store.ResolveUncertainOperation(
		t.Context(),
		op.ID,
		operation.ResolutionNotExecuted,
		"local-user",
	); err != nil {
		t.Fatalf("ResolveUncertainOperation() error = %v", err)
	}
	resolved, err := store.GetOperation(t.Context(), op.ID)
	if err != nil || resolved.Status != operation.StatusFailed ||
		resolved.ErrorCode != "user_confirmed_not_executed" {
		t.Fatalf("resolved operation = %#v, %v", resolved, err)
	}
}

func TestStoreApprovalReceiptPreservesScope(t *testing.T) {
	store := openTestStore(t)
	createdTask := createTestTask(t, store)
	transitionTaskToRunning(t, store, createdTask.ID)
	op, _, err := store.CreateOperation(
		t.Context(),
		createdTask.ID,
		"change.write",
		"write-readme",
		operation.EffectLocalWrite,
		map[string]string{"path": "README.md"},
	)
	if err != nil {
		t.Fatalf("CreateOperation() error = %v", err)
	}
	request, err := store.CreateApproval(
		t.Context(),
		op.ID,
		map[string]any{"paths": []string{"README.md"}, "effect": "local_write"},
		approval.RiskMedium,
		"modify README.md",
		time.Minute,
	)
	if err != nil {
		t.Fatalf("CreateApproval() error = %v", err)
	}
	pending, err := store.PendingApprovals(t.Context(), createdTask.ID)
	if err != nil {
		t.Fatalf("PendingApprovals() error = %v", err)
	}
	if len(pending) != 1 || pending[0].ID != request.ID {
		t.Fatalf("PendingApprovals() = %#v", pending)
	}
	receipt, err := store.DecideApproval(
		t.Context(),
		request.ID,
		approval.DecisionApproved,
		"local-user",
	)
	if err != nil {
		t.Fatalf("DecideApproval() error = %v", err)
	}
	if string(receipt.Scope) != string(request.Scope) {
		t.Fatalf("receipt scope = %s, want %s", receipt.Scope, request.Scope)
	}
	approved, err := store.GetOperation(t.Context(), op.ID)
	if err != nil {
		t.Fatalf("GetOperation() error = %v", err)
	}
	if approved.Status != operation.StatusPrepared {
		t.Fatalf("operation status = %q, want prepared", approved.Status)
	}
}

func TestListAttemptOperationsIncludesResultAndApprovalReceipt(t *testing.T) {
	store := openTestStore(t)
	createdTask := createTestTask(t, store)
	transitionTaskToRunning(t, store, createdTask.ID)
	op, _, err := store.CreateOperation(
		t.Context(),
		createdTask.ID,
		"execute",
		"run-tests",
		operation.EffectProcess,
		map[string]any{"argv": []string{"go", "test", "./..."}},
	)
	if err != nil {
		t.Fatalf("CreateOperation() error = %v", err)
	}
	request, err := store.CreateApproval(
		t.Context(),
		op.ID,
		map[string]string{"operation_id": op.ID},
		approval.RiskHigh,
		"Run tests",
		time.Minute,
	)
	if err != nil {
		t.Fatalf("CreateApproval() error = %v", err)
	}
	receipt, err := store.DecideApproval(
		t.Context(),
		request.ID,
		approval.DecisionApproved,
		"local-user",
	)
	if err != nil {
		t.Fatalf("DecideApproval() error = %v", err)
	}
	if _, err := store.TransitionOperation(
		t.Context(),
		op.ID,
		operation.StatusExecuting,
		nil,
		"",
		"",
	); err != nil {
		t.Fatalf("TransitionOperation(executing) error = %v", err)
	}
	exitCode := 0
	if _, err := store.TransitionOperation(
		t.Context(),
		op.ID,
		operation.StatusSucceeded,
		&operation.Result{OutputRef: "artifact:output", ExitCode: &exitCode},
		"tests passed",
		"",
	); err != nil {
		t.Fatalf("TransitionOperation(succeeded) error = %v", err)
	}
	records, err := store.ListAttemptOperations(t.Context(), createdTask.ID, createdTask.ActiveAttemptID)
	if err != nil {
		t.Fatalf("ListAttemptOperations() error = %v", err)
	}
	if len(records) != 1 || records[0].Operation.ID != op.ID || records[0].Result == nil ||
		records[0].Result.ExitCode == nil || *records[0].Result.ExitCode != 0 ||
		records[0].ApprovalReceiptID != receipt.ID {
		t.Fatalf("ListAttemptOperations() = %#v", records)
	}
}

func TestStoreExpiredApprovalCannotAuthorizeOperation(t *testing.T) {
	store := openTestStore(t)
	createdTask := createTestTask(t, store)
	transitionTaskToRunning(t, store, createdTask.ID)
	op, _, err := store.CreateOperation(
		t.Context(),
		createdTask.ID,
		"execute.command",
		"run-tests",
		operation.EffectProcess,
		map[string]any{"argv": []string{"go", "test", "./..."}},
	)
	if err != nil {
		t.Fatalf("CreateOperation() error = %v", err)
	}
	request, err := store.CreateApproval(
		t.Context(),
		op.ID,
		map[string]any{"argv": []string{"go", "test", "./..."}},
		approval.RiskLow,
		"run repository tests",
		time.Minute,
	)
	if err != nil {
		t.Fatalf("CreateApproval() error = %v", err)
	}
	if _, err := store.db.ExecContext(
		t.Context(),
		"UPDATE approval_requests SET expires_at = ? WHERE id = ?",
		formatTime(time.Now().UTC().Add(-time.Minute)),
		request.ID,
	); err != nil {
		t.Fatalf("expiring approval: %v", err)
	}
	if _, err := store.DecideApproval(
		t.Context(),
		request.ID,
		approval.DecisionApproved,
		"local-user",
	); !errors.Is(err, approval.ErrExpired) {
		t.Fatalf("DecideApproval() error = %v, want ErrExpired", err)
	}
	loaded, err := store.GetOperation(t.Context(), op.ID)
	if err != nil {
		t.Fatalf("GetOperation() error = %v", err)
	}
	if loaded.Status != operation.StatusCancelled {
		t.Fatalf("expired operation status = %q, want cancelled", loaded.Status)
	}
}

func TestStoreTaskCancellationRevokesPendingApprovals(t *testing.T) {
	store := openTestStore(t)
	createdTask := createTestTask(t, store)
	transitionTaskToRunning(t, store, createdTask.ID)
	op, _, err := store.CreateOperation(
		t.Context(),
		createdTask.ID,
		"change",
		"cancel-change",
		operation.EffectLocalWrite,
		map[string]string{"path": "README.md"},
	)
	if err != nil {
		t.Fatalf("CreateOperation() error = %v", err)
	}
	request, err := store.CreateApproval(
		t.Context(),
		op.ID,
		map[string]string{"path": "README.md"},
		approval.RiskMedium,
		"modify README.md",
		time.Minute,
	)
	if err != nil {
		t.Fatalf("CreateApproval() error = %v", err)
	}
	if _, err := store.CancelTask(t.Context(), createdTask.ID); err != nil {
		t.Fatalf("CancelTask() error = %v", err)
	}
	loaded, err := store.GetOperation(t.Context(), op.ID)
	if err != nil || loaded.Status != operation.StatusCancelled {
		t.Fatalf("operation after cancellation = %#v, %v", loaded, err)
	}
	pending, err := store.PendingApprovals(t.Context(), createdTask.ID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("PendingApprovals() = %#v, %v", pending, err)
	}
	if _, err := store.DecideApproval(
		t.Context(),
		request.ID,
		approval.DecisionApproved,
		"test-user",
	); !errors.Is(err, approval.ErrAlreadyDecided) {
		t.Fatalf("DecideApproval() error = %v", err)
	}
}

func transitionTaskToRunning(t *testing.T, store *Store, taskID string) {
	t.Helper()
	if err := store.Transition(t.Context(), taskID, task.StatusPlanning, "", ""); err != nil {
		t.Fatalf("Transition(planning) error = %v", err)
	}
	if err := store.Transition(t.Context(), taskID, task.StatusRunning, "", ""); err != nil {
		t.Fatalf("Transition(running) error = %v", err)
	}
}
