package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/userInner/kern/internal/workspace"
)

const (
	maxModelFinalOutputBytes = 128 << 10
	maxModelEvidenceBytes    = 256 << 10
	maxModelEvidenceFile     = 64 << 10
)

// ModelEvidenceFile is one explicitly allowlisted, bounded workspace input to
// a supplemental model grader.
type ModelEvidenceFile struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Content string `json:"content"`
}

// ModelJudgeRequest contains only fixed rubric and bounded untrusted evidence.
// Judges never receive tools or write access.
type ModelJudgeRequest struct {
	GraderID    string              `json:"grader_id"`
	Prompt      string              `json:"prompt"`
	Rubric      string              `json:"rubric"`
	FinalOutput string              `json:"final_output"`
	Files       []ModelEvidenceFile `json:"files"`
}

// ModelJudgeResponse is the strict normalized supplemental judgment.
type ModelJudgeResponse struct {
	Status     GradeStatus  `json:"status"`
	Score      float64      `json:"score"`
	ReasonCode string       `json:"reason_code"`
	Summary    string       `json:"summary"`
	RequestID  string       `json:"request_id,omitempty"`
	Usage      UsageMetrics `json:"usage"`
}

// ModelJudge evaluates bounded evidence without access to Kern tools.
type ModelJudge interface {
	Judge(ctx context.Context, config JudgeConfig, request ModelJudgeRequest) (ModelJudgeResponse, error)
}

func gradeModel(
	ctx context.Context,
	root *workspace.Workspace,
	input GradeInput,
	grader Grader,
) GradeResult {
	result := GradeResult{
		GraderID: grader.ID, Status: GradeError, Score: 0,
		ReasonCode: "model_grader_failed",
		Details:    map[string]any{},
	}
	if input.Judge == nil || input.JudgeConfig == nil {
		result.ReasonCode = "model_judge_unavailable"
		return result
	}
	request, err := buildModelJudgeRequest(ctx, root, input, grader)
	if err != nil {
		result.ReasonCode = "model_evidence_unavailable"
		result.Details["error"] = err.Error()
		return result
	}
	response, err := input.Judge.Judge(ctx, *input.JudgeConfig, request)
	if err != nil {
		result.Details["error"] = err.Error()
		return result
	}
	if response.Status != GradePassed && response.Status != GradeFailed ||
		math.IsNaN(response.Score) || math.IsInf(response.Score, 0) || response.Score < 0 || response.Score > 1 ||
		!safeModelReasonCode(response.ReasonCode) || strings.TrimSpace(response.Summary) == "" || len(response.Summary) > 4_096 {
		result.ReasonCode = "model_judge_invalid_response"
		return result
	}
	result.Status = response.Status
	result.Score = response.Score
	result.ReasonCode = response.ReasonCode
	result.Evidence = []string{"model_request:" + digestModelJudgeRequest(request)}
	for _, file := range request.Files {
		result.Evidence = append(result.Evidence, "workspace:"+file.Path+"#sha256:"+file.SHA256)
	}
	if response.RequestID != "" {
		result.Evidence = append(result.Evidence, "model_response:"+response.RequestID)
	}
	result.Details = map[string]any{
		"summary":  response.Summary,
		"provider": input.JudgeConfig.Provider,
		"model":    input.JudgeConfig.Model,
	}
	result.Usage = &response.Usage
	return result
}

func buildModelJudgeRequest(
	ctx context.Context,
	root *workspace.Workspace,
	input GradeInput,
	grader Grader,
) (ModelJudgeRequest, error) {
	if len(input.FinalOutput) > maxModelFinalOutputBytes {
		return ModelJudgeRequest{}, errors.New("evaluation: final output exceeds model grader limit")
	}
	request := ModelJudgeRequest{
		GraderID: grader.ID, Prompt: input.Prompt, Rubric: grader.Instructions,
		FinalOutput: input.FinalOutput, Files: make([]ModelEvidenceFile, 0, len(grader.EvidencePaths)),
	}
	total := 0
	for _, name := range grader.EvidencePaths {
		file, err := root.ReadFile(ctx, name)
		if err != nil {
			return ModelJudgeRequest{}, fmt.Errorf("evaluation: reading model evidence %q: %w", name, err)
		}
		if len(file.Data) > maxModelEvidenceFile || total+len(file.Data) > maxModelEvidenceBytes {
			return ModelJudgeRequest{}, errors.New("evaluation: model evidence exceeds byte limit")
		}
		if !utf8.Valid(file.Data) || strings.IndexByte(string(file.Data), 0) >= 0 {
			return ModelJudgeRequest{}, fmt.Errorf("evaluation: model evidence %q is not UTF-8 text", name)
		}
		total += len(file.Data)
		request.Files = append(request.Files, ModelEvidenceFile{
			Path: file.Path, SHA256: file.SHA256, Content: string(file.Data),
		})
	}
	return request, nil
}

func digestModelJudgeRequest(request ModelJudgeRequest) string {
	data, _ := json.Marshal(request)
	return digestString(string(data))
}

func safeModelReasonCode(value string) bool {
	if value == "" || len(value) > 100 {
		return false
	}
	for _, character := range value {
		if character < 'a' || character > 'z' {
			if character < '0' || character > '9' {
				if character != '_' {
					return false
				}
			}
		}
	}
	return true
}
