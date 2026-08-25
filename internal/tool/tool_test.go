package tool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/userInner/kern/internal/model"
	"github.com/userInner/kern/internal/operation"
)

func TestRegistryOrdersAndDispatchesHandlers(t *testing.T) {
	first := &fakeHandler{name: "zeta"}
	second := &fakeHandler{name: "alpha"}
	registry, err := NewRegistry(first, second)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	definitions := registry.Definitions()
	if len(definitions) != 2 || definitions[0].Name != "alpha" || definitions[1].Name != "zeta" {
		t.Fatalf("Definitions() = %#v", definitions)
	}
	if _, err := registry.Execute(t.Context(), "alpha", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if second.calls != 1 {
		t.Fatalf("handler calls = %d, want 1", second.calls)
	}
	if _, err := registry.Execute(t.Context(), "missing", json.RawMessage(`{}`)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Execute(missing) error = %v", err)
	}
}

func TestRegistryRejectsDuplicateNames(t *testing.T) {
	_, err := NewRegistry(&fakeHandler{name: "same"}, &fakeHandler{name: "same"})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("NewRegistry() error = %v, want ErrDuplicate", err)
	}
}

type fakeHandler struct {
	name  string
	calls int
}

func (h *fakeHandler) Definition() model.ToolDefinition {
	return model.ToolDefinition{Name: h.name, InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func (h *fakeHandler) Effect(json.RawMessage) (operation.Effect, error) {
	return operation.EffectRead, nil
}

func (h *fakeHandler) Execute(context.Context, json.RawMessage) (Result, error) {
	h.calls++
	return Result{Content: "ok"}, nil
}
