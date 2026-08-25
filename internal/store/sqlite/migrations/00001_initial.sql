-- +goose Up
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS tasks (
    id TEXT PRIMARY KEY,
    schema_version TEXT NOT NULL,
    title TEXT NOT NULL,
    goal TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN (
        'created', 'planning', 'running', 'waiting_approval', 'waiting_input',
        'verifying', 'completed', 'partially_completed', 'failed', 'cancelled'
    )),
    result TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    active_attempt_id TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS attempts (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    status TEXT NOT NULL,
    started_at TEXT NOT NULL,
    finished_at TEXT
);

CREATE TABLE IF NOT EXISTS verifications (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE CASCADE,
    verifier TEXT NOT NULL,
    status TEXT NOT NULL,
    evidence_json TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS task_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    schema_version TEXT NOT NULL,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE CASCADE,
    type TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_tasks_created_at ON tasks(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_task_events_task_id_id ON task_events(task_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_attempts_one_active
ON attempts(task_id)
WHERE finished_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_attempts_one_active;
DROP INDEX IF EXISTS idx_task_events_task_id_id;
DROP INDEX IF EXISTS idx_tasks_created_at;
DROP TABLE IF EXISTS task_events;
DROP TABLE IF EXISTS verifications;
DROP TABLE IF EXISTS attempts;
DROP TABLE IF EXISTS tasks;
