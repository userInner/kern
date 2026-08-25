package evalrunner

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/userInner/kern/internal/evaluation"
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
