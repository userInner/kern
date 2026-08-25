package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/userInner/kern/internal/id"
	"github.com/userInner/kern/internal/plan"
)

// CreatePlan stores a complete validated revision and its pending steps in one
// transaction. An older active revision for the same Attempt is superseded.
func (s *Store) CreatePlan(
	ctx context.Context,
	taskID string,
	draft plan.Draft,
) (plan.Plan, error) {
	if err := plan.ValidateDraft(draft); err != nil {
		return plan.Plan{}, err
	}
	planID, err := id.New()
	if err != nil {
		return plan.Plan{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return plan.Plan{}, fmt.Errorf("beginning plan creation: %w", err)
	}
	defer tx.Rollback()
	_, attemptID, err := readTaskState(ctx, tx, taskID)
	if err != nil {
		return plan.Plan{}, err
	}
	var revision int
	if err := tx.QueryRowContext(
		ctx,
		"SELECT COALESCE(MAX(revision), 0) + 1 FROM plans WHERE attempt_id = ?",
		attemptID,
	).Scan(&revision); err != nil {
		return plan.Plan{}, fmt.Errorf("allocating plan revision: %w", err)
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(
		ctx,
		"UPDATE plans SET status = ?, updated_at = ? WHERE attempt_id = ? AND status = ?",
		plan.StatusSuperseded,
		formatTime(now),
		attemptID,
		plan.StatusActive,
	); err != nil {
		return plan.Plan{}, fmt.Errorf("superseding active plan: %w", err)
	}
	created := plan.Plan{
		SchemaVersion: plan.SchemaVersion,
		ID:            planID,
		TaskID:        taskID,
		AttemptID:     attemptID,
		Revision:      revision,
		Status:        plan.StatusActive,
		Rationale:     draft.Rationale,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO plans (
            id, schema_version, task_id, attempt_id, revision, status,
            rationale, created_at, updated_at
         ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		created.ID,
		created.SchemaVersion,
		created.TaskID,
		created.AttemptID,
		created.Revision,
		created.Status,
		created.Rationale,
		formatTime(created.CreatedAt),
		formatTime(created.UpdatedAt),
	); err != nil {
		return plan.Plan{}, fmt.Errorf("inserting plan: %w", err)
	}
	created.Steps = make([]plan.Step, 0, len(draft.Steps))
	for index, draftStep := range draft.Steps {
		stepID, err := id.New()
		if err != nil {
			return plan.Plan{}, err
		}
		step := plan.Step{
			ID:          stepID,
			PlanID:      created.ID,
			Ordinal:     index + 1,
			Title:       draftStep.Title,
			Description: draftStep.Description,
			Phase:       draftStep.Phase,
			Required:    draftStep.Required,
			Status:      plan.StepStatusPending,
			UpdatedAt:   now,
		}
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO plan_steps (
                id, plan_id, ordinal, title, description, phase, required,
                status, updated_at
             ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			step.ID,
			step.PlanID,
			step.Ordinal,
			step.Title,
			step.Description,
			step.Phase,
			boolInt(step.Required),
			step.Status,
			formatTime(step.UpdatedAt),
		); err != nil {
			return plan.Plan{}, fmt.Errorf("inserting plan step: %w", err)
		}
		created.Steps = append(created.Steps, step)
	}
	if err := insertEvent(ctx, tx, taskID, attemptID, "task.plan_updated", created, now); err != nil {
		return plan.Plan{}, err
	}
	if err := tx.Commit(); err != nil {
		return plan.Plan{}, fmt.Errorf("committing plan creation: %w", err)
	}
	return created, nil
}

// CurrentPlan returns the newest plan revision for a task's active Attempt.
func (s *Store) CurrentPlan(ctx context.Context, taskID string) (plan.Plan, error) {
	item, err := scanPlan(s.db.QueryRowContext(
		ctx,
		`SELECT p.schema_version, p.id, p.task_id, p.attempt_id, p.revision,
                p.status, p.rationale, p.created_at, p.updated_at
         FROM plans p
         JOIN tasks t ON t.active_attempt_id = p.attempt_id
         WHERE p.task_id = ?
         ORDER BY p.revision DESC LIMIT 1`,
		taskID,
	))
	if err != nil {
		return plan.Plan{}, err
	}
	steps, err := s.listPlanSteps(ctx, item.ID)
	if err != nil {
		return plan.Plan{}, err
	}
	item.Steps = steps
	return item, nil
}

// TransitionPlanStep changes a step and plan aggregate atomically and appends
// an audit event. Retrying a failed step is the only backward transition.
func (s *Store) TransitionPlanStep(
	ctx context.Context,
	stepID string,
	to plan.StepStatus,
	failure string,
) (plan.Plan, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return plan.Plan{}, fmt.Errorf("beginning plan step transition: %w", err)
	}
	defer tx.Rollback()
	current, taskID, attemptID, err := scanStepWithOwner(tx.QueryRowContext(
		ctx,
		`SELECT s.id, s.plan_id, s.ordinal, s.title, s.description, s.phase,
                s.required, s.status, s.failure, s.retry_count,
                s.started_at, s.completed_at, s.updated_at,
                p.task_id, p.attempt_id
         FROM plan_steps s JOIN plans p ON p.id = s.plan_id
         WHERE s.id = ?`,
		stepID,
	))
	if err != nil {
		return plan.Plan{}, err
	}
	if err := plan.ValidateStepTransition(current.Status, to); err != nil {
		return plan.Plan{}, err
	}
	now := time.Now().UTC()
	startedAt := current.StartedAt
	completedAt := current.CompletedAt
	retryCount := current.RetryCount
	if current.Status == plan.StepStatusPending && to == plan.StepStatusRunning {
		startedAt = &now
		completedAt = nil
		failure = ""
	}
	if to == plan.StepStatusCompleted || to == plan.StepStatusFailed || to == plan.StepStatusSkipped {
		completedAt = &now
	}
	if current.Status == plan.StepStatusFailed && to == plan.StepStatusPending {
		retryCount++
		startedAt = nil
		completedAt = nil
		failure = ""
	}
	changed, err := tx.ExecContext(
		ctx,
		`UPDATE plan_steps
         SET status = ?, failure = ?, retry_count = ?, started_at = ?,
             completed_at = ?, updated_at = ?
         WHERE id = ? AND status = ?`,
		to,
		failure,
		retryCount,
		nullableTime(startedAt),
		nullableTime(completedAt),
		formatTime(now),
		current.ID,
		current.Status,
	)
	if err != nil {
		return plan.Plan{}, fmt.Errorf("updating plan step: %w", err)
	}
	if err := requireOneRow(changed, plan.ErrInvalidTransition); err != nil {
		return plan.Plan{}, err
	}
	aggregate, err := aggregatePlanStatus(ctx, tx, current.PlanID)
	if err != nil {
		return plan.Plan{}, err
	}
	if _, err := tx.ExecContext(
		ctx,
		"UPDATE plans SET status = ?, updated_at = ? WHERE id = ?",
		aggregate,
		formatTime(now),
		current.PlanID,
	); err != nil {
		return plan.Plan{}, fmt.Errorf("updating plan aggregate: %w", err)
	}
	payload := map[string]any{
		"plan_id":     current.PlanID,
		"step_id":     current.ID,
		"ordinal":     current.Ordinal,
		"phase":       current.Phase,
		"from":        current.Status,
		"to":          to,
		"retry_count": retryCount,
		"failure":     failure,
		"plan_status": aggregate,
	}
	if err := insertEvent(ctx, tx, taskID, attemptID, "task.step_updated", payload, now); err != nil {
		return plan.Plan{}, err
	}
	if err := tx.Commit(); err != nil {
		return plan.Plan{}, fmt.Errorf("committing plan step transition: %w", err)
	}
	return s.CurrentPlan(ctx, taskID)
}

func (s *Store) listPlanSteps(ctx context.Context, planID string) ([]plan.Step, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, plan_id, ordinal, title, description, phase, required,
                status, failure, retry_count, started_at, completed_at, updated_at
         FROM plan_steps WHERE plan_id = ? ORDER BY ordinal ASC`,
		planID,
	)
	if err != nil {
		return nil, fmt.Errorf("querying plan steps: %w", err)
	}
	defer rows.Close()
	steps := make([]plan.Step, 0)
	for rows.Next() {
		step, err := scanPlanStep(rows)
		if err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating plan steps: %w", err)
	}
	return steps, nil
}

func aggregatePlanStatus(ctx context.Context, tx *sql.Tx, planID string) (plan.Status, error) {
	var failedRequired int
	var unfinishedRequired int
	if err := tx.QueryRowContext(
		ctx,
		`SELECT
            COALESCE(SUM(CASE WHEN required = 1 AND status = 'failed' THEN 1 ELSE 0 END), 0),
            COALESCE(SUM(CASE WHEN required = 1 AND status NOT IN ('completed', 'skipped', 'failed') THEN 1 ELSE 0 END), 0)
         FROM plan_steps WHERE plan_id = ?`,
		planID,
	).Scan(&failedRequired, &unfinishedRequired); err != nil {
		return plan.StatusUnknown, fmt.Errorf("aggregating plan status: %w", err)
	}
	if failedRequired > 0 {
		return plan.StatusFailed, nil
	}
	if unfinishedRequired == 0 {
		return plan.StatusCompleted, nil
	}
	return plan.StatusActive, nil
}

func scanPlan(row rowScanner) (plan.Plan, error) {
	var item plan.Plan
	var createdAt string
	var updatedAt string
	err := row.Scan(
		&item.SchemaVersion,
		&item.ID,
		&item.TaskID,
		&item.AttemptID,
		&item.Revision,
		&item.Status,
		&item.Rationale,
		&createdAt,
		&updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return plan.Plan{}, plan.ErrNotFound
	}
	if err != nil {
		return plan.Plan{}, fmt.Errorf("scanning plan: %w", err)
	}
	item.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return plan.Plan{}, err
	}
	item.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return plan.Plan{}, err
	}
	return item, nil
}

func scanPlanStep(row rowScanner) (plan.Step, error) {
	var item plan.Step
	var required int
	var startedAt sql.NullString
	var completedAt sql.NullString
	var updatedAt string
	err := row.Scan(
		&item.ID,
		&item.PlanID,
		&item.Ordinal,
		&item.Title,
		&item.Description,
		&item.Phase,
		&required,
		&item.Status,
		&item.Failure,
		&item.RetryCount,
		&startedAt,
		&completedAt,
		&updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return plan.Step{}, plan.ErrNotFound
	}
	if err != nil {
		return plan.Step{}, fmt.Errorf("scanning plan step: %w", err)
	}
	item.Required = required == 1
	item.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return plan.Step{}, err
	}
	if startedAt.Valid {
		value, err := parseTime(startedAt.String)
		if err != nil {
			return plan.Step{}, err
		}
		item.StartedAt = &value
	}
	if completedAt.Valid {
		value, err := parseTime(completedAt.String)
		if err != nil {
			return plan.Step{}, err
		}
		item.CompletedAt = &value
	}
	return item, nil
}

func scanStepWithOwner(row rowScanner) (plan.Step, string, string, error) {
	var item plan.Step
	var taskID string
	var attemptID string
	var required int
	var startedAt sql.NullString
	var completedAt sql.NullString
	var updatedAt string
	err := row.Scan(
		&item.ID,
		&item.PlanID,
		&item.Ordinal,
		&item.Title,
		&item.Description,
		&item.Phase,
		&required,
		&item.Status,
		&item.Failure,
		&item.RetryCount,
		&startedAt,
		&completedAt,
		&updatedAt,
		&taskID,
		&attemptID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return plan.Step{}, "", "", plan.ErrNotFound
	}
	if err != nil {
		return plan.Step{}, "", "", fmt.Errorf("scanning owned plan step: %w", err)
	}
	item.Required = required == 1
	item.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return plan.Step{}, "", "", err
	}
	if startedAt.Valid {
		value, err := parseTime(startedAt.String)
		if err != nil {
			return plan.Step{}, "", "", err
		}
		item.StartedAt = &value
	}
	if completedAt.Valid {
		value, err := parseTime(completedAt.String)
		if err != nil {
			return plan.Step{}, "", "", err
		}
		item.CompletedAt = &value
	}
	return item, taskID, attemptID, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}
