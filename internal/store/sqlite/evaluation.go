package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/userInner/kern/internal/evaluation"
)

// UpsertEvalSuite records the exact immutable suite manifest used by a run.
func (s *Store) UpsertEvalSuite(ctx context.Context, suite evaluation.Suite) error {
	if err := evaluation.Validate(suite); err != nil {
		return err
	}
	manifest, err := json.Marshal(suite)
	if err != nil {
		return fmt.Errorf("encoding evaluation suite: %w", err)
	}
	now := formatTime(time.Now().UTC())
	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO eval_suites (
			suite_id, version, schema_version, name, manifest_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(suite_id, version) DO UPDATE SET
			name = excluded.name,
			manifest_json = excluded.manifest_json,
			updated_at = excluded.updated_at`,
		suite.ID,
		suite.Version,
		suite.SchemaVersion,
		suite.Name,
		string(manifest),
		now,
		now,
	)
	if err != nil {
		return fmt.Errorf("persisting evaluation suite: %w", err)
	}
	return nil
}

// CreateEvalRun persists a queued evaluation before any external work begins.
func (s *Store) CreateEvalRun(
	ctx context.Context,
	run evaluation.Run,
	config evaluation.RunConfig,
) error {
	if run.SchemaVersion != evaluation.SchemaVersion || run.ID == "" || run.SuiteID == "" ||
		run.SuiteVersion == "" || run.Status != evaluation.StatusQueued || run.CaseCount < 1 {
		return errors.New("sqlite: invalid evaluation run")
	}
	variants, err := json.Marshal(run.Variants)
	if err != nil {
		return fmt.Errorf("encoding evaluation variants: %w", err)
	}
	configJSON, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("encoding evaluation config: %w", err)
	}
	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO eval_runs (
			run_id, schema_version, suite_id, suite_version, status, variants_json,
			config_json, config_digest, case_count, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID,
		run.SchemaVersion,
		run.SuiteID,
		run.SuiteVersion,
		run.Status,
		string(variants),
		string(configJSON),
		run.ConfigDigest,
		run.CaseCount,
		formatTime(run.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("creating evaluation run: %w", err)
	}
	return nil
}

// StartEvalRun transitions a queued run to running.
func (s *Store) StartEvalRun(ctx context.Context, runID string, startedAt time.Time) error {
	result, err := s.db.ExecContext(
		ctx,
		"UPDATE eval_runs SET status = ?, started_at = COALESCE(started_at, ?), error_message = '' WHERE run_id = ? AND status = ?",
		evaluation.StatusRunning,
		formatTime(startedAt),
		runID,
		evaluation.StatusQueued,
	)
	if err != nil {
		return fmt.Errorf("starting evaluation run: %w", err)
	}
	return requireOneRow(result, evaluation.ErrRunNotFound)
}

// PauseEvalRun persists a cooperative pause after active case workers have
// stopped. Completed case rows remain available for an exact resume.
func (s *Store) PauseEvalRun(ctx context.Context, runID, message string) error {
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE eval_runs SET status = ?, error_message = ?, completed_at = NULL
		 WHERE run_id = ? AND status IN (?, ?)`,
		evaluation.StatusPaused,
		message,
		runID,
		evaluation.StatusQueued,
		evaluation.StatusRunning,
	)
	if err != nil {
		return fmt.Errorf("pausing evaluation run: %w", err)
	}
	return requireOneRow(result, evaluation.ErrRunNotFound)
}

// ResumeEvalRun atomically queues a paused run for its remaining jobs.
func (s *Store) ResumeEvalRun(ctx context.Context, runID string) error {
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE eval_runs SET status = ?, error_message = '', completed_at = NULL
		 WHERE run_id = ? AND status = ?`,
		evaluation.StatusQueued,
		runID,
		evaluation.StatusPaused,
	)
	if err != nil {
		return fmt.Errorf("resuming evaluation run: %w", err)
	}
	return requireOneRow(result, evaluation.ErrRunNotFound)
}

// RecordEvalProgress atomically advances one run and retains every completed
// attempt before the full report exists. Replaying the same progress update is
// idempotent.
func (s *Store) RecordEvalProgress(
	ctx context.Context,
	runID string,
	completedCases int,
	results []evaluation.CaseResult,
) error {
	if runID == "" || completedCases < 1 || len(results) == 0 {
		return errors.New("sqlite: invalid evaluation progress")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning evaluation progress: %w", err)
	}
	defer tx.Rollback()
	updated, err := tx.ExecContext(
		ctx,
		`UPDATE eval_runs SET completed_cases = ?
		 WHERE run_id = ? AND status = ? AND completed_cases <= ? AND case_count >= ?`,
		completedCases,
		runID,
		evaluation.StatusRunning,
		completedCases,
		completedCases,
	)
	if err != nil {
		return fmt.Errorf("advancing evaluation progress: %w", err)
	}
	if err := requireOneRow(updated, evaluation.ErrRunNotFound); err != nil {
		return err
	}
	for _, caseResult := range results {
		if caseResult.CaseID == "" || caseResult.VariantID == "" || caseResult.Attempt < 1 {
			return errors.New("sqlite: invalid evaluation case progress")
		}
		resultJSON, scoreJSON, err := encodeEvalCase(caseResult)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO eval_cases (
				run_id, case_id, variant_id, attempt, task_id, result_json, score_json
			) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(run_id, case_id, variant_id, attempt) DO UPDATE SET
				task_id = excluded.task_id,
				result_json = excluded.result_json,
				score_json = excluded.score_json`,
			runID,
			caseResult.CaseID,
			caseResult.VariantID,
			caseResult.Attempt,
			caseResult.TaskID,
			resultJSON,
			scoreJSON,
		); err != nil {
			return fmt.Errorf("persisting evaluation case progress: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing evaluation progress: %w", err)
	}
	return nil
}

// CompleteEvalRun atomically stores the final report and every case attempt.
func (s *Store) CompleteEvalRun(ctx context.Context, report evaluation.Report) error {
	if report.SchemaVersion != evaluation.SchemaVersion || report.RunID == "" ||
		report.Status != evaluation.StatusCompleted {
		return errors.New("sqlite: invalid evaluation report")
	}
	reportJSON, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encoding evaluation report: %w", err)
	}
	metricsJSON, err := json.Marshal(struct {
		Variants    []evaluation.VariantMetrics `json:"variants"`
		Comparisons []evaluation.Comparison     `json:"comparisons,omitempty"`
	}{report.Variants, report.Comparisons})
	if err != nil {
		return fmt.Errorf("encoding evaluation metrics: %w", err)
	}
	completedCases := 0
	for _, metrics := range report.Variants {
		completedCases += metrics.Cases
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning evaluation completion: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(
		ctx,
		`UPDATE eval_runs SET
			status = ?, config_digest = ?, completed_cases = ?, metrics_json = ?,
			report_json = ?, error_message = '', completed_at = ?
		 WHERE run_id = ? AND status IN (?, ?)`,
		evaluation.StatusCompleted,
		report.ConfigDigest,
		completedCases,
		string(metricsJSON),
		string(reportJSON),
		formatTime(report.CompletedAt),
		report.RunID,
		evaluation.StatusQueued,
		evaluation.StatusRunning,
	)
	if err != nil {
		return fmt.Errorf("completing evaluation run: %w", err)
	}
	if err := requireOneRow(result, evaluation.ErrRunNotFound); err != nil {
		return err
	}
	for _, caseResult := range report.Results {
		resultJSON, scoreJSON, err := encodeEvalCase(caseResult)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO eval_cases (
				run_id, case_id, variant_id, attempt, task_id, result_json, score_json
			) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(run_id, case_id, variant_id, attempt) DO UPDATE SET
				task_id = excluded.task_id,
				result_json = excluded.result_json,
				score_json = excluded.score_json`,
			report.RunID,
			caseResult.CaseID,
			caseResult.VariantID,
			caseResult.Attempt,
			caseResult.TaskID,
			string(resultJSON),
			string(scoreJSON),
		); err != nil {
			return fmt.Errorf("persisting evaluation case: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing evaluation report: %w", err)
	}
	return nil
}

func encodeEvalCase(caseResult evaluation.CaseResult) (string, string, error) {
	resultJSON, err := json.Marshal(caseResult)
	if err != nil {
		return "", "", fmt.Errorf("encoding evaluation case result: %w", err)
	}
	scoreJSON, err := json.Marshal(struct {
		Passed bool                     `json:"passed"`
		Score  float64                  `json:"score"`
		Grades []evaluation.GradeResult `json:"grades"`
	}{caseResult.Passed, caseResult.Score, caseResult.Grades})
	if err != nil {
		return "", "", fmt.Errorf("encoding evaluation score: %w", err)
	}
	return string(resultJSON), string(scoreJSON), nil
}

// FinishEvalRun stores a failed or cancelled terminal state.
func (s *Store) FinishEvalRun(
	ctx context.Context,
	runID string,
	status evaluation.Status,
	message string,
	completedAt time.Time,
) error {
	if status != evaluation.StatusFailed && status != evaluation.StatusCancelled {
		return errors.New("sqlite: invalid terminal evaluation status")
	}
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE eval_runs SET status = ?, error_message = ?, completed_at = ?
		 WHERE run_id = ? AND status IN (?, ?, ?)`,
		status,
		message,
		formatTime(completedAt),
		runID,
		evaluation.StatusQueued,
		evaluation.StatusRunning,
		evaluation.StatusPaused,
	)
	if err != nil {
		return fmt.Errorf("finishing evaluation run: %w", err)
	}
	return requireOneRow(result, evaluation.ErrRunNotFound)
}

// GetEvalRunConfig returns the immutable caller configuration used to start a
// run. It deliberately excludes secret or runtime-only state.
func (s *Store) GetEvalRunConfig(ctx context.Context, runID string) (evaluation.RunConfig, error) {
	var encoded string
	err := s.db.QueryRowContext(ctx, "SELECT config_json FROM eval_runs WHERE run_id = ?", runID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return evaluation.RunConfig{}, evaluation.ErrRunNotFound
	}
	if err != nil {
		return evaluation.RunConfig{}, fmt.Errorf("reading evaluation config: %w", err)
	}
	var config evaluation.RunConfig
	if err := json.Unmarshal([]byte(encoded), &config); err != nil {
		return evaluation.RunConfig{}, fmt.Errorf("decoding evaluation config: %w", err)
	}
	return config, nil
}

// ListEvalCaseResults returns every atomically persisted attempt in stable
// Case/Variant/Attempt order for resume and audit.
func (s *Store) ListEvalCaseResults(ctx context.Context, runID string) ([]evaluation.CaseResult, error) {
	if _, err := s.GetEvalRun(ctx, runID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT result_json FROM eval_cases WHERE run_id = ?
		 ORDER BY variant_id, case_id, attempt`,
		runID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing evaluation case results: %w", err)
	}
	defer rows.Close()
	results := make([]evaluation.CaseResult, 0)
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			return nil, fmt.Errorf("scanning evaluation case result: %w", err)
		}
		var result evaluation.CaseResult
		if err := json.Unmarshal([]byte(encoded), &result); err != nil {
			return nil, fmt.Errorf("decoding evaluation case result: %w", err)
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating evaluation case results: %w", err)
	}
	return results, nil
}

// GetEvalRun returns one durable evaluation snapshot.
func (s *Store) GetEvalRun(ctx context.Context, runID string) (evaluation.Run, error) {
	return scanEvalRun(s.db.QueryRowContext(ctx, evalRunSelect+" WHERE r.run_id = ?", runID))
}

// ListEvalRuns returns recent runs newest first.
func (s *Store) ListEvalRuns(ctx context.Context, limit int) ([]evaluation.Run, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, evalRunSelect+" ORDER BY r.created_at DESC LIMIT ?", limit)
	if err != nil {
		return nil, fmt.Errorf("listing evaluation runs: %w", err)
	}
	defer rows.Close()
	items := make([]evaluation.Run, 0, limit)
	for rows.Next() {
		item, err := scanEvalRun(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating evaluation runs: %w", err)
	}
	return items, nil
}

// GetEvalReport returns the immutable completed report.
func (s *Store) GetEvalReport(ctx context.Context, runID string) (evaluation.Report, error) {
	var encoded sql.NullString
	err := s.db.QueryRowContext(ctx, "SELECT report_json FROM eval_runs WHERE run_id = ?", runID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return evaluation.Report{}, evaluation.ErrRunNotFound
	}
	if err != nil {
		return evaluation.Report{}, fmt.Errorf("reading evaluation report: %w", err)
	}
	if !encoded.Valid {
		return evaluation.Report{}, evaluation.ErrReportNotReady
	}
	var report evaluation.Report
	if err := json.Unmarshal([]byte(encoded.String), &report); err != nil {
		return evaluation.Report{}, fmt.Errorf("decoding evaluation report: %w", err)
	}
	return report, nil
}

const evalRunSelect = `SELECT
	r.schema_version, r.run_id, r.suite_id, s.name, r.suite_version, r.status,
	r.variants_json, r.case_count, r.completed_cases, r.config_digest,
	r.error_message, r.created_at, r.started_at, r.completed_at
	FROM eval_runs r
	JOIN eval_suites s ON s.suite_id = r.suite_id AND s.version = r.suite_version`

func scanEvalRun(row rowScanner) (evaluation.Run, error) {
	var item evaluation.Run
	var variants, createdAt string
	var startedAt, completedAt sql.NullString
	err := row.Scan(
		&item.SchemaVersion,
		&item.ID,
		&item.SuiteID,
		&item.SuiteName,
		&item.SuiteVersion,
		&item.Status,
		&variants,
		&item.CaseCount,
		&item.CompletedCases,
		&item.ConfigDigest,
		&item.ErrorMessage,
		&createdAt,
		&startedAt,
		&completedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return evaluation.Run{}, evaluation.ErrRunNotFound
	}
	if err != nil {
		return evaluation.Run{}, fmt.Errorf("scanning evaluation run: %w", err)
	}
	if err := json.Unmarshal([]byte(variants), &item.Variants); err != nil {
		return evaluation.Run{}, fmt.Errorf("decoding evaluation variants: %w", err)
	}
	item.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return evaluation.Run{}, err
	}
	if startedAt.Valid {
		value, err := parseTime(startedAt.String)
		if err != nil {
			return evaluation.Run{}, err
		}
		item.StartedAt = &value
	}
	if completedAt.Valid {
		value, err := parseTime(completedAt.String)
		if err != nil {
			return evaluation.Run{}, err
		}
		item.CompletedAt = &value
	}
	return item, nil
}
