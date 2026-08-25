-- +goose Up
ALTER TABLE operations ADD COLUMN recovery_json TEXT NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE operations DROP COLUMN recovery_json;
