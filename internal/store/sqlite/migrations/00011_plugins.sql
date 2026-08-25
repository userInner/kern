-- +goose Up
CREATE TABLE plugins (
    plugin_id TEXT PRIMARY KEY,
    schema_version TEXT NOT NULL,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    version TEXT NOT NULL,
    source TEXT NOT NULL,
    install_path TEXT NOT NULL,
    manifest_json TEXT NOT NULL CHECK (json_valid(manifest_json)),
    digest TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    trust_status TEXT NOT NULL CHECK (trust_status IN ('local-unverified', 'verified', 'revoked')),
    installed_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(plugin_id, version, digest)
);

CREATE INDEX idx_plugins_enabled_id ON plugins(enabled, plugin_id);

-- +goose Down
DROP INDEX IF EXISTS idx_plugins_enabled_id;
DROP TABLE IF EXISTS plugins;
