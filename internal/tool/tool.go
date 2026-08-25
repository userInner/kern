// Package tool defines provider-neutral executable tool contracts and a registry.
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/userInner/kern/internal/executionphase"
	"github.com/userInner/kern/internal/model"
	"github.com/userInner/kern/internal/operation"
)

var (
	ErrNotFound     = errors.New("tool: not found")
	ErrDuplicate    = errors.New("tool: duplicate name")
	ErrInvalidInput = errors.New("tool: invalid input")
)

// RecoveryDisposition is a deterministic conclusion about an interrupted
// side effect based on current external state and metadata persisted before it
// started.
type RecoveryDisposition string

const (
	RecoveryUnknown     RecoveryDisposition = ""
	RecoverySucceeded   RecoveryDisposition = "succeeded"
	RecoveryNotExecuted RecoveryDisposition = "not_executed"
	RecoveryConflict    RecoveryDisposition = "conflict"
)

// Reconciliation contains no user content; it records the observable fact
// that allowed or prevented automatic recovery.
type Reconciliation struct {
	Disposition RecoveryDisposition `json:"disposition"`
	Summary     string              `json:"summary"`
}

// Result is the bounded output of a tool call.
type Result struct {
	Content   string
	OutputRef string
	ExitCode  *int
	Artifacts []OutputArtifact
}

// OutputArtifact is immutable evidence produced alongside a tool result.
type OutputArtifact struct {
	Name      string
	MediaType string
	Content   []byte
}

type taskScopeKey struct{}

// TaskScope identifies the Attempt allowed to access task-scoped capabilities.
type TaskScope struct {
	TaskID    string
	AttemptID string
	Phase     executionphase.Phase
}

// WithTaskScope binds a tool invocation to its durable task Attempt.
func WithTaskScope(ctx context.Context, taskID, attemptID string) context.Context {
	return WithTaskScopePhase(ctx, taskID, attemptID, executionphase.Execute)
}

// WithTaskScopePhase also binds the Core-owned model stage. Tool handlers may
// use it for defense-in-depth, but cannot alter it.
func WithTaskScopePhase(
	ctx context.Context,
	taskID string,
	attemptID string,
	current executionphase.Phase,
) context.Context {
	return context.WithValue(ctx, taskScopeKey{}, TaskScope{
		TaskID: taskID, AttemptID: attemptID, Phase: current,
	})
}

// TaskScopeFromContext returns the Core-bound invocation scope.
func TaskScopeFromContext(ctx context.Context) (TaskScope, bool) {
	scope, ok := ctx.Value(taskScopeKey{}).(TaskScope)
	return scope, ok && scope.TaskID != "" && scope.AttemptID != "" && scope.Phase.Valid()
}

// Handler implements one named tool.
type Handler interface {
	Definition() model.ToolDefinition
	Effect(input json.RawMessage) (operation.Effect, error)
	Execute(ctx context.Context, input json.RawMessage) (Result, error)
}

// RecoveryHandler is implemented only by tools that can persist enough intent
// before execution to reconcile an interrupted call without guessing.
type RecoveryHandler interface {
	PrepareRecovery(ctx context.Context, input json.RawMessage) (json.RawMessage, error)
	Reconcile(
		ctx context.Context,
		input json.RawMessage,
		metadata json.RawMessage,
	) (Reconciliation, error)
}

// Registry resolves the stable tool surface exposed to the model.
type Registry struct {
	handlers map[string]Handler
}

// NewRegistry validates and registers handlers.
func NewRegistry(handlers ...Handler) (*Registry, error) {
	registry := &Registry{handlers: make(map[string]Handler, len(handlers))}
	for _, handler := range handlers {
		if handler == nil {
			return nil, errors.New("tool: nil handler")
		}
		definition := handler.Definition()
		if definition.Name == "" || !json.Valid(definition.InputSchema) {
			return nil, errors.New("tool: invalid definition")
		}
		if _, exists := registry.handlers[definition.Name]; exists {
			return nil, fmt.Errorf("%w: %s", ErrDuplicate, definition.Name)
		}
		registry.handlers[definition.Name] = handler
	}
	return registry, nil
}

// Definitions returns a deterministic model-facing tool list.
func (r *Registry) Definitions() []model.ToolDefinition {
	names := make([]string, 0, len(r.handlers))
	for name := range r.handlers {
		names = append(names, name)
	}
	sort.Strings(names)
	definitions := make([]model.ToolDefinition, 0, len(names))
	for _, name := range names {
		definitions = append(definitions, r.handlers[name].Definition())
	}
	return definitions
}

// Effect classifies a call before any side effect occurs.
func (r *Registry) Effect(name string, input json.RawMessage) (operation.Effect, error) {
	handler, ok := r.handlers[name]
	if !ok {
		return operation.EffectUnknown, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return handler.Effect(input)
}

// Execute invokes a registered handler.
func (r *Registry) Execute(
	ctx context.Context,
	name string,
	input json.RawMessage,
) (Result, error) {
	handler, ok := r.handlers[name]
	if !ok {
		return Result{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return handler.Execute(ctx, input)
}

// PrepareRecovery asks a tool for metadata that must be persisted before its
// side effect begins. supported=false means Core must use conservative manual
// recovery for this tool.
func (r *Registry) PrepareRecovery(
	ctx context.Context,
	name string,
	input json.RawMessage,
) (metadata json.RawMessage, supported bool, err error) {
	handler, ok := r.handlers[name]
	if !ok {
		return nil, false, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	recoverable, ok := handler.(RecoveryHandler)
	if !ok {
		return nil, false, nil
	}
	metadata, err = recoverable.PrepareRecovery(ctx, input)
	return metadata, true, err
}

// Reconcile compares durable pre-execution metadata with the tool's current
// external state.
func (r *Registry) Reconcile(
	ctx context.Context,
	name string,
	input json.RawMessage,
	metadata json.RawMessage,
) (Reconciliation, bool, error) {
	handler, ok := r.handlers[name]
	if !ok {
		return Reconciliation{}, false, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	recoverable, ok := handler.(RecoveryHandler)
	if !ok {
		return Reconciliation{}, false, nil
	}
	result, err := recoverable.Reconcile(ctx, input, metadata)
	return result, true, err
}
