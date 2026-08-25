-- +goose Up
CREATE TABLE context_messages (
    id TEXT PRIMARY KEY,
    schema_version TEXT NOT NULL,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('system', 'user', 'assistant', 'tool')),
    content_json TEXT NOT NULL,
    trust_level TEXT NOT NULL CHECK (trust_level IN (
        'trusted_system', 'user_asserted', 'model_generated',
        'tool_untrusted', 'plugin_untrusted'
    )),
    source TEXT NOT NULL CHECK (source IN (
        'core', 'original_goal', 'model', 'tool', 'summary', 'plugin'
    )),
    source_ref TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    UNIQUE(attempt_id, sequence)
);

CREATE INDEX idx_context_messages_attempt_sequence
ON context_messages(attempt_id, sequence);

-- +goose Down
DROP INDEX IF EXISTS idx_context_messages_attempt_sequence;
DROP TABLE IF EXISTS context_messages;
