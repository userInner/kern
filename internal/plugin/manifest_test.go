package plugin

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndVerifyPackage(t *testing.T) {
	directory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(directory, "knowledge"), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	payload := filepath.Join(directory, "knowledge", "principles.md")
	if err := os.WriteFile(payload, []byte("Go evidence\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(payload) error = %v", err)
	}
	digest, err := PackageDigest(directory)
	if err != nil {
		t.Fatalf("PackageDigest() error = %v", err)
	}
	manifest := validManifest(digest)
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, ManifestFile), encoded, 0o600); err != nil {
		t.Fatalf("WriteFile(manifest) error = %v", err)
	}
	loaded, _, err := LoadManifest(directory)
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}
	verified, err := VerifyPackage(directory, loaded)
	if err != nil || verified != digest {
		t.Fatalf("VerifyPackage() = %q, %v; want %q", verified, err, digest)
	}
	if err := os.WriteFile(payload, []byte("tampered\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(tampered) error = %v", err)
	}
	if _, err := VerifyPackage(directory, loaded); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("VerifyPackage(tampered) error = %v, want ErrIntegrity", err)
	}
}

func TestValidateManifestRejectsTraversalAndIncompatibility(t *testing.T) {
	manifest := validManifest("sha256:" + string(make([]byte, 64)))
	manifest.Integrity.Files = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	manifest.Entrypoints.Knowledge = []string{"../secret"}
	if err := ValidateManifest(manifest); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("ValidateManifest(traversal) error = %v", err)
	}
	manifest = validManifest("sha256:0000000000000000000000000000000000000000000000000000000000000000")
	manifest.Core = ">=0.2.0 <0.3.0"
	if err := ValidateManifest(manifest); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("ValidateManifest(incompatible) error = %v", err)
	}
	manifest = validManifest("sha256:0000000000000000000000000000000000000000000000000000000000000000")
	manifest.ID = "host.reserved"
	if err := ValidateManifest(manifest); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("ValidateManifest(host namespace) error = %v", err)
	}
}

func TestValidateManifestProcessPermissions(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		executable string
		wantError  bool
	}{
		{name: "Windows executable suffix", executable: "app.test.exe"},
		{name: "digit in executable", executable: "tool0"},
		{name: "slash", executable: "bin/tool", wantError: true},
		{name: "backslash", executable: `bin\tool`, wantError: true},
		{name: "NUL", executable: "tool\x00name", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			manifest := validManifest("sha256:0000000000000000000000000000000000000000000000000000000000000000")
			manifest.Permissions.Process = []string{test.executable}
			err := ValidateManifest(manifest)
			if test.wantError && !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("ValidateManifest() error = %v, want ErrInvalidManifest", err)
			}
			if !test.wantError && err != nil {
				t.Fatalf("ValidateManifest() error = %v", err)
			}
		})
	}
}

func TestLoadManifestRejectsUnknownFields(t *testing.T) {
	directory := t.TempDir()
	manifest := `{
		"schema_version":"1",
		"id":"dev.kern.test",
		"name":"Test",
		"version":"0.1.0",
		"core":">=0.1.0 <0.2.0",
		"entrypoints":{"knowledge":["knowledge.md"]},
		"activation":{},
		"permissions":{},
		"integrity":{"files":"sha256:0000000000000000000000000000000000000000000000000000000000000000"},
		"unexpected":true
	}`
	if err := os.WriteFile(filepath.Join(directory, ManifestFile), []byte(manifest), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, _, err := LoadManifest(directory); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("LoadManifest() error = %v, want ErrInvalidManifest", err)
	}
}

func TestCompatible(t *testing.T) {
	compatible, err := Compatible(">=0.1.0 <0.2.0", "0.1.9")
	if err != nil || !compatible {
		t.Fatalf("Compatible() = %v, %v", compatible, err)
	}
	compatible, err = Compatible(">=0.1.0 <0.2.0", "0.2.0")
	if err != nil || compatible {
		t.Fatalf("Compatible(upper bound) = %v, %v", compatible, err)
	}
}

func validManifest(digest string) Manifest {
	return Manifest{
		SchemaVersion: SchemaVersion,
		ID:            "dev.kern.test",
		Name:          "Test Expert",
		Version:       "0.1.0",
		Core:          ">=0.1.0 <0.2.0",
		Entrypoints: Entrypoints{
			Knowledge: []string{"knowledge/principles.md"},
		},
		Activation: Activation{Signals: []string{"go.mod"}, Intents: []string{"code.review"}},
		Permissions: Permissions{
			Filesystem: []string{"workspace:read"},
		},
		Integrity: Integrity{Files: digest},
	}
}
