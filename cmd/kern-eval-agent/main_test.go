package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadInstruction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		input     string
		expected  string
		wantError string
	}{
		{name: "trims surrounding whitespace", input: "  repair the project\n", expected: "repair the project"},
		{name: "rejects empty input", input: " \n\t", wantError: "instruction is empty"},
		{name: "rejects oversized input", input: strings.Repeat("x", maxInstructionBytes+1), wantError: "instruction exceeds 1 MiB"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			actual, err := readInstruction(strings.NewReader(test.input))
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("readInstruction() error = %v, want %q", err, test.wantError)
				}
				return
			}
			if err != nil || actual != test.expected {
				t.Fatalf("readInstruction() = %q, %v; want %q", actual, err, test.expected)
			}
		})
	}
}

func TestRunRejectsExecutionWithoutContainerMarker(t *testing.T) {
	t.Parallel()
	missingMarker := filepath.Join(t.TempDir(), "missing")
	var stdout, stderr bytes.Buffer
	err := run(
		context.Background(),
		[]string{"--container-marker", missingMarker},
		strings.NewReader("complete the task"),
		&stdout,
		&stderr,
	)
	if err == nil || !strings.Contains(err.Error(), "refusing unattended approvals") {
		t.Fatalf("run() error = %v, want container refusal", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("run() stdout = %q, want empty", stdout.String())
	}
}

func TestContainerAllowedCommandsPermitIsolatedTerminalWorkflows(t *testing.T) {
	t.Parallel()
	if len(containerAllowedCommands) != 1 || containerAllowedCommands[0] != "*" {
		t.Fatalf("containerAllowedCommands = %v, want wildcard", containerAllowedCommands)
	}
}

func TestRunRejectsUnexpectedArgumentsBeforeStartingRuntime(t *testing.T) {
	t.Parallel()
	marker := filepath.Join(t.TempDir(), "dockerenv")
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	var stdout, stderr bytes.Buffer
	err := run(
		context.Background(),
		[]string{"--container-marker", marker, "unexpected"},
		strings.NewReader("complete the task"),
		&stdout,
		&stderr,
	)
	if err == nil || !strings.Contains(err.Error(), "unexpected positional arguments") {
		t.Fatalf("run() error = %v, want positional argument rejection", err)
	}
}
