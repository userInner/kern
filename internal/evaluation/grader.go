package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/userInner/kern/internal/jsonschema"
	"github.com/userInner/kern/internal/tool/builtin"
	"github.com/userInner/kern/internal/workspace"
)

// GradeInput supplies the isolated workspace, immutable baseline fixture, and
// runtime safety counters used by deterministic graders.
type GradeInput struct {
	WorkspaceDir     string
	BaselineDir      string
	SafetyViolations int
	Prompt           string
	FinalOutput      string
	JudgeConfig      *JudgeConfig
	Judge            ModelJudge
}

// GradeWorkspace preserves the narrow public helper for file and command
// graders that do not need a baseline or runtime metrics.
func GradeWorkspace(ctx context.Context, workspaceDir string, graders []Grader) ([]GradeResult, bool, float64, error) {
	return Grade(ctx, GradeInput{WorkspaceDir: workspaceDir}, graders)
}

// Grade executes the configured graders against one completed case attempt.
func Grade(ctx context.Context, input GradeInput, graders []Grader) ([]GradeResult, bool, float64, error) {
	root, err := workspace.Open(input.WorkspaceDir)
	if err != nil {
		return nil, false, 0, err
	}
	defer root.Close()
	executor, err := builtin.NewExecute(root)
	if err != nil {
		return nil, false, 0, err
	}
	results := make([]GradeResult, 0, len(graders))
	passed := true
	var total float64
	for _, grader := range graders {
		var result GradeResult
		switch grader.Type {
		case "command":
			result = gradeCommand(ctx, executor, grader)
		case "file_exists", "file_contains", "file_not_contains":
			result = gradeFile(ctx, root, grader)
		case "json_schema":
			result = gradeJSONSchema(ctx, root, grader)
		case "patch_rule":
			result = gradePatchRule(ctx, input.BaselineDir, input.WorkspaceDir, grader)
		case "safety":
			result = gradeSafety(input.SafetyViolations, grader)
		case "human_review":
			result = gradeHumanReview(grader)
		case "model":
			result = gradeModel(ctx, root, input, grader)
		default:
			return nil, false, 0, fmt.Errorf("evaluation: unsupported grader type %q", grader.Type)
		}
		if grader.Required && result.Status != GradePassed {
			passed = false
		}
		total += result.Score
		results = append(results, result)
	}
	if len(results) == 0 {
		return nil, false, 0, errors.New("evaluation: no graders")
	}
	return results, passed, total / float64(len(results)), nil
}

func gradeSafety(violations int, grader Grader) GradeResult {
	maximum := 0
	if grader.MaxSafetyViolations != nil {
		maximum = *grader.MaxSafetyViolations
	}
	result := GradeResult{
		GraderID: grader.ID, Status: GradePassed, Score: 1,
		Evidence: []string{"metrics:safety_violations"}, ReasonCode: "safety_passed",
		Details: map[string]any{"observed": violations, "maximum": maximum},
	}
	if violations > maximum {
		result.Status = GradeFailed
		result.Score = 0
		result.ReasonCode = "safety_violations_exceeded"
	}
	return result
}

func gradeHumanReview(grader Grader) GradeResult {
	return GradeResult{
		GraderID: grader.ID, Status: GradeManualRequired, Score: 0,
		Evidence: []string{"human_review:" + grader.ID}, ReasonCode: "human_review_required",
		Details: map[string]any{"instructions": grader.Instructions},
	}
}

func gradeJSONSchema(ctx context.Context, root *workspace.Workspace, grader Grader) GradeResult {
	file, err := root.ReadFile(ctx, grader.Path)
	result := GradeResult{
		GraderID:   grader.ID,
		Status:     GradePassed,
		Score:      1,
		Evidence:   []string{"workspace:" + grader.Path},
		ReasonCode: "json_schema_passed",
		Details:    map[string]any{"path": grader.Path},
	}
	if err != nil {
		result.Status = GradeFailed
		result.Score = 0
		result.ReasonCode = "json_file_unavailable"
		result.Details["error"] = err.Error()
		return result
	}
	result.Details["sha256"] = file.SHA256
	if err := jsonschema.Validate(grader.Schema, file.Data); err != nil {
		result.Status = GradeFailed
		result.Score = 0
		result.ReasonCode = "json_schema_failed"
		result.Details["error"] = err.Error()
	}
	return result
}

func gradeCommand(ctx context.Context, executor *builtin.Execute, grader Grader) GradeResult {
	input, _ := json.Marshal(map[string]any{"argv": grader.Command})
	toolResult, err := executor.Execute(ctx, input)
	exitCode := -1
	if toolResult.ExitCode != nil {
		exitCode = *toolResult.ExitCode
	}
	result := GradeResult{
		GraderID:   grader.ID,
		Status:     GradePassed,
		Score:      1,
		Evidence:   []string{"command:" + strings.Join(grader.Command, " ")},
		ReasonCode: "command_passed",
		Details: map[string]any{
			"command":       grader.Command,
			"exit_code":     exitCode,
			"output_sha256": digestString(toolResult.Content),
		},
	}
	if err != nil {
		result.Status = GradeFailed
		result.Score = 0
		result.ReasonCode = "command_failed"
		result.Details["error"] = err.Error()
	}
	return result
}

func gradeFile(ctx context.Context, root *workspace.Workspace, grader Grader) GradeResult {
	file, err := root.ReadFile(ctx, grader.Path)
	result := GradeResult{
		GraderID:   grader.ID,
		Status:     GradePassed,
		Score:      1,
		Evidence:   []string{"workspace:" + grader.Path},
		ReasonCode: "file_assertion_passed",
		Details:    map[string]any{"path": grader.Path},
	}
	if err != nil {
		result.Status = GradeFailed
		result.Score = 0
		result.ReasonCode = "file_unavailable"
		result.Details["error"] = err.Error()
		return result
	}
	result.Details["sha256"] = file.SHA256
	switch grader.Type {
	case "file_contains":
		if !strings.Contains(string(file.Data), grader.Contains) {
			result.Status = GradeFailed
			result.Score = 0
			result.ReasonCode = "content_missing"
		}
	case "file_not_contains":
		if strings.Contains(string(file.Data), grader.Contains) {
			result.Status = GradeFailed
			result.Score = 0
			result.ReasonCode = "forbidden_content_present"
		}
	}
	return result
}

func digestString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}
