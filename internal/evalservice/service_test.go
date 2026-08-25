package evalservice

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/userInner/kern/internal/evaluation"
	"github.com/userInner/kern/internal/plugin"
	"github.com/userInner/kern/internal/store/sqlite"
)

func TestServiceCompletesAndPersistsReport(t *testing.T) {
	store, service := openTestService(t)
	suiteRoot := writeServiceSuite(t, service.evalRoot, false)
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

func TestServiceCloseSerializesWithStartAndConcurrentCallers(t *testing.T) {
	database, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := &blockingCreateStore{
		Store: database, entered: make(chan struct{}), release: make(chan struct{}),
	}
	evalRoot := t.TempDir()
	service, err := New(t.Context(), Config{
		DataRoot: filepath.Join(t.TempDir(), "eval-data"), EvalRoot: evalRoot,
		Store: store, MaxActive: 1,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	relative := writeServiceSuite(t, evalRoot, false)
	type startResult struct {
		run evaluation.Run
		err error
	}
	startDone := make(chan startResult, 1)
	go func() {
		run, err := service.Start(t.Context(), StartInput{SuitePath: relative})
		startDone <- startResult{run: run, err: err}
	}()
	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Start() did not reach durable admission")
	}

	closeDone := make(chan struct{}, 2)
	go func() { service.Close(); closeDone <- struct{}{} }()
	go func() { service.Close(); closeDone <- struct{}{} }()
	select {
	case <-closeDone:
		t.Fatal("Close() returned before admitted Start() could launch")
	case <-time.After(100 * time.Millisecond):
	}
	close(store.release)
	result := <-startDone
	if result.err != nil || result.run.ID == "" {
		t.Fatalf("Start() = %#v, %v", result.run, result.err)
	}
	for range 2 {
		select {
		case <-closeDone:
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent Close() did not wait for shutdown")
		}
	}
	if _, err := service.Start(t.Context(), StartInput{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Start(after Close) error = %v, want ErrClosed", err)
	}
	if _, err := service.Resume(t.Context(), "missing"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Resume(after Close) error = %v, want ErrClosed", err)
	}
}

func TestServiceCloseSerializesWithResumeAdmission(t *testing.T) {
	database, service := openTestService(t)
	relative := writeServiceSuite(t, service.evalRoot, true)
	run, err := service.Start(t.Context(), StartInput{SuitePath: relative})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForRun(t, database, run.ID, evaluation.StatusRunning)
	if _, err := service.Pause(t.Context(), run.ID); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	store := &blockingResumeStore{
		Store: database, entered: make(chan struct{}), release: make(chan struct{}),
	}
	service.store = store
	resumeDone := make(chan error, 1)
	go func() {
		_, err := service.Resume(t.Context(), run.ID)
		resumeDone <- err
	}()
	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Resume() did not reach durable admission")
	}
	closeDone := make(chan struct{})
	go func() { service.Close(); close(closeDone) }()
	select {
	case <-closeDone:
		t.Fatal("Close() returned before admitted Resume() could launch")
	case <-time.After(100 * time.Millisecond):
	}
	close(store.release)
	if err := <-resumeDone; err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	select {
	case <-closeDone:
	case <-time.After(10 * time.Second):
		t.Fatal("Close() did not wait for resumed run shutdown")
	}
}

func TestServiceCancelsRunningEvaluation(t *testing.T) {
	store, service := openTestService(t)
	suiteRoot := writeServiceSuite(t, service.evalRoot, true)
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
	suiteRoot := writeServiceSuite(t, service.evalRoot, true)
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

func TestServiceResumeRequiresConfiguredEvaluationRoot(t *testing.T) {
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := New(t.Context(), Config{
		DataRoot: filepath.Join(t.TempDir(), "evals"),
		Store:    store,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer service.Close()

	if _, err := service.Resume(t.Context(), "paused-run"); err == nil ||
		!strings.Contains(err.Error(), "evaluation root is not configured") {
		t.Fatalf("Resume() error = %v", err)
	}
}

func TestServiceResumeUsesFrozenInputsWhenSourceChanges(t *testing.T) {
	store, service := openTestService(t)
	suiteRoot := writeServiceSuite(t, service.evalRoot, true)
	run, err := service.Start(t.Context(), StartInput{SuitePath: suiteRoot})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForRun(t, store, run.ID, evaluation.StatusRunning)
	if _, err := service.Pause(t.Context(), run.ID); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(service.evalRoot, suiteRoot, "prompt.md"), []byte("changed after pause\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resume(t.Context(), run.ID); err != nil {
		t.Fatalf("Resume() error = %v after source prompt mutation", err)
	}
	completed := waitForRun(t, store, run.ID, evaluation.StatusCompleted)
	if completed.ConfigDigest != run.ConfigDigest {
		t.Fatalf("resumed config digest = %q, want %q", completed.ConfigDigest, run.ConfigDigest)
	}
}

func TestServiceRefusesResumeWhenFrozenSnapshotIsTampered(t *testing.T) {
	store, service := openTestService(t)
	relative := writeServiceSuite(t, service.evalRoot, true)
	run, err := service.Start(t.Context(), StartInput{SuitePath: relative})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForRun(t, store, run.ID, evaluation.StatusRunning)
	if _, err := service.Pause(t.Context(), run.ID); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(service.inputRoot, run.ID, "cases", "go.service-case", "prompt.txt"),
		[]byte("tampered frozen prompt\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resume(t.Context(), run.ID); err == nil ||
		!strings.Contains(err.Error(), "changed while paused") {
		t.Fatalf("Resume(tampered snapshot) error = %v", err)
	}
	paused, err := store.GetEvalRun(t.Context(), run.ID)
	if err != nil || paused.Status != evaluation.StatusPaused {
		t.Fatalf("GetEvalRun() = %#v, %v", paused, err)
	}
}

func TestServiceRetainsOpenedEvaluationRootAfterPathReplacement(t *testing.T) {
	store, service := openTestService(t)
	relative := writeServiceSuite(t, service.evalRoot, false)
	original := t.TempDir()
	if err := os.Remove(original); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(service.evalRoot, original); err != nil {
		t.Skipf("Rename(evaluation root) unavailable: %v", err)
	}
	if err := os.Mkdir(service.evalRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	writeServiceSuite(t, service.evalRoot, false)
	if err := os.WriteFile(
		filepath.Join(service.evalRoot, relative, "suite.json"),
		[]byte(`{"schema_version":"attacker"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	run, err := service.Start(t.Context(), StartInput{SuitePath: relative})
	if err != nil {
		t.Fatalf("Start() through retained root error = %v", err)
	}
	waitForRun(t, store, run.ID, evaluation.StatusCompleted)
	service.Close()
}

func TestServiceSnapshotsFixtureBeforeAsynchronousExecution(t *testing.T) {
	store, service := openTestService(t)
	relative := writeServiceSuite(t, service.evalRoot, false)
	run, err := service.Start(t.Context(), StartInput{SuitePath: relative})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := os.Remove(filepath.Join(service.evalRoot, relative, "fixture", "go.mod")); err != nil {
		t.Fatal(err)
	}
	waitForRun(t, store, run.ID, evaluation.StatusCompleted)
}

func TestServiceResolvesArbitraryInstalledPlugin(t *testing.T) {
	store, service := openTestService(t)
	relative := writeServiceSuite(t, service.evalRoot, false)
	pluginID := "org.example.second-expert"
	installed := writeInstalledPlugin(t, pluginID)
	service.pluginResolver = staticPluginResolver{installed.ID: installed}
	suiteName := filepath.Join(service.evalRoot, relative, "suite.json")
	data, err := os.ReadFile(suiteName)
	if err != nil {
		t.Fatal(err)
	}
	var suite evaluation.Suite
	if err := json.Unmarshal(data, &suite); err != nil {
		t.Fatal(err)
	}
	suite.Variants[0].Plugins = []string{pluginID + "@0.1.0"}
	data, err = json.Marshal(suite)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(suiteName, data, 0o600); err != nil {
		t.Fatal(err)
	}

	run, err := service.Start(t.Context(), StartInput{SuitePath: relative})
	if err != nil {
		t.Fatalf("Start(installed plugin) error = %v", err)
	}
	waitForRun(t, store, run.ID, evaluation.StatusCompleted)
}

func TestServiceRejectsSuitePathsOutsideConfiguredRoot(t *testing.T) {
	store, service := openTestService(t)
	outside := t.TempDir()
	outsideRelative := writeServiceSuite(t, outside, false)
	absoluteOutside := filepath.Join(outside, outsideRelative)

	for _, test := range []struct {
		name string
		path string
	}{
		{name: "absolute", path: absoluteOutside},
		{name: "parent traversal", path: "../" + outsideRelative},
		{name: "embedded traversal", path: "nested/../" + outsideRelative},
		{name: "backslash", path: `nested\suite`},
		{name: "NUL", path: "suite\x00ignored"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := service.Start(t.Context(), StartInput{SuitePath: test.path}); err == nil {
				t.Fatalf("Start(%q) error = nil", test.path)
			}
		})
	}
	runs, err := store.ListEvalRuns(t.Context(), 10)
	if err != nil {
		t.Fatalf("ListEvalRuns() error = %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("rejected suite paths persisted %d runs", len(runs))
	}
}

func TestServiceFailsClosedWithoutConfiguredEvaluationRoot(t *testing.T) {
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := New(t.Context(), Config{DataRoot: t.TempDir(), Store: store})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer service.Close()

	if _, err := service.Start(t.Context(), StartInput{SuitePath: "suite"}); err == nil ||
		!strings.Contains(err.Error(), "evaluation root is not configured") {
		t.Fatalf("Start() error = %v", err)
	}
}

func TestServiceBorrowsConfiguredEvaluationRootHandle(t *testing.T) {
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	service, err := New(t.Context(), Config{
		DataRoot: t.TempDir(), EvalRootHandle: root, Store: store,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	service.Close()
	if _, err := root.Stat("."); err != nil {
		t.Fatalf("borrowed evaluation root was closed: %v", err)
	}
}

func TestServiceRejectsOverlappingEvaluationAndDataRoots(t *testing.T) {
	tests := []struct {
		name  string
		roots func(t *testing.T) (string, string)
	}{
		{
			name: "same directory",
			roots: func(t *testing.T) (string, string) {
				root := t.TempDir()
				return root, root
			},
		},
		{
			name: "data beneath evaluation root",
			roots: func(t *testing.T) (string, string) {
				evalRoot := t.TempDir()
				return evalRoot, filepath.Join(evalRoot, "private-data")
			},
		},
		{
			name: "evaluation root beneath data",
			roots: func(t *testing.T) (string, string) {
				dataRoot := t.TempDir()
				evalRoot := filepath.Join(dataRoot, "source-evals")
				if err := os.Mkdir(evalRoot, 0o700); err != nil {
					t.Fatal(err)
				}
				return evalRoot, dataRoot
			},
		},
		{
			name: "symlink alias",
			roots: func(t *testing.T) (string, string) {
				evalRoot := t.TempDir()
				alias := filepath.Join(t.TempDir(), "eval-alias")
				if err := os.Symlink(evalRoot, alias); err != nil {
					t.Skipf("Symlink() unavailable: %v", err)
				}
				return evalRoot, alias
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evalRoot, dataRoot := test.roots(t)
			store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			service, err := New(t.Context(), Config{
				DataRoot: dataRoot, EvalRoot: evalRoot, Store: store,
			})
			if service != nil {
				service.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "must not overlap") {
				t.Fatalf("New(overlapping roots) error = %v", err)
			}
		})
	}
}

func TestServiceRejectsSnapshotWorkspaceAlias(t *testing.T) {
	dataRoot := t.TempDir()
	inputs := filepath.Join(dataRoot, "inputs")
	if err := os.Mkdir(inputs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inputs, filepath.Join(dataRoot, "workspaces")); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := New(t.Context(), Config{
		DataRoot: dataRoot, EvalRoot: t.TempDir(), Store: store,
	})
	if service != nil {
		service.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "unsafe directory") {
		t.Fatalf("New(snapshot/workspace alias) error = %v", err)
	}
}

func TestServiceRejectsMainAgentWorkspaceOverlaps(t *testing.T) {
	tests := []struct {
		name      string
		configure func(t *testing.T) (evalRoot, dataRoot, writableRoot string)
		want      string
	}{
		{
			name: "main workspace overlaps trusted suites",
			configure: func(t *testing.T) (string, string, string) {
				evalRoot := t.TempDir()
				writableRoot := filepath.Join(evalRoot, "primary-workspace")
				if err := os.Mkdir(writableRoot, 0o700); err != nil {
					t.Fatal(err)
				}
				return evalRoot, t.TempDir(), writableRoot
			},
			want: "evaluation root and writable agent roots",
		},
		{
			name: "main workspace overlaps frozen run data",
			configure: func(t *testing.T) (string, string, string) {
				dataRoot := t.TempDir()
				writableRoot := filepath.Join(dataRoot, "primary-workspace")
				if err := os.Mkdir(writableRoot, 0o700); err != nil {
					t.Fatal(err)
				}
				return t.TempDir(), dataRoot, writableRoot
			},
			want: "data root and writable agent roots",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evalRoot, dataRoot, writableRoot := test.configure(t)
			store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			service, err := New(t.Context(), Config{
				DataRoot: dataRoot, EvalRoot: evalRoot, Store: store,
				WritableRoots: []string{writableRoot},
			})
			if service != nil {
				service.Close()
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("New(overlapping main workspace) error = %v", err)
			}
		})
	}
}

func TestServiceDoesNotRequireWritableRootIsolationWhenEvaluationIsDisabled(t *testing.T) {
	dataRoot := t.TempDir()
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := New(t.Context(), Config{
		DataRoot: dataRoot, Store: store, WritableRoots: []string{dataRoot},
	})
	if err != nil {
		t.Fatalf("New(no EvalRoot) error = %v", err)
	}
	service.Close()
}

func TestServiceDoesNotDiscoverPluginsBesideSuite(t *testing.T) {
	_, service := openTestService(t)
	relative := writeServiceSuite(t, service.evalRoot, false)
	suiteRoot := filepath.Join(service.evalRoot, relative)
	data, err := os.ReadFile(filepath.Join(suiteRoot, "suite.json"))
	if err != nil {
		t.Fatal(err)
	}
	var suite evaluation.Suite
	if err := json.Unmarshal(data, &suite); err != nil {
		t.Fatal(err)
	}
	suite.Variants[0].Plugins = []string{"dev.kern.go-expert@0.1.0"}
	data, err = json.Marshal(suite)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suiteRoot, "suite.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	adjacent := filepath.Join(suiteRoot, "plugins", "go-expert")
	if err := os.CopyFS(adjacent, os.DirFS(filepath.Join("..", "..", "plugins", "go-expert"))); err != nil {
		t.Fatalf("CopyFS(plugin) error = %v", err)
	}

	if _, err := service.Start(t.Context(), StartInput{SuitePath: relative}); err == nil ||
		!strings.Contains(err.Error(), "no trusted source configured") {
		t.Fatalf("Start(adjacent plugin) error = %v", err)
	}
}

func openTestService(t *testing.T) (*sqlite.Store, *Service) {
	t.Helper()
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "kern.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	evalRoot := t.TempDir()
	service, err := New(t.Context(), Config{
		DataRoot: filepath.Join(t.TempDir(), "evals"),
		EvalRoot: evalRoot,
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

func writeServiceSuite(t *testing.T, evalRoot string, slow bool) string {
	t.Helper()
	const relative = "service-suite"
	root := filepath.Join(evalRoot, relative)
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
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
		testSource := "package serviceeval\n\nimport (\"testing\"; \"time\")\n\nfunc TestSlow(t *testing.T) { time.Sleep(time.Second) }\n"
		if err := os.WriteFile(filepath.Join(fixture, "slow_test.go"), []byte(testSource), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	suite := evaluation.Suite{
		SchemaVersion: evaluation.SchemaVersion,
		ID:            "kern.go.service", Name: "Service test", Version: "1.0.0",
		Defaults: evaluation.Defaults{
			TimeoutMS: 60_000, TokenBudget: 1_000, CostBudgetMicros: 0,
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
	return relative
}

func waitForRun(
	t *testing.T,
	store *sqlite.Store,
	runID string,
	want evaluation.Status,
) evaluation.Run {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
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

type staticPluginResolver map[string]plugin.Installed

type blockingCreateStore struct {
	Store
	entered chan struct{}
	release chan struct{}
}

type blockingResumeStore struct {
	Store
	entered chan struct{}
	release chan struct{}
}

func (s *blockingCreateStore) CreateEvalRun(
	ctx context.Context,
	run evaluation.Run,
	config evaluation.RunConfig,
) error {
	close(s.entered)
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.Store.CreateEvalRun(ctx, run, config)
}

func (s *blockingResumeStore) ResumeEvalRun(ctx context.Context, runID string) error {
	close(s.entered)
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.Store.ResumeEvalRun(ctx, runID)
}

func (r staticPluginResolver) Get(_ context.Context, pluginID string) (plugin.Installed, error) {
	item, ok := r[pluginID]
	if !ok {
		return plugin.Installed{}, plugin.ErrNotFound
	}
	return item, nil
}

func writeInstalledPlugin(t *testing.T, pluginID string) plugin.Installed {
	t.Helper()
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "knowledge"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(directory, "knowledge", "guide.md"),
		[]byte("Use deterministic evidence.\n"),
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
		ID:            pluginID, Name: "Installed Expert", Version: "0.1.0", Core: ">=0.1.0 <0.2.0",
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
	return plugin.Installed{
		SchemaVersion: plugin.SchemaVersion,
		ID:            pluginID, Name: manifest.Name, Version: manifest.Version,
		Digest: digest, Manifest: manifest, InstallPath: directory,
	}
}
