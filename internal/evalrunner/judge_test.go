package evalrunner

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/userInner/kern/internal/evaluation"
)

func TestCompatibleJudgeUsesNoToolsAndParsesStrictResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer judge-secret" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		var payload struct {
			Model      string `json:"model"`
			Tools      []any  `json:"tools"`
			ToolChoice string `json:"tool_choice"`
			Messages   []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("Decode() error = %v", err)
		}
		if payload.Model != "judge-model" || len(payload.Tools) != 0 || payload.ToolChoice != "none" ||
			len(payload.Messages) != 2 || payload.Messages[0].Role != "system" {
			t.Errorf("judge request = %#v", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-request-id", "judge-request")
		_, _ = w.Write([]byte(`{"id":"body-id","choices":[{"message":{"role":"assistant","content":"{\"status\":\"passed\",\"score\":0.75,\"reason_code\":\"quality_satisfied\",\"summary\":\"Evidence satisfies the rubric.\"}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":40,"completion_tokens":12}}`))
	}))
	defer server.Close()
	t.Setenv("KERN_TEST_JUDGE_KEY", "judge-secret")
	config := evaluation.JudgeConfig{
		Provider: "openai-compatible", BaseURL: server.URL, Model: "judge-model",
		APIKeyEnv: "KERN_TEST_JUDGE_KEY", MaxTokens: 256,
	}
	judge, err := NewCompatibleJudge(config)
	if err != nil {
		t.Fatalf("NewCompatibleJudge() error = %v", err)
	}
	response, err := judge.Judge(t.Context(), config, evaluation.ModelJudgeRequest{
		GraderID: "model.quality", Prompt: "untrusted prompt", Rubric: "Assess quality.",
		FinalOutput: "done",
	})
	if err != nil || response.Status != evaluation.GradePassed || response.Score != 0.75 ||
		response.RequestID != "judge-request" || response.Usage.InputTokens != 40 || response.Usage.OutputTokens != 12 {
		t.Fatalf("Judge() = %#v, %v", response, err)
	}
}

func TestCompatibleJudgeRejectsNonJSONOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"pass"}}]}`))
	}))
	defer server.Close()
	config := evaluation.JudgeConfig{
		Provider: "openai-compatible", BaseURL: server.URL, Model: "judge-model", MaxTokens: 128,
	}
	judge, err := NewCompatibleJudge(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := judge.Judge(t.Context(), config, evaluation.ModelJudgeRequest{GraderID: "model.quality"}); err == nil {
		t.Fatal("Judge() error = nil")
	}
}
