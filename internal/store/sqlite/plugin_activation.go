package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/userInner/kern/internal/plugin"
)

// SetTaskPluginPreferences replaces all explicit plugin choices for a task.
func (s *Store) SetTaskPluginPreferences(
	ctx context.Context,
	taskID string,
	enable []string,
	disable []string,
) error {
	choices := make(map[string]plugin.PreferenceMode, len(enable)+len(disable))
	for _, pluginID := range enable {
		pluginID = strings.TrimSpace(pluginID)
		if !plugin.ValidID(pluginID) {
			return errors.New("sqlite: invalid enabled plugin ID")
		}
		choices[pluginID] = plugin.PreferenceEnable
	}
	for _, pluginID := range disable {
		pluginID = strings.TrimSpace(pluginID)
		if !plugin.ValidID(pluginID) {
			return errors.New("sqlite: invalid disabled plugin ID")
		}
		choices[pluginID] = plugin.PreferenceDisable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning plugin preference update: %w", err)
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM tasks WHERE id = ?)", taskID).Scan(&exists); err != nil {
		return fmt.Errorf("checking plugin preference task: %w", err)
	}
	if !exists {
		return errors.New("sqlite: plugin preference task not found")
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM task_plugin_preferences WHERE task_id = ?", taskID); err != nil {
		return fmt.Errorf("clearing plugin preferences: %w", err)
	}
	now := time.Now().UTC()
	for pluginID, mode := range choices {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO task_plugin_preferences (task_id, plugin_id, mode, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?)`,
			taskID,
			pluginID,
			mode,
			formatTime(now),
			formatTime(now),
		); err != nil {
			return fmt.Errorf("setting plugin preference for %s: %w", pluginID, err)
		}
	}
	return tx.Commit()
}

// TaskPluginPreferences returns explicit choices ordered by plugin ID.
func (s *Store) TaskPluginPreferences(ctx context.Context, taskID string) ([]plugin.Preference, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT task_id, plugin_id, mode, created_at, updated_at
		 FROM task_plugin_preferences WHERE task_id = ? ORDER BY plugin_id`,
		taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing plugin preferences: %w", err)
	}
	defer rows.Close()
	items := make([]plugin.Preference, 0)
	for rows.Next() {
		var item plugin.Preference
		var createdAt, updatedAt string
		if err := rows.Scan(&item.TaskID, &item.PluginID, &item.Mode, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scanning plugin preference: %w", err)
		}
		item.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		item.UpdatedAt, err = parseTime(updatedAt)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// RecordAttemptPlugin stores immutable activation evidence and emits its task event.
func (s *Store) RecordAttemptPlugin(ctx context.Context, usage plugin.Usage) (bool, error) {
	if usage.SchemaVersion != plugin.SchemaVersion || !plugin.ValidID(usage.PluginID) ||
		usage.TaskID == "" || usage.AttemptID == "" || usage.Version == "" || usage.Digest == "" ||
		usage.Reason == "" || !json.Valid(usage.Resources) {
		return false, errors.New("sqlite: invalid attempt plugin usage")
	}
	if usage.CreatedAt.IsZero() {
		usage.CreatedAt = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("beginning plugin usage: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(
		ctx,
		`INSERT INTO attempt_plugins (
			task_id, attempt_id, plugin_id, schema_version, version, digest,
			reason, resources_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(attempt_id, plugin_id) DO NOTHING`,
		usage.TaskID,
		usage.AttemptID,
		usage.PluginID,
		usage.SchemaVersion,
		usage.Version,
		usage.Digest,
		usage.Reason,
		string(usage.Resources),
		formatTime(usage.CreatedAt),
	)
	if err != nil {
		return false, fmt.Errorf("recording plugin usage: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("checking plugin usage insert: %w", err)
	}
	if rows == 0 {
		return false, tx.Commit()
	}
	if err := insertEvent(ctx, tx, usage.TaskID, usage.AttemptID, "plugin.activated", map[string]any{
		"plugin_id": usage.PluginID,
		"version":   usage.Version,
		"digest":    usage.Digest,
		"reason":    usage.Reason,
		"resources": json.RawMessage(usage.Resources),
	}, usage.CreatedAt); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// ListAttemptPlugins returns immutable activation evidence.
func (s *Store) ListAttemptPlugins(ctx context.Context, taskID, attemptID string) ([]plugin.Usage, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT schema_version, task_id, attempt_id, plugin_id, version, digest,
			reason, resources_json, created_at
		 FROM attempt_plugins WHERE task_id = ? AND attempt_id = ? ORDER BY plugin_id`,
		taskID,
		attemptID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing attempt plugins: %w", err)
	}
	defer rows.Close()
	items := make([]plugin.Usage, 0)
	for rows.Next() {
		var item plugin.Usage
		var resources, createdAt string
		if err := rows.Scan(
			&item.SchemaVersion,
			&item.TaskID,
			&item.AttemptID,
			&item.PluginID,
			&item.Version,
			&item.Digest,
			&item.Reason,
			&resources,
			&createdAt,
		); err != nil {
			return nil, fmt.Errorf("scanning attempt plugin: %w", err)
		}
		item.Resources = json.RawMessage(resources)
		item.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("iterating attempt plugins: %w", err)
	}
	return items, nil
}
