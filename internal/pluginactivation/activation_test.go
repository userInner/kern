package pluginactivation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/userInner/kern/internal/contextbuilder"
	"github.com/userInner/kern/internal/model"
	"github.com/userInner/kern/internal/plugin"
	"github.com/userInner/kern/internal/task"
)

func TestActivatorRespectsExplicitPreferenceAndWorkspaceSignal(t *testing.T) {
	workspaceRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspaceRoot, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(go.mod) error = %v", err)
	}
	installed := activationPlugin(t, "dev.kern.go", false)
	lister := fakeLister{items: []plugin.Installed{installed}}
	repo := &fakeActivationRepo{preferences: []plugin.Preference{{PluginID: installed.ID, Mode: plugin.PreferenceEnable}}}
	activator, err := New(lister, repo, fakeWorkspace{root: workspaceRoot}, Config{
		AutoActivate: true,
		SystemPrompt: "Core prompt",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	item := task.Task{ID: "task-1", ActiveAttemptID: "attempt-1", Goal: "summarize files"}
	selected, err := activator.Activate(t.Context(), item)
	if err != nil || len(selected) != 1 || selected[0].Reason != "manual_enable" {
		t.Fatalf("Activate(manual) = %#v, %v", selected, err)
	}

	repo.preferences = []plugin.Preference{{PluginID: installed.ID, Mode: plugin.PreferenceDisable}}
	selected, err = activator.Activate(t.Context(), item)
	if err != nil || len(selected) != 0 {
		t.Fatalf("Activate(disabled) = %#v, %v", selected, err)
	}

	installed.Enabled = true
	lister.items[0] = installed
	repo.preferences = nil
	selected, err = activator.Activate(t.Context(), item)
	if err != nil || len(selected) != 1 || selected[0].Reason != "signal:go.mod" {
		t.Fatalf("Activate(signal) = %#v, %v", selected, err)
	}
}

func TestProcessorPersistsAuditBeforeDelegating(t *testing.T) {
	workspaceRoot := t.TempDir()
	installed := activationPlugin(t, "dev.kern.go", false)
	repo := &fakeActivationRepo{preferences: []plugin.Preference{{PluginID: installed.ID, Mode: plugin.PreferenceEnable}}}
	activator, err := New(
		fakeLister{items: []plugin.Installed{installed}},
		repo,
		fakeWorkspace{root: workspaceRoot},
		Config{SystemPrompt: "Core prompt"},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	contexts := &fakeContext{}
	delegate := &fakeProcessor{}
	processor, err := NewProcessor(delegate, activator, contexts)
	if err != nil {
		t.Fatalf("NewProcessor() error = %v", err)
	}
	item := task.Task{ID: "task-1", ActiveAttemptID: "attempt-1", Goal: "review code"}
	result, err := processor.Process(t.Context(), item)
	if err != nil || result != "general result" || !delegate.called {
		t.Fatalf("Process() = %q, %v; called=%v", result, err, delegate.called)
	}
	if len(repo.usages) != 1 || repo.usages[0].PluginID != installed.ID || len(contexts.records) != 3 {
		t.Fatalf("usage=%#v context=%#v", repo.usages, contexts.records)
	}
	wantPhases := []string{"prepare", "execute", "verify"}
	for index, record := range contexts.records {
		if record.Trust != contextbuilder.TrustPluginUntrusted ||
			record.Source != contextbuilder.SourcePlugin ||
			!strings.HasSuffix(record.SourceRef, "#phase="+wantPhases[index]) ||
			!strings.Contains(record.Message.Content[0].Text, `"phase":"`+wantPhases[index]+`"`) {
			t.Fatalf("phase context[%d] = %#v", index, record)
		}
	}
	if !strings.Contains(string(repo.usages[0].Resources), `"phases"`) {
		t.Fatalf("usage resources omit phase audit: %s", repo.usages[0].Resources)
	}
}

func TestProcessorActivatesAndAuditsSubprocessOnlyPlugin(t *testing.T) {
	workspaceRoot := t.TempDir()
	installed := executableActivationPlugin(t)
	repo := &fakeActivationRepo{preferences: []plugin.Preference{{PluginID: installed.ID, Mode: plugin.PreferenceEnable}}}
	activator, err := New(
		fakeLister{items: []plugin.Installed{installed}},
		repo,
		fakeWorkspace{root: workspaceRoot},
		Config{SystemPrompt: "Core prompt"},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	processor, err := NewProcessor(&fakeProcessor{}, activator, &fakeContext{})
	if err != nil {
		t.Fatalf("NewProcessor() error = %v", err)
	}
	item := task.Task{ID: "task-1", ActiveAttemptID: "attempt-1", Goal: "inspect package"}
	if _, err := processor.Process(t.Context(), item); err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if len(repo.usages) != 1 || !strings.Contains(string(repo.usages[0].Resources), `"id":"package_report"`) ||
		!strings.Contains(string(repo.usages[0].Resources), `"runtime":"subprocess"`) {
		t.Fatalf("usages = %#v", repo.usages)
	}
}

type fakeLister struct{ items []plugin.Installed }

func (f fakeLister) List(context.Context) ([]plugin.Installed, error) { return f.items, nil }

type fakeWorkspace struct{ root string }

func (f fakeWorkspace) Root() string { return f.root }

type fakeActivationRepo struct {
	preferences []plugin.Preference
	usages      []plugin.Usage
	events      []string
}

func (f *fakeActivationRepo) TaskPluginPreferences(context.Context, string) ([]plugin.Preference, error) {
	return f.preferences, nil
}

func (f *fakeActivationRepo) RecordAttemptPlugin(_ context.Context, usage plugin.Usage) (bool, error) {
	f.usages = append(f.usages, usage)
	return true, nil
}

func (f *fakeActivationRepo) AppendEvent(_ context.Context, _, _, eventType string, _ any) error {
	f.events = append(f.events, eventType)
	return nil
}

type fakeContext struct{ records []contextbuilder.Record }

func (f *fakeContext) Initialize(context.Context, task.Task, string) ([]model.Message, contextbuilder.BuildReport, error) {
	return nil, contextbuilder.BuildReport{}, nil
}

func (f *fakeContext) Append(
	_ context.Context,
	_ task.Task,
	message model.Message,
	trust contextbuilder.TrustLevel,
	source contextbuilder.Source,
	sourceRef string,
) (contextbuilder.Record, error) {
	record := contextbuilder.Record{Message: message, Trust: trust, Source: source, SourceRef: sourceRef}
	f.records = append(f.records, record)
	return record, nil
}

type fakeProcessor struct{ called bool }

func (f *fakeProcessor) Process(context.Context, task.Task) (string, error) {
	f.called = true
	return "general result", nil
}

func activationPlugin(t *testing.T, pluginID string, enabled bool) plugin.Installed {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "knowledge.md"), []byte("Use Go evidence.\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(knowledge) error = %v", err)
	}
	digest, err := plugin.PackageDigest(directory)
	if err != nil {
		t.Fatalf("PackageDigest() error = %v", err)
	}
	manifest := plugin.Manifest{
		SchemaVersion: plugin.SchemaVersion,
		ID:            pluginID, Name: "Go Expert", Version: "0.1.0", Core: ">=0.1.0 <0.2.0",
		Entrypoints: plugin.Entrypoints{Knowledge: []string{"knowledge.md"}},
		Activation:  plugin.Activation{Signals: []string{"go.mod"}},
		Integrity:   plugin.Integrity{Files: digest},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("Marshal(manifest) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, plugin.ManifestFile), encoded, 0o600); err != nil {
		t.Fatalf("WriteFile(manifest) error = %v", err)
	}
	return plugin.Installed{
		SchemaVersion: plugin.SchemaVersion,
		ID:            manifest.ID, Name: manifest.Name, Version: manifest.Version,
		Digest: digest, Enabled: enabled, Manifest: manifest, InstallPath: directory,
		InstalledAt: time.Now(), UpdatedAt: time.Now(),
	}
}

func executableActivationPlugin(t *testing.T) plugin.Installed {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(directory, "tool.json"),
		[]byte(`{"schema_version":"1","id":"package_report","description":"Inspect packages.","runtime":"subprocess","command":["go","version"],"input_schema":{"type":"object"}}`),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(tool) error = %v", err)
	}
	digest, err := plugin.PackageDigest(directory)
	if err != nil {
		t.Fatalf("PackageDigest() error = %v", err)
	}
	manifest := plugin.Manifest{
		SchemaVersion: plugin.SchemaVersion,
		ID:            "dev.kern.package-report", Name: "Package Report", Version: "0.1.0", Core: ">=0.1.0 <0.2.0",
		Entrypoints: plugin.Entrypoints{Tools: []string{"tool.json"}},
		Permissions: plugin.Permissions{Process: []string{"go"}},
		Integrity:   plugin.Integrity{Files: digest},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("Marshal(manifest) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, plugin.ManifestFile), encoded, 0o600); err != nil {
		t.Fatalf("WriteFile(manifest) error = %v", err)
	}
	return plugin.Installed{
		SchemaVersion: plugin.SchemaVersion,
		ID:            manifest.ID, Name: manifest.Name, Version: manifest.Version,
		Digest: digest, Manifest: manifest, InstallPath: directory,
		InstalledAt: time.Now(), UpdatedAt: time.Now(),
	}
}
