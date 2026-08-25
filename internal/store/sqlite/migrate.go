package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func migrate(ctx context.Context, db *sql.DB, databasePath string) (string, error) {
	migrations, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return "", fmt.Errorf("opening migration files: %w", err)
	}
	targetVersion, err := latestMigrationVersion(migrations)
	if err != nil {
		return "", err
	}
	needsBackup, currentVersion, err := migrationBackupRequired(ctx, db, targetVersion)
	if err != nil {
		return "", err
	}
	backupPath := ""
	if needsBackup {
		backupPath, err = backupDatabase(ctx, db, databasePath, currentVersion, targetVersion)
		if err != nil {
			return "", err
		}
	}
	provider, err := goose.NewProvider(
		goose.DialectSQLite3,
		db,
		migrations,
		goose.WithTableName("kern_schema_versions"),
	)
	if err != nil {
		return backupPath, fmt.Errorf("creating migration provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return backupPath, fmt.Errorf("applying database migrations: %w", err)
	}
	return backupPath, nil
}

func latestMigrationVersion(migrations fs.FS) (int64, error) {
	entries, err := fs.ReadDir(migrations, ".")
	if err != nil {
		return 0, fmt.Errorf("listing migration files: %w", err)
	}
	var latest int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			return 0, fmt.Errorf("invalid migration filename %q", entry.Name())
		}
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil || version < 1 {
			return 0, fmt.Errorf("invalid migration version in %q", entry.Name())
		}
		if version > latest {
			latest = version
		}
	}
	if latest == 0 {
		return 0, errors.New("sqlite: no migration files")
	}
	return latest, nil
}

func migrationBackupRequired(ctx context.Context, db *sql.DB, targetVersion int64) (bool, int64, error) {
	var versionTable bool
	if err := db.QueryRowContext(
		ctx,
		`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'kern_schema_versions')`,
	).Scan(&versionTable); err != nil {
		return false, 0, fmt.Errorf("checking migration metadata: %w", err)
	}

	var currentVersion int64
	if versionTable {
		if err := db.QueryRowContext(
			ctx,
			`SELECT COALESCE(MAX(version_id), 0) FROM kern_schema_versions WHERE is_applied = 1`,
		).Scan(&currentVersion); err != nil {
			return false, 0, fmt.Errorf("reading current schema version: %w", err)
		}
	}
	if currentVersion > targetVersion {
		return false, currentVersion, fmt.Errorf(
			"sqlite: database schema v%d is newer than supported v%d",
			currentVersion,
			targetVersion,
		)
	}
	if currentVersion == targetVersion {
		return false, currentVersion, nil
	}

	var userTables int
	if err := db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM sqlite_master
		 WHERE type = 'table'
		   AND name NOT LIKE 'sqlite_%'
		   AND name != 'kern_schema_versions'`,
	).Scan(&userTables); err != nil {
		return false, currentVersion, fmt.Errorf("checking existing database content: %w", err)
	}
	return userTables > 0, currentVersion, nil
}

func backupDatabase(
	ctx context.Context,
	db *sql.DB,
	databasePath string,
	currentVersion int64,
	targetVersion int64,
) (string, error) {
	directory := filepath.Dir(databasePath)
	base := filepath.Base(databasePath)
	reserved, err := os.CreateTemp(
		directory,
		fmt.Sprintf("%s.pre-migration-v%d-to-v%d-*.db", base, currentVersion, targetVersion),
	)
	if err != nil {
		return "", fmt.Errorf("reserving migration backup: %w", err)
	}
	backupPath := reserved.Name()
	if closeErr := reserved.Close(); closeErr != nil {
		_ = os.Remove(backupPath)
		return "", fmt.Errorf("closing migration backup placeholder: %w", closeErr)
	}
	if err := os.Remove(backupPath); err != nil {
		return "", fmt.Errorf("preparing migration backup path: %w", err)
	}

	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, backupPath); err != nil {
		_ = os.Remove(backupPath)
		return "", fmt.Errorf("creating consistent pre-migration backup: %w", err)
	}
	if err := os.Chmod(backupPath, 0o600); err != nil {
		_ = os.Remove(backupPath)
		return "", fmt.Errorf("securing migration backup: %w", err)
	}
	return backupPath, nil
}
