package sqlite

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/userInner/kern/internal/modelconfig"
)

func TestRecordModelSelectionIsSecretFreeAndIdempotent(t *testing.T) {
	store := newModelConfigTestStore(t)
	config, err := store.CreateModelConfig(t.Context(), modelconfig.Draft{
		Name:      "Remote",
		Provider:  modelconfig.ProviderOpenAICompatible,
		BaseURL:   "https://models.example.com",
		Model:     "agent-model",
		SecretRef: "env:SECRET_MODEL_KEY",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("CreateModelConfig() error = %v", err)
	}
	item, err := store.CreateTaskWithModel(t.Context(), "task", "run safely", config.ID)
	if err != nil {
		t.Fatalf("CreateTaskWithModel() error = %v", err)
	}
	parameters := json.RawMessage(`{ "tool_choice": "auto" }`)
	selection, created, err := store.RecordModelSelection(t.Context(), item, config, parameters)
	if err != nil || !created {
		t.Fatalf("RecordModelSelection() = %#v, %v, %v", selection, created, err)
	}
	if string(selection.Parameters) != `{"tool_choice":"auto"}` {
		t.Fatalf("parameters = %s", selection.Parameters)
	}
	selection, created, err = store.RecordModelSelection(t.Context(), item, config, parameters)
	if err != nil || created {
		t.Fatalf("RecordModelSelection(second) = %#v, %v, %v", selection, created, err)
	}
	encoded, err := json.Marshal(selection)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if strings.Contains(string(encoded), "SECRET_MODEL_KEY") {
		t.Fatalf("selection leaked secret reference: %s", encoded)
	}
}
