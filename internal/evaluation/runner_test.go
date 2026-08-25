package evaluation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeEvalAgent struct{}

func (fakeEvalAgent) Run(_ context.Context, request AgentRequest) (AgentResult, error) {
	content := "base"
	if request.Variant.ID == "expert.go" || request.Attempt > 1 {
		content = "fixed"
	}
	if err := os.WriteFile(filepath.Join(request.WorkspaceDir, "result.txt"), []byte(content), 0o600); err != nil {
		return AgentResult{}, err
	}
	return AgentResult{
		TaskID:     "task-" + request.Variant.ID,
		TaskStatus: "completed",
		Usage:      UsageMetrics{InputTokens: len(request.Prompt), ToolCalls: 1},
	}, nil
}

type snapshotMutatingAgent struct {
	mu     sync.Mutex
	calls  int
	mutate func() error
}

func (a *snapshotMutatingAgent) Run(_ context.Context, request AgentRequest) (AgentResult, error) {
	a.mu.Lock()
	a.calls++
	call := a.calls
	a.mu.Unlock()
	if call == 1 {
		if err := a.mutate(); err != nil {
			return AgentResult{}, err
		}
	}
	if err := os.WriteFile(filepath.Join(request.WorkspaceDir, "result.txt"), []byte("fixed"), 0o600); err != nil {
		return AgentResult{}, err
	}
	return AgentResult{TaskID: "task-" + request.CaseID, TaskStatus: "completed"}, nil
}

type pairedStartAgent struct {
	mu            sync.Mutex
	starts        []AgentRequest
	firstPair     chan struct{}
	fastCompleted chan struct{}
	releaseSlow   chan struct{}
	once          sync.Once
}

func (a *pairedStartAgent) Run(ctx context.Context, request AgentRequest) (AgentResult, error) {
	a.mu.Lock()
	a.starts = append(a.starts, request)
	position := len(a.starts)
	if position == 2 {
		a.once.Do(func() { close(a.firstPair) })
	}
	a.mu.Unlock()

	if request.CaseID == "case.first" && request.Variant.ID == "expert.go" {
		select {
		case <-a.releaseSlow:
		case <-ctx.Done():
			return AgentResult{}, ctx.Err()
		}
	}
	if err := os.WriteFile(filepath.Join(request.WorkspaceDir, "result.txt"), []byte("fixed"), 0o600); err != nil {
		return AgentResult{}, err
	}
	if request.CaseID == "case.first" && request.Variant.ID == "general.base" {
		close(a.fastCompleted)
	}
	return AgentResult{TaskID: "task-" + request.Variant.ID, TaskStatus: "completed"}, nil
}

func TestRunnerStartsVariantsForTheSameCaseAsAPair(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"first", "second"} {
		fixture := filepath.Join(root, name)
		if err := os.Mkdir(fixture, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixture, "result.txt"), []byte("broken"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name+".md"), []byte("repair result"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	suite := validSuite()
	suite.Root = root
	suite.Defaults.WorkerCount = 2
	suite.Defaults.Retries = 0
	suite.Cases = []Case{
		{ID: "case.first", Fixture: "first", Prompt: "first.md", Graders: []Grader{{
			ID: "result.fixed", Type: "file_contains", Required: true, Path: "result.txt", Contains: "fixed",
		}}},
		{ID: "case.second", Fixture: "second", Prompt: "second.md", Graders: []Grader{{
			ID: "result.fixed", Type: "file_contains", Required: true, Path: "result.txt", Contains: "fixed",
		}}},
	}
	agent := &pairedStartAgent{
		firstPair: make(chan struct{}), fastCompleted: make(chan struct{}), releaseSlow: make(chan struct{}),
	}
	runner, err := NewRunner(t.TempDir(), agent, nil)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, runErr := runner.Run(t.Context(), suite, nil)
		done <- runErr
	}()
	select {
	case <-agent.firstPair:
	case <-time.After(5 * time.Second):
		t.Fatal("first evaluation pair did not start")
	}
	agent.mu.Lock()
	starts := append([]AgentRequest(nil), agent.starts...)
	agent.mu.Unlock()
	if len(starts) != 2 || starts[0].CaseID != "case.first" || starts[1].CaseID != "case.first" ||
		starts[0].Variant.ID == starts[1].Variant.ID {
		t.Fatalf("first starts = %#v, want both variants for case.first", starts)
	}
	select {
	case <-agent.fastCompleted:
	case <-time.After(5 * time.Second):
		t.Fatal("fast member of first pair did not complete")
	}
	time.Sleep(50 * time.Millisecond)
	agent.mu.Lock()
	starts = append([]AgentRequest(nil), agent.starts...)
	agent.mu.Unlock()
	if len(starts) != 2 {
		t.Fatalf("started %d jobs before the first pair completed, want 2", len(starts))
	}
	close(agent.releaseSlow)
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRunnerUsesIsolatedWorkspacesAndRetainsRetries(t *testing.T) {
	suiteRoot := t.TempDir()
	fixture := filepath.Join(suiteRoot, "fixture")
	if err := os.Mkdir(fixture, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture, "result.txt"), []byte("broken"), 0o600); err != nil {
		t.Fatalf("WriteFile(fixture) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(suiteRoot, "prompt.md"), []byte("repair result"), 0o600); err != nil {
		t.Fatalf("WriteFile(prompt) error = %v", err)
	}
	suite := validSuite()
	suite.Root = suiteRoot
	suite.Defaults.Retries = 1
	suite.Cases[0].Graders = []Grader{{
		ID: "result.fixed", Type: "file_contains", Required: true, Path: "result.txt", Contains: "fixed",
	}}
	runRoot := t.TempDir()
	runner, err := NewRunner(runRoot, fakeEvalAgent{}, nil)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	report, err := runner.Run(t.Context(), suite, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(report.Results) != 3 || len(report.Variants) != 2 || len(report.Comparisons) != 1 {
		t.Fatalf("report shape = results:%d variants:%d comparisons:%d", len(report.Results), len(report.Variants), len(report.Comparisons))
	}
	if report.Variants[0].Passed != 0 || report.Variants[1].Passed != 1 {
		t.Fatalf("first-attempt metrics = %#v", report.Variants)
	}
	if !strings.HasPrefix(report.ConfigDigest, "sha256:") {
		t.Fatalf("config digest = %q", report.ConfigDigest)
	}
	if report.Reproducibility.CoreVersion == "" || report.Reproducibility.GoVersion == "" ||
		report.Reproducibility.GOOS == "" || report.Reproducibility.GOARCH == "" ||
		len(report.Reproducibility.Inputs) != 1 || len(report.Reproducibility.Variants) != 2 {
		t.Fatalf("reproducibility = %#v", report.Reproducibility)
	}
	input := report.Reproducibility.Inputs[0]
	if input.CaseID != "go.test-case" || !strings.HasPrefix(input.PromptSHA256, "sha256:") ||
		!strings.HasPrefix(input.FixtureSHA256, "sha256:") {
		t.Fatalf("input digest = %#v", input)
	}
	baseFirst := filepath.Join(runRoot, report.RunID, "general.base", "go.test-case", "attempt-01", "result.txt")
	expertFirst := filepath.Join(runRoot, report.RunID, "expert.go", "go.test-case", "attempt-01", "result.txt")
	baseData, baseErr := os.ReadFile(baseFirst)
	expertData, expertErr := os.ReadFile(expertFirst)
	if baseErr != nil || expertErr != nil || string(baseData) != "base" || string(expertData) != "fixed" {
		t.Fatalf("isolated results = base:%q/%v expert:%q/%v", baseData, baseErr, expertData, expertErr)
	}
}

func TestSuiteDigestChangesWhenPromptOrFixtureChanges(t *testing.T) {
	root := t.TempDir()
	fixture := filepath.Join(root, "fixture")
	if err := os.Mkdir(fixture, 0o700); err != nil {
		t.Fatal(err)
	}
	promptPath := filepath.Join(root, "prompt.md")
	fixturePath := filepath.Join(fixture, "result.txt")
	if err := os.WriteFile(promptPath, []byte("first prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixturePath, []byte("first fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	suite := validSuite()
	suite.Root = root
	first, firstIdentity, err := suiteDigest(t.Context(), suite, suite.Variants)
	if err != nil {
		t.Fatalf("suiteDigest(first) error = %v", err)
	}
	if err := os.WriteFile(promptPath, []byte("second prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, secondIdentity, err := suiteDigest(t.Context(), suite, suite.Variants)
	if err != nil {
		t.Fatalf("suiteDigest(second) error = %v", err)
	}
	if first == second || firstIdentity.Inputs[0].PromptSHA256 == secondIdentity.Inputs[0].PromptSHA256 {
		t.Fatalf("prompt mutation did not change digest: %q", first)
	}
	if err := os.WriteFile(fixturePath, []byte("second fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	third, thirdIdentity, err := suiteDigest(t.Context(), suite, suite.Variants)
	if err != nil {
		t.Fatalf("suiteDigest(third) error = %v", err)
	}
	if second == third || secondIdentity.Inputs[0].FixtureSHA256 == thirdIdentity.Inputs[0].FixtureSHA256 {
		t.Fatalf("fixture mutation did not change digest: %q", second)
	}
}

func TestRunnerRejectsInputsChangedAfterRunIdentityIsFixed(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(root string) error
		want   string
	}{
		{
			name: "prompt",
			mutate: func(root string) error {
				return os.WriteFile(filepath.Join(root, "second.md"), []byte("tampered prompt"), 0o600)
			},
			want: "prompt changed after run identity was fixed",
		},
		{
			name: "fixture",
			mutate: func(root string) error {
				return os.WriteFile(filepath.Join(root, "second", "result.txt"), []byte("tampered fixture"), 0o600)
			},
			want: "fixture changed after run identity was fixed",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			for _, name := range []string{"first", "second"} {
				if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, name, "result.txt"), []byte("broken"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, name+".md"), []byte("repair result"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			suite := validSuite()
			suite.Root = root
			suite.Defaults.WorkerCount = 1
			suite.Defaults.Retries = 0
			suite.Variants = suite.Variants[:1]
			suite.Cases = []Case{
				{ID: "case.first", Fixture: "first", Prompt: "first.md", Graders: []Grader{{
					ID: "result.fixed", Type: "file_contains", Required: true, Path: "result.txt", Contains: "fixed",
				}}},
				{ID: "case.second", Fixture: "second", Prompt: "second.md", Graders: []Grader{{
					ID: "result.fixed", Type: "file_contains", Required: true, Path: "result.txt", Contains: "fixed",
				}}},
			}
			agent := &snapshotMutatingAgent{mutate: func() error { return test.mutate(root) }}
			runner, err := NewRunner(t.TempDir(), agent, nil)
			if err != nil {
				t.Fatal(err)
			}
			report, err := runner.Run(t.Context(), suite, nil)
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if len(report.Results) != 2 || !strings.Contains(report.Results[1].Error, test.want) {
				t.Fatalf("results = %#v, want second failure containing %q", report.Results, test.want)
			}
			agent.mu.Lock()
			calls := agent.calls
			agent.mu.Unlock()
			if calls != 1 {
				t.Fatalf("agent calls = %d, want 1", calls)
			}
		})
	}
}

func TestRunnerRejectsUnknownVariant(t *testing.T) {
	runner, err := NewRunner(t.TempDir(), fakeEvalAgent{}, nil)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	if _, err := runner.Run(t.Context(), validSuite(), []string{"missing.variant"}); err == nil {
		t.Fatal("Run() error = nil")
	}
}

func TestRunnerResumesWithoutRepeatingPersistedJobs(t *testing.T) {
	root := t.TempDir()
	fixture := filepath.Join(root, "fixture")
	if err := os.Mkdir(fixture, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture, "result.txt"), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "prompt.md"), []byte("repair result"), 0o600); err != nil {
		t.Fatal(err)
	}
	suite := validSuite()
	suite.Root = root
	suite.Defaults.Retries = 0
	suite.Cases[0].Graders = []Grader{{
		ID: "result.fixed", Type: "file_contains", Required: true, Path: "result.txt", Contains: "fixed",
	}}
	priorTime := time.Now().UTC().Add(-time.Minute)
	prior := []CaseResult{{
		CaseID: "go.test-case", VariantID: "general.base", Attempt: 1,
		TaskStatus: "completed", Passed: true, Score: 1,
		StartedAt: priorTime, CompletedAt: priorTime.Add(time.Second),
	}}
	runRoot := t.TempDir()
	abandoned := filepath.Join(runRoot, "run-resume", "expert.go", "go.test-case", "attempt-01")
	if err := os.MkdirAll(abandoned, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(abandoned, "stale.txt"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(runRoot, fakeEvalAgent{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var progresses []Progress
	report, err := runner.RunWithIDProgressFrom(
		t.Context(), "run-resume", suite, nil, prior, priorTime,
		func(_ context.Context, progress Progress) error {
			progresses = append(progresses, progress)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("RunWithIDProgressFrom() error = %v", err)
	}
	if len(report.Results) != 2 || len(progresses) != 1 || progresses[0].CompletedCases != 2 {
		t.Fatalf("resume report=%#v progress=%#v", report.Results, progresses)
	}
	if !report.StartedAt.Equal(priorTime) {
		t.Fatalf("report started_at = %s, want %s", report.StartedAt, priorTime)
	}
	if _, err := os.Stat(filepath.Join(abandoned, "stale.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("abandoned workspace was not reset: %v", err)
	}
	if report.Results[0].VariantID != "expert.go" && report.Results[1].VariantID != "expert.go" {
		t.Fatalf("expert job was not executed: %#v", report.Results)
	}
}

func TestRunnerReportsTerminalJobProgressAfterRetries(t *testing.T) {
	suiteRoot := t.TempDir()
	fixture := filepath.Join(suiteRoot, "fixture")
	if err := os.Mkdir(fixture, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture, "result.txt"), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suiteRoot, "prompt.md"), []byte("repair result"), 0o600); err != nil {
		t.Fatal(err)
	}
	suite := validSuite()
	suite.Root = suiteRoot
	suite.Defaults.Retries = 1
	suite.Cases[0].Graders = []Grader{{
		ID: "result.fixed", Type: "file_contains", Required: true, Path: "result.txt", Contains: "fixed",
	}}
	runner, err := NewRunner(t.TempDir(), fakeEvalAgent{}, nil)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	var progresses []Progress
	report, err := runner.RunWithIDProgress(
		t.Context(),
		"run-progress",
		suite,
		nil,
		func(_ context.Context, progress Progress) error {
			progresses = append(progresses, progress)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("RunWithIDProgress() error = %v", err)
	}
	if len(progresses) != 2 || progresses[0].CompletedCases != 1 || progresses[1].CompletedCases != 2 ||
		progresses[1].TotalCases != 2 || len(report.Results) != 3 {
		t.Fatalf("progresses = %#v; results=%d", progresses, len(report.Results))
	}
	for _, progress := range progresses {
		if len(progress.Results) == 0 || progress.CaseID == "" || progress.VariantID == "" {
			t.Fatalf("progress = %#v", progress)
		}
	}
}
