-- +goose Up
CREATE TABLE plans (
    id TEXT PRIMARY KEY,
    schema_version TEXT NOT NULL,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE CASCADE,
    revision INTEGER NOT NULL CHECK (revision > 0),
    status TEXT NOT NULL CHECK (status IN ('active', 'completed', 'failed', 'superseded')),
    rationale TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(attempt_id, revision)
);

CREATE TABLE plan_steps (
    id TEXT PRIMARY KEY,
    plan_id TEXT NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal > 0),
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    phase TEXT NOT NULL CHECK (phase IN ('prepare', 'execute', 'verify')),
    required INTEGER NOT NULL CHECK (required IN (0, 1)),
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'completed', 'failed', 'skipped')),
    failure TEXT NOT NULL DEFAULT '',
    retry_count INTEGER NOT NULL DEFAULT 0 CHECK (retry_count >= 0),
    started_at TEXT,
    completed_at TEXT,
    updated_at TEXT NOT NULL,
    UNIQUE(plan_id, ordinal)
);

CREATE INDEX idx_plans_task_revision ON plans(task_id, revision DESC);
CREATE INDEX idx_plan_steps_plan_ordinal ON plan_steps(plan_id, ordinal);

-- +goose Down
DROP INDEX IF EXISTS idx_plan_steps_plan_ordinal;
DROP INDEX IF EXISTS idx_plans_task_revision;
DROP TABLE IF EXISTS plan_steps;
DROP TABLE IF EXISTS plans;
