package sqlite

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestOpenMigratesLegacyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	legacy := `
CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    schema_version TEXT NOT NULL,
    title TEXT NOT NULL,
    goal TEXT NOT NULL,
    status TEXT NOT NULL,
    result TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    active_attempt_id TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE attempts (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    status TEXT NOT NULL,
    started_at TEXT NOT NULL,
    finished_at TEXT
);
CREATE TABLE verifications (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE CASCADE,
    verifier TEXT NOT NULL,
    status TEXT NOT NULL,
    evidence_json TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE task_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    schema_version TEXT NOT NULL,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    attempt_id TEXT NOT NULL REFERENCES attempts(id) ON DELETE CASCADE,
    type TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_attempts_one_active ON attempts(task_id) WHERE finished_at IS NULL;
`
	if _, err := db.ExecContext(t.Context(), legacy); err != nil {
		_ = db.Close()
		t.Fatalf("creating legacy database: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing legacy database: %v", err)
	}

	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open(legacy) error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var version int64
	if err := store.db.QueryRowContext(
		t.Context(),
		"SELECT MAX(version_id) FROM kern_schema_versions WHERE is_applied = 1",
	).Scan(&version); err != nil {
		t.Fatalf("querying migration version: %v", err)
	}
	if version != 14 {
		t.Fatalf("migration version = %d, want 14", version)
	}
	for _, table := range []string{
		"checkpoints",
		"operations",
		"operation_results",
		"approval_requests",
		"approval_receipts",
		"artifacts",
		"context_messages",
		"operation_resolutions",
		"plans",
		"plan_steps",
		"model_configs",
		"attempt_model_configs",
		"plugins",
		"task_plugin_preferences",
		"attempt_plugins",
		"eval_suites",
		"eval_runs",
		"eval_cases",
	} {
		var count int
		if err := store.db.QueryRowContext(
			t.Context(),
			"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?",
			table,
		).Scan(&count); err != nil {
			t.Fatalf("checking table %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("table %s count = %d, want 1", table, count)
		}
	}

	backups, err := filepath.Glob(path + ".pre-migration-v0-to-v14-*.db")
	if err != nil {
		t.Fatalf("Glob(pre-migration backup) error = %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("pre-migration backups = %v, want exactly one", backups)
	}
	if store.MigrationBackupPath() != backups[0] {
		t.Fatalf("MigrationBackupPath() = %q, want %q", store.MigrationBackupPath(), backups[0])
	}
	info, err := os.Stat(backups[0])
	if err != nil {
		t.Fatalf("Stat(pre-migration backup) error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("pre-migration backup permissions = %o, want 600", got)
	}
	backup, err := sql.Open("sqlite", "file:"+filepath.ToSlash(backups[0])+"?mode=ro")
	if err != nil {
		t.Fatalf("opening pre-migration backup: %v", err)
	}
	defer backup.Close()
	var legacyTasks, migratedMetadata int
	if err := backup.QueryRowContext(
		t.Context(),
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'tasks'`,
	).Scan(&legacyTasks); err != nil {
		t.Fatalf("checking legacy backup tasks table: %v", err)
	}
	if err := backup.QueryRowContext(
		t.Context(),
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'kern_schema_versions'`,
	).Scan(&migratedMetadata); err != nil {
		t.Fatalf("checking legacy backup metadata table: %v", err)
	}
	if legacyTasks != 1 || migratedMetadata != 0 {
		t.Fatalf("backup schema = tasks:%d migration metadata:%d", legacyTasks, migratedMetadata)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("closing migrated store: %v", err)
	}
	reopened, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open(current) error = %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("closing current store: %v", err)
	}
	if reopened.MigrationBackupPath() != "" {
		t.Fatalf("current-schema MigrationBackupPath() = %q, want empty", reopened.MigrationBackupPath())
	}
	backupsAfterReopen, err := filepath.Glob(path + ".pre-migration-*.db")
	if err != nil {
		t.Fatalf("Glob(backups after reopen) error = %v", err)
	}
	if len(backupsAfterReopen) != 1 {
		t.Fatalf("backups after current-schema reopen = %v, want one", backupsAfterReopen)
	}
}
