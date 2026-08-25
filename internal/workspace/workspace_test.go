package workspace

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceConfinesAndBlocksSensitivePaths(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "README.md"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, ".env"), []byte("SECRET=value"), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatalf("WriteFile(outside) error = %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(rootPath, "escape")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	workspace := openTestWorkspace(t, rootPath)

	file, err := workspace.ReadFile(t.Context(), "README.md")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(file.Data) != "hello" || file.SHA256 == "" {
		t.Fatalf("ReadFile() = %#v", file)
	}
	if _, err := workspace.ReadFile(t.Context(), "../outside.txt"); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("ReadFile(outside) error = %v, want ErrOutsideRoot", err)
	}
	if _, err := workspace.ReadFile(t.Context(), ".env"); !errors.Is(err, ErrSensitive) {
		t.Fatalf("ReadFile(.env) error = %v, want ErrSensitive", err)
	}
	if _, err := workspace.ReadFile(t.Context(), "escape"); err == nil {
		t.Fatal("ReadFile(escaping symlink) error = nil")
	}
}

func TestWorkspaceAtomicWriteRequiresCurrentHash(t *testing.T) {
	workspace := openTestWorkspace(t, t.TempDir())
	created, err := workspace.WriteFile(t.Context(), "notes/a.txt", []byte("first"), "")
	if err != nil {
		t.Fatalf("WriteFile(create) error = %v", err)
	}
	if !created.Created || created.BeforeSHA256 != "" || created.AfterSHA256 == "" ||
		created.DiffStatus != "complete" || !strings.Contains(created.Diff, "+first") {
		t.Fatalf("WriteFile(create) = %#v", created)
	}
	if _, err := workspace.WriteFile(t.Context(), "notes/a.txt", []byte("unsafe"), ""); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("WriteFile(no hash) error = %v, want ErrHashMismatch", err)
	}
	updated, err := workspace.WriteFile(
		t.Context(),
		"notes/a.txt",
		[]byte("second"),
		created.AfterSHA256,
	)
	if err != nil {
		t.Fatalf("WriteFile(update) error = %v", err)
	}
	if updated.Created || updated.BeforeSHA256 != created.AfterSHA256 ||
		updated.DiffStatus != "complete" || !strings.Contains(updated.Diff, "-first") ||
		!strings.Contains(updated.Diff, "+second") {
		t.Fatalf("WriteFile(update) = %#v", updated)
	}
	loaded, err := workspace.ReadFile(t.Context(), "notes/a.txt")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(loaded.Data) != "second" || loaded.SHA256 != updated.AfterSHA256 {
		t.Fatalf("ReadFile() = %#v", loaded)
	}
	temporary, err := filepath.Glob(filepath.Join(workspace.Root(), "notes", ".kern-tmp-*"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(temporary) != 0 {
		t.Fatalf("temporary files remain: %v", temporary)
	}
}

func TestWorkspaceMoveRequiresCurrentHashAndAbsentDestination(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "source.txt"), []byte("move me"), 0o640); err != nil {
		t.Fatalf("WriteFile(source) error = %v", err)
	}
	workspace := openTestWorkspace(t, rootPath)
	source, err := workspace.ReadFile(t.Context(), "source.txt")
	if err != nil {
		t.Fatalf("ReadFile(source) error = %v", err)
	}
	intent, err := workspace.PrepareMove(t.Context(), "./source.txt", "destination.txt", source.SHA256)
	if err != nil {
		t.Fatalf("PrepareMove() error = %v", err)
	}
	if intent.SourcePath != "source.txt" || intent.DestinationPath != "destination.txt" ||
		intent.SHA256 != source.SHA256 {
		t.Fatalf("PrepareMove() = %#v", intent)
	}
	change, err := workspace.MoveFile(t.Context(), "source.txt", "destination.txt", source.SHA256)
	if err != nil {
		t.Fatalf("MoveFile() error = %v", err)
	}
	if change.Path != "destination.txt" || change.MovedFrom != "source.txt" ||
		change.BeforeSHA256 != source.SHA256 || change.AfterSHA256 != source.SHA256 ||
		!change.Created || change.Bytes != source.Size || change.DiffStatus != "complete" ||
		!strings.Contains(change.Diff, `rename from "source.txt"`) ||
		!strings.Contains(change.Diff, `rename to "destination.txt"`) {
		t.Fatalf("MoveFile() = %#v", change)
	}
	if _, err := workspace.ReadFile(t.Context(), "source.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadFile(source after move) error = %v, want fs.ErrNotExist", err)
	}
	destination, err := workspace.ReadFile(t.Context(), "destination.txt")
	if err != nil {
		t.Fatalf("ReadFile(destination) error = %v", err)
	}
	if string(destination.Data) != "move me" || destination.SHA256 != source.SHA256 {
		t.Fatalf("ReadFile(destination) = %#v", destination)
	}
	info, err := os.Stat(filepath.Join(rootPath, "destination.txt"))
	if err != nil {
		t.Fatalf("Stat(destination) error = %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("destination mode = %v, want 0640", info.Mode().Perm())
	}
}

func TestWorkspaceMoveRejectsOverwriteStaleHashAndInvalidPaths(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "source.txt"), []byte("source"), 0o600); err != nil {
		t.Fatalf("WriteFile(source) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "destination.txt"), []byte("destination"), 0o600); err != nil {
		t.Fatalf("WriteFile(destination) error = %v", err)
	}
	workspace := openTestWorkspace(t, rootPath)
	source, err := workspace.ReadFile(t.Context(), "source.txt")
	if err != nil {
		t.Fatalf("ReadFile(source) error = %v", err)
	}
	if _, err := workspace.MoveFile(t.Context(), "source.txt", "destination.txt", source.SHA256); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("MoveFile(overwrite) error = %v, want fs.ErrExist", err)
	}
	for path, want := range map[string]string{"source.txt": "source", "destination.txt": "destination"} {
		file, err := workspace.ReadFile(t.Context(), path)
		if err != nil || string(file.Data) != want {
			t.Fatalf("ReadFile(%s) = %#v, %v", path, file, err)
		}
	}
	if _, err := workspace.MoveFile(t.Context(), "source.txt", "other.txt", strings.Repeat("0", 64)); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("MoveFile(stale hash) error = %v, want ErrHashMismatch", err)
	}
	if _, err := workspace.MoveFile(t.Context(), "source.txt", "source.txt", source.SHA256); err == nil {
		t.Fatal("MoveFile(same path) error = nil")
	}
	if _, err := workspace.MoveFile(t.Context(), "source.txt", ".env", source.SHA256); !errors.Is(err, ErrSensitive) {
		t.Fatalf("MoveFile(sensitive destination) error = %v, want ErrSensitive", err)
	}
	if _, err := workspace.MoveFile(t.Context(), "source.txt", "../outside.txt", source.SHA256); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("MoveFile(outside destination) error = %v, want ErrOutsideRoot", err)
	}
}

func TestWorkspaceMoveRejectsSymlinkSource(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "target.txt"), []byte("target"), 0o600); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}
	if err := os.Symlink("target.txt", filepath.Join(rootPath, "source.txt")); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}
	workspace := openTestWorkspace(t, rootPath)
	source, err := workspace.ReadFile(t.Context(), "source.txt")
	if err != nil {
		t.Fatalf("ReadFile(source) error = %v", err)
	}
	if _, err := workspace.MoveFile(t.Context(), "source.txt", "destination.txt", source.SHA256); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("MoveFile(symlink source) error = %v, want ErrNotRegular", err)
	}
}

func TestUnifiedDiffClassifiesBinaryAndOversizedContent(t *testing.T) {
	t.Parallel()
	if diff, status := unifiedDiff("binary.dat", []byte{0, 1}, []byte{0, 2}); diff != "" || status != "binary" {
		t.Fatalf("unifiedDiff(binary) = %q, %q", diff, status)
	}
	large := bytes.Repeat([]byte{'x'}, maxDiffInputBytes+1)
	if diff, status := unifiedDiff("large.txt", nil, large); diff != "" || status != "too_large" {
		t.Fatalf("unifiedDiff(large) = %q, %q", diff, status)
	}
}

func TestWorkspaceReplaceAndSearch(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(rootPath, "main.go"),
		[]byte("package main\n\nconst greeting = \"hello\"\n"),
		0o644,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	workspace := openTestWorkspace(t, rootPath)
	file, err := workspace.ReadFile(t.Context(), "main.go")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	change, err := workspace.Replace(
		t.Context(),
		"main.go",
		`"hello"`,
		`"kern"`,
		file.SHA256,
	)
	if err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if change.AfterSHA256 == file.SHA256 {
		t.Fatal("Replace() did not change hash")
	}
	matches, err := workspace.Search(t.Context(), ".", "greeting")
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(matches) != 1 || matches[0].Path != "main.go" || matches[0].Line != 3 {
		t.Fatalf("Search() = %#v", matches)
	}
	entries, err := workspace.ListDir(t.Context(), ".")
	if err != nil {
		t.Fatalf("ListDir() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "main.go" {
		t.Fatalf("ListDir() = %#v", entries)
	}
}

func openTestWorkspace(t *testing.T, path string) *Workspace {
	t.Helper()
	workspace, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := workspace.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return workspace
}
