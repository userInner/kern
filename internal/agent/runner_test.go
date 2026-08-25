package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/userInner/kern/internal/artifact"
	"github.com/userInner/kern/internal/contextbuilder"
	"github.com/userInner/kern/internal/executionphase"
	"github.com/userInner/kern/internal/model"
	"github.com/userInner/kern/internal/operation"
	"github.com/userInner/kern/internal/store/sqlite"
	"github.com/userInner/kern/internal/task"
	"github.com/userInner/kern/internal/tool"
)

func TestRunnerExecutesReadToolAndContinues(t *testing.T) {
	store, item := newRunnerStore(t)
	generator := &sequenceGenerator{responses: []model.Response{
		{
			Message: model.Message{
				Role: model.RoleAssistant,
				Content: []model.ContentBlock{{
					Kind: model.ContentToolCall,
					ToolCall: &model.ToolCall{
						ID:        "call-1",
						Name:      "inspect",
						Arguments: json.RawMessage(`{"path":"README.md"}`),
					},
				}},
			},
			Usage: model.Usage{InputTokens: 10, OutputTokens: 3},
		},
		{
			Message: model.Message{
				Role: model.RoleAssistant,
				Content: []model.ContentBlock{{
					Kind: model.ContentText,
					Text: "README inspected with evidence.",
				}},
			},
			Usage: model.Usage{InputTokens: 15, OutputTokens: 6},
		},
	}}
	tools := &fakeTools{effect: operation.EffectRead, result: tool.Result{Content: "# Kern"}}
	runner, err := New(generator, store, store, tools, Config{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := runner.Process(t.Context(), item)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if result != "README inspected with evidence." {
		t.Fatalf("Process() = %q", result)
	}
	if tools.calls != 1 {
		t.Fatalf("tool calls = %d, want 1", tools.calls)
	}
	events, err := store.EventsAfter(t.Context(), item.ID, 0, 100)
	if err != nil {
		t.Fatalf("EventsAfter() error = %v", err)
	}
	wantTypes := map[string]bool{
		"model.usage":        false,
		"operation.proposed": false,
		"operation.started":  false,
		"operation.output":   false,
	}
	for _, event := range events {
		if _, ok := wantTypes[event.Type]; ok {
			wantTypes[event.Type] = true
		}
	}
	for eventType, found := range wantTypes {
		if !found {
			t.Errorf("event %q was not persisted", eventType)
		}
	}
}

func TestRunnerScopesPluginContextAndToolsByExecutionPhase(t *testing.T) {
	store, item := newRunnerStore(t)
	contexts, err := contextbuilder.New(store, contextbuilder.Config{})
	if err != nil {
		t.Fatalf("contextbuilder.New() error = %v", err)
	}
	if _, _, err := contexts.Initialize(t.Context(), item, SystemPrompt()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	for _, current := range executionphase.All() {
		sourceRef, err := contextbuilder.PluginSourceRef("dev.kern.test@0.1.0", current)
		if err != nil {
			t.Fatalf("PluginSourceRef(%s) error = %v", current, err)
		}
		if _, err := contexts.Append(
			t.Context(),
			item,
			model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{
				Kind: model.ContentText,
				Text: strings.ToUpper(string(current)) + "_ONLY_PLUGIN_RESOURCE",
			}}},
			contextbuilder.TrustPluginUntrusted,
			contextbuilder.SourcePlugin,
			sourceRef,
		); err != nil {
			t.Fatalf("Append(%s) error = %v", current, err)
		}
	}
	generator := &sequenceGenerator{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{
			Kind: model.ContentText, Text: "Prepared a bounded plan.",
		}}}},
		{Message: model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{
			Kind: model.ContentToolCall,
			ToolCall: &model.ToolCall{
				ID: "inspect-1", Name: "inspect", Arguments: json.RawMessage(`{"path":"README.md"}`),
			},
		}}}},
		{Message: model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{
			Kind: model.ContentText, Text: "Execution is complete.",
		}}}},
		{Message: model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{
			Kind: model.ContentText, Text: "Verified final answer.",
		}}}},
	}}
	tools := &fakeTools{effect: operation.EffectRead, result: tool.Result{Content: "evidence"}}
	runner, err := New(generator, store, store, tools, Config{Context: contexts, MaxTurns: 6})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := runner.Process(t.Context(), item)
	if err != nil || result != "Verified final answer." {
		t.Fatalf("Process() = %q, %v", result, err)
	}
	if len(generator.requests) != 4 || tools.calls != 1 {
		t.Fatalf("requests=%d tool calls=%d", len(generator.requests), tools.calls)
	}
	wantPhases := []executionphase.Phase{
		executionphase.Prepare,
		executionphase.Execute,
		executionphase.Execute,
		executionphase.Verify,
	}
	for index, request := range generator.requests {
		if request.Metadata["kern.execution_phase"] != string(wantPhases[index]) {
			t.Errorf("request[%d] metadata = %#v", index, request.Metadata)
		}
		content, _ := json.Marshal(request.Messages)
		wantResource := strings.ToUpper(string(wantPhases[index])) + "_ONLY_PLUGIN_RESOURCE"
		if !strings.Contains(string(content), wantResource) {
			t.Errorf("request[%d] omits %s: %s", index, wantResource, content)
		}
		for _, other := range executionphase.All() {
			otherResource := strings.ToUpper(string(other)) + "_ONLY_PLUGIN_RESOURCE"
			if other != wantPhases[index] && strings.Contains(string(content), otherResource) {
				t.Errorf("request[%d] leaked %s: %s", index, other, content)
			}
		}
		toolNames := definitionNames(request.Tools)
		if wantPhases[index] == executionphase.Execute {
			if !toolNames["inspect"] || !toolNames["change"] {
				t.Errorf("execute tools = %#v", request.Tools)
			}
		} else if !toolNames["inspect"] || toolNames["change"] {
			t.Errorf("%s tools = %#v", wantPhases[index], request.Tools)
		}
	}

	events, err := store.EventsAfter(t.Context(), item.ID, 0, 200)
	if err != nil {
		t.Fatalf("EventsAfter() error = %v", err)
	}
	var transitions int
	for _, event := range events {
		if event.Type == "agent.phase_changed" {
			transitions++
		}
	}
	if transitions != 2 {
		t.Fatalf("phase transitions = %d, want 2", transitions)
	}
}

func TestRunnerDoesNotExecuteToolHiddenFromCurrentPhase(t *testing.T) {
	store, item := newRunnerStore(t)
	contexts, err := contextbuilder.New(store, contextbuilder.Config{})
	if err != nil {
		t.Fatalf("contextbuilder.New() error = %v", err)
	}
	if _, _, err := contexts.Initialize(t.Context(), item, SystemPrompt()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	for _, current := range executionphase.All() {
		sourceRef, _ := contextbuilder.PluginSourceRef("dev.kern.test@0.1.0", current)
		if _, err := contexts.Append(
			t.Context(),
			item,
			model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{
				Kind: model.ContentText, Text: "plugin " + string(current),
			}}},
			contextbuilder.TrustPluginUntrusted,
			contextbuilder.SourcePlugin,
			sourceRef,
		); err != nil {
			t.Fatalf("Append(%s) error = %v", current, err)
		}
	}
	generator := &sequenceGenerator{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{
			Kind: model.ContentToolCall,
			ToolCall: &model.ToolCall{
				ID: "hidden-change", Name: "change", Arguments: json.RawMessage(`{"path":"note.txt","content":"unsafe"}`),
			},
		}}}},
		{Message: model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{
			Kind: model.ContentText, Text: "No hidden tool was executed.",
		}}}},
		{Message: model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{
			Kind: model.ContentText, Text: "Verified safe result.",
		}}}},
	}}
	tools := &fakeTools{effect: operation.EffectLocalWrite}
	runner, err := New(generator, store, store, tools, Config{Context: contexts, MaxTurns: 5})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := runner.Process(t.Context(), item)
	if err != nil || result != "Verified safe result." {
		t.Fatalf("Process() = %q, %v", result, err)
	}
	if tools.calls != 0 {
		t.Fatalf("hidden tool executions = %d, want 0", tools.calls)
	}
	if len(generator.requests) != 3 ||
		generator.requests[0].Metadata["kern.execution_phase"] != "prepare" ||
		generator.requests[1].Metadata["kern.execution_phase"] != "execute" ||
		generator.requests[2].Metadata["kern.execution_phase"] != "verify" {
		t.Fatalf("phase requests = %#v", generator.requests)
	}
	secondRequest, _ := json.Marshal(generator.requests[1].Messages)
	if !strings.Contains(string(secondRequest), "not available during the prepare phase") {
		t.Fatalf("execute phase did not receive the rejected tool result: %s", secondRequest)
	}
	operations, err := store.ListAttemptOperations(t.Context(), item.ID, item.ActiveAttemptID)
	if err != nil || len(operations) != 0 {
		t.Fatalf("hidden tool created operations = %#v, %v", operations, err)
	}
}

func TestNewRejectsNegativeBudgets(t *testing.T) {
	store, _ := newRunnerStore(t)
	tests := []struct {
		name   string
		config Config
	}{
		{name: "turns", config: Config{MaxTurns: -1}},
		{name: "tool calls", config: Config{MaxToolCalls: -1}},
		{name: "tokens", config: Config{MaxTokens: -1}},
		{name: "cost", config: Config{MaxCostMicros: -1}},
		{name: "duration", config: Config{MaxDuration: -time.Second}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(&sequenceGenerator{}, store, store, nil, test.config); err == nil {
				t.Fatal("New() error = nil")
			}
		})
	}
}

func TestNormalizeConfigReturnsEffectiveFiniteDefaults(t *testing.T) {
	config, err := NormalizeConfig(Config{})
	if err != nil {
		t.Fatalf("NormalizeConfig() error = %v", err)
	}
	if config.MaxTurns <= 0 || config.MaxToolCalls <= 0 || config.MaxTokens <= 0 ||
		config.MaxCostMicros <= 0 || config.MaxDuration <= 0 || config.MaxRetries <= 0 {
		t.Fatalf("NormalizeConfig() = %#v, want finite defaults", config)
	}

	explicit := Config{
		MaxTurns:      7,
		MaxToolCalls:  11,
		MaxTokens:     13,
		MaxCostMicros: 17,
		MaxDuration:   19 * time.Second,
		MaxRetries:    23,
	}
	normalized, err := NormalizeConfig(explicit)
	if err != nil {
		t.Fatalf("NormalizeConfig(explicit) error = %v", err)
	}
	if normalized.MaxTurns != explicit.MaxTurns ||
		normalized.MaxToolCalls != explicit.MaxToolCalls ||
		normalized.MaxTokens != explicit.MaxTokens ||
		normalized.MaxCostMicros != explicit.MaxCostMicros ||
		normalized.MaxDuration != explicit.MaxDuration ||
		normalized.MaxRetries != explicit.MaxRetries {
		t.Fatalf("NormalizeConfig(explicit) = %#v, want %#v", normalized, explicit)
	}
}

func TestRunnerNeverExecutesUnapprovedSideEffect(t *testing.T) {
	store, item := newRunnerStore(t)
	generator := &sequenceGenerator{responses: []model.Response{
		{
			Message: model.Message{
				Role: model.RoleAssistant,
				Content: []model.ContentBlock{{
					Kind: model.ContentToolCall,
					ToolCall: &model.ToolCall{
						ID:        "call-1",
						Name:      "change",
						Arguments: json.RawMessage(`{"path":"README.md","content":"changed"}`),
					},
				}},
			},
		},
		{
			Message: model.Message{
				Role: model.RoleAssistant,
				Content: []model.ContentBlock{{
					Kind: model.ContentText,
					Text: "The change was not executed because approval is required.",
				}},
			},
		},
	}}
	tools := &fakeTools{effect: operation.EffectLocalWrite}
	runner, err := New(generator, store, store, tools, Config{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := runner.Process(t.Context(), item)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if result == "" || tools.calls != 0 {
		t.Fatalf("result = %q, tool calls = %d", result, tools.calls)
	}
}

func TestRunnerPersistsBoundedStreamingDeltas(t *testing.T) {
	store, item := newRunnerStore(t)
	generator := &streamGenerator{events: []model.StreamEvent{
		{Kind: model.StreamEventReasoningDelta, Text: "checking evidence"},
		{Kind: model.StreamEventTextDelta, Text: "streamed answer"},
		{Kind: model.StreamEventUsage, Usage: &model.Usage{InputTokens: 4, OutputTokens: 2}},
		{Kind: model.StreamEventCompleted, FinishReason: "stop"},
	}}
	runner, err := New(generator, store, store, nil, Config{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := runner.Process(t.Context(), item)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if result != "streamed answer" {
		t.Fatalf("Process() = %q", result)
	}
	events, err := store.EventsAfter(t.Context(), item.ID, 0, 100)
	if err != nil {
		t.Fatalf("EventsAfter() error = %v", err)
	}
	var hasTextDelta, hasReasoningDelta bool
	for _, event := range events {
		hasTextDelta = hasTextDelta || event.Type == "message.delta"
		hasReasoningDelta = hasReasoningDelta || event.Type == "reasoning.delta"
	}
	if !hasTextDelta || !hasReasoningDelta {
		t.Fatalf("stream events missing: text=%v reasoning=%v", hasTextDelta, hasReasoningDelta)
	}
}

func TestRunnerPersistsToolOutputAsArtifact(t *testing.T) {
	store, item := newRunnerStore(t)
	artifacts, err := artifact.Open(filepath.Join(t.TempDir(), "artifacts"), store)
	if err != nil {
		t.Fatalf("artifact.Open() error = %v", err)
	}
	generator := &sequenceGenerator{responses: []model.Response{
		{
			Message: model.Message{
				Role: model.RoleAssistant,
				Content: []model.ContentBlock{{
					Kind: model.ContentToolCall,
					ToolCall: &model.ToolCall{
						ID:        "call-evidence",
						Name:      "inspect",
						Arguments: json.RawMessage(`{"path":"README.md"}`),
					},
				}},
			},
		},
		{
			Message: model.Message{
				Role:    model.RoleAssistant,
				Content: []model.ContentBlock{{Kind: model.ContentText, Text: "Evidence saved."}},
			},
		},
	}}
	tools := &fakeTools{
		effect: operation.EffectRead,
		result: tool.Result{
			Content: `{"path":"README.md","content":"Kern"}`,
			Artifacts: []tool.OutputArtifact{{
				Name:      "README.md.diff",
				MediaType: "text/x-diff",
				Content:   []byte("--- a/README.md\n+++ b/README.md\n+Kern\n"),
			}},
		},
	}
	runner, err := New(generator, store, store, tools, Config{Artifacts: artifacts})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := runner.Process(t.Context(), item); err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	items, err := artifacts.List(t.Context(), item.ID)
	if err != nil || len(items) != 2 {
		t.Fatalf("Artifacts.List() = %#v, %v", items, err)
	}
	if items[0].SourceOperationID == "" || items[0].MediaType != "application/json" ||
		items[1].SourceOperationID != items[0].SourceOperationID || items[1].MediaType != "text/x-diff" {
		t.Fatalf("artifact metadata = %#v", items)
	}
	_, file, err := artifacts.OpenContent(t.Context(), item.ID, items[0].ID)
	if err != nil {
		t.Fatalf("OpenContent() error = %v", err)
	}
	data, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || !strings.Contains(string(data), "README.md") {
		t.Fatalf("artifact content = %q, read=%v close=%v", data, readErr, closeErr)
	}
	var referenced bool
	events, err := store.EventsAfter(t.Context(), item.ID, 0, 100)
	if err != nil {
		t.Fatalf("EventsAfter() error = %v", err)
	}
	for _, event := range events {
		if event.Type == "operation.output" && strings.Contains(string(event.Payload), "artifact:") {
			referenced = true
		}
	}
	if !referenced {
		t.Fatal("operation output did not reference its artifact")
	}
}

func TestRunnerEnforcesTokenBudget(t *testing.T) {
	store, item := newRunnerStore(t)
	generator := &sequenceGenerator{responses: []model.Response{{
		Message: model.Message{
			Role:    model.RoleAssistant,
			Content: []model.ContentBlock{{Kind: model.ContentText, Text: "too expensive"}},
		},
		Usage: model.Usage{InputTokens: 8, OutputTokens: 8},
	}}}
	runner, err := New(generator, store, store, nil, Config{MaxTokens: 10})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = runner.Process(t.Context(), item)
	if !errors.Is(err, ErrTokenBudget) {
		t.Fatalf("Process() error = %v, want ErrTokenBudget", err)
	}
	assertBudgetEvent(t, store, item, "tokens")
}

func TestRunnerEnforcesCostBudget(t *testing.T) {
	store, item := newRunnerStore(t)
	generator := &sequenceGenerator{responses: []model.Response{{
		Message: model.Message{
			Role:    model.RoleAssistant,
			Content: []model.ContentBlock{{Kind: model.ContentText, Text: "too expensive"}},
		},
		Usage: model.Usage{InputTokens: 1, OutputTokens: 1, CostMicros: 11},
	}}}
	runner, err := New(generator, store, store, nil, Config{MaxCostMicros: 10})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = runner.Process(t.Context(), item)
	if !errors.Is(err, ErrCostBudget) {
		t.Fatalf("Process() error = %v, want ErrCostBudget", err)
	}
	assertBudgetEvent(t, store, item, "cost_micros")
}

func TestRunnerClassifiesDurationBudget(t *testing.T) {
	store, item := newRunnerStore(t)
	runner, err := New(blockingGenerator{}, store, store, nil, Config{MaxDuration: time.Millisecond})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = runner.Process(t.Context(), item)
	if !errors.Is(err, ErrDurationBudget) {
		t.Fatalf("Process() error = %v, want ErrDurationBudget", err)
	}
	assertBudgetEvent(t, store, item, "duration_ms")
}

func TestRunnerDoesNotMisclassifyParentDeadline(t *testing.T) {
	store, item := newRunnerStore(t)
	runner, err := New(blockingGenerator{}, store, store, nil, Config{MaxDuration: time.Hour})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	_, err = runner.Process(ctx, item)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrDurationBudget) {
		t.Fatalf("Process() error = %v, want parent deadline only", err)
	}
}

func TestRunnerRejectsNegativeUsage(t *testing.T) {
	store, item := newRunnerStore(t)
	generator := &sequenceGenerator{responses: []model.Response{{
		Usage: model.Usage{InputTokens: -1},
	}}}
	runner, err := New(generator, store, store, nil, Config{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = runner.Process(t.Context(), item)
	if err == nil || !strings.Contains(err.Error(), "negative usage") {
		t.Fatalf("Process() error = %v", err)
	}
}

func TestAccumulateUsageTreatsIntegerOverflowAsBudgetExhaustion(t *testing.T) {
	tests := []struct {
		name       string
		tokens     int
		costMicros int64
		usage      model.Usage
		want       string
	}{
		{
			name:   "tokens",
			tokens: math.MaxInt - 1,
			usage:  model.Usage{InputTokens: 2},
			want:   "tokens",
		},
		{
			name:       "cost",
			costMicros: math.MaxInt64 - 1,
			usage:      model.Usage{CostMicros: 2},
			want:       "cost_micros",
		},
	}
	config := Config{MaxTokens: math.MaxInt, MaxCostMicros: math.MaxInt64}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, got, err := accumulateUsage(test.tokens, test.costMicros, test.usage, config)
			if err != nil {
				t.Fatalf("accumulateUsage() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("accumulateUsage() exceeded = %q, want %q", got, test.want)
			}
		})
	}
}

type sequenceGenerator struct {
	responses []model.Response
	requests  []model.Request
	index     int
}

func (g *sequenceGenerator) Generate(_ context.Context, request model.Request) (model.Response, error) {
	g.requests = append(g.requests, request)
	if g.index >= len(g.responses) {
		return model.Response{}, errors.New("test: response sequence exhausted")
	}
	response := g.responses[g.index]
	g.index++
	return response, nil
}

type streamGenerator struct {
	events []model.StreamEvent
}

type blockingGenerator struct{}

func (blockingGenerator) Generate(ctx context.Context, _ model.Request) (model.Response, error) {
	<-ctx.Done()
	return model.Response{}, ctx.Err()
}

func (g *streamGenerator) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, errors.New("test: non-streaming path used")
}

func (g *streamGenerator) Stream(context.Context, model.Request) (model.EventStream, error) {
	return &fakeStream{events: g.events}, nil
}

type fakeStream struct {
	events []model.StreamEvent
	index  int
}

func (s *fakeStream) Next(context.Context) (model.StreamEvent, error) {
	if s.index >= len(s.events) {
		return model.StreamEvent{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *fakeStream) Close() error { return nil }

type fakeTools struct {
	effect operation.Effect
	result tool.Result
	err    error
	calls  int
}

func (t *fakeTools) Definitions() []model.ToolDefinition {
	return []model.ToolDefinition{{
		Name:        "inspect",
		Description: "inspect",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, {
		Name:        "change",
		Description: "change",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}}
}

func (t *fakeTools) Effect(string, json.RawMessage) (operation.Effect, error) {
	return t.effect, nil
}

func (t *fakeTools) Execute(context.Context, string, json.RawMessage) (tool.Result, error) {
	t.calls++
	return t.result, t.err
}

func newRunnerStore(t *testing.T) (*sqlite.Store, task.Task) {
	t.Helper()
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	item, err := store.CreateTask(t.Context(), "agent test", "use tools safely")
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	return store, item
}

func assertBudgetEvent(t *testing.T, store *sqlite.Store, item task.Task, dimension string) {
	t.Helper()
	events, err := store.EventsAfter(t.Context(), item.ID, 0, 100)
	if err != nil {
		t.Fatalf("EventsAfter() error = %v", err)
	}
	for _, event := range events {
		if event.Type != "task.budget_exhausted" {
			continue
		}
		var payload struct {
			Dimension string `json:"dimension"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("budget event payload = %s: %v", event.Payload, err)
		}
		if payload.Dimension == dimension {
			return
		}
	}
	t.Fatalf("task.budget_exhausted event for %q not found", dimension)
}
