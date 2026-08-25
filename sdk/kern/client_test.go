package kern

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientTaskArtifactAndErrorFlow(t *testing.T) {
	var submittedTask CreateTaskInput
	var submittedInput SubmitTaskInput
	var submittedModel ModelConfigInput
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		switch request.URL.Path {
		case "/api/v1/health":
			_, _ = io.WriteString(w, `{"schema_version":"1","status":"ok","mode":"offline-baseline"}`)
		case "/api/v1/ready":
			_, _ = io.WriteString(w, `{"schema_version":"1","status":"ready","mode":"offline-baseline"}`)
		case "/api/v1/settings":
			if request.Method == http.MethodPut {
				var input UpdateSettingsInput
				if err := json.NewDecoder(request.Body).Decode(&input); err != nil ||
					input.PolicyProfile != PolicyReadOnly || input.RetentionDays != 45 {
					t.Errorf("settings input = %#v, %v", input, err)
				}
				_, _ = io.WriteString(w, `{"schema_version":"1","max_turns":8,"max_tool_calls":24,"max_tokens":100000,"max_cost_usd":3,"task_timeout":"5m0s","policy_profile":"read-only","retention_days":45}`)
				return
			}
			_, _ = io.WriteString(w, `{"schema_version":"1","max_turns":12,"max_tool_calls":32,"max_tokens":200000,"max_cost_usd":5,"task_timeout":"10m0s","policy_profile":"local-safe","retention_days":90}`)
		case "/api/v1/settings/cleanup-preview":
			_, _ = io.WriteString(w, `{"cutoff":"2026-05-26T00:00:00Z","retention_days":90,"task_count":2,"artifact_count":3,"artifact_bytes":1024}`)
		case "/api/v1/settings/cleanup":
			var input struct {
				Confirm bool `json:"confirm"`
			}
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil || !input.Confirm {
				t.Errorf("cleanup input = %#v, %v", input, err)
			}
			_, _ = io.WriteString(w, `{"cutoff":"2026-05-26T00:00:00Z","retention_days":90,"task_count":2,"artifact_count":3,"artifact_bytes":1024,"removed_objects":2,"removed_bytes":768}`)
		case "/api/v1/models":
			_, _ = io.WriteString(w, `{"schema_version":"1","credential_store_available":true,"providers":["openai-compatible","ollama"],"configs":[{"schema_version":"1","id":"model-1","name":"Local Qwen","provider":"ollama","base_url":"http://127.0.0.1:11434","model":"qwen3","has_api_key":false,"enabled":true,"is_default":true,"created_at":"2026-08-24T00:00:00Z","updated_at":"2026-08-24T00:00:00Z"}]}`)
		case "/api/v1/model-configs":
			if err := json.NewDecoder(request.Body).Decode(&submittedModel); err != nil {
				t.Errorf("model input = %#v, %v", submittedModel, err)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"schema_version":"1","id":"model-1","name":"Local Qwen","provider":"ollama","base_url":"http://127.0.0.1:11434","model":"qwen3","has_api_key":false,"enabled":true,"is_default":true,"created_at":"2026-08-24T00:00:00Z","updated_at":"2026-08-24T00:00:00Z"}`)
		case "/api/v1/model-configs/model-1":
			if request.Method == http.MethodDelete {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			_, _ = io.WriteString(w, `{"schema_version":"1","id":"model-1","name":"Updated Qwen","provider":"ollama","base_url":"http://127.0.0.1:11434","model":"qwen3","has_api_key":false,"enabled":true,"is_default":true,"created_at":"2026-08-24T00:00:00Z","updated_at":"2026-08-24T00:01:00Z"}`)
		case "/api/v1/model-configs/model-1/test":
			_, _ = io.WriteString(w, `{"schema_version":"1","config_id":"model-1","provider":"ollama","model":"qwen3","ok":true,"latency_ms":12,"capabilities":{"tools":true}}`)
		case "/api/v1/tasks":
			if request.Method == http.MethodPost {
				if err := json.NewDecoder(request.Body).Decode(&submittedTask); err != nil {
					t.Errorf("task input = %#v, %v", submittedTask, err)
				}
				w.WriteHeader(http.StatusAccepted)
				_, _ = io.WriteString(w, `{"schema_version":"1","id":"task-1","status":"created"}`)
				return
			}
			_, _ = io.WriteString(w, `{"tasks":[{"id":"task-1","status":"completed"}]}`)
		case "/metrics":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "kern_tasks_created_total 1\n")
		case "/api/v1/tasks/task-1/artifacts/artifact-1":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "evidence")
		case "/api/v1/tasks/task-1/messages":
			if err := json.NewDecoder(request.Body).Decode(&submittedInput); err != nil {
				t.Errorf("task input = %#v, %v", submittedInput, err)
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"schema_version":"1","id":"task-1","status":"created","active_attempt_id":"attempt-2"}`)
		case "/api/v1/tasks/task-1/plugins":
			_, _ = io.WriteString(w, `{"schema_version":"1","attempt_id":"attempt-1","plugins":[{"schema_version":"1","task_id":"task-1","attempt_id":"attempt-1","plugin_id":"dev.kern.go","version":"0.1.0","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","reason":"manual_enable","resources":{"knowledge":["go.md"]},"created_at":"2026-08-24T00:00:00Z"}]}`)
		case "/api/v1/tasks/task-1/plan":
			_, _ = io.WriteString(w, `{"schema_version":"1","plan":{"schema_version":"1","id":"plan-1","task_id":"task-1","attempt_id":"attempt-1","revision":1,"status":"active","rationale":"Verify work","steps":[{"id":"step-1","plan_id":"plan-1","ordinal":1,"title":"Inspect","description":"Inspect inputs","phase":"prepare","required":true,"status":"completed","failure":"","retry_count":0,"updated_at":"2026-08-24T00:00:00Z"}],"created_at":"2026-08-24T00:00:00Z","updated_at":"2026-08-24T00:00:00Z"}}`)
		case "/api/v1/tasks/task-1/operations/uncertain":
			_, _ = io.WriteString(w, `{"schema_version":"1","operations":[{"id":"operation-1","task_id":"task-1","attempt_id":"attempt-1","tool":"execute","input":{"argv":["tool"]},"input_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","idempotency_key":"attempt-1:1:0","effect":"process","status":"unknown","output_summary":"","error_code":"interrupted","created_at":"2026-08-24T00:00:00Z","updated_at":"2026-08-24T00:00:00Z"}]}`)
		case "/api/v1/operations/operation-1/resolution":
			var input struct {
				Resolution OperationResolution `json:"resolution"`
			}
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil ||
				input.Resolution != OperationConfirmedNotExecuted {
				t.Errorf("resolution input = %#v, %v", input, err)
			}
			_, _ = io.WriteString(w, `{"id":"receipt-1","operation_id":"operation-1","task_id":"task-1","attempt_id":"attempt-1","resolution":"confirmed_not_executed","actor":"local-user","decided_at":"2026-08-24T00:00:00Z"}`)
		case "/api/v1/tasks/missing":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"task not found"}`)
		case "/api/v1/plugins":
			_, _ = io.WriteString(w, `{"schema_version":"1","plugins":[{"schema_version":"1","id":"dev.kern.go","name":"Go Expert","version":"0.1.0","enabled":true,"manifest":{"schema_version":"1","id":"dev.kern.go","name":"Go Expert","version":"0.1.0","core":">=0.1.0 <0.2.0","entrypoints":{"knowledge":["go.md"]},"activation":{},"permissions":{},"integrity":{"files":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}]}`)
		case "/api/v1/plugins/install":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"schema_version":"1","id":"dev.kern.go","name":"Go Expert","version":"0.1.0","enabled":false,"manifest":{"schema_version":"1","id":"dev.kern.go","name":"Go Expert","version":"0.1.0","core":">=0.1.0 <0.2.0","entrypoints":{"knowledge":["go.md"]},"activation":{},"permissions":{},"integrity":{"files":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}`)
		case "/api/v1/plugins/dev.kern.go/enable":
			_, _ = io.WriteString(w, `{"schema_version":"1","id":"dev.kern.go","name":"Go Expert","version":"0.1.0","enabled":true,"manifest":{"schema_version":"1","id":"dev.kern.go","name":"Go Expert","version":"0.1.0","core":">=0.1.0 <0.2.0","entrypoints":{"knowledge":["go.md"]},"activation":{},"permissions":{},"integrity":{"files":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}`)
		case "/api/v1/plugins/dev.kern.go":
			if request.Method == http.MethodDelete {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			_, _ = io.WriteString(w, `{"schema_version":"1","id":"dev.kern.go","name":"Go Expert","version":"0.1.0","enabled":true,"manifest":{"schema_version":"1","id":"dev.kern.go","name":"Go Expert","version":"0.1.0","core":">=0.1.0 <0.2.0","entrypoints":{"knowledge":["go.md"]},"activation":{},"permissions":{},"integrity":{"files":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}`)
		case "/api/v1/evals/runs":
			if request.Method == http.MethodPost {
				w.WriteHeader(http.StatusAccepted)
				_, _ = io.WriteString(w, `{"schema_version":"1","id":"eval-1","suite_id":"kern.go","suite_name":"Go","suite_version":"1.0.0","status":"queued","variants":["general.base"],"case_count":30,"completed_cases":0,"created_at":"2026-08-24T00:00:00Z"}`)
				return
			}
			_, _ = io.WriteString(w, `{"runs":[{"schema_version":"1","id":"eval-1","suite_id":"kern.go","suite_name":"Go","suite_version":"1.0.0","status":"completed","variants":["general.base"],"case_count":30,"completed_cases":30,"created_at":"2026-08-24T00:00:00Z"}]}`)
		case "/api/v1/evals/runs/eval-1":
			_, _ = io.WriteString(w, `{"schema_version":"1","id":"eval-1","suite_id":"kern.go","suite_name":"Go","suite_version":"1.0.0","status":"completed","variants":["general.base"],"case_count":30,"completed_cases":30,"created_at":"2026-08-24T00:00:00Z"}`)
		case "/api/v1/evals/runs/eval-1/cancel":
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"schema_version":"1","id":"eval-1","suite_id":"kern.go","suite_name":"Go","suite_version":"1.0.0","status":"cancelled","variants":["general.base"],"case_count":30,"completed_cases":0,"created_at":"2026-08-24T00:00:00Z"}`)
		case "/api/v1/evals/runs/eval-1/pause":
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"schema_version":"1","id":"eval-1","suite_id":"kern.go","suite_name":"Go","suite_version":"1.0.0","status":"paused","variants":["general.base"],"case_count":30,"completed_cases":10,"created_at":"2026-08-24T00:00:00Z"}`)
		case "/api/v1/evals/runs/eval-1/resume":
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"schema_version":"1","id":"eval-1","suite_id":"kern.go","suite_name":"Go","suite_version":"1.0.0","status":"queued","variants":["general.base"],"case_count":30,"completed_cases":10,"created_at":"2026-08-24T00:00:00Z"}`)
		case "/api/v1/evals/runs/eval-1/report":
			_, _ = io.WriteString(w, `{"schema_version":"1","run_id":"eval-1","suite_id":"kern.go","suite_version":"1.0.0","status":"completed","config_digest":"sha256:test","variants":[],"results":[],"reproducibility":{"core_version":"0.1.0","go_version":"go1.26.6","goos":"darwin","goarch":"arm64","defaults":{"timeout_ms":60000,"token_budget":10000,"cost_budget_micros":0,"retries":1,"worker_count":2,"allowed_commands":["go"]},"variants":[{"id":"general.base","agent":"kern","agent_version":"kern-core/0.1.0","model":"offline-baseline","plugins":[]}],"inputs":[{"case_id":"go.test","prompt_sha256":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","fixture_sha256":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]},"started_at":"2026-08-24T00:00:00Z","completed_at":"2026-08-24T00:01:00Z"}`)
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL, Token: "test-token"})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	created, err := client.CreateTask(t.Context(), CreateTaskInput{Goal: "prove SDK"})
	if err != nil || created.ID != "task-1" {
		t.Fatalf("CreateTask() = %#v, %v", created, err)
	}
	created, err = client.CreateTask(t.Context(), CreateTaskInput{
		Goal: "describe image",
		Attachments: []TaskAttachment{{
			Name: "screen.png", MediaType: "image/png", Data: []byte("png"),
		}},
	})
	if err != nil || created.ID != "task-1" || len(submittedTask.Attachments) != 1 ||
		string(submittedTask.Attachments[0].Data) != "png" {
		t.Fatalf("CreateTask(attachment) = %#v, input=%#v, %v", created, submittedTask, err)
	}
	health, err := client.Health(t.Context())
	if err != nil || health.Status != "ok" || health.Mode != "offline-baseline" {
		t.Fatalf("Health() = %#v, %v", health, err)
	}
	ready, err := client.Ready(t.Context())
	if err != nil || ready.Status != "ready" {
		t.Fatalf("Ready() = %#v, %v", ready, err)
	}
	settings, err := client.GetSettings(t.Context())
	if err != nil || settings.PolicyProfile != PolicyLocalSafe || settings.RetentionDays != 90 {
		t.Fatalf("GetSettings() = %#v, %v", settings, err)
	}
	settings, err = client.UpdateSettings(t.Context(), UpdateSettingsInput{
		MaxTurns: 8, MaxToolCalls: 24, MaxTokens: 100_000, MaxCostUSD: 3,
		TaskTimeout: "5m", PolicyProfile: PolicyReadOnly, RetentionDays: 45,
	})
	if err != nil || settings.MaxTurns != 8 || settings.PolicyProfile != PolicyReadOnly {
		t.Fatalf("UpdateSettings() = %#v, %v", settings, err)
	}
	preview, err := client.PreviewCleanup(t.Context())
	if err != nil || preview.TaskCount != 2 || preview.ArtifactBytes != 1024 {
		t.Fatalf("PreviewCleanup() = %#v, %v", preview, err)
	}
	cleanup, err := client.CleanupExpired(t.Context())
	if err != nil || cleanup.TaskCount != 2 || cleanup.RemovedObjects != 2 || cleanup.RemovedBytes != 768 {
		t.Fatalf("CleanupExpired() = %#v, %v", cleanup, err)
	}
	models, err := client.ListModelConfigs(t.Context())
	if err != nil || !models.CredentialStoreAvailable || len(models.Configs) != 1 ||
		models.Configs[0].Provider != ModelProviderOllama {
		t.Fatalf("ListModelConfigs() = %#v, %v", models, err)
	}
	modelInput := ModelConfigInput{
		Name: "Local Qwen", Provider: ModelProviderOllama,
		BaseURL: "http://127.0.0.1:11434", Model: "qwen3",
		APIKey: "write-only-test-key", SetDefault: true,
	}
	modelConfig, err := client.CreateModelConfig(t.Context(), modelInput)
	if err != nil || modelConfig.ID != "model-1" || !modelConfig.IsDefault {
		t.Fatalf("CreateModelConfig() = %#v, %v", modelConfig, err)
	}
	if submittedModel.APIKey != "write-only-test-key" {
		t.Fatalf("submitted model credential = %q", submittedModel.APIKey)
	}
	modelInput.APIKey = ""
	modelInput.Name = "Updated Qwen"
	modelConfig, err = client.UpdateModelConfig(t.Context(), modelConfig.ID, modelInput)
	if err != nil || modelConfig.Name != "Updated Qwen" {
		t.Fatalf("UpdateModelConfig() = %#v, %v", modelConfig, err)
	}
	modelTest, err := client.TestModelConfig(t.Context(), modelConfig.ID)
	if err != nil || !modelTest.OK || !modelTest.Capabilities["tools"] {
		t.Fatalf("TestModelConfig() = %#v, %v", modelTest, err)
	}
	if err := client.DeleteModelConfig(t.Context(), modelConfig.ID); err != nil {
		t.Fatalf("DeleteModelConfig() error = %v", err)
	}
	continued, err := client.SubmitInput(t.Context(), created.ID, "continue")
	if err != nil || continued.ActiveAttemptID != "attempt-2" || submittedInput.Content != "continue" ||
		len(submittedInput.Attachments) != 0 {
		t.Fatalf("SubmitInput() = %#v, %v", continued, err)
	}
	continued, err = client.SubmitTaskInput(t.Context(), created.ID, SubmitTaskInput{
		Content: "compare images",
		Attachments: []TaskAttachment{{
			Name: "follow-up.png", MediaType: "image/png", Data: []byte("png-2"),
		}},
	})
	if err != nil || continued.ActiveAttemptID != "attempt-2" ||
		submittedInput.Content != "compare images" || len(submittedInput.Attachments) != 1 ||
		string(submittedInput.Attachments[0].Data) != "png-2" {
		t.Fatalf("SubmitTaskInput() = %#v, input=%#v, %v", continued, submittedInput, err)
	}
	tasks, err := client.ListTasks(t.Context())
	if err != nil || len(tasks) != 1 || !tasks[0].Status.Terminal() {
		t.Fatalf("ListTasks() = %#v, %v", tasks, err)
	}
	metrics, err := client.Metrics(t.Context())
	if err != nil || metrics != "kern_tasks_created_total 1\n" {
		t.Fatalf("Metrics() = %q, %v", metrics, err)
	}
	body, headers, err := client.DownloadArtifact(t.Context(), "task-1", "artifact-1")
	if err != nil {
		t.Fatalf("DownloadArtifact() error = %v", err)
	}
	content, readErr := io.ReadAll(body)
	closeErr := body.Close()
	if readErr != nil || closeErr != nil || string(content) != "evidence" ||
		!strings.HasPrefix(headers.Get("Content-Type"), "text/plain") {
		t.Fatalf("artifact content=%q headers=%v read=%v close=%v", content, headers, readErr, closeErr)
	}
	_, err = client.GetTask(t.Context(), "missing")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound || apiErr.Message != "task not found" {
		t.Fatalf("GetTask(missing) error = %#v", err)
	}
	plugins, err := client.ListPlugins(t.Context())
	if err != nil || len(plugins) != 1 || plugins[0].ID != "dev.kern.go" || !plugins[0].Enabled {
		t.Fatalf("ListPlugins() = %#v, %v", plugins, err)
	}
	installed, err := client.InstallPlugin(t.Context(), InstallPluginInput{Source: "/plugins/go"})
	if err != nil || installed.Enabled {
		t.Fatalf("InstallPlugin() = %#v, %v", installed, err)
	}
	enabled, err := client.EnablePlugin(t.Context(), installed.ID)
	if err != nil || !enabled.Enabled {
		t.Fatalf("EnablePlugin() = %#v, %v", enabled, err)
	}
	got, err := client.GetPlugin(t.Context(), enabled.ID)
	if err != nil || got.Manifest.Entrypoints.Knowledge[0] != "go.md" {
		t.Fatalf("GetPlugin() = %#v, %v", got, err)
	}
	if err := client.RemovePlugin(t.Context(), enabled.ID); err != nil {
		t.Fatalf("RemovePlugin() error = %v", err)
	}
	usage, err := client.ListTaskPlugins(t.Context(), "task-1")
	if err != nil || len(usage) != 1 || usage[0].PluginID != "dev.kern.go" {
		t.Fatalf("ListTaskPlugins() = %#v, %v", usage, err)
	}
	currentPlan, err := client.GetPlan(t.Context(), "task-1")
	if err != nil || currentPlan == nil || len(currentPlan.Steps) != 1 || currentPlan.Steps[0].Phase != "prepare" {
		t.Fatalf("GetPlan() = %#v, %v", currentPlan, err)
	}
	uncertain, err := client.ListUncertainOperations(t.Context(), "task-1")
	if err != nil || len(uncertain) != 1 || uncertain[0].Status != "unknown" {
		t.Fatalf("ListUncertainOperations() = %#v, %v", uncertain, err)
	}
	receipt, err := client.ResolveUncertainOperation(
		t.Context(), uncertain[0].ID, OperationConfirmedNotExecuted,
	)
	if err != nil || receipt.Resolution != OperationConfirmedNotExecuted {
		t.Fatalf("ResolveUncertainOperation() = %#v, %v", receipt, err)
	}
	if _, err := client.ResolveUncertainOperation(t.Context(), uncertain[0].ID, "maybe"); err == nil {
		t.Fatal("ResolveUncertainOperation(invalid) error = nil")
	}
	evalRun, err := client.StartEvaluation(t.Context(), StartEvaluationInput{SuitePath: "evals/go", Variants: []string{"general.base"}})
	if err != nil || evalRun.ID != "eval-1" || evalRun.Status != EvaluationQueued {
		t.Fatalf("StartEvaluation() = %#v, %v", evalRun, err)
	}
	evalRuns, err := client.ListEvaluationRuns(t.Context(), 20)
	if err != nil || len(evalRuns) != 1 || !evalRuns[0].Status.Terminal() {
		t.Fatalf("ListEvaluationRuns() = %#v, %v", evalRuns, err)
	}
	evalRun, err = client.GetEvaluationRun(t.Context(), "eval-1")
	if err != nil || evalRun.CompletedCases != 30 {
		t.Fatalf("GetEvaluationRun() = %#v, %v", evalRun, err)
	}
	pausedEval, err := client.PauseEvaluation(t.Context(), "eval-1")
	if err != nil || pausedEval.Status != EvaluationPaused || pausedEval.CompletedCases != 10 {
		t.Fatalf("PauseEvaluation() = %#v, %v", pausedEval, err)
	}
	resumedEval, err := client.ResumeEvaluation(t.Context(), "eval-1")
	if err != nil || resumedEval.Status != EvaluationQueued || resumedEval.CompletedCases != 10 {
		t.Fatalf("ResumeEvaluation() = %#v, %v", resumedEval, err)
	}
	cancelledEval, err := client.CancelEvaluation(t.Context(), "eval-1")
	if err != nil || cancelledEval.Status != EvaluationCancelled {
		t.Fatalf("CancelEvaluation() = %#v, %v", cancelledEval, err)
	}
	evalReport, err := client.GetEvaluationReport(t.Context(), "eval-1")
	if err != nil || evalReport.ConfigDigest != "sha256:test" ||
		evalReport.Reproducibility.CoreVersion != "0.1.0" ||
		len(evalReport.Reproducibility.Inputs) != 1 ||
		evalReport.Reproducibility.Variants[0].Model != "offline-baseline" {
		t.Fatalf("GetEvaluationReport() = %#v, %v", evalReport, err)
	}
}

func TestEventStreamReplaysAfterCursor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Last-Event-ID"); got != "41" {
			t.Errorf("Last-Event-ID = %q, want 41", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": keep-alive\n\nid: 42\nevent: task.completed\ndata: {\"schema_version\":\"1\",\"id\":42,\"task_id\":\"task-1\",\"type\":\"task.completed\",\"payload\":{}}\n\n")
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	stream, err := client.OpenEventStream(context.Background(), "task-1", 41)
	if err != nil {
		t.Fatalf("OpenEventStream() error = %v", err)
	}
	defer stream.Close()
	event, err := stream.Next()
	if err != nil || event.ID != 42 || event.Type != "task.completed" {
		t.Fatalf("Next() = %#v, %v", event, err)
	}
}

func TestNewClientRejectsUnsafeBaseURL(t *testing.T) {
	for _, baseURL := range []string{"", "ftp://localhost", "http://user:pass@localhost", "http://localhost?q=1"} {
		if _, err := NewClient(Config{BaseURL: baseURL}); err == nil {
			t.Fatalf("NewClient(%q) error = nil", baseURL)
		}
	}
}
