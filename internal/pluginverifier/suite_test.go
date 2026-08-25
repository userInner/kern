package pluginverifier

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/userInner/kern/internal/operation"
	"github.com/userInner/kern/internal/plugin"
	"github.com/userInner/kern/internal/task"
	"github.com/userInner/kern/internal/verification"
	"github.com/userInner/kern/internal/workspace"
)

func TestSuiteVerifiesExactCoreCommandEvidence(t *testing.T) {
	installed := verifierPlugin(t)
	exitCode := 0
	item := task.Task{ID: "task-1", ActiveAttemptID: "attempt-1"}
	repo := &fakeRepository{
		usage: []plugin.Usage{{
			SchemaVersion: plugin.SchemaVersion,
			TaskID:        item.ID, AttemptID: item.ActiveAttemptID,
			PluginID: installed.ID, Version: installed.Version, Digest: installed.Digest,
			Reason: "manual_enable", Resources: json.RawMessage(`{"verifiers":["go.test"]}`),
		}},
		operations: []operation.Record{{
			Operation: operation.Operation{
				ID: "operation-1", Tool: "execute", Status: operation.StatusSucceeded,
				Input: json.RawMessage(`{"argv":["go","test","./..."],"cwd":"."}`),
			},
			Result: &operation.Result{ExitCode: &exitCode},
		}},
	}
	suite, err := New(
		fakeCore{},
		repo,
		fakeGetter{item: installed},
		fakeWorkspace{},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	report, err := suite.Verify(t.Context(), item, "done")
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if report.Status != verification.StatusVerified || len(report.Checks) != 2 ||
		report.Checks[1].Verifier != "plugin:dev.kern.go-expert:go.test" ||
		report.Checks[1].Status != verification.StatusPassed {
		t.Fatalf("report = %#v", report)
	}

	repo.operations = nil
	report, err = suite.Verify(t.Context(), item, "done")
	if err != nil {
		t.Fatalf("Verify(missing command) error = %v", err)
	}
	if report.Status != verification.StatusPartial || report.Checks[1].Status != verification.StatusNotRun {
		t.Fatalf("missing command report = %#v", report)
	}
}

type fakeCore struct{}

func (fakeCore) Verify(context.Context, task.Task, string) (verification.Report, error) {
	return verification.Report{
		Checks: []verification.CheckResult{{
			Verifier: "core.result", Status: verification.StatusPassed, Required: true,
			Summary:  "Core passed.",
			Evidence: []verification.Evidence{{Kind: "task_result", Ref: "task:task-1", Summary: "Core passed."}},
		}},
		CreatedAt: time.Now().UTC(),
	}, nil
}

type fakeRepository struct {
	usage      []plugin.Usage
	operations []operation.Record
}

func (f *fakeRepository) ListAttemptPlugins(context.Context, string, string) ([]plugin.Usage, error) {
	return f.usage, nil
}

func (f *fakeRepository) ListAttemptOperations(context.Context, string, string) ([]operation.Record, error) {
	return f.operations, nil
}

type fakeGetter struct{ item plugin.Installed }

func (f fakeGetter) Get(context.Context, string) (plugin.Installed, error) { return f.item, nil }

type fakeWorkspace struct{}

func (fakeWorkspace) ReadFile(context.Context, string) (workspace.File, error) {
	return workspace.File{}, os.ErrNotExist
}

func verifierPlugin(t *testing.T) plugin.Installed {
	t.Helper()
	directory := t.TempDir()
	verifier := `{"schema_version":"1","id":"go.test","description":"Run all Go tests.","type":"command","required":true,"command":["go","test","./..."]}`
	if err := os.WriteFile(filepath.Join(directory, "verifier.json"), []byte(verifier), 0o600); err != nil {
		t.Fatalf("WriteFile(verifier) error = %v", err)
	}
	digest, err := plugin.PackageDigest(directory)
	if err != nil {
		t.Fatalf("PackageDigest() error = %v", err)
	}
	manifest := plugin.Manifest{
		SchemaVersion: plugin.SchemaVersion,
		ID:            "dev.kern.go-expert", Name: "Go Expert", Version: "0.1.0", Core: ">=0.1.0 <0.2.0",
		Entrypoints: plugin.Entrypoints{Verifiers: []string{"verifier.json"}},
		Permissions: plugin.Permissions{Process: []string{"go"}},
		Integrity:   plugin.Integrity{Files: digest},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, plugin.ManifestFile), encoded, 0o600); err != nil {
		t.Fatalf("WriteFile(manifest) error = %v", err)
	}
	return plugin.Installed{
		SchemaVersion: plugin.SchemaVersion,
		ID:            manifest.ID, Name: manifest.Name, Version: manifest.Version,
		Digest: digest, Manifest: manifest, InstallPath: directory,
	}
}
