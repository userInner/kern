-- +goose Up
CREATE TABLE model_configs (
    id TEXT PRIMARY KEY,
    schema_version TEXT NOT NULL,
    name TEXT NOT NULL COLLATE NOCASE UNIQUE,
    provider TEXT NOT NULL CHECK (provider IN ('openai-compatible', 'ollama')),
    base_url TEXT NOT NULL,
    model TEXT NOT NULL,
    secret_ref TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    is_default INTEGER NOT NULL CHECK (is_default IN (0, 1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE UNIQUE INDEX idx_model_configs_one_default
ON model_configs(is_default)
WHERE is_default = 1;

CREATE INDEX idx_model_configs_enabled_name
ON model_configs(enabled, name);

-- +goose Down
DROP INDEX IF EXISTS idx_model_configs_enabled_name;
DROP INDEX IF EXISTS idx_model_configs_one_default;
DROP TABLE IF EXISTS model_configs;
