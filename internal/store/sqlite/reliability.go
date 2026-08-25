package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/userInner/kern/internal/approval"
	"github.com/userInner/kern/internal/contextbuilder"
	"github.com/userInner/kern/internal/id"
	"github.com/userInner/kern/internal/operation"
	"github.com/userInner/kern/internal/task"
)

// AcquireLease claims exclusive execution ownership for a non-terminal task.
func (s *Store) AcquireLease(
	ctx context.Context,
	taskID string,
	owner string,
	ttl time.Duration,
) error {
	if owner == "" || ttl <= 0 {
		return errors.New("sqlite: lease owner and positive ttl are required")
	}
	now := time.Now().UTC()
	expiresAt := now.Add(ttl)
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE tasks
         SET lease_owner = ?, lease_expires_at = ?, heartbeat_at = ?, updated_at = ?
         WHERE id = ?
           AND status NOT IN ('completed', 'partially_completed', 'failed', 'cancelled')
           AND (
               lease_owner IS NULL OR lease_owner = '' OR lease_owner = ? OR lease_expires_at < ?
           )`,
		owner,
		formatTime(expiresAt),
		formatTime(now),
		formatTime(now),
		taskID,
		owner,
		formatTime(now),
	)
	if err != nil {
		return fmt.Errorf("acquiring task lease: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking acquired lease: %w", err)
	}
	if rows == 1 {
		return nil
	}
	if _, err := s.GetTask(ctx, taskID); errors.Is(err, task.ErrNotFound) {
		return task.ErrNotFound
	}
	return task.ErrLeaseHeld
}

// RenewLease extends a lease only when the caller still owns it.
func (s *Store) RenewLease(
	ctx context.Context,
	taskID string,
	owner string,
	ttl time.Duration,
) error {
	if owner == "" || ttl <= 0 {
		return errors.New("sqlite: lease owner and positive ttl are required")
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE tasks
         SET lease_expires_at = ?, heartbeat_at = ?, updated_at = ?
         WHERE id = ? AND lease_owner = ? AND lease_expires_at >= ?`,
		formatTime(now.Add(ttl)),
		formatTime(now),
		formatTime(now),
		taskID,
		owner,
		formatTime(now),
	)
	if err != nil {
		return fmt.Errorf("renewing task lease: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking renewed lease: %w", err)
	}
	if rows != 1 {
		return task.ErrLeaseLost
	}
	return nil
}

// ReleaseLease clears execution ownership if owner still holds it.
func (s *Store) ReleaseLease(ctx context.Context, taskID, owner string) error {
	if owner == "" {
		return errors.New("sqlite: lease owner is required")
	}
	_, err := s.db.ExecContext(
		ctx,
		`UPDATE tasks
         SET lease_owner = NULL, lease_expires_at = NULL, heartbeat_at = NULL
         WHERE id = ? AND lease_owner = ?`,
		taskID,
		owner,
	)
	if err != nil {
		return fmt.Errorf("releasing task lease: %w", err)
	}
	return nil
}

// RecoverableTasks lists queued or interrupted active tasks without a live lease.
func (s *Store) RecoverableTasks(
	ctx context.Context,
	now time.Time,
	limit int,
) ([]task.Task, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT schema_version, id, title, goal, status, result, error_message,
				active_attempt_id, model_config_id, created_at, updated_at, lease_owner,
                lease_expires_at, heartbeat_at, paused_from_status
         FROM tasks
         WHERE status IN ('created', 'planning', 'running', 'verifying')
           AND (lease_expires_at IS NULL OR lease_expires_at < ?)
         ORDER BY COALESCE(lease_expires_at, created_at) ASC LIMIT ?`,
		formatTime(now.UTC()),
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("querying recoverable tasks: %w", err)
	}
	defer rows.Close()

	items := make([]task.Task, 0, limit)
	for rows.Next() {
		item, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating recoverable tasks: %w", err)
	}
	return items, nil
}

// PauseTask moves an active task into waiting_input and closes its attempt.
func (s *Store) PauseTask(ctx context.Context, taskID string) (task.Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return task.Task{}, fmt.Errorf("beginning task pause: %w", err)
	}
	defer tx.Rollback()

	from, attemptID, err := readTaskState(ctx, tx, taskID)
	if err != nil {
		return task.Task{}, err
	}
	canPause := from == task.StatusPlanning || from == task.StatusRunning ||
		from == task.StatusVerifying
	if !canPause {
		return task.Task{}, fmt.Errorf("%w: cannot pause from %s", task.ErrInvalidTransition, from)
	}
	now := time.Now().UTC()
	result, err := tx.ExecContext(
		ctx,
		`UPDATE tasks
         SET status = ?, paused_from_status = ?, lease_owner = NULL,
             lease_expires_at = NULL, heartbeat_at = NULL, updated_at = ?
         WHERE id = ? AND status = ?`,
		task.StatusWaitingInput,
		from,
		formatTime(now),
		taskID,
		from,
	)
	if err != nil {
		return task.Task{}, fmt.Errorf("updating paused task: %w", err)
	}
	if err := requireOneRow(result, task.ErrInvalidTransition); err != nil {
		return task.Task{}, err
	}
	if _, err := tx.ExecContext(
		ctx,
		"UPDATE attempts SET status = ?, finished_at = ? WHERE id = ? AND finished_at IS NULL",
		task.StatusWaitingInput,
		formatTime(now),
		attemptID,
	); err != nil {
		return task.Task{}, fmt.Errorf("closing paused attempt: %w", err)
	}
	payload := map[string]string{"from": string(from), "to": string(task.StatusWaitingInput)}
	if err := insertEvent(ctx, tx, taskID, attemptID, "task.status_changed", payload, now); err != nil {
		return task.Task{}, err
	}
	if err := insertEvent(ctx, tx, taskID, attemptID, "task.paused", payload, now); err != nil {
		return task.Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return task.Task{}, fmt.Errorf("committing task pause: %w", err)
	}
	return s.GetTask(ctx, taskID)
}

// CancelTask moves any non-terminal task to cancelled and closes its attempt.
func (s *Store) CancelTask(ctx context.Context, taskID string) (task.Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return task.Task{}, fmt.Errorf("beginning task cancellation: %w", err)
	}
	defer tx.Rollback()
	from, attemptID, err := readTaskState(ctx, tx, taskID)
	if err != nil {
		return task.Task{}, err
	}
	if from.IsTerminal() {
		return task.Task{}, fmt.Errorf("%w: cannot cancel from %s", task.ErrInvalidTransition, from)
	}
	now := time.Now().UTC()
	type pendingApproval struct {
		requestID   string
		operationID string
	}
	pending := make([]pendingApproval, 0)
	rows, err := tx.QueryContext(
		ctx,
		`SELECT id, operation_id FROM approval_requests
		 WHERE task_id = ? AND status = ?`,
		taskID,
		approval.StatusPending,
	)
	if err != nil {
		return task.Task{}, fmt.Errorf("querying approvals during cancellation: %w", err)
	}
	for rows.Next() {
		var item pendingApproval
		if err := rows.Scan(&item.requestID, &item.operationID); err != nil {
			rows.Close()
			return task.Task{}, fmt.Errorf("scanning approval during cancellation: %w", err)
		}
		pending = append(pending, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return task.Task{}, fmt.Errorf("iterating approvals during cancellation: %w", err)
	}
	if err := rows.Close(); err != nil {
		return task.Task{}, fmt.Errorf("closing approvals during cancellation: %w", err)
	}
	if _, err := tx.ExecContext(
		ctx,
		"UPDATE approval_requests SET status = ? WHERE task_id = ? AND status = ?",
		approval.StatusCancelled,
		taskID,
		approval.StatusPending,
	); err != nil {
		return task.Task{}, fmt.Errorf("cancelling pending approvals: %w", err)
	}
	if _, err := tx.ExecContext(
		ctx,
		"UPDATE operations SET status = ?, updated_at = ? WHERE task_id = ? AND status = ?",
		operation.StatusCancelled,
		formatTime(now),
		taskID,
		operation.StatusAwaitingApproval,
	); err != nil {
		return task.Task{}, fmt.Errorf("cancelling awaiting operations: %w", err)
	}
	result, err := tx.ExecContext(
		ctx,
		`UPDATE tasks
         SET status = ?, lease_owner = NULL, lease_expires_at = NULL,
             heartbeat_at = NULL, updated_at = ?
         WHERE id = ? AND status = ?`,
		task.StatusCancelled,
		formatTime(now),
		taskID,
		from,
	)
	if err != nil {
		return task.Task{}, fmt.Errorf("updating cancelled task: %w", err)
	}
	if err := requireOneRow(result, task.ErrInvalidTransition); err != nil {
		return task.Task{}, err
	}
	if _, err := tx.ExecContext(
		ctx,
		"UPDATE attempts SET status = ?, finished_at = ? WHERE id = ? AND finished_at IS NULL",
		task.StatusCancelled,
		formatTime(now),
		attemptID,
	); err != nil {
		return task.Task{}, fmt.Errorf("closing cancelled attempt: %w", err)
	}
	payload := map[string]string{"from": string(from), "to": string(task.StatusCancelled)}
	if err := insertEvent(ctx, tx, taskID, attemptID, "task.status_changed", payload, now); err != nil {
		return task.Task{}, err
	}
	if err := insertEvent(ctx, tx, taskID, attemptID, "task.cancelled", payload, now); err != nil {
		return task.Task{}, err
	}
	for _, item := range pending {
		if err := insertEvent(ctx, tx, taskID, attemptID, "approval.cancelled", map[string]string{
			"request_id":   item.requestID,
			"operation_id": item.operationID,
		}, now); err != nil {
			return task.Task{}, err
		}
		if err := insertEvent(ctx, tx, taskID, attemptID, "operation.completed", map[string]string{
			"operation_id": item.operationID,
			"to":           string(operation.StatusCancelled),
			"error_code":   "task_cancelled",
		}, now); err != nil {
			return task.Task{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return task.Task{}, fmt.Errorf("committing task cancellation: %w", err)
	}
	return s.GetTask(ctx, taskID)
}

// CreateAttempt starts a new attempt for a paused or terminal task.
func (s *Store) CreateAttempt(
	ctx context.Context,
	taskID string,
	reason string,
) (task.Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return task.Task{}, fmt.Errorf("beginning new attempt: %w", err)
	}
	defer tx.Rollback()
	from, previousAttemptID, err := readTaskState(ctx, tx, taskID)
	if err != nil {
		return task.Task{}, err
	}
	if !from.IsTerminal() && from != task.StatusWaitingInput {
		return task.Task{}, fmt.Errorf("%w: cannot create attempt from %s", task.ErrInvalidTransition, from)
	}
	attemptID, err := id.New()
	if err != nil {
		return task.Task{}, err
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO attempts (id, task_id, status, started_at) VALUES (?, ?, ?, ?)`,
		attemptID,
		taskID,
		task.StatusCreated,
		formatTime(now),
	); err != nil {
		return task.Task{}, fmt.Errorf("inserting new attempt: %w", err)
	}
	inherited, err := inheritAttemptContext(
		ctx,
		tx,
		taskID,
		previousAttemptID,
		attemptID,
	)
	if err != nil {
		return task.Task{}, err
	}
	result, err := tx.ExecContext(
		ctx,
		`UPDATE tasks
         SET status = ?, active_attempt_id = ?, result = '', error_message = '',
             paused_from_status = NULL, lease_owner = NULL, lease_expires_at = NULL,
             heartbeat_at = NULL, updated_at = ?
         WHERE id = ? AND status = ?`,
		task.StatusCreated,
		attemptID,
		formatTime(now),
		taskID,
		from,
	)
	if err != nil {
		return task.Task{}, fmt.Errorf("activating new attempt: %w", err)
	}
	if err := requireOneRow(result, task.ErrInvalidTransition); err != nil {
		return task.Task{}, err
	}
	payload := map[string]string{
		"previous_attempt_id": previousAttemptID,
		"attempt_id":          attemptID,
		"reason":              reason,
	}
	if err := insertEvent(ctx, tx, taskID, attemptID, "task.attempt_created", payload, now); err != nil {
		return task.Task{}, err
	}
	if inherited > 0 {
		if err := insertEvent(ctx, tx, taskID, attemptID, "context.inherited", map[string]any{
			"previous_attempt_id": previousAttemptID,
			"records":             inherited,
		}, now); err != nil {
			return task.Task{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return task.Task{}, fmt.Errorf("committing new attempt: %w", err)
	}
	return s.GetTask(ctx, taskID)
}

func inheritAttemptContext(
	ctx context.Context,
	tx *sql.Tx,
	taskID string,
	previousAttemptID string,
	attemptID string,
) (int, error) {
	rows, err := tx.QueryContext(
		ctx,
		`SELECT schema_version, id, task_id, attempt_id, sequence, role,
			content_json, trust_level, source, source_ref, created_at
		 FROM context_messages WHERE attempt_id = ? ORDER BY sequence ASC`,
		previousAttemptID,
	)
	if err != nil {
		return 0, fmt.Errorf("loading previous attempt context: %w", err)
	}
	records := make([]contextbuilder.Record, 0)
	for rows.Next() {
		record, err := scanContextRecord(rows)
		if err != nil {
			rows.Close()
			return 0, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterating previous attempt context: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("closing previous attempt context: %w", err)
	}
	excluded := make(map[string]bool)
	for _, record := range records {
		_, phaseScoped := contextbuilder.PhaseFromSourceRef(record.SourceRef)
		if record.Source == contextbuilder.SourcePlugin || phaseScoped {
			excluded[record.ID] = true
		}
	}
	changed := true
	for changed {
		changed = false
		for _, record := range records {
			if excluded[record.ID] || record.Source != contextbuilder.SourceSummary {
				continue
			}
			var references []string
			if json.Unmarshal([]byte(record.SourceRef), &references) != nil {
				continue
			}
			for _, reference := range references {
				if excluded[reference] {
					excluded[record.ID] = true
					changed = true
					break
				}
			}
		}
	}
	filtered := records[:0]
	for _, record := range records {
		if !excluded[record.ID] {
			filtered = append(filtered, record)
		}
	}
	records = filtered
	ids := make(map[string]string, len(records))
	for index := range records {
		newID, err := id.New()
		if err != nil {
			return 0, err
		}
		ids[records[index].ID] = newID
		records[index].ID = newID
		records[index].AttemptID = attemptID
	}
	for _, record := range records {
		sourceRef := record.SourceRef
		if record.Source == contextbuilder.SourceSummary && sourceRef != "" {
			var references []string
			if err := json.Unmarshal([]byte(sourceRef), &references); err != nil {
				return 0, fmt.Errorf("decoding inherited summary references: %w", err)
			}
			for index, reference := range references {
				if mapped, ok := ids[reference]; ok {
					references[index] = mapped
				}
			}
			encoded, err := json.Marshal(references)
			if err != nil {
				return 0, fmt.Errorf("encoding inherited summary references: %w", err)
			}
			sourceRef = string(encoded)
		}
		content, err := json.Marshal(record.Message.Content)
		if err != nil {
			return 0, fmt.Errorf("encoding inherited context message: %w", err)
		}
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO context_messages (
				id, schema_version, task_id, attempt_id, sequence, role, content_json,
				trust_level, source, source_ref, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			record.ID,
			record.SchemaVersion,
			taskID,
			attemptID,
			record.Sequence,
			record.Message.Role,
			string(content),
			record.Trust,
			record.Source,
			sourceRef,
			formatTime(record.CreatedAt),
		); err != nil {
			return 0, fmt.Errorf("inheriting context message: %w", err)
		}
	}
	return len(records), nil
}

// SaveCheckpoint stores application state after a durable boundary.
func (s *Store) SaveCheckpoint(
	ctx context.Context,
	taskID string,
	reason string,
	state any,
) (task.Checkpoint, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return task.Checkpoint{}, fmt.Errorf("encoding checkpoint state: %w", err)
	}
	checkpointID, err := id.New()
	if err != nil {
		return task.Checkpoint{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return task.Checkpoint{}, fmt.Errorf("beginning checkpoint: %w", err)
	}
	defer tx.Rollback()
	_, attemptID, err := readTaskState(ctx, tx, taskID)
	if err != nil {
		return task.Checkpoint{}, err
	}
	var ordinal int
	if err := tx.QueryRowContext(
		ctx,
		"SELECT COALESCE(MAX(ordinal), 0) + 1 FROM checkpoints WHERE attempt_id = ?",
		attemptID,
	).Scan(&ordinal); err != nil {
		return task.Checkpoint{}, fmt.Errorf("allocating checkpoint ordinal: %w", err)
	}
	now := time.Now().UTC()
	checkpoint := task.Checkpoint{
		ID:        checkpointID,
		TaskID:    taskID,
		AttemptID: attemptID,
		Ordinal:   ordinal,
		Reason:    reason,
		State:     encoded,
		CreatedAt: now,
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO checkpoints (
            id, task_id, attempt_id, ordinal, reason, state_json, created_at
         ) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		checkpoint.ID,
		checkpoint.TaskID,
		checkpoint.AttemptID,
		checkpoint.Ordinal,
		checkpoint.Reason,
		string(checkpoint.State),
		formatTime(checkpoint.CreatedAt),
	); err != nil {
		return task.Checkpoint{}, fmt.Errorf("inserting checkpoint: %w", err)
	}
	payload := map[string]any{"checkpoint_id": checkpoint.ID, "ordinal": ordinal, "reason": reason}
	if err := insertEvent(ctx, tx, taskID, attemptID, "task.checkpoint_saved", payload, now); err != nil {
		return task.Checkpoint{}, err
	}
	if err := tx.Commit(); err != nil {
		return task.Checkpoint{}, fmt.Errorf("committing checkpoint: %w", err)
	}
	return checkpoint, nil
}

// LatestCheckpoint returns the newest checkpoint for the active attempt.
func (s *Store) LatestCheckpoint(ctx context.Context, taskID string) (task.Checkpoint, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT c.id, c.task_id, c.attempt_id, c.ordinal, c.reason, c.state_json, c.created_at
         FROM checkpoints c
         JOIN tasks t ON t.active_attempt_id = c.attempt_id
         WHERE t.id = ?
         ORDER BY c.ordinal DESC LIMIT 1`,
		taskID,
	)
	var checkpoint task.Checkpoint
	var state string
	var createdAt string
	err := row.Scan(
		&checkpoint.ID,
		&checkpoint.TaskID,
		&checkpoint.AttemptID,
		&checkpoint.Ordinal,
		&checkpoint.Reason,
		&state,
		&createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return task.Checkpoint{}, task.ErrNotFound
	}
	if err != nil {
		return task.Checkpoint{}, fmt.Errorf("querying latest checkpoint: %w", err)
	}
	checkpoint.State = json.RawMessage(state)
	checkpoint.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return task.Checkpoint{}, err
	}
	return checkpoint, nil
}

func readTaskState(
	ctx context.Context,
	tx *sql.Tx,
	taskID string,
) (task.Status, string, error) {
	var status task.Status
	var attemptID string
	err := tx.QueryRowContext(
		ctx,
		"SELECT status, active_attempt_id FROM tasks WHERE id = ?",
		taskID,
	).Scan(&status, &attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return task.StatusUnknown, "", task.ErrNotFound
	}
	if err != nil {
		return task.StatusUnknown, "", fmt.Errorf("reading task state: %w", err)
	}
	return status, attemptID, nil
}

func requireOneRow(result sql.Result, cause error) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking changed rows: %w", err)
	}
	if rows != 1 {
		return cause
	}
	return nil
}
