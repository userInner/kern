package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// CleanupSummary reports terminal task records and artifact references that
// match one retention cutoff. Content object removal is handled after commit.
type CleanupSummary struct {
	TaskCount     int      `json:"task_count"`
	ArtifactCount int      `json:"artifact_count"`
	ArtifactBytes int64    `json:"artifact_bytes"`
	ArtifactPaths []string `json:"-"`
}

const expiredTerminalTasks = `status IN ('completed', 'partially_completed', 'failed', 'cancelled') AND updated_at < ?`

// PreviewTerminalTaskCleanup counts data eligible for retention cleanup.
func (s *Store) PreviewTerminalTaskCleanup(ctx context.Context, before time.Time) (CleanupSummary, error) {
	if before.IsZero() {
		return CleanupSummary{}, errors.New("sqlite: cleanup cutoff is required")
	}
	cutoff := formatTime(before)
	var summary CleanupSummary
	if err := s.db.QueryRowContext(
		ctx,
		"SELECT COUNT(*) FROM tasks WHERE "+expiredTerminalTasks,
		cutoff,
	).Scan(&summary.TaskCount); err != nil {
		return CleanupSummary{}, fmt.Errorf("counting expired tasks: %w", err)
	}
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*), COALESCE(SUM(a.size), 0)
         FROM artifacts a JOIN tasks t ON t.id = a.task_id
         WHERE t.`+expiredTerminalTasks,
		cutoff,
	).Scan(&summary.ArtifactCount, &summary.ArtifactBytes); err != nil {
		return CleanupSummary{}, fmt.Errorf("counting expired task artifacts: %w", err)
	}
	return summary, nil
}

// DeleteTerminalTasksBefore atomically deletes only tasks that are still
// terminal and older than before. Foreign-key cascades remove their ledger
// records; returned paths are candidates for content-object pruning.
func (s *Store) DeleteTerminalTasksBefore(ctx context.Context, before time.Time) (CleanupSummary, error) {
	if before.IsZero() {
		return CleanupSummary{}, errors.New("sqlite: cleanup cutoff is required")
	}
	cutoff := formatTime(before)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CleanupSummary{}, fmt.Errorf("beginning task cleanup: %w", err)
	}
	defer tx.Rollback()
	summary, err := previewCleanupTx(ctx, tx, cutoff)
	if err != nil {
		return CleanupSummary{}, err
	}
	rows, err := tx.QueryContext(
		ctx,
		`SELECT DISTINCT a.storage_path
         FROM artifacts a JOIN tasks t ON t.id = a.task_id
         WHERE t.`+expiredTerminalTasks+`
         ORDER BY a.storage_path`,
		cutoff,
	)
	if err != nil {
		return CleanupSummary{}, fmt.Errorf("listing cleanup artifact objects: %w", err)
	}
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			rows.Close()
			return CleanupSummary{}, fmt.Errorf("scanning cleanup artifact object: %w", err)
		}
		summary.ArtifactPaths = append(summary.ArtifactPaths, path)
	}
	if err := rows.Close(); err != nil {
		return CleanupSummary{}, fmt.Errorf("closing cleanup artifact rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return CleanupSummary{}, fmt.Errorf("iterating cleanup artifact objects: %w", err)
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM tasks WHERE "+expiredTerminalTasks, cutoff)
	if err != nil {
		return CleanupSummary{}, fmt.Errorf("deleting expired terminal tasks: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return CleanupSummary{}, fmt.Errorf("counting deleted terminal tasks: %w", err)
	}
	summary.TaskCount = int(deleted)
	if err := tx.Commit(); err != nil {
		return CleanupSummary{}, fmt.Errorf("committing task cleanup: %w", err)
	}
	return summary, nil
}

func previewCleanupTx(ctx context.Context, tx *sql.Tx, cutoff string) (CleanupSummary, error) {
	var summary CleanupSummary
	if err := tx.QueryRowContext(
		ctx,
		"SELECT COUNT(*) FROM tasks WHERE "+expiredTerminalTasks,
		cutoff,
	).Scan(&summary.TaskCount); err != nil {
		return CleanupSummary{}, fmt.Errorf("counting cleanup tasks: %w", err)
	}
	if err := tx.QueryRowContext(
		ctx,
		`SELECT COUNT(*), COALESCE(SUM(a.size), 0)
         FROM artifacts a JOIN tasks t ON t.id = a.task_id
         WHERE t.`+expiredTerminalTasks,
		cutoff,
	).Scan(&summary.ArtifactCount, &summary.ArtifactBytes); err != nil {
		return CleanupSummary{}, fmt.Errorf("counting cleanup artifacts: %w", err)
	}
	return summary, nil
}

// ArtifactStoragePathReferenced reports whether a content-addressed object is
// still referenced after task cleanup commits.
func (s *Store) ArtifactStoragePathReferenced(ctx context.Context, path string) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(
		ctx,
		"SELECT 1 FROM artifacts WHERE storage_path = ? LIMIT 1",
		path,
	).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking artifact object reference: %w", err)
	}
	return true, nil
}
