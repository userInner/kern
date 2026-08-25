-- +goose Up
CREATE TABLE eval_suites (
    suite_id TEXT NOT NULL,
    version TEXT NOT NULL,
    schema_version TEXT NOT NULL,
    name TEXT NOT NULL,
    manifest_json TEXT NOT NULL CHECK (json_valid(manifest_json)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY(suite_id, version)
);

CREATE TABLE eval_runs (
    run_id TEXT PRIMARY KEY,
    schema_version TEXT NOT NULL,
    suite_id TEXT NOT NULL,
    suite_version TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'completed', 'failed', 'cancelled')),
    variants_json TEXT NOT NULL CHECK (json_valid(variants_json)),
    config_json TEXT NOT NULL CHECK (json_valid(config_json)),
    config_digest TEXT NOT NULL DEFAULT '',
    case_count INTEGER NOT NULL CHECK (case_count >= 0),
    completed_cases INTEGER NOT NULL DEFAULT 0 CHECK (completed_cases >= 0),
    metrics_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metrics_json)),
    report_json TEXT CHECK (report_json IS NULL OR json_valid(report_json)),
    error_message TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT,
    FOREIGN KEY(suite_id, suite_version) REFERENCES eval_suites(suite_id, version)
);

CREATE TABLE eval_cases (
    run_id TEXT NOT NULL REFERENCES eval_runs(run_id) ON DELETE CASCADE,
    case_id TEXT NOT NULL,
    variant_id TEXT NOT NULL,
    attempt INTEGER NOT NULL CHECK (attempt > 0),
    task_id TEXT NOT NULL DEFAULT '',
    result_json TEXT NOT NULL CHECK (json_valid(result_json)),
    score_json TEXT NOT NULL CHECK (json_valid(score_json)),
    PRIMARY KEY(run_id, case_id, variant_id, attempt)
);

CREATE INDEX idx_eval_runs_created ON eval_runs(created_at DESC);
CREATE INDEX idx_eval_cases_run_variant ON eval_cases(run_id, variant_id, case_id);

-- +goose Down
DROP INDEX IF EXISTS idx_eval_cases_run_variant;
DROP INDEX IF EXISTS idx_eval_runs_created;
DROP TABLE IF EXISTS eval_cases;
DROP TABLE IF EXISTS eval_runs;
DROP TABLE IF EXISTS eval_suites;
