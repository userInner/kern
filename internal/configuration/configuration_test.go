package configuration

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveLoadRoundTripAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	config := Defaults()
	config.Runtime.MaxTurns = 17
	config.Runtime.TaskTimeout = Duration{Duration: 3 * time.Minute}
	config.Storage.RetentionDays = 45
	config.Policy.Profile = "confirm-all"
	if err := Save(path, config); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Runtime.MaxTurns != 17 || loaded.Runtime.TaskTimeout.Duration != 3*time.Minute ||
		loaded.Storage.RetentionDays != 45 || loaded.Policy.Profile != "confirm-all" {
		t.Fatalf("Load() = %#v", loaded.Runtime)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestLoadOverlaysDefaultsAndRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":"1","runtime":{"max_turns":21}}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Runtime.MaxTurns != 21 || loaded.Runtime.MaxToolCalls == 0 || loaded.Server.Address == "" {
		t.Fatalf("Load() did not retain defaults: %#v", loaded)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":"1","unknown":true}`), 0o600); err != nil {
		t.Fatalf("WriteFile(unknown) error = %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load(unknown) error = nil")
	}
}

func TestValidateRejectsUnsafeValues(t *testing.T) {
	tests := []File{Defaults(), Defaults(), Defaults(), Defaults(), Defaults(), Defaults()}
	tests[0].Server.Address = "0.0.0.0:8787"
	tests[1].Runtime.MaxActiveTasks = 0
	tests[2].Runtime.TaskTimeout = Duration{}
	tests[3].Observability.LogLevel = "verbose"
	tests[4].Storage.RetentionDays = 0
	tests[5].Policy.Profile = "unsafe"
	for _, config := range tests {
		if err := Validate(config); err == nil {
			t.Fatalf("Validate(%#v) error = nil", config)
		}
	}
}

func TestLoadMissingReturnsDefaults(t *testing.T) {
	loaded, err := Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("Load(missing) error = %v", err)
	}
	if loaded != Defaults() {
		t.Fatalf("Load(missing) = %#v", loaded)
	}
}

func TestLoadEffectiveAppliesEnvironmentAfterFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	config := Defaults()
	config.Runtime.MaxTurns = 14
	if err := Save(path, config); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	t.Setenv("KERN_MAX_TURNS", "29")
	t.Setenv("KERN_TASK_TIMEOUT", "7m")
	t.Setenv("KERN_DATA_DIR", filepath.Join(t.TempDir(), "environment-data"))
	loaded, err := LoadEffective(path)
	if err != nil {
		t.Fatalf("LoadEffective() error = %v", err)
	}
	if loaded.Runtime.MaxTurns != 29 || loaded.Runtime.TaskTimeout.Duration != 7*time.Minute ||
		loaded.Storage.DataDir == config.Storage.DataDir {
		t.Fatalf("LoadEffective() = %#v", loaded)
	}
}

func TestLoadEffectiveRejectsInvalidEnvironment(t *testing.T) {
	t.Setenv("KERN_MAX_ACTIVE_TASKS", "many")
	if _, err := LoadEffective(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("LoadEffective() error = nil")
	}
}
