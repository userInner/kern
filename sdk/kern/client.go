package kern

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maxErrorBody = 64 << 10

const maxMetricsBody = 1 << 20

// Config configures a Kern API client.
type Config struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

// Client calls one Kern Core server.
type Client struct {
	baseURL    *url.URL
	token      string
	httpClient *http.Client
}

// APIError is a non-success response with a safe, bounded server message.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("kern: API returned %d: %s", e.StatusCode, e.Message)
}

// NewClient validates config and constructs a reusable client.
func NewClient(config Config) (*Client, error) {
	baseURL, err := url.Parse(strings.TrimSpace(config.BaseURL))
	if err != nil {
		return nil, fmt.Errorf("kern: parsing base URL: %w", err)
	}
	if (baseURL.Scheme != "http" && baseURL.Scheme != "https") || baseURL.Host == "" {
		return nil, errors.New("kern: base URL must be an http or https origin")
	}
	if baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return nil, errors.New("kern: base URL must not contain credentials, query, or fragment")
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/")
	client := config.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return &Client{baseURL: baseURL, token: strings.TrimSpace(config.Token), httpClient: client}, nil
}

// CreateTask submits an asynchronous task.
func (c *Client) CreateTask(ctx context.Context, input CreateTaskInput) (Task, error) {
	var item Task
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/tasks", input, &item)
	return item, err
}

// ListTasks returns recent tasks.
func (c *Client) ListTasks(ctx context.Context) ([]Task, error) {
	var response struct {
		Tasks []Task `json:"tasks"`
	}
	err := c.doJSON(ctx, http.MethodGet, "/api/v1/tasks", nil, &response)
	return response.Tasks, err
}

// GetTask returns the current durable snapshot.
func (c *Client) GetTask(ctx context.Context, taskID string) (Task, error) {
	var item Task
	err := c.doJSON(ctx, http.MethodGet, taskPath(taskID, ""), nil, &item)
	return item, err
}

// Health checks whether the HTTP process is alive. This public endpoint does
// not require authentication, though the client still sends its token.
func (c *Client) Health(ctx context.Context) (Health, error) {
	var health Health
	err := c.doJSON(ctx, http.MethodGet, "/api/v1/health", nil, &health)
	return health, err
}

// Ready checks whether Core can read its durable store.
func (c *Client) Ready(ctx context.Context) (Health, error) {
	var health Health
	err := c.doJSON(ctx, http.MethodGet, "/api/v1/ready", nil, &health)
	return health, err
}

// Metrics returns the authenticated Prometheus exposition when the server has
// observability.metrics_enabled set. Disabled servers return an APIError 404.
func (c *Client) Metrics(ctx context.Context) (string, error) {
	request, err := c.newRequest(ctx, http.MethodGet, "/metrics", nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "text/plain")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("kern: reading metrics: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", decodeAPIError(response)
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, maxMetricsBody+1)
	encoded, err := io.ReadAll(limited)
	if err != nil {
		return "", fmt.Errorf("kern: reading metrics body: %w", err)
	}
	if len(encoded) > maxMetricsBody {
		return "", errors.New("kern: metrics body exceeds 1 MiB")
	}
	return string(encoded), nil
}

// GetSettings returns the secret-free settings used by executions that start
// after the request.
func (c *Client) GetSettings(ctx context.Context) (RuntimeSettings, error) {
	var settings RuntimeSettings
	err := c.doJSON(ctx, http.MethodGet, "/api/v1/settings", nil, &settings)
	return settings, err
}

// UpdateSettings validates, persists, and applies settings to future task
// executions. Running Attempts retain their original configuration.
func (c *Client) UpdateSettings(
	ctx context.Context,
	input UpdateSettingsInput,
) (RuntimeSettings, error) {
	var settings RuntimeSettings
	err := c.doJSON(ctx, http.MethodPut, "/api/v1/settings", input, &settings)
	return settings, err
}

// PreviewCleanup returns expired terminal-task data without deleting it.
func (c *Client) PreviewCleanup(ctx context.Context) (CleanupPreview, error) {
	var preview CleanupPreview
	err := c.doJSON(ctx, http.MethodGet, "/api/v1/settings/cleanup-preview", nil, &preview)
	return preview, err
}

// CleanupExpired permanently deletes terminal tasks older than the active
// retention window and prunes only artifact objects with no surviving refs.
func (c *Client) CleanupExpired(ctx context.Context) (CleanupResult, error) {
	var result CleanupResult
	err := c.doJSON(
		ctx,
		http.MethodPost,
		"/api/v1/settings/cleanup",
		struct {
			Confirm bool `json:"confirm"`
		}{Confirm: true},
		&result,
	)
	return result, err
}

// ListModelConfigs returns the supported provider protocols and public-safe
// saved model connections.
func (c *Client) ListModelConfigs(ctx context.Context) (ModelConfigs, error) {
	var models ModelConfigs
	err := c.doJSON(ctx, http.MethodGet, "/api/v1/models", nil, &models)
	return models, err
}

// CreateModelConfig saves one public connection. A supplied API key is
// write-only and Core stores only its opaque system-credential reference.
func (c *Client) CreateModelConfig(ctx context.Context, input ModelConfigInput) (ModelConfig, error) {
	var config ModelConfig
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/model-configs", input, &config)
	return config, err
}

// UpdateModelConfig replaces one saved model connection.
func (c *Client) UpdateModelConfig(
	ctx context.Context,
	configID string,
	input ModelConfigInput,
) (ModelConfig, error) {
	var config ModelConfig
	err := c.doJSON(ctx, http.MethodPut, modelConfigPath(configID, ""), input, &config)
	return config, err
}

// DeleteModelConfig removes a saved model connection. Historical task
// snapshots are retained by Core.
func (c *Client) DeleteModelConfig(ctx context.Context, configID string) error {
	return c.doJSON(ctx, http.MethodDelete, modelConfigPath(configID, ""), nil, nil)
}

// TestModelConfig performs a bounded live provider probe without returning
// response content or credentials.
func (c *Client) TestModelConfig(ctx context.Context, configID string) (ModelConfigTestResult, error) {
	var result ModelConfigTestResult
	err := c.doJSON(ctx, http.MethodPost, modelConfigPath(configID, "test"), nil, &result)
	return result, err
}

// Pause pauses a running task at a durable boundary.
func (c *Client) Pause(ctx context.Context, taskID string) (Task, error) {
	return c.taskAction(ctx, taskID, "pause")
}

// Resume creates a new Attempt from a paused task.
func (c *Client) Resume(ctx context.Context, taskID string) (Task, error) {
	return c.taskAction(ctx, taskID, "resume")
}

// Cancel requests cancellation of a task.
func (c *Client) Cancel(ctx context.Context, taskID string) (Task, error) {
	return c.taskAction(ctx, taskID, "cancel")
}

// Retry creates a new Attempt for a terminal task.
func (c *Client) Retry(ctx context.Context, taskID string) (Task, error) {
	return c.taskAction(ctx, taskID, "retry")
}

// SubmitInput continues a paused or terminal task in a new Attempt while
// preserving its prior context.
func (c *Client) SubmitInput(ctx context.Context, taskID, content string) (Task, error) {
	return c.SubmitTaskInput(ctx, taskID, SubmitTaskInput{Content: content})
}

// SubmitTaskInput continues a paused or terminal task with text and optional
// new images while preserving the prior context.
func (c *Client) SubmitTaskInput(
	ctx context.Context,
	taskID string,
	input SubmitTaskInput,
) (Task, error) {
	var item Task
	err := c.doJSON(
		ctx,
		http.MethodPost,
		taskPath(taskID, "messages"),
		input,
		&item,
	)
	return item, err
}

func (c *Client) taskAction(ctx context.Context, taskID, action string) (Task, error) {
	var item Task
	err := c.doJSON(ctx, http.MethodPost, taskPath(taskID, action), nil, &item)
	return item, err
}

// PendingApprovals returns exact-scope requests awaiting a user decision.
func (c *Client) PendingApprovals(ctx context.Context, taskID string) ([]Approval, error) {
	var response struct {
		Approvals []Approval `json:"approvals"`
	}
	err := c.doJSON(ctx, http.MethodGet, taskPath(taskID, "approvals"), nil, &response)
	return response.Approvals, err
}

// DecideApproval approves or denies one request.
func (c *Client) DecideApproval(
	ctx context.Context,
	requestID string,
	decision string,
) (ApprovalReceipt, error) {
	if decision != "approved" && decision != "denied" {
		return ApprovalReceipt{}, errors.New("kern: decision must be approved or denied")
	}
	var receipt ApprovalReceipt
	err := c.doJSON(
		ctx,
		http.MethodPost,
		"/api/v1/approvals/"+url.PathEscape(requestID)+"/decision",
		map[string]string{"decision": decision},
		&receipt,
	)
	return receipt, err
}

// ListArtifacts returns immutable outputs for a task.
func (c *Client) ListArtifacts(ctx context.Context, taskID string) ([]Artifact, error) {
	var response struct {
		Artifacts []Artifact `json:"artifacts"`
	}
	err := c.doJSON(ctx, http.MethodGet, taskPath(taskID, "artifacts"), nil, &response)
	return response.Artifacts, err
}

// DownloadArtifact returns a streaming artifact body. The caller must close it.
func (c *Client) DownloadArtifact(
	ctx context.Context,
	taskID string,
	artifactID string,
) (io.ReadCloser, http.Header, error) {
	endpoint := taskPath(taskID, "artifacts/"+url.PathEscape(artifactID))
	request, err := c.newRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, nil, err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, nil, fmt.Errorf("kern: downloading artifact: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		err := decodeAPIError(response)
		return nil, nil, err
	}
	return response.Body, response.Header.Clone(), nil
}

// ListVerifications returns the active Attempt's verifier records.
func (c *Client) ListVerifications(ctx context.Context, taskID string) ([]Verification, error) {
	var response struct {
		Verifications []Verification `json:"verifications"`
	}
	err := c.doJSON(ctx, http.MethodGet, taskPath(taskID, "verifications"), nil, &response)
	return response.Verifications, err
}

// ListTaskPlugins returns the exact plugin versions used by the active Attempt.
func (c *Client) ListTaskPlugins(ctx context.Context, taskID string) ([]PluginUsage, error) {
	var response struct {
		Plugins []PluginUsage `json:"plugins"`
	}
	err := c.doJSON(ctx, http.MethodGet, taskPath(taskID, "plugins"), nil, &response)
	return response.Plugins, err
}

// GetPlan returns the active Attempt's current plan, or nil for a simple task.
func (c *Client) GetPlan(ctx context.Context, taskID string) (*Plan, error) {
	var response struct {
		Plan *Plan `json:"plan"`
	}
	err := c.doJSON(ctx, http.MethodGet, taskPath(taskID, "plan"), nil, &response)
	return response.Plan, err
}

// ListUncertainOperations returns interrupted side effects that require an
// explicit human conclusion before task continuation.
func (c *Client) ListUncertainOperations(ctx context.Context, taskID string) ([]Operation, error) {
	var response struct {
		Operations []Operation `json:"operations"`
	}
	err := c.doJSON(ctx, http.MethodGet, taskPath(taskID, "operations/uncertain"), nil, &response)
	return response.Operations, err
}

// ResolveUncertainOperation records an immutable human recovery conclusion.
func (c *Client) ResolveUncertainOperation(
	ctx context.Context,
	operationID string,
	resolution OperationResolution,
) (OperationResolutionReceipt, error) {
	if resolution != OperationConfirmedSucceeded && resolution != OperationConfirmedNotExecuted {
		return OperationResolutionReceipt{}, errors.New("kern: invalid operation resolution")
	}
	var receipt OperationResolutionReceipt
	err := c.doJSON(
		ctx,
		http.MethodPost,
		operationPath(operationID, "resolution"),
		struct {
			Resolution OperationResolution `json:"resolution"`
		}{Resolution: resolution},
		&receipt,
	)
	return receipt, err
}

// ListPlugins returns all locally installed plugins.
func (c *Client) ListPlugins(ctx context.Context) ([]Plugin, error) {
	var response struct {
		Plugins []Plugin `json:"plugins"`
	}
	err := c.doJSON(ctx, http.MethodGet, "/api/v1/plugins", nil, &response)
	return response.Plugins, err
}

// GetPlugin returns one installed plugin by its stable reverse-domain ID.
func (c *Client) GetPlugin(ctx context.Context, pluginID string) (Plugin, error) {
	var item Plugin
	err := c.doJSON(ctx, http.MethodGet, pluginPath(pluginID, ""), nil, &item)
	return item, err
}

// InstallPlugin copies and verifies a local plugin package on the Kern server.
func (c *Client) InstallPlugin(ctx context.Context, input InstallPluginInput) (Plugin, error) {
	var item Plugin
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/plugins/install", input, &item)
	return item, err
}

// EnablePlugin re-verifies and enables an installed plugin.
func (c *Client) EnablePlugin(ctx context.Context, pluginID string) (Plugin, error) {
	return c.pluginAction(ctx, pluginID, "enable")
}

// DisablePlugin prevents an installed plugin from being selected for new work.
func (c *Client) DisablePlugin(ctx context.Context, pluginID string) (Plugin, error) {
	return c.pluginAction(ctx, pluginID, "disable")
}

// RemovePlugin removes one installed package.
func (c *Client) RemovePlugin(ctx context.Context, pluginID string) error {
	return c.doJSON(ctx, http.MethodDelete, pluginPath(pluginID, ""), nil, nil)
}

func (c *Client) pluginAction(ctx context.Context, pluginID, action string) (Plugin, error) {
	var item Plugin
	err := c.doJSON(ctx, http.MethodPost, pluginPath(pluginID, action), nil, &item)
	return item, err
}

// StartEvaluation validates a local suite on the server and queues it.
func (c *Client) StartEvaluation(ctx context.Context, input StartEvaluationInput) (EvaluationRun, error) {
	var run EvaluationRun
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/evals/runs", input, &run)
	return run, err
}

// ListEvaluationRuns returns recent durable evaluation snapshots.
func (c *Client) ListEvaluationRuns(ctx context.Context, limit int) ([]EvaluationRun, error) {
	endpoint := "/api/v1/evals/runs"
	if limit > 0 {
		endpoint += "?limit=" + fmt.Sprint(limit)
	}
	var response struct {
		Runs []EvaluationRun `json:"runs"`
	}
	err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &response)
	return response.Runs, err
}

// GetEvaluationRun returns current progress for one evaluation.
func (c *Client) GetEvaluationRun(ctx context.Context, runID string) (EvaluationRun, error) {
	var run EvaluationRun
	err := c.doJSON(ctx, http.MethodGet, evaluationPath(runID, ""), nil, &run)
	return run, err
}

// PauseEvaluation cooperatively stops a queued or running evaluation after
// persisting all terminal Case/Variant jobs.
func (c *Client) PauseEvaluation(ctx context.Context, runID string) (EvaluationRun, error) {
	return c.evaluationAction(ctx, runID, "pause")
}

// ResumeEvaluation continues a paused evaluation without repeating persisted
// Case/Variant jobs.
func (c *Client) ResumeEvaluation(ctx context.Context, runID string) (EvaluationRun, error) {
	return c.evaluationAction(ctx, runID, "resume")
}

// CancelEvaluation requests cancellation of a queued or running evaluation.
func (c *Client) CancelEvaluation(ctx context.Context, runID string) (EvaluationRun, error) {
	return c.evaluationAction(ctx, runID, "cancel")
}

func (c *Client) evaluationAction(ctx context.Context, runID, action string) (EvaluationRun, error) {
	var run EvaluationRun
	err := c.doJSON(ctx, http.MethodPost, evaluationPath(runID, action), nil, &run)
	return run, err
}

// GetEvaluationReport returns the immutable completed report.
func (c *Client) GetEvaluationReport(ctx context.Context, runID string) (EvaluationReport, error) {
	var report EvaluationReport
	err := c.doJSON(ctx, http.MethodGet, evaluationPath(runID, "report"), nil, &report)
	return report, err
}

func (c *Client) doJSON(ctx context.Context, method, endpoint string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("kern: encoding request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := c.newRequest(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("kern: calling API: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return decodeAPIError(response)
	}
	defer response.Body.Close()
	if output == nil || response.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil {
		return fmt.Errorf("kern: decoding response: %w", err)
	}
	return nil
}

func (c *Client) newRequest(
	ctx context.Context,
	method string,
	endpoint string,
	body io.Reader,
) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL.String()+endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("kern: creating request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	return request, nil
}

func decodeAPIError(response *http.Response) error {
	defer response.Body.Close()
	limited, err := io.ReadAll(io.LimitReader(response.Body, maxErrorBody+1))
	if err != nil {
		return fmt.Errorf("kern: reading API error: %w", err)
	}
	message := strings.TrimSpace(string(limited))
	var problem struct {
		Error string `json:"error"`
	}
	if len(limited) <= maxErrorBody && json.Unmarshal(limited, &problem) == nil && problem.Error != "" {
		message = problem.Error
	}
	if len(limited) > maxErrorBody || message == "" {
		message = http.StatusText(response.StatusCode)
	}
	return &APIError{StatusCode: response.StatusCode, Message: message}
}

func taskPath(taskID, suffix string) string {
	path := "/api/v1/tasks/" + url.PathEscape(taskID)
	if suffix != "" {
		path += "/" + suffix
	}
	return path
}

func pluginPath(pluginID, suffix string) string {
	path := "/api/v1/plugins/" + url.PathEscape(pluginID)
	if suffix != "" {
		path += "/" + suffix
	}
	return path
}

func evaluationPath(runID, suffix string) string {
	path := "/api/v1/evals/runs/" + url.PathEscape(runID)
	if suffix != "" {
		path += "/" + suffix
	}
	return path
}

func modelConfigPath(configID, suffix string) string {
	path := "/api/v1/model-configs/" + url.PathEscape(configID)
	if suffix != "" {
		path += "/" + suffix
	}
	return path
}

func operationPath(operationID, suffix string) string {
	path := "/api/v1/operations/" + url.PathEscape(operationID)
	if suffix != "" {
		path += "/" + suffix
	}
	return path
}
