-- +goose Up
CREATE TABLE task_plugin_preferences (
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    plugin_id TEXT NOT NULL REFERENCES plugins(plugin_id) ON DELETE CASCADE,
    mode TEXT NOT NULL CHECK (mode IN ('enable', 'disable')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY(task_id, plugin_id)
);

CREATE TABLE attempt_plugins (
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE CASCADE,
    plugin_id TEXT NOT NULL,
    schema_version TEXT NOT NULL,
    version TEXT NOT NULL,
    digest TEXT NOT NULL,
    reason TEXT NOT NULL,
    resources_json TEXT NOT NULL CHECK (json_valid(resources_json)),
    created_at TEXT NOT NULL,
    PRIMARY KEY(attempt_id, plugin_id)
);

CREATE INDEX idx_attempt_plugins_task_attempt ON attempt_plugins(task_id, attempt_id);

-- +goose Down
DROP INDEX IF EXISTS idx_attempt_plugins_task_attempt;
DROP TABLE IF EXISTS attempt_plugins;
DROP TABLE IF EXISTS task_plugin_preferences;
