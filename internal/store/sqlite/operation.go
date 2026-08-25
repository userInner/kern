package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/userInner/kern/internal/id"
	"github.com/userInner/kern/internal/operation"
)

// CreateOperation records a proposed tool call before policy evaluation.
// An identical idempotency key and input returns the existing operation.
func (s *Store) CreateOperation(
	ctx context.Context,
	taskID string,
	tool string,
	idempotencyKey string,
	effect operation.Effect,
	input any,
) (operation.Operation, bool, error) {
	if tool == "" || idempotencyKey == "" || effect == operation.EffectUnknown {
		return operation.Operation{}, false, errors.New("sqlite: operation identity is incomplete")
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return operation.Operation{}, false, fmt.Errorf("encoding operation input: %w", err)
	}
	digest := sha256.Sum256(encoded)
	inputHash := hex.EncodeToString(digest[:])

	itemID, err := id.New()
	if err != nil {
		return operation.Operation{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return operation.Operation{}, false, fmt.Errorf("beginning operation creation: %w", err)
	}
	defer tx.Rollback()
	_, attemptID, err := readTaskState(ctx, tx, taskID)
	if err != nil {
		return operation.Operation{}, false, err
	}
	now := time.Now().UTC()
	item := operation.Operation{
		ID:             itemID,
		TaskID:         taskID,
		AttemptID:      attemptID,
		Tool:           tool,
		Input:          encoded,
		InputHash:      inputHash,
		IdempotencyKey: idempotencyKey,
		Effect:         effect,
		Status:         operation.StatusProposed,
		Recovery:       json.RawMessage(`{}`),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	inserted, err := tx.ExecContext(
		ctx,
		`INSERT INTO operations (
            id, task_id, attempt_id, tool, input_json, input_hash,
            idempotency_key, effect, status, created_at, updated_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(task_id, idempotency_key) DO NOTHING`,
		item.ID,
		item.TaskID,
		item.AttemptID,
		item.Tool,
		string(item.Input),
		item.InputHash,
		item.IdempotencyKey,
		item.Effect,
		item.Status,
		formatTime(item.CreatedAt),
		formatTime(item.UpdatedAt),
	)
	if err != nil {
		return operation.Operation{}, false, fmt.Errorf("inserting operation: %w", err)
	}
	rows, err := inserted.RowsAffected()
	if err != nil {
		return operation.Operation{}, false, fmt.Errorf("checking operation creation: %w", err)
	}
	if rows == 0 {
		existing, err := scanOperation(tx.QueryRowContext(
			ctx,
			`SELECT id, task_id, attempt_id, tool, input_json, input_hash,
				    idempotency_key, effect, status, output_summary, error_code, recovery_json,
				    created_at, updated_at
             FROM operations WHERE task_id = ? AND idempotency_key = ?`,
			taskID,
			idempotencyKey,
		))
		if err != nil {
			return operation.Operation{}, false, err
		}
		if existing.InputHash != inputHash || existing.Tool != tool || existing.Effect != effect {
			return operation.Operation{}, false, operation.ErrDuplicate
		}
		return existing, false, nil
	}
	payload := map[string]any{
		"operation_id": item.ID,
		"tool":         item.Tool,
		"effect":       item.Effect,
		"input_hash":   item.InputHash,
	}
	if err := insertEvent(ctx, tx, taskID, attemptID, "operation.proposed", payload, now); err != nil {
		return operation.Operation{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return operation.Operation{}, false, fmt.Errorf("committing operation creation: %w", err)
	}
	return item, true, nil
}

// GetOperation returns a durable operation envelope.
func (s *Store) GetOperation(ctx context.Context, operationID string) (operation.Operation, error) {
	return scanOperation(s.db.QueryRowContext(
		ctx,
		`SELECT id, task_id, attempt_id, tool, input_json, input_hash,
		        idempotency_key, effect, status, output_summary, error_code, recovery_json,
		        created_at, updated_at
         FROM operations WHERE id = ?`,
		operationID,
	))
}

// SetOperationRecovery persists tool-specific reconciliation metadata before
// an authorized operation is allowed to enter executing state.
func (s *Store) SetOperationRecovery(
	ctx context.Context,
	operationID string,
	metadata json.RawMessage,
) (operation.Operation, error) {
	if !json.Valid(metadata) || bytes.Equal(bytes.TrimSpace(metadata), []byte("null")) {
		return operation.Operation{}, errors.New("sqlite: invalid operation recovery metadata")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, metadata); err != nil {
		return operation.Operation{}, errors.New("sqlite: invalid operation recovery metadata")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return operation.Operation{}, fmt.Errorf("beginning operation recovery preparation: %w", err)
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
		return operation.Operation{}, err
	}
	if current.Status != operation.StatusPrepared {
		return operation.Operation{}, operation.ErrInvalidTransition
	}
	if string(current.Recovery) != "{}" {
		if bytes.Equal(current.Recovery, compact.Bytes()) {
			return current, nil
		}
		return operation.Operation{}, operation.ErrDuplicate
	}
	now := time.Now().UTC()
	changed, err := tx.ExecContext(
		ctx,
		`UPDATE operations SET recovery_json = ?, updated_at = ?
		 WHERE id = ? AND status = ? AND recovery_json = '{}'`,
		compact.String(),
		formatTime(now),
		operationID,
		operation.StatusPrepared,
	)
	if err != nil {
		return operation.Operation{}, fmt.Errorf("storing operation recovery metadata: %w", err)
	}
	if err := requireOneRow(changed, operation.ErrInvalidTransition); err != nil {
		return operation.Operation{}, err
	}
	if err := insertEvent(ctx, tx, current.TaskID, current.AttemptID, "operation.recovery_prepared", map[string]any{
		"operation_id": operationID,
		"tool":         current.Tool,
	}, now); err != nil {
		return operation.Operation{}, err
	}
	if err := tx.Commit(); err != nil {
		return operation.Operation{}, fmt.Errorf("committing operation recovery metadata: %w", err)
	}
	return s.GetOperation(ctx, operationID)
}

// ListUncertainOperations returns unresolved outcomes that must not be replayed
// until a human has confirmed what happened outside Kern.
func (s *Store) ListUncertainOperations(
	ctx context.Context,
	taskID string,
) ([]operation.Operation, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, task_id, attempt_id, tool, input_json, input_hash,
		        idempotency_key, effect, status, output_summary, error_code, recovery_json,
		        created_at, updated_at
         FROM operations
         WHERE task_id = ? AND status = ?
         ORDER BY created_at ASC, id ASC`,
		taskID,
		operation.StatusUncertain,
	)
	if err != nil {
		return nil, fmt.Errorf("querying uncertain operations: %w", err)
	}
	defer rows.Close()

	items := make([]operation.Operation, 0)
	for rows.Next() {
		item, err := scanOperation(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating uncertain operations: %w", err)
	}
	return items, nil
}

// ListAttemptOperations returns the complete operation ledger for one Attempt,
// including durable results and exact approval receipt references.
func (s *Store) ListAttemptOperations(
	ctx context.Context,
	taskID string,
	attemptID string,
) ([]operation.Record, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT o.id, o.task_id, o.attempt_id, o.tool, o.input_json, o.input_hash,
		        o.idempotency_key, o.effect, o.status, o.output_summary, o.error_code, o.recovery_json,
		        o.created_at, o.updated_at,
                r.operation_id, r.output_ref, r.exit_code, r.error_code,
                r.timing_json, r.created_at,
                COALESCE((
                    SELECT receipt.id
                    FROM approval_requests request
                    JOIN approval_receipts receipt ON receipt.request_id = request.id
                    WHERE request.operation_id = o.id AND receipt.decision = 'approved'
                    ORDER BY receipt.decided_at DESC LIMIT 1
                ), '')
         FROM operations o
         LEFT JOIN operation_results r ON r.operation_id = o.id
         WHERE o.task_id = ? AND o.attempt_id = ?
         ORDER BY o.created_at ASC, o.id ASC`,
		taskID,
		attemptID,
	)
	if err != nil {
		return nil, fmt.Errorf("querying attempt operations: %w", err)
	}
	defer rows.Close()
	records := make([]operation.Record, 0)
	for rows.Next() {
		record, err := scanOperationRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating attempt operations: %w", err)
	}
	return records, nil
}

// ResolveUncertainOperation atomically records an immutable human receipt and
// closes one unknown operation. A repeated identical request is idempotent.
func (s *Store) ResolveUncertainOperation(
	ctx context.Context,
	operationID string,
	resolution operation.Resolution,
	actor string,
) (operation.ResolutionReceipt, error) {
	if !resolution.Valid() || actor == "" {
		return operation.ResolutionReceipt{}, operation.ErrInvalidResolution
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return operation.ResolutionReceipt{}, fmt.Errorf("beginning operation resolution: %w", err)
	}
	defer tx.Rollback()

	if existing, found, err := findResolution(ctx, tx, operationID); err != nil {
		return operation.ResolutionReceipt{}, err
	} else if found {
		if existing.Resolution == resolution {
			return existing, nil
		}
		return operation.ResolutionReceipt{}, operation.ErrAlreadyResolved
	}
	current, err := scanOperation(tx.QueryRowContext(
		ctx,
		`SELECT id, task_id, attempt_id, tool, input_json, input_hash,
		        idempotency_key, effect, status, output_summary, error_code, recovery_json,
		        created_at, updated_at
         FROM operations WHERE id = ?`,
		operationID,
	))
	if err != nil {
		return operation.ResolutionReceipt{}, err
	}
	if current.Status != operation.StatusUncertain {
		return operation.ResolutionReceipt{}, operation.ErrAlreadyResolved
	}

	automated := strings.HasPrefix(actor, "kern-recovery:")
	target := operation.StatusSucceeded
	outputSummary := "User confirmed the operation completed before runtime interruption."
	if automated {
		outputSummary = "Kern reconciled the interrupted operation as completed from current tool state."
	}
	errorCode := ""
	if resolution == operation.ResolutionNotExecuted {
		target = operation.StatusFailed
		outputSummary = "User confirmed the operation did not execute before runtime interruption."
		errorCode = "user_confirmed_not_executed"
		if automated {
			outputSummary = "Kern reconciled the interrupted operation as not executed from current tool state."
			errorCode = "reconciled_not_executed"
		}
	}
	if err := operation.ValidateTransition(current.Status, target); err != nil {
		return operation.ResolutionReceipt{}, err
	}
	receiptID, err := id.New()
	if err != nil {
		return operation.ResolutionReceipt{}, err
	}
	now := time.Now().UTC()
	receipt := operation.ResolutionReceipt{
		ID:          receiptID,
		OperationID: current.ID,
		TaskID:      current.TaskID,
		AttemptID:   current.AttemptID,
		Resolution:  resolution,
		Actor:       actor,
		DecidedAt:   now,
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO operation_resolutions (
            id, operation_id, task_id, attempt_id, resolution, actor, decided_at
         ) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		receipt.ID,
		receipt.OperationID,
		receipt.TaskID,
		receipt.AttemptID,
		receipt.Resolution,
		receipt.Actor,
		formatTime(receipt.DecidedAt),
	); err != nil {
		return operation.ResolutionReceipt{}, fmt.Errorf("inserting operation resolution: %w", err)
	}
	changed, err := tx.ExecContext(
		ctx,
		`UPDATE operations
         SET status = ?, output_summary = ?, error_code = ?, updated_at = ?
         WHERE id = ? AND status = ?`,
		target,
		outputSummary,
		errorCode,
		formatTime(now),
		current.ID,
		operation.StatusUncertain,
	)
	if err != nil {
		return operation.ResolutionReceipt{}, fmt.Errorf("closing uncertain operation: %w", err)
	}
	if err := requireOneRow(changed, operation.ErrAlreadyResolved); err != nil {
		return operation.ResolutionReceipt{}, err
	}
	payload := map[string]any{
		"operation_id": current.ID,
		"resolution":   resolution,
		"actor":        actor,
		"to":           target,
		"error_code":   errorCode,
	}
	if err := insertEvent(ctx, tx, current.TaskID, current.AttemptID, "operation.resolved", payload, now); err != nil {
		return operation.ResolutionReceipt{}, err
	}
	if err := insertEvent(ctx, tx, current.TaskID, current.AttemptID, "operation.completed", payload, now); err != nil {
		return operation.ResolutionReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return operation.ResolutionReceipt{}, fmt.Errorf("committing operation resolution: %w", err)
	}
	return receipt, nil
}

// TransitionOperation changes operation state and optionally records its bounded result.
func (s *Store) TransitionOperation(
	ctx context.Context,
	operationID string,
	to operation.Status,
	result *operation.Result,
	outputSummary string,
	errorCode string,
) (operation.Operation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return operation.Operation{}, fmt.Errorf("beginning operation transition: %w", err)
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
		return operation.Operation{}, err
	}
	if err := operation.ValidateTransition(current.Status, to); err != nil {
		return operation.Operation{}, err
	}
	now := time.Now().UTC()
	changed, err := tx.ExecContext(
		ctx,
		`UPDATE operations
         SET status = ?, output_summary = ?, error_code = ?, updated_at = ?
         WHERE id = ? AND status = ?`,
		to,
		outputSummary,
		errorCode,
		formatTime(now),
		operationID,
		current.Status,
	)
	if err != nil {
		return operation.Operation{}, fmt.Errorf("updating operation: %w", err)
	}
	if err := requireOneRow(changed, operation.ErrInvalidTransition); err != nil {
		return operation.Operation{}, err
	}
	if result != nil {
		if result.OperationID != "" && result.OperationID != operationID {
			return operation.Operation{}, errors.New("sqlite: operation result id mismatch")
		}
		result.OperationID = operationID
		result.CreatedAt = now
		if len(result.Timing) == 0 {
			result.Timing = json.RawMessage(`{}`)
		}
		if !json.Valid(result.Timing) {
			return operation.Operation{}, errors.New("sqlite: invalid operation timing")
		}
		_, err = tx.ExecContext(
			ctx,
			`INSERT INTO operation_results (
                operation_id, output_ref, exit_code, error_code, timing_json, created_at
             ) VALUES (?, ?, ?, ?, ?, ?)
             ON CONFLICT(operation_id) DO UPDATE SET
                output_ref = excluded.output_ref,
                exit_code = excluded.exit_code,
                error_code = excluded.error_code,
                timing_json = excluded.timing_json`,
			result.OperationID,
			result.OutputRef,
			result.ExitCode,
			result.ErrorCode,
			string(result.Timing),
			formatTime(result.CreatedAt),
		)
		if err != nil {
			return operation.Operation{}, fmt.Errorf("upserting operation result: %w", err)
		}
	}
	payload := map[string]any{
		"operation_id": operationID,
		"from":         current.Status,
		"to":           to,
		"error_code":   errorCode,
	}
	eventType := "operation.status_changed"
	if to == operation.StatusExecuting {
		eventType = "operation.started"
	} else if to.IsTerminal() {
		eventType = "operation.completed"
	}
	if err := insertEvent(
		ctx,
		tx,
		current.TaskID,
		current.AttemptID,
		eventType,
		payload,
		now,
	); err != nil {
		return operation.Operation{}, err
	}
	if err := tx.Commit(); err != nil {
		return operation.Operation{}, fmt.Errorf("committing operation transition: %w", err)
	}
	return s.GetOperation(ctx, operationID)
}

// MarkInterruptedOperationsUnknown prevents silent replay after a crash.
func (s *Store) MarkInterruptedOperationsUnknown(ctx context.Context, taskID string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("beginning interrupted operation scan: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(
		ctx,
		"SELECT id, attempt_id FROM operations WHERE task_id = ? AND status = ?",
		taskID,
		operation.StatusExecuting,
	)
	if err != nil {
		return 0, fmt.Errorf("querying interrupted operations: %w", err)
	}
	type interrupted struct {
		id        string
		attemptID string
	}
	items := make([]interrupted, 0)
	for rows.Next() {
		var item interrupted
		if err := rows.Scan(&item.id, &item.attemptID); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scanning interrupted operation: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterating interrupted operations: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("closing interrupted operations: %w", err)
	}
	now := time.Now().UTC()
	for _, item := range items {
		if _, err := tx.ExecContext(
			ctx,
			"UPDATE operations SET status = ?, updated_at = ? WHERE id = ? AND status = ?",
			operation.StatusUncertain,
			formatTime(now),
			item.id,
			operation.StatusExecuting,
		); err != nil {
			return 0, fmt.Errorf("marking operation unknown: %w", err)
		}
		payload := map[string]string{"operation_id": item.id, "status": string(operation.StatusUncertain)}
		if err := insertEvent(
			ctx,
			tx,
			taskID,
			item.attemptID,
			"operation.unknown",
			payload,
			now,
		); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("committing interrupted operation scan: %w", err)
	}
	return len(items), nil
}

func scanOperation(row rowScanner) (operation.Operation, error) {
	var item operation.Operation
	var input string
	var recovery string
	var createdAt string
	var updatedAt string
	err := row.Scan(
		&item.ID,
		&item.TaskID,
		&item.AttemptID,
		&item.Tool,
		&input,
		&item.InputHash,
		&item.IdempotencyKey,
		&item.Effect,
		&item.Status,
		&item.OutputSummary,
		&item.ErrorCode,
		&recovery,
		&createdAt,
		&updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return operation.Operation{}, operation.ErrNotFound
	}
	if err != nil {
		return operation.Operation{}, fmt.Errorf("scanning operation: %w", err)
	}
	item.Input = json.RawMessage(input)
	if !json.Valid([]byte(recovery)) {
		return operation.Operation{}, errors.New("sqlite: stored operation recovery metadata is invalid")
	}
	item.Recovery = json.RawMessage(recovery)
	item.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return operation.Operation{}, err
	}
	item.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return operation.Operation{}, err
	}
	return item, nil
}

func scanOperationRecord(row rowScanner) (operation.Record, error) {
	var record operation.Record
	var input string
	var recovery string
	var operationCreatedAt string
	var operationUpdatedAt string
	var resultOperationID sql.NullString
	var outputRef sql.NullString
	var exitCode sql.NullInt64
	var resultErrorCode sql.NullString
	var timing sql.NullString
	var resultCreatedAt sql.NullString
	err := row.Scan(
		&record.Operation.ID,
		&record.Operation.TaskID,
		&record.Operation.AttemptID,
		&record.Operation.Tool,
		&input,
		&record.Operation.InputHash,
		&record.Operation.IdempotencyKey,
		&record.Operation.Effect,
		&record.Operation.Status,
		&record.Operation.OutputSummary,
		&record.Operation.ErrorCode,
		&recovery,
		&operationCreatedAt,
		&operationUpdatedAt,
		&resultOperationID,
		&outputRef,
		&exitCode,
		&resultErrorCode,
		&timing,
		&resultCreatedAt,
		&record.ApprovalReceiptID,
	)
	if err != nil {
		return operation.Record{}, fmt.Errorf("scanning operation record: %w", err)
	}
	record.Operation.Input = json.RawMessage(input)
	if !json.Valid([]byte(recovery)) {
		return operation.Record{}, errors.New("sqlite: stored operation recovery metadata is invalid")
	}
	record.Operation.Recovery = json.RawMessage(recovery)
	record.Operation.CreatedAt, err = parseTime(operationCreatedAt)
	if err != nil {
		return operation.Record{}, err
	}
	record.Operation.UpdatedAt, err = parseTime(operationUpdatedAt)
	if err != nil {
		return operation.Record{}, err
	}
	if resultOperationID.Valid {
		result := &operation.Result{
			OperationID: resultOperationID.String,
			OutputRef:   outputRef.String,
			ErrorCode:   resultErrorCode.String,
			Timing:      json.RawMessage(timing.String),
		}
		if exitCode.Valid {
			value := int(exitCode.Int64)
			result.ExitCode = &value
		}
		result.CreatedAt, err = parseTime(resultCreatedAt.String)
		if err != nil {
			return operation.Record{}, err
		}
		record.Result = result
	}
	return record, nil
}

func findResolution(
	ctx context.Context,
	queryer interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	},
	operationID string,
) (operation.ResolutionReceipt, bool, error) {
	var receipt operation.ResolutionReceipt
	var decidedAt string
	err := queryer.QueryRowContext(
		ctx,
		`SELECT id, operation_id, task_id, attempt_id, resolution, actor, decided_at
         FROM operation_resolutions WHERE operation_id = ?`,
		operationID,
	).Scan(
		&receipt.ID,
		&receipt.OperationID,
		&receipt.TaskID,
		&receipt.AttemptID,
		&receipt.Resolution,
		&receipt.Actor,
		&decidedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return operation.ResolutionReceipt{}, false, nil
	}
	if err != nil {
		return operation.ResolutionReceipt{}, false, fmt.Errorf("reading operation resolution: %w", err)
	}
	receipt.DecidedAt, err = parseTime(decidedAt)
	if err != nil {
		return operation.ResolutionReceipt{}, false, err
	}
	return receipt, true, nil
}
