package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/userInner/kern/internal/approval"
	"github.com/userInner/kern/internal/configuration"
	"github.com/userInner/kern/internal/contextbuilder"
	"github.com/userInner/kern/internal/model"
	"github.com/userInner/kern/internal/modelconfig"
	"github.com/userInner/kern/internal/operation"
	"github.com/userInner/kern/internal/plugin"
	"github.com/userInner/kern/internal/pluginruntime"
	"github.com/userInner/kern/internal/policy"
	"github.com/userInner/kern/internal/secret"
	"github.com/userInner/kern/internal/store/sqlite"
	"github.com/userInner/kern/internal/task"
	"github.com/userInner/kern/internal/workspace"
)

func TestRuntimeOfflineLifecycle(t *testing.T) {
	t.Setenv("KERN_MODEL_BASE_URL", "")
	t.Setenv("KERN_MODEL", "")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	runtime, err := Open(ctx, Config{
		DataDir: t.TempDir(),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	created, err := runtime.Submit(ctx, "", "prove the lifecycle")
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	completed, err := runtime.Wait(ctx, created.ID)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if completed.Status != task.StatusCompleted {
		t.Fatalf("Wait() status = %q, want completed", completed.Status)
	}
	if completed.Result == "" {
		t.Fatal("Wait() returned an empty result")
	}
	events, err := runtime.Store.EventsAfter(ctx, created.ID, 0, 100)
	if err != nil {
		t.Fatalf("EventsAfter() error = %v", err)
	}
	if len(events) < 10 {
		t.Fatalf("EventsAfter() count = %d, want at least 10", len(events))
	}
}

func TestRuntimeSettingsPersistAndApplyToFutureExecutions(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "config.json")
	generator := &settingsTestGenerator{maxTokens: make(chan int, 2)}
	runtime, err := Open(t.Context(), Config{
		DataDir: t.TempDir(), SettingsPath: settingsPath, ModelGenerator: generator,
		MaxTurns: 3, MaxToolCalls: 4, MaxTokens: 50, MaxCostMicros: 2_000_000,
		MaxTaskDuration: time.Minute, PolicyProfile: string(policy.ProfileLocalSafe), RetentionDays: 90,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	before := runtime.CurrentSettings()
	if before.MaxTokens != 50 || before.PolicyProfile != string(policy.ProfileLocalSafe) ||
		before.RetentionDays != 90 {
		t.Fatalf("CurrentSettings(before) = %#v", before)
	}
	updated, err := runtime.UpdateSettings(t.Context(), SettingsDraft{
		MaxTurns: 5, MaxToolCalls: 6, MaxTokens: 123, MaxCostUSD: 3.5,
		TaskTimeout: "2m", PolicyProfile: string(policy.ProfileReadOnly), RetentionDays: 45,
	})
	if err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	if updated.MaxTurns != 5 || updated.MaxToolCalls != 6 || updated.MaxTokens != 123 ||
		updated.MaxCostUSD != 3.5 || updated.TaskTimeout != "2m0s" ||
		updated.PolicyProfile != string(policy.ProfileReadOnly) || updated.RetentionDays != 45 {
		t.Fatalf("UpdateSettings() = %#v", updated)
	}
	persisted, err := configuration.Load(settingsPath)
	if err != nil {
		t.Fatalf("configuration.Load() error = %v", err)
	}
	if persisted.Runtime.MaxTokens != 123 || persisted.Runtime.MaxCostUSD != 3.5 ||
		persisted.Policy.Profile != string(policy.ProfileReadOnly) || persisted.Storage.RetentionDays != 45 {
		t.Fatalf("persisted settings = %#v", persisted)
	}
	decision, err := runtime.policy.Evaluate(operation.Operation{
		Tool: "change", Input: json.RawMessage(`{"path":"file.txt"}`),
		InputHash: "digest", Effect: operation.EffectLocalWrite,
	})
	if err != nil || decision.Action != policy.ActionDeny {
		t.Fatalf("Evaluate(read-only write) = %#v, %v", decision, err)
	}
	created, err := runtime.Submit(t.Context(), "", "use updated settings")
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if _, err := runtime.Wait(t.Context(), created.ID); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	select {
	case got := <-generator.maxTokens:
		if got != 123 {
			t.Fatalf("model MaxTokens = %d, want 123", got)
		}
	case <-time.After(time.Second):
		t.Fatal("model request was not observed")
	}
	if _, err := runtime.UpdateSettings(t.Context(), SettingsDraft{
		MaxTurns: 5, MaxToolCalls: 6, MaxTokens: 123, MaxCostUSD: 3.5,
		TaskTimeout: "2m", PolicyProfile: "unsafe", RetentionDays: 45,
	}); err == nil {
		t.Fatal("UpdateSettings(unsafe profile) error = nil")
	}
	if runtime.CurrentSettings() != updated {
		t.Fatalf("invalid update changed settings: got %#v want %#v", runtime.CurrentSettings(), updated)
	}
}

type settingsTestGenerator struct {
	maxTokens chan int
}

func (g *settingsTestGenerator) Generate(_ context.Context, request model.Request) (model.Response, error) {
	g.maxTokens <- request.MaxTokens
	return model.Response{
		Message: model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{
			Kind: model.ContentText, Text: "settings applied",
		}}},
		FinishReason: "stop",
	}, nil
}

func TestRuntimePersistsAndMaterializesImageAttachments(t *testing.T) {
	generator := &attachmentTestGenerator{requests: make(chan model.Request, 1)}
	runtime, err := Open(t.Context(), Config{
		DataDir: t.TempDir(), ModelGenerator: generator,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	png := append([]byte("\x89PNG\r\n\x1a\n"), []byte("bounded-test-image")...)
	created, err := runtime.SubmitWithOptions(t.Context(), TaskOptions{
		Goal: "describe the attached image",
		Attachments: []TaskAttachment{{
			Name: "screen.png", MediaType: "image/png", Data: png,
		}},
	})
	if err != nil {
		t.Fatalf("SubmitWithOptions() error = %v", err)
	}

	var request model.Request
	select {
	case request = <-generator.requests:
	case <-time.After(3 * time.Second):
		t.Fatal("model request was not received")
	}
	var imageURL string
	for _, message := range request.Messages {
		for _, block := range message.Content {
			if block.Kind == model.ContentImage && block.Image != nil {
				imageURL = block.Image.URL
			}
		}
	}
	if !strings.HasPrefix(imageURL, "data:image/png;base64,") {
		t.Fatalf("materialized image URL = %q", imageURL)
	}
	completed, err := runtime.Wait(t.Context(), created.ID)
	if err != nil || completed.Result != "image received" {
		t.Fatalf("Wait() = %#v, %v", completed, err)
	}
	items, err := runtime.Artifacts.List(t.Context(), created.ID)
	if err != nil || len(items) != 1 || items[0].Name != "screen.png" || items[0].MediaType != "image/png" {
		t.Fatalf("Artifacts.List() = %#v, %v", items, err)
	}
	records, err := runtime.Store.ListContextMessages(t.Context(), created.ActiveAttemptID)
	if err != nil || len(records) < 2 {
		t.Fatalf("ListContextMessages() = %#v, %v", records, err)
	}
	encoded, err := json.Marshal(records[1].Message)
	if err != nil || !strings.Contains(string(encoded), `"kind":"artifact_ref"`) ||
		strings.Contains(string(encoded), "data:image/png;base64") {
		t.Fatalf("durable user message = %s, %v", encoded, err)
	}

	followUpPNG := append([]byte("\x89PNG\r\n\x1a\n"), []byte("second-bounded-test-image")...)
	continued, err := runtime.SubmitInputWithAttachments(t.Context(), completed.ID, TaskInput{
		Content: "compare this image with the previous image",
		Attachments: []TaskAttachment{{
			Name: "follow-up.png", MediaType: "image/png", Data: followUpPNG,
		}},
	})
	if err != nil {
		t.Fatalf("SubmitInputWithAttachments() error = %v", err)
	}
	if continued.ActiveAttemptID == completed.ActiveAttemptID {
		t.Fatal("SubmitInputWithAttachments() did not create a new Attempt")
	}

	var followUpRequest model.Request
	select {
	case followUpRequest = <-generator.requests:
	case <-time.After(3 * time.Second):
		t.Fatal("follow-up model request was not received")
	}
	imageCount := 0
	for _, message := range followUpRequest.Messages {
		for _, block := range message.Content {
			if block.Kind == model.ContentImage && block.Image != nil {
				imageCount++
			}
		}
	}
	if imageCount != 2 {
		t.Fatalf("follow-up materialized image count = %d, want 2", imageCount)
	}
	if _, err := runtime.Wait(t.Context(), continued.ID); err != nil {
		t.Fatalf("Wait(follow-up) error = %v", err)
	}

	items, err = runtime.Artifacts.List(t.Context(), continued.ID)
	if err != nil || len(items) != 2 || items[1].Name != "follow-up.png" ||
		items[1].AttemptID != continued.ActiveAttemptID {
		t.Fatalf("Artifacts.List(follow-up) = %#v, %v", items, err)
	}
	continuedRecords, err := runtime.Store.ListContextMessages(t.Context(), continued.ActiveAttemptID)
	if err != nil {
		t.Fatalf("ListContextMessages(follow-up) error = %v", err)
	}
	encoded, err = json.Marshal(continuedRecords)
	if err != nil || strings.Count(string(encoded), `"kind":"artifact_ref"`) != 2 ||
		strings.Contains(string(encoded), "data:image/png;base64") {
		t.Fatalf("durable follow-up context = %s, %v", encoded, err)
	}
	foundFollowUp := false
	for _, record := range continuedRecords {
		if record.Source != contextbuilder.SourceUserInput {
			continue
		}
		message, marshalErr := json.Marshal(record.Message)
		if marshalErr != nil {
			t.Fatalf("Marshal(follow-up message) error = %v", marshalErr)
		}
		if strings.Contains(string(message), "compare this image") &&
			strings.Contains(string(message), `"kind":"artifact_ref"`) {
			foundFollowUp = true
		}
	}
	if !foundFollowUp {
		t.Fatalf("follow-up user message missing from %#v", continuedRecords)
	}
}

func TestRuntimeRejectsSpoofedImageAttachmentBeforeCreatingTask(t *testing.T) {
	runtime, err := Open(t.Context(), Config{
		DataDir: t.TempDir(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	_, err = runtime.SubmitWithOptions(t.Context(), TaskOptions{
		Goal: "inspect",
		Attachments: []TaskAttachment{{
			Name: "not-image.png", MediaType: "image/png", Data: []byte("not a png"),
		}},
	})
	if !errors.Is(err, ErrInvalidTaskAttachment) {
		t.Fatalf("SubmitWithOptions() error = %v", err)
	}
	items, listErr := runtime.Store.ListTasks(t.Context(), 10)
	if listErr != nil || len(items) != 0 {
		t.Fatalf("ListTasks() = %#v, %v", items, listErr)
	}
}

type attachmentTestGenerator struct {
	requests chan model.Request
}

func (g *attachmentTestGenerator) Generate(_ context.Context, request model.Request) (model.Response, error) {
	g.requests <- request
	return model.Response{
		Message: model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{
			Kind: model.ContentText, Text: "image received",
		}}},
		FinishReason: "stop",
	}, nil
}

func TestRuntimeAutoActivatesDeclarativePluginAndAuditsVersion(t *testing.T) {
	t.Setenv("KERN_MODEL_BASE_URL", "")
	t.Setenv("KERN_MODEL", "")
	workspaceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspaceDir, "go.mod"), []byte("module example.test/plugin\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(go.mod) error = %v", err)
	}
	runtime, err := Open(t.Context(), Config{
		DataDir:             t.TempDir(),
		WorkspaceDir:        workspaceDir,
		AutoActivatePlugins: true,
		Logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "knowledge.md"), []byte("Prefer table-driven Go tests.\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(knowledge) error = %v", err)
	}
	digest, err := plugin.PackageDigest(source)
	if err != nil {
		t.Fatalf("PackageDigest() error = %v", err)
	}
	manifest := plugin.Manifest{
		SchemaVersion: plugin.SchemaVersion,
		ID:            "dev.kern.go-expert",
		Name:          "Go Expert",
		Version:       "0.1.0",
		Core:          ">=0.1.0 <0.2.0",
		Entrypoints:   plugin.Entrypoints{Knowledge: []string{"knowledge.md"}},
		Activation:    plugin.Activation{Signals: []string{"go.mod"}, Intents: []string{"code.test"}},
		Permissions:   plugin.Permissions{Filesystem: []string{"workspace:read"}},
		Integrity:     plugin.Integrity{Files: digest},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("Marshal(manifest) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, plugin.ManifestFile), encoded, 0o600); err != nil {
		t.Fatalf("WriteFile(manifest) error = %v", err)
	}
	installed, _, err := runtime.Plugins.Install(t.Context(), source)
	if err != nil {
		t.Fatalf("Plugins.Install() error = %v", err)
	}
	if _, err := runtime.Plugins.Enable(t.Context(), installed.ID); err != nil {
		t.Fatalf("Plugins.Enable() error = %v", err)
	}
	created, err := runtime.Submit(t.Context(), "", "review and test this Go module")
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	completed, err := runtime.Wait(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	usages, err := runtime.Store.ListAttemptPlugins(t.Context(), completed.ID, completed.ActiveAttemptID)
	if err != nil || len(usages) != 1 || usages[0].PluginID != installed.ID ||
		usages[0].Version != installed.Version || !strings.Contains(usages[0].Reason, "signal:go.mod") {
		t.Fatalf("ListAttemptPlugins() = %#v, %v", usages, err)
	}
	records, err := runtime.Store.ListContextMessages(t.Context(), completed.ActiveAttemptID)
	if err != nil {
		t.Fatalf("ListContextMessages() error = %v", err)
	}
	foundPhases := make(map[string]bool)
	for _, record := range records {
		if record.Source == contextbuilder.SourcePlugin &&
			record.Trust == contextbuilder.TrustPluginUntrusted &&
			strings.HasPrefix(record.SourceRef, installed.ID+"@"+installed.Version+"#phase=") {
			foundPhases[strings.TrimPrefix(
				record.SourceRef,
				installed.ID+"@"+installed.Version+"#phase=",
			)] = true
		}
	}
	if !foundPhases["prepare"] || !foundPhases["execute"] || !foundPhases["verify"] {
		t.Fatalf("plugin context record missing: %#v", records)
	}
}

func TestRuntimeFollowUpInputCreatesAttemptWithInheritedContext(t *testing.T) {
	t.Setenv("KERN_MODEL_BASE_URL", "")
	t.Setenv("KERN_MODEL", "")
	runtime, err := Open(t.Context(), Config{
		DataDir: t.TempDir(),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	created, err := runtime.Submit(t.Context(), "", "first request")
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	completed, err := runtime.Wait(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Wait(first) error = %v", err)
	}
	continued, err := runtime.SubmitInput(t.Context(), completed.ID, "follow-up evidence")
	if err != nil {
		t.Fatalf("SubmitInput() error = %v", err)
	}
	if continued.ActiveAttemptID == completed.ActiveAttemptID {
		t.Fatal("SubmitInput() did not create a new Attempt")
	}
	records, err := runtime.Store.ListContextMessages(t.Context(), continued.ActiveAttemptID)
	if err != nil {
		t.Fatalf("ListContextMessages() error = %v", err)
	}
	if len(records) != 3 || records[0].Source != contextbuilder.SourceCore ||
		records[1].Source != contextbuilder.SourceOriginalGoal ||
		records[2].Source != contextbuilder.SourceUserInput ||
		records[2].Message.Content[0].Text != "follow-up evidence" {
		t.Fatalf("continued context = %#v", records)
	}
	if _, err := runtime.Wait(t.Context(), continued.ID); err != nil {
		t.Fatalf("Wait(follow-up) error = %v", err)
	}
}

func TestRuntimeModelModeUsesStreamingAgent(t *testing.T) {
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"check\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"agent result\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer modelServer.Close()
	t.Setenv("KERN_MODEL_BASE_URL", modelServer.URL)
	t.Setenv("KERN_MODEL", "test-model")
	t.Setenv("KERN_MODEL_API_KEY", "")
	runtime, err := Open(t.Context(), Config{
		DataDir: t.TempDir(),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	created, err := runtime.Submit(t.Context(), "", "stream through the agent")
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	completed, err := runtime.Wait(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if completed.Result != "agent result" {
		t.Fatalf("result = %q, want agent result", completed.Result)
	}
	events, err := runtime.Store.EventsAfter(t.Context(), created.ID, 0, 100)
	if err != nil {
		t.Fatalf("EventsAfter() error = %v", err)
	}
	var hasDelta, hasReasoning, hasUsage bool
	for _, event := range events {
		hasDelta = hasDelta || event.Type == "message.delta"
		hasReasoning = hasReasoning || event.Type == "reasoning.delta"
		hasUsage = hasUsage || event.Type == "model.usage"
	}
	if !hasDelta || !hasReasoning || !hasUsage {
		t.Fatalf("agent events missing: delta=%v reasoning=%v usage=%v", hasDelta, hasReasoning, hasUsage)
	}
}

func TestRuntimeRoutesTaskThroughDurableModelConfig(t *testing.T) {
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if authorization := request.Header.Get("Authorization"); authorization != "Bearer private-key" {
			t.Errorf("Authorization = %q", authorization)
		}
		var input struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("Decode() error = %v", err)
		}
		if !input.Stream {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("x-request-id", "probe-request")
			_, _ = fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"configured result\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer modelServer.Close()
	t.Setenv("KERN_MODEL_BASE_URL", "")
	t.Setenv("KERN_MODEL", "")
	vault := &runtimeTestVault{values: map[string]string{"keyring:runtime-model": "private-key"}}
	runtime, err := Open(t.Context(), Config{
		DataDir:     t.TempDir(),
		SecretVault: vault,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	config, err := runtime.Store.CreateModelConfig(t.Context(), modelconfig.Draft{
		Name:      "Runtime model",
		Provider:  modelconfig.ProviderOpenAICompatible,
		BaseURL:   modelServer.URL,
		Model:     "configured-model",
		SecretRef: "keyring:runtime-model",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("CreateModelConfig() error = %v", err)
	}
	probe, err := runtime.TestModelConfig(t.Context(), config.ID)
	if err != nil {
		t.Fatalf("TestModelConfig() error = %v", err)
	}
	if !probe.OK || probe.RequestID != "probe-request" || probe.ConfigID != config.ID {
		t.Fatalf("TestModelConfig() = %#v", probe)
	}
	created, err := runtime.Submit(t.Context(), "", "use the configured model")
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if created.ModelConfigID != config.ID {
		t.Fatalf("model config ID = %q, want %q", created.ModelConfigID, config.ID)
	}
	completed, err := runtime.Wait(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if completed.Result != "configured result" {
		t.Fatalf("result = %q", completed.Result)
	}
	selection, err := runtime.Store.ModelSelection(t.Context(), created.ActiveAttemptID)
	if err != nil {
		t.Fatalf("ModelSelection() error = %v", err)
	}
	if selection.ConfigID != config.ID || selection.Model != "configured-model" {
		t.Fatalf("selection = %#v", selection)
	}
	var parameters struct {
		MaxTurns      int   `json:"max_turns"`
		MaxToolCalls  int   `json:"max_tool_calls"`
		MaxTokens     int   `json:"max_tokens"`
		MaxCostMicros int64 `json:"max_cost_micros"`
		MaxDurationMS int64 `json:"max_duration_ms"`
	}
	if err := json.Unmarshal(selection.Parameters, &parameters); err != nil {
		t.Fatalf("model selection parameters: %v", err)
	}
	if parameters.MaxTurns <= 0 || parameters.MaxToolCalls <= 0 || parameters.MaxTokens <= 0 ||
		parameters.MaxCostMicros <= 0 || parameters.MaxDurationMS <= 0 {
		t.Fatalf("model selection stored ineffective defaults: %s", selection.Parameters)
	}
	events, err := runtime.Store.EventsAfter(t.Context(), created.ID, 0, 100)
	if err != nil {
		t.Fatalf("EventsAfter() error = %v", err)
	}
	hasSelection := false
	for _, event := range events {
		hasSelection = hasSelection || event.Type == "model.selected"
		if strings.Contains(string(event.Payload), "private-key") ||
			strings.Contains(string(event.Payload), "KERN_ROUTER_TEST_KEY") {
			t.Fatalf("event leaked credential material: %s", event.Payload)
		}
	}
	if !hasSelection {
		t.Fatal("model.selected event not found")
	}
}

type runtimeTestVault struct {
	values         map[string]string
	deleted        []string
	next           int
	cancelAfterPut context.CancelFunc
	checkErr       error
}

func (v *runtimeTestVault) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return v.checkErr
}

func (v *runtimeTestVault) Resolve(ctx context.Context, reference string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !strings.HasPrefix(reference, "keyring:") {
		return "", secret.ErrUnsupported
	}
	value, ok := v.values[reference]
	if !ok {
		return "", secret.ErrNotFound
	}
	return value, nil
}

func (v *runtimeTestVault) Owns(reference string) bool {
	return strings.HasPrefix(reference, "keyring:")
}

func (v *runtimeTestVault) Put(ctx context.Context, value string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	v.next++
	reference := fmt.Sprintf("keyring:test-%d", v.next)
	v.values[reference] = value
	if v.cancelAfterPut != nil {
		v.cancelAfterPut()
	}
	return reference, nil
}

func (v *runtimeTestVault) Delete(ctx context.Context, reference string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	delete(v.values, reference)
	v.deleted = append(v.deleted, reference)
	return nil
}

func TestRuntimeCompensatesCredentialAfterRequestCancellation(t *testing.T) {
	requestCtx, cancelRequest := context.WithCancel(t.Context())
	vault := &runtimeTestVault{
		values:         make(map[string]string),
		cancelAfterPut: cancelRequest,
	}
	runtime, err := Open(t.Context(), Config{
		DataDir:     t.TempDir(),
		SecretVault: vault,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	draft := modelconfig.Draft{
		Name: "Cancelled model", Provider: modelconfig.ProviderOpenAICompatible,
		BaseURL: "https://example.test", Model: "test-model", Enabled: true,
	}
	_, err = runtime.CreateModelConfigWithAPIKey(requestCtx, draft, "temporary-private-key")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateModelConfigWithAPIKey() error = %v, want context cancellation", err)
	}
	if len(vault.values) != 0 || len(vault.deleted) != 1 {
		t.Fatalf("cancelled request left credential state: values=%#v deleted=%#v", vault.values, vault.deleted)
	}
}

func TestRuntimeDisablesUnavailableCredentialVault(t *testing.T) {
	vault := &runtimeTestVault{
		values:   make(map[string]string),
		checkErr: errors.New("credential service is unavailable"),
	}
	runtime, err := Open(t.Context(), Config{
		DataDir:     t.TempDir(),
		SecretVault: vault,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	if runtime.CredentialStoreAvailable() || runtime.Capabilities().WritableCredentialStore {
		t.Fatalf("unavailable vault reported writable: %#v", runtime.Capabilities())
	}
	_, err = runtime.CreateModelConfigWithAPIKey(t.Context(), modelconfig.Draft{
		Name: "Unavailable vault", Provider: modelconfig.ProviderOpenAICompatible,
		BaseURL: "https://example.test", Model: "test-model", Enabled: true,
	}, "must-not-be-written")
	if !errors.Is(err, ErrCredentialStoreUnavailable) {
		t.Fatalf("CreateModelConfigWithAPIKey() error = %v", err)
	}
	if len(vault.values) != 0 {
		t.Fatalf("unavailable vault received a write: %#v", vault.values)
	}
}

func TestRuntimeStoresOnlyOpaqueModelCredentialReferences(t *testing.T) {
	vault := &runtimeTestVault{values: make(map[string]string)}
	runtime, err := Open(t.Context(), Config{
		DataDir:     t.TempDir(),
		SecretVault: vault,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	draft := modelconfig.Draft{
		Name: "Vault model", Provider: modelconfig.ProviderOpenAICompatible,
		BaseURL: "https://example.test", Model: "test-model", Enabled: true,
	}
	created, err := runtime.CreateModelConfigWithAPIKey(t.Context(), draft, "first-private-key")
	if err != nil {
		t.Fatalf("CreateModelConfigWithAPIKey() error = %v", err)
	}
	stored, err := runtime.Store.GetModelConfig(t.Context(), created.ID)
	if err != nil || !strings.HasPrefix(stored.SecretRef, "keyring:") ||
		vault.values[stored.SecretRef] != "first-private-key" {
		t.Fatalf("stored config = %#v, vault = %#v, error = %v", stored, vault.values, err)
	}
	encoded, err := json.Marshal(created)
	if err != nil || strings.Contains(string(encoded), "keyring:") ||
		strings.Contains(string(encoded), "first-private-key") {
		t.Fatalf("public config leaked credential data: %s, %v", encoded, err)
	}

	previousRef := stored.SecretRef
	updated, err := runtime.UpdateModelConfigWithAPIKey(
		t.Context(),
		created.ID,
		draft,
		"second-private-key",
	)
	if err != nil {
		t.Fatalf("UpdateModelConfigWithAPIKey() error = %v", err)
	}
	stored, err = runtime.Store.GetModelConfig(t.Context(), updated.ID)
	if err != nil || stored.SecretRef == previousRef || vault.values[stored.SecretRef] != "second-private-key" {
		t.Fatalf("updated config = %#v, vault = %#v, error = %v", stored, vault.values, err)
	}
	if _, exists := vault.values[previousRef]; exists {
		t.Fatalf("replaced credential %q still exists", previousRef)
	}

	replacementRef := stored.SecretRef
	draft.SecretRef = ""
	cleared, err := runtime.UpdateModelConfig(t.Context(), created.ID, draft)
	if err != nil || cleared.HasAPIKey {
		t.Fatalf("UpdateModelConfig(clear) = %#v, %v", cleared, err)
	}
	if _, exists := vault.values[replacementRef]; exists {
		t.Fatalf("cleared credential %q still exists", replacementRef)
	}

	beforeConflict := len(vault.values)
	if _, err := runtime.CreateModelConfigWithAPIKey(t.Context(), draft, "orphan-candidate"); !errors.Is(err, modelconfig.ErrConflict) {
		t.Fatalf("CreateModelConfigWithAPIKey(conflict) error = %v", err)
	}
	if len(vault.values) != beforeConflict {
		t.Fatalf("failed create left credential behind: %#v", vault.values)
	}

	secondDraft := draft
	secondDraft.Name = "Disposable model"
	disposable, err := runtime.CreateModelConfigWithAPIKey(t.Context(), secondDraft, "delete-me")
	if err != nil {
		t.Fatalf("CreateModelConfigWithAPIKey(disposable) error = %v", err)
	}
	disposableStored, err := runtime.Store.GetModelConfig(t.Context(), disposable.ID)
	if err != nil {
		t.Fatalf("GetModelConfig(disposable) error = %v", err)
	}
	if err := runtime.DeleteModelConfig(t.Context(), disposable.ID); err != nil {
		t.Fatalf("DeleteModelConfig() error = %v", err)
	}
	if _, exists := vault.values[disposableStored.SecretRef]; exists {
		t.Fatalf("deleted model credential %q still exists", disposableStored.SecretRef)
	}
}

func TestRuntimeModelModeExecutesWorkspaceInspect(t *testing.T) {
	workspaceDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(workspaceDir, "README.md"),
		[]byte("Kern fixture evidence\n"),
		0o644,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	var calls atomic.Int32
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var input struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("Decode() error = %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch calls.Add(1) {
		case 1:
			toolNames := make(map[string]bool, len(input.Tools))
			for _, definition := range input.Tools {
				toolNames[definition.Function.Name] = true
			}
			for _, name := range []string{"change", "execute", "inspect", "capability"} {
				if !toolNames[name] {
					t.Errorf("model request is missing tool %q", name)
				}
			}
			if toolNames["network"] || len(toolNames) != 4 {
				t.Errorf("model-facing tools = %#v, want exactly four high-level tools", toolNames)
			}
			_, _ = fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-inspect","function":{"name":"inspect","arguments":"{\"action\":\"read_file\",\"path\":\"README.md\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		case 2:
			var foundResult bool
			for _, message := range input.Messages {
				if message.Role == "tool" && strings.Contains(string(message.Content), "Kern fixture evidence") {
					foundResult = true
				}
			}
			if !foundResult {
				t.Error("second model request is missing inspect output")
			}
			_, _ = fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"README was inspected."},"finish_reason":"stop"}]}`+"\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		default:
			t.Errorf("unexpected model request %d", calls.Load())
		}
	}))
	defer modelServer.Close()
	t.Setenv("KERN_MODEL_BASE_URL", modelServer.URL)
	t.Setenv("KERN_MODEL", "test-model")
	t.Setenv("KERN_MODEL_API_KEY", "")
	runtime, err := Open(t.Context(), Config{
		DataDir:      t.TempDir(),
		WorkspaceDir: workspaceDir,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	created, err := runtime.Submit(t.Context(), "", "inspect the readme")
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	completed, err := runtime.Wait(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if completed.Result != "README was inspected." || calls.Load() != 2 {
		t.Fatalf("result = %q, model calls = %d", completed.Result, calls.Load())
	}
	events, err := runtime.Store.EventsAfter(t.Context(), created.ID, 0, 100)
	if err != nil {
		t.Fatalf("EventsAfter() error = %v", err)
	}
	var operationCompleted bool
	for _, event := range events {
		operationCompleted = operationCompleted || event.Type == "operation.completed"
	}
	if !operationCompleted {
		t.Fatal("inspect operation did not reach a durable terminal state")
	}
	contextRecords, err := runtime.Store.ListContextMessages(t.Context(), completed.ActiveAttemptID)
	if err != nil {
		t.Fatalf("ListContextMessages() error = %v", err)
	}
	if len(contextRecords) != 5 {
		t.Fatalf("context message count = %d, want 5: %#v", len(contextRecords), contextRecords)
	}
	wantTrust := []contextbuilder.TrustLevel{
		contextbuilder.TrustSystem,
		contextbuilder.TrustUser,
		contextbuilder.TrustModel,
		contextbuilder.TrustToolUntrusted,
		contextbuilder.TrustModel,
	}
	for index, record := range contextRecords {
		if record.Sequence != index+1 || record.Trust != wantTrust[index] {
			t.Fatalf("context record %d = %#v", index, record)
		}
	}
}

func TestRuntimeApprovalExecutesExactWorkspaceChange(t *testing.T) {
	workspaceDir := t.TempDir()
	var calls atomic.Int32
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("ReadAll() error = %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch calls.Add(1) {
		case 1:
			if !strings.Contains(string(body), `"name":"change"`) {
				t.Error("model request is missing change tool")
			}
			_, _ = fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-change","function":{"name":"change","arguments":"{\"action\":\"write_file\",\"path\":\"note.txt\",\"content\":\"approved content\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		case 2:
			if !strings.Contains(string(body), `"role":"tool"`) ||
				!strings.Contains(string(body), `note.txt`) {
				t.Error("second model request is missing change result")
			}
			_, _ = fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"The approved file was created."},"finish_reason":"stop"}]}`+"\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		default:
			t.Errorf("unexpected model request %d", calls.Load())
		}
	}))
	defer modelServer.Close()
	t.Setenv("KERN_MODEL_BASE_URL", modelServer.URL)
	t.Setenv("KERN_MODEL", "test-model")
	t.Setenv("KERN_MODEL_API_KEY", "")
	runtime, err := Open(t.Context(), Config{
		DataDir:      t.TempDir(),
		WorkspaceDir: workspaceDir,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	created, err := runtime.Submit(t.Context(), "", "create note.txt")
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	request := waitForRuntimeApproval(t, runtime, created.ID)
	var scope map[string]any
	if err := json.Unmarshal(request.Scope, &scope); err != nil {
		t.Fatalf("approval scope error = %v", err)
	}
	if scope["tool"] != "change" || scope["effect"] != "local_write" {
		t.Fatalf("approval scope = %#v", scope)
	}
	if _, err := runtime.DecideApproval(
		t.Context(),
		request.ID,
		approval.DecisionApproved,
	); err != nil {
		t.Fatalf("DecideApproval() error = %v", err)
	}
	completed, err := runtime.Wait(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if completed.Result != "The approved file was created." {
		t.Fatalf("result = %q", completed.Result)
	}
	content, err := os.ReadFile(filepath.Join(workspaceDir, "note.txt"))
	if err != nil {
		t.Fatalf("ReadFile(note.txt) error = %v", err)
	}
	if string(content) != "approved content" {
		t.Fatalf("note.txt = %q", content)
	}
	records, err := runtime.Store.ListAttemptOperations(
		t.Context(),
		completed.ID,
		completed.ActiveAttemptID,
	)
	if err != nil || len(records) != 1 || string(records[0].Operation.Recovery) == "{}" {
		t.Fatalf("change recovery metadata = %#v, %v", records, err)
	}
}

func TestRuntimeApprovalExecutesAuditedPluginCapability(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	t.Setenv("PATH", filepath.Dir(executable)+string(os.PathListSeparator)+os.Getenv("PATH"))
	var calls atomic.Int32
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("ReadAll() error = %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch calls.Add(1) {
		case 1:
			if !strings.Contains(string(body), `"name":"capability"`) ||
				!strings.Contains(string(body), "dev.kern.runtime-test") ||
				!strings.Contains(string(body), `\"phase\":\"prepare\"`) ||
				strings.Contains(string(body), `\"id\":\"echo\"`) {
				t.Error("prepare request has the wrong plugin capability context")
			}
			_, _ = fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Prepared capability execution."},"finish_reason":"stop"}]}`+"\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		case 2:
			if !strings.Contains(string(body), `"name":"capability"`) ||
				!strings.Contains(string(body), `\"phase\":\"execute\"`) ||
				!strings.Contains(string(body), `\"id\":\"echo\"`) {
				t.Error("execute request is missing active plugin tool context")
			}
			_, _ = fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-capability","function":{"name":"capability","arguments":"{\"action\":\"invoke\",\"plugin_id\":\"dev.kern.runtime-test\",\"tool_id\":\"echo\",\"input\":{\"message\":\"hello\"}}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		case 3:
			if !strings.Contains(string(body), `"role":"tool"`) ||
				!strings.Contains(string(body), "plugin-result") {
				t.Error("post-execution request is missing plugin result")
			}
			_, _ = fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Execution completed with plugin evidence."},"finish_reason":"stop"}]}`+"\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		case 4:
			if !strings.Contains(string(body), `\"phase\":\"verify\"`) ||
				strings.Contains(string(body), `"name":"change"`) {
				t.Error("verify request has the wrong phase or tool surface")
			}
			_, _ = fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"The approved plugin capability completed."},"finish_reason":"stop"}]}`+"\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		default:
			t.Errorf("unexpected model request %d", calls.Load())
		}
	}))
	defer modelServer.Close()
	t.Setenv("KERN_MODEL_BASE_URL", modelServer.URL)
	t.Setenv("KERN_MODEL", "test-model")
	t.Setenv("KERN_MODEL_API_KEY", "")
	runtime, err := Open(t.Context(), Config{
		DataDir: t.TempDir(),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	source := t.TempDir()
	toolSpec := fmt.Sprintf(
		`{"schema_version":"1","id":"echo","description":"Echo one message.","runtime":"subprocess","command":[%q,"-test.run=^TestRuntimePluginHelperProcess$","--","echo"],"input_schema":{"type":"object","additionalProperties":false,"required":["message"],"properties":{"message":{"type":"string"}}}}`,
		filepath.Base(executable),
	)
	if err := os.WriteFile(filepath.Join(source, "tool.json"), []byte(toolSpec), 0o600); err != nil {
		t.Fatalf("WriteFile(tool) error = %v", err)
	}
	digest, err := plugin.PackageDigest(source)
	if err != nil {
		t.Fatalf("PackageDigest() error = %v", err)
	}
	manifest := plugin.Manifest{
		SchemaVersion: plugin.SchemaVersion,
		ID:            "dev.kern.runtime-test",
		Name:          "Runtime Test",
		Version:       "0.1.0",
		Core:          ">=0.1.0 <0.2.0",
		Entrypoints:   plugin.Entrypoints{Tools: []string{"tool.json"}},
		Permissions:   plugin.Permissions{Process: []string{filepath.Base(executable)}},
		Integrity:     plugin.Integrity{Files: digest},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("Marshal(manifest) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, plugin.ManifestFile), encoded, 0o600); err != nil {
		t.Fatalf("WriteFile(manifest) error = %v", err)
	}
	installed, _, err := runtime.Plugins.Install(t.Context(), source)
	if err != nil {
		t.Fatalf("Plugins.Install() error = %v", err)
	}
	created, err := runtime.SubmitWithOptions(t.Context(), TaskOptions{
		Goal:          "invoke the echo capability",
		EnablePlugins: []string{installed.ID},
	})
	if err != nil {
		t.Fatalf("SubmitWithOptions() error = %v", err)
	}
	request := waitForRuntimeApproval(t, runtime, created.ID)
	var scope map[string]any
	if err := json.Unmarshal(request.Scope, &scope); err != nil {
		t.Fatalf("approval scope error = %v", err)
	}
	if scope["tool"] != "capability" || scope["effect"] != "process" || request.Risk != approval.RiskHigh {
		t.Fatalf("approval request = %#v; scope=%#v", request, scope)
	}
	if _, err := runtime.DecideApproval(t.Context(), request.ID, approval.DecisionApproved); err != nil {
		t.Fatalf("DecideApproval() error = %v", err)
	}
	completed, err := runtime.Wait(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if completed.Result != "The approved plugin capability completed." || calls.Load() != 4 {
		t.Fatalf("result = %q, model calls = %d", completed.Result, calls.Load())
	}
	records, err := runtime.Store.ListAttemptOperations(t.Context(), completed.ID, completed.ActiveAttemptID)
	if err != nil || len(records) != 1 || records[0].Operation.Tool != "capability" ||
		records[0].Operation.Status != operation.StatusSucceeded || records[0].ApprovalReceiptID == "" {
		t.Fatalf("plugin operation records = %#v, %v", records, err)
	}
	usages, err := runtime.Store.ListAttemptPlugins(t.Context(), completed.ID, completed.ActiveAttemptID)
	if err != nil || len(usages) != 1 || !strings.Contains(string(usages[0].Resources), `"id":"echo"`) {
		t.Fatalf("plugin usages = %#v, %v", usages, err)
	}
}

type appWASMSandboxFunc func(context.Context, pluginruntime.WASMInvocation) ([]byte, error)

func (appWASMSandboxFunc) Check(ctx context.Context) error {
	return ctx.Err()
}

func (f appWASMSandboxFunc) Invoke(
	ctx context.Context,
	invocation pluginruntime.WASMInvocation,
) ([]byte, error) {
	return f(ctx, invocation)
}

type unavailableWASMSandbox struct {
	checks atomic.Int32
	calls  atomic.Int32
}

func (s *unavailableWASMSandbox) Check(ctx context.Context) error {
	s.checks.Add(1)
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.New("WASM engine failed its startup probe")
}

func (s *unavailableWASMSandbox) Invoke(
	context.Context,
	pluginruntime.WASMInvocation,
) ([]byte, error) {
	s.calls.Add(1)
	return nil, errors.New("unavailable WASM sandbox was invoked")
}

func TestRuntimeDisablesUnavailableWASMSandbox(t *testing.T) {
	sandbox := &unavailableWASMSandbox{}
	var logs bytes.Buffer
	runtime, err := Open(t.Context(), Config{
		DataDir:     t.TempDir(),
		WASMSandbox: sandbox,
		Logger:      slog.New(slog.NewTextHandler(&logs, nil)),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if runtime.Capabilities().WASMPluginRuntime {
		t.Fatal("unavailable WASM sandbox reported ready")
	}
	if sandbox.checks.Load() != 1 || sandbox.calls.Load() != 0 {
		t.Fatalf("sandbox checks=%d calls=%d", sandbox.checks.Load(), sandbox.calls.Load())
	}
	if !strings.Contains(logs.String(), "WASM plugin runtime unavailable") {
		t.Fatalf("startup warning missing: %s", logs.String())
	}
}

type wasmCapabilityGenerator struct {
	calls atomic.Int32
}

func (g *wasmCapabilityGenerator) Generate(_ context.Context, request model.Request) (model.Response, error) {
	switch g.calls.Add(1) {
	case 1:
		return textModelResponse("Prepared WASM capability execution."), nil
	case 2:
		return model.Response{
			Message: model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{
				Kind: model.ContentToolCall,
				ToolCall: &model.ToolCall{
					ID:   "call-wasm-capability",
					Name: "capability",
					Arguments: json.RawMessage(
						`{"action":"invoke","plugin_id":"dev.kern.wasm-app-test","tool_id":"echo","input":{"message":"hello"}}`,
					),
				},
			}}},
			FinishReason: "tool_calls",
		}, nil
	case 3:
		encoded, err := json.Marshal(request.Messages)
		if err != nil {
			return model.Response{}, err
		}
		if !strings.Contains(string(encoded), "wasm-plugin-result") {
			return model.Response{}, errors.New("WASM result was not returned to the model")
		}
		return textModelResponse("WASM execution completed with plugin evidence."), nil
	case 4:
		return textModelResponse("The approved WASM capability completed."), nil
	default:
		return model.Response{}, fmt.Errorf("unexpected WASM model request %d", g.calls.Load())
	}
}

func textModelResponse(content string) model.Response {
	return model.Response{
		Message: model.Message{Role: model.RoleAssistant, Content: []model.ContentBlock{{
			Kind: model.ContentText,
			Text: content,
		}}},
		FinishReason: "stop",
	}
}

func TestRuntimeInjectsWASMSandboxThroughApprovedCapability(t *testing.T) {
	generator := &wasmCapabilityGenerator{}
	invocations := make(chan pluginruntime.WASMInvocation, 1)
	sandbox := appWASMSandboxFunc(func(
		_ context.Context,
		invocation pluginruntime.WASMInvocation,
	) ([]byte, error) {
		invocations <- invocation
		return []byte(`{"jsonrpc":"2.0","id":"1","result":{"message":"wasm-plugin-result"}}`), nil
	})
	runtime, err := Open(t.Context(), Config{
		DataDir:        t.TempDir(),
		ModelGenerator: generator,
		WASMSandbox:    sandbox,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	source := t.TempDir()
	toolSpec := `{"schema_version":"1","id":"echo","description":"Echo one message.","runtime":"wasm","module":"echo.wasm","input_schema":{"type":"object","additionalProperties":false,"required":["message"],"properties":{"message":{"type":"string"}}}}`
	if err := os.WriteFile(filepath.Join(source, "tool.json"), []byte(toolSpec), 0o600); err != nil {
		t.Fatalf("WriteFile(tool) error = %v", err)
	}
	module := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	if err := os.WriteFile(filepath.Join(source, "echo.wasm"), module, 0o600); err != nil {
		t.Fatalf("WriteFile(module) error = %v", err)
	}
	digest, err := plugin.PackageDigest(source)
	if err != nil {
		t.Fatalf("PackageDigest() error = %v", err)
	}
	manifest := plugin.Manifest{
		SchemaVersion: plugin.SchemaVersion,
		ID:            "dev.kern.wasm-app-test",
		Name:          "WASM App Test",
		Version:       "0.1.0",
		Core:          ">=0.1.0 <0.2.0",
		Entrypoints:   plugin.Entrypoints{Tools: []string{"tool.json"}},
		Integrity:     plugin.Integrity{Files: digest},
	}
	encodedManifest, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("Marshal(manifest) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, plugin.ManifestFile), encodedManifest, 0o600); err != nil {
		t.Fatalf("WriteFile(manifest) error = %v", err)
	}
	installed, _, err := runtime.Plugins.Install(t.Context(), source)
	if err != nil {
		t.Fatalf("Plugins.Install() error = %v", err)
	}
	created, err := runtime.SubmitWithOptions(t.Context(), TaskOptions{
		Goal:          "invoke the WASM echo capability",
		EnablePlugins: []string{installed.ID},
	})
	if err != nil {
		t.Fatalf("SubmitWithOptions() error = %v", err)
	}
	request := waitForRuntimeApproval(t, runtime, created.ID)
	if request.Risk != approval.RiskHigh {
		t.Fatalf("approval risk = %q, want high", request.Risk)
	}
	if _, err := runtime.DecideApproval(t.Context(), request.ID, approval.DecisionApproved); err != nil {
		t.Fatalf("DecideApproval() error = %v", err)
	}
	completed, err := runtime.Wait(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if completed.Result != "The approved WASM capability completed." || generator.calls.Load() != 4 {
		t.Fatalf("result = %q, model calls = %d", completed.Result, generator.calls.Load())
	}
	select {
	case invocation := <-invocations:
		if invocation.PluginID != installed.ID || invocation.ToolID != "echo" ||
			!bytes.Equal(invocation.Module, module) ||
			!strings.Contains(string(invocation.Request), `"message":"hello"`) {
			t.Fatalf("WASM invocation = %#v", invocation)
		}
	default:
		t.Fatal("WASM sandbox was not invoked")
	}
	records, err := runtime.Store.ListAttemptOperations(t.Context(), completed.ID, completed.ActiveAttemptID)
	if err != nil || len(records) != 1 || records[0].Operation.Status != operation.StatusSucceeded ||
		records[0].ApprovalReceiptID == "" {
		t.Fatalf("WASM operation records = %#v, %v", records, err)
	}
}

func TestRuntimeReportsInjectedProductionAdapters(t *testing.T) {
	t.Setenv("KERN_MODEL_BASE_URL", "")
	t.Setenv("KERN_MODEL", "")

	runtime, err := Open(t.Context(), Config{
		DataDir:      t.TempDir(),
		WorkspaceDir: t.TempDir(),
		SecretVault:  &runtimeTestVault{values: make(map[string]string)},
		WASMSandbox: appWASMSandboxFunc(func(context.Context, pluginruntime.WASMInvocation) ([]byte, error) {
			return []byte(`{"ok":true}`), nil
		}),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer runtime.Close()

	capabilities := runtime.Capabilities()
	if !capabilities.WritableCredentialStore || !capabilities.WASMPluginRuntime {
		t.Fatalf("Capabilities() = %#v", capabilities)
	}
}

func TestRuntimePluginHelperProcess(t *testing.T) {
	for index, argument := range os.Args {
		if argument != "--" || index+1 >= len(os.Args) || os.Args[index+1] != "echo" {
			continue
		}
		fmt.Print(`{"jsonrpc":"2.0","id":"1","result":{"message":"plugin-result"}}`)
		os.Exit(0)
	}
}

func TestRuntimeRecoversShutdownWhileWaitingApproval(t *testing.T) {
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-change","function":{"name":"change","arguments":"{\"action\":\"write_file\",\"path\":\"shutdown.txt\",\"content\":\"should not run\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer modelServer.Close()
	dataDir := t.TempDir()
	workspaceDir := t.TempDir()
	t.Setenv("KERN_MODEL_BASE_URL", modelServer.URL)
	t.Setenv("KERN_MODEL", "test-model")
	runtime, err := Open(t.Context(), Config{
		DataDir:      dataDir,
		WorkspaceDir: workspaceDir,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	created, err := runtime.Submit(t.Context(), "", "request a change")
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	request := waitForRuntimeApproval(t, runtime, created.ID)
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspaceDir, "shutdown.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unapproved file exists or stat failed: %v", err)
	}

	t.Setenv("KERN_MODEL_BASE_URL", "")
	t.Setenv("KERN_MODEL", "")
	recovered, err := Open(t.Context(), Config{
		DataDir:      dataDir,
		WorkspaceDir: workspaceDir,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Open(recovery) error = %v", err)
	}
	t.Cleanup(func() { _ = recovered.Close() })
	completed, err := recovered.Wait(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Wait(recovery) error = %v", err)
	}
	if completed.Status != task.StatusCompleted || completed.ActiveAttemptID == created.ActiveAttemptID {
		t.Fatalf("recovered task = %#v", completed)
	}
	pending, err := recovered.Store.PendingApprovals(t.Context(), created.ID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("PendingApprovals() = %#v, %v", pending, err)
	}
	op, err := recovered.Store.GetOperation(t.Context(), request.OperationID)
	if err != nil || op.Status != operation.StatusCancelled {
		t.Fatalf("interrupted approval operation = %#v, %v", op, err)
	}
}

func waitForRuntimeApproval(t *testing.T, runtime *Runtime, taskID string) approval.Request {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		requests, err := runtime.Store.PendingApprovals(t.Context(), taskID)
		if err != nil {
			t.Fatalf("PendingApprovals() error = %v", err)
		}
		if len(requests) == 1 {
			return requests[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	current, taskErr := runtime.Store.GetTask(t.Context(), taskID)
	events, eventErr := runtime.Store.EventsAfter(t.Context(), taskID, 0, 100)
	t.Fatalf(
		"approval request was not persisted; task=%#v task_err=%v events=%#v event_err=%v",
		current,
		taskErr,
		events,
		eventErr,
	)
	return approval.Request{}
}

func TestRuntimeRecoversInterruptedAttempt(t *testing.T) {
	t.Setenv("KERN_MODEL_BASE_URL", "")
	t.Setenv("KERN_MODEL", "")
	dataDir := t.TempDir()
	store, err := sqlite.Open(t.Context(), filepath.Join(dataDir, "kern.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	created, err := store.CreateTask(t.Context(), "recover", "recover interrupted task")
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	if err := store.Transition(t.Context(), created.ID, task.StatusPlanning, "", ""); err != nil {
		t.Fatalf("Transition(planning) error = %v", err)
	}
	if err := store.AcquireLease(t.Context(), created.ID, "dead-runtime", time.Nanosecond); err != nil {
		t.Fatalf("AcquireLease() error = %v", err)
	}
	time.Sleep(time.Millisecond)
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	runtime, err := Open(t.Context(), Config{
		DataDir: dataDir,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Open(recovery) error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	completed, err := runtime.Wait(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if completed.Status != task.StatusCompleted {
		t.Fatalf("recovered status = %q, want completed", completed.Status)
	}
	if completed.ActiveAttemptID == created.ActiveAttemptID {
		t.Fatal("recovery reused interrupted attempt")
	}
}

func TestRuntimeBlocksRecoveryUntilUnknownSideEffectIsResolved(t *testing.T) {
	t.Setenv("KERN_MODEL_BASE_URL", "")
	t.Setenv("KERN_MODEL", "")
	dataDir := t.TempDir()
	store, err := sqlite.Open(t.Context(), filepath.Join(dataDir, "kern.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	created, err := store.CreateTask(t.Context(), "recover safely", "do not replay an uncertain write")
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	if err := store.Transition(t.Context(), created.ID, task.StatusPlanning, "", ""); err != nil {
		t.Fatalf("Transition(planning) error = %v", err)
	}
	op, _, err := store.CreateOperation(
		t.Context(),
		created.ID,
		"network.request",
		"external-write-1",
		operation.EffectNetworkWrite,
		map[string]string{"url": "https://example.invalid/action"},
	)
	if err != nil {
		t.Fatalf("CreateOperation() error = %v", err)
	}
	for _, status := range []operation.Status{operation.StatusPrepared, operation.StatusExecuting} {
		if _, err := store.TransitionOperation(t.Context(), op.ID, status, nil, "", ""); err != nil {
			t.Fatalf("TransitionOperation(%s) error = %v", status, err)
		}
	}
	if err := store.AcquireLease(t.Context(), created.ID, "dead-runtime", time.Nanosecond); err != nil {
		t.Fatalf("AcquireLease() error = %v", err)
	}
	time.Sleep(time.Millisecond)
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	runtime, err := Open(t.Context(), Config{
		DataDir: dataDir,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Open(recovery) error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	blocked, err := runtime.Store.GetTask(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if blocked.Status != task.StatusWaitingInput || blocked.ActiveAttemptID != created.ActiveAttemptID {
		t.Fatalf("blocked task = %#v", blocked)
	}
	uncertain, err := runtime.Store.ListUncertainOperations(t.Context(), created.ID)
	if err != nil || len(uncertain) != 1 || uncertain[0].ID != op.ID {
		t.Fatalf("ListUncertainOperations() = %#v, %v", uncertain, err)
	}
	if _, err := runtime.Resume(t.Context(), created.ID); !errors.Is(err, operation.ErrUnresolvedEffects) {
		t.Fatalf("Resume(unresolved) error = %v", err)
	}
	if _, err := runtime.ResolveUncertainOperation(
		t.Context(),
		op.ID,
		operation.ResolutionNotExecuted,
	); err != nil {
		t.Fatalf("ResolveUncertainOperation() error = %v", err)
	}
	resumed, err := runtime.Resume(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Resume(resolved) error = %v", err)
	}
	if resumed.ActiveAttemptID == created.ActiveAttemptID {
		t.Fatal("Resume(resolved) reused interrupted attempt")
	}
	completed, err := runtime.Wait(t.Context(), created.ID)
	if err != nil || completed.Status != task.StatusCompleted {
		t.Fatalf("Wait() = %#v, %v", completed, err)
	}
}

func TestRuntimeAutomaticallyReconcilesInterruptedFileWrite(t *testing.T) {
	tests := []struct {
		name        string
		current     string
		wantTask    task.Status
		wantOp      operation.Status
		wantError   string
		wantBlocked bool
	}{
		{
			name: "target hash means write completed", current: "target",
			wantTask: task.StatusCompleted, wantOp: operation.StatusSucceeded,
		},
		{
			name: "before hash means write did not execute", current: "before",
			wantTask: task.StatusCompleted, wantOp: operation.StatusFailed,
			wantError: "reconciled_not_executed",
		},
		{
			name: "third hash blocks recovery", current: "external edit",
			wantTask: task.StatusWaitingInput, wantOp: operation.StatusUncertain,
			wantBlocked: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dataDir := t.TempDir()
			workspaceDir := t.TempDir()
			path := filepath.Join(workspaceDir, "note.txt")
			if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
				t.Fatalf("WriteFile(before) error = %v", err)
			}
			before := sha256.Sum256([]byte("before"))
			after := sha256.Sum256([]byte("target"))
			store, err := sqlite.Open(t.Context(), filepath.Join(dataDir, "kern.db"))
			if err != nil {
				t.Fatalf("sqlite.Open() error = %v", err)
			}
			created, err := store.CreateTask(t.Context(), "recover file", "recover an interrupted file write")
			if err != nil {
				t.Fatalf("CreateTask() error = %v", err)
			}
			if err := store.Transition(t.Context(), created.ID, task.StatusPlanning, "", ""); err != nil {
				t.Fatalf("Transition(planning) error = %v", err)
			}
			if err := store.Transition(t.Context(), created.ID, task.StatusRunning, "", ""); err != nil {
				t.Fatalf("Transition(running) error = %v", err)
			}
			input := map[string]any{
				"action": "write_file", "path": "note.txt", "content": "target",
				"expected_sha256": fmt.Sprintf("%x", before),
			}
			op, _, err := store.CreateOperation(
				t.Context(), created.ID, "change", "interrupted-file", operation.EffectLocalWrite, input,
			)
			if err != nil {
				t.Fatalf("CreateOperation() error = %v", err)
			}
			if _, err := store.TransitionOperation(
				t.Context(), op.ID, operation.StatusPrepared, nil, "", "",
			); err != nil {
				t.Fatalf("TransitionOperation(prepared) error = %v", err)
			}
			metadata, err := json.Marshal(workspace.WriteIntent{
				Path: "note.txt", BeforeSHA256: fmt.Sprintf("%x", before),
				AfterSHA256: fmt.Sprintf("%x", after),
			})
			if err != nil {
				t.Fatalf("Marshal(intent) error = %v", err)
			}
			if _, err := store.SetOperationRecovery(t.Context(), op.ID, metadata); err != nil {
				t.Fatalf("SetOperationRecovery() error = %v", err)
			}
			if _, err := store.TransitionOperation(
				t.Context(), op.ID, operation.StatusExecuting, nil, "", "",
			); err != nil {
				t.Fatalf("TransitionOperation(executing) error = %v", err)
			}
			if err := os.WriteFile(path, []byte(tt.current), 0o644); err != nil {
				t.Fatalf("WriteFile(current) error = %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatalf("Close(store) error = %v", err)
			}

			t.Setenv("KERN_MODEL_BASE_URL", "")
			t.Setenv("KERN_MODEL", "")
			runtime, err := Open(t.Context(), Config{
				DataDir: dataDir, WorkspaceDir: workspaceDir,
				Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			})
			if err != nil {
				t.Fatalf("Open(recovery) error = %v", err)
			}
			t.Cleanup(func() { _ = runtime.Close() })
			var recovered task.Task
			if tt.wantBlocked {
				recovered, err = runtime.Store.GetTask(t.Context(), created.ID)
			} else {
				recovered, err = runtime.Wait(t.Context(), created.ID)
			}
			if err != nil || recovered.Status != tt.wantTask {
				t.Fatalf("recovered task = %#v, %v", recovered, err)
			}
			resolved, err := runtime.Store.GetOperation(t.Context(), op.ID)
			if err != nil || resolved.Status != tt.wantOp || resolved.ErrorCode != tt.wantError {
				t.Fatalf("reconciled operation = %#v, %v", resolved, err)
			}
			uncertain, err := runtime.Store.ListUncertainOperations(t.Context(), created.ID)
			if err != nil || (len(uncertain) > 0) != tt.wantBlocked {
				t.Fatalf("uncertain operations = %#v, %v", uncertain, err)
			}
		})
	}
}
