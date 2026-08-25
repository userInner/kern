package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/userInner/kern/internal/approval"
	"github.com/userInner/kern/internal/id"
	"github.com/userInner/kern/internal/operation"
	"github.com/userInner/kern/internal/task"
)

// CreateApproval requests explicit authorization for a proposed operation.
func (s *Store) CreateApproval(
	ctx context.Context,
	operationID string,
	scope any,
	risk approval.Risk,
	explanation string,
	ttl time.Duration,
) (approval.Request, error) {
	if risk == approval.RiskUnknown || explanation == "" || ttl <= 0 {
		return approval.Request{}, errors.New("sqlite: approval risk, explanation, and positive ttl are required")
	}
	encodedScope, err := json.Marshal(scope)
	if err != nil {
		return approval.Request{}, fmt.Errorf("encoding approval scope: %w", err)
	}
	requestID, err := id.New()
	if err != nil {
		return approval.Request{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return approval.Request{}, fmt.Errorf("beginning approval request: %w", err)
	}
	defer tx.Rollback()
	current, err := scanOperation(tx.QueryRowContext(
		ctx,
		`SELECT id, task_id, attempt_id, tool, input_json, input_hash,
		        idempotency_key, effect, status, output_summary, error_code, recovery_json,
		        created_at, updated_at
         FROM operations WHERE id = ?`,
		operationID,
	))
	if err != nil {
		return approval.Request{}, err
	}
	if err := operation.ValidateTransition(current.Status, operation.StatusAwaitingApproval); err != nil {
		return approval.Request{}, err
	}
	now := time.Now().UTC()
	request := approval.Request{
		ID:          requestID,
		TaskID:      current.TaskID,
		AttemptID:   current.AttemptID,
		OperationID: operationID,
		Scope:       encodedScope,
		Risk:        risk,
		Explanation: explanation,
		Status:      approval.StatusPending,
		ExpiresAt:   now.Add(ttl),
		CreatedAt:   now,
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO approval_requests (
            id, task_id, attempt_id, operation_id, scope_json, risk,
            explanation, status, expires_at, created_at
         ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		request.ID,
		request.TaskID,
		request.AttemptID,
		request.OperationID,
		string(request.Scope),
		request.Risk,
		request.Explanation,
		request.Status,
		formatTime(request.ExpiresAt),
		formatTime(request.CreatedAt),
	); err != nil {
		return approval.Request{}, fmt.Errorf("inserting approval request: %w", err)
	}
	changed, err := tx.ExecContext(
		ctx,
		"UPDATE operations SET status = ?, updated_at = ? WHERE id = ? AND status = ?",
		operation.StatusAwaitingApproval,
		formatTime(now),
		operationID,
		current.Status,
	)
	if err != nil {
		return approval.Request{}, fmt.Errorf("marking operation awaiting approval: %w", err)
	}
	if err := requireOneRow(changed, operation.ErrInvalidTransition); err != nil {
		return approval.Request{}, err
	}
	changed, err = tx.ExecContext(
		ctx,
		`UPDATE tasks SET status = ?, updated_at = ?
		 WHERE id = ? AND active_attempt_id = ? AND status = ?`,
		task.StatusWaitingApproval,
		formatTime(now),
		request.TaskID,
		request.AttemptID,
		task.StatusRunning,
	)
	if err != nil {
		return approval.Request{}, fmt.Errorf("marking task awaiting approval: %w", err)
	}
	if err := requireOneRow(changed, task.ErrInvalidTransition); err != nil {
		return approval.Request{}, err
	}
	if _, err := tx.ExecContext(
		ctx,
		"UPDATE attempts SET status = ? WHERE id = ? AND finished_at IS NULL",
		task.StatusWaitingApproval,
		request.AttemptID,
	); err != nil {
		return approval.Request{}, fmt.Errorf("marking attempt awaiting approval: %w", err)
	}
	payload := map[string]any{
		"request_id":   request.ID,
		"operation_id": operationID,
		"risk":         risk,
		"explanation":  explanation,
		"scope":        json.RawMessage(request.Scope),
		"expires_at":   request.ExpiresAt,
	}
	if err := insertEvent(
		ctx,
		tx,
		request.TaskID,
		request.AttemptID,
		"approval.requested",
		payload,
		now,
	); err != nil {
		return approval.Request{}, err
	}
	if err := insertEvent(
		ctx,
		tx,
		request.TaskID,
		request.AttemptID,
		"task.status_changed",
		map[string]string{
			"from": string(task.StatusRunning),
			"to":   string(task.StatusWaitingApproval),
		},
		now,
	); err != nil {
		return approval.Request{}, err
	}
	if err := tx.Commit(); err != nil {
		return approval.Request{}, fmt.Errorf("committing approval request: %w", err)
	}
	return request, nil
}

// DecideApproval persists a receipt and moves the operation to prepared or cancelled.
func (s *Store) DecideApproval(
	ctx context.Context,
	requestID string,
	decision approval.Decision,
	actor string,
) (approval.Receipt, error) {
	if decision != approval.DecisionApproved && decision != approval.DecisionDenied {
		return approval.Receipt{}, errors.New("sqlite: approval decision is invalid")
	}
	if actor == "" {
		return approval.Receipt{}, errors.New("sqlite: approval actor is required")
	}
	receiptID, err := id.New()
	if err != nil {
		return approval.Receipt{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return approval.Receipt{}, fmt.Errorf("beginning approval decision: %w", err)
	}
	defer tx.Rollback()
	request, err := scanApprovalRequest(tx.QueryRowContext(
		ctx,
		`SELECT id, task_id, attempt_id, operation_id, scope_json, risk,
                explanation, status, expires_at, created_at
         FROM approval_requests WHERE id = ?`,
		requestID,
	))
	if err != nil {
		return approval.Receipt{}, err
	}
	if request.Status != approval.StatusPending {
		return approval.Receipt{}, approval.ErrAlreadyDecided
	}
	now := time.Now().UTC()
	if now.After(request.ExpiresAt) {
		changed, err := tx.ExecContext(
			ctx,
			"UPDATE approval_requests SET status = ? WHERE id = ? AND status = ?",
			approval.StatusExpired,
			requestID,
			approval.StatusPending,
		)
		if err != nil {
			return approval.Receipt{}, fmt.Errorf("expiring approval request: %w", err)
		}
		if err := requireOneRow(changed, approval.ErrAlreadyDecided); err != nil {
			return approval.Receipt{}, err
		}
		changed, err = tx.ExecContext(
			ctx,
			"UPDATE operations SET status = ?, updated_at = ? WHERE id = ? AND status = ?",
			operation.StatusCancelled,
			formatTime(now),
			request.OperationID,
			operation.StatusAwaitingApproval,
		)
		if err != nil {
			return approval.Receipt{}, fmt.Errorf("cancelling expired operation: %w", err)
		}
		if err := requireOneRow(changed, operation.ErrInvalidTransition); err != nil {
			return approval.Receipt{}, err
		}
		if err := insertEvent(
			ctx,
			tx,
			request.TaskID,
			request.AttemptID,
			"approval.expired",
			map[string]string{
				"request_id":   request.ID,
				"operation_id": request.OperationID,
			},
			now,
		); err != nil {
			return approval.Receipt{}, err
		}
		if err := tx.Commit(); err != nil {
			return approval.Receipt{}, fmt.Errorf("committing expired approval: %w", err)
		}
		return approval.Receipt{}, approval.ErrExpired
	}
	status := approval.StatusDenied
	operationStatus := operation.StatusCancelled
	if decision == approval.DecisionApproved {
		status = approval.StatusApproved
		operationStatus = operation.StatusPrepared
	}
	changed, err := tx.ExecContext(
		ctx,
		"UPDATE approval_requests SET status = ? WHERE id = ? AND status = ?",
		status,
		requestID,
		approval.StatusPending,
	)
	if err != nil {
		return approval.Receipt{}, fmt.Errorf("updating approval request: %w", err)
	}
	if err := requireOneRow(changed, approval.ErrAlreadyDecided); err != nil {
		return approval.Receipt{}, err
	}
	changed, err = tx.ExecContext(
		ctx,
		"UPDATE operations SET status = ?, updated_at = ? WHERE id = ? AND status = ?",
		operationStatus,
		formatTime(now),
		request.OperationID,
		operation.StatusAwaitingApproval,
	)
	if err != nil {
		return approval.Receipt{}, fmt.Errorf("updating approved operation: %w", err)
	}
	if err := requireOneRow(changed, operation.ErrInvalidTransition); err != nil {
		return approval.Receipt{}, err
	}
	receipt := approval.Receipt{
		ID:        receiptID,
		RequestID: requestID,
		Decision:  decision,
		Actor:     actor,
		Scope:     request.Scope,
		DecidedAt: now,
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO approval_receipts (
            id, request_id, decision, actor, scope_json, decided_at
         ) VALUES (?, ?, ?, ?, ?, ?)`,
		receipt.ID,
		receipt.RequestID,
		receipt.Decision,
		receipt.Actor,
		string(receipt.Scope),
		formatTime(receipt.DecidedAt),
	); err != nil {
		return approval.Receipt{}, fmt.Errorf("inserting approval receipt: %w", err)
	}
	payload := map[string]any{
		"request_id":   requestID,
		"operation_id": request.OperationID,
		"decision":     decision,
		"actor":        actor,
	}
	if err := insertEvent(
		ctx,
		tx,
		request.TaskID,
		request.AttemptID,
		"approval.decided",
		payload,
		now,
	); err != nil {
		return approval.Receipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return approval.Receipt{}, fmt.Errorf("committing approval decision: %w", err)
	}
	return receipt, nil
}

// PendingApprovals returns unexpired pending requests for a task.
func (s *Store) PendingApprovals(ctx context.Context, taskID string) ([]approval.Request, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, task_id, attempt_id, operation_id, scope_json, risk,
                explanation, status, expires_at, created_at
         FROM approval_requests
		 WHERE task_id = ? AND status = ? AND expires_at > ?
         ORDER BY created_at ASC`,
		taskID,
		approval.StatusPending,
		formatTime(time.Now().UTC()),
	)
	if err != nil {
		return nil, fmt.Errorf("querying pending approvals: %w", err)
	}
	defer rows.Close()
	requests := make([]approval.Request, 0)
	for rows.Next() {
		request, err := scanApprovalRequest(rows)
		if err != nil {
			return nil, err
		}
		requests = append(requests, request)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating pending approvals: %w", err)
	}
	return requests, nil
}

// CancelApproval revokes a pending request and prevents its operation from executing.
func (s *Store) CancelApproval(ctx context.Context, requestID, actor string) error {
	if actor == "" {
		return errors.New("sqlite: approval cancellation actor is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning approval cancellation: %w", err)
	}
	defer tx.Rollback()
	request, err := scanApprovalRequest(tx.QueryRowContext(
		ctx,
		`SELECT id, task_id, attempt_id, operation_id, scope_json, risk,
		        explanation, status, expires_at, created_at
		 FROM approval_requests WHERE id = ?`,
		requestID,
	))
	if err != nil {
		return err
	}
	if request.Status != approval.StatusPending {
		return approval.ErrAlreadyDecided
	}
	now := time.Now().UTC()
	changed, err := tx.ExecContext(
		ctx,
		"UPDATE approval_requests SET status = ? WHERE id = ? AND status = ?",
		approval.StatusCancelled,
		requestID,
		approval.StatusPending,
	)
	if err != nil {
		return fmt.Errorf("cancelling approval request: %w", err)
	}
	if err := requireOneRow(changed, approval.ErrAlreadyDecided); err != nil {
		return err
	}
	changed, err = tx.ExecContext(
		ctx,
		"UPDATE operations SET status = ?, updated_at = ? WHERE id = ? AND status = ?",
		operation.StatusCancelled,
		formatTime(now),
		request.OperationID,
		operation.StatusAwaitingApproval,
	)
	if err != nil {
		return fmt.Errorf("cancelling approval operation: %w", err)
	}
	if err := requireOneRow(changed, operation.ErrInvalidTransition); err != nil {
		return err
	}
	if err := insertEvent(ctx, tx, request.TaskID, request.AttemptID, "approval.cancelled", map[string]string{
		"request_id":   request.ID,
		"operation_id": request.OperationID,
		"actor":        actor,
	}, now); err != nil {
		return err
	}
	if err := insertEvent(ctx, tx, request.TaskID, request.AttemptID, "operation.completed", map[string]string{
		"operation_id": request.OperationID,
		"to":           string(operation.StatusCancelled),
		"error_code":   "approval_cancelled",
	}, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing approval cancellation: %w", err)
	}
	return nil
}

func scanApprovalRequest(row rowScanner) (approval.Request, error) {
	var request approval.Request
	var scope string
	var expiresAt string
	var createdAt string
	err := row.Scan(
		&request.ID,
		&request.TaskID,
		&request.AttemptID,
		&request.OperationID,
		&scope,
		&request.Risk,
		&request.Explanation,
		&request.Status,
		&expiresAt,
		&createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return approval.Request{}, approval.ErrNotFound
	}
	if err != nil {
		return approval.Request{}, fmt.Errorf("scanning approval request: %w", err)
	}
	request.Scope = json.RawMessage(scope)
	request.ExpiresAt, err = parseTime(expiresAt)
	if err != nil {
		return approval.Request{}, err
	}
	request.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return approval.Request{}, err
	}
	return request, nil
}
