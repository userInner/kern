package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/userInner/kern/internal/contextbuilder"
	"github.com/userInner/kern/internal/model"
)

// AppendContextMessage atomically assigns the next attempt-local sequence.
func (s *Store) AppendContextMessage(
	ctx context.Context,
	record contextbuilder.Record,
) (contextbuilder.Record, error) {
	encoded, err := json.Marshal(record.Message.Content)
	if err != nil {
		return contextbuilder.Record{}, fmt.Errorf("encoding context message: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return contextbuilder.Record{}, fmt.Errorf("beginning context append: %w", err)
	}
	defer tx.Rollback()
	var taskID string
	if err := tx.QueryRowContext(ctx, "SELECT task_id FROM attempts WHERE id = ?", record.AttemptID).Scan(&taskID); err != nil {
		return contextbuilder.Record{}, fmt.Errorf("checking context attempt: %w", err)
	}
	if taskID != record.TaskID {
		return contextbuilder.Record{}, errors.New("sqlite: context attempt does not belong to task")
	}
	if err := tx.QueryRowContext(
		ctx,
		"SELECT COALESCE(MAX(sequence), 0) + 1 FROM context_messages WHERE attempt_id = ?",
		record.AttemptID,
	).Scan(&record.Sequence); err != nil {
		return contextbuilder.Record{}, fmt.Errorf("assigning context sequence: %w", err)
	}
	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO context_messages (
			id, schema_version, task_id, attempt_id, sequence, role, content_json,
			trust_level, source, source_ref, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID,
		record.SchemaVersion,
		record.TaskID,
		record.AttemptID,
		record.Sequence,
		record.Message.Role,
		string(encoded),
		record.Trust,
		record.Source,
		record.SourceRef,
		formatTime(record.CreatedAt),
	)
	if err != nil {
		return contextbuilder.Record{}, fmt.Errorf("inserting context message: %w", err)
	}
	if err := insertEvent(ctx, tx, record.TaskID, record.AttemptID, "context.message_saved", map[string]any{
		"message_id":  record.ID,
		"sequence":    record.Sequence,
		"role":        record.Message.Role,
		"trust_level": record.Trust,
		"source":      record.Source,
		"source_ref":  record.SourceRef,
	}, record.CreatedAt); err != nil {
		return contextbuilder.Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return contextbuilder.Record{}, fmt.Errorf("committing context append: %w", err)
	}
	return record, nil
}

// ListContextMessages returns an attempt's immutable history in sequence order.
func (s *Store) ListContextMessages(
	ctx context.Context,
	attemptID string,
) ([]contextbuilder.Record, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT schema_version, id, task_id, attempt_id, sequence, role,
			content_json, trust_level, source, source_ref, created_at
		 FROM context_messages WHERE attempt_id = ? ORDER BY sequence ASC`,
		attemptID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing context messages: %w", err)
	}
	defer rows.Close()
	records := make([]contextbuilder.Record, 0)
	for rows.Next() {
		record, err := scanContextRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating context messages: %w", err)
	}
	return records, nil
}

func scanContextRecord(row rowScanner) (contextbuilder.Record, error) {
	var record contextbuilder.Record
	var role model.Role
	var content string
	var createdAt string
	err := row.Scan(
		&record.SchemaVersion,
		&record.ID,
		&record.TaskID,
		&record.AttemptID,
		&record.Sequence,
		&role,
		&content,
		&record.Trust,
		&record.Source,
		&record.SourceRef,
		&createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return contextbuilder.Record{}, errors.New("sqlite: context message not found")
	}
	if err != nil {
		return contextbuilder.Record{}, fmt.Errorf("scanning context message: %w", err)
	}
	record.Message.Role = role
	if err := json.Unmarshal([]byte(content), &record.Message.Content); err != nil {
		return contextbuilder.Record{}, fmt.Errorf("decoding context message: %w", err)
	}
	record.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return contextbuilder.Record{}, err
	}
	return record, nil
}
