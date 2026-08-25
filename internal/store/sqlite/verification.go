package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/userInner/kern/internal/task"
)

// ListVerifications returns all deterministic checks for one Attempt in the
// order they completed. The final item is the aggregate check when the Engine
// completed the full suite.
func (s *Store) ListVerifications(
	ctx context.Context,
	taskID string,
	attemptID string,
) ([]task.Verification, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, task_id, attempt_id, verifier, status, evidence_json, created_at
		 FROM verifications
		 WHERE task_id = ? AND attempt_id = ?
		 ORDER BY created_at ASC, rowid ASC`,
		taskID,
		attemptID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing verifications: %w", err)
	}
	defer rows.Close()
	items := make([]task.Verification, 0)
	for rows.Next() {
		item, err := scanVerification(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating verifications: %w", err)
	}
	return items, nil
}

func scanVerification(row rowScanner) (task.Verification, error) {
	var item task.Verification
	var evidence string
	var createdAt string
	err := row.Scan(
		&item.ID,
		&item.TaskID,
		&item.AttemptID,
		&item.Verifier,
		&item.Status,
		&evidence,
		&createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return task.Verification{}, task.ErrNotFound
	}
	if err != nil {
		return task.Verification{}, fmt.Errorf("scanning verification: %w", err)
	}
	if !json.Valid([]byte(evidence)) {
		return task.Verification{}, errors.New("sqlite: stored verification evidence is invalid")
	}
	item.Evidence = json.RawMessage(evidence)
	item.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return task.Verification{}, err
	}
	return item, nil
}
