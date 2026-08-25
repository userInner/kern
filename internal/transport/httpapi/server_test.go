package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/userInner/kern/internal/app"
	"github.com/userInner/kern/internal/approval"
	"github.com/userInner/kern/internal/artifact"
	"github.com/userInner/kern/internal/evalservice"
	"github.com/userInner/kern/internal/evaluation"
	"github.com/userInner/kern/internal/operation"
	"github.com/userInner/kern/internal/plan"
	"github.com/userInner/kern/internal/plugin"
	"github.com/userInner/kern/internal/secret"
	"github.com/userInner/kern/internal/task"
)

func newLoopbackTestServer(t *testing.T, config Config) *httptest.Server {
	t.Helper()

	server := httptest.NewUnstartedServer(nil)
	config.Origin = "http://" + server.Listener.Addr().String()
	config.AllowPlainHTTPLoopback = true
	handler, err := New(config)
	if err != nil {
		server.Close()
		t.Fatalf("New() error = %v", err)
	}
	server.Config.Handler = handler
	server.Start()
	t.Cleanup(server.Close)
	return server
}

func newDirectTestHandler(t *testing.T, config Config) http.Handler {
	t.Helper()
	config.Origin = "http://127.0.0.1:8787"
	config.AllowPlainHTTPLoopback = true
	handler, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = "127.0.0.1:8787"
		r.RemoteAddr = "127.0.0.1:50000"
		handler.ServeHTTP(w, r)
	})
}

type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}

func authenticatedTestClient(server *httptest.Server, token string) *http.Client {
	client := server.Client()
	client.Transport = bearerTransport{base: client.Transport, token: token}
	return client
}

func TestServerTaskAndSSEFlow(t *testing.T) {
	t.Setenv("KERN_MODEL_BASE_URL", "")
	t.Setenv("KERN_MODEL", "")
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	runtime, err := app.Open(ctx, app.Config{
		DataDir: t.TempDir(),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("app.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	server := newLoopbackTestServer(t, Config{
		Store:          runtime.Store,
		Artifacts:      runtime.Artifacts,
		Submitter:      runtime,
		MetricsEnabled: true,
		Token:          "test-token",
		Mode:           runtime.Mode,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	client := authenticatedTestClient(server, "test-token")

	response, err := client.Get(server.URL + "/")
	if err != nil {
		t.Fatalf("GET / error = %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d", response.StatusCode)
	}
	if traceparent := response.Header.Get("Traceparent"); len(traceparent) != 55 {
		t.Fatalf("GET / Traceparent = %q", traceparent)
	}
	response, err = client.Get(server.URL + "/api/v1/ready")
	if err != nil {
		t.Fatalf("GET ready error = %v", err)
	}
	readyBody, readyErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readyErr != nil || response.StatusCode != http.StatusOK ||
		!strings.Contains(string(readyBody), `"status":"ready"`) {
		t.Fatalf("GET ready status=%d body=%s error=%v", response.StatusCode, readyBody, readyErr)
	}

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		server.URL+"/api/v1/tasks",
		bytes.NewBufferString(`{"goal":"prove http flow"}`),
	)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", server.URL)
	response, err = client.Do(request)
	if err != nil {
		t.Fatalf("POST /api/v1/tasks error = %v", err)
	}
	createdBody, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /api/v1/tasks status = %d, body = %s", response.StatusCode, createdBody)
	}
	if traceID := jsonField(t, createdBody, "trace_id"); len(traceID) != 32 {
		t.Fatalf("created trace_id = %q", traceID)
	}
	taskID := jsonField(t, createdBody, "id")
	completed, err := runtime.Wait(ctx, taskID)
	if err != nil {
		t.Fatalf("runtime.Wait() error = %v", err)
	}
	response, err = client.Get(server.URL + "/api/v1/tasks/" + taskID + "/verifications")
	if err != nil {
		t.Fatalf("GET verifications error = %v", err)
	}
	verificationBody, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK ||
		!strings.Contains(string(verificationBody), `"verifier":"core.aggregate"`) ||
		!strings.Contains(string(verificationBody), `"status":"verified"`) {
		t.Fatalf(
			"GET verifications status=%d body=%s error=%v",
			response.StatusCode,
			verificationBody,
			readErr,
		)
	}
	createdArtifact, err := runtime.Artifacts.Put(
		ctx,
		completed.ID,
		completed.ActiveAttemptID,
		"evidence.txt",
		"text/plain",
		"",
		strings.NewReader("downloadable evidence"),
	)
	if err != nil {
		t.Fatalf("Artifacts.Put() error = %v", err)
	}
	response, err = client.Get(server.URL + "/api/v1/tasks/" + taskID + "/artifacts")
	if err != nil {
		t.Fatalf("GET artifacts error = %v", err)
	}
	listBody, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(listBody), createdArtifact.ID) {
		t.Fatalf("GET artifacts status=%d body=%s error=%v", response.StatusCode, listBody, err)
	}
	response, err = client.Get(
		server.URL + "/api/v1/tasks/" + taskID + "/artifacts/" + createdArtifact.ID,
	)
	if err != nil {
		t.Fatalf("GET artifact content error = %v", err)
	}
	downloadBody, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || string(downloadBody) != "downloadable evidence" {
		t.Fatalf("GET artifact status=%d body=%q error=%v", response.StatusCode, downloadBody, err)
	}
	if disposition := response.Header.Get("Content-Disposition"); !strings.Contains(disposition, "evidence.txt") {
		t.Fatalf("Content-Disposition = %q", disposition)
	}
	response, err = client.Get(server.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET metrics error = %v", err)
	}
	metricsBody, metricsErr := io.ReadAll(response.Body)
	response.Body.Close()
	if metricsErr != nil || response.StatusCode != http.StatusOK ||
		!strings.Contains(response.Header.Get("Content-Type"), "text/plain") ||
		!strings.Contains(string(metricsBody), "kern_tasks_created_total 1") ||
		!strings.Contains(string(metricsBody), `kern_tasks_current{status="completed"} 1`) {
		t.Fatalf("GET metrics status=%d body=%s error=%v", response.StatusCode, metricsBody, metricsErr)
	}

	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()
	request, err = http.NewRequestWithContext(
		streamCtx,
		http.MethodGet,
		server.URL+"/api/v1/tasks/"+taskID+"/events?after=0",
		nil,
	)
	if err != nil {
		t.Fatalf("NewRequest(SSE) error = %v", err)
	}
	response, err = client.Do(request)
	if err != nil {
		t.Fatalf("GET events error = %v", err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("ReadString() error = %v", err)
	}
	if !strings.HasPrefix(line, "id: ") {
		t.Fatalf("first SSE line = %q, want id", line)
	}
}

func TestMetricsRequireAuthenticationAndRespectConfiguration(t *testing.T) {
	t.Parallel()
	runtime, err := app.Open(t.Context(), app.Config{
		DataDir: t.TempDir(),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	newServer := func(enabled bool) *httptest.Server {
		return newLoopbackTestServer(t, Config{
			Store: runtime.Store, Artifacts: runtime.Artifacts, Submitter: runtime,
			MetricsEnabled: enabled, Token: "metrics-token", Mode: runtime.Mode,
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
	}

	enabled := newServer(true)
	response, err := enabled.Client().Get(enabled.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated metrics status = %d", response.StatusCode)
	}
	request, err := http.NewRequest(http.MethodGet, enabled.URL+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer metrics-token")
	request.Header.Set("Traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	response, err = enabled.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK ||
		!strings.HasPrefix(response.Header.Get("Traceparent"), "00-4bf92f3577b34da6a3ce929d0e0e4736-") {
		t.Fatalf("authenticated metrics status=%d traceparent=%q", response.StatusCode, response.Header.Get("Traceparent"))
	}

	disabled := newServer(false)
	request, err = http.NewRequest(http.MethodGet, disabled.URL+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer metrics-token")
	response, err = disabled.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled metrics status = %d", response.StatusCode)
	}
}

func TestServerPluginLifecycle(t *testing.T) {
	t.Parallel()
	workspaceRoot := t.TempDir()
	runtime, err := app.Open(t.Context(), app.Config{
		DataDir:      t.TempDir(),
		WorkspaceDir: workspaceRoot,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("app.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	pluginImportRoot, err := os.OpenRoot(workspaceRoot)
	if err != nil {
		t.Fatalf("os.OpenRoot() error = %v", err)
	}
	t.Cleanup(func() { _ = pluginImportRoot.Close() })
	server := newLoopbackTestServer(t, Config{
		Store:            runtime.Store,
		Artifacts:        runtime.Artifacts,
		Submitter:        runtime,
		Plugins:          runtime.Plugins,
		PluginImportRoot: pluginImportRoot,
		Token:            "plugin-token",
		Mode:             runtime.Mode,
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	const source = "packages/http"
	createHTTPPluginPackage(t, filepath.Join(workspaceRoot, filepath.FromSlash(source)), "dev.kern.http")
	for _, unsafeSource := range []string{
		filepath.Join(workspaceRoot, filepath.FromSlash(source)),
		"../http",
		`packages\http`,
		"packages/http\x00ignored",
	} {
		unsafeBody, marshalErr := json.Marshal(map[string]any{"source": unsafeSource})
		if marshalErr != nil {
			t.Fatalf("json.Marshal(unsafe source) error = %v", marshalErr)
		}
		response := pluginRequest(
			t,
			server.Client(),
			http.MethodPost,
			server.URL+"/api/v1/plugins/install",
			unsafeBody,
		)
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("unsafe source %q status = %d, want %d", unsafeSource, response.StatusCode, http.StatusBadRequest)
		}
	}
	installBody, err := json.Marshal(map[string]any{"source": source, "enable": true})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	response := pluginRequest(t, server.Client(), http.MethodPost, server.URL+"/api/v1/plugins/install", installBody)
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusCreated ||
		!strings.Contains(string(body), `"id":"dev.kern.http"`) ||
		!strings.Contains(string(body), `"enabled":true`) ||
		strings.Contains(string(body), `"install_path"`) {
		t.Fatalf("install status=%d body=%s error=%v", response.StatusCode, body, readErr)
	}

	response = pluginRequest(t, server.Client(), http.MethodGet, server.URL+"/api/v1/plugins", nil)
	body, readErr = io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK ||
		!strings.Contains(string(body), `"dev.kern.http"`) {
		t.Fatalf("list status=%d body=%s error=%v", response.StatusCode, body, readErr)
	}

	response = pluginRequest(t, server.Client(), http.MethodPost, server.URL+"/api/v1/plugins/dev.kern.http/disable", nil)
	body, readErr = io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK ||
		!strings.Contains(string(body), `"enabled":false`) {
		t.Fatalf("disable status=%d body=%s error=%v", response.StatusCode, body, readErr)
	}

	response = pluginRequest(
		t,
		server.Client(),
		http.MethodPost,
		server.URL+"/api/v1/tasks",
		[]byte(`{"goal":"review this project","plugins":{"enable":["dev.kern.http"]}}`),
	)
	body, readErr = io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusAccepted {
		t.Fatalf("create selected task status=%d body=%s error=%v", response.StatusCode, body, readErr)
	}
	taskID := jsonField(t, body, "id")
	completed, err := runtime.Wait(t.Context(), taskID)
	if err != nil {
		t.Fatalf("Wait(selected task) error = %v", err)
	}
	response = pluginRequest(
		t,
		server.Client(),
		http.MethodGet,
		server.URL+"/api/v1/tasks/"+completed.ID+"/plugins",
		nil,
	)
	body, readErr = io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK ||
		!strings.Contains(string(body), `"plugin_id":"dev.kern.http"`) ||
		!strings.Contains(string(body), `"reason":"manual_enable"`) {
		t.Fatalf("task plugins status=%d body=%s error=%v", response.StatusCode, body, readErr)
	}

	response = pluginRequest(t, server.Client(), http.MethodDelete, server.URL+"/api/v1/plugins/dev.kern.http", nil)
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("remove status=%d", response.StatusCode)
	}
	response = pluginRequest(t, server.Client(), http.MethodGet, server.URL+"/api/v1/plugins/dev.kern.http", nil)
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("get removed status=%d", response.StatusCode)
	}
}

func TestServerEvaluationAPI(t *testing.T) {
	t.Parallel()
	runtime, err := app.Open(t.Context(), app.Config{
		DataDir: t.TempDir(),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("app.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	now := time.Now().UTC()
	run := evaluation.Run{
		SchemaVersion: evaluation.SchemaVersion,
		ID:            "eval-run", SuiteID: "kern.go.test", SuiteName: "Go Test", SuiteVersion: "1.0.0",
		Status: evaluation.StatusCompleted, Variants: []string{"general.base"},
		CaseCount: 1, CompletedCases: 1, CreatedAt: now,
	}
	report := evaluation.Report{
		SchemaVersion: evaluation.SchemaVersion, RunID: run.ID,
		SuiteID: run.SuiteID, SuiteVersion: run.SuiteVersion, Status: evaluation.StatusCompleted,
		StartedAt: now, CompletedAt: now,
	}
	evals := &fakeEvaluationManager{run: run, report: report}
	server := newLoopbackTestServer(t, Config{
		Store: runtime.Store, Artifacts: runtime.Artifacts, Submitter: runtime,
		Evals: evals, Token: "eval-token", Mode: runtime.Mode,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	response := authenticatedRequest(t, server.Client(), "eval-token", http.MethodPost, server.URL+"/api/v1/evals/runs", []byte(`{"suite_path":"evals/go","variants":["general.base"]}`))
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusAccepted || !strings.Contains(string(body), `"id":"eval-run"`) || evals.input.SuitePath != "evals/go" {
		t.Fatalf("start eval status=%d body=%s error=%v input=%#v", response.StatusCode, body, readErr, evals.input)
	}
	for _, endpoint := range []string{"/api/v1/evals/runs", "/api/v1/evals/runs/eval-run"} {
		response = authenticatedRequest(t, server.Client(), "eval-token", http.MethodGet, server.URL+endpoint, nil)
		body, readErr = io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(body), "eval-run") {
			t.Fatalf("GET %s status=%d body=%s error=%v", endpoint, response.StatusCode, body, readErr)
		}
	}
	response = authenticatedRequest(t, server.Client(), "eval-token", http.MethodGet, server.URL+"/api/v1/evals/runs/eval-run/report", nil)
	body, readErr = io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"run_id":"eval-run"`) ||
		!strings.Contains(response.Header.Get("Content-Disposition"), "kern-eval-eval-run.json") {
		t.Fatalf("report status=%d body=%s disposition=%q error=%v", response.StatusCode, body, response.Header.Get("Content-Disposition"), readErr)
	}
	response = authenticatedRequest(t, server.Client(), "eval-token", http.MethodPost, server.URL+"/api/v1/evals/runs/eval-run/pause", nil)
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted || !evals.paused {
		t.Fatalf("pause status=%d paused=%t", response.StatusCode, evals.paused)
	}
	response = authenticatedRequest(t, server.Client(), "eval-token", http.MethodPost, server.URL+"/api/v1/evals/runs/eval-run/resume", nil)
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted || !evals.resumed {
		t.Fatalf("resume status=%d resumed=%t", response.StatusCode, evals.resumed)
	}
	response = authenticatedRequest(t, server.Client(), "eval-token", http.MethodPost, server.URL+"/api/v1/evals/runs/eval-run/cancel", nil)
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted || !evals.cancelled {
		t.Fatalf("cancel status=%d cancelled=%t", response.StatusCode, evals.cancelled)
	}
}

func TestServerSettingsAndConfirmedCleanupFlow(t *testing.T) {
	t.Setenv("KERN_MODEL_BASE_URL", "")
	t.Setenv("KERN_MODEL", "")
	runtime, err := app.Open(t.Context(), app.Config{
		DataDir: t.TempDir(), SettingsPath: filepath.Join(t.TempDir(), "config.json"),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("app.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	server := newLoopbackTestServer(t, Config{
		Store: runtime.Store, Artifacts: runtime.Artifacts, Submitter: runtime,
		Token: "settings-token", Mode: runtime.Mode,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	response := authenticatedRequest(
		t, server.Client(), "settings-token", http.MethodGet, server.URL+"/api/v1/settings", nil,
	)
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK ||
		!strings.Contains(string(body), `"policy_profile":"local-safe"`) {
		t.Fatalf("GET settings status=%d body=%s error=%v", response.StatusCode, body, readErr)
	}
	updatedBody := []byte(`{
		"max_turns":7,
		"max_tool_calls":11,
		"max_tokens":9000,
		"max_cost_usd":4.25,
		"task_timeout":"15m",
		"policy_profile":"confirm-all",
		"retention_days":30
	}`)
	response = authenticatedRequest(
		t, server.Client(), "settings-token", http.MethodPut, server.URL+"/api/v1/settings", updatedBody,
	)
	body, readErr = io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK ||
		!strings.Contains(string(body), `"max_turns":7`) ||
		!strings.Contains(string(body), `"policy_profile":"confirm-all"`) {
		t.Fatalf("PUT settings status=%d body=%s error=%v", response.StatusCode, body, readErr)
	}
	response = authenticatedRequest(
		t, server.Client(), "settings-token", http.MethodGet,
		server.URL+"/api/v1/settings/cleanup-preview", nil,
	)
	body, readErr = io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK ||
		!strings.Contains(string(body), `"retention_days":30`) ||
		!strings.Contains(string(body), `"task_count":0`) {
		t.Fatalf("GET cleanup preview status=%d body=%s error=%v", response.StatusCode, body, readErr)
	}
	response = authenticatedRequest(
		t, server.Client(), "settings-token", http.MethodPost,
		server.URL+"/api/v1/settings/cleanup", []byte(`{"confirm":false}`),
	)
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST cleanup without confirmation status=%d", response.StatusCode)
	}
	response = authenticatedRequest(
		t, server.Client(), "settings-token", http.MethodPost,
		server.URL+"/api/v1/settings/cleanup", []byte(`{"confirm":true}`),
	)
	body, readErr = io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK ||
		!strings.Contains(string(body), `"removed_objects":0`) {
		t.Fatalf("POST cleanup status=%d body=%s error=%v", response.StatusCode, body, readErr)
	}
	invalidBody := []byte(`{
		"max_turns":7,
		"max_tool_calls":11,
		"max_tokens":9000,
		"max_cost_usd":4.25,
		"task_timeout":"15m",
		"policy_profile":"confirm-all",
		"retention_days":30,
		"unknown":true
	}`)
	response = authenticatedRequest(
		t, server.Client(), "settings-token", http.MethodPut,
		server.URL+"/api/v1/settings", invalidBody,
	)
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT settings with unknown field status=%d", response.StatusCode)
	}
}

func pluginRequest(
	t *testing.T,
	client *http.Client,
	method string,
	endpoint string,
	body []byte,
) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), method, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("Authorization", "Bearer plugin-token")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("%s %s error = %v", method, endpoint, err)
	}
	return response
}

func authenticatedRequest(
	t *testing.T,
	client *http.Client,
	token string,
	method string,
	endpoint string,
	body []byte,
) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), method, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("%s %s error = %v", method, endpoint, err)
	}
	return response
}

type fakeEvaluationManager struct {
	run       evaluation.Run
	report    evaluation.Report
	input     evalservice.StartInput
	paused    bool
	resumed   bool
	cancelled bool
}

func (manager *fakeEvaluationManager) Pause(_ context.Context, runID string) (evaluation.Run, error) {
	if runID != manager.run.ID {
		return evaluation.Run{}, evaluation.ErrRunNotFound
	}
	manager.paused = true
	return manager.run, nil
}

func (manager *fakeEvaluationManager) Resume(_ context.Context, runID string) (evaluation.Run, error) {
	if runID != manager.run.ID {
		return evaluation.Run{}, evaluation.ErrRunNotFound
	}
	manager.resumed = true
	return manager.run, nil
}

func (manager *fakeEvaluationManager) Start(_ context.Context, input evalservice.StartInput) (evaluation.Run, error) {
	manager.input = input
	return manager.run, nil
}

func (manager *fakeEvaluationManager) Cancel(_ context.Context, runID string) (evaluation.Run, error) {
	if runID != manager.run.ID {
		return evaluation.Run{}, evaluation.ErrRunNotFound
	}
	manager.cancelled = true
	return manager.run, nil
}

func (manager *fakeEvaluationManager) Get(_ context.Context, runID string) (evaluation.Run, error) {
	if runID != manager.run.ID {
		return evaluation.Run{}, evaluation.ErrRunNotFound
	}
	return manager.run, nil
}

func (manager *fakeEvaluationManager) List(context.Context, int) ([]evaluation.Run, error) {
	return []evaluation.Run{manager.run}, nil
}

func (manager *fakeEvaluationManager) Report(_ context.Context, runID string) (evaluation.Report, error) {
	if runID != manager.run.ID {
		return evaluation.Report{}, evaluation.ErrRunNotFound
	}
	return manager.report, nil
}

func createHTTPPluginPackage(t *testing.T, directory, pluginID string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "knowledge.md"), []byte("HTTP plugin evidence\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	digest, err := plugin.PackageDigest(directory)
	if err != nil {
		t.Fatalf("PackageDigest() error = %v", err)
	}
	manifest := plugin.Manifest{
		SchemaVersion: plugin.SchemaVersion,
		ID:            pluginID,
		Name:          "HTTP Plugin",
		Version:       "0.1.0",
		Core:          ">=0.1.0 <0.2.0",
		Entrypoints:   plugin.Entrypoints{Knowledge: []string{"knowledge.md"}},
		Activation:    plugin.Activation{Intents: []string{"code.review"}},
		Permissions:   plugin.Permissions{Filesystem: []string{"workspace:read"}},
		Integrity:     plugin.Integrity{Files: digest},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("json.Marshal(manifest) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, plugin.ManifestFile), encoded, 0o600); err != nil {
		t.Fatalf("WriteFile(manifest) error = %v", err)
	}
}

func TestServerModelConfigAndTaskSelectionFlow(t *testing.T) {
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if authorization := request.Header.Get("Authorization"); authorization != "Bearer api-secret" {
			t.Errorf("Authorization = %q", authorization)
		}
		var input struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("Decode() error = %v", err)
		}
		if !input.Stream {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("x-request-id", "http-probe-request")
			_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"selected model\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer modelServer.Close()
	t.Setenv("KERN_MODEL_BASE_URL", "")
	t.Setenv("KERN_MODEL", "")
	vault := &httpTestVault{values: make(map[string]string)}
	runtime, err := app.Open(t.Context(), app.Config{
		DataDir:     t.TempDir(),
		SecretVault: vault,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("app.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	server := newLoopbackTestServer(t, Config{
		Store:     runtime.Store,
		Artifacts: runtime.Artifacts,
		Submitter: runtime,
		Models:    runtime,
		Token:     "model-test-token",
		Mode:      runtime.Mode,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	client := authenticatedTestClient(server, "model-test-token")
	response, err := client.Get(server.URL + "/")
	if err != nil {
		t.Fatalf("GET / error = %v", err)
	}
	response.Body.Close()

	createBody := `{"name":"Local test","provider":"openai-compatible","base_url":"` +
		modelServer.URL + `","model":"test-model","api_key":"api-secret"}`
	response = doJSONMutation(t, client, http.MethodPost, server.URL+"/api/v1/model-configs", server.URL, createBody)
	createdBody, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusCreated {
		t.Fatalf("create model config status=%d body=%s error=%v", response.StatusCode, createdBody, err)
	}
	configID := jsonField(t, createdBody, "id")
	if vault.count() != 1 || strings.Contains(string(createdBody), "api-secret") ||
		strings.Contains(string(createdBody), "keyring:") {
		t.Fatalf("credential persistence leaked or failed: body=%s count=%d", createdBody, vault.count())
	}
	response = doJSONMutation(
		t,
		client,
		http.MethodPost,
		server.URL+"/api/v1/model-configs/"+configID+"/test",
		server.URL,
		"",
	)
	probeBody, probeErr := io.ReadAll(response.Body)
	response.Body.Close()
	if probeErr != nil || response.StatusCode != http.StatusOK ||
		!strings.Contains(string(probeBody), `"ok":true`) ||
		!strings.Contains(string(probeBody), `"request_id":"http-probe-request"`) ||
		strings.Contains(string(probeBody), "KERN_HTTP_TEST_KEY") ||
		strings.Contains(string(probeBody), "api-secret") {
		t.Fatalf("test model config status=%d body=%s error=%v", response.StatusCode, probeBody, probeErr)
	}
	response, err = client.Get(server.URL + "/api/v1/models")
	if err != nil {
		t.Fatalf("GET models error = %v", err)
	}
	modelsBody, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || !strings.Contains(string(modelsBody), `"credential_store_available":true`) ||
		!strings.Contains(string(modelsBody), `"has_api_key":true`) ||
		strings.Contains(string(modelsBody), "KERN_HTTP_TEST_KEY") ||
		strings.Contains(string(modelsBody), "api-secret") {
		t.Fatalf("GET models body=%s error=%v", modelsBody, err)
	}

	response = doJSONMutation(
		t,
		client,
		http.MethodPost,
		server.URL+"/api/v1/tasks",
		server.URL,
		`{"goal":"use selected model","model_config_id":"`+configID+`"}`,
	)
	taskBody, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusAccepted {
		t.Fatalf("create task status=%d body=%s error=%v", response.StatusCode, taskBody, err)
	}
	createdTaskID := jsonField(t, taskBody, "id")
	completed, err := runtime.Wait(t.Context(), createdTaskID)
	if err != nil || completed.Result != "selected model" {
		t.Fatalf("Wait() = %#v, %v", completed, err)
	}
	response = doJSONMutation(
		t,
		client,
		http.MethodPost,
		server.URL+"/api/v1/tasks/"+createdTaskID+"/messages",
		server.URL,
		`{"content":"continue through HTTP"}`,
	)
	continuedBody, continuedErr := io.ReadAll(response.Body)
	response.Body.Close()
	if continuedErr != nil || response.StatusCode != http.StatusAccepted {
		t.Fatalf("submit task input status=%d body=%s error=%v", response.StatusCode, continuedBody, continuedErr)
	}
	continued, err := runtime.Wait(t.Context(), createdTaskID)
	if err != nil || continued.ActiveAttemptID == completed.ActiveAttemptID {
		t.Fatalf("continued task = %#v, %v", continued, err)
	}

	updateBody := `{"name":"Local test","provider":"openai-compatible","base_url":"` +
		modelServer.URL + `","model":"test-model","enabled":false}`
	response = doJSONMutation(
		t,
		client,
		http.MethodPut,
		server.URL+"/api/v1/model-configs/"+configID,
		server.URL,
		updateBody,
	)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("update model config status=%d", response.StatusCode)
	}
	response = doJSONMutation(
		t,
		client,
		http.MethodPost,
		server.URL+"/api/v1/tasks",
		server.URL,
		`{"goal":"must reject disabled model","model_config_id":"`+configID+`"}`,
	)
	response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("disabled model task status=%d", response.StatusCode)
	}
	response = doJSONMutation(
		t,
		client,
		http.MethodDelete,
		server.URL+"/api/v1/model-configs/"+configID,
		server.URL,
		"",
	)
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete model config status=%d", response.StatusCode)
	}
	if vault.count() != 0 {
		t.Fatalf("delete model config left %d credentials", vault.count())
	}
}

func TestServerReportsUnavailableCredentialServiceAndRejectsRawKey(t *testing.T) {
	t.Setenv("KERN_MODEL_BASE_URL", "")
	t.Setenv("KERN_MODEL", "")
	vault := &httpTestVault{
		values:   make(map[string]string),
		checkErr: errors.New("headless credential service"),
	}
	runtime, err := app.Open(t.Context(), app.Config{
		DataDir:     t.TempDir(),
		SecretVault: vault,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("app.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	server := newLoopbackTestServer(t, Config{
		Store: runtime.Store, Artifacts: runtime.Artifacts, Submitter: runtime,
		Models: runtime, Token: "unavailable-vault-token", Mode: runtime.Mode,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	client := authenticatedTestClient(server, "unavailable-vault-token")
	response, err := client.Get(server.URL + "/")
	if err != nil {
		t.Fatalf("GET / error = %v", err)
	}
	response.Body.Close()

	response, err = client.Get(server.URL + "/api/v1/models")
	if err != nil {
		t.Fatalf("GET models error = %v", err)
	}
	modelsBody, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK ||
		!strings.Contains(string(modelsBody), `"credential_store_available":false`) {
		t.Fatalf("GET models status=%d body=%s error=%v", response.StatusCode, modelsBody, readErr)
	}

	response = doJSONMutation(
		t, client, http.MethodPost, server.URL+"/api/v1/model-configs", server.URL,
		`{"name":"Unavailable","provider":"openai-compatible","base_url":"https://example.test","model":"test-model","api_key":"must-not-write"}`,
	)
	responseBody, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusServiceUnavailable ||
		!strings.Contains(string(responseBody), "system credential store is unavailable") || vault.count() != 0 {
		t.Fatalf("POST model status=%d body=%s vault=%d error=%v", response.StatusCode, responseBody, vault.count(), readErr)
	}
}

func TestValidateModelCredentialInputRejectsOversizedRawKey(t *testing.T) {
	value := strings.Repeat("x", app.MaxModelCredentialBytes+1)
	err := validateModelCredentialInput(modelConfigInput{APIKey: &value}, true)
	if err == nil || !strings.Contains(err.Error(), "must not exceed 2048 bytes") {
		t.Fatalf("validateModelCredentialInput() error = %v", err)
	}
}

func TestServerMapsNativeCredentialErrorsWithoutLeakingBackendDetails(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{
			name: "unavailable", err: fmt.Errorf("storing model credential: %w: private dbus detail", secret.ErrUnavailable),
			wantStatus: http.StatusServiceUnavailable, wantBody: "system credential store became unavailable",
		},
		{
			name: "too large", err: fmt.Errorf("storing model credential: %w: platform detail", secret.ErrTooLarge),
			wantStatus: http.StatusBadRequest, wantBody: "API key exceeds the portable system credential limit",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/v1/model-configs", nil)
			new(Server).writeModelConfigError(response, request, test.err)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), test.wantBody) ||
				strings.Contains(response.Body.String(), "private dbus detail") ||
				strings.Contains(response.Body.String(), "platform detail") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestServerInternalErrorNeverLogsTheCause(t *testing.T) {
	const secretValue = "sk-must-never-appear-in-logs"
	var logs bytes.Buffer
	server := &Server{logger: slog.New(slog.NewTextHandler(&logs, nil))}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/model-configs", nil)

	server.internalError(response, request, errors.New("credential adapter echoed "+secretValue))

	if response.Code != http.StatusInternalServerError || strings.Contains(logs.String(), secretValue) ||
		strings.Contains(logs.String(), "credential adapter echoed") ||
		!strings.Contains(logs.String(), "error_kind=internal") {
		t.Fatalf("status=%d logs=%q", response.Code, logs.String())
	}
}

type httpTestVault struct {
	mu       sync.Mutex
	values   map[string]string
	next     int
	checkErr error
}

func (v *httpTestVault) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return v.checkErr
}

func (v *httpTestVault) Owns(reference string) bool {
	return strings.HasPrefix(reference, "keyring:")
}

func (v *httpTestVault) Put(ctx context.Context, value string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.next++
	reference := fmt.Sprintf("keyring:http-%d", v.next)
	v.values[reference] = value
	return reference, nil
}

func (v *httpTestVault) Resolve(ctx context.Context, reference string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !v.Owns(reference) {
		return "", secret.ErrUnsupported
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	value, ok := v.values[reference]
	if !ok {
		return "", secret.ErrNotFound
	}
	return value, nil
}

func (v *httpTestVault) Delete(ctx context.Context, reference string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !v.Owns(reference) {
		return secret.ErrUnsupported
	}
	v.mu.Lock()
	delete(v.values, reference)
	v.mu.Unlock()
	return nil
}

func (v *httpTestVault) count() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.values)
}

func doJSONMutation(
	t *testing.T,
	client *http.Client,
	method string,
	url string,
	origin string,
	body string,
) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", origin)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("%s %s error = %v", method, url, err)
	}
	return response
}

func TestNewRequiresExplicitTrustedOrigin(t *testing.T) {
	t.Parallel()
	store := &stubStore{}
	base := Config{Store: store, Artifacts: store, Submitter: store, Token: "secret", Mode: "test"}

	tests := []struct {
		name                   string
		origin                 string
		allowPlainHTTPLoopback bool
	}{
		{name: "missing origin"},
		{name: "external HTTP origin", origin: "http://agent.example.test:8787", allowPlainHTTPLoopback: true},
		{name: "implicit insecure loopback transport", origin: "http://127.0.0.1:8787"},
		{name: "insecure exception on HTTPS", origin: "https://127.0.0.1", allowPlainHTTPLoopback: true},
		{name: "external HTTPS origin", origin: "https://agent.example.test"},
		{name: "zero port", origin: "https://agent.example.test:0"},
		{name: "oversized port", origin: "https://agent.example.test:65536"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := base
			config.Origin = tt.origin
			config.AllowPlainHTTPLoopback = tt.allowPlainHTTPLoopback
			if _, err := New(config); err == nil {
				t.Fatal("New() error = nil")
			}
		})
	}

	secure := base
	secure.Origin = "https://127.0.0.1"
	if _, err := New(secure); err != nil {
		t.Fatalf("New(HTTPS) error = %v", err)
	}
	loopback := base
	loopback.Origin = "http://127.0.0.1:8787"
	loopback.AllowPlainHTTPLoopback = true
	if _, err := New(loopback); err != nil {
		t.Fatalf("New(explicit HTTP loopback) error = %v", err)
	}
}

func TestCanonicalAuthorityNormalizesDefaultPorts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		scheme    string
		raw       string
		authority string
	}{
		{name: "implicit HTTPS", scheme: "https", raw: "agent.example.test", authority: "agent.example.test"},
		{name: "explicit HTTPS default", scheme: "https", raw: "agent.example.test:443", authority: "agent.example.test"},
		{name: "implicit HTTP IPv6", scheme: "http", raw: "[::1]", authority: "[::1]"},
		{name: "explicit HTTP default IPv6", scheme: "http", raw: "[::1]:80", authority: "[::1]"},
		{name: "nondefault port", scheme: "https", raw: "agent.example.test:8443", authority: "agent.example.test:8443"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authority, _, err := canonicalAuthority(tt.scheme, tt.raw)
			if err != nil || authority != tt.authority {
				t.Fatalf("canonicalAuthority() = %q, %v; want %q", authority, err, tt.authority)
			}
		})
	}
	for _, raw := range []string{"agent.example.test:0", "agent.example.test:65536", "agent.example.test:080"} {
		if _, _, err := canonicalAuthority("https", raw); err == nil {
			t.Fatalf("canonicalAuthority(%q) error = nil", raw)
		}
	}
}

func TestServerLocksTransportHostOriginAndBootstrapToken(t *testing.T) {
	t.Parallel()
	const trustedOrigin = "http://127.0.0.1:8787"
	store := &stubStore{item: task.Task{ID: "task-1", Status: task.StatusRunning}}
	server, err := New(Config{
		Store: store, Artifacts: store, Submitter: store,
		Token: "session-secret", Mode: "test", Origin: trustedOrigin,
		AllowPlainHTTPLoopback: true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "http://evil.example:8787/", nil)
	request.RemoteAddr = "127.0.0.1:51000"
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusMisdirectedRequest {
		t.Fatalf("untrusted Host status = %d, want %d", response.Code, http.StatusMisdirectedRequest)
	}
	if header := response.Header().Get("Set-Cookie"); header != "" {
		t.Fatalf("untrusted Host received Set-Cookie = %q", header)
	}

	request = httptest.NewRequest(http.MethodPost, "http://evil.example:8787/api/v1/tasks/task-1/pause", nil)
	request.RemoteAddr = "127.0.0.1:51001"
	request.Header.Set("Origin", "http://evil.example:8787")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusMisdirectedRequest || store.lastAction != "" {
		t.Fatalf("rebound mutation status=%d action=%q", response.Code, store.lastAction)
	}

	request = httptest.NewRequest(http.MethodGet, "http://evil.example:8787/api/v1/tasks", nil)
	request.RemoteAddr = "127.0.0.1:51002"
	request.Header.Set("Authorization", "Bearer session-secret")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusMisdirectedRequest {
		t.Fatalf("Bearer request with untrusted Host status = %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "http://evil.example:8787/", nil)
	request.Host = "127.0.0.1:8787"
	request.RemoteAddr = "127.0.0.1:51003"
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusMisdirectedRequest {
		t.Fatalf("untrusted absolute request target status = %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, trustedOrigin+"/", nil)
	request.RemoteAddr = "192.0.2.1:51004"
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusMisdirectedRequest {
		t.Fatalf("trusted Host with non-loopback peer status = %d", response.Code)
	}
	if header := response.Header().Get("Set-Cookie"); header != "" {
		t.Fatalf("non-loopback peer received Set-Cookie = %q", header)
	}

	request = httptest.NewRequest(http.MethodPost, trustedOrigin+"/api/v1/tasks/task-1/pause", nil)
	request.RemoteAddr = "127.0.0.1:51005"
	request.Header.Set("Origin", "http://evil.example:8787")
	request.Header.Set("Authorization", "Bearer session-secret")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || store.lastAction != "" {
		t.Fatalf("untrusted Origin status=%d action=%q", response.Code, store.lastAction)
	}

	request = httptest.NewRequest(http.MethodGet, trustedOrigin+"/", nil)
	request.RemoteAddr = "127.0.0.1:51006"
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("trusted Host status = %d", response.Code)
	}
	if header := response.Header().Get("Set-Cookie"); header != "" {
		t.Fatalf("trusted Host received Set-Cookie = %q", header)
	}
	bootstrapToken := bootstrapTokenFromHTML(t, response.Body.String())
	if bootstrapToken == "session-secret" {
		t.Fatal("bootstrap exposed the configured API token")
	}

	request = httptest.NewRequest(http.MethodPost, trustedOrigin+"/api/v1/tasks/task-1/pause", nil)
	request.RemoteAddr = "127.0.0.1:51007"
	request.Header.Set("Origin", trustedOrigin)
	request.Header.Set("Authorization", "Bearer "+bootstrapToken)
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || store.lastAction != "pause" {
		t.Fatalf("trusted mutation status=%d action=%q body=%s", response.Code, store.lastAction, response.Body)
	}

	request = httptest.NewRequest(http.MethodGet, trustedOrigin+"/api/v1/tasks", nil)
	request.RemoteAddr = "127.0.0.1:51008"
	request.Header.Set("Authorization", "Bearer "+bootstrapToken+"tampered")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("tampered bootstrap token status = %d", response.Code)
	}
}

func TestServerEnforcesConfiguredTLSMode(t *testing.T) {
	t.Parallel()
	store := &stubStore{}
	server, err := New(Config{
		Store: store, Artifacts: store, Submitter: store,
		Token: "session-secret", Mode: "test", Origin: "https://127.0.0.1",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "https://127.0.0.1/", nil)
	request.RemoteAddr = "127.0.0.1:51010"
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Set-Cookie") != "" {
		t.Fatalf("HTTPS response status=%d cookie=%q", response.Code, response.Header().Get("Set-Cookie"))
	}

	request = httptest.NewRequest(http.MethodGet, "https://127.0.0.1/", nil)
	request.RemoteAddr = "192.0.2.1:51012"
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusMisdirectedRequest {
		t.Fatalf("non-loopback HTTPS peer status = %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	request.Host = "127.0.0.1"
	request.RemoteAddr = "127.0.0.1:51011"
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusMisdirectedRequest {
		t.Fatalf("plain request to HTTPS configuration status = %d", response.Code)
	}

	httpServer, err := New(Config{
		Store: store, Artifacts: store, Submitter: store,
		Token: "session-secret", Mode: "test", Origin: "http://127.0.0.1:8787",
		AllowPlainHTTPLoopback: true,
	})
	if err != nil {
		t.Fatalf("New(HTTP) error = %v", err)
	}
	request = httptest.NewRequest(http.MethodGet, "https://127.0.0.1:8787/", nil)
	request.RemoteAddr = "127.0.0.1:51009"
	response = httptest.NewRecorder()
	httpServer.ServeHTTP(response, request)
	if response.Code != http.StatusMisdirectedRequest {
		t.Fatalf("TLS request to HTTP configuration status = %d", response.Code)
	}
}

func TestBrowserSessionTokenExpiresAndCannotBeForged(t *testing.T) {
	t.Parallel()
	store := &stubStore{}
	server, err := New(Config{
		Store: store, Artifacts: store, Submitter: store,
		Token: "persistent-api-token", Mode: "test", Origin: "https://127.0.0.1",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	issuedAt := time.Unix(1_800_000_000, 0)
	token, err := server.issueBrowserToken(issuedAt)
	if err != nil {
		t.Fatalf("issueBrowserToken() error = %v", err)
	}
	if !server.validBrowserToken(token, issuedAt.Add(browserSessionTTL-time.Second)) {
		t.Fatal("browser token expired before its deadline")
	}
	if server.validBrowserToken(token, issuedAt.Add(browserSessionTTL+time.Second)) {
		t.Fatal("browser token remained valid after its deadline")
	}
	if server.validBrowserToken(token+"x", issuedAt) {
		t.Fatal("forged browser token was accepted")
	}
}

func TestServerRejectsUnauthenticatedAPI(t *testing.T) {
	t.Parallel()

	store := &stubStore{}
	server := newDirectTestHandler(t, Config{
		Store: store, Artifacts: store, Submitter: store, Token: "secret", Mode: "test",
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestServerTaskControlActions(t *testing.T) {
	t.Parallel()
	store := &stubStore{item: task.Task{ID: "task-1", Status: task.StatusRunning}}
	server := newDirectTestHandler(t, Config{
		Store: store, Artifacts: store, Submitter: store, Token: "secret", Mode: "test",
	})
	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantAction string
	}{
		{name: "pause", path: "/api/v1/tasks/task-1/pause", wantStatus: http.StatusOK, wantAction: "pause"},
		{name: "cancel", path: "/api/v1/tasks/task-1/cancel", wantStatus: http.StatusOK, wantAction: "cancel"},
		{name: "resume", path: "/api/v1/tasks/task-1/resume", wantStatus: http.StatusAccepted, wantAction: "resume"},
		{name: "retry", path: "/api/v1/tasks/task-1/retry", wantStatus: http.StatusAccepted, wantAction: "retry"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, tt.path, nil)
			request.Header.Set("Authorization", "Bearer secret")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, tt.wantStatus, response.Body)
			}
			if store.lastAction != tt.wantAction {
				t.Fatalf("last action = %q, want %q", store.lastAction, tt.wantAction)
			}
		})
	}
}

func TestServerAcceptsSupplementaryTaskInput(t *testing.T) {
	t.Parallel()
	store := &stubStore{item: task.Task{ID: "task-1", Status: task.StatusCreated}}
	server := newDirectTestHandler(t, Config{
		Store: store, Artifacts: store, Submitter: store, Token: "secret", Mode: "test",
	})
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks/task-1/messages",
		strings.NewReader(`{"content":"continue with this evidence"}`),
	)
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || store.lastAction != "input:continue with this evidence" {
		t.Fatalf("status=%d action=%q body=%s", response.Code, store.lastAction, response.Body)
	}

	request = httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tasks/task-1/messages",
		strings.NewReader(`{"content":"compare images","attachments":[{"name":"screen.png","media_type":"image/png","data":"iVBORw0KGgo="}]}`),
	)
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || store.lastAction != "multimodal-input:compare images" ||
		len(store.lastInput.Attachments) != 1 || store.lastInput.Attachments[0].Name != "screen.png" ||
		!bytes.Equal(store.lastInput.Attachments[0].Data, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf(
			"multimodal status=%d action=%q input=%#v body=%s",
			response.Code,
			store.lastAction,
			store.lastInput,
			response.Body,
		)
	}
}

func TestServerApprovalEndpoints(t *testing.T) {
	t.Parallel()
	store := &stubStore{
		approvals: []approval.Request{{
			ID:          "approval-1",
			TaskID:      "task-1",
			OperationID: "operation-1",
			Status:      approval.StatusPending,
		}},
	}
	server := newDirectTestHandler(t, Config{
		Store: store, Artifacts: store, Submitter: store, Token: "secret", Mode: "test",
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/task-1/approvals", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "approval-1") {
		t.Fatalf("GET approvals status = %d, body = %s", response.Code, response.Body)
	}

	request = httptest.NewRequest(
		http.MethodPost,
		"/api/v1/approvals/approval-1/decision",
		strings.NewReader(`{"decision":"approved"}`),
	)
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || store.lastAction != "approval:approved" {
		t.Fatalf("POST decision status = %d, action = %q, body = %s", response.Code, store.lastAction, response.Body)
	}
}

func TestServerUncertainOperationEndpoints(t *testing.T) {
	t.Parallel()
	store := &stubStore{
		item: task.Task{ID: "task-1", Status: task.StatusWaitingInput},
		uncertain: []operation.Operation{{
			ID:     "operation-1",
			TaskID: "task-1",
			Tool:   "network",
			Effect: operation.EffectNetworkWrite,
			Status: operation.StatusUncertain,
		}},
	}
	server := newDirectTestHandler(t, Config{
		Store: store, Artifacts: store, Submitter: store, Token: "secret", Mode: "test",
	})
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/tasks/task-1/operations/uncertain",
		nil,
	)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "operation-1") {
		t.Fatalf("GET uncertain status = %d, body = %s", response.Code, response.Body)
	}

	request = httptest.NewRequest(
		http.MethodPost,
		"/api/v1/operations/operation-1/resolution",
		strings.NewReader(`{"resolution":"confirmed_not_executed"}`),
	)
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || store.lastAction != "resolution:confirmed_not_executed" {
		t.Fatalf("POST resolution status = %d, action = %q, body = %s", response.Code, store.lastAction, response.Body)
	}
}

func TestServerCurrentPlanEndpoint(t *testing.T) {
	t.Parallel()
	store := &stubStore{
		item: task.Task{ID: "task-1", Status: task.StatusRunning},
		currentPlan: plan.Plan{
			ID:       "plan-1",
			TaskID:   "task-1",
			Revision: 1,
			Status:   plan.StatusActive,
			Steps: []plan.Step{{
				ID:      "step-1",
				PlanID:  "plan-1",
				Ordinal: 1,
				Title:   "Inspect",
				Phase:   plan.PhasePrepare,
				Status:  plan.StepStatusCompleted,
			}},
		},
	}
	server := newDirectTestHandler(t, Config{
		Store: store, Artifacts: store, Submitter: store, Token: "secret", Mode: "test",
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/task-1/plan", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "plan-1") ||
		!strings.Contains(response.Body.String(), "Inspect") {
		t.Fatalf("GET plan status = %d, body = %s", response.Code, response.Body)
	}
}

func TestServerShutdownCancelsAdmittedRequests(t *testing.T) {
	t.Parallel()
	store := &blockingStore{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	server, err := New(Config{
		Store: store, Artifacts: store, Submitter: store,
		Token: "secret", Mode: "test", Origin: "http://127.0.0.1:8787",
		AllowPlainHTTPLoopback: true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/api/v1/tasks", nil)
		request.RemoteAddr = "127.0.0.1:51000"
		request.Header.Set("Authorization", "Bearer secret")
		server.ServeHTTP(httptest.NewRecorder(), request)
	}()
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("request did not enter dependency")
	}

	server.BeginShutdown()
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/api/v1/tasks", nil)
	request.RemoteAddr = "127.0.0.1:51001"
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || store.callCount() != 1 {
		t.Fatalf("new request status=%d dependency calls=%d", response.Code, store.callCount())
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("admitted request was not cancelled")
	}
	if err := server.WaitForIdle(t.Context()); err != nil {
		t.Fatalf("WaitForIdle() error = %v", err)
	}
}

func TestServerShutdownStopsAdmittedEventStream(t *testing.T) {
	t.Parallel()
	store := &stubStore{item: task.Task{ID: "task-1"}}
	server, err := New(Config{
		Store: store, Artifacts: store, Submitter: store,
		Token: "secret", Mode: "test", Origin: "http://127.0.0.1:8787",
		AllowPlainHTTPLoopback: true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	writer := newStreamResponseWriter()
	done := make(chan struct{})
	go func() {
		defer close(done)
		request := httptest.NewRequest(
			http.MethodGet,
			"http://127.0.0.1:8787/api/v1/tasks/task-1/events",
			nil,
		)
		request.RemoteAddr = "127.0.0.1:51000"
		request.Header.Set("Authorization", "Bearer secret")
		server.ServeHTTP(writer, request)
	}()
	select {
	case <-writer.flushed:
	case <-time.After(time.Second):
		t.Fatal("event stream did not flush its response headers")
	}

	server.BeginShutdown()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("event stream did not stop during shutdown")
	}
	if err := server.WaitForIdle(t.Context()); err != nil {
		t.Fatalf("WaitForIdle() error = %v", err)
	}
}

func bootstrapTokenFromHTML(t *testing.T, body string) string {
	t.Helper()
	const prefix = `<meta name="kern-session-token" content="`
	start := strings.Index(body, prefix)
	if start < 0 {
		t.Fatalf("bootstrap token missing from %s", body)
	}
	start += len(prefix)
	end := strings.Index(body[start:], `">`)
	if end < 0 {
		t.Fatalf("bootstrap token is unterminated in %s", body)
	}
	return body[start : start+end]
}

func jsonField(t *testing.T, body []byte, field string) string {
	t.Helper()
	text := string(body)
	prefix := `"` + field + `":"`
	start := strings.Index(text, prefix)
	if start < 0 {
		t.Fatalf("field %q missing from %s", field, text)
	}
	start += len(prefix)
	end := strings.Index(text[start:], `"`)
	if end < 0 {
		t.Fatalf("field %q is unterminated in %s", field, text)
	}
	return text[start : start+end]
}

type stubStore struct {
	item          task.Task
	lastAction    string
	lastInput     app.TaskInput
	approvals     []approval.Request
	uncertain     []operation.Operation
	currentPlan   plan.Plan
	verifications []task.Verification
}

type blockingStore struct {
	stubStore
	mu      sync.Mutex
	calls   int
	entered chan struct{}
	release chan struct{}
}

type streamResponseWriter struct {
	header    http.Header
	flushed   chan struct{}
	flushOnce sync.Once
	mu        sync.Mutex
	status    int
}

func newStreamResponseWriter() *streamResponseWriter {
	return &streamResponseWriter{header: make(http.Header), flushed: make(chan struct{})}
}

func (w *streamResponseWriter) Header() http.Header {
	return w.header
}

func (w *streamResponseWriter) WriteHeader(status int) {
	w.mu.Lock()
	w.status = status
	w.mu.Unlock()
}

func (w *streamResponseWriter) Write(data []byte) (int, error) {
	return len(data), nil
}

func (w *streamResponseWriter) Flush() {
	w.flushOnce.Do(func() { close(w.flushed) })
}

func (s *blockingStore) ListTasks(ctx context.Context, _ int) ([]task.Task, error) {
	s.mu.Lock()
	s.calls++
	first := s.calls == 1
	s.mu.Unlock()
	if first {
		close(s.entered)
	}
	select {
	case <-s.release:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *blockingStore) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *stubStore) GetTask(context.Context, string) (task.Task, error) {
	if s.item.ID != "" {
		return s.item, nil
	}
	return task.Task{}, task.ErrNotFound
}

func (s *stubStore) ListTasks(context.Context, int) ([]task.Task, error) {
	return []task.Task{}, nil
}

func (s *stubStore) EventsAfter(context.Context, string, int64, int) ([]task.Event, error) {
	return []task.Event{}, nil
}

func (s *stubStore) PendingApprovals(context.Context, string) ([]approval.Request, error) {
	return s.approvals, nil
}

func (s *stubStore) ListUncertainOperations(context.Context, string) ([]operation.Operation, error) {
	return s.uncertain, nil
}

func (s *stubStore) CurrentPlan(context.Context, string) (plan.Plan, error) {
	if s.currentPlan.ID == "" {
		return plan.Plan{}, plan.ErrNotFound
	}
	return s.currentPlan, nil
}

func (s *stubStore) ListVerifications(context.Context, string, string) ([]task.Verification, error) {
	return s.verifications, nil
}

func (s *stubStore) List(context.Context, string) ([]artifact.Artifact, error) {
	return []artifact.Artifact{}, nil
}

func (s *stubStore) OpenContent(context.Context, string, string) (artifact.Artifact, *os.File, error) {
	return artifact.Artifact{}, nil, artifact.ErrNotFound
}

func (s *stubStore) Submit(context.Context, string, string) (task.Task, error) {
	return task.Task{}, nil
}

func (s *stubStore) Pause(context.Context, string) (task.Task, error) {
	s.lastAction = "pause"
	return s.item, nil
}

func (s *stubStore) Cancel(context.Context, string) (task.Task, error) {
	s.lastAction = "cancel"
	return s.item, nil
}

func (s *stubStore) Resume(context.Context, string) (task.Task, error) {
	s.lastAction = "resume"
	return s.item, nil
}

func (s *stubStore) Retry(context.Context, string) (task.Task, error) {
	s.lastAction = "retry"
	return s.item, nil
}

func (s *stubStore) SubmitInput(_ context.Context, _ string, input string) (task.Task, error) {
	s.lastAction = "input:" + input
	return s.item, nil
}

func (s *stubStore) SubmitInputWithAttachments(
	_ context.Context,
	_ string,
	input app.TaskInput,
) (task.Task, error) {
	s.lastAction = "multimodal-input:" + input.Content
	s.lastInput = input
	return s.item, nil
}

func (s *stubStore) DecideApproval(
	_ context.Context,
	_ string,
	decision approval.Decision,
) (approval.Receipt, error) {
	s.lastAction = "approval:" + string(decision)
	return approval.Receipt{Decision: decision}, nil
}

func (s *stubStore) ResolveUncertainOperation(
	_ context.Context,
	operationID string,
	resolution operation.Resolution,
) (operation.ResolutionReceipt, error) {
	s.lastAction = "resolution:" + string(resolution)
	return operation.ResolutionReceipt{
		OperationID: operationID,
		Resolution:  resolution,
	}, nil
}
