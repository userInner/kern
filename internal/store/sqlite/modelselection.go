package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/userInner/kern/internal/modelconfig"
	"github.com/userInner/kern/internal/task"
)

// RecordModelSelection stores the secret-free model snapshot used by one Attempt.
// Repeating the exact snapshot is idempotent; a different snapshot is rejected.
func (s *Store) RecordModelSelection(
	ctx context.Context,
	item task.Task,
	config modelconfig.Config,
	parameters json.RawMessage,
) (modelconfig.Selection, bool, error) {
	if item.ID == "" || item.ActiveAttemptID == "" || config.ID == "" {
		return modelconfig.Selection{}, false, errors.New("sqlite: task, attempt, and model config are required")
	}
	if len(parameters) == 0 {
		parameters = json.RawMessage(`{}`)
	}
	if len(parameters) > 64<<10 || !json.Valid(parameters) {
		return modelconfig.Selection{}, false, errors.New("sqlite: model parameters must be valid JSON within 64 KiB")
	}
	compacted := make(json.RawMessage, 0, len(parameters))
	buffer := bytes.NewBuffer(compacted)
	if err := json.Compact(buffer, parameters); err != nil {
		return modelconfig.Selection{}, false, fmt.Errorf("compacting model parameters: %w", err)
	}
	parameters = json.RawMessage(buffer.Bytes())
	now := time.Now().UTC()

	result, err := s.db.ExecContext(
		ctx,
		`INSERT INTO attempt_model_configs (
            attempt_id, task_id, config_id, schema_version, provider,
            base_url, model, parameters_json, created_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(attempt_id) DO NOTHING`,
		item.ActiveAttemptID,
		item.ID,
		config.ID,
		modelconfig.SchemaVersion,
		config.Provider,
		config.BaseURL,
		config.Model,
		string(parameters),
		formatTime(now),
	)
	if err != nil {
		return modelconfig.Selection{}, false, fmt.Errorf("recording attempt model selection: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return modelconfig.Selection{}, false, fmt.Errorf("checking attempt model selection: %w", err)
	}
	selection, err := s.ModelSelection(ctx, item.ActiveAttemptID)
	if err != nil {
		return modelconfig.Selection{}, false, err
	}
	if selection.TaskID != item.ID || selection.ConfigID != config.ID ||
		selection.Provider != config.Provider || selection.BaseURL != config.BaseURL ||
		selection.Model != config.Model || !bytes.Equal(selection.Parameters, parameters) {
		return modelconfig.Selection{}, false, errors.New("sqlite: attempt model selection conflicts with its durable snapshot")
	}
	return selection, rows == 1, nil
}

// ModelSelection returns the immutable model snapshot for one Attempt.
func (s *Store) ModelSelection(
	ctx context.Context,
	attemptID string,
) (modelconfig.Selection, error) {
	var selection modelconfig.Selection
	var parameters, createdAt string
	var configID sql.NullString
	err := s.db.QueryRowContext(
		ctx,
		`SELECT schema_version, task_id, attempt_id, config_id, provider,
                base_url, model, parameters_json, created_at
         FROM attempt_model_configs WHERE attempt_id = ?`,
		attemptID,
	).Scan(
		&selection.SchemaVersion,
		&selection.TaskID,
		&selection.AttemptID,
		&configID,
		&selection.Provider,
		&selection.BaseURL,
		&selection.Model,
		&parameters,
		&createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return modelconfig.Selection{}, modelconfig.ErrNotFound
	}
	if err != nil {
		return modelconfig.Selection{}, fmt.Errorf("reading attempt model selection: %w", err)
	}
	selection.ConfigID = configID.String
	selection.Parameters = json.RawMessage(parameters)
	selection.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return modelconfig.Selection{}, err
	}
	return selection, nil
}
