package embedded_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/userInner/kern/sdk/embedded"
	"github.com/userInner/kern/sdk/kern"
)

func TestRuntimeRunsTypedClientAndReplayableEvents(t *testing.T) {
	t.Setenv("KERN_MODEL_BASE_URL", "")
	t.Setenv("KERN_MODEL", "")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	runtime, err := embedded.Open(ctx, embedded.Config{
		DataDir:      t.TempDir(),
		WorkspaceDir: t.TempDir(),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	parsed, err := url.Parse(runtime.BaseURL())
	if err != nil {
		t.Fatalf("Parse(BaseURL) error = %v", err)
	}
	host, _, err := net.SplitHostPort(parsed.Host)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		t.Fatalf("BaseURL() = %q, host=%q error=%v", runtime.BaseURL(), host, err)
	}
	client := runtime.Client()
	if client == nil {
		t.Fatal("Client() = nil")
	}
	created, err := client.CreateTask(t.Context(), kern.CreateTaskInput{
		Goal: "Explain the embedded Kern runtime in one sentence.",
	})
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}

	streamCtx, streamCancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer streamCancel()
	stream, err := client.OpenEventStream(streamCtx, created.ID, 0)
	if err != nil {
		t.Fatalf("OpenEventStream() error = %v", err)
	}
	var terminalEvent bool
	for !terminalEvent {
		event, err := stream.Next()
		if err != nil {
			t.Fatalf("EventStream.Next() error = %v", err)
		}
		switch event.Type {
		case "task.completed", "task.partially_completed", "task.failed", "task.cancelled":
			terminalEvent = true
		}
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("EventStream.Close() error = %v", err)
	}
	item, err := client.GetTask(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if !item.Status.Terminal() || item.Result == "" {
		t.Fatalf("task = %#v, want terminal result", item)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close(second) error = %v", err)
	}
	if _, ok := <-runtime.Errors(); ok {
		t.Fatal("Errors() remained open after normal shutdown")
	}
}

func TestRuntimeClosesWhenHostContextIsCancelled(t *testing.T) {
	t.Setenv("KERN_MODEL_BASE_URL", "")
	t.Setenv("KERN_MODEL", "")
	ctx, cancel := context.WithCancel(t.Context())
	runtime, err := embedded.Open(ctx, embedded.Config{
		DataDir:      t.TempDir(),
		WorkspaceDir: t.TempDir(),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	cancel()
	select {
	case _, ok := <-runtime.Errors():
		if ok {
			t.Fatal("Errors() reported an unexpected shutdown failure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runtime did not close after host context cancellation")
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestRuntimeReloadsPersistedSettings(t *testing.T) {
	t.Setenv("KERN_MODEL_BASE_URL", "")
	t.Setenv("KERN_MODEL", "")
	dataDir := t.TempDir()
	workspaceDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	first, err := embedded.Open(t.Context(), embedded.Config{
		DataDir: dataDir, WorkspaceDir: workspaceDir, Logger: logger,
	})
	if err != nil {
		t.Fatalf("Open(first) error = %v", err)
	}
	updated, err := first.Client().UpdateSettings(t.Context(), kern.UpdateSettingsInput{
		MaxTurns: 7, MaxToolCalls: 19, MaxTokens: 75_000, MaxCostUSD: 2.5,
		TaskTimeout: "4m", PolicyProfile: kern.PolicyReadOnly, RetentionDays: 31,
	})
	if err != nil || updated.MaxTurns != 7 || updated.PolicyProfile != kern.PolicyReadOnly {
		_ = first.Close()
		t.Fatalf("UpdateSettings() = %#v, %v", updated, err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close(first) error = %v", err)
	}

	second, err := embedded.Open(t.Context(), embedded.Config{
		DataDir: dataDir, WorkspaceDir: workspaceDir, Logger: logger,
	})
	if err != nil {
		t.Fatalf("Open(second) error = %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	reloaded, err := second.Client().GetSettings(t.Context())
	if err != nil {
		t.Fatalf("GetSettings() error = %v", err)
	}
	if reloaded.MaxTurns != 7 || reloaded.MaxToolCalls != 19 ||
		reloaded.MaxTokens != 75_000 || reloaded.MaxCostUSD != 2.5 ||
		reloaded.TaskTimeout != "4m0s" || reloaded.PolicyProfile != kern.PolicyReadOnly ||
		reloaded.RetentionDays != 31 {
		t.Fatalf("reloaded settings = %#v", reloaded)
	}
}

func TestRuntimeUsesRegisteredModelProvider(t *testing.T) {
	t.Setenv("KERN_MODEL_BASE_URL", "")
	t.Setenv("KERN_MODEL", "")
	provider := &recordingProvider{}
	runtime, err := embedded.Open(t.Context(), embedded.Config{
		DataDir:       t.TempDir(),
		WorkspaceDir:  t.TempDir(),
		ModelProvider: provider,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	created, err := runtime.Client().CreateTask(t.Context(), kern.CreateTaskInput{
		Goal: "Use the registered provider.",
	})
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var item kern.Task
	for time.Now().Before(deadline) {
		item, err = runtime.Client().GetTask(t.Context(), created.ID)
		if err != nil {
			t.Fatalf("GetTask() error = %v", err)
		}
		if item.Status.Terminal() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !item.Status.Terminal() || item.Result != "Response from the registered provider." {
		t.Fatalf("task = %#v", item)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.calls != 1 || len(provider.request.Messages) < 2 || len(provider.request.Tools) != 4 {
		t.Fatalf("provider calls=%d request=%#v", provider.calls, provider.request)
	}
}

func TestRuntimeInvokesRegisteredCapabilityThroughCoreTool(t *testing.T) {
	provider := &capabilityCallingProvider{}
	var handlerCalls atomic.Int32
	runtime, err := embedded.Open(t.Context(), embedded.Config{
		DataDir:       t.TempDir(),
		WorkspaceDir:  t.TempDir(),
		ModelProvider: provider,
		Capabilities: []embedded.Capability{{
			ProviderID: "host.example", ProviderName: "Example Host",
			ID: "lookup", Description: "Look up one key.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["key"],"properties":{"key":{"type":"string"}}}`),
			Effect:      embedded.CapabilityRead,
			Handler: func(_ context.Context, input json.RawMessage) (embedded.CapabilityResult, error) {
				handlerCalls.Add(1)
				return embedded.CapabilityResult{
					Content: `{"value":"registered"}`,
					Artifacts: []embedded.CapabilityArtifact{{
						Name: "host-result.json", MediaType: "application/json",
						Content: []byte(`{"value":"registered"}`),
					}},
				}, nil
			},
		}},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	created, err := runtime.Client().CreateTask(t.Context(), kern.CreateTaskInput{Goal: "Use the host lookup."})
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var item kern.Task
	for time.Now().Before(deadline) {
		item, err = runtime.Client().GetTask(t.Context(), created.ID)
		if err != nil {
			t.Fatalf("GetTask() error = %v", err)
		}
		if item.Status.Terminal() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !item.Status.Terminal() || item.Result != "Host capability completed with evidence." || handlerCalls.Load() != 1 {
		t.Fatalf("task=%#v handler calls=%d", item, handlerCalls.Load())
	}
	artifacts, err := runtime.Client().ListArtifacts(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("ListArtifacts() error = %v", err)
	}
	var found bool
	for _, artifact := range artifacts {
		found = found || artifact.Name == "host-result.json"
	}
	if !found {
		t.Fatalf("artifacts = %#v", artifacts)
	}
}

func TestRegisteredCapabilityCannotBypassApproval(t *testing.T) {
	provider := &capabilityCallingProvider{}
	var handlerCalls atomic.Int32
	runtime, err := embedded.Open(t.Context(), embedded.Config{
		DataDir:       t.TempDir(),
		WorkspaceDir:  t.TempDir(),
		ModelProvider: provider,
		Capabilities: []embedded.Capability{{
			ProviderID: "host.example", ProviderName: "Example Host",
			ID: "lookup", Description: "A test capability declaring a local write.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["key"],"properties":{"key":{"type":"string"}}}`),
			Effect:      embedded.CapabilityLocalWrite,
			Handler: func(context.Context, json.RawMessage) (embedded.CapabilityResult, error) {
				handlerCalls.Add(1)
				return embedded.CapabilityResult{Content: `{"value":"registered"}`}, nil
			},
		}},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	created, err := runtime.Client().CreateTask(t.Context(), kern.CreateTaskInput{Goal: "Use the approved host lookup."})
	if err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var approvals []kern.Approval
	for time.Now().Before(deadline) {
		approvals, err = runtime.Client().PendingApprovals(t.Context(), created.ID)
		if err != nil {
			t.Fatalf("PendingApprovals() error = %v", err)
		}
		if len(approvals) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(approvals) != 1 || handlerCalls.Load() != 0 {
		t.Fatalf("approvals=%#v handler calls=%d", approvals, handlerCalls.Load())
	}
	if _, err := runtime.Client().DecideApproval(t.Context(), approvals[0].ID, "approved"); err != nil {
		t.Fatalf("DecideApproval() error = %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	var item kern.Task
	for time.Now().Before(deadline) {
		item, err = runtime.Client().GetTask(t.Context(), created.ID)
		if err != nil {
			t.Fatalf("GetTask() error = %v", err)
		}
		if item.Status.Terminal() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !item.Status.Terminal() || handlerCalls.Load() != 1 {
		t.Fatalf("task=%#v handler calls=%d", item, handlerCalls.Load())
	}
}

type recordingProvider struct {
	mu      sync.Mutex
	calls   int
	request embedded.ModelRequest
}

type capabilityCallingProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *capabilityCallingProvider) Generate(
	_ context.Context,
	request embedded.ModelRequest,
) (embedded.ModelResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.calls == 1 {
		return embedded.ModelResponse{Message: embedded.ModelMessage{
			Role: embedded.ModelRoleAssistant,
			Content: []embedded.ModelContentBlock{{
				Kind: embedded.ModelContentToolCall,
				ToolCall: &embedded.ModelToolCall{
					ID: "host-call", Name: "capability",
					Arguments: json.RawMessage(`{"action":"invoke","plugin_id":"host.example","tool_id":"lookup","input":{"key":"status"}}`),
				},
			}},
		}}, nil
	}
	encoded, _ := json.Marshal(request.Messages)
	if !strings.Contains(string(encoded), `registered`) {
		return embedded.ModelResponse{}, errors.New("test: registered capability result is missing")
	}
	return embedded.ModelResponse{Message: embedded.ModelMessage{
		Role: embedded.ModelRoleAssistant,
		Content: []embedded.ModelContentBlock{{
			Kind: embedded.ModelContentText, Text: "Host capability completed with evidence.",
		}},
	}}, nil
}

func (p *recordingProvider) Generate(
	_ context.Context,
	request embedded.ModelRequest,
) (embedded.ModelResponse, error) {
	p.mu.Lock()
	p.calls++
	p.request = request
	p.mu.Unlock()
	return embedded.ModelResponse{
		Message: embedded.ModelMessage{
			Role: embedded.ModelRoleAssistant,
			Content: []embedded.ModelContentBlock{{
				Kind: embedded.ModelContentText,
				Text: "Response from the registered provider.",
			}},
		},
		Usage: embedded.ModelUsage{InputTokens: 8, OutputTokens: 6},
	}, nil
}
