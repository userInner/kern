// Package httpapi exposes Kern over local HTTP and SSE.
package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/userInner/kern/internal/app"
	"github.com/userInner/kern/internal/approval"
	"github.com/userInner/kern/internal/artifact"
	"github.com/userInner/kern/internal/evalservice"
	"github.com/userInner/kern/internal/evaluation"
	"github.com/userInner/kern/internal/modelconfig"
	"github.com/userInner/kern/internal/observability"
	"github.com/userInner/kern/internal/operation"
	"github.com/userInner/kern/internal/plan"
	"github.com/userInner/kern/internal/plugin"
	"github.com/userInner/kern/internal/pluginmanager"
	"github.com/userInner/kern/internal/secret"
	"github.com/userInner/kern/internal/task"
	"github.com/userInner/kern/internal/tracecontext"
)

const (
	maxRequestBytes     = 1 << 20
	maxTaskRequestBytes = 18 << 20
	browserSessionTTL   = 24 * time.Hour
	browserSessionV1    = byte(1)
)

//go:embed web/*
var webAssets embed.FS

type taskReader interface {
	GetTask(ctx context.Context, taskID string) (task.Task, error)
	ListTasks(ctx context.Context, limit int) ([]task.Task, error)
	EventsAfter(ctx context.Context, taskID string, afterID int64, limit int) ([]task.Event, error)
	PendingApprovals(ctx context.Context, taskID string) ([]approval.Request, error)
	ListUncertainOperations(ctx context.Context, taskID string) ([]operation.Operation, error)
	CurrentPlan(ctx context.Context, taskID string) (plan.Plan, error)
	ListVerifications(ctx context.Context, taskID, attemptID string) ([]task.Verification, error)
}

type artifactReader interface {
	List(ctx context.Context, taskID string) ([]artifact.Artifact, error)
	OpenContent(ctx context.Context, taskID, artifactID string) (artifact.Artifact, *os.File, error)
}

type taskSubmitter interface {
	Submit(ctx context.Context, title, goal string) (task.Task, error)
	Pause(ctx context.Context, taskID string) (task.Task, error)
	Cancel(ctx context.Context, taskID string) (task.Task, error)
	Resume(ctx context.Context, taskID string) (task.Task, error)
	Retry(ctx context.Context, taskID string) (task.Task, error)
	SubmitInput(ctx context.Context, taskID, input string) (task.Task, error)
	DecideApproval(
		ctx context.Context,
		requestID string,
		decision approval.Decision,
	) (approval.Receipt, error)
	ResolveUncertainOperation(
		ctx context.Context,
		operationID string,
		resolution operation.Resolution,
	) (operation.ResolutionReceipt, error)
}

type modelConfigStore interface {
	GetModelConfig(ctx context.Context, configID string) (modelconfig.Config, error)
	ListModelConfigs(ctx context.Context) ([]modelconfig.Config, error)
	CreateModelConfig(ctx context.Context, draft modelconfig.Draft) (modelconfig.Config, error)
	UpdateModelConfig(ctx context.Context, configID string, draft modelconfig.Draft) (modelconfig.Config, error)
	DeleteModelConfig(ctx context.Context, configID string) error
}

type credentialModelConfigStore interface {
	CreateModelConfigWithAPIKey(
		ctx context.Context,
		draft modelconfig.Draft,
		apiKey string,
	) (modelconfig.Config, error)
	UpdateModelConfigWithAPIKey(
		ctx context.Context,
		configID string,
		draft modelconfig.Draft,
		apiKey string,
	) (modelconfig.Config, error)
}

type credentialStoreStatus interface {
	CredentialStoreAvailable() bool
}

type modelTaskSubmitter interface {
	SubmitWithModel(ctx context.Context, title, goal, modelConfigID string) (task.Task, error)
}

type pluginTaskSubmitter interface {
	SubmitWithPlugins(
		ctx context.Context,
		title string,
		goal string,
		modelConfigID string,
		enablePlugins []string,
		disablePlugins []string,
	) (task.Task, error)
}

type optionTaskSubmitter interface {
	SubmitWithOptions(ctx context.Context, options app.TaskOptions) (task.Task, error)
}

type multimodalTaskInputSubmitter interface {
	SubmitInputWithAttachments(
		ctx context.Context,
		taskID string,
		input app.TaskInput,
	) (task.Task, error)
}

type pluginUsageReader interface {
	ListAttemptPlugins(ctx context.Context, taskID, attemptID string) ([]plugin.Usage, error)
}

type modelConfigTester interface {
	TestModelConfig(ctx context.Context, configID string) (modelconfig.TestResult, error)
}

type pluginManager interface {
	InstallFromRoot(ctx context.Context, root *os.Root, source string) (plugin.Installed, bool, error)
	Get(ctx context.Context, pluginID string) (plugin.Installed, error)
	List(ctx context.Context) ([]plugin.Installed, error)
	Enable(ctx context.Context, pluginID string) (plugin.Installed, error)
	Disable(ctx context.Context, pluginID string) (plugin.Installed, error)
	Remove(ctx context.Context, pluginID string) error
}

type evaluationManager interface {
	Start(ctx context.Context, input evalservice.StartInput) (evaluation.Run, error)
	Pause(ctx context.Context, runID string) (evaluation.Run, error)
	Resume(ctx context.Context, runID string) (evaluation.Run, error)
	Cancel(ctx context.Context, runID string) (evaluation.Run, error)
	Get(ctx context.Context, runID string) (evaluation.Run, error)
	List(ctx context.Context, limit int) ([]evaluation.Run, error)
	Report(ctx context.Context, runID string) (evaluation.Report, error)
}

type metricsReader interface {
	ObservabilitySnapshot(ctx context.Context) (observability.Snapshot, error)
}

type settingsManager interface {
	CurrentSettings() app.Settings
	UpdateSettings(ctx context.Context, draft app.SettingsDraft) (app.Settings, error)
	PreviewCleanup(ctx context.Context) (app.CleanupPreview, error)
	CleanupExpired(ctx context.Context) (app.CleanupResult, error)
}

// Config contains HTTP transport dependencies.
type Config struct {
	Store          taskReader
	Artifacts      artifactReader
	Submitter      taskSubmitter
	Models         modelConfigStore
	Plugins        pluginManager
	Evals          evaluationManager
	Metrics        metricsReader
	Settings       settingsManager
	MetricsEnabled bool
	Token          string
	Mode           string
	Logger         *slog.Logger
	// PluginImportRoot confines browser-selected plugin sources to one trusted
	// directory opened by the composition root at process startup.
	PluginImportRoot *os.Root
	// Origin is the one browser origin this handler serves. Requests with any
	// other Host are rejected before routing or bootstrap-token issuance.
	Origin string
	// AllowPlainHTTPLoopback explicitly opts into serving the browser over
	// unencrypted HTTP. It is accepted only for a loopback origin; HTTPS must
	// leave it disabled.
	AllowPlainHTTPLoopback bool
}

// Server is Kern's local HTTP handler.
type Server struct {
	store            taskReader
	artifacts        artifactReader
	submitter        taskSubmitter
	models           modelConfigStore
	plugins          pluginManager
	evals            evaluationManager
	metrics          metricsReader
	settings         settingsManager
	metricsEnabled   bool
	token            string
	mode             string
	logger           *slog.Logger
	pluginImportRoot *os.Root
	origin           string
	originScheme     string
	authority        string
	secureTransport  bool
	browserTokenKey  [sha256.Size]byte
	handler          http.Handler
	requestsMu       sync.Mutex
	closing          bool
	inFlight         int
	idle             chan struct{}
	shutdownCtx      context.Context
	shutdownCancel   context.CancelFunc
	shutdownOnce     sync.Once
}

// New constructs the HTTP API and embedded Web handler.
func New(config Config) (*Server, error) {
	if config.Store == nil || config.Artifacts == nil || config.Submitter == nil {
		return nil, errors.New("httpapi: store, artifacts, and submitter are required")
	}
	if config.Token == "" {
		return nil, errors.New("httpapi: session token is required")
	}
	if config.Plugins != nil && config.PluginImportRoot == nil {
		return nil, errors.New("httpapi: plugin import root is required when plugin management is enabled")
	}
	origin, err := newOriginPolicy(config.Origin, config.AllowPlainHTTPLoopback)
	if err != nil {
		return nil, err
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	models := config.Models
	if models == nil {
		models, _ = config.Store.(modelConfigStore)
	}
	metrics := config.Metrics
	if metrics == nil {
		metrics, _ = config.Store.(metricsReader)
	}
	if config.MetricsEnabled && metrics == nil {
		return nil, errors.New("httpapi: metrics reader is required when metrics are enabled")
	}
	settings := config.Settings
	if settings == nil {
		settings, _ = config.Submitter.(settingsManager)
	}
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	server := &Server{
		store:            config.Store,
		artifacts:        config.Artifacts,
		submitter:        config.Submitter,
		models:           models,
		plugins:          config.Plugins,
		evals:            config.Evals,
		metrics:          metrics,
		settings:         settings,
		metricsEnabled:   config.MetricsEnabled,
		token:            config.Token,
		mode:             config.Mode,
		logger:           logger,
		pluginImportRoot: config.PluginImportRoot,
		origin:           origin.value,
		originScheme:     origin.scheme,
		authority:        origin.authority,
		secureTransport:  origin.secureTransport,
		browserTokenKey:  sha256.Sum256([]byte("kern/browser-session/v1\x00" + config.Token)),
		idle:             closedSignal(),
		shutdownCtx:      shutdownCtx,
		shutdownCancel:   shutdownCancel,
	}
	server.handler = server.routes()
	return server, nil
}

type originPolicy struct {
	value           string
	scheme          string
	authority       string
	secureTransport bool
}

func newOriginPolicy(raw string, allowPlainHTTPLoopback bool) (originPolicy, error) {
	value := strings.TrimSpace(raw)
	parsed, err := url.Parse(value)
	if err != nil {
		return originPolicy{}, fmt.Errorf("httpapi: parsing browser origin: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return originPolicy{}, errors.New("httpapi: browser origin must use http or https")
	}
	if parsed.User != nil || parsed.Opaque != "" || parsed.Host == "" ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawPath != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return originPolicy{}, errors.New("httpapi: browser origin must contain only scheme and authority")
	}
	authority, hostname, err := canonicalAuthority(scheme, parsed.Host)
	if err != nil {
		return originPolicy{}, err
	}
	loopback := strings.EqualFold(hostname, "localhost")
	if address := net.ParseIP(hostname); address != nil {
		loopback = address.IsLoopback()
	}
	if !loopback {
		return originPolicy{}, errors.New("httpapi: browser origin must be loopback")
	}
	if scheme == "http" {
		if !allowPlainHTTPLoopback {
			return originPolicy{}, errors.New("httpapi: plain HTTP requires an explicit loopback transport exception")
		}
	} else if allowPlainHTTPLoopback {
		return originPolicy{}, errors.New("httpapi: insecure loopback transport exception is invalid for HTTPS")
	}
	return originPolicy{
		value:           scheme + "://" + authority,
		scheme:          scheme,
		authority:       authority,
		secureTransport: scheme == "https",
	}, nil
}

func canonicalAuthority(scheme, raw string) (string, string, error) {
	parsed, err := url.Parse(scheme + "://" + raw)
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.Path != "" ||
		parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", errors.New("httpapi: browser origin authority is invalid")
	}
	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "" || strings.HasSuffix(hostname, ".") {
		return "", "", errors.New("httpapi: browser origin host is invalid")
	}
	port := parsed.Port()
	if port != "" {
		portNumber, parseErr := strconv.Atoi(port)
		if parseErr != nil || portNumber < 1 || portNumber > 65535 || strconv.Itoa(portNumber) != port {
			return "", "", errors.New("httpapi: browser origin port is invalid")
		}
	}
	inputAuthority := formatAuthority(hostname, port)
	if !strings.EqualFold(parsed.Host, inputAuthority) {
		return "", "", errors.New("httpapi: browser origin authority is not canonical")
	}
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	authority := formatAuthority(hostname, port)
	return authority, hostname, nil
}

func formatAuthority(hostname, port string) string {
	if port != "" {
		return net.JoinHostPort(hostname, port)
	}
	if strings.Contains(hostname, ":") {
		return "[" + hostname + "]"
	}
	return hostname
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.beginRequest() {
		writeProblem(w, http.StatusServiceUnavailable, "server is shutting down")
		return
	}
	defer s.endRequest()
	requestCtx, cancel := context.WithCancel(r.Context())
	stopShutdownCancellation := context.AfterFunc(s.shutdownCtx, cancel)
	defer stopShutdownCancellation()
	defer cancel()
	s.handler.ServeHTTP(w, r.WithContext(requestCtx))
}

// BeginShutdown prevents new requests from entering the handler and cancels
// every admitted request context before the owning http.Server drains them.
func (s *Server) BeginShutdown() {
	s.requestsMu.Lock()
	s.closing = true
	s.requestsMu.Unlock()
	s.shutdownOnce.Do(s.shutdownCancel)
}

// WaitForIdle waits until every request admitted before shutdown has left the
// handler. Call BeginShutdown before WaitForIdle to prevent new admissions.
func (s *Server) WaitForIdle(ctx context.Context) error {
	s.requestsMu.Lock()
	idle := s.idle
	s.requestsMu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) beginRequest() bool {
	s.requestsMu.Lock()
	defer s.requestsMu.Unlock()
	if s.closing {
		return false
	}
	if s.inFlight == 0 {
		s.idle = make(chan struct{})
	}
	s.inFlight++
	return true
}

func (s *Server) endRequest() {
	s.requestsMu.Lock()
	defer s.requestsMu.Unlock()
	s.inFlight--
	if s.inFlight == 0 {
		close(s.idle)
	}
}

func closedSignal() chan struct{} {
	closed := make(chan struct{})
	close(closed)
	return closed
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	for _, route := range s.apiRoutes() {
		mux.Handle(route.method+" "+route.path, route.handler)
	}
	mux.Handle("/", s.handleWeb())
	return s.securityHeaders(s.requireConfiguredHost(s.requestTrace(s.requestLog(mux))))
}

type apiRoute struct {
	method  string
	path    string
	handler http.Handler
}

// apiRoutes is the authoritative HTTP method/path inventory. Keeping routing
// data separate from registration lets the OpenAPI contract test prove that
// every public endpoint is documented and every documented endpoint exists.
func (s *Server) apiRoutes() []apiRoute {
	public := func(method, path string, handler http.HandlerFunc) apiRoute {
		return apiRoute{method: method, path: path, handler: handler}
	}
	authenticated := func(method, path string, handler http.HandlerFunc) apiRoute {
		var protected http.Handler = handler
		if method != http.MethodGet && method != http.MethodHead {
			protected = s.requireMutationOrigin(protected)
		}
		return apiRoute{method: method, path: path, handler: s.authenticate(protected)}
	}
	return []apiRoute{
		public(http.MethodGet, "/api/v1/health", s.handleHealth),
		public(http.MethodGet, "/api/v1/ready", s.handleReady),
		authenticated(http.MethodGet, "/metrics", s.handleMetrics),
		authenticated(http.MethodGet, "/api/v1/tasks", s.handleListTasks),
		authenticated(http.MethodGet, "/api/v1/settings", s.handleGetSettings),
		authenticated(http.MethodPut, "/api/v1/settings", s.handleUpdateSettings),
		authenticated(http.MethodGet, "/api/v1/settings/cleanup-preview", s.handleCleanupPreview),
		authenticated(http.MethodPost, "/api/v1/settings/cleanup", s.handleCleanup),
		authenticated(http.MethodPost, "/api/v1/tasks", s.handleCreateTask),
		authenticated(http.MethodGet, "/api/v1/models", s.handleListModelConfigs),
		authenticated(http.MethodPost, "/api/v1/model-configs", s.handleCreateModelConfig),
		authenticated(http.MethodPut, "/api/v1/model-configs/{configID}", s.handleUpdateModelConfig),
		authenticated(http.MethodDelete, "/api/v1/model-configs/{configID}", s.handleDeleteModelConfig),
		authenticated(http.MethodPost, "/api/v1/model-configs/{configID}/test", s.handleTestModelConfig),
		authenticated(http.MethodGet, "/api/v1/plugins", s.handleListPlugins),
		authenticated(http.MethodPost, "/api/v1/plugins/install", s.handleInstallPlugin),
		authenticated(http.MethodGet, "/api/v1/plugins/{pluginID}", s.handleGetPlugin),
		authenticated(http.MethodPost, "/api/v1/plugins/{pluginID}/enable", s.handleEnablePlugin),
		authenticated(http.MethodPost, "/api/v1/plugins/{pluginID}/disable", s.handleDisablePlugin),
		authenticated(http.MethodDelete, "/api/v1/plugins/{pluginID}", s.handleRemovePlugin),
		authenticated(http.MethodGet, "/api/v1/evals/runs", s.handleListEvalRuns),
		authenticated(http.MethodPost, "/api/v1/evals/runs", s.handleStartEvalRun),
		authenticated(http.MethodGet, "/api/v1/evals/runs/{runID}", s.handleGetEvalRun),
		authenticated(http.MethodGet, "/api/v1/evals/runs/{runID}/report", s.handleEvalReport),
		authenticated(http.MethodPost, "/api/v1/evals/runs/{runID}/pause", s.handlePauseEvalRun),
		authenticated(http.MethodPost, "/api/v1/evals/runs/{runID}/resume", s.handleResumeEvalRun),
		authenticated(http.MethodPost, "/api/v1/evals/runs/{runID}/cancel", s.handleCancelEvalRun),
		authenticated(http.MethodGet, "/api/v1/tasks/{taskID}", s.handleGetTask),
		authenticated(http.MethodGet, "/api/v1/tasks/{taskID}/events", s.handleEvents),
		authenticated(http.MethodPost, "/api/v1/tasks/{taskID}/messages", s.handleTaskInput),
		authenticated(http.MethodGet, "/api/v1/tasks/{taskID}/approvals", s.handlePendingApprovals),
		authenticated(http.MethodGet, "/api/v1/tasks/{taskID}/plan", s.handleCurrentPlan),
		authenticated(http.MethodGet, "/api/v1/tasks/{taskID}/verifications", s.handleVerifications),
		authenticated(http.MethodGet, "/api/v1/tasks/{taskID}/plugins", s.handleTaskPlugins),
		authenticated(http.MethodGet, "/api/v1/tasks/{taskID}/operations/uncertain", s.handleUncertainOperations),
		authenticated(http.MethodGet, "/api/v1/tasks/{taskID}/artifacts", s.handleArtifacts),
		authenticated(http.MethodGet, "/api/v1/tasks/{taskID}/artifacts/{artifactID}", s.handleArtifactContent),
		authenticated(http.MethodPost, "/api/v1/tasks/{taskID}/pause", s.handlePauseTask),
		authenticated(http.MethodPost, "/api/v1/tasks/{taskID}/cancel", s.handleCancelTask),
		authenticated(http.MethodPost, "/api/v1/tasks/{taskID}/resume", s.handleResumeTask),
		authenticated(http.MethodPost, "/api/v1/tasks/{taskID}/retry", s.handleRetryTask),
		authenticated(http.MethodPost, "/api/v1/approvals/{requestID}/decision", s.handleApprovalDecision),
		authenticated(http.MethodPost, "/api/v1/operations/{operationID}/resolution", s.handleOperationResolution),
	}
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeProblem(w, http.StatusNotImplemented, "runtime settings are unavailable")
		return
	}
	writeJSON(w, http.StatusOK, s.settings.CurrentSettings())
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeProblem(w, http.StatusNotImplemented, "runtime settings are unavailable")
		return
	}
	var draft app.SettingsDraft
	if err := decodeJSONBody(w, r, &draft); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid request body")
		return
	}
	updated, err := s.settings.UpdateSettings(r.Context(), draft)
	if err != nil {
		if strings.HasPrefix(err.Error(), "configuration:") || strings.HasPrefix(err.Error(), "app:") ||
			strings.HasPrefix(err.Error(), "policy:") || strings.HasPrefix(err.Error(), "agent:") {
			writeProblem(w, http.StatusBadRequest, err.Error())
			return
		}
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleCleanupPreview(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeProblem(w, http.StatusNotImplemented, "runtime settings are unavailable")
		return
	}
	preview, err := s.settings.PreviewCleanup(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (s *Server) handleCleanup(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeProblem(w, http.StatusNotImplemented, "runtime settings are unavailable")
		return
	}
	var input struct {
		Confirm bool `json:"confirm"`
	}
	if err := decodeJSONBody(w, r, &input); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !input.Confirm {
		writeProblem(w, http.StatusBadRequest, "cleanup requires explicit confirmation")
		return
	}
	result, err := s.settings.CleanupExpired(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleListEvalRuns(w http.ResponseWriter, r *http.Request) {
	if s.evals == nil {
		writeProblem(w, http.StatusNotImplemented, "evaluation service is unavailable")
		return
	}
	limit := 50
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 200 {
			writeProblem(w, http.StatusBadRequest, "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	items, err := s.evals.List(r.Context(), limit)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": evaluation.SchemaVersion,
		"runs":           items,
	})
}

func (s *Server) handleStartEvalRun(w http.ResponseWriter, r *http.Request) {
	if !s.validMutationOrigin(r) {
		writeProblem(w, http.StatusForbidden, "request origin is not allowed")
		return
	}
	if s.evals == nil {
		writeProblem(w, http.StatusNotImplemented, "evaluation service is unavailable")
		return
	}
	body := http.MaxBytesReader(w, r.Body, maxRequestBytes)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var input evalservice.StartInput
	if err := decoder.Decode(&input); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeProblem(w, http.StatusBadRequest, "invalid request body")
		return
	}
	item, err := s.evals.Start(r.Context(), input)
	if err != nil {
		if errors.Is(err, evaluation.ErrInvalidSuite) || errors.Is(err, os.ErrNotExist) ||
			strings.HasPrefix(err.Error(), "evalservice:") || strings.HasPrefix(err.Error(), "evaluation:") {
			writeProblem(w, http.StatusBadRequest, err.Error())
			return
		}
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, item)
}

func (s *Server) handleGetEvalRun(w http.ResponseWriter, r *http.Request) {
	if s.evals == nil {
		writeProblem(w, http.StatusNotImplemented, "evaluation service is unavailable")
		return
	}
	item, err := s.evals.Get(r.Context(), r.PathValue("runID"))
	if errors.Is(err, evaluation.ErrRunNotFound) {
		writeProblem(w, http.StatusNotFound, "evaluation run not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) handleEvalReport(w http.ResponseWriter, r *http.Request) {
	if s.evals == nil {
		writeProblem(w, http.StatusNotImplemented, "evaluation service is unavailable")
		return
	}
	report, err := s.evals.Report(r.Context(), r.PathValue("runID"))
	switch {
	case errors.Is(err, evaluation.ErrRunNotFound):
		writeProblem(w, http.StatusNotFound, "evaluation run not found")
	case errors.Is(err, evaluation.ErrReportNotReady):
		writeProblem(w, http.StatusConflict, "evaluation report is not ready")
	case err != nil:
		s.internalError(w, r, err)
	default:
		w.Header().Set("Content-Disposition", `attachment; filename="kern-eval-`+report.RunID+`.json"`)
		writeJSON(w, http.StatusOK, report)
	}
}

func (s *Server) handleCancelEvalRun(w http.ResponseWriter, r *http.Request) {
	s.handleEvalControl(w, r, "cancel")
}

func (s *Server) handlePauseEvalRun(w http.ResponseWriter, r *http.Request) {
	s.handleEvalControl(w, r, "pause")
}

func (s *Server) handleResumeEvalRun(w http.ResponseWriter, r *http.Request) {
	s.handleEvalControl(w, r, "resume")
}

func (s *Server) handleEvalControl(
	w http.ResponseWriter,
	r *http.Request,
	action string,
) {
	if !s.validMutationOrigin(r) {
		writeProblem(w, http.StatusForbidden, "request origin is not allowed")
		return
	}
	if s.evals == nil {
		writeProblem(w, http.StatusNotImplemented, "evaluation service is unavailable")
		return
	}
	var item evaluation.Run
	var err error
	switch action {
	case "pause":
		item, err = s.evals.Pause(r.Context(), r.PathValue("runID"))
	case "resume":
		item, err = s.evals.Resume(r.Context(), r.PathValue("runID"))
	default:
		item, err = s.evals.Cancel(r.Context(), r.PathValue("runID"))
	}
	if errors.Is(err, evaluation.ErrRunNotFound) {
		writeProblem(w, http.StatusNotFound, "evaluation run not found")
		return
	}
	if err != nil && strings.HasPrefix(err.Error(), "evalservice:") {
		writeProblem(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, item)
}

func (s *Server) handleArtifacts(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskID")
	if _, err := s.store.GetTask(r.Context(), taskID); err != nil {
		if errors.Is(err, task.ErrNotFound) {
			writeProblem(w, http.StatusNotFound, "task not found")
			return
		}
		s.internalError(w, r, err)
		return
	}
	items, err := s.artifacts.List(r.Context(), taskID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": task.SchemaVersion,
		"artifacts":      items,
	})
}

func (s *Server) handleArtifactContent(w http.ResponseWriter, r *http.Request) {
	item, file, err := s.artifacts.OpenContent(
		r.Context(),
		r.PathValue("taskID"),
		r.PathValue("artifactID"),
	)
	if errors.Is(err, artifact.ErrNotFound) || errors.Is(err, artifact.ErrForbidden) {
		writeProblem(w, http.StatusNotFound, "artifact not found")
		return
	}
	if errors.Is(err, artifact.ErrCorrupt) {
		writeProblem(w, http.StatusConflict, "artifact integrity check failed")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer file.Close()
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": item.Name})
	w.Header().Set("Content-Type", item.MediaType)
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("ETag", `"sha256:`+item.Digest+`"`)
	http.ServeContent(w, r, item.Name, item.CreatedAt, file)
}

func (s *Server) handlePauseTask(w http.ResponseWriter, r *http.Request) {
	s.handleTaskAction(w, r, http.StatusOK, s.submitter.Pause)
}

func (s *Server) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	s.handleTaskAction(w, r, http.StatusOK, s.submitter.Cancel)
}

func (s *Server) handleResumeTask(w http.ResponseWriter, r *http.Request) {
	s.handleTaskAction(w, r, http.StatusAccepted, s.submitter.Resume)
}

func (s *Server) handleRetryTask(w http.ResponseWriter, r *http.Request) {
	s.handleTaskAction(w, r, http.StatusAccepted, s.submitter.Retry)
}

func (s *Server) handleTaskInput(w http.ResponseWriter, r *http.Request) {
	if !s.validMutationOrigin(r) {
		writeProblem(w, http.StatusForbidden, "request origin is not allowed")
		return
	}
	body := http.MaxBytesReader(w, r.Body, maxTaskRequestBytes)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var input struct {
		Content     string               `json:"content"`
		Attachments []app.TaskAttachment `json:"attachments,omitempty"`
	}
	if err := decoder.Decode(&input); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid request body")
		return
	}
	input.Content = strings.TrimSpace(input.Content)
	if input.Content == "" || len([]rune(input.Content)) > 20_000 {
		writeProblem(w, http.StatusBadRequest, "content must contain 1 to 20000 characters")
		return
	}
	var item task.Task
	var err error
	if len(input.Attachments) > 0 {
		if submitter, ok := s.submitter.(multimodalTaskInputSubmitter); ok {
			item, err = submitter.SubmitInputWithAttachments(
				r.Context(),
				r.PathValue("taskID"),
				app.TaskInput{Content: input.Content, Attachments: input.Attachments},
			)
		} else {
			writeProblem(w, http.StatusNotImplemented, "task input attachments are unavailable")
			return
		}
	} else {
		item, err = s.submitter.SubmitInput(r.Context(), r.PathValue("taskID"), input.Content)
	}
	switch {
	case errors.Is(err, task.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "task not found")
	case errors.Is(err, task.ErrInvalidTransition), errors.Is(err, operation.ErrUnresolvedEffects):
		writeProblem(w, http.StatusConflict, "task cannot accept input in its current state")
	case errors.Is(err, app.ErrInvalidTaskAttachment):
		writeProblem(w, http.StatusBadRequest, err.Error())
	case err != nil:
		s.internalError(w, r, err)
	default:
		writeJSON(w, http.StatusAccepted, item)
	}
}

func (s *Server) handlePendingApprovals(w http.ResponseWriter, r *http.Request) {
	requests, err := s.store.PendingApprovals(r.Context(), r.PathValue("taskID"))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": task.SchemaVersion,
		"approvals":      requests,
	})
}

func (s *Server) handleCurrentPlan(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskID")
	if _, err := s.store.GetTask(r.Context(), taskID); err != nil {
		if errors.Is(err, task.ErrNotFound) {
			writeProblem(w, http.StatusNotFound, "task not found")
			return
		}
		s.internalError(w, r, err)
		return
	}
	current, err := s.store.CurrentPlan(r.Context(), taskID)
	if errors.Is(err, plan.ErrNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{
			"schema_version": task.SchemaVersion,
			"plan":           nil,
		})
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": task.SchemaVersion,
		"plan":           current,
	})
}

func (s *Server) handleVerifications(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskID")
	current, err := s.store.GetTask(r.Context(), taskID)
	if errors.Is(err, task.ErrNotFound) {
		writeProblem(w, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items, err := s.store.ListVerifications(r.Context(), taskID, current.ActiveAttemptID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": task.SchemaVersion,
		"attempt_id":     current.ActiveAttemptID,
		"verifications":  items,
	})
}

func (s *Server) handleTaskPlugins(w http.ResponseWriter, r *http.Request) {
	reader, ok := s.store.(pluginUsageReader)
	if !ok {
		writeProblem(w, http.StatusNotImplemented, "task plugin audit is unavailable")
		return
	}
	taskID := r.PathValue("taskID")
	current, err := s.store.GetTask(r.Context(), taskID)
	if errors.Is(err, task.ErrNotFound) {
		writeProblem(w, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items, err := reader.ListAttemptPlugins(r.Context(), taskID, current.ActiveAttemptID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": plugin.SchemaVersion,
		"attempt_id":     current.ActiveAttemptID,
		"plugins":        items,
	})
}

func (s *Server) handleUncertainOperations(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskID")
	if _, err := s.store.GetTask(r.Context(), taskID); err != nil {
		if errors.Is(err, task.ErrNotFound) {
			writeProblem(w, http.StatusNotFound, "task not found")
			return
		}
		s.internalError(w, r, err)
		return
	}
	items, err := s.store.ListUncertainOperations(r.Context(), taskID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": task.SchemaVersion,
		"operations":     items,
	})
}

func (s *Server) handleApprovalDecision(w http.ResponseWriter, r *http.Request) {
	if !s.validMutationOrigin(r) {
		writeProblem(w, http.StatusForbidden, "request origin is not allowed")
		return
	}
	body := http.MaxBytesReader(w, r.Body, maxRequestBytes)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var input struct {
		Decision approval.Decision `json:"decision"`
	}
	if err := decoder.Decode(&input); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if input.Decision != approval.DecisionApproved && input.Decision != approval.DecisionDenied {
		writeProblem(w, http.StatusBadRequest, "decision must be approved or denied")
		return
	}
	receipt, err := s.submitter.DecideApproval(
		r.Context(),
		r.PathValue("requestID"),
		input.Decision,
	)
	if errors.Is(err, approval.ErrNotFound) {
		writeProblem(w, http.StatusNotFound, "approval request not found")
		return
	}
	if errors.Is(err, approval.ErrAlreadyDecided) || errors.Is(err, approval.ErrExpired) {
		writeProblem(w, http.StatusConflict, "approval request is no longer pending")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, receipt)
}

func (s *Server) handleOperationResolution(w http.ResponseWriter, r *http.Request) {
	if !s.validMutationOrigin(r) {
		writeProblem(w, http.StatusForbidden, "request origin is not allowed")
		return
	}
	body := http.MaxBytesReader(w, r.Body, maxRequestBytes)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var input struct {
		Resolution operation.Resolution `json:"resolution"`
	}
	if err := decoder.Decode(&input); err != nil || !input.Resolution.Valid() {
		writeProblem(w, http.StatusBadRequest, "resolution must be confirmed_succeeded or confirmed_not_executed")
		return
	}
	receipt, err := s.submitter.ResolveUncertainOperation(
		r.Context(),
		r.PathValue("operationID"),
		input.Resolution,
	)
	if errors.Is(err, operation.ErrNotFound) {
		writeProblem(w, http.StatusNotFound, "operation not found")
		return
	}
	if errors.Is(err, operation.ErrAlreadyResolved) {
		writeProblem(w, http.StatusConflict, "operation is no longer uncertain")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, receipt)
}

func (s *Server) handleTaskAction(
	w http.ResponseWriter,
	r *http.Request,
	status int,
	action func(context.Context, string) (task.Task, error),
) {
	if !s.validMutationOrigin(r) {
		writeProblem(w, http.StatusForbidden, "request origin is not allowed")
		return
	}
	item, err := action(r.Context(), r.PathValue("taskID"))
	if errors.Is(err, task.ErrNotFound) {
		writeProblem(w, http.StatusNotFound, "task not found")
		return
	}
	if errors.Is(err, task.ErrInvalidTransition) {
		writeProblem(w, http.StatusConflict, "task action is not valid in its current state")
		return
	}
	if errors.Is(err, operation.ErrUnresolvedEffects) {
		writeProblem(w, http.StatusConflict, "resolve every uncertain operation before resuming the task")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, status, item)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": task.SchemaVersion,
		"status":         "ok",
		"mode":           s.mode,
	})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if _, err := s.store.ListTasks(r.Context(), 1); err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "runtime is not ready")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": task.SchemaVersion,
		"status":         "ready",
		"mode":           s.mode,
	})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.metricsEnabled {
		writeProblem(w, http.StatusNotFound, "metrics are disabled")
		return
	}
	snapshot, err := s.metrics.ObservabilitySnapshot(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var encoded bytes.Buffer
	if err := observability.WritePrometheus(&encoded, snapshot); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded.Bytes())
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListTasks(r.Context(), 50)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": task.SchemaVersion,
		"tasks":          items,
	})
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	if !s.validMutationOrigin(r) {
		writeProblem(w, http.StatusForbidden, "request origin is not allowed")
		return
	}
	body := http.MaxBytesReader(w, r.Body, maxTaskRequestBytes)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var input struct {
		Title         string `json:"title"`
		Goal          string `json:"goal"`
		ModelConfigID string `json:"model_config_id,omitempty"`
		Plugins       struct {
			Enable  []string `json:"enable,omitempty"`
			Disable []string `json:"disable,omitempty"`
		} `json:"plugins,omitempty"`
		Attachments []app.TaskAttachment `json:"attachments,omitempty"`
	}
	if err := decoder.Decode(&input); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid request body")
		return
	}
	input.Goal = strings.TrimSpace(input.Goal)
	if input.Goal == "" {
		writeProblem(w, http.StatusBadRequest, "goal is required")
		return
	}
	if len([]rune(input.Goal)) > 20_000 {
		writeProblem(w, http.StatusBadRequest, "goal is too long")
		return
	}
	input.Title = strings.TrimSpace(input.Title)
	if input.Title == "" {
		input.Title = taskTitle(input.Goal)
	}
	input.ModelConfigID = strings.TrimSpace(input.ModelConfigID)
	var created task.Task
	var err error
	if len(input.Attachments) > 0 {
		if submitter, ok := s.submitter.(optionTaskSubmitter); ok {
			created, err = submitter.SubmitWithOptions(r.Context(), app.TaskOptions{
				Title:          input.Title,
				Goal:           input.Goal,
				ModelConfigID:  input.ModelConfigID,
				EnablePlugins:  input.Plugins.Enable,
				DisablePlugins: input.Plugins.Disable,
				Attachments:    input.Attachments,
			})
		} else {
			writeProblem(w, http.StatusNotImplemented, "task attachments are unavailable")
			return
		}
	} else if len(input.Plugins.Enable) > 0 || len(input.Plugins.Disable) > 0 {
		if submitter, ok := s.submitter.(pluginTaskSubmitter); ok {
			created, err = submitter.SubmitWithPlugins(
				r.Context(),
				input.Title,
				input.Goal,
				input.ModelConfigID,
				input.Plugins.Enable,
				input.Plugins.Disable,
			)
		} else {
			writeProblem(w, http.StatusNotImplemented, "task plugin selection is unavailable")
			return
		}
	} else if input.ModelConfigID == "" {
		created, err = s.submitter.Submit(r.Context(), input.Title, input.Goal)
	} else if submitter, ok := s.submitter.(modelTaskSubmitter); ok {
		created, err = submitter.SubmitWithModel(
			r.Context(),
			input.Title,
			input.Goal,
			input.ModelConfigID,
		)
	} else {
		writeProblem(w, http.StatusNotImplemented, "task model selection is unavailable")
		return
	}
	if err != nil {
		if errors.Is(err, app.ErrInvalidTaskAttachment) {
			writeProblem(w, http.StatusBadRequest, err.Error())
			return
		}
		if errors.Is(err, plugin.ErrNotFound) {
			writeProblem(w, http.StatusConflict, "selected plugin is unavailable")
			return
		}
		if errors.Is(err, plugin.ErrInvalidManifest) {
			writeProblem(w, http.StatusBadRequest, "plugin selection is invalid")
			return
		}
		if errors.Is(err, plugin.ErrConflict) {
			writeProblem(w, http.StatusConflict, "plugin selection contains conflicting choices")
			return
		}
		if errors.Is(err, modelconfig.ErrNotFound) || errors.Is(err, modelconfig.ErrDisabled) {
			writeProblem(w, http.StatusConflict, "selected model config is unavailable")
			return
		}
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, created)
}

type modelConfigInput struct {
	Name        string               `json:"name"`
	Provider    modelconfig.Provider `json:"provider"`
	BaseURL     string               `json:"base_url"`
	Model       string               `json:"model"`
	APIKeyEnv   string               `json:"api_key_env,omitempty"`
	APIKey      *string              `json:"api_key,omitempty"`
	ClearAPIKey bool                 `json:"clear_api_key,omitempty"`
	Enabled     *bool                `json:"enabled,omitempty"`
	SetDefault  bool                 `json:"set_default,omitempty"`
}

func (s *Server) handleListModelConfigs(w http.ResponseWriter, r *http.Request) {
	if s.models == nil {
		writeProblem(w, http.StatusNotImplemented, "model settings are unavailable")
		return
	}
	configs, err := s.models.ListModelConfigs(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version":             modelconfig.SchemaVersion,
		"credential_store_available": credentialStoreAvailable(s.models),
		"providers": []modelconfig.Provider{
			modelconfig.ProviderOpenAICompatible,
			modelconfig.ProviderOllama,
		},
		"configs": configs,
	})
}

func credentialStoreAvailable(models modelConfigStore) bool {
	status, ok := models.(credentialStoreStatus)
	return ok && status.CredentialStoreAvailable()
}

func (s *Server) handleCreateModelConfig(w http.ResponseWriter, r *http.Request) {
	if !s.validMutationOrigin(r) {
		writeProblem(w, http.StatusForbidden, "request origin is not allowed")
		return
	}
	if s.models == nil {
		writeProblem(w, http.StatusNotImplemented, "model settings are unavailable")
		return
	}
	input, ok := decodeModelConfigInput(w, r)
	if !ok {
		return
	}
	if err := validateModelCredentialInput(input, true); err != nil {
		writeProblem(w, http.StatusBadRequest, err.Error())
		return
	}
	draft, err := modelConfigDraft(input, "", true)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, err.Error())
		return
	}
	var created modelconfig.Config
	if input.APIKey == nil {
		created, err = s.models.CreateModelConfig(r.Context(), draft)
	} else if credentialStore, ok := s.models.(credentialModelConfigStore); ok {
		created, err = credentialStore.CreateModelConfigWithAPIKey(r.Context(), draft, *input.APIKey)
	} else {
		err = app.ErrCredentialStoreUnavailable
	}
	if err != nil {
		s.writeModelConfigError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleUpdateModelConfig(w http.ResponseWriter, r *http.Request) {
	if !s.validMutationOrigin(r) {
		writeProblem(w, http.StatusForbidden, "request origin is not allowed")
		return
	}
	if s.models == nil {
		writeProblem(w, http.StatusNotImplemented, "model settings are unavailable")
		return
	}
	current, err := s.models.GetModelConfig(r.Context(), r.PathValue("configID"))
	if err != nil {
		s.writeModelConfigError(w, r, err)
		return
	}
	input, ok := decodeModelConfigInput(w, r)
	if !ok {
		return
	}
	if err := validateModelCredentialInput(input, false); err != nil {
		writeProblem(w, http.StatusBadRequest, err.Error())
		return
	}
	secretRef := current.SecretRef
	if input.ClearAPIKey {
		secretRef = ""
	}
	draft, err := modelConfigDraft(input, secretRef, current.Enabled)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, err.Error())
		return
	}
	var updated modelconfig.Config
	if input.APIKey == nil {
		updated, err = s.models.UpdateModelConfig(r.Context(), current.ID, draft)
	} else if credentialStore, ok := s.models.(credentialModelConfigStore); ok {
		updated, err = credentialStore.UpdateModelConfigWithAPIKey(
			r.Context(), current.ID, draft, *input.APIKey,
		)
	} else {
		err = app.ErrCredentialStoreUnavailable
	}
	if err != nil {
		s.writeModelConfigError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteModelConfig(w http.ResponseWriter, r *http.Request) {
	if !s.validMutationOrigin(r) {
		writeProblem(w, http.StatusForbidden, "request origin is not allowed")
		return
	}
	if s.models == nil {
		writeProblem(w, http.StatusNotImplemented, "model settings are unavailable")
		return
	}
	if err := s.models.DeleteModelConfig(r.Context(), r.PathValue("configID")); err != nil {
		s.writeModelConfigError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTestModelConfig(w http.ResponseWriter, r *http.Request) {
	if !s.validMutationOrigin(r) {
		writeProblem(w, http.StatusForbidden, "request origin is not allowed")
		return
	}
	tester, ok := s.submitter.(modelConfigTester)
	if !ok {
		writeProblem(w, http.StatusNotImplemented, "model connection testing is unavailable")
		return
	}
	result, err := tester.TestModelConfig(r.Context(), r.PathValue("configID"))
	if err != nil {
		if errors.Is(err, modelconfig.ErrNotFound) {
			writeProblem(w, http.StatusNotFound, "model config not found")
			return
		}
		if errors.Is(err, modelconfig.ErrDisabled) {
			writeProblem(w, http.StatusConflict, "model config is disabled")
			return
		}
		writeProblem(w, http.StatusBadGateway, "model connection test failed")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleListPlugins(w http.ResponseWriter, r *http.Request) {
	if s.plugins == nil {
		writeProblem(w, http.StatusNotImplemented, "plugin management is unavailable")
		return
	}
	items, err := s.plugins.List(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": plugin.SchemaVersion,
		"plugins":        items,
	})
}

func (s *Server) handleGetPlugin(w http.ResponseWriter, r *http.Request) {
	if s.plugins == nil {
		writeProblem(w, http.StatusNotImplemented, "plugin management is unavailable")
		return
	}
	item, err := s.plugins.Get(r.Context(), r.PathValue("pluginID"))
	if err != nil {
		s.writePluginError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) handleInstallPlugin(w http.ResponseWriter, r *http.Request) {
	if !s.validMutationOrigin(r) {
		writeProblem(w, http.StatusForbidden, "request origin is not allowed")
		return
	}
	if s.plugins == nil {
		writeProblem(w, http.StatusNotImplemented, "plugin management is unavailable")
		return
	}
	body := http.MaxBytesReader(w, r.Body, maxRequestBytes)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var input struct {
		Source string `json:"source"`
		Enable bool   `json:"enable,omitempty"`
	}
	if err := decoder.Decode(&input); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid request body")
		return
	}
	input.Source = strings.TrimSpace(input.Source)
	if input.Source == "" || len(input.Source) > 4_096 {
		writeProblem(w, http.StatusBadRequest, "source must be a workspace-relative plugin directory")
		return
	}
	item, created, err := s.plugins.InstallFromRoot(r.Context(), s.pluginImportRoot, input.Source)
	if err != nil {
		s.writePluginError(w, r, err)
		return
	}
	if input.Enable && !item.Enabled {
		item, err = s.plugins.Enable(r.Context(), item.ID)
		if err != nil {
			s.writePluginError(w, r, err)
			return
		}
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, item)
}

func (s *Server) handleEnablePlugin(w http.ResponseWriter, r *http.Request) {
	s.handlePluginState(w, r, true)
}

func (s *Server) handleDisablePlugin(w http.ResponseWriter, r *http.Request) {
	s.handlePluginState(w, r, false)
}

func (s *Server) handlePluginState(w http.ResponseWriter, r *http.Request, enabled bool) {
	if !s.validMutationOrigin(r) {
		writeProblem(w, http.StatusForbidden, "request origin is not allowed")
		return
	}
	if s.plugins == nil {
		writeProblem(w, http.StatusNotImplemented, "plugin management is unavailable")
		return
	}
	var item plugin.Installed
	var err error
	if enabled {
		item, err = s.plugins.Enable(r.Context(), r.PathValue("pluginID"))
	} else {
		item, err = s.plugins.Disable(r.Context(), r.PathValue("pluginID"))
	}
	if err != nil {
		s.writePluginError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) handleRemovePlugin(w http.ResponseWriter, r *http.Request) {
	if !s.validMutationOrigin(r) {
		writeProblem(w, http.StatusForbidden, "request origin is not allowed")
		return
	}
	if s.plugins == nil {
		writeProblem(w, http.StatusNotImplemented, "plugin management is unavailable")
		return
	}
	if err := s.plugins.Remove(r.Context(), r.PathValue("pluginID")); err != nil {
		s.writePluginError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) writePluginError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, plugin.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "plugin not found")
	case errors.Is(err, plugin.ErrConflict):
		writeProblem(w, http.StatusConflict, "another plugin version is already installed")
	case errors.Is(err, pluginmanager.ErrInvalidSource):
		writeProblem(w, http.StatusBadRequest, "plugin source must be a valid workspace-relative directory")
	case errors.Is(err, plugin.ErrInvalidManifest), errors.Is(err, plugin.ErrIncompatible),
		errors.Is(err, plugin.ErrIntegrity):
		writeProblem(w, http.StatusBadRequest, err.Error())
	default:
		s.internalError(w, r, err)
	}
}

func decodeModelConfigInput(w http.ResponseWriter, r *http.Request) (modelConfigInput, bool) {
	body := http.MaxBytesReader(w, r.Body, maxRequestBytes)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var input modelConfigInput
	if err := decoder.Decode(&input); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid request body")
		return modelConfigInput{}, false
	}
	return input, true
}

func modelConfigDraft(
	input modelConfigInput,
	currentSecretRef string,
	defaultEnabled bool,
) (modelconfig.Draft, error) {
	secretRef := currentSecretRef
	if strings.TrimSpace(input.APIKeyEnv) != "" {
		var err error
		secretRef, err = secret.Ref(input.APIKeyEnv)
		if err != nil {
			return modelconfig.Draft{}, err
		}
	}
	enabled := defaultEnabled
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	return modelconfig.Draft{
		Name:       input.Name,
		Provider:   input.Provider,
		BaseURL:    input.BaseURL,
		Model:      input.Model,
		SecretRef:  secretRef,
		Enabled:    enabled,
		SetDefault: input.SetDefault,
	}, nil
}

func validateModelCredentialInput(input modelConfigInput, creating bool) error {
	hasEnvironment := strings.TrimSpace(input.APIKeyEnv) != ""
	hasAPIKey := input.APIKey != nil
	if creating && input.ClearAPIKey {
		return errors.New("clear_api_key is invalid when creating a model config")
	}
	if hasAPIKey && *input.APIKey == "" {
		return errors.New("api_key must not be empty; omit it when no key is required")
	}
	if hasAPIKey && len(*input.APIKey) > app.MaxModelCredentialBytes {
		return fmt.Errorf("api_key must not exceed %d bytes", app.MaxModelCredentialBytes)
	}
	credentialChoices := 0
	if hasEnvironment {
		credentialChoices++
	}
	if hasAPIKey {
		credentialChoices++
	}
	if input.ClearAPIKey {
		credentialChoices++
	}
	if credentialChoices > 1 {
		return errors.New("api_key, api_key_env, and clear_api_key are mutually exclusive")
	}
	return nil
}

func (s *Server) writeModelConfigError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, app.ErrCredentialStoreUnavailable):
		writeProblem(w, http.StatusServiceUnavailable, "system credential store is unavailable")
	case errors.Is(err, secret.ErrUnavailable):
		writeProblem(w, http.StatusServiceUnavailable, "system credential store became unavailable")
	case errors.Is(err, secret.ErrTooLarge):
		writeProblem(w, http.StatusBadRequest, "API key exceeds the portable system credential limit")
	case errors.Is(err, modelconfig.ErrInvalid):
		writeProblem(w, http.StatusBadRequest, "invalid model config")
	case errors.Is(err, modelconfig.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "model config not found")
	case errors.Is(err, modelconfig.ErrConflict):
		writeProblem(w, http.StatusConflict, "model config name already exists")
	case errors.Is(err, modelconfig.ErrDisabled):
		writeProblem(w, http.StatusConflict, "model config is disabled")
	default:
		s.internalError(w, r, err)
	}
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetTask(r.Context(), r.PathValue("taskID"))
	if errors.Is(err, task.ErrNotFound) {
		writeProblem(w, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeProblem(w, http.StatusInternalServerError, "streaming is unavailable")
		return
	}
	afterID, err := eventCursor(r)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid event cursor")
		return
	}
	if _, err := s.store.GetTask(r.Context(), r.PathValue("taskID")); err != nil {
		if errors.Is(err, task.ErrNotFound) {
			writeProblem(w, http.StatusNotFound, "task not found")
			return
		}
		s.internalError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	poll := time.NewTicker(250 * time.Millisecond)
	heartbeat := time.NewTicker(15 * time.Second)
	defer poll.Stop()
	defer heartbeat.Stop()
	for {
		events, err := s.store.EventsAfter(r.Context(), r.PathValue("taskID"), afterID, 200)
		if err != nil {
			s.logger.WarnContext(r.Context(), "sse event query failed", "error", err)
			return
		}
		for _, event := range events {
			if err := writeSSE(w, event); err != nil {
				return
			}
			afterID = event.ID
		}
		if len(events) > 0 {
			flusher.Flush()
		}

		select {
		case <-s.shutdownCtx.Done():
			return
		case <-r.Context().Done():
			return
		case <-poll.C:
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		staticTokenValid := subtle.ConstantTimeCompare([]byte(provided), []byte(s.token)) == 1
		if !staticTokenValid && !s.validBrowserToken(provided, time.Now()) {
			writeProblem(w, http.StatusUnauthorized, "authentication required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireConfiguredHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.secureTransport != (r.TLS != nil) {
			writeProblem(w, http.StatusMisdirectedRequest, "request transport is not allowed")
			return
		}
		authority, _, err := canonicalAuthority(s.originScheme, r.Host)
		if err != nil || authority != s.authority {
			writeProblem(w, http.StatusMisdirectedRequest, "request host is not allowed")
			return
		}
		if !isLoopbackPeer(r.RemoteAddr) {
			writeProblem(w, http.StatusMisdirectedRequest, "request peer is not loopback")
			return
		}
		if r.URL.IsAbs() {
			absoluteAuthority, _, absoluteErr := canonicalAuthority(strings.ToLower(r.URL.Scheme), r.URL.Host)
			if absoluteErr != nil || strings.ToLower(r.URL.Scheme) != s.originScheme ||
				absoluteAuthority != s.authority {
				writeProblem(w, http.StatusMisdirectedRequest, "request target is not allowed")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isLoopbackPeer(remoteAddress string) bool {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		return false
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func (s *Server) validMutationOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	return origin == s.origin
}

func (s *Server) requireMutationOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.validMutationOrigin(r) {
			writeProblem(w, http.StatusForbidden, "request origin is not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleWeb() http.Handler {
	assets, err := fs.Sub(webAssets, "web")
	if err != nil {
		panic(err)
	}
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				writeProblem(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			token, tokenErr := s.issueBrowserToken(time.Now())
			if tokenErr != nil {
				s.internalError(w, r, tokenErr)
				return
			}
			const head = "</head>"
			injection := []byte(`<meta name="kern-session-token" content="` + html.EscapeString(token) + `">`)
			page := bytes.Replace(index, []byte(head), append(injection, []byte(head)...), 1)
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Length", strconv.Itoa(len(page)))
			w.WriteHeader(http.StatusOK)
			if r.Method != http.MethodHead {
				_, _ = w.Write(page)
			}
			return
		}
		files.ServeHTTP(w, r)
	})
}

func (s *Server) issueBrowserToken(now time.Time) (string, error) {
	payload := make([]byte, 1+8+16)
	payload[0] = browserSessionV1
	binary.BigEndian.PutUint64(payload[1:9], uint64(now.Add(browserSessionTTL).Unix()))
	if _, err := rand.Read(payload[9:]); err != nil {
		return "", fmt.Errorf("httpapi: generating browser session token: %w", err)
	}
	mac := hmac.New(sha256.New, s.browserTokenKey[:])
	_, _ = mac.Write(payload)
	return "ks1." + base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *Server) validBrowserToken(token string, now time.Time) bool {
	prefix, encoded, ok := strings.Cut(token, ".")
	if !ok || prefix != "ks1" {
		return false
	}
	encodedPayload, encodedMAC, ok := strings.Cut(encoded, ".")
	if !ok || strings.Contains(encodedMAC, ".") {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil || len(payload) != 1+8+16 || payload[0] != browserSessionV1 {
		return false
	}
	providedMAC, err := base64.RawURLEncoding.DecodeString(encodedMAC)
	if err != nil || len(providedMAC) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, s.browserTokenKey[:])
	_, _ = mac.Write(payload)
	if !hmac.Equal(providedMAC, mac.Sum(nil)) {
		return false
	}
	expiresAt := int64(binary.BigEndian.Uint64(payload[1:9]))
	return expiresAt >= now.Unix() && expiresAt <= now.Add(browserSessionTTL).Unix()
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		identity, _ := tracecontext.From(r.Context())
		s.logger.InfoContext(
			r.Context(),
			"http request",
			"method", r.Method,
			"path", r.URL.Path,
			"duration_ms", time.Since(started).Milliseconds(),
			"trace_id", identity.TraceID,
			"span_id", identity.SpanID,
		)
	})
}

func (s *Server) requestTrace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := tracecontext.NewRequest(r.Header.Get("Traceparent"))
		w.Header().Set("Traceparent", identity.Traceparent())
		next.ServeHTTP(w, r.WithContext(tracecontext.With(r.Context(), identity)))
	})
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, _ error) {
	identity, _ := tracecontext.From(r.Context())
	s.logger.ErrorContext(
		r.Context(),
		"http request failed",
		"error_kind", "internal",
		"trace_id", identity.TraceID,
		"span_id", identity.SpanID,
	)
	writeProblem(w, http.StatusInternalServerError, "request failed")
}

func eventCursor(r *http.Request) (int64, error) {
	raw := r.Header.Get("Last-Event-ID")
	if query := r.URL.Query().Get("after"); query != "" {
		raw = query
	}
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, errors.New("httpapi: invalid event cursor")
	}
	return value, nil
}

func writeSSE(w http.ResponseWriter, event task.Event) error {
	encoded, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encoding sse event: %w", err)
	}
	_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.ID, event.Type, encoded)
	if err != nil {
		return fmt.Errorf("writing sse event: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, destination any) error {
	body := http.MaxBytesReader(w, r.Body, maxRequestBytes)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("httpapi: request body must contain one JSON object")
	}
	return nil
}

func writeProblem(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{
		"schema_version": task.SchemaVersion,
		"error":          message,
	})
}

func taskTitle(goal string) string {
	runes := []rune(strings.TrimSpace(goal))
	if len(runes) <= 36 {
		return string(runes)
	}
	return string(runes[:36]) + "…"
}
