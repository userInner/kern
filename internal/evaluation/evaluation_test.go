package evaluation

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadFromRootConfinesSuiteSelection(t *testing.T) {
	evalRoot := t.TempDir()
	writeSuiteFiles(t, filepath.Join(evalRoot, "trusted"), validSuite())

	loaded, err := LoadFromRoot(evalRoot, "./trusted")
	if err != nil {
		t.Fatalf("LoadFromRoot(valid) error = %v", err)
	}
	if prompt, err := loaded.PromptText(loaded.Cases[0]); err != nil || prompt != "Fix the failing test.\n" {
		t.Fatalf("PromptText() = %q, %v", prompt, err)
	}
	defer loaded.Close()

	for _, test := range []struct {
		name string
		path string
	}{
		{name: "absolute", path: filepath.Join(evalRoot, "trusted")},
		{name: "parent traversal", path: "../trusted"},
		{name: "embedded traversal", path: "nested/../trusted"},
		{name: "backslash", path: `nested\trusted`},
		{name: "drive qualified", path: "C:/trusted"},
		{name: "NUL", path: "trusted\x00ignored"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := LoadFromRoot(evalRoot, test.path); !errors.Is(err, ErrInvalidSuite) {
				t.Fatalf("LoadFromRoot(%q) error = %v, want ErrInvalidSuite", test.path, err)
			}
		})
	}
}

func TestLoadFromRootRejectsSuiteDirectorySymlinkEscape(t *testing.T) {
	evalRoot := t.TempDir()
	outside := t.TempDir()
	writeSuiteFiles(t, outside, validSuite())
	requireSymlink(t, outside, filepath.Join(evalRoot, "escaped"))

	if _, err := LoadFromRoot(evalRoot, "escaped"); err == nil {
		t.Fatal("LoadFromRoot(symlink escape) error = nil")
	}
}

func TestLoadFromRootHandleSurvivesRootPathReplacement(t *testing.T) {
	evalRoot := t.TempDir()
	writeSuiteFiles(t, filepath.Join(evalRoot, "trusted"), validSuite())
	root, err := os.OpenRoot(evalRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	loaded, err := LoadFromRootHandle(root, "trusted")
	if err != nil {
		t.Fatalf("LoadFromRootHandle() error = %v", err)
	}
	moved := t.TempDir()
	if err := os.Remove(moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(evalRoot, moved); err != nil {
		t.Skipf("Rename(root) unavailable: %v", err)
	}
	if err := os.Mkdir(evalRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSuiteFiles(t, filepath.Join(evalRoot, "trusted"), validSuite())
	if err := os.WriteFile(
		filepath.Join(evalRoot, "trusted", "prompt.md"),
		[]byte("attacker replacement\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	prompt, err := loaded.PromptText(loaded.Cases[0])
	if err != nil || prompt != "Fix the failing test.\n" {
		t.Fatalf("PromptText() = %q, %v", prompt, err)
	}
}

func TestSnapshotFreezesPromptAndFixture(t *testing.T) {
	sourceRoot := t.TempDir()
	writeSuiteFiles(t, filepath.Join(sourceRoot, "trusted"), validSuite())
	source, err := LoadFromRoot(sourceRoot, "trusted")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	snapshot, err := Snapshot(t.Context(), source, filepath.Join(t.TempDir(), "input"))
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	defer snapshot.Close()
	before, _, err := PrepareRun(snapshot, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(sourceRoot, "trusted", "prompt.md"),
		[]byte("changed after snapshot\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(sourceRoot, "trusted", "fixture", "go.mod"),
		[]byte("module attacker.example/changed\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	after, _, err := PrepareRun(snapshot, nil)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("snapshot digest changed: %s -> %s", before, after)
	}
	prompt, err := snapshot.PromptText(snapshot.Cases[0])
	if err != nil || prompt != "Fix the failing test.\n" {
		t.Fatalf("snapshot PromptText() = %q, %v", prompt, err)
	}
}

func TestSuiteInputsRejectSymlinkEscape(t *testing.T) {
	t.Run("final prompt symlink", func(t *testing.T) {
		evalRoot := t.TempDir()
		suiteRoot := filepath.Join(evalRoot, "suite")
		writeSuiteFiles(t, suiteRoot, validSuite())
		outside := filepath.Join(t.TempDir(), "secret.txt")
		if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(suiteRoot, "prompt.md")); err != nil {
			t.Fatal(err)
		}
		requireSymlink(t, outside, filepath.Join(suiteRoot, "prompt.md"))

		suite, err := LoadFromRoot(evalRoot, "suite")
		if err != nil {
			t.Fatalf("LoadFromRoot() error = %v", err)
		}
		defer suite.Close()
		if _, err := suite.PromptText(suite.Cases[0]); err == nil {
			t.Fatal("PromptText(symlink escape) error = nil")
		}
	})

	t.Run("intermediate prompt symlink", func(t *testing.T) {
		evalRoot := t.TempDir()
		suiteRoot := filepath.Join(evalRoot, "suite")
		suite := validSuite()
		suite.Cases[0].Prompt = "escaped/secret.txt"
		writeSuiteFiles(t, suiteRoot, suite)
		outside := t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		requireSymlink(t, outside, filepath.Join(suiteRoot, "escaped"))

		loaded, err := LoadFromRoot(evalRoot, "suite")
		if err != nil {
			t.Fatalf("LoadFromRoot() error = %v", err)
		}
		defer loaded.Close()
		if _, err := loaded.PromptText(loaded.Cases[0]); err == nil {
			t.Fatal("PromptText(intermediate symlink escape) error = nil")
		}
	})

	t.Run("intermediate fixture symlink", func(t *testing.T) {
		evalRoot := t.TempDir()
		suiteRoot := filepath.Join(evalRoot, "suite")
		suite := validSuite()
		suite.Cases[0].Fixture = "escaped/fixture"
		writeSuiteFiles(t, suiteRoot, suite)
		outside := t.TempDir()
		if err := os.Mkdir(filepath.Join(outside, "fixture"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outside, "fixture", "secret.txt"), []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		requireSymlink(t, outside, filepath.Join(suiteRoot, "escaped"))

		loaded, err := LoadFromRoot(evalRoot, "suite")
		if err != nil {
			t.Fatalf("LoadFromRoot() error = %v", err)
		}
		defer loaded.Close()
		if _, err := loaded.openFixture(loaded.Cases[0]); err == nil {
			t.Fatal("openFixture(intermediate symlink escape) error = nil")
		}
		if _, _, err := PrepareRun(loaded, nil); err == nil {
			t.Fatal("PrepareRun(intermediate symlink escape) error = nil")
		}
	})
}

func TestLoadSuiteAndResolveInputs(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "fixture"), 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "prompt.md"), []byte("Fix the failing test.\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(prompt) error = %v", err)
	}
	suite := validSuite()
	data, err := json.Marshal(suite)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	name := filepath.Join(root, "suite.json")
	if err := os.WriteFile(name, data, 0o600); err != nil {
		t.Fatalf("WriteFile(suite) error = %v", err)
	}
	loaded, err := Load(name)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	prompt, err := loaded.PromptText(loaded.Cases[0])
	if err != nil || prompt != "Fix the failing test.\n" {
		t.Fatalf("PromptText() = %q, %v", prompt, err)
	}
	fixture, err := loaded.openFixture(loaded.Cases[0])
	if err != nil {
		t.Fatalf("openFixture() error = %v", err)
	}
	if err := fixture.Close(); err != nil {
		t.Fatalf("Close(fixture) error = %v", err)
	}
}

func TestShippedGoReferenceSuite(t *testing.T) {
	t.Parallel()

	suite, err := Load(filepath.Join("..", "..", "evals", "go"))
	if err != nil {
		t.Fatalf("Load(shipped Go suite) error = %v", err)
	}
	if len(suite.Cases) != 30 || len(suite.Variants) != 2 {
		t.Fatalf("shipped Go suite has %d cases and %d variants, want 30 and 2", len(suite.Cases), len(suite.Variants))
	}
	for _, evalCase := range suite.Cases {
		if _, err := suite.PromptText(evalCase); err != nil {
			t.Errorf("PromptText(%s) error = %v", evalCase.ID, err)
		}
		fixture, err := suite.openFixture(evalCase)
		if err != nil {
			t.Errorf("openFixture(%s) error = %v", evalCase.ID, err)
			continue
		}
		if err := fixture.Close(); err != nil {
			t.Errorf("Close(fixture %s) error = %v", evalCase.ID, err)
		}
	}
}

func TestSuitePathsUsePortableSlashSeparators(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		path string
		want bool
	}{
		{name: "nested path", path: "fixtures/example/main.go", want: true},
		{name: "backslash", path: `fixtures\example\main.go`, want: false},
		{name: "drive-qualified", path: "C:/example/main.go", want: false},
		{name: "traversal", path: "../example/main.go", want: false},
		{name: "cleaned traversal", path: "fixtures/../main.go", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := safeRelative(test.path); got != test.want {
				t.Fatalf("safeRelative(%q) = %t, want %t", test.path, got, test.want)
			}
		})
	}
}

func TestValidateRejectsUnsafeOrAmbiguousSuites(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Suite)
	}{
		{name: "duplicate variant", mutate: func(s *Suite) { s.Variants = append(s.Variants, s.Variants[0]) }},
		{name: "fixture traversal", mutate: func(s *Suite) { s.Cases[0].Fixture = "../fixture" }},
		{name: "undeclared command", mutate: func(s *Suite) { s.Cases[0].Graders[0].Command[0] = "bash" }},
		{name: "invalid plugin reference", mutate: func(s *Suite) { s.Variants[1].Plugins[0] = "dev.kern.go-expert" }},
		{name: "duplicate grader", mutate: func(s *Suite) { s.Cases[0].Graders = append(s.Cases[0].Graders, s.Cases[0].Graders[0]) }},
		{name: "unsupported schema keyword", mutate: func(s *Suite) {
			s.Cases[0].Graders[0] = Grader{ID: "json.schema", Type: "json_schema", Required: true, Path: "result.json", Schema: json.RawMessage(`{"type":"object","oneOf":[]}`)}
		}},
		{name: "unsafe patch glob", mutate: func(s *Suite) {
			s.Cases[0].Graders[0] = Grader{ID: "patch.scope", Type: "patch_rule", Required: true, AllowedPaths: []string{"../*"}, MinChangedFiles: 1}
		}},
		{name: "model grader without judge", mutate: func(s *Suite) {
			s.Cases[0].Graders = append(s.Cases[0].Graders, Grader{ID: "model.quality", Type: "model", Instructions: "Assess quality."})
		}},
		{name: "required model grader", mutate: func(s *Suite) {
			s.Defaults.Judge = validJudgeConfig()
			s.Cases[0].Graders = append(s.Cases[0].Graders, Grader{ID: "model.quality", Type: "model", Required: true, Instructions: "Assess quality."})
		}},
		{name: "model grader replaces deterministic grader", mutate: func(s *Suite) {
			s.Defaults.Judge = validJudgeConfig()
			s.Cases[0].Graders = []Grader{{ID: "model.quality", Type: "model", Instructions: "Assess quality."}}
		}},
		{name: "unsafe model evidence", mutate: func(s *Suite) {
			s.Defaults.Judge = validJudgeConfig()
			s.Cases[0].Graders = append(s.Cases[0].Graders, Grader{
				ID: "model.quality", Type: "model", Instructions: "Assess quality.", EvidencePaths: []string{"../secret"},
			})
		}},
		{name: "judge URL contains credentials", mutate: func(s *Suite) {
			s.Defaults.Judge = validJudgeConfig()
			s.Defaults.Judge.BaseURL = "https://user:secret@example.test"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			suite := validSuite()
			test.mutate(&suite)
			if err := Validate(suite); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

func validJudgeConfig() *JudgeConfig {
	return &JudgeConfig{
		Provider: "openai-compatible", BaseURL: "http://127.0.0.1:11434",
		Model: "judge-model", MaxTokens: 256,
	}
}

func TestBuildReportComparesFirstAttempts(t *testing.T) {
	suite := validSuite()
	suite.Cases = append(suite.Cases, Case{
		ID: "go.second-case", Fixture: "fixture", Prompt: "prompt.md",
		Graders: []Grader{{ID: "go.test", Type: "command", Required: true, Command: []string{"go", "test", "./..."}}},
	})
	results := []CaseResult{
		{CaseID: "go.test-case", VariantID: "general.base", Attempt: 1, Passed: false, Score: 0},
		{CaseID: "go.test-case", VariantID: "expert.go", Attempt: 1, Passed: true, Score: 1, Usage: UsageMetrics{InputTokens: 10}},
		{CaseID: "go.second-case", VariantID: "general.base", Attempt: 1, Passed: true, Score: 1},
		{CaseID: "go.second-case", VariantID: "expert.go", Attempt: 1, Passed: false, Score: 0, Usage: UsageMetrics{SafetyViolations: 1}},
		{CaseID: "go.second-case", VariantID: "expert.go", Attempt: 2, Passed: true, Score: 1},
	}
	now := time.Now().UTC()
	report := BuildReport("run-1", "sha256:abc", suite, results, now, now.Add(time.Second))
	if len(report.Variants) != 2 || report.Variants[1].Cases != 2 || report.Variants[1].Passed != 1 {
		t.Fatalf("variant metrics = %#v", report.Variants)
	}
	if len(report.Comparisons) != 1 || report.Comparisons[0].SuccessRateDelta != 0 ||
		len(report.Comparisons[0].Improvements) != 1 || len(report.Comparisons[0].Regressions) != 1 ||
		report.Comparisons[0].PairedCases != 2 || report.Comparisons[0].SuccessRatePValue != 1 ||
		report.Comparisons[0].SuccessRateImprovementSignificant || !report.Comparisons[0].SafetyRegressed {
		t.Fatalf("comparison = %#v", report.Comparisons)
	}
	if report.Variants[0].Confidence95.High <= report.Variants[0].Confidence95.Low {
		t.Fatalf("confidence interval = %#v", report.Variants[0].Confidence95)
	}
}

func TestExactMcNemarPValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                      string
		improvements, regressions int
		want                      float64
	}{
		{name: "no difference", want: 1},
		{name: "balanced", improvements: 1, regressions: 1, want: 1},
		{name: "five clean improvements are not enough", improvements: 5, want: 0.0625},
		{name: "six clean improvements", improvements: 6, want: 0.03125},
		{name: "direction is symmetric", regressions: 6, want: 0.03125},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := exactMcNemarPValue(test.improvements, test.regressions); got != test.want {
				t.Fatalf("exactMcNemarPValue(%d, %d) = %g, want %g", test.improvements, test.regressions, got, test.want)
			}
		})
	}
}

func TestBuildReportMarksSignificantPairedImprovement(t *testing.T) {
	t.Parallel()

	suite := validSuite()
	suite.Cases = nil
	results := make([]CaseResult, 0, 12)
	for _, caseID := range []string{
		"go.case-one", "go.case-two", "go.case-three",
		"go.case-four", "go.case-five", "go.case-six",
	} {
		suite.Cases = append(suite.Cases, Case{ID: caseID})
		results = append(results,
			CaseResult{CaseID: caseID, VariantID: "general.base", Attempt: 1},
			CaseResult{CaseID: caseID, VariantID: "expert.go", Attempt: 1, Passed: true, Score: 1},
		)
	}
	now := time.Now().UTC()
	report := BuildReport("run-significant", "sha256:paired", suite, results, now, now)
	if len(report.Comparisons) != 1 {
		t.Fatalf("comparisons = %#v", report.Comparisons)
	}
	comparison := report.Comparisons[0]
	if comparison.PairedCases != 6 || comparison.SuccessRatePValue != 0.03125 ||
		!comparison.SuccessRateImprovementSignificant {
		t.Fatalf("comparison = %#v", comparison)
	}
}

func TestBuildReportEncodesEmptyComparisonCasesAsArrays(t *testing.T) {
	t.Parallel()

	suite := validSuite()
	results := []CaseResult{
		{CaseID: "go.test-case", VariantID: "general.base", Attempt: 1, Passed: true},
		{CaseID: "go.test-case", VariantID: "expert.go", Attempt: 1, Passed: true},
	}
	now := time.Now().UTC()
	report := BuildReport("run-empty-pairs", "sha256:pairs", suite, results, now, now)
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("Marshal(report) error = %v", err)
	}
	if !bytes.Contains(encoded, []byte(`"improvements":[]`)) ||
		!bytes.Contains(encoded, []byte(`"regressions":[]`)) {
		t.Fatalf("comparison arrays encoded as null: %s", encoded)
	}
}

func validSuite() Suite {
	return Suite{
		SchemaVersion: SchemaVersion,
		ID:            "kern.go.test",
		Name:          "Go test suite",
		Version:       "0.1.0",
		Defaults: Defaults{
			TimeoutMS:        60_000,
			TokenBudget:      10_000,
			CostBudgetMicros: 1_000_000,
			Retries:          1,
			WorkerCount:      2,
			AllowedCommands:  []string{"go"},
		},
		Variants: []Variant{
			{ID: "general.base"},
			{ID: "expert.go", Plugins: []string{"dev.kern.go-expert@0.1.0"}},
		},
		Cases: []Case{{
			ID: "go.test-case", Fixture: "fixture", Prompt: "prompt.md",
			Graders: []Grader{{ID: "go.test", Type: "command", Required: true, Command: []string{"go", "test", "./..."}}},
		}},
	}
}

func writeSuiteFiles(t *testing.T, root string, suite Suite) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "prompt.md"), []byte("Fix the failing test.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(suite)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "suite.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func requireSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
}
