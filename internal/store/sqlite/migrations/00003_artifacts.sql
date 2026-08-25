-- +goose Up
CREATE TABLE artifacts (
    id TEXT PRIMARY KEY,
    schema_version TEXT NOT NULL,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    digest TEXT NOT NULL CHECK (length(digest) = 64),
    media_type TEXT NOT NULL,
    size INTEGER NOT NULL CHECK (size >= 0),
    storage_path TEXT NOT NULL,
    source_operation_id TEXT REFERENCES operations(id) ON DELETE SET NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX idx_artifacts_task_created ON artifacts(task_id, created_at, id);
CREATE INDEX idx_artifacts_digest ON artifacts(digest);

-- +goose Down
DROP INDEX IF EXISTS idx_artifacts_digest;
DROP INDEX IF EXISTS idx_artifacts_task_created;
DROP TABLE IF EXISTS artifacts;
