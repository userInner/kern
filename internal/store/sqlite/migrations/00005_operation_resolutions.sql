-- +goose Up
CREATE TABLE operation_resolutions (
    id TEXT PRIMARY KEY,
    operation_id TEXT NOT NULL UNIQUE REFERENCES operations(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE CASCADE,
    resolution TEXT NOT NULL CHECK (resolution IN (
        'confirmed_succeeded', 'confirmed_not_executed'
    )),
    actor TEXT NOT NULL,
    decided_at TEXT NOT NULL
);

CREATE INDEX idx_operation_resolutions_task_decided
ON operation_resolutions(task_id, decided_at, id);

-- +goose Down
DROP INDEX IF EXISTS idx_operation_resolutions_task_decided;
DROP TABLE IF EXISTS operation_resolutions;
