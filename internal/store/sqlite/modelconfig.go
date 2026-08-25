package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/userInner/kern/internal/id"
	"github.com/userInner/kern/internal/modelconfig"
)

// CreateModelConfig stores a validated model configuration. The first config
// becomes the default even when SetDefault is false.
func (s *Store) CreateModelConfig(
	ctx context.Context,
	draft modelconfig.Draft,
) (modelconfig.Config, error) {
	normalized, err := modelconfig.Normalize(draft)
	if err != nil {
		return modelconfig.Config{}, err
	}
	configID, err := id.New()
	if err != nil {
		return modelconfig.Config{}, err
	}
	now := time.Now().UTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return modelconfig.Config{}, fmt.Errorf("beginning model config creation: %w", err)
	}
	defer tx.Rollback()

	if conflict, err := modelConfigNameExists(ctx, tx, normalized.Name, ""); err != nil {
		return modelconfig.Config{}, err
	} else if conflict {
		return modelconfig.Config{}, modelconfig.ErrConflict
	}
	hasDefault, err := modelConfigDefaultExists(ctx, tx, "")
	if err != nil {
		return modelconfig.Config{}, err
	}
	isDefault := normalized.Enabled && (normalized.SetDefault || !hasDefault)
	if isDefault {
		if _, err := tx.ExecContext(ctx, "UPDATE model_configs SET is_default = 0"); err != nil {
			return modelconfig.Config{}, fmt.Errorf("clearing default model config: %w", err)
		}
	}
	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO model_configs (
            id, schema_version, name, provider, base_url, model, secret_ref,
            enabled, is_default, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		configID,
		modelconfig.SchemaVersion,
		normalized.Name,
		normalized.Provider,
		normalized.BaseURL,
		normalized.Model,
		normalized.SecretRef,
		normalized.Enabled,
		isDefault,
		formatTime(now),
		formatTime(now),
	)
	if err != nil {
		return modelconfig.Config{}, fmt.Errorf("inserting model config: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return modelconfig.Config{}, fmt.Errorf("committing model config creation: %w", err)
	}
	return modelconfig.Config{
		SchemaVersion: modelconfig.SchemaVersion,
		ID:            configID,
		Name:          normalized.Name,
		Provider:      normalized.Provider,
		BaseURL:       normalized.BaseURL,
		Model:         normalized.Model,
		SecretRef:     normalized.SecretRef,
		HasAPIKey:     normalized.SecretRef != "",
		Enabled:       normalized.Enabled,
		IsDefault:     isDefault,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

// GetModelConfig returns one model configuration including its internal Secret Store reference.
func (s *Store) GetModelConfig(ctx context.Context, configID string) (modelconfig.Config, error) {
	return scanModelConfig(s.db.QueryRowContext(ctx, modelConfigSelect+" WHERE id = ?", configID))
}

// DefaultModelConfig returns the enabled default model configuration.
func (s *Store) DefaultModelConfig(ctx context.Context) (modelconfig.Config, error) {
	return scanModelConfig(s.db.QueryRowContext(
		ctx,
		modelConfigSelect+" WHERE enabled = 1 AND is_default = 1",
	))
}

// ListModelConfigs returns all model configurations with the default first.
func (s *Store) ListModelConfigs(ctx context.Context) ([]modelconfig.Config, error) {
	rows, err := s.db.QueryContext(ctx, modelConfigSelect+" ORDER BY is_default DESC, name, id")
	if err != nil {
		return nil, fmt.Errorf("listing model configs: %w", err)
	}
	defer rows.Close()

	configs := make([]modelconfig.Config, 0)
	for rows.Next() {
		config, err := scanModelConfig(rows)
		if err != nil {
			return nil, err
		}
		configs = append(configs, config)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating model configs: %w", err)
	}
	return configs, nil
}

// UpdateModelConfig replaces mutable settings without ever returning a secret value.
func (s *Store) UpdateModelConfig(
	ctx context.Context,
	configID string,
	draft modelconfig.Draft,
) (modelconfig.Config, error) {
	normalized, err := modelconfig.Normalize(draft)
	if err != nil {
		return modelconfig.Config{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return modelconfig.Config{}, fmt.Errorf("beginning model config update: %w", err)
	}
	defer tx.Rollback()

	current, err := scanModelConfig(tx.QueryRowContext(ctx, modelConfigSelect+" WHERE id = ?", configID))
	if err != nil {
		return modelconfig.Config{}, err
	}
	if conflict, err := modelConfigNameExists(ctx, tx, normalized.Name, configID); err != nil {
		return modelconfig.Config{}, err
	} else if conflict {
		return modelconfig.Config{}, modelconfig.ErrConflict
	}
	hasOtherDefault, err := modelConfigDefaultExists(ctx, tx, configID)
	if err != nil {
		return modelconfig.Config{}, err
	}
	makeDefault := normalized.Enabled &&
		(normalized.SetDefault || current.IsDefault || !hasOtherDefault)
	if makeDefault {
		if _, err := tx.ExecContext(ctx, "UPDATE model_configs SET is_default = 0"); err != nil {
			return modelconfig.Config{}, fmt.Errorf("clearing default model config: %w", err)
		}
	}
	now := time.Now().UTC()
	result, err := tx.ExecContext(
		ctx,
		`UPDATE model_configs SET
            name = ?, provider = ?, base_url = ?, model = ?, secret_ref = ?,
            enabled = ?, is_default = ?,
            updated_at = ?
         WHERE id = ?`,
		normalized.Name,
		normalized.Provider,
		normalized.BaseURL,
		normalized.Model,
		normalized.SecretRef,
		normalized.Enabled,
		makeDefault,
		formatTime(now),
		configID,
	)
	if err != nil {
		return modelconfig.Config{}, fmt.Errorf("updating model config: %w", err)
	}
	if err := requireOneRow(result, modelconfig.ErrNotFound); err != nil {
		return modelconfig.Config{}, err
	}
	if current.IsDefault && !makeDefault {
		if err := promoteDefaultModelConfig(ctx, tx, configID, now); err != nil {
			return modelconfig.Config{}, err
		}
	}
	updated, err := scanModelConfig(tx.QueryRowContext(ctx, modelConfigSelect+" WHERE id = ?", configID))
	if err != nil {
		return modelconfig.Config{}, err
	}
	if err := tx.Commit(); err != nil {
		return modelconfig.Config{}, fmt.Errorf("committing model config update: %w", err)
	}
	return updated, nil
}

// DeleteModelConfig deletes one configuration and promotes another enabled config when needed.
func (s *Store) DeleteModelConfig(ctx context.Context, configID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning model config deletion: %w", err)
	}
	defer tx.Rollback()

	current, err := scanModelConfig(tx.QueryRowContext(ctx, modelConfigSelect+" WHERE id = ?", configID))
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM model_configs WHERE id = ?", configID)
	if err != nil {
		return fmt.Errorf("deleting model config: %w", err)
	}
	if err := requireOneRow(result, modelconfig.ErrNotFound); err != nil {
		return err
	}
	if current.IsDefault {
		if err := promoteDefaultModelConfig(ctx, tx, configID, time.Now().UTC()); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing model config deletion: %w", err)
	}
	return nil
}

const modelConfigSelect = `SELECT
    schema_version, id, name, provider, base_url, model, secret_ref,
    enabled, is_default, created_at, updated_at
FROM model_configs`

type modelConfigScanner interface {
	Scan(dest ...any) error
}

func scanModelConfig(scanner modelConfigScanner) (modelconfig.Config, error) {
	var config modelconfig.Config
	var enabled, isDefault bool
	var createdAt, updatedAt string
	err := scanner.Scan(
		&config.SchemaVersion,
		&config.ID,
		&config.Name,
		&config.Provider,
		&config.BaseURL,
		&config.Model,
		&config.SecretRef,
		&enabled,
		&isDefault,
		&createdAt,
		&updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return modelconfig.Config{}, modelconfig.ErrNotFound
	}
	if err != nil {
		return modelconfig.Config{}, fmt.Errorf("scanning model config: %w", err)
	}
	config.Enabled = enabled
	config.IsDefault = isDefault
	config.HasAPIKey = config.SecretRef != ""
	config.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return modelconfig.Config{}, err
	}
	config.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return modelconfig.Config{}, err
	}
	return config, nil
}

func modelConfigNameExists(
	ctx context.Context,
	tx *sql.Tx,
	name string,
	excludeID string,
) (bool, error) {
	var exists bool
	if err := tx.QueryRowContext(
		ctx,
		"SELECT EXISTS(SELECT 1 FROM model_configs WHERE name = ? AND id <> ?)",
		name,
		excludeID,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("checking model config name: %w", err)
	}
	return exists, nil
}

func modelConfigDefaultExists(
	ctx context.Context,
	tx *sql.Tx,
	excludeID string,
) (bool, error) {
	var exists bool
	if err := tx.QueryRowContext(
		ctx,
		"SELECT EXISTS(SELECT 1 FROM model_configs WHERE enabled = 1 AND is_default = 1 AND id <> ?)",
		excludeID,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("checking default model config: %w", err)
	}
	return exists, nil
}

func promoteDefaultModelConfig(
	ctx context.Context,
	tx *sql.Tx,
	excludeID string,
	now time.Time,
) error {
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE model_configs SET is_default = 1, updated_at = ?
         WHERE id = (
             SELECT id FROM model_configs WHERE enabled = 1 AND id <> ?
             ORDER BY updated_at DESC, id LIMIT 1
         )`,
		formatTime(now),
		excludeID,
	); err != nil {
		return fmt.Errorf("promoting replacement default model config: %w", err)
	}
	return nil
}
