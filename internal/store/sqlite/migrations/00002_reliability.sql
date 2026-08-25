-- +goose Up
ALTER TABLE tasks ADD COLUMN lease_owner TEXT;
ALTER TABLE tasks ADD COLUMN lease_expires_at TEXT;
ALTER TABLE tasks ADD COLUMN heartbeat_at TEXT;
ALTER TABLE tasks ADD COLUMN paused_from_status TEXT;

CREATE TABLE checkpoints (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL,
    reason TEXT NOT NULL,
    state_json TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(attempt_id, ordinal)
);

CREATE TABLE operations (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE CASCADE,
    tool TEXT NOT NULL,
    input_json TEXT NOT NULL,
    input_hash TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    effect TEXT NOT NULL CHECK (effect IN (
        'read', 'local_write', 'process', 'network_read', 'network_write', 'destructive'
    )),
    status TEXT NOT NULL CHECK (status IN (
        'proposed', 'awaiting_approval', 'prepared', 'executing',
        'succeeded', 'failed', 'unknown', 'cancelled'
    )),
    output_summary TEXT NOT NULL DEFAULT '',
    error_code TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(task_id, idempotency_key)
);

CREATE TABLE operation_results (
    operation_id TEXT PRIMARY KEY REFERENCES operations(id) ON DELETE CASCADE,
    output_ref TEXT NOT NULL DEFAULT '',
    exit_code INTEGER,
    error_code TEXT NOT NULL DEFAULT '',
    timing_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL
);

CREATE TABLE approval_requests (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE CASCADE,
    operation_id TEXT NOT NULL REFERENCES operations(id) ON DELETE CASCADE,
    scope_json TEXT NOT NULL,
    risk TEXT NOT NULL CHECK (risk IN ('low', 'medium', 'high', 'critical')),
    explanation TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'denied', 'expired', 'cancelled')),
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE approval_receipts (
    id TEXT PRIMARY KEY,
    request_id TEXT NOT NULL UNIQUE REFERENCES approval_requests(id) ON DELETE CASCADE,
    decision TEXT NOT NULL CHECK (decision IN ('approved', 'denied')),
    actor TEXT NOT NULL,
    scope_json TEXT NOT NULL,
    decided_at TEXT NOT NULL
);

CREATE INDEX idx_checkpoints_attempt_ordinal ON checkpoints(attempt_id, ordinal DESC);
CREATE INDEX idx_operations_attempt_status ON operations(attempt_id, status);
CREATE INDEX idx_approval_requests_task_status ON approval_requests(task_id, status);
CREATE INDEX idx_tasks_lease_expiry ON tasks(lease_expires_at);

-- +goose Down
DROP INDEX IF EXISTS idx_tasks_lease_expiry;
DROP INDEX IF EXISTS idx_approval_requests_task_status;
DROP INDEX IF EXISTS idx_operations_attempt_status;
DROP INDEX IF EXISTS idx_checkpoints_attempt_ordinal;
DROP TABLE IF EXISTS approval_receipts;
DROP TABLE IF EXISTS approval_requests;
DROP TABLE IF EXISTS operation_results;
DROP TABLE IF EXISTS operations;
DROP TABLE IF EXISTS checkpoints;
