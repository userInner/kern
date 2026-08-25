package evalservice

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/userInner/kern/internal/evaluation"
	"github.com/userInner/kern/internal/store/sqlite"
)

func TestServiceCompletesAndPersistsReport(t *testing.T) {
	store, service := openTestService(t)
	suiteRoot := writeServiceSuite(t, false)
	run, err := service.Start(t.Context(), StartInput{SuitePath: suiteRoot})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	completed := waitForRun(t, store, run.ID, evaluation.StatusCompleted)
	if completed.CompletedCases != 1 || completed.CaseCount != 1 {
		t.Fatalf("completed run = %#v", completed)
	}
	report, err := service.Report(t.Context(), run.ID)
	if err != nil || report.RunID != run.ID || len(report.Results) != 1 || !report.Results[0].Passed {
		t.Fatalf("Report() = %#v, %v", report, err)
	}
}

func TestServiceCancelsRunningEvaluation(t *testing.T) {
	store, service := openTestService(t)
	suiteRoot := writeServiceSuite(t, true)
	run, err := service.Start(t.Context(), StartInput{SuitePath: suiteRoot})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForRun(t, store, run.ID, evaluation.StatusRunning)
	if _, err := service.Cancel(t.Context(), run.ID); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	waitForRun(t, store, run.ID, evaluation.StatusCancelled)
}

func TestServicePausesAndResumesFromCleanIncompleteWorkspace(t *testing.T) {
	store, service := openTestService(t)
	suiteRoot := writeServiceSuite(t, true)
	run, err := service.Start(t.Context(), StartInput{SuitePath: suiteRoot})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForRun(t, store, run.ID, evaluation.StatusRunning)
	paused, err := service.Pause(t.Context(), run.ID)
	if err != nil || paused.Status != evaluation.StatusPaused || paused.ConfigDigest == "" {
		t.Fatalf("Pause() = %#v, %v", paused, err)
	}
	resumed, err := service.Resume(t.Context(), run.ID)
	if err != nil || resumed.Status != evaluation.StatusQueued {
		t.Fatalf("Resume() = %#v, %v", resumed, err)
	}
	completed := waitForRun(t, store, run.ID, evaluation.StatusCompleted)
	if completed.CompletedCases != 1 || completed.ConfigDigest != paused.ConfigDigest {
		t.Fatalf("completed resumed run = %#v", completed)
	}
}

func TestServiceRefusesResumeWhenEvaluationInputChanged(t *testing.T) {
	store, service := openTestService(t)
	suiteRoot := writeServiceSuite(t, true)
	run, err := service.Start(t.Context(), StartInput{SuitePath: suiteRoot})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForRun(t, store, run.ID, evaluation.StatusRunning)
	if _, err := service.Pause(t.Context(), run.ID); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(suiteRoot, "prompt.md"), []byte("changed after pause\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resume(t.Context(), run.ID); err == nil {
		t.Fatal("Resume() error = nil after prompt mutation")
	}
	paused, err := store.GetEvalRun(t.Context(), run.ID)
	if err != nil || paused.Status != evaluation.StatusPaused {
		t.Fatalf("GetEvalRun() = %#v, %v", paused, err)
	}
}

func openTestService(t *testing.T) (*sqlite.Store, *Service) {
	t.Helper()
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	service, err := New(t.Context(), Config{
		DataRoot: filepath.Join(t.TempDir(), "evals"),
		Store:    store, MaxActive: 1,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		_ = store.Close()
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() {
		service.Close()
		_ = store.Close()
	})
	return store, service
}

func writeServiceSuite(t *testing.T, slow bool) string {
	t.Helper()
	root := t.TempDir()
	fixture := filepath.Join(root, "fixture")
	if err := os.Mkdir(fixture, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "prompt.md"), []byte("Inspect this fixture and complete the task.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	graders := []evaluation.Grader{{ID: "file.exists", Type: "file_exists", Required: true, Path: "go.mod"}}
	if err := os.WriteFile(filepath.Join(fixture, "go.mod"), []byte("module example.test/serviceeval\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if slow {
		graders = []evaluation.Grader{{ID: "go.test-all", Type: "command", Required: true, Command: []string{"go", "test", "./..."}}}
		testSource := "package serviceeval\n\nimport (\"testing\"; \"time\")\n\nfunc TestSlow(t *testing.T) { time.Sleep(5 * time.Second) }\n"
		if err := os.WriteFile(filepath.Join(fixture, "slow_test.go"), []byte(testSource), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	suite := evaluation.Suite{
		SchemaVersion: evaluation.SchemaVersion,
		ID:            "kern.go.service", Name: "Service test", Version: "1.0.0",
		Defaults: evaluation.Defaults{
			TimeoutMS: 30_000, TokenBudget: 1_000, CostBudgetMicros: 0,
			Retries: 0, WorkerCount: 1, AllowedCommands: []string{"go"},
		},
		Variants: []evaluation.Variant{{ID: "general.base"}},
		Cases: []evaluation.Case{{
			ID: "go.service-case", Fixture: "fixture", Prompt: "prompt.md", Graders: graders,
		}},
	}
	data, err := json.Marshal(suite)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "suite.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func waitForRun(
	t *testing.T,
	store *sqlite.Store,
	runID string,
	want evaluation.Status,
) evaluation.Run {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		run, err := store.GetEvalRun(ctx, runID)
		if err != nil {
			t.Fatalf("GetEvalRun() error = %v", err)
		}
		if run.Status == want {
			return run
		}
		if run.Status.Terminal() && run.Status != want {
			t.Fatalf("run reached %s, want %s: %s", run.Status, want, run.ErrorMessage)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s; current=%s", want, run.Status)
		case <-ticker.C:
		}
	}
}
