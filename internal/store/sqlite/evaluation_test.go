package sqlite

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/userInner/kern/internal/evaluation"
)

func TestEvaluationRunPersistence(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	suite := persistenceTestSuite()
	if err := store.UpsertEvalSuite(t.Context(), suite); err != nil {
		t.Fatalf("UpsertEvalSuite() error = %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	run := evaluation.Run{
		SchemaVersion: evaluation.SchemaVersion,
		ID:            "run-test",
		SuiteID:       suite.ID,
		SuiteName:     suite.Name,
		SuiteVersion:  suite.Version,
		Status:        evaluation.StatusQueued,
		Variants:      []string{"general.base", "expert.go"},
		CaseCount:     2,
		CreatedAt:     now,
	}
	config := evaluation.RunConfig{SuitePath: "/fixtures/go", Variants: run.Variants}
	if err := store.CreateEvalRun(t.Context(), run, config); err != nil {
		t.Fatalf("CreateEvalRun() error = %v", err)
	}
	if err := store.StartEvalRun(t.Context(), run.ID, now); err != nil {
		t.Fatalf("StartEvalRun() error = %v", err)
	}
	stored, err := store.GetEvalRun(t.Context(), run.ID)
	if err != nil || stored.Status != evaluation.StatusRunning || stored.StartedAt == nil {
		t.Fatalf("GetEvalRun() = %#v, %v", stored, err)
	}

	results := []evaluation.CaseResult{
		{CaseID: "go.case-one", VariantID: "general.base", Attempt: 1, Passed: false, Score: 0, StartedAt: now, CompletedAt: now},
		{CaseID: "go.case-one", VariantID: "expert.go", Attempt: 1, TaskID: "task-1", Passed: true, Score: 1, StartedAt: now, CompletedAt: now},
	}
	if err := store.RecordEvalProgress(t.Context(), run.ID, 1, results[:1]); err != nil {
		t.Fatalf("RecordEvalProgress() error = %v", err)
	}
	progressed, err := store.GetEvalRun(t.Context(), run.ID)
	if err != nil || progressed.Status != evaluation.StatusRunning || progressed.CompletedCases != 1 {
		t.Fatalf("progressed GetEvalRun() = %#v, %v", progressed, err)
	}
	if err := store.PauseEvalRun(t.Context(), run.ID, "paused by user"); err != nil {
		t.Fatalf("PauseEvalRun() error = %v", err)
	}
	paused, err := store.GetEvalRun(t.Context(), run.ID)
	if err != nil || paused.Status != evaluation.StatusPaused || paused.CompletedCases != 1 || paused.CompletedAt != nil {
		t.Fatalf("paused GetEvalRun() = %#v, %v", paused, err)
	}
	loadedConfig, err := store.GetEvalRunConfig(t.Context(), run.ID)
	if err != nil || loadedConfig.SuitePath != config.SuitePath || len(loadedConfig.Variants) != 2 {
		t.Fatalf("GetEvalRunConfig() = %#v, %v", loadedConfig, err)
	}
	prior, err := store.ListEvalCaseResults(t.Context(), run.ID)
	if err != nil || len(prior) != 1 || prior[0].VariantID != "general.base" {
		t.Fatalf("ListEvalCaseResults() = %#v, %v", prior, err)
	}
	if err := store.ResumeEvalRun(t.Context(), run.ID); err != nil {
		t.Fatalf("ResumeEvalRun() error = %v", err)
	}
	if err := store.StartEvalRun(t.Context(), run.ID, now.Add(time.Minute)); err != nil {
		t.Fatalf("StartEvalRun(resumed) error = %v", err)
	}
	resumed, err := store.GetEvalRun(t.Context(), run.ID)
	if err != nil || resumed.StartedAt == nil || !resumed.StartedAt.Equal(now) {
		t.Fatalf("resumed GetEvalRun() = %#v, %v", resumed, err)
	}
	report := evaluation.BuildReport(run.ID, "sha256:test", suite, results, now, now.Add(time.Second))
	if err := store.CompleteEvalRun(t.Context(), report); err != nil {
		t.Fatalf("CompleteEvalRun() error = %v", err)
	}
	stored, err = store.GetEvalRun(t.Context(), run.ID)
	if err != nil || stored.Status != evaluation.StatusCompleted || stored.CompletedCases != 2 ||
		stored.ConfigDigest != "sha256:test" {
		t.Fatalf("completed GetEvalRun() = %#v, %v", stored, err)
	}
	loaded, err := store.GetEvalReport(t.Context(), run.ID)
	if err != nil || loaded.RunID != run.ID || len(loaded.Results) != 2 {
		t.Fatalf("GetEvalReport() = %#v, %v", loaded, err)
	}
	items, err := store.ListEvalRuns(t.Context(), 10)
	if err != nil || len(items) != 1 || items[0].ID != run.ID {
		t.Fatalf("ListEvalRuns() = %#v, %v", items, err)
	}
	var caseRows int
	if err := store.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM eval_cases WHERE run_id = ?", run.ID).Scan(&caseRows); err != nil || caseRows != 2 {
		t.Fatalf("eval case rows = %d, %v", caseRows, err)
	}
}

func TestEvaluationReportNotReadyAndCancellation(t *testing.T) {
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	suite := persistenceTestSuite()
	if err := store.UpsertEvalSuite(t.Context(), suite); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	run := evaluation.Run{
		SchemaVersion: evaluation.SchemaVersion,
		ID:            "run-cancel", SuiteID: suite.ID, SuiteName: suite.Name, SuiteVersion: suite.Version,
		Status: evaluation.StatusQueued, Variants: []string{"general.base"}, CaseCount: 1, CreatedAt: now,
	}
	if err := store.CreateEvalRun(t.Context(), run, evaluation.RunConfig{SuitePath: "suite.json"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetEvalReport(t.Context(), run.ID); !errors.Is(err, evaluation.ErrReportNotReady) {
		t.Fatalf("GetEvalReport() error = %v", err)
	}
	if err := store.FinishEvalRun(t.Context(), run.ID, evaluation.StatusCancelled, "cancelled by user", now); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetEvalRun(t.Context(), run.ID)
	if err != nil || stored.Status != evaluation.StatusCancelled {
		t.Fatalf("GetEvalRun() = %#v, %v", stored, err)
	}
}

func persistenceTestSuite() evaluation.Suite {
	return evaluation.Suite{
		SchemaVersion: evaluation.SchemaVersion,
		ID:            "kern.go.persistence", Name: "Persistence", Version: "1.0.0",
		Defaults: evaluation.Defaults{
			TimeoutMS: 1_000, TokenBudget: 100, CostBudgetMicros: 0,
			Retries: 0, WorkerCount: 1, AllowedCommands: []string{"go"},
		},
		Variants: []evaluation.Variant{{ID: "general.base"}, {ID: "expert.go"}},
		Cases: []evaluation.Case{{
			ID: "go.case-one", Fixture: "fixture", Prompt: "prompt.md",
			Graders: []evaluation.Grader{{ID: "go.test-all", Type: "command", Required: true, Command: []string{"go", "test", "./..."}}},
		}},
	}
}
