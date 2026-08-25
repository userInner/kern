package sqlite

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/userInner/kern/internal/modelconfig"
)

func TestModelConfigLifecycle(t *testing.T) {
	store := newModelConfigTestStore(t)
	first, err := store.CreateModelConfig(t.Context(), modelconfig.Draft{
		Name:      "Local Qwen",
		Provider:  modelconfig.ProviderOllama,
		BaseURL:   "http://127.0.0.1:11434",
		Model:     "qwen3",
		Enabled:   true,
		SecretRef: "keyring:first",
	})
	if err != nil {
		t.Fatalf("CreateModelConfig() error = %v", err)
	}
	if !first.IsDefault || !first.HasAPIKey {
		t.Fatalf("first config = %#v", first)
	}
	second, err := store.CreateModelConfig(t.Context(), modelconfig.Draft{
		Name:       "Remote",
		Provider:   modelconfig.ProviderOpenAICompatible,
		BaseURL:    "https://models.example.com",
		Model:      "reasoning-model",
		Enabled:    true,
		SetDefault: true,
	})
	if err != nil {
		t.Fatalf("CreateModelConfig(second) error = %v", err)
	}
	if !second.IsDefault {
		t.Fatalf("second config = %#v", second)
	}
	gotFirst, err := store.GetModelConfig(t.Context(), first.ID)
	if err != nil || gotFirst.IsDefault {
		t.Fatalf("GetModelConfig(first) = %#v, %v", gotFirst, err)
	}

	updated, err := store.UpdateModelConfig(t.Context(), first.ID, modelconfig.Draft{
		Name:       "Local Qwen Updated",
		Provider:   modelconfig.ProviderOllama,
		BaseURL:    "http://localhost:11434/",
		Model:      "qwen3.1",
		Enabled:    true,
		SecretRef:  first.SecretRef,
		SetDefault: true,
	})
	if err != nil {
		t.Fatalf("UpdateModelConfig() error = %v", err)
	}
	if !updated.IsDefault || updated.BaseURL != "http://localhost:11434" {
		t.Fatalf("updated config = %#v", updated)
	}

	configs, err := store.ListModelConfigs(t.Context())
	if err != nil || len(configs) != 2 || configs[0].ID != first.ID {
		t.Fatalf("ListModelConfigs() = %#v, %v", configs, err)
	}
	if err := store.DeleteModelConfig(t.Context(), first.ID); err != nil {
		t.Fatalf("DeleteModelConfig() error = %v", err)
	}
	promoted, err := store.DefaultModelConfig(t.Context())
	if err != nil || promoted.ID != second.ID {
		t.Fatalf("DefaultModelConfig() = %#v, %v", promoted, err)
	}
}

func TestModelConfigNameConflict(t *testing.T) {
	store := newModelConfigTestStore(t)
	draft := modelconfig.Draft{
		Name:     "Qwen",
		Provider: modelconfig.ProviderOllama,
		BaseURL:  "http://localhost:11434",
		Model:    "qwen3",
		Enabled:  true,
	}
	if _, err := store.CreateModelConfig(t.Context(), draft); err != nil {
		t.Fatalf("CreateModelConfig() error = %v", err)
	}
	draft.Name = "qWEN"
	if _, err := store.CreateModelConfig(t.Context(), draft); !errors.Is(err, modelconfig.ErrConflict) {
		t.Fatalf("CreateModelConfig(conflict) error = %v", err)
	}
}

func TestDefaultModelConfigNotFound(t *testing.T) {
	store := newModelConfigTestStore(t)
	if _, err := store.DefaultModelConfig(t.Context()); !errors.Is(err, modelconfig.ErrNotFound) {
		t.Fatalf("DefaultModelConfig() error = %v", err)
	}
}

func TestDisablingDefaultPromotesEnabledReplacement(t *testing.T) {
	store := newModelConfigTestStore(t)
	first, err := store.CreateModelConfig(t.Context(), modelconfig.Draft{
		Name:     "First",
		Provider: modelconfig.ProviderOllama,
		BaseURL:  "http://localhost:11434",
		Model:    "first",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("CreateModelConfig(first) error = %v", err)
	}
	second, err := store.CreateModelConfig(t.Context(), modelconfig.Draft{
		Name:     "Second",
		Provider: modelconfig.ProviderOllama,
		BaseURL:  "http://localhost:11434",
		Model:    "second",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("CreateModelConfig(second) error = %v", err)
	}
	if _, err := store.UpdateModelConfig(t.Context(), first.ID, modelconfig.Draft{
		Name:     first.Name,
		Provider: first.Provider,
		BaseURL:  first.BaseURL,
		Model:    first.Model,
		Enabled:  false,
	}); err != nil {
		t.Fatalf("UpdateModelConfig() error = %v", err)
	}
	got, err := store.DefaultModelConfig(t.Context())
	if err != nil || got.ID != second.ID {
		t.Fatalf("DefaultModelConfig() = %#v, %v", got, err)
	}
}

func newModelConfigTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
