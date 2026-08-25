package contextbuilder_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/userInner/kern/internal/contextbuilder"
	"github.com/userInner/kern/internal/executionphase"
	"github.com/userInner/kern/internal/model"
	"github.com/userInner/kern/internal/store/sqlite"
	"github.com/userInner/kern/internal/task"
)

func TestBuilderPersistsProvenanceWithoutLeakingContentToEvents(t *testing.T) {
	repository, item := openContextFixture(t)
	builder, err := contextbuilder.New(repository, contextbuilder.Config{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	messages, report, err := builder.Initialize(t.Context(), item, "SYSTEM SAFETY")
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if len(messages) != 2 || report.NeedsSummary {
		t.Fatalf("Initialize() = %#v, %#v", messages, report)
	}
	if _, err := builder.Append(
		t.Context(),
		item,
		model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{
			Kind: model.ContentText,
			Text: "TOP_SECRET_MODEL_CONTENT",
		}}},
		contextbuilder.TrustModel,
		contextbuilder.SourceModel,
		"request-1",
	); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	records, err := repository.ListContextMessages(t.Context(), item.ActiveAttemptID)
	if err != nil || len(records) != 3 {
		t.Fatalf("ListContextMessages() = %#v, %v", records, err)
	}
	if records[0].Trust != contextbuilder.TrustSystem ||
		records[1].Trust != contextbuilder.TrustUser ||
		records[2].Trust != contextbuilder.TrustModel {
		t.Fatalf("trust provenance = %#v", records)
	}
	events, err := repository.EventsAfter(t.Context(), item.ID, 0, 100)
	if err != nil {
		t.Fatalf("EventsAfter() error = %v", err)
	}
	for _, event := range events {
		if strings.Contains(string(event.Payload), "TOP_SECRET_MODEL_CONTENT") {
			t.Fatalf("event leaked message content: %s", event.Payload)
		}
	}
}

func TestBuilderHidesInactiveAndLegacyPluginRecordsBeforeBudgeting(t *testing.T) {
	repository, item := openContextFixture(t)
	builder, err := contextbuilder.New(repository, contextbuilder.Config{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, _, err := builder.Initialize(t.Context(), item, "SYSTEM"); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	for _, current := range executionphase.All() {
		sourceRef, err := contextbuilder.PluginSourceRef("dev.kern.go@0.1.0", current)
		if err != nil {
			t.Fatalf("PluginSourceRef(%s) error = %v", current, err)
		}
		appendRecord := model.Message{
			Role: model.RoleAssistant,
			Content: []model.ContentBlock{{
				Kind: model.ContentText,
				Text: strings.ToUpper(string(current)) + "_PLUGIN_CONTEXT",
			}},
		}
		if _, err := builder.Append(
			t.Context(), item, appendRecord,
			contextbuilder.TrustPluginUntrusted, contextbuilder.SourcePlugin, sourceRef,
		); err != nil {
			t.Fatalf("Append(%s) error = %v", current, err)
		}
	}
	if _, err := builder.Append(
		t.Context(),
		item,
		model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{
			Kind: model.ContentText, Text: "LEGACY_FULL_BUNDLE",
		}}},
		contextbuilder.TrustPluginUntrusted,
		contextbuilder.SourcePlugin,
		"dev.kern.legacy@0.0.1",
	); err != nil {
		t.Fatalf("Append(legacy) error = %v", err)
	}

	for _, current := range executionphase.All() {
		messages, report, err := builder.BuildForPhase(t.Context(), item.ActiveAttemptID, current)
		if err != nil {
			t.Fatalf("BuildForPhase(%s) error = %v", current, err)
		}
		encoded, _ := json.Marshal(messages)
		content := string(encoded)
		want := strings.ToUpper(string(current)) + "_PLUGIN_CONTEXT"
		if !strings.Contains(content, want) {
			t.Fatalf("phase %s omits current plugin context: %s", current, content)
		}
		for _, other := range executionphase.All() {
			otherValue := strings.ToUpper(string(other)) + "_PLUGIN_CONTEXT"
			if other != current && strings.Contains(content, otherValue) {
				t.Fatalf("phase %s leaked %s: %s", current, other, content)
			}
		}
		if strings.Contains(content, "LEGACY_FULL_BUNDLE") || report.PhaseScopedRecords != 3 ||
			report.PluginPhaseRecords != 3 ||
			len(report.HiddenRecords) != 3 || len(report.OmittedRecords) != 0 || report.NeedsSummary {
			t.Fatalf("phase %s report/content = %#v %s", current, report, content)
		}
	}
	messages, report, err := builder.Build(t.Context(), item.ActiveAttemptID)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	encoded, _ := json.Marshal(messages)
	if strings.Contains(string(encoded), "PLUGIN_CONTEXT") || strings.Contains(string(encoded), "LEGACY_FULL_BUNDLE") ||
		len(report.HiddenRecords) != 4 {
		t.Fatalf("unscoped Build leaked plugin context: %#v %s", report, encoded)
	}
}

func TestBuilderKeepsPhaseScopedToolGroupsInsideTheirPhase(t *testing.T) {
	repository, item := openContextFixture(t)
	builder, err := contextbuilder.New(repository, contextbuilder.Config{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, _, err := builder.Initialize(t.Context(), item, "SYSTEM"); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	callRef, _ := contextbuilder.PhaseSourceRef("request-prepare", executionphase.Prepare)
	resultRef, _ := contextbuilder.PhaseSourceRef("call-prepare", executionphase.Prepare)
	if _, err := builder.Append(
		t.Context(),
		item,
		model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{
			Kind: model.ContentToolCall,
			ToolCall: &model.ToolCall{
				ID: "call-prepare", Name: "capability",
				Arguments: json.RawMessage(`{"action":"get_bundle","plugin_id":"dev.kern.go"}`),
			},
		}}},
		contextbuilder.TrustModel,
		contextbuilder.SourceModel,
		callRef,
	); err != nil {
		t.Fatalf("Append(call) error = %v", err)
	}
	if _, err := builder.Append(
		t.Context(),
		item,
		model.Message{Role: model.RoleTool, Content: []model.ContentBlock{{
			Kind: model.ContentToolResult,
			ToolResult: &model.ToolResult{
				CallID: "call-prepare", Content: "PREPARE_CAPABILITY_RESULT",
			},
		}}},
		contextbuilder.TrustToolUntrusted,
		contextbuilder.SourceTool,
		resultRef,
	); err != nil {
		t.Fatalf("Append(result) error = %v", err)
	}

	prepare, _, err := builder.BuildForPhase(t.Context(), item.ActiveAttemptID, executionphase.Prepare)
	if err != nil {
		t.Fatalf("BuildForPhase(prepare) error = %v", err)
	}
	execute, report, err := builder.BuildForPhase(t.Context(), item.ActiveAttemptID, executionphase.Execute)
	if err != nil {
		t.Fatalf("BuildForPhase(execute) error = %v", err)
	}
	prepareJSON, _ := json.Marshal(prepare)
	executeJSON, _ := json.Marshal(execute)
	if !strings.Contains(string(prepareJSON), "PREPARE_CAPABILITY_RESULT") ||
		strings.Contains(string(executeJSON), "PREPARE_CAPABILITY_RESULT") || len(report.HiddenRecords) != 2 {
		t.Fatalf("prepare=%s execute=%s report=%#v", prepareJSON, executeJSON, report)
	}
	if _, created, err := builder.Summarize(t.Context(), item, report); err != nil || created {
		t.Fatalf("Summarize(hidden phase records) created=%v error=%v", created, err)
	}
}

func TestBuilderKeepsToolCallAndResultAsOneBudgetGroup(t *testing.T) {
	repository, item := openContextFixture(t)
	builder, err := contextbuilder.New(repository, contextbuilder.Config{MaxCharacters: 5 << 10})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, _, err := builder.Initialize(t.Context(), item, "Follow safety constraints."); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	appendMessage(t, builder, item, model.Message{
		Role:    model.RoleAssistant,
		Content: []model.ContentBlock{{Kind: model.ContentText, Text: strings.Repeat("old", 1400)}},
	}, contextbuilder.TrustModel, contextbuilder.SourceModel)
	appendMessage(t, builder, item, model.Message{
		Role: model.RoleAssistant,
		Content: []model.ContentBlock{{Kind: model.ContentToolCall, ToolCall: &model.ToolCall{
			ID: "call-1", Name: "inspect", Arguments: json.RawMessage(`{"path":"README.md"}`),
		}}},
	}, contextbuilder.TrustModel, contextbuilder.SourceModel)
	appendMessage(t, builder, item, model.Message{
		Role: model.RoleTool,
		Content: []model.ContentBlock{{Kind: model.ContentToolResult, ToolResult: &model.ToolResult{
			CallID: "call-1", Content: strings.Repeat("evidence", 80),
		}}},
	}, contextbuilder.TrustToolUntrusted, contextbuilder.SourceTool)
	appendMessage(t, builder, item, model.Message{
		Role:    model.RoleAssistant,
		Content: []model.ContentBlock{{Kind: model.ContentText, Text: "newest conclusion"}},
	}, contextbuilder.TrustModel, contextbuilder.SourceModel)

	messages, report, err := builder.Build(t.Context(), item.ActiveAttemptID)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !report.NeedsSummary || len(report.OmittedRecords) == 0 {
		t.Fatalf("Build() report = %#v", report)
	}
	var callIndex, resultIndex = -1, -1
	for index, message := range messages {
		for _, block := range message.Content {
			if block.ToolCall != nil && block.ToolCall.ID == "call-1" {
				callIndex = index
			}
			if block.ToolResult != nil && block.ToolResult.CallID == "call-1" {
				resultIndex = index
			}
		}
	}
	if callIndex < 0 || resultIndex != callIndex+1 {
		t.Fatalf("tool group was split: call=%d result=%d messages=%#v", callIndex, resultIndex, messages)
	}
	summary, created, err := builder.Summarize(t.Context(), item, report)
	if err != nil || !created || summary.Source != contextbuilder.SourceSummary {
		t.Fatalf("Summarize() = %#v, %v, %v", summary, created, err)
	}
	for _, omittedID := range report.OmittedRecords {
		if !strings.Contains(summary.SourceRef, omittedID) {
			t.Fatalf("summary source refs %q omit %s", summary.SourceRef, omittedID)
		}
	}
	messages, _, err = builder.Build(t.Context(), item.ActiveAttemptID)
	if err != nil {
		t.Fatalf("Build(after summary) error = %v", err)
	}
	var foundSummary bool
	for _, message := range messages {
		for _, block := range message.Content {
			foundSummary = foundSummary || (block.Kind == model.ContentReasoningSummary &&
				strings.Contains(block.Text, "Phase summary"))
		}
	}
	if !foundSummary {
		t.Fatal("active context does not include the phase summary")
	}
	if _, createdAgain, err := builder.Summarize(t.Context(), item, report); err != nil || createdAgain {
		t.Fatalf("Summarize(covered) created=%v error=%v", createdAgain, err)
	}
}

func TestBuilderCompactsActiveToolResultWithoutChangingDurableHistory(t *testing.T) {
	repository, item := openContextFixture(t)
	builder, err := contextbuilder.New(repository, contextbuilder.Config{MaxCharacters: 12 << 10})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, _, err := builder.Initialize(t.Context(), item, "system"); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	appendMessage(t, builder, item, model.Message{
		Role: model.RoleAssistant,
		Content: []model.ContentBlock{{Kind: model.ContentToolCall, ToolCall: &model.ToolCall{
			ID: "call-large", Name: "inspect", Arguments: json.RawMessage(`{"path":"large.log"}`),
		}}},
	}, contextbuilder.TrustModel, contextbuilder.SourceModel)
	appendMessage(t, builder, item, model.Message{
		Role: model.RoleTool,
		Content: []model.ContentBlock{{Kind: model.ContentToolResult, ToolResult: &model.ToolResult{
			CallID: "call-large", Content: strings.Repeat("x", 20<<10),
		}}},
	}, contextbuilder.TrustToolUntrusted, contextbuilder.SourceTool)

	messages, report, err := builder.Build(t.Context(), item.ActiveAttemptID)
	if err != nil || report.NeedsSummary {
		t.Fatalf("Build() = %#v, %#v, %v", messages, report, err)
	}
	activeContent := messages[len(messages)-1].Content[0].ToolResult.Content
	if len(activeContent) >= 20<<10 || !strings.Contains(activeContent, "compacted") {
		t.Fatalf("active tool content was not compacted: %d bytes", len(activeContent))
	}
	records, err := repository.ListContextMessages(t.Context(), item.ActiveAttemptID)
	if err != nil {
		t.Fatalf("ListContextMessages() error = %v", err)
	}
	durableContent := records[len(records)-1].Message.Content[0].ToolResult.Content
	if len(durableContent) != 20<<10 {
		t.Fatalf("durable tool content = %d bytes", len(durableContent))
	}
}

func appendMessage(
	t *testing.T,
	builder *contextbuilder.Builder,
	item task.Task,
	message model.Message,
	trust contextbuilder.TrustLevel,
	source contextbuilder.Source,
) {
	t.Helper()
	if _, err := builder.Append(t.Context(), item, message, trust, source, ""); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
}

func openContextFixture(t *testing.T) (*sqlite.Store, task.Task) {
	t.Helper()
	repository, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	item, err := repository.CreateTask(t.Context(), "context", "Keep the user's original goal.")
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	return repository, item
}
