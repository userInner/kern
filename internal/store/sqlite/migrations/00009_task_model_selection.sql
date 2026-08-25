-- +goose Up
ALTER TABLE tasks
ADD COLUMN model_config_id TEXT REFERENCES model_configs(id) ON DELETE SET NULL;

CREATE INDEX idx_tasks_model_config
ON tasks(model_config_id);

CREATE TABLE attempt_model_configs (
    attempt_id TEXT PRIMARY KEY REFERENCES attempts(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    config_id TEXT REFERENCES model_configs(id) ON DELETE SET NULL,
    schema_version TEXT NOT NULL,
    provider TEXT NOT NULL CHECK (provider IN ('openai-compatible', 'ollama')),
    base_url TEXT NOT NULL,
    model TEXT NOT NULL,
    parameters_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL
);

CREATE INDEX idx_attempt_model_configs_task
ON attempt_model_configs(task_id, created_at);

-- +goose Down
DROP INDEX IF EXISTS idx_attempt_model_configs_task;
DROP TABLE IF EXISTS attempt_model_configs;
DROP INDEX IF EXISTS idx_tasks_model_config;
ALTER TABLE tasks DROP COLUMN model_config_id;
