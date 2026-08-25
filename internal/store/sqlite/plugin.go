package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/userInner/kern/internal/plugin"
)

// InstallPlugin persists one validated version. Repeating the exact package is idempotent.
func (s *Store) InstallPlugin(
	ctx context.Context,
	item plugin.Installed,
) (plugin.Installed, bool, error) {
	if err := plugin.ValidateManifest(item.Manifest); err != nil {
		return plugin.Installed{}, false, err
	}
	manifest, err := json.Marshal(item.Manifest)
	if err != nil {
		return plugin.Installed{}, false, fmt.Errorf("encoding plugin manifest: %w", err)
	}
	if item.ID != item.Manifest.ID || item.Version != item.Manifest.Version ||
		item.Digest != item.Manifest.Integrity.Files || item.InstallPath == "" || item.Source == "" {
		return plugin.Installed{}, false, errors.New("sqlite: inconsistent plugin installation")
	}
	if item.SchemaVersion == "" {
		item.SchemaVersion = plugin.SchemaVersion
	}
	if item.TrustStatus == "" {
		item.TrustStatus = "local-unverified"
	}
	if item.InstalledAt.IsZero() {
		item.InstalledAt = time.Now().UTC()
	}
	item.UpdatedAt = item.InstalledAt
	result, err := s.db.ExecContext(
		ctx,
		`INSERT INTO plugins (
			plugin_id, schema_version, name, description, version, source, install_path,
			manifest_json, digest, enabled, trust_status, installed_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(plugin_id) DO NOTHING`,
		item.ID,
		item.SchemaVersion,
		item.Name,
		item.Description,
		item.Version,
		item.Source,
		item.InstallPath,
		string(manifest),
		item.Digest,
		item.Enabled,
		item.TrustStatus,
		formatTime(item.InstalledAt),
		formatTime(item.UpdatedAt),
	)
	if err != nil {
		return plugin.Installed{}, false, fmt.Errorf("installing plugin: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return plugin.Installed{}, false, fmt.Errorf("checking plugin insert: %w", err)
	}
	stored, err := s.GetPlugin(ctx, item.ID)
	if err != nil {
		return plugin.Installed{}, false, err
	}
	if rows == 0 && (stored.Version != item.Version || stored.Digest != item.Digest) {
		return plugin.Installed{}, false, plugin.ErrConflict
	}
	return stored, rows == 1, nil
}

// GetPlugin returns one installed plugin by stable ID.
func (s *Store) GetPlugin(ctx context.Context, pluginID string) (plugin.Installed, error) {
	return scanPlugin(s.db.QueryRowContext(ctx, pluginSelect+" WHERE plugin_id = ?", pluginID))
}

// ListPlugins returns installed plugins ordered by stable ID.
func (s *Store) ListPlugins(ctx context.Context) ([]plugin.Installed, error) {
	rows, err := s.db.QueryContext(ctx, pluginSelect+" ORDER BY plugin_id ASC")
	if err != nil {
		return nil, fmt.Errorf("listing plugins: %w", err)
	}
	defer rows.Close()
	items := make([]plugin.Installed, 0)
	for rows.Next() {
		item, err := scanPlugin(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating plugins: %w", err)
	}
	return items, nil
}

// SetPluginEnabled changes activation eligibility without changing package bytes.
func (s *Store) SetPluginEnabled(
	ctx context.Context,
	pluginID string,
	enabled bool,
) (plugin.Installed, error) {
	result, err := s.db.ExecContext(
		ctx,
		"UPDATE plugins SET enabled = ?, updated_at = ? WHERE plugin_id = ?",
		enabled,
		formatTime(time.Now().UTC()),
		pluginID,
	)
	if err != nil {
		return plugin.Installed{}, fmt.Errorf("updating plugin state: %w", err)
	}
	if err := requireOneRow(result, plugin.ErrNotFound); err != nil {
		return plugin.Installed{}, err
	}
	return s.GetPlugin(ctx, pluginID)
}

// DeletePlugin deletes only the durable installation record.
func (s *Store) DeletePlugin(ctx context.Context, pluginID string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM plugins WHERE plugin_id = ?", pluginID)
	if err != nil {
		return fmt.Errorf("deleting plugin: %w", err)
	}
	return requireOneRow(result, plugin.ErrNotFound)
}

const pluginSelect = `SELECT
	schema_version, plugin_id, name, description, version, source, install_path,
	manifest_json, digest, enabled, trust_status, installed_at, updated_at
	FROM plugins`

func scanPlugin(row rowScanner) (plugin.Installed, error) {
	var item plugin.Installed
	var manifest string
	var enabled int
	var installedAt, updatedAt string
	err := row.Scan(
		&item.SchemaVersion,
		&item.ID,
		&item.Name,
		&item.Description,
		&item.Version,
		&item.Source,
		&item.InstallPath,
		&manifest,
		&item.Digest,
		&enabled,
		&item.TrustStatus,
		&installedAt,
		&updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return plugin.Installed{}, plugin.ErrNotFound
	}
	if err != nil {
		return plugin.Installed{}, fmt.Errorf("scanning plugin: %w", err)
	}
	if err := json.Unmarshal([]byte(manifest), &item.Manifest); err != nil {
		return plugin.Installed{}, fmt.Errorf("decoding plugin manifest: %w", err)
	}
	item.Enabled = enabled != 0
	item.InstalledAt, err = parseTime(installedAt)
	if err != nil {
		return plugin.Installed{}, err
	}
	item.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return plugin.Installed{}, err
	}
	return item, nil
}
