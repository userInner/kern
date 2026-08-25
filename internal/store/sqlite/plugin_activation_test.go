package sqlite

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/userInner/kern/internal/plugin"
)

func TestTaskPluginPreferencesAndAttemptAudit(t *testing.T) {
	store := openTestStore(t)
	item := testInstalledPlugin()
	if _, _, err := store.InstallPlugin(t.Context(), item); err != nil {
		t.Fatalf("InstallPlugin() error = %v", err)
	}
	created, err := store.CreateTask(t.Context(), "plugin audit", "review this Go project")
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	if err := store.SetTaskPluginPreferences(
		t.Context(),
		created.ID,
		[]string{item.ID},
		nil,
	); err != nil {
		t.Fatalf("SetTaskPluginPreferences() error = %v", err)
	}
	preferences, err := store.TaskPluginPreferences(t.Context(), created.ID)
	if err != nil || len(preferences) != 1 || preferences[0].Mode != plugin.PreferenceEnable {
		t.Fatalf("TaskPluginPreferences() = %#v, %v", preferences, err)
	}
	usage := plugin.Usage{
		SchemaVersion: plugin.SchemaVersion,
		TaskID:        created.ID,
		AttemptID:     created.ActiveAttemptID,
		PluginID:      item.ID,
		Version:       item.Version,
		Digest:        item.Digest,
		Reason:        "manual_enable",
		Resources:     json.RawMessage(`{"knowledge":1}`),
		CreatedAt:     time.Now().UTC(),
	}
	recorded, err := store.RecordAttemptPlugin(t.Context(), usage)
	if err != nil || !recorded {
		t.Fatalf("RecordAttemptPlugin() = %v, %v", recorded, err)
	}
	recorded, err = store.RecordAttemptPlugin(t.Context(), usage)
	if err != nil || recorded {
		t.Fatalf("RecordAttemptPlugin(idempotent) = %v, %v", recorded, err)
	}
	items, err := store.ListAttemptPlugins(t.Context(), created.ID, created.ActiveAttemptID)
	if err != nil || len(items) != 1 || items[0].Reason != "manual_enable" {
		t.Fatalf("ListAttemptPlugins() = %#v, %v", items, err)
	}
	events, err := store.EventsAfter(t.Context(), created.ID, 0, 100)
	if err != nil {
		t.Fatalf("EventsAfter() error = %v", err)
	}
	count := 0
	for _, event := range events {
		if event.Type == "plugin.activated" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("plugin.activated event count = %d, want 1", count)
	}
}
