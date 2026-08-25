// Package sqlite provides durable task snapshots and append-only events.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/userInner/kern/internal/id"
	"github.com/userInner/kern/internal/modelconfig"
	"github.com/userInner/kern/internal/plugin"
	"github.com/userInner/kern/internal/task"
	"github.com/userInner/kern/internal/tracecontext"
	_ "modernc.org/sqlite"
)

// Store persists task state in SQLite.
type Store struct {
	db                  *sql.DB
	metrics             metricsCache
	migrationBackupPath string
}

// Open opens a SQLite store and applies the initial schema.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxIdleTime(time.Minute)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("pinging sqlite: %w", err)
	}
	backupPath, err := migrate(ctx, db, path)
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	return &Store{db: db, migrationBackupPath: backupPath}, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// MigrationBackupPath reports the consistent pre-migration backup created by
// this Open call. It is empty when no schema upgrade was required.
func (s *Store) MigrationBackupPath() string {
	return s.migrationBackupPath
}

// CreateTask atomically creates a task, its first attempt, and the initial event.
func (s *Store) CreateTask(ctx context.Context, title, goal string) (task.Task, error) {
	return s.CreateTaskWithModel(ctx, title, goal, "")
}

// CreateTaskWithModel creates a task pinned to one enabled model configuration.
// An empty config ID leaves the runtime free to use its explicit offline fallback.
func (s *Store) CreateTaskWithModel(
	ctx context.Context,
	title string,
	goal string,
	modelConfigID string,
) (task.Task, error) {
	return s.CreateTaskWithOptions(ctx, title, goal, modelConfigID, nil, nil)
}

// CreateTaskWithOptions atomically creates a task and its explicit plugin
// selection before any worker can observe or enqueue it.
func (s *Store) CreateTaskWithOptions(
	ctx context.Context,
	title string,
	goal string,
	modelConfigID string,
	enablePlugins []string,
	disablePlugins []string,
) (task.Task, error) {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return task.Task{}, errors.New("sqlite: task goal is required")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = titleFromGoal(goal)
	}
	taskID, err := id.New()
	if err != nil {
		return task.Task{}, err
	}
	attemptID, err := id.New()
	if err != nil {
		return task.Task{}, err
	}
	now := time.Now().UTC()
	created := task.Task{
		SchemaVersion:   task.SchemaVersion,
		ID:              taskID,
		TraceID:         tracecontext.AttemptTraceID(attemptID),
		Title:           title,
		Goal:            goal,
		Status:          task.StatusCreated,
		ActiveAttemptID: attemptID,
		ModelConfigID:   strings.TrimSpace(modelConfigID),
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return task.Task{}, fmt.Errorf("beginning create task: %w", err)
	}
	defer tx.Rollback()
	if created.ModelConfigID != "" {
		var enabled bool
		err := tx.QueryRowContext(
			ctx,
			"SELECT enabled FROM model_configs WHERE id = ?",
			created.ModelConfigID,
		).Scan(&enabled)
		if errors.Is(err, sql.ErrNoRows) {
			return task.Task{}, modelconfig.ErrNotFound
		}
		if err != nil {
			return task.Task{}, fmt.Errorf("checking task model config: %w", err)
		}
		if !enabled {
			return task.Task{}, modelconfig.ErrDisabled
		}
	}
	pluginChoices, err := validatePluginChoices(enablePlugins, disablePlugins)
	if err != nil {
		return task.Task{}, err
	}
	for pluginID := range pluginChoices {
		var exists bool
		if err := tx.QueryRowContext(
			ctx,
			"SELECT EXISTS(SELECT 1 FROM plugins WHERE plugin_id = ?)",
			pluginID,
		).Scan(&exists); err != nil {
			return task.Task{}, fmt.Errorf("checking selected plugin: %w", err)
		}
		if !exists {
			return task.Task{}, plugin.ErrNotFound
		}
	}

	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO tasks (
            id, schema_version, title, goal, status, active_attempt_id,
            model_config_id, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?)`,
		created.ID,
		created.SchemaVersion,
		created.Title,
		created.Goal,
		created.Status,
		created.ActiveAttemptID,
		created.ModelConfigID,
		formatTime(now),
		formatTime(now),
	)
	if err != nil {
		return task.Task{}, fmt.Errorf("inserting task: %w", err)
	}

	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO attempts (id, task_id, status, started_at) VALUES (?, ?, ?, ?)`,
		attemptID,
		taskID,
		task.StatusCreated,
		formatTime(now),
	)
	if err != nil {
		return task.Task{}, fmt.Errorf("inserting attempt: %w", err)
	}
	for pluginID, mode := range pluginChoices {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO task_plugin_preferences (task_id, plugin_id, mode, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?)`,
			created.ID,
			pluginID,
			mode,
			formatTime(now),
			formatTime(now),
		); err != nil {
			return task.Task{}, fmt.Errorf("inserting task plugin preference: %w", err)
		}
	}

	payload := map[string]any{
		"status":          string(task.StatusCreated),
		"model_config_id": created.ModelConfigID,
		"plugin_enable":   enablePlugins,
		"plugin_disable":  disablePlugins,
	}
	if err := insertEvent(ctx, tx, taskID, attemptID, "task.created", payload, now); err != nil {
		return task.Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return task.Task{}, fmt.Errorf("committing create task: %w", err)
	}

	return created, nil
}

func validatePluginChoices(
	enable []string,
	disable []string,
) (map[string]plugin.PreferenceMode, error) {
	choices := make(map[string]plugin.PreferenceMode, len(enable)+len(disable))
	for _, pluginID := range enable {
		pluginID = strings.TrimSpace(pluginID)
		if !plugin.ValidID(pluginID) {
			return nil, fmt.Errorf("%w: invalid enabled plugin ID", plugin.ErrInvalidManifest)
		}
		if _, duplicate := choices[pluginID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate plugin selection", plugin.ErrConflict)
		}
		choices[pluginID] = plugin.PreferenceEnable
	}
	for _, pluginID := range disable {
		pluginID = strings.TrimSpace(pluginID)
		if !plugin.ValidID(pluginID) {
			return nil, fmt.Errorf("%w: invalid disabled plugin ID", plugin.ErrInvalidManifest)
		}
		if _, duplicate := choices[pluginID]; duplicate {
			return nil, fmt.Errorf("%w: plugin cannot be enabled and disabled together", plugin.ErrConflict)
		}
		choices[pluginID] = plugin.PreferenceDisable
	}
	return choices, nil
}

// GetTask returns the current task snapshot.
func (s *Store) GetTask(ctx context.Context, taskID string) (task.Task, error) {
	row := s.db.QueryRowContext(
		ctx,
		`SELECT schema_version, id, title, goal, status, result, error_message,
				active_attempt_id, model_config_id, created_at, updated_at, lease_owner,
				lease_expires_at, heartbeat_at, paused_from_status
		 FROM tasks WHERE id = ?`,
		taskID,
	)

	return scanTask(row)
}

// ListTasks returns newest tasks first.
func (s *Store) ListTasks(ctx context.Context, limit int) ([]task.Task, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT schema_version, id, title, goal, status, result, error_message,
				active_attempt_id, model_config_id, created_at, updated_at, lease_owner,
				lease_expires_at, heartbeat_at, paused_from_status
		 FROM tasks ORDER BY created_at DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("listing tasks: %w", err)
	}
	defer rows.Close()

	tasks := make([]task.Task, 0, limit)
	for rows.Next() {
		item, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating tasks: %w", err)
	}

	return tasks, nil
}

// Transition updates a snapshot and appends its event in one transaction.
func (s *Store) Transition(
	ctx context.Context,
	taskID string,
	to task.Status,
	result string,
	errorMessage string,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transition: %w", err)
	}
	defer tx.Rollback()

	var from task.Status
	var attemptID string
	err = tx.QueryRowContext(
		ctx,
		"SELECT status, active_attempt_id FROM tasks WHERE id = ?",
		taskID,
	).Scan(&from, &attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return task.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("reading transition state: %w", err)
	}
	if err := task.ValidateTransition(from, to); err != nil {
		return err
	}

	now := time.Now().UTC()
	query := `UPDATE tasks
         SET status = ?, result = ?, error_message = ?, updated_at = ?
         WHERE id = ? AND status = ?`
	if to.IsTerminal() {
		query = `UPDATE tasks
         SET status = ?, result = ?, error_message = ?, updated_at = ?,
             lease_owner = NULL, lease_expires_at = NULL, heartbeat_at = NULL
         WHERE id = ? AND status = ?`
	}
	resultValue, err := tx.ExecContext(
		ctx,
		query,
		to,
		result,
		errorMessage,
		formatTime(now),
		taskID,
		from,
	)
	if err != nil {
		return fmt.Errorf("updating task transition: %w", err)
	}
	rowsAffected, err := resultValue.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking task transition: %w", err)
	}
	if rowsAffected != 1 {
		return fmt.Errorf("%w: concurrent status change", task.ErrInvalidTransition)
	}

	if to.IsTerminal() {
		_, err = tx.ExecContext(
			ctx,
			"UPDATE attempts SET status = ?, finished_at = ? WHERE id = ?",
			to,
			formatTime(now),
			attemptID,
		)
	} else {
		_, err = tx.ExecContext(
			ctx,
			"UPDATE attempts SET status = ? WHERE id = ?",
			to,
			attemptID,
		)
	}
	if err != nil {
		return fmt.Errorf("updating attempt transition: %w", err)
	}

	payload := map[string]string{"from": string(from), "to": string(to)}
	if err := insertEvent(ctx, tx, taskID, attemptID, "task.status_changed", payload, now); err != nil {
		return err
	}
	if to.IsTerminal() {
		if err := insertEvent(ctx, tx, taskID, attemptID, "task."+string(to), payload, now); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transition: %w", err)
	}
	return nil
}

// AppendEvent appends an immutable event after validating its JSON payload.
func (s *Store) AppendEvent(
	ctx context.Context,
	taskID string,
	attemptID string,
	eventType string,
	payload any,
) error {
	return insertEvent(ctx, s.db, taskID, attemptID, eventType, payload, time.Now().UTC())
}

// RecordVerification persists a verification result and its event atomically.
func (s *Store) RecordVerification(ctx context.Context, verification task.Verification) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning verification: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO verifications (
            id, task_id, attempt_id, verifier, status, evidence_json, created_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		verification.ID,
		verification.TaskID,
		verification.AttemptID,
		verification.Verifier,
		verification.Status,
		string(verification.Evidence),
		formatTime(verification.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("inserting verification: %w", err)
	}
	payload := map[string]any{
		"verification_id": verification.ID,
		"verifier":        verification.Verifier,
		"status":          verification.Status,
		"evidence":        json.RawMessage(verification.Evidence),
	}
	if err := insertEvent(
		ctx,
		tx,
		verification.TaskID,
		verification.AttemptID,
		"verification.completed",
		payload,
		verification.CreatedAt,
	); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing verification: %w", err)
	}
	return nil
}

// EventsAfter returns persisted events with IDs greater than afterID.
func (s *Store) EventsAfter(
	ctx context.Context,
	taskID string,
	afterID int64,
	limit int,
) ([]task.Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT schema_version, id, task_id, attempt_id, type, payload_json, created_at
         FROM task_events
         WHERE task_id = ? AND id > ?
         ORDER BY id ASC LIMIT ?`,
		taskID,
		afterID,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("querying task events: %w", err)
	}
	defer rows.Close()

	events := make([]task.Event, 0, limit)
	for rows.Next() {
		var item task.Event
		var payload string
		var createdAt string
		if err := rows.Scan(
			&item.SchemaVersion,
			&item.ID,
			&item.TaskID,
			&item.AttemptID,
			&item.Type,
			&payload,
			&createdAt,
		); err != nil {
			return nil, fmt.Errorf("scanning task event: %w", err)
		}
		item.Payload = json.RawMessage(payload)
		item.TraceID = tracecontext.AttemptTraceID(item.AttemptID)
		item.SpanID, item.ParentSpanID = tracecontext.EventSpan(item.AttemptID, item.Type, item.Payload)
		item.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		events = append(events, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating task events: %w", err)
	}
	return events, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

type queryExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func scanTask(row rowScanner) (task.Task, error) {
	var item task.Task
	var createdAt string
	var updatedAt string
	var leaseOwner sql.NullString
	var modelConfigID sql.NullString
	var leaseExpiresAt sql.NullString
	var heartbeatAt sql.NullString
	var pausedFrom sql.NullString
	err := row.Scan(
		&item.SchemaVersion,
		&item.ID,
		&item.Title,
		&item.Goal,
		&item.Status,
		&item.Result,
		&item.ErrorMessage,
		&item.ActiveAttemptID,
		&modelConfigID,
		&createdAt,
		&updatedAt,
		&leaseOwner,
		&leaseExpiresAt,
		&heartbeatAt,
		&pausedFrom,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return task.Task{}, task.ErrNotFound
	}
	if err != nil {
		return task.Task{}, fmt.Errorf("scanning task: %w", err)
	}
	item.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return task.Task{}, err
	}
	item.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return task.Task{}, err
	}
	item.LeaseOwner = leaseOwner.String
	item.TraceID = tracecontext.AttemptTraceID(item.ActiveAttemptID)
	item.ModelConfigID = modelConfigID.String
	item.PausedFrom = task.Status(pausedFrom.String)
	if leaseExpiresAt.Valid {
		parsed, err := parseTime(leaseExpiresAt.String)
		if err != nil {
			return task.Task{}, err
		}
		item.LeaseExpiresAt = &parsed
	}
	if heartbeatAt.Valid {
		parsed, err := parseTime(heartbeatAt.String)
		if err != nil {
			return task.Task{}, err
		}
		item.HeartbeatAt = &parsed
	}
	return item, nil
}

func insertEvent(
	ctx context.Context,
	execer queryExecer,
	taskID string,
	attemptID string,
	eventType string,
	payload any,
	createdAt time.Time,
) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encoding event payload: %w", err)
	}
	if !json.Valid(encoded) {
		return errors.New("sqlite: invalid event payload")
	}
	_, err = execer.ExecContext(
		ctx,
		`INSERT INTO task_events (
            schema_version, task_id, attempt_id, type, payload_json, created_at
        ) VALUES (?, ?, ?, ?, ?, ?)`,
		task.SchemaVersion,
		taskID,
		attemptID,
		eventType,
		string(encoded),
		formatTime(createdAt),
	)
	if err != nil {
		return fmt.Errorf("inserting task event: %w", err)
	}
	return nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing database time: %w", err)
	}
	return parsed, nil
}

func titleFromGoal(goal string) string {
	runes := []rune(goal)
	if len(runes) <= 36 {
		return goal
	}
	return string(runes[:36]) + "…"
}
