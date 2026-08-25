package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/userInner/kern/internal/task"
)

func TestStoreTaskLifecycle(t *testing.T) {
	ctx := t.Context()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	created, err := store.CreateTask(ctx, "", "Inspect the repository")
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	if created.Title != "Inspect the repository" {
		t.Fatalf("CreateTask() title = %q", created.Title)
	}
	transitions := []task.Status{
		task.StatusPlanning,
		task.StatusRunning,
		task.StatusVerifying,
		task.StatusCompleted,
	}
	for _, status := range transitions {
		result := ""
		if status == task.StatusVerifying || status == task.StatusCompleted {
			result = "done"
		}
		if err := store.Transition(ctx, created.ID, status, result, ""); err != nil {
			t.Fatalf("Transition(%q) error = %v", status, err)
		}
	}

	completed, err := store.GetTask(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if completed.Status != task.StatusCompleted || completed.Result != "done" {
		t.Fatalf("GetTask() = status %q, result %q", completed.Status, completed.Result)
	}
	events, err := store.EventsAfter(ctx, created.ID, 0, 100)
	if err != nil {
		t.Fatalf("EventsAfter() error = %v", err)
	}
	if len(events) != 6 {
		t.Fatalf("EventsAfter() count = %d, want 6", len(events))
	}
	for index := 1; index < len(events); index++ {
		if events[index].ID <= events[index-1].ID {
			t.Fatalf("event IDs are not monotonic: %d then %d", events[index-1].ID, events[index].ID)
		}
	}
	resumed, err := store.EventsAfter(ctx, created.ID, events[2].ID, 100)
	if err != nil {
		t.Fatalf("EventsAfter(cursor) error = %v", err)
	}
	if len(resumed) != len(events)-3 {
		t.Fatalf("EventsAfter(cursor) count = %d, want %d", len(resumed), len(events)-3)
	}
}

func TestStoreRejectsInvalidTransition(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	created, err := store.CreateTask(ctx, "test", "test transition")
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	err = store.Transition(ctx, created.ID, task.StatusCompleted, "", "")
	if !errors.Is(err, task.ErrInvalidTransition) {
		t.Fatalf("Transition() error = %v, want ErrInvalidTransition", err)
	}
	loaded, err := store.GetTask(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if loaded.Status != task.StatusCreated {
		t.Fatalf("GetTask() status = %q, want created", loaded.Status)
	}
}
