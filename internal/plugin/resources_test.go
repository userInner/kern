package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/userInner/kern/internal/executionphase"
)

func TestLoadBundleValidatesDeclarativeResources(t *testing.T) {
	directory := t.TempDir()
	files := map[string]string{
		"knowledge.md":  "Use context cancellation at API boundaries.\n",
		"workflow.json": `{"schema_version":"1","id":"go.fix","name":"Go fix","steps":[{"phase":"prepare","instruction":"Inspect go.mod."},{"phase":"verify","instruction":"Run tests."}]}`,
		"rules.json":    `{"schema_version":"1","rules":[{"id":"go.errors","description":"Wrap causal errors.","severity":"warning","phases":["execute"]}]}`,
		"verifier.json": `{"schema_version":"1","id":"go.test","description":"Run Go tests.","type":"command","required":true,"command":["go","test","./..."]}`,
		"tool.json":     `{"schema_version":"1","id":"package_report","description":"Inspect one Go package.","runtime":"subprocess","command":["go","version"],"input_schema":{"type":"object","additionalProperties":false}}`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	digest, err := PackageDigest(directory)
	if err != nil {
		t.Fatalf("PackageDigest() error = %v", err)
	}
	manifest := Manifest{
		SchemaVersion: SchemaVersion,
		ID:            "dev.kern.go",
		Name:          "Go Expert",
		Version:       "0.1.0",
		Core:          ">=0.1.0 <0.2.0",
		Entrypoints: Entrypoints{
			Knowledge: []string{"knowledge.md"},
			Workflows: []string{"workflow.json"},
			Rules:     []string{"rules.json"},
			Verifiers: []string{"verifier.json"},
			Tools:     []string{"tool.json"},
		},
		Permissions: Permissions{Process: []string{"go"}},
		Integrity:   Integrity{Files: digest},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, ManifestFile), encoded, 0o600); err != nil {
		t.Fatalf("WriteFile(manifest) error = %v", err)
	}
	bundle, err := LoadBundle(Installed{
		ID: manifest.ID, Version: manifest.Version, Manifest: manifest,
		InstallPath: directory, InstalledAt: time.Now(), UpdatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("LoadBundle() error = %v", err)
	}
	if len(bundle.Knowledge) != 1 || len(bundle.Workflows) != 1 ||
		len(bundle.Rules) != 1 || len(bundle.Verifiers) != 1 || len(bundle.Tools) != 1 ||
		bundle.Tools[0].ID != "package_report" {
		t.Fatalf("bundle = %#v", bundle)
	}
	context, err := bundle.ContextJSON()
	if err != nil || len(context) == 0 {
		t.Fatalf("ContextJSON() = %q, %v", context, err)
	}
}

func TestBundleContextIsStrictlyPhaseScoped(t *testing.T) {
	bundle := Bundle{
		PluginID:  "dev.kern.go",
		Version:   "0.1.0",
		Knowledge: []Knowledge{{Path: "knowledge.md", Content: "GLOBAL_KNOWLEDGE"}},
		Workflows: []Workflow{
			{
				SchemaVersion: SchemaVersion,
				ID:            "go.fix",
				Name:          "Fix",
				Intents:       []string{"code.fix"},
				Steps: []WorkflowStep{
					{Phase: "prepare", Instruction: "PREPARE_STEP"},
					{Phase: "execute", Instruction: "EXECUTE_STEP"},
					{Phase: "verify", Instruction: "VERIFY_STEP"},
				},
			},
			{
				SchemaVersion: SchemaVersion,
				ID:            "go.review",
				Name:          "Review",
				Intents:       []string{"code.review"},
				Steps:         []WorkflowStep{{Phase: "prepare", Instruction: "UNRELATED_WORKFLOW"}},
			},
		},
		Rules: []RuleSet{{SchemaVersion: SchemaVersion, Rules: []Rule{
			{ID: "prepare.rule", Description: "PREPARE_RULE", Severity: "error", Phases: []string{"prepare"}},
			{ID: "execute.rule", Description: "EXECUTE_RULE", Severity: "error", Phases: []string{"execute"}},
			{ID: "global.rule", Description: "GLOBAL_RULE", Severity: "warning"},
		}}},
		Verifiers: []VerifierSpec{{SchemaVersion: SchemaVersion, ID: "go.test", Description: "VERIFY_DECLARATION"}},
		Tools:     []ToolSpec{{SchemaVersion: SchemaVersion, ID: "package_report", Description: "EXECUTABLE_TOOL"}},
	}

	tests := []struct {
		phase   executionphase.Phase
		present []string
		absent  []string
	}{
		{
			phase:   executionphase.Prepare,
			present: []string{"GLOBAL_KNOWLEDGE", "PREPARE_STEP", "PREPARE_RULE", "GLOBAL_RULE"},
			absent:  []string{"EXECUTE_STEP", "VERIFY_STEP", "EXECUTE_RULE", "VERIFY_DECLARATION", "EXECUTABLE_TOOL", "UNRELATED_WORKFLOW"},
		},
		{
			phase:   executionphase.Execute,
			present: []string{"GLOBAL_KNOWLEDGE", "EXECUTE_STEP", "EXECUTE_RULE", "GLOBAL_RULE", "EXECUTABLE_TOOL"},
			absent:  []string{"PREPARE_STEP", "VERIFY_STEP", "PREPARE_RULE", "VERIFY_DECLARATION", "UNRELATED_WORKFLOW"},
		},
		{
			phase:   executionphase.Verify,
			present: []string{"GLOBAL_KNOWLEDGE", "VERIFY_STEP", "GLOBAL_RULE", "VERIFY_DECLARATION", "EXECUTABLE_TOOL"},
			absent:  []string{"PREPARE_STEP", "EXECUTE_STEP", "PREPARE_RULE", "EXECUTE_RULE", "UNRELATED_WORKFLOW"},
		},
	}
	for _, test := range tests {
		t.Run(string(test.phase), func(t *testing.T) {
			encoded, err := bundle.ContextJSONForPhase(test.phase, []string{"code.fix"})
			if err != nil {
				t.Fatalf("ContextJSONForPhase() error = %v", err)
			}
			content := string(encoded)
			for _, value := range test.present {
				if !strings.Contains(content, value) {
					t.Errorf("phase context omits %q: %s", value, content)
				}
			}
			for _, value := range test.absent {
				if strings.Contains(content, value) {
					t.Errorf("phase context leaked %q: %s", value, content)
				}
			}
		})
	}
}

func TestLoadBundleRejectsToolUsingUndeclaredProcess(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(directory, "tool.json"),
		[]byte(`{"schema_version":"1","id":"scan","description":"Scan files.","runtime":"subprocess","command":["scanner"],"input_schema":{"type":"object"}}`),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(tool) error = %v", err)
	}
	digest, err := PackageDigest(directory)
	if err != nil {
		t.Fatalf("PackageDigest() error = %v", err)
	}
	manifest := Manifest{
		SchemaVersion: SchemaVersion,
		ID:            "dev.kern.scan", Name: "Scanner", Version: "0.1.0", Core: ">=0.1.0 <0.2.0",
		Entrypoints: Entrypoints{Tools: []string{"tool.json"}},
		Integrity:   Integrity{Files: digest},
	}
	if _, err := LoadBundle(Installed{
		ID: manifest.ID, Version: manifest.Version, Manifest: manifest, InstallPath: directory,
	}); err == nil {
		t.Fatal("LoadBundle() error = nil")
	}
}

func TestShippedGoExpertPackage(t *testing.T) {
	directory := filepath.Join("..", "..", "plugins", "go-expert")
	manifest, _, err := LoadManifest(directory)
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}
	digest, err := VerifyPackage(directory, manifest)
	if err != nil {
		t.Fatalf("VerifyPackage() error = %v", err)
	}
	bundle, err := LoadBundle(Installed{
		ID:          manifest.ID,
		Version:     manifest.Version,
		Digest:      digest,
		Manifest:    manifest,
		InstallPath: directory,
	})
	if err != nil {
		t.Fatalf("LoadBundle() error = %v", err)
	}
	if len(bundle.Knowledge) != 1 || len(bundle.Workflows) != 3 ||
		len(bundle.Rules) != 1 || len(bundle.Rules[0].Rules) != 15 || len(bundle.Verifiers) != 3 {
		t.Fatalf("Go Expert bundle shape = knowledge:%d workflows:%d rule sets:%d rules:%d verifiers:%d",
			len(bundle.Knowledge), len(bundle.Workflows), len(bundle.Rules), len(bundle.Rules[0].Rules), len(bundle.Verifiers))
	}
}

func TestLoadBundleRejectsUnknownWorkflowFields(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(directory, "workflow.json"),
		[]byte(`{"schema_version":"1","id":"go.fix","name":"Go fix","steps":[],"unknown":true}`),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	digest, err := PackageDigest(directory)
	if err != nil {
		t.Fatalf("PackageDigest() error = %v", err)
	}
	manifest := Manifest{
		SchemaVersion: SchemaVersion,
		ID:            "dev.kern.go", Name: "Go Expert", Version: "0.1.0", Core: ">=0.1.0 <0.2.0",
		Entrypoints: Entrypoints{Workflows: []string{"workflow.json"}}, Integrity: Integrity{Files: digest},
	}
	encoded, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(directory, ManifestFile), encoded, 0o600); err != nil {
		t.Fatalf("WriteFile(manifest) error = %v", err)
	}
	if _, err := LoadBundle(Installed{ID: manifest.ID, Version: manifest.Version, Manifest: manifest, InstallPath: directory}); err == nil {
		t.Fatal("LoadBundle() error = nil")
	}
}
