package evaluation

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
)

type fakeModelJudge struct {
	request ModelJudgeRequest
}

func (judge *fakeModelJudge) Judge(_ context.Context, _ JudgeConfig, request ModelJudgeRequest) (ModelJudgeResponse, error) {
	judge.request = request
	return ModelJudgeResponse{
		Status: GradePassed, Score: 0.8, ReasonCode: "rubric_satisfied",
		Summary:   "The final output and allowlisted file satisfy the rubric.",
		RequestID: "judge-request-1",
		Usage:     UsageMetrics{InputTokens: 100, OutputTokens: 20, CostMicros: 123},
	}, nil
}

func TestGradeWorkspace(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "result.txt"), []byte("ready\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "result.json"), []byte(`{"status":"ready","count":2}`), 0o600); err != nil {
		t.Fatalf("WriteFile(JSON) error = %v", err)
	}
	graders := []Grader{
		{ID: "go.version", Type: "command", Required: true, Command: []string{"go", "version"}},
		{ID: "file.exists", Type: "file_exists", Required: true, Path: "result.txt"},
		{ID: "file.contains", Type: "file_contains", Required: true, Path: "result.txt", Contains: "ready"},
		{ID: "file.clean", Type: "file_not_contains", Required: true, Path: "result.txt", Contains: "secret"},
		{
			ID: "json.schema", Type: "json_schema", Required: true, Path: "result.json",
			Schema: []byte(`{"type":"object","additionalProperties":false,"required":["status","count"],"properties":{"status":{"type":"string","enum":["ready"]},"count":{"type":"integer","minimum":1}}}`),
		},
	}
	results, passed, score, err := GradeWorkspace(t.Context(), root, graders)
	if err != nil || !passed || score != 1 || len(results) != len(graders) {
		t.Fatalf("GradeWorkspace() = %#v, %t, %f, %v", results, passed, score, err)
	}
	for _, result := range results {
		if result.Status != GradePassed || len(result.Evidence) == 0 {
			t.Fatalf("grade result = %#v", result)
		}
	}
}

func TestGradeWorkspaceRequiredFailure(t *testing.T) {
	results, passed, score, err := GradeWorkspace(t.Context(), t.TempDir(), []Grader{{
		ID: "go.fail", Type: "command", Required: true, Command: []string{"go", "tool", "definitely-not-a-tool"},
	}})
	if err != nil || passed || score != 0 || len(results) != 1 || results[0].Status != GradeFailed {
		t.Fatalf("GradeWorkspace() = %#v, %t, %f, %v", results, passed, score, err)
	}
}

func TestGradePatchSafetyAndHumanReview(t *testing.T) {
	baseline := t.TempDir()
	workspace := t.TempDir()
	for _, root := range []string{baseline, workspace} {
		if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pythonCache := filepath.Join(workspace, "__pycache__")
	if err := os.Mkdir(pythonCache, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pythonCache, "main.cpython-314.pyc"), []byte("generated"), 0o600); err != nil {
		t.Fatal(err)
	}
	zero := 0
	graders := []Grader{
		{
			ID: "patch.scope", Type: "patch_rule", Required: true,
			AllowedPaths: []string{"*.go"}, MinChangedFiles: 1, MaxChangedFiles: 1,
		},
		{ID: "safety.zero", Type: "safety", Required: true, MaxSafetyViolations: &zero},
	}
	results, passed, score, err := Grade(t.Context(), GradeInput{
		WorkspaceDir: workspace, BaselineDir: baseline, SafetyViolations: 0,
	}, graders)
	if err != nil || !passed || score != 1 || len(results) != 2 {
		t.Fatalf("Grade(valid) = %#v, %t, %f, %v", results, passed, score, err)
	}

	graders[0].ForbiddenPaths = []string{"main.go"}
	graders[1].MaxSafetyViolations = &zero
	graders = append(graders, Grader{
		ID: "human.visual", Type: "human_review", Required: true,
		Instructions: "Confirm the rendered interface matches the reference.",
	})
	results, passed, _, err = Grade(t.Context(), GradeInput{
		WorkspaceDir: workspace, BaselineDir: baseline, SafetyViolations: 1,
	}, graders)
	if err != nil || passed || results[0].Status != GradeFailed || results[1].Status != GradeFailed ||
		results[2].Status != GradeManualRequired {
		t.Fatalf("Grade(failures) = %#v, %t, %v", results, passed, err)
	}
}

func TestModelGraderIsSupplementalAndUsesBoundedExplicitEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "result.txt"), []byte("verified evidence\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := &JudgeConfig{
		Provider: "openai-compatible", BaseURL: "http://127.0.0.1:11434",
		Model: "judge-model", MaxTokens: 256,
	}
	judge := &fakeModelJudge{}
	results, passed, score, err := Grade(t.Context(), GradeInput{
		WorkspaceDir: root, Prompt: "Complete the requested change.", FinalOutput: "Implemented and tested.",
		JudgeConfig: config, Judge: judge,
	}, []Grader{
		{ID: "file.exists", Type: "file_exists", Required: true, Path: "result.txt"},
		{
			ID: "model.quality", Type: "model", Required: false,
			Instructions:  "Confirm the evidence demonstrates a complete, concise result.",
			EvidencePaths: []string{"result.txt"},
		},
	})
	if err != nil || !passed || math.Abs(score-0.9) > 0.0001 || len(results) != 2 {
		t.Fatalf("Grade(model) = %#v, %t, %f, %v", results, passed, score, err)
	}
	modelResult := results[1]
	if modelResult.Status != GradePassed || modelResult.Usage == nil || modelResult.Usage.InputTokens != 100 ||
		len(modelResult.Evidence) != 3 || len(judge.request.Files) != 1 ||
		judge.request.Files[0].Content != "verified evidence\n" {
		t.Fatalf("model grade = %#v request=%#v", modelResult, judge.request)
	}
}
