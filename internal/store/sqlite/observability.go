package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/userInner/kern/internal/observability"
)

const metricsCacheTTL = 2 * time.Second

type metricsCache struct {
	mu       sync.Mutex
	snapshot observability.Snapshot
	updated  time.Time
}

// ObservabilitySnapshot derives restart-safe metrics from durable Core facts.
// It deliberately emits no task, attempt, plugin, model, path, or URL labels.
func (s *Store) ObservabilitySnapshot(ctx context.Context) (observability.Snapshot, error) {
	s.metrics.mu.Lock()
	defer s.metrics.mu.Unlock()
	if !s.metrics.updated.IsZero() && time.Since(s.metrics.updated) < metricsCacheTTL {
		return s.metrics.snapshot, nil
	}
	snapshot := observability.NewSnapshot()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return snapshot, fmt.Errorf("beginning observability snapshot: %w", err)
	}
	defer tx.Rollback()

	if err := collectTaskMetrics(ctx, tx, &snapshot); err != nil {
		return snapshot, err
	}
	if err := collectModelMetrics(ctx, tx, &snapshot); err != nil {
		return snapshot, err
	}
	if err := collectOperationMetrics(ctx, tx, &snapshot); err != nil {
		return snapshot, err
	}
	if err := collectApprovalMetrics(ctx, tx, &snapshot); err != nil {
		return snapshot, err
	}
	if err := collectPluginAndRecoveryMetrics(ctx, tx, &snapshot); err != nil {
		return snapshot, err
	}
	if err := collectVerificationMetrics(ctx, tx, &snapshot); err != nil {
		return snapshot, err
	}
	if err := collectEvalMetrics(ctx, tx, &snapshot); err != nil {
		return snapshot, err
	}
	if err := tx.Commit(); err != nil {
		return snapshot, fmt.Errorf("committing observability snapshot: %w", err)
	}

	dbStats := s.db.Stats()
	snapshot.Goroutines = runtime.NumGoroutine()
	snapshot.Database = observability.Database{
		MaxOpenConnections: dbStats.MaxOpenConnections,
		OpenConnections:    dbStats.OpenConnections,
		InUse:              dbStats.InUse,
		Idle:               dbStats.Idle,
		WaitCount:          dbStats.WaitCount,
		WaitDuration:       dbStats.WaitDuration,
	}
	s.metrics.snapshot = snapshot
	s.metrics.updated = time.Now()
	return snapshot, nil
}

func collectTaskMetrics(ctx context.Context, tx *sql.Tx, snapshot *observability.Snapshot) error {
	rows, err := tx.QueryContext(ctx, "SELECT status, COUNT(*) FROM tasks GROUP BY status")
	if err != nil {
		return fmt.Errorf("collecting task states: %w", err)
	}
	for rows.Next() {
		var status string
		var count uint64
		if err := rows.Scan(&status, &count); err != nil {
			rows.Close()
			return fmt.Errorf("scanning task states: %w", err)
		}
		status = boundedTaskStatus(status)
		snapshot.TaskCurrent[status] += count
		snapshot.TasksCreated += count
		switch status {
		case "created":
			snapshot.QueuedTasks += int(count)
		case "planning", "running", "waiting_approval", "waiting_input", "verifying":
			snapshot.ActiveTasks += int(count)
		}
	}
	if err := closeRows(rows, "task states"); err != nil {
		return err
	}

	rows, err = tx.QueryContext(ctx, "SELECT status, started_at, finished_at FROM attempts")
	if err != nil {
		return fmt.Errorf("collecting attempts: %w", err)
	}
	for rows.Next() {
		var status, startedAt string
		var finishedAt sql.NullString
		if err := rows.Scan(&status, &startedAt, &finishedAt); err != nil {
			rows.Close()
			return fmt.Errorf("scanning attempts: %w", err)
		}
		snapshot.Attempts[boundedTaskStatus(status)]++
		if !finishedAt.Valid {
			continue
		}
		started, err := parseTime(startedAt)
		if err != nil {
			rows.Close()
			return err
		}
		finished, err := parseTime(finishedAt.String)
		if err != nil {
			rows.Close()
			return err
		}
		snapshot.TaskDuration.Observe(finished.Sub(started).Seconds())
	}
	return closeRows(rows, "attempts")
}

func collectModelMetrics(ctx context.Context, tx *sql.Tx, snapshot *observability.Snapshot) error {
	rows, err := tx.QueryContext(
		ctx,
		`SELECT type, payload_json FROM task_events
		 WHERE type IN ('model.call_started', 'model.call_completed', 'model.usage', 'model.retry')`,
	)
	if err != nil {
		return fmt.Errorf("collecting model metrics: %w", err)
	}
	for rows.Next() {
		var eventType, payload string
		if err := rows.Scan(&eventType, &payload); err != nil {
			rows.Close()
			return fmt.Errorf("scanning model metrics: %w", err)
		}
		switch eventType {
		case "model.call_started":
			snapshot.ModelCallsStarted++
		case "model.call_completed":
			var call struct {
				Status     string `json:"status"`
				DurationMS int64  `json:"duration_ms"`
			}
			if json.Unmarshal([]byte(payload), &call) != nil {
				continue
			}
			snapshot.ModelCalls[boundedOutcome(call.Status)]++
			if call.DurationMS >= 0 {
				snapshot.ModelCallDuration.Observe(float64(call.DurationMS) / 1000)
			}
		case "model.usage":
			var usage struct {
				InputTokens     int64 `json:"input_tokens"`
				OutputTokens    int64 `json:"output_tokens"`
				ReasoningTokens int64 `json:"reasoning_tokens"`
				CachedTokens    int64 `json:"cached_tokens"`
				CostMicros      int64 `json:"cost_micros"`
			}
			if json.Unmarshal([]byte(payload), &usage) != nil {
				continue
			}
			addNonNegative(snapshot.ModelTokens, "input", usage.InputTokens)
			addNonNegative(snapshot.ModelTokens, "output", usage.OutputTokens)
			addNonNegative(snapshot.ModelTokens, "reasoning", usage.ReasoningTokens)
			addNonNegative(snapshot.ModelTokens, "cached", usage.CachedTokens)
			if usage.CostMicros > 0 {
				snapshot.ModelCostMicros += uint64(usage.CostMicros)
			}
		case "model.retry":
			snapshot.ModelRetries++
		}
	}
	return closeRows(rows, "model metrics")
}

func collectOperationMetrics(ctx context.Context, tx *sql.Tx, snapshot *observability.Snapshot) error {
	rows, err := tx.QueryContext(
		ctx,
		`SELECT o.tool, o.effect, o.status, r.timing_json
		 FROM operations o LEFT JOIN operation_results r ON r.operation_id = o.id`,
	)
	if err != nil {
		return fmt.Errorf("collecting operation metrics: %w", err)
	}
	for rows.Next() {
		var tool, effect, status string
		var timing sql.NullString
		if err := rows.Scan(&tool, &effect, &status, &timing); err != nil {
			rows.Close()
			return fmt.Errorf("scanning operation metrics: %w", err)
		}
		status = boundedOperationStatus(status)
		key := boundedTool(tool) + "\x00" + boundedEffect(effect) + "\x00" + status
		snapshot.Operations[key]++
		if status == "unknown" {
			snapshot.UnknownOperations++
		}
		if timing.Valid {
			var value struct {
				DurationMS int64 `json:"duration_ms"`
			}
			if json.Unmarshal([]byte(timing.String), &value) == nil && value.DurationMS >= 0 {
				snapshot.OperationDuration.Observe(float64(value.DurationMS) / 1000)
			}
		}
	}
	return closeRows(rows, "operation metrics")
}

func collectApprovalMetrics(ctx context.Context, tx *sql.Tx, snapshot *observability.Snapshot) error {
	rows, err := tx.QueryContext(
		ctx,
		`SELECT request.risk, request.status, request.created_at, receipt.decided_at
		 FROM approval_requests request
		 LEFT JOIN approval_receipts receipt ON receipt.request_id = request.id`,
	)
	if err != nil {
		return fmt.Errorf("collecting approval metrics: %w", err)
	}
	for rows.Next() {
		var risk, status, createdAt string
		var decidedAt sql.NullString
		if err := rows.Scan(&risk, &status, &createdAt, &decidedAt); err != nil {
			rows.Close()
			return fmt.Errorf("scanning approval metrics: %w", err)
		}
		key := boundedRisk(risk) + "\x00" + boundedApprovalStatus(status)
		snapshot.Approvals[key]++
		if decidedAt.Valid {
			created, err := parseTime(createdAt)
			if err != nil {
				rows.Close()
				return err
			}
			decided, err := parseTime(decidedAt.String)
			if err != nil {
				rows.Close()
				return err
			}
			snapshot.ApprovalWait.Observe(decided.Sub(created).Seconds())
		}
	}
	return closeRows(rows, "approval metrics")
}

func collectPluginAndRecoveryMetrics(ctx context.Context, tx *sql.Tx, snapshot *observability.Snapshot) error {
	var activations uint64
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM attempt_plugins").Scan(&activations); err != nil {
		return fmt.Errorf("collecting plugin activations: %w", err)
	}
	snapshot.PluginEvents["activated"] = activations

	rows, err := tx.QueryContext(
		ctx,
		`SELECT type, payload_json FROM task_events
		 WHERE type IN ('plugin.activation_failed', 'plugin.activation_skipped', 'operation.resolved')`,
	)
	if err != nil {
		return fmt.Errorf("collecting plugin and recovery metrics: %w", err)
	}
	for rows.Next() {
		var eventType, payload string
		if err := rows.Scan(&eventType, &payload); err != nil {
			rows.Close()
			return fmt.Errorf("scanning plugin and recovery metrics: %w", err)
		}
		switch eventType {
		case "plugin.activation_failed":
			snapshot.PluginEvents["failed"]++
		case "plugin.activation_skipped":
			snapshot.PluginEvents["skipped"]++
		case "operation.resolved":
			var resolution struct {
				Resolution string `json:"resolution"`
			}
			if json.Unmarshal([]byte(payload), &resolution) == nil {
				snapshot.RecoveryResolutions[boundedResolution(resolution.Resolution)]++
			}
		}
	}
	return closeRows(rows, "plugin and recovery metrics")
}

func collectVerificationMetrics(ctx context.Context, tx *sql.Tx, snapshot *observability.Snapshot) error {
	rows, err := tx.QueryContext(ctx, "SELECT status, COUNT(*) FROM verifications GROUP BY status")
	if err != nil {
		return fmt.Errorf("collecting verification metrics: %w", err)
	}
	for rows.Next() {
		var status string
		var count uint64
		if err := rows.Scan(&status, &count); err != nil {
			rows.Close()
			return fmt.Errorf("scanning verification metrics: %w", err)
		}
		snapshot.Verifications[boundedVerificationStatus(status)] += count
	}
	return closeRows(rows, "verification metrics")
}

func collectEvalMetrics(ctx context.Context, tx *sql.Tx, snapshot *observability.Snapshot) error {
	rows, err := tx.QueryContext(ctx, "SELECT status, COUNT(*) FROM eval_runs GROUP BY status")
	if err != nil {
		return fmt.Errorf("collecting evaluation run metrics: %w", err)
	}
	for rows.Next() {
		var status string
		var count uint64
		if err := rows.Scan(&status, &count); err != nil {
			rows.Close()
			return fmt.Errorf("scanning evaluation run metrics: %w", err)
		}
		snapshot.EvalRuns[boundedEvalStatus(status)] += count
	}
	if err := closeRows(rows, "evaluation runs"); err != nil {
		return err
	}

	rows, err = tx.QueryContext(ctx, "SELECT result_json FROM eval_cases WHERE attempt = 1")
	if err != nil {
		return fmt.Errorf("collecting evaluation case metrics: %w", err)
	}
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			rows.Close()
			return fmt.Errorf("scanning evaluation case metrics: %w", err)
		}
		var result struct {
			Passed bool `json:"passed"`
		}
		if json.Unmarshal([]byte(encoded), &result) != nil {
			continue
		}
		outcome := "failed"
		if result.Passed {
			outcome = "passed"
		}
		snapshot.EvalCases[outcome]++
	}
	return closeRows(rows, "evaluation cases")
}

func closeRows(rows *sql.Rows, subject string) error {
	err := rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return fmt.Errorf("iterating %s: %w", subject, err)
	}
	if closeErr != nil {
		return fmt.Errorf("closing %s: %w", subject, closeErr)
	}
	return nil
}

func addNonNegative(values map[string]uint64, key string, value int64) {
	if value > 0 {
		values[key] += uint64(value)
	}
}

func boundedTaskStatus(value string) string {
	switch value {
	case "created", "planning", "running", "waiting_approval", "waiting_input",
		"verifying", "completed", "partially_completed", "failed", "cancelled":
		return value
	default:
		return "unknown"
	}
}

func boundedOperationStatus(value string) string {
	switch value {
	case "proposed", "awaiting_approval", "prepared", "executing", "succeeded", "failed", "unknown", "cancelled":
		return value
	default:
		return "unknown"
	}
}

func boundedTool(value string) string {
	switch value {
	case "inspect", "change", "execute", "network", "capability":
		return value
	default:
		return "other"
	}
}

func boundedEffect(value string) string {
	switch value {
	case "read", "local_write", "process", "network_read", "network_write", "destructive":
		return value
	default:
		return "unknown"
	}
}

func boundedOutcome(value string) string {
	switch value {
	case "succeeded", "failed":
		return value
	default:
		return "unknown"
	}
}

func boundedRisk(value string) string {
	switch value {
	case "low", "medium", "high", "critical":
		return value
	default:
		return "unknown"
	}
}

func boundedApprovalStatus(value string) string {
	switch value {
	case "pending", "approved", "denied", "expired", "cancelled":
		return value
	default:
		return "unknown"
	}
}

func boundedResolution(value string) string {
	switch value {
	case "confirmed_succeeded", "confirmed_not_executed":
		return value
	default:
		return "unknown"
	}
}

func boundedVerificationStatus(value string) string {
	switch value {
	case "passed", "failed", "partial", "manual_required", "not_run":
		return value
	default:
		return "unknown"
	}
}

func boundedEvalStatus(value string) string {
	switch value {
	case "queued", "running", "paused", "completed", "failed", "cancelled":
		return value
	default:
		return "unknown"
	}
}
