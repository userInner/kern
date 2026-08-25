package evaluation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotRootEnforcesActualByteLimit(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootDir, "large.txt"), []byte("1234"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	_, err = snapshotRootWithLimits(t.Context(), root, 1, 3)
	if err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("snapshotRootWithLimits(oversized) error = %v, want byte limit", err)
	}
}

func TestDigestFixtureEnforcesActualByteLimitAndCancellation(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootDir, "large.txt"), []byte("1234"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	if _, err := digestFixtureWithLimits(t.Context(), root, 1, 3); err == nil ||
		!strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("digestFixtureWithLimits(oversized) error = %v, want byte limit", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := digestFixtureWithLimits(canceled, root, 1, 8); !errors.Is(err, context.Canceled) {
		t.Fatalf("digestFixtureWithLimits(canceled) error = %v, want context.Canceled", err)
	}
}
