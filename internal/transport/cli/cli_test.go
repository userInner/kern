package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/userInner/kern/internal/configuration"
	"github.com/userInner/kern/internal/evaluation"
	"github.com/userInner/kern/internal/plugin"
)

func TestBudgetFlagsConfig(t *testing.T) {
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	budgets := bindBudgetFlags(flags, configuration.Defaults().Runtime)
	if err := flags.Parse([]string{
		"--max-turns", "7",
		"--max-tool-calls", "9",
		"--max-tokens", "1234",
		"--max-cost-usd", "1.25",
		"--max-duration", "3m",
	}); err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	config, err := budgets.config()
	if err != nil {
		t.Fatalf("config() error = %v", err)
	}
	if config.MaxTurns != 7 || config.MaxToolCalls != 9 || config.MaxTokens != 1234 ||
		config.MaxCostMicros != 1_250_000 || config.MaxTaskDuration != 3*time.Minute {
		t.Fatalf("config() = %#v", config)
	}
}

func TestAttachProductionAdaptersToBudgetConfig(t *testing.T) {
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	budgets := bindBudgetFlags(flags, configuration.Defaults().Runtime)
	config, err := budgets.config()
	if err != nil {
		t.Fatalf("config() error = %v", err)
	}
	if config.SecretVault != nil || config.WASMSandbox != nil {
		t.Fatal("budget config unexpectedly contains production adapters before composition")
	}

	attachProductionAdapters(&config)
	if config.SecretVault == nil || config.WASMSandbox == nil {
		t.Fatalf("production adapters missing: %#v", config)
	}
}

func TestBudgetFlagsRejectInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "negative turns", args: []string{"--max-turns", "-1"}},
		{name: "negative duration", args: []string{"--max-duration", "-1s"}},
		{name: "not a number cost", args: []string{"--max-cost-usd", "NaN"}},
		{name: "infinite cost", args: []string{"--max-cost-usd", "+Inf"}},
		{name: "overflowing cost", args: []string{"--max-cost-usd", "1e30"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			flags := flag.NewFlagSet("test", flag.ContinueOnError)
			budgets := bindBudgetFlags(flags, configuration.Defaults().Runtime)
			if err := flags.Parse(test.args); err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if _, err := budgets.config(); err == nil {
				t.Fatal("config() error = nil")
			}
		})
	}
}

func TestBudgetFlagsAcceptMaximumFiniteCost(t *testing.T) {
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	budgets := bindBudgetFlags(flags, configuration.Defaults().Runtime)
	*budgets.maxCostUSD = float64(math.MaxInt64) / 1_000_000
	if _, err := budgets.config(); err != nil {
		t.Fatalf("config() error = %v", err)
	}
}

func TestExecuteTaskCommandsAndDoctor(t *testing.T) {
	t.Setenv("KERN_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("KERN_MODEL_BASE_URL", "")
	t.Setenv("KERN_MODEL", "")
	dataDir := t.TempDir()
	workspaceDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	exitCode := Execute(context.Background(), []string{
		"run",
		"--data-dir", dataDir,
		"--workspace", workspaceDir,
		"--output", "json",
		"prove CLI persistence",
	}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("run exit=%d stderr=%s", exitCode, stderr.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("run output=%s error=%v", stdout.String(), err)
	}

	stdout.Reset()
	stderr.Reset()
	exitCode = Execute(context.Background(), []string{
		"task", "list",
		"--data-dir", dataDir,
		"--workspace", workspaceDir,
		"--output", "jsonl",
	}, &stdout, &stderr)
	if exitCode != 0 || !strings.Contains(stdout.String(), created.ID) {
		t.Fatalf("task list exit=%d stdout=%s stderr=%s", exitCode, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	exitCode = Execute(context.Background(), []string{
		"task", "show",
		"--data-dir", dataDir,
		"--workspace", workspaceDir,
		"--output", "json",
		created.ID,
	}, &stdout, &stderr)
	if exitCode != 0 || !strings.Contains(stdout.String(), `"status":"completed"`) {
		t.Fatalf("task show exit=%d stdout=%s stderr=%s", exitCode, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	exitCode = Execute(context.Background(), []string{
		"doctor",
		"--data-dir", dataDir,
		"--workspace", workspaceDir,
		"--output", "json",
	}, &stdout, &stderr)
	var doctor struct {
		Status                   string `json:"status"`
		ProductionAdaptersReady  bool   `json:"production_adapters_ready"`
		ModelExecutionConfigured bool   `json:"model_execution_configured"`
		Capabilities             struct {
			WritableCredentialStore bool `json:"writable_credential_store"`
			WASMPluginRuntime       bool `json:"wasm_plugin_runtime"`
		} `json:"capabilities"`
		Warnings []string `json:"warnings"`
	}
	decodeErr := json.Unmarshal(stdout.Bytes(), &doctor)
	if exitCode != 0 || decodeErr != nil || doctor.Status != "ok" ||
		doctor.ModelExecutionConfigured || !doctor.Capabilities.WASMPluginRuntime ||
		doctor.ProductionAdaptersReady != doctor.Capabilities.WritableCredentialStore ||
		!strings.Contains(strings.Join(doctor.Warnings, ","), "model_execution_unconfigured") {
		t.Fatalf("doctor exit=%d stdout=%s stderr=%s", exitCode, stdout.String(), stderr.String())
	}
}

func TestExecuteChatContinuesOneDurableTask(t *testing.T) {
	t.Setenv("KERN_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("KERN_MODEL_BASE_URL", "")
	t.Setenv("KERN_MODEL", "")
	dataDir := t.TempDir()
	workspaceDir := t.TempDir()
	stdin := strings.NewReader("first message\nfollow-up message\n/exit\n")
	var stdout, stderr bytes.Buffer
	exitCode := ExecuteWithInput(context.Background(), []string{
		"chat",
		"--data-dir", dataDir,
		"--workspace", workspaceDir,
		"--output", "jsonl",
	}, stdin, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("chat exit=%d stdout=%s stderr=%s", exitCode, stdout.String(), stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("chat output lines=%d, want 2; output=%s", len(lines), stdout.String())
	}
	var first, second struct {
		ID              string `json:"id"`
		ActiveAttemptID string `json:"active_attempt_id"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("first chat output: %v", err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("second chat output: %v", err)
	}
	if first.ID == "" || first.ID != second.ID || first.ActiveAttemptID == second.ActiveAttemptID {
		t.Fatalf("chat attempts first=%#v second=%#v", first, second)
	}
}

func TestExecuteConfigSetGetAndEnvironmentPrecedence(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("KERN_CONFIG", configPath)
	var stdout, stderr bytes.Buffer
	exitCode := Execute(context.Background(), []string{
		"config", "set", "runtime.max_turns", "19",
	}, &stdout, &stderr)
	if exitCode != 0 || !strings.Contains(stdout.String(), "runtime.max_turns=19") {
		t.Fatalf("config set exit=%d stdout=%s stderr=%s", exitCode, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	exitCode = Execute(context.Background(), []string{
		"config", "get", "--output", "text", "runtime.max_turns",
	}, &stdout, &stderr)
	if exitCode != 0 || strings.TrimSpace(stdout.String()) != "19" {
		t.Fatalf("config get exit=%d stdout=%s stderr=%s", exitCode, stdout.String(), stderr.String())
	}
	t.Setenv("KERN_MAX_TURNS", "27")
	stdout.Reset()
	stderr.Reset()
	exitCode = Execute(context.Background(), []string{
		"config", "get", "--effective", "--output", "text", "runtime.max_turns",
	}, &stdout, &stderr)
	if exitCode != 0 || strings.TrimSpace(stdout.String()) != "27" {
		t.Fatalf("effective config get exit=%d stdout=%s stderr=%s", exitCode, stdout.String(), stderr.String())
	}
}

func TestExecutePluginLifecycle(t *testing.T) {
	t.Setenv("KERN_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("KERN_MODEL_BASE_URL", "")
	t.Setenv("KERN_MODEL", "")
	dataDir := t.TempDir()
	workspaceDir := t.TempDir()
	source := createCLIPluginPackage(t)
	var stdout, stderr bytes.Buffer
	exitCode := Execute(context.Background(), []string{
		"plugin", "install",
		"--data-dir", dataDir,
		"--workspace", workspaceDir,
		"--output", "json",
		"--enable",
		source,
	}, &stdout, &stderr)
	if exitCode != 0 || !strings.Contains(stdout.String(), `"id":"dev.kern.cli-test"`) ||
		!strings.Contains(stdout.String(), `"enabled":true`) {
		t.Fatalf("plugin install exit=%d stdout=%s stderr=%s", exitCode, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	exitCode = Execute(context.Background(), []string{
		"plugin", "list",
		"--data-dir", dataDir,
		"--workspace", workspaceDir,
		"--output", "json",
	}, &stdout, &stderr)
	if exitCode != 0 || !strings.Contains(stdout.String(), "dev.kern.cli-test") {
		t.Fatalf("plugin list exit=%d stdout=%s stderr=%s", exitCode, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	exitCode = Execute(context.Background(), []string{
		"plugin", "remove",
		"--data-dir", dataDir,
		"--workspace", workspaceDir,
		"dev.kern.cli-test",
	}, &stdout, &stderr)
	if exitCode != 0 || !strings.Contains(stdout.String(), "removed") {
		t.Fatalf("plugin remove exit=%d stdout=%s stderr=%s", exitCode, stdout.String(), stderr.String())
	}
}

func TestExecutePluginDigest(t *testing.T) {
	t.Setenv("KERN_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	directory := createCLIPluginPackage(t)
	want, err := plugin.PackageDigest(directory)
	if err != nil {
		t.Fatalf("PackageDigest() error = %v", err)
	}
	var stdout, stderr bytes.Buffer
	exitCode := Execute(context.Background(), []string{
		"plugin", "digest", "--output", "json", directory,
	}, &stdout, &stderr)
	if exitCode != 0 || !strings.Contains(stdout.String(), `"digest":"`+want+`"`) {
		t.Fatalf("plugin digest exit=%d stdout=%s stderr=%s", exitCode, stdout.String(), stderr.String())
	}
}

func TestExecuteEvalRunAndShow(t *testing.T) {
	t.Setenv("KERN_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("KERN_MODEL_BASE_URL", "")
	t.Setenv("KERN_MODEL", "")
	suiteRoot := t.TempDir()
	fixture := filepath.Join(suiteRoot, "fixture")
	if err := os.Mkdir(fixture, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture, "go.mod"), []byte("module example.test/eval\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(fixture) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(suiteRoot, "prompt.md"), []byte("Inspect the project and report its state.\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(prompt) error = %v", err)
	}
	suite := evaluation.Suite{
		SchemaVersion: evaluation.SchemaVersion,
		ID:            "kern.go.smoke", Name: "Go smoke", Version: "0.1.0",
		Defaults: evaluation.Defaults{
			TimeoutMS: 30_000, TokenBudget: 1_000, CostBudgetMicros: 100_000,
			Retries: 0, WorkerCount: 1, AllowedCommands: []string{"go"},
		},
		Variants: []evaluation.Variant{{ID: "general.base"}},
		Cases: []evaluation.Case{{
			ID: "go.module-exists", Fixture: "fixture", Prompt: "prompt.md",
			Graders: []evaluation.Grader{{ID: "go.mod-exists", Type: "file_exists", Required: true, Path: "go.mod"}},
		}},
	}
	encoded, err := json.Marshal(suite)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(suiteRoot, "suite.json"), encoded, 0o600); err != nil {
		t.Fatalf("WriteFile(suite) error = %v", err)
	}
	dataDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	exitCode := Execute(context.Background(), []string{
		"eval", "run", "--data-dir", dataDir, "--output", "json", suiteRoot,
	}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("eval run exit=%d stdout=%s stderr=%s", exitCode, stdout.String(), stderr.String())
	}
	var report evaluation.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil || report.RunID == "" ||
		len(report.Variants) != 1 || report.Variants[0].Passed != 1 {
		t.Fatalf("eval report = %#v, %v; stderr=%s", report, err, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	exitCode = Execute(context.Background(), []string{
		"eval", "show", "--data-dir", dataDir, "--output", "text", report.RunID,
	}, &stdout, &stderr)
	if exitCode != 0 || !strings.Contains(stdout.String(), "general.base") {
		t.Fatalf("eval show exit=%d stdout=%s stderr=%s", exitCode, stdout.String(), stderr.String())
	}
}

func TestExecuteEvalValidateChecksShippedAssetsAndPlugin(t *testing.T) {
	t.Setenv("KERN_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("KERN_MODEL", "")
	var stdout, stderr bytes.Buffer
	exitCode := Execute(context.Background(), []string{
		"eval", "validate", "--output", "json", "--variants", "expert.go", "../../../evals/go",
	}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("eval validate exit=%d stdout=%s stderr=%s", exitCode, stdout.String(), stderr.String())
	}
	var report evalValidationReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("Unmarshal() error = %v; output=%s", err, stdout.String())
	}
	if !report.Valid || report.SuiteID != "kern.go.reference" || report.CaseCount != 30 ||
		report.VariantCount != 1 || !strings.HasPrefix(report.ConfigDigest, "sha256:") ||
		len(report.Reproducibility.Variants) != 1 ||
		report.Reproducibility.Variants[0].PluginDigests["dev.kern.go-expert"] == "" {
		t.Fatalf("eval validation report = %#v", report)
	}
}

func TestExecuteEvalCompareRequiresTwoEffectiveVariants(t *testing.T) {
	t.Setenv("KERN_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	root := t.TempDir()
	suitePath := filepath.Join(root, "suite.json")
	if err := os.WriteFile(suitePath, []byte(`{
  "schema_version": "1",
  "id": "test.single",
  "name": "single variant",
  "version": "1.0.0",
  "defaults": {
    "timeout_ms": 1000,
    "token_budget": 100,
    "cost_budget_micros": 0,
    "retries": 0,
    "worker_count": 1,
    "allowed_commands": ["go"]
  },
  "variants": [{"id": "base.only", "plugins": []}],
  "cases": [{
    "id": "case.one",
    "fixture": "fixture",
    "prompt": "prompt.md",
    "graders": [{"id": "test.all", "type": "command", "required": true, "command": ["go", "test", "./..."]}]
  }]
}`), 0o600); err != nil {
		t.Fatalf("WriteFile(suite) error = %v", err)
	}

	var stdout, stderr bytes.Buffer
	exitCode := Execute(context.Background(), []string{"eval", "compare", suitePath}, &stdout, &stderr)
	if exitCode == 0 || !strings.Contains(stderr.String(), "requires at least two variants") {
		t.Fatalf("Execute() exit=%d stderr=%s, want compare variant validation", exitCode, stderr.String())
	}
}

func TestSuiteUsesAgentHonorsVariantSelection(t *testing.T) {
	suite := evaluation.Suite{Variants: []evaluation.Variant{
		{ID: "general.base"},
		{ID: "external.codex", Agent: "codex"},
	}}
	if !suiteUsesAgent(suite, nil, "codex") || suiteUsesAgent(suite, []string{"general.base"}, "codex") ||
		!suiteUsesAgent(suite, []string{"external.codex"}, "codex") {
		t.Fatal("suiteUsesAgent() selection mismatch")
	}
}

func createCLIPluginPackage(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(directory, "knowledge"), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(directory, "knowledge", "principles.md"),
		[]byte("CLI plugin evidence\n"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(payload) error = %v", err)
	}
	digest, err := plugin.PackageDigest(directory)
	if err != nil {
		t.Fatalf("PackageDigest() error = %v", err)
	}
	manifest := plugin.Manifest{
		SchemaVersion: plugin.SchemaVersion,
		ID:            "dev.kern.cli-test",
		Name:          "CLI Test Expert",
		Version:       "0.1.0",
		Core:          ">=0.1.0 <0.2.0",
		Entrypoints: plugin.Entrypoints{
			Knowledge: []string{"knowledge/principles.md"},
		},
		Permissions: plugin.Permissions{Filesystem: []string{"workspace:read"}},
		Integrity:   plugin.Integrity{Files: digest},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("Marshal(manifest) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, plugin.ManifestFile), encoded, 0o600); err != nil {
		t.Fatalf("WriteFile(manifest) error = %v", err)
	}
	return directory
}
