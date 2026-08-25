package evaluation

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCopyFixtureUsesConfinedRootHandles(t *testing.T) {
	sourceDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(sourceDir, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "nested", "file.txt"), []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	source, err := os.OpenRoot(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	destination := filepath.Join(t.TempDir(), "workspace")

	if err := copyFixture(t.Context(), source, destination); err != nil {
		t.Fatalf("copyFixture() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "nested", "file.txt"))
	if err != nil || string(data) != "fixture" {
		t.Fatalf("copied file = %q, %v", data, err)
	}
	info, err := os.Stat(filepath.Join(destination, "nested", "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		// Windows reports synthesized rw bits for regular files and does not
		// expose owner/group/other ACL separation through FileMode. Still verify
		// that copying did not introduce executable permission bits.
		if info.Mode().Perm()&0o111 != 0 {
			t.Fatalf("copied file mode = %v, want no executable bits", info.Mode().Perm())
		}
	} else if info.Mode().Perm() != 0o700 {
		t.Fatalf("copied file mode = %v, want 0700", info.Mode().Perm())
	}
}

func TestCopyFixtureRejectsSymlinkEntry(t *testing.T) {
	sourceDir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	requireSymlink(t, outside, filepath.Join(sourceDir, "leak.txt"))
	source, err := os.OpenRoot(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()

	if err := copyFixture(t.Context(), source, filepath.Join(t.TempDir(), "workspace")); err == nil {
		t.Fatal("copyFixture(symlink) error = nil")
	}
}

func TestCopyFixtureFileRejectsGrowthBeyondObservedSize(t *testing.T) {
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "growing.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := os.OpenRoot(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	observed, err := source.Lstat("growing.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "growing.txt"), []byte("grew"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()

	_, err = copyFixtureFile(t.Context(), source, destination, "growing.txt", observed.Size())
	if !errors.Is(err, errFileByteLimit) {
		t.Fatalf("copyFixtureFile(grown) error = %v, want errFileByteLimit", err)
	}
	if _, statErr := destination.Stat("growing.txt"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("destination.Stat(growing.txt) error = %v, want not exist", statErr)
	}
}

func TestCopyFixtureContentsEnforcesActualByteLimit(t *testing.T) {
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "large.txt"), []byte("1234"), 0o700); err != nil {
		t.Fatal(err)
	}
	source, err := os.OpenRoot(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	destination, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()

	if err := copyFixtureContentsWithLimits(t.Context(), source, destination, 1, 3); err == nil {
		t.Fatal("copyFixtureContentsWithLimits(oversized) error = nil")
	}
	if _, statErr := destination.Stat("large.txt"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("destination.Stat(large.txt) error = %v, want not exist", statErr)
	}
}
