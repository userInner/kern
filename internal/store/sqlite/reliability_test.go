package sqlite

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/userInner/kern/internal/contextbuilder"
	"github.com/userInner/kern/internal/executionphase"
	"github.com/userInner/kern/internal/model"
	"github.com/userInner/kern/internal/task"
)

func TestStoreLeaseLifecycle(t *testing.T) {
	store := openTestStore(t)
	created := createTestTask(t, store)

	if err := store.AcquireLease(t.Context(), created.ID, "worker-a", time.Minute); err != nil {
		t.Fatalf("AcquireLease(worker-a) error = %v", err)
	}
	if err := store.AcquireLease(t.Context(), created.ID, "worker-b", time.Minute); !errors.Is(err, task.ErrLeaseHeld) {
		t.Fatalf("AcquireLease(worker-b) error = %v, want ErrLeaseHeld", err)
	}
	if err := store.RenewLease(t.Context(), created.ID, "worker-a", 2*time.Minute); err != nil {
		t.Fatalf("RenewLease() error = %v", err)
	}
	leased, err := store.GetTask(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if leased.LeaseOwner != "worker-a" || leased.LeaseExpiresAt == nil || leased.HeartbeatAt == nil {
		t.Fatalf("GetTask() lease = %#v", leased)
	}
	if err := store.ReleaseLease(t.Context(), created.ID, "worker-b"); err != nil {
		t.Fatalf("ReleaseLease(other owner) error = %v", err)
	}
	stillLeased, err := store.GetTask(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetTask() after other release error = %v", err)
	}
	if stillLeased.LeaseOwner != "worker-a" {
		t.Fatalf("lease owner = %q, want worker-a", stillLeased.LeaseOwner)
	}
	if err := store.ReleaseLease(t.Context(), created.ID, "worker-a"); err != nil {
		t.Fatalf("ReleaseLease(owner) error = %v", err)
	}
	released, err := store.GetTask(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetTask() after release error = %v", err)
	}
	if released.LeaseOwner != "" || released.LeaseExpiresAt != nil || released.HeartbeatAt != nil {
		t.Fatalf("released lease = %#v", released)
	}
}

func TestStoreFindsExpiredActiveLease(t *testing.T) {
	store := openTestStore(t)
	created := createTestTask(t, store)
	if err := store.Transition(t.Context(), created.ID, task.StatusPlanning, "", ""); err != nil {
		t.Fatalf("Transition(planning) error = %v", err)
	}
	if err := store.AcquireLease(t.Context(), created.ID, "dead-worker", time.Minute); err != nil {
		t.Fatalf("AcquireLease() error = %v", err)
	}
	past := formatTime(time.Now().UTC().Add(-time.Minute))
	if _, err := store.db.ExecContext(
		t.Context(),
		"UPDATE tasks SET lease_expires_at = ? WHERE id = ?",
		past,
		created.ID,
	); err != nil {
		t.Fatalf("expiring lease: %v", err)
	}

	recoverable, err := store.RecoverableTasks(t.Context(), time.Now().UTC(), 10)
	if err != nil {
		t.Fatalf("RecoverableTasks() error = %v", err)
	}
	if len(recoverable) != 1 || recoverable[0].ID != created.ID {
		t.Fatalf("RecoverableTasks() = %#v", recoverable)
	}
}

func TestStorePauseAndNewAttempt(t *testing.T) {
	store := openTestStore(t)
	created := createTestTask(t, store)
	if err := store.Transition(t.Context(), created.ID, task.StatusPlanning, "", ""); err != nil {
		t.Fatalf("Transition(planning) error = %v", err)
	}
	if err := store.Transition(t.Context(), created.ID, task.StatusRunning, "", ""); err != nil {
		t.Fatalf("Transition(running) error = %v", err)
	}

	paused, err := store.PauseTask(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("PauseTask() error = %v", err)
	}
	if paused.Status != task.StatusWaitingInput || paused.PausedFrom != task.StatusRunning {
		t.Fatalf("PauseTask() = status %q, paused from %q", paused.Status, paused.PausedFrom)
	}
	resumed, err := store.CreateAttempt(t.Context(), created.ID, "resume")
	if err != nil {
		t.Fatalf("CreateAttempt() error = %v", err)
	}
	if resumed.Status != task.StatusCreated || resumed.ActiveAttemptID == created.ActiveAttemptID {
		t.Fatalf("CreateAttempt() = %#v", resumed)
	}

	var openAttempts int
	if err := store.db.QueryRowContext(
		t.Context(),
		"SELECT COUNT(*) FROM attempts WHERE task_id = ? AND finished_at IS NULL",
		created.ID,
	).Scan(&openAttempts); err != nil {
		t.Fatalf("counting active attempts: %v", err)
	}
	if openAttempts != 1 {
		t.Fatalf("active attempts = %d, want 1", openAttempts)
	}
}

func TestStoreNewAttemptInheritsContextAndRemapsSummaryReferences(t *testing.T) {
	store := openTestStore(t)
	created := createTestTask(t, store)
	builder, err := contextbuilder.New(store, contextbuilder.Config{})
	if err != nil {
		t.Fatalf("contextbuilder.New() error = %v", err)
	}
	if _, _, err := builder.Initialize(t.Context(), created, "system prompt"); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	assistant, err := builder.Append(t.Context(), created, model.Message{
		Role: model.RoleAssistant,
		Content: []model.ContentBlock{{
			Kind: model.ContentText,
			Text: "prior evidence",
		}},
	}, contextbuilder.TrustModel, contextbuilder.SourceModel, "")
	if err != nil {
		t.Fatalf("Append(assistant) error = %v", err)
	}
	encodedRef, err := json.Marshal([]string{assistant.ID})
	if err != nil {
		t.Fatalf("Marshal(summary ref) error = %v", err)
	}
	if _, err := builder.Append(t.Context(), created, model.Message{
		Role: model.RoleAssistant,
		Content: []model.ContentBlock{{
			Kind: model.ContentReasoningSummary,
			Text: "summary",
		}},
	}, contextbuilder.TrustModel, contextbuilder.SourceSummary, string(encodedRef)); err != nil {
		t.Fatalf("Append(summary) error = %v", err)
	}
	pluginRef, _ := contextbuilder.PluginSourceRef("dev.kern.go@0.1.0", executionphase.Prepare)
	callRef, _ := contextbuilder.PhaseSourceRef("request-prepare", executionphase.Prepare)
	resultRef, _ := contextbuilder.PhaseSourceRef("call-prepare", executionphase.Prepare)
	if _, err := builder.Append(t.Context(), created, model.Message{
		Role:    model.RoleAssistant,
		Content: []model.ContentBlock{{Kind: model.ContentText, Text: "phase plugin resource"}},
	}, contextbuilder.TrustPluginUntrusted, contextbuilder.SourcePlugin, pluginRef); err != nil {
		t.Fatalf("Append(plugin) error = %v", err)
	}
	if _, err := builder.Append(t.Context(), created, model.Message{
		Role: model.RoleAssistant,
		Content: []model.ContentBlock{{Kind: model.ContentToolCall, ToolCall: &model.ToolCall{
			ID: "call-prepare", Name: "inspect", Arguments: json.RawMessage(`{"path":"README.md"}`),
		}}},
	}, contextbuilder.TrustModel, contextbuilder.SourceModel, callRef); err != nil {
		t.Fatalf("Append(phase call) error = %v", err)
	}
	if _, err := builder.Append(t.Context(), created, model.Message{
		Role: model.RoleTool,
		Content: []model.ContentBlock{{Kind: model.ContentToolResult, ToolResult: &model.ToolResult{
			CallID: "call-prepare", Content: "phase evidence",
		}}},
	}, contextbuilder.TrustToolUntrusted, contextbuilder.SourceTool, resultRef); err != nil {
		t.Fatalf("Append(phase result) error = %v", err)
	}
	if err := store.Transition(t.Context(), created.ID, task.StatusPlanning, "", ""); err != nil {
		t.Fatalf("Transition(planning) error = %v", err)
	}
	paused, err := store.PauseTask(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("PauseTask() error = %v", err)
	}
	resumed, err := store.CreateAttempt(t.Context(), paused.ID, "resume with context")
	if err != nil {
		t.Fatalf("CreateAttempt() error = %v", err)
	}
	previous, err := store.ListContextMessages(t.Context(), created.ActiveAttemptID)
	if err != nil {
		t.Fatalf("ListContextMessages(previous) error = %v", err)
	}
	inherited, err := store.ListContextMessages(t.Context(), resumed.ActiveAttemptID)
	if err != nil {
		t.Fatalf("ListContextMessages(inherited) error = %v", err)
	}
	if len(previous) != 7 || len(inherited) != 4 {
		t.Fatalf("inherited records = %d, previous = %d", len(inherited), len(previous))
	}
	for index := range inherited {
		if inherited[index].ID == previous[index].ID || inherited[index].AttemptID != resumed.ActiveAttemptID {
			t.Fatalf("record %d was not cloned: previous=%#v inherited=%#v", index, previous[index], inherited[index])
		}
	}
	var summaryRefs []string
	if err := json.Unmarshal([]byte(inherited[3].SourceRef), &summaryRefs); err != nil {
		t.Fatalf("Unmarshal(inherited summary refs) error = %v", err)
	}
	if len(summaryRefs) != 1 || summaryRefs[0] != inherited[2].ID {
		t.Fatalf("inherited summary refs = %#v, want %q", summaryRefs, inherited[2].ID)
	}
}

func TestStoreCheckpointSequenceIsPerAttempt(t *testing.T) {
	store := openTestStore(t)
	created := createTestTask(t, store)
	first, err := store.SaveCheckpoint(t.Context(), created.ID, "planned", map[string]int{"step": 1})
	if err != nil {
		t.Fatalf("SaveCheckpoint(first) error = %v", err)
	}
	second, err := store.SaveCheckpoint(t.Context(), created.ID, "processed", map[string]int{"step": 2})
	if err != nil {
		t.Fatalf("SaveCheckpoint(second) error = %v", err)
	}
	if first.Ordinal != 1 || second.Ordinal != 2 {
		t.Fatalf("checkpoint ordinals = %d, %d", first.Ordinal, second.Ordinal)
	}
	latest, err := store.LatestCheckpoint(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("LatestCheckpoint() error = %v", err)
	}
	if latest.ID != second.ID || string(latest.State) != `{"step":2}` {
		t.Fatalf("LatestCheckpoint() = %#v", latest)
	}

	if err := store.Transition(t.Context(), created.ID, task.StatusPlanning, "", ""); err != nil {
		t.Fatalf("Transition(planning) error = %v", err)
	}
	paused, err := store.PauseTask(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("PauseTask() error = %v", err)
	}
	if _, err := store.CreateAttempt(t.Context(), paused.ID, "resume"); err != nil {
		t.Fatalf("CreateAttempt() error = %v", err)
	}
	newAttemptCheckpoint, err := store.SaveCheckpoint(
		t.Context(),
		created.ID,
		"new attempt",
		map[string]int{"step": 1},
	)
	if err != nil {
		t.Fatalf("SaveCheckpoint(new attempt) error = %v", err)
	}
	if newAttemptCheckpoint.Ordinal != 1 {
		t.Fatalf("new attempt checkpoint ordinal = %d, want 1", newAttemptCheckpoint.Ordinal)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return store
}

func createTestTask(t *testing.T, store *Store) task.Task {
	t.Helper()
	created, err := store.CreateTask(t.Context(), "test task", "exercise reliability")
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	return created
}
