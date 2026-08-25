package evalrunner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/userInner/kern/internal/evaluation"
	"github.com/userInner/kern/internal/plugin"
	"github.com/userInner/kern/internal/task"
)

func TestUnattendedEvaluationPrompt(t *testing.T) {
	prompt := unattendedEvaluationPrompt("  repair the repository  ")
	for _, want := range []string{
		"unattended",
		"No human can answer",
		"temporary deterministic value scoped to the workspace",
		"User task:\nrepair the repository",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("unattendedEvaluationPrompt() missing %q: %q", want, prompt)
		}
	}
}

func TestApprovalAllowed(t *testing.T) {
	tests := []struct {
		name    string
		scope   map[string]any
		allowed []string
		want    bool
	}{
		{name: "workspace write", scope: map[string]any{"effect": "local_write"}, want: true},
		{name: "allowed command", scope: map[string]any{"effect": "process", "input": map[string]any{"argv": []string{"go", "test", "./..."}}}, allowed: []string{"go"}, want: true},
		{name: "wildcard command", scope: map[string]any{"effect": "process", "input": map[string]any{"argv": []string{"ls", "/data"}}}, allowed: []string{"*"}, want: true},
		{name: "unlisted command", scope: map[string]any{"effect": "process", "input": map[string]any{"argv": []string{"git", "push"}}}, allowed: []string{"go"}},
		{name: "network", scope: map[string]any{"effect": "network_read"}, allowed: []string{"go"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.scope)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			if got := approvalAllowed(encoded, test.allowed); got != test.want {
				t.Fatalf("approvalAllowed() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestExternalVerifierCanGrade(t *testing.T) {
	tests := []struct {
		name string
		item task.Task
		want bool
	}{
		{
			name: "core verification failure",
			item: task.Task{Status: task.StatusFailed, ErrorMessage: "verification failed: temporary file was removed"},
			want: true,
		},
		{
			name: "token budget exhausted after execution",
			item: task.Task{Status: task.StatusFailed, ErrorMessage: "processing task: agent: token budget exhausted"},
			want: true,
		},
		{
			name: "processor failure",
			item: task.Task{Status: task.StatusFailed, ErrorMessage: "processing task: model unavailable"},
		},
		{
			name: "completed task",
			item: task.Task{Status: task.StatusCompleted},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := externalVerifierCanGrade(test.item); got != test.want {
				t.Fatalf("externalVerifierCanGrade() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestKernAgentRunsOfflineBaseline(t *testing.T) {
	t.Setenv("KERN_MODEL_BASE_URL", "")
	t.Setenv("KERN_MODEL", "")
	agent, err := NewKernAgent(Config{DataRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("NewKernAgent() error = %v", err)
	}
	result, err := agent.Run(t.Context(), evaluation.AgentRequest{
		RunID: "run.test", CaseID: "case.test", Variant: evaluation.Variant{ID: "general.base"},
		Attempt: 1, WorkspaceDir: t.TempDir(), Prompt: "Inspect this workspace and summarize it.",
		Timeout: time.Minute, TokenBudget: 1_000, CostBudgetMicros: 100_000,
		AllowedCommands: []string{"go"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.TaskID == "" || result.TaskStatus != "completed" || result.Usage.DurationMS < 0 {
		t.Fatalf("Run() = %#v", result)
	}
}

func TestKernAgentRejectsInstalledPluginDigestMismatch(t *testing.T) {
	source := writeEvalPlugin(t, "dev.kern.digest")
	agent, err := NewKernAgent(Config{
		DataRoot:      t.TempDir(),
		PluginSources: map[string]string{"dev.kern.digest": source},
	})
	if err != nil {
		t.Fatalf("NewKernAgent() error = %v", err)
	}
	_, err = agent.Run(t.Context(), evaluation.AgentRequest{
		RunID: "run.digest", CaseID: "case.digest",
		Variant: evaluation.Variant{
			ID: "plugin.digest", Plugins: []string{"dev.kern.digest@0.1.0"},
			PluginDigests: map[string]string{"dev.kern.digest": "sha256:" + strings.Repeat("0", 64)},
		},
		Attempt: 1, WorkspaceDir: t.TempDir(), Prompt: "Inspect the workspace.",
		Timeout: time.Minute, TokenBudget: 1_000, AllowedCommands: []string{"go"},
	})
	if err == nil || !strings.Contains(err.Error(), "digest is") {
		t.Fatalf("Run(digest mismatch) error = %v", err)
	}
}

func writeEvalPlugin(t *testing.T, pluginID string) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "knowledge"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(directory, "knowledge", "guide.md"),
		[]byte("Use reproducible evidence.\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	digest, err := plugin.PackageDigest(directory)
	if err != nil {
		t.Fatal(err)
	}
	manifest := plugin.Manifest{
		SchemaVersion: plugin.SchemaVersion,
		ID:            pluginID, Name: "Evaluation plugin", Version: "0.1.0", Core: ">=0.1.0 <0.2.0",
		Entrypoints: plugin.Entrypoints{Knowledge: []string{"knowledge/guide.md"}},
		Activation:  plugin.Activation{Intents: []string{"code.review"}},
		Permissions: plugin.Permissions{Filesystem: []string{"workspace:read"}},
		Integrity:   plugin.Integrity{Files: digest},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, plugin.ManifestFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return directory
}
