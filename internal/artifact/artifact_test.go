package artifact_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/userInner/kern/internal/artifact"
	"github.com/userInner/kern/internal/store/sqlite"
	"github.com/userInner/kern/internal/task"
)

func TestStoreDeduplicatesContentAndScopesReferencesToTask(t *testing.T) {
	dataDir := t.TempDir()
	repository, err := sqlite.Open(t.Context(), filepath.Join(dataDir, "kern.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	firstTask, err := repository.CreateTask(t.Context(), "artifact", "save evidence")
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	secondTask, err := repository.CreateTask(t.Context(), "other", "must not read evidence")
	if err != nil {
		t.Fatalf("CreateTask(other) error = %v", err)
	}
	storeRoot := filepath.Join(dataDir, "artifacts")
	contentStore, err := artifact.Open(storeRoot, repository)
	if err != nil {
		t.Fatalf("artifact.Open() error = %v", err)
	}

	first, err := contentStore.Put(
		t.Context(),
		firstTask.ID,
		firstTask.ActiveAttemptID,
		"report.txt",
		"text/plain; charset=utf-8",
		"",
		strings.NewReader("verified evidence"),
	)
	if err != nil {
		t.Fatalf("Put(first) error = %v", err)
	}
	second, err := contentStore.Put(
		t.Context(),
		firstTask.ID,
		firstTask.ActiveAttemptID,
		"report-copy.txt",
		"text/plain",
		"",
		strings.NewReader("verified evidence"),
	)
	if err != nil {
		t.Fatalf("Put(second) error = %v", err)
	}
	if first.ID == second.ID || first.Digest != second.Digest || first.StoragePath != second.StoragePath {
		t.Fatalf("content references were not deduplicated: first=%#v second=%#v", first, second)
	}
	items, err := contentStore.List(t.Context(), firstTask.ID)
	if err != nil || len(items) != 2 {
		t.Fatalf("List() = %#v, %v", items, err)
	}
	bounded, err := contentStore.ReadContent(t.Context(), firstTask.ID, first.ID, 64)
	if err != nil || string(bounded) != "verified evidence" {
		t.Fatalf("ReadContent() = %q, %v", bounded, err)
	}
	if _, err := contentStore.ReadContent(t.Context(), firstTask.ID, first.ID, 4); !errors.Is(err, artifact.ErrTooLarge) {
		t.Fatalf("ReadContent(too small) error = %v", err)
	}

	opened, file, err := contentStore.OpenContent(t.Context(), firstTask.ID, first.ID)
	if err != nil {
		t.Fatalf("OpenContent() error = %v", err)
	}
	data, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || string(data) != "verified evidence" || opened.ID != first.ID {
		t.Fatalf("opened content = %q, %#v, read=%v close=%v", data, opened, readErr, closeErr)
	}
	if _, file, err := contentStore.OpenContent(t.Context(), secondTask.ID, first.ID); !errors.Is(err, artifact.ErrForbidden) {
		if file != nil {
			file.Close()
		}
		t.Fatalf("OpenContent(other task) error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(storeRoot, filepath.FromSlash(first.StoragePath)), []byte("tampered"), 0o600); err != nil {
		t.Fatalf("WriteFile(tamper) error = %v", err)
	}
	if _, file, err := contentStore.OpenContent(t.Context(), firstTask.ID, first.ID); !errors.Is(err, artifact.ErrCorrupt) {
		if file != nil {
			file.Close()
		}
		t.Fatalf("OpenContent(tampered) error = %v", err)
	}
}

func TestRetentionCleanupPrunesOnlyObjectsWithoutRemainingReferences(t *testing.T) {
	dataDir := t.TempDir()
	repository, err := sqlite.Open(t.Context(), filepath.Join(dataDir, "kern.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	firstTask, err := repository.CreateTask(t.Context(), "expired", "remove this task")
	if err != nil {
		t.Fatalf("CreateTask(first) error = %v", err)
	}
	secondTask, err := repository.CreateTask(t.Context(), "retained", "keep shared content")
	if err != nil {
		t.Fatalf("CreateTask(second) error = %v", err)
	}
	contentStore, err := artifact.Open(filepath.Join(dataDir, "artifacts"), repository)
	if err != nil {
		t.Fatalf("artifact.Open() error = %v", err)
	}
	first, err := contentStore.Put(
		t.Context(), firstTask.ID, firstTask.ActiveAttemptID, "first.txt", "text/plain", "",
		strings.NewReader("shared content"),
	)
	if err != nil {
		t.Fatalf("Put(first) error = %v", err)
	}
	second, err := contentStore.Put(
		t.Context(), secondTask.ID, secondTask.ActiveAttemptID, "second.txt", "text/plain", "",
		strings.NewReader("shared content"),
	)
	if err != nil {
		t.Fatalf("Put(second) error = %v", err)
	}
	if first.StoragePath != second.StoragePath {
		t.Fatalf("shared objects differ: %q != %q", first.StoragePath, second.StoragePath)
	}
	completeTask(t, repository, firstTask.ID)
	preview, err := repository.PreviewTerminalTaskCleanup(t.Context(), time.Now().Add(time.Hour))
	if err != nil || preview.TaskCount != 1 || preview.ArtifactCount != 1 || preview.ArtifactBytes != first.Size {
		t.Fatalf("PreviewTerminalTaskCleanup() = %#v, %v", preview, err)
	}
	deleted, err := repository.DeleteTerminalTasksBefore(t.Context(), time.Now().Add(time.Hour))
	if err != nil || deleted.TaskCount != 1 || len(deleted.ArtifactPaths) != 1 {
		t.Fatalf("DeleteTerminalTasksBefore(first) = %#v, %v", deleted, err)
	}
	removed, removedBytes, err := contentStore.PruneUnreferenced(t.Context(), deleted.ArtifactPaths)
	if err != nil || removed != 0 || removedBytes != 0 {
		t.Fatalf("PruneUnreferenced(shared) = %d, %d, %v", removed, removedBytes, err)
	}
	objectPath := filepath.Join(dataDir, "artifacts", filepath.FromSlash(first.StoragePath))
	if _, err := os.Stat(objectPath); err != nil {
		t.Fatalf("shared object was removed: %v", err)
	}
	completeTask(t, repository, secondTask.ID)
	deleted, err = repository.DeleteTerminalTasksBefore(t.Context(), time.Now().Add(time.Hour))
	if err != nil || deleted.TaskCount != 1 {
		t.Fatalf("DeleteTerminalTasksBefore(second) = %#v, %v", deleted, err)
	}
	removed, removedBytes, err = contentStore.PruneUnreferenced(t.Context(), deleted.ArtifactPaths)
	if err != nil || removed != 1 || removedBytes != second.Size {
		t.Fatalf("PruneUnreferenced(orphan) = %d, %d, %v", removed, removedBytes, err)
	}
	if _, err := os.Stat(objectPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan object Stat() error = %v, want os.ErrNotExist", err)
	}
	if _, _, err := contentStore.PruneUnreferenced(t.Context(), []string{"../outside"}); err == nil {
		t.Fatal("PruneUnreferenced(invalid path) error = nil")
	}
}

func completeTask(t *testing.T, repository *sqlite.Store, taskID string) {
	t.Helper()
	for _, status := range []task.Status{task.StatusRunning, task.StatusVerifying, task.StatusCompleted} {
		if err := repository.Transition(t.Context(), taskID, status, "done", ""); err != nil {
			t.Fatalf("Transition(%s) error = %v", status, err)
		}
	}
}
