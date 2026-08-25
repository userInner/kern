package pluginmanager

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/userInner/kern/internal/plugin"
	"github.com/userInner/kern/internal/store/sqlite"
)

func TestManagerInstallEnableDisableRemove(t *testing.T) {
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	root := filepath.Join(t.TempDir(), "plugins")
	manager, err := New(root, store)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	source := createPluginPackage(t, "dev.kern.test")
	installed, created, err := manager.Install(t.Context(), source)
	if err != nil || !created || installed.Enabled {
		t.Fatalf("Install() = %#v, %v, %v", installed, created, err)
	}
	if installed.InstallPath == source || !filepath.IsAbs(installed.InstallPath) {
		t.Fatalf("InstallPath = %q", installed.InstallPath)
	}
	if _, err := os.Stat(filepath.Join(installed.InstallPath, "knowledge", "principles.md")); err != nil {
		t.Fatalf("installed payload: %v", err)
	}
	repeated, created, err := manager.Install(t.Context(), source)
	if err != nil || created || repeated.ID != installed.ID {
		t.Fatalf("Install(repeat) = %#v, %v, %v", repeated, created, err)
	}
	enabled, err := manager.Enable(t.Context(), installed.ID)
	if err != nil || !enabled.Enabled {
		t.Fatalf("Enable() = %#v, %v", enabled, err)
	}
	disabled, err := manager.Disable(t.Context(), installed.ID)
	if err != nil || disabled.Enabled {
		t.Fatalf("Disable() = %#v, %v", disabled, err)
	}
	if err := manager.Remove(t.Context(), installed.ID); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := manager.Get(t.Context(), installed.ID); !errors.Is(err, plugin.ErrNotFound) {
		t.Fatalf("Get(removed) error = %v", err)
	}
	if _, err := os.Stat(installed.InstallPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("installed path after remove error = %v", err)
	}
}

func TestManagerRefusesToEnableTamperedInstalledPackage(t *testing.T) {
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := New(filepath.Join(t.TempDir(), "plugins"), store)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	installed, _, err := manager.Install(t.Context(), createPluginPackage(t, "dev.kern.tamper"))
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(installed.InstallPath, "knowledge", "principles.md"),
		[]byte("tampered"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(tampered) error = %v", err)
	}
	if _, err := manager.Enable(t.Context(), installed.ID); !errors.Is(err, plugin.ErrIntegrity) {
		t.Fatalf("Enable(tampered) error = %v", err)
	}
}

func createPluginPackage(t *testing.T, pluginID string) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(directory, "knowledge"), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(directory, "knowledge", "principles.md"),
		[]byte("Use deterministic evidence.\n"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(payload) error = %v", err)
	}
	digest, err := plugin.PackageDigest(directory)
	if err != nil {
		t.Fatalf("PackageDigest() error = %v", err)
	}
	manifest := plugin.Manifest{
		SchemaVersion: plugin.SchemaVersion,
		ID:            pluginID,
		Name:          "Test Expert",
		Version:       "0.1.0",
		Core:          ">=0.1.0 <0.2.0",
		Entrypoints: plugin.Entrypoints{
			Knowledge: []string{"knowledge/principles.md"},
		},
		Activation: plugin.Activation{Signals: []string{"go.mod"}, Intents: []string{"code.review"}},
		Permissions: plugin.Permissions{
			Filesystem: []string{"workspace:read"},
		},
		Integrity: plugin.Integrity{Files: digest},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("Marshal(manifest) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, plugin.ManifestFile), encoded, 0o600); err != nil {
		t.Fatalf("WriteFile(manifest) error = %v", err)
	}
	return directory
}
