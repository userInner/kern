package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/userInner/kern/internal/artifact"
)

// SaveArtifact persists one immutable task reference and its creation event.
func (s *Store) SaveArtifact(ctx context.Context, item artifact.Artifact) error {
	if item.ID == "" || item.TaskID == "" || item.AttemptID == "" || item.Digest == "" {
		return errors.New("sqlite: artifact identity is incomplete")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning artifact creation: %w", err)
	}
	defer tx.Rollback()
	var attemptTaskID string
	if err := tx.QueryRowContext(ctx, "SELECT task_id FROM attempts WHERE id = ?", item.AttemptID).Scan(&attemptTaskID); err != nil {
		return fmt.Errorf("checking artifact attempt: %w", err)
	}
	if attemptTaskID != item.TaskID {
		return errors.New("sqlite: artifact attempt does not belong to task")
	}
	if item.SourceOperationID != "" {
		var operationTaskID, operationAttemptID string
		if err := tx.QueryRowContext(
			ctx,
			"SELECT task_id, attempt_id FROM operations WHERE id = ?",
			item.SourceOperationID,
		).Scan(&operationTaskID, &operationAttemptID); err != nil {
			return fmt.Errorf("checking artifact source operation: %w", err)
		}
		if operationTaskID != item.TaskID || operationAttemptID != item.AttemptID {
			return errors.New("sqlite: artifact source operation does not belong to attempt")
		}
	}
	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO artifacts (
			id, schema_version, task_id, attempt_id, name, digest, media_type,
			size, storage_path, source_operation_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?)`,
		item.ID,
		item.SchemaVersion,
		item.TaskID,
		item.AttemptID,
		item.Name,
		item.Digest,
		item.MediaType,
		item.Size,
		item.StoragePath,
		item.SourceOperationID,
		formatTime(item.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("inserting artifact: %w", err)
	}
	payload := map[string]any{
		"artifact_id":         item.ID,
		"name":                item.Name,
		"digest":              item.Digest,
		"media_type":          item.MediaType,
		"size":                item.Size,
		"source_operation_id": item.SourceOperationID,
	}
	if err := insertEvent(ctx, tx, item.TaskID, item.AttemptID, "artifact.created", payload, item.CreatedAt); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing artifact creation: %w", err)
	}
	return nil
}

// GetArtifact returns one artifact reference by id.
func (s *Store) GetArtifact(ctx context.Context, artifactID string) (artifact.Artifact, error) {
	return scanArtifact(s.db.QueryRowContext(
		ctx,
		`SELECT schema_version, id, task_id, attempt_id, name, digest,
			media_type, size, storage_path, COALESCE(source_operation_id, ''), created_at
		 FROM artifacts WHERE id = ?`,
		artifactID,
	))
}

// ListArtifacts returns all references for a task in creation order.
func (s *Store) ListArtifacts(ctx context.Context, taskID string) ([]artifact.Artifact, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT schema_version, id, task_id, attempt_id, name, digest,
			media_type, size, storage_path, COALESCE(source_operation_id, ''), created_at
		 FROM artifacts WHERE task_id = ? ORDER BY created_at ASC, id ASC`,
		taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing artifacts: %w", err)
	}
	defer rows.Close()
	items := make([]artifact.Artifact, 0)
	for rows.Next() {
		item, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating artifacts: %w", err)
	}
	return items, nil
}

func scanArtifact(row rowScanner) (artifact.Artifact, error) {
	var item artifact.Artifact
	var createdAt string
	err := row.Scan(
		&item.SchemaVersion,
		&item.ID,
		&item.TaskID,
		&item.AttemptID,
		&item.Name,
		&item.Digest,
		&item.MediaType,
		&item.Size,
		&item.StoragePath,
		&item.SourceOperationID,
		&createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return artifact.Artifact{}, artifact.ErrNotFound
	}
	if err != nil {
		return artifact.Artifact{}, fmt.Errorf("scanning artifact: %w", err)
	}
	item.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return artifact.Artifact{}, err
	}
	return item, nil
}
