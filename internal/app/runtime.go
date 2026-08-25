// Package app wires Kern's application components together.
package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/userInner/kern/internal/agent"
	"github.com/userInner/kern/internal/approval"
	"github.com/userInner/kern/internal/artifact"
	"github.com/userInner/kern/internal/authorization"
	"github.com/userInner/kern/internal/contextbuilder"
	"github.com/userInner/kern/internal/engine"
	"github.com/userInner/kern/internal/model"
	"github.com/userInner/kern/internal/model/baseline"
	"github.com/userInner/kern/internal/model/compatible"
	"github.com/userInner/kern/internal/modelconfig"
	"github.com/userInner/kern/internal/operation"
	"github.com/userInner/kern/internal/pluginactivation"
	"github.com/userInner/kern/internal/pluginmanager"
	"github.com/userInner/kern/internal/pluginruntime"
	"github.com/userInner/kern/internal/pluginverifier"
	"github.com/userInner/kern/internal/policy"
	"github.com/userInner/kern/internal/secret"
	"github.com/userInner/kern/internal/store/sqlite"
	"github.com/userInner/kern/internal/task"
	"github.com/userInner/kern/internal/tool"
	"github.com/userInner/kern/internal/tool/builtin"
	"github.com/userInner/kern/internal/verifier"
	"github.com/userInner/kern/internal/workspace"
)

// Config controls the local runtime.
type Config struct {
	DataDir             string
	WorkspaceDir        string
	WorkerCount         int
	Logger              *slog.Logger
	MaxTurns            int
	MaxToolCalls        int
	MaxTokens           int
	MaxCostMicros       int64
	MaxTaskDuration     time.Duration
	AutoActivatePlugins bool
	ModelGenerator      model.Generator
	HostCapabilities    []builtin.HostCapability
	WASMSandbox         pluginruntime.WASMSandbox
	SecretVault         secret.Vault
	SettingsPath        string
	PolicyProfile       string
	RetentionDays       int
}

type processor interface {
	Process(ctx context.Context, item task.Task) (string, error)
}

// TaskOptions selects task-scoped model and plugin overrides before execution.
type TaskOptions struct {
	Title          string
	Goal           string
	ModelConfigID  string
	EnablePlugins  []string
	DisablePlugins []string
	Attachments    []TaskAttachment
}

// TaskAttachment is one bounded image supplied explicitly by the user. Its
// bytes are persisted as an immutable task artifact before execution starts.
type TaskAttachment struct {
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Data      []byte `json:"data"`
}

// TaskInput is one user follow-up that starts a new Attempt while preserving
// inherited context and adding any new immutable image artifacts.
type TaskInput struct {
	Content     string
	Attachments []TaskAttachment
}

const (
	maxTaskAttachments     = 4
	maxTaskAttachmentSize  = 8 << 20
	maxTaskAttachmentTotal = 12 << 20
)

// Runtime is the local composition root used by CLI and HTTP transports.
type Runtime struct {
	Store             *sqlite.Store
	Workspace         *workspace.Workspace
	Artifacts         *artifact.Store
	Plugins           *pluginmanager.Manager
	Mode              string
	engine            *engine.Engine
	tools             *tool.Registry
	secrets           secretResolver
	context           *contextbuilder.Builder
	configs           *agentConfigSource
	policy            *policy.Evaluator
	settings          *settingsState
	vault             secret.Vault
	vaultReady        bool
	wasmPluginRuntime bool
	logger            *slog.Logger
	closeOnce         sync.Once
	closeErr          error
}

var (
	errExecutionPaused    = errors.New("app: execution paused")
	errExecutionCancelled = errors.New("app: execution cancelled")
	// ErrInvalidTaskAttachment identifies a user-correctable attachment error.
	ErrInvalidTaskAttachment = errors.New("app: invalid task attachment")
)

// Open creates a local runtime and starts its workers.
func Open(ctx context.Context, config Config) (*Runtime, error) {
	if strings.TrimSpace(config.DataDir) == "" {
		return nil, errors.New("app: data directory is required")
	}
	if err := os.MkdirAll(config.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating data directory: %w", err)
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	store, err := sqlite.Open(ctx, filepath.Join(config.DataDir, "kern.db"))
	if err != nil {
		return nil, err
	}
	if backupPath := store.MigrationBackupPath(); backupPath != "" {
		logger.InfoContext(ctx, "database migration backup created", "path", backupPath)
	}
	workspaceDir := strings.TrimSpace(config.WorkspaceDir)
	if workspaceDir == "" {
		workspaceDir, err = os.Getwd()
		if err != nil {
			_ = store.Close()
			return nil, fmt.Errorf("resolving default workspace: %w", err)
		}
	}
	workspaceRoot, err := workspace.Open(workspaceDir)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	artifactStore, err := artifact.Open(filepath.Join(config.DataDir, "artifacts"), store)
	if err != nil {
		_ = workspaceRoot.Close()
		_ = store.Close()
		return nil, err
	}
	pluginManager, err := pluginmanager.New(filepath.Join(config.DataDir, "plugins"), store)
	if err != nil {
		_ = workspaceRoot.Close()
		_ = store.Close()
		return nil, err
	}
	cleanupPluginManager := true
	defer func() {
		if cleanupPluginManager {
			_ = pluginManager.Close()
		}
	}()
	wasmSandbox := config.WASMSandbox
	wasmReady := false
	if wasmSandbox != nil {
		if checkErr := wasmSandbox.Check(ctx); checkErr != nil {
			logger.WarnContext(ctx, "WASM plugin runtime unavailable", "error", checkErr)
			wasmSandbox = nil
		} else {
			wasmReady = true
		}
	}
	tools, err := builtinRegistry(
		workspaceRoot,
		store,
		pluginManager,
		wasmSandbox,
		config.HostCapabilities,
	)
	if err != nil {
		_ = pluginManager.Close()
		_ = workspaceRoot.Close()
		_ = store.Close()
		return nil, err
	}
	policyEvaluator := policy.New()
	profile := policy.Profile(strings.TrimSpace(config.PolicyProfile))
	if profile == "" {
		profile = policy.ProfileLocalSafe
	}
	if err := policyEvaluator.SetProfile(profile); err != nil {
		_ = pluginManager.Close()
		_ = workspaceRoot.Close()
		_ = store.Close()
		return nil, err
	}
	authorizer, err := authorization.New(store, policyEvaluator, authorization.Config{})
	if err != nil {
		_ = pluginManager.Close()
		_ = workspaceRoot.Close()
		_ = store.Close()
		return nil, err
	}
	contextManager, err := contextbuilder.New(store, contextbuilder.Config{})
	if err != nil {
		_ = pluginManager.Close()
		_ = workspaceRoot.Close()
		_ = store.Close()
		return nil, err
	}

	secretResolvers := []secret.Resolver{secret.Environment{}}
	vaultReady := false
	if config.SecretVault != nil {
		secretResolvers = append(secretResolvers, config.SecretVault)
		if checkErr := config.SecretVault.Check(ctx); checkErr != nil {
			logger.WarnContext(ctx, "system credential store unavailable", "error", checkErr)
		} else {
			vaultReady = true
		}
	}
	secretStore, err := secret.NewChain(secretResolvers...)
	if err != nil {
		_ = pluginManager.Close()
		_ = workspaceRoot.Close()
		_ = store.Close()
		return nil, err
	}
	selectedProcessor, mode, configSource, err := configuredProcessor(
		store,
		tools,
		authorizer,
		artifactStore,
		contextManager,
		secretStore,
		config.ModelGenerator,
		agent.Config{
			MaxTurns:      config.MaxTurns,
			MaxToolCalls:  config.MaxToolCalls,
			MaxTokens:     config.MaxTokens,
			MaxCostMicros: config.MaxCostMicros,
			MaxDuration:   config.MaxTaskDuration,
		},
	)
	if err != nil {
		_ = pluginManager.Close()
		_ = workspaceRoot.Close()
		_ = store.Close()
		return nil, err
	}
	retentionDays := config.RetentionDays
	if retentionDays == 0 {
		retentionDays = 90
	}
	settingsPath := strings.TrimSpace(config.SettingsPath)
	if settingsPath == "" {
		settingsPath = filepath.Join(config.DataDir, "runtime-config.json")
	}
	settings, err := newSettingsState(settingsPath, retentionDays)
	if err != nil {
		_ = pluginManager.Close()
		_ = workspaceRoot.Close()
		_ = store.Close()
		return nil, err
	}
	pluginActivator, err := pluginactivation.New(
		pluginManager,
		store,
		workspaceRoot,
		pluginactivation.Config{
			AutoActivate:       config.AutoActivatePlugins,
			SystemPrompt:       agent.SystemPrompt(),
			ExecutableRuntimes: availablePluginRuntimes(wasmSandbox),
		},
	)
	if err != nil {
		_ = pluginManager.Close()
		_ = workspaceRoot.Close()
		_ = store.Close()
		return nil, err
	}
	selectedProcessor, err = pluginactivation.NewProcessor(
		selectedProcessor,
		pluginActivator,
		contextManager,
	)
	if err != nil {
		_ = pluginManager.Close()
		_ = workspaceRoot.Close()
		_ = store.Close()
		return nil, err
	}
	workerCount := config.WorkerCount
	if workerCount < 1 {
		workerCount = 1
	}
	coreVerificationSuite, err := verifier.New(store, artifactStore, workspaceRoot)
	if err != nil {
		_ = pluginManager.Close()
		_ = workspaceRoot.Close()
		_ = store.Close()
		return nil, err
	}
	verificationSuite, err := pluginverifier.New(
		coreVerificationSuite,
		store,
		pluginManager,
		workspaceRoot,
	)
	if err != nil {
		_ = pluginManager.Close()
		_ = workspaceRoot.Close()
		_ = store.Close()
		return nil, err
	}

	runtime := &Runtime{
		Store:             store,
		Workspace:         workspaceRoot,
		Artifacts:         artifactStore,
		Plugins:           pluginManager,
		Mode:              mode,
		tools:             tools,
		secrets:           secretStore,
		context:           contextManager,
		configs:           configSource,
		policy:            policyEvaluator,
		settings:          settings,
		vault:             config.SecretVault,
		vaultReady:        vaultReady,
		wasmPluginRuntime: wasmReady,
		logger:            logger,
		engine: engine.NewWithVerifier(
			ctx,
			store,
			selectedProcessor,
			verificationSuite,
			logger,
			workerCount,
		),
	}
	if err := runtime.recoverInterrupted(ctx); err != nil {
		runtime.engine.Close()
		_ = pluginManager.Close()
		_ = workspaceRoot.Close()
		_ = store.Close()
		return nil, err
	}
	cleanupPluginManager = false
	return runtime, nil
}

func availablePluginRuntimes(wasmSandbox pluginruntime.WASMSandbox) []string {
	if wasmSandbox == nil {
		return nil
	}
	return []string{"wasm"}
}

// Pause durably pauses a task before cancelling its in-memory execution.
func (r *Runtime) Pause(ctx context.Context, taskID string) (task.Task, error) {
	paused, err := r.Store.PauseTask(ctx, taskID)
	if err != nil {
		return task.Task{}, err
	}
	r.engine.CancelExecution(taskID, errExecutionPaused)
	return paused, nil
}

// Cancel durably cancels a task before cancelling its in-memory execution.
func (r *Runtime) Cancel(ctx context.Context, taskID string) (task.Task, error) {
	cancelled, err := r.Store.CancelTask(ctx, taskID)
	if err != nil {
		return task.Task{}, err
	}
	r.engine.CancelExecution(taskID, errExecutionCancelled)
	return cancelled, nil
}

// Resume creates a new attempt for a paused task and schedules it.
func (r *Runtime) Resume(ctx context.Context, taskID string) (task.Task, error) {
	current, err := r.Store.GetTask(ctx, taskID)
	if err != nil {
		return task.Task{}, err
	}
	if current.Status != task.StatusWaitingInput {
		return task.Task{}, fmt.Errorf(
			"%w: cannot resume from %s",
			task.ErrInvalidTransition,
			current.Status,
		)
	}
	uncertain, err := r.Store.ListUncertainOperations(ctx, taskID)
	if err != nil {
		return task.Task{}, err
	}
	if len(uncertain) > 0 {
		return task.Task{}, operation.ErrUnresolvedEffects
	}
	return r.startNewAttempt(ctx, taskID, "resume after pause")
}

// SubmitInput starts a new Attempt with explicit user-provided follow-up context.
func (r *Runtime) SubmitInput(ctx context.Context, taskID, input string) (task.Task, error) {
	return r.SubmitInputWithAttachments(ctx, taskID, TaskInput{Content: input})
}

// SubmitInputWithAttachments starts a new Attempt with text and bounded image
// inputs. Attachment bytes are persisted before the Attempt is enqueued.
func (r *Runtime) SubmitInputWithAttachments(
	ctx context.Context,
	taskID string,
	input TaskInput,
) (task.Task, error) {
	input.Content = strings.TrimSpace(input.Content)
	if input.Content == "" {
		return task.Task{}, errors.New("app: task input is required")
	}
	if len([]rune(input.Content)) > 20_000 {
		return task.Task{}, errors.New("app: task input exceeds 20000 characters")
	}
	if err := validateTaskAttachments(input.Attachments); err != nil {
		return task.Task{}, err
	}
	current, err := r.Store.GetTask(ctx, taskID)
	if err != nil {
		return task.Task{}, err
	}
	if current.Status != task.StatusWaitingInput && !current.Status.IsTerminal() {
		return task.Task{}, fmt.Errorf(
			"%w: cannot submit input from %s",
			task.ErrInvalidTransition,
			current.Status,
		)
	}
	uncertain, err := r.Store.ListUncertainOperations(ctx, taskID)
	if err != nil {
		return task.Task{}, err
	}
	if len(uncertain) > 0 {
		return task.Task{}, operation.ErrUnresolvedEffects
	}
	return r.startNewAttemptWithInput(ctx, taskID, "continue with user input", input)
}

// Retry creates and schedules a new attempt for a terminal task.
func (r *Runtime) Retry(ctx context.Context, taskID string) (task.Task, error) {
	current, err := r.Store.GetTask(ctx, taskID)
	if err != nil {
		return task.Task{}, err
	}
	if !current.Status.IsTerminal() {
		return task.Task{}, fmt.Errorf(
			"%w: cannot retry from %s",
			task.ErrInvalidTransition,
			current.Status,
		)
	}
	return r.startNewAttempt(ctx, taskID, "retry terminal task")
}

// DecideApproval records the local user's explicit decision for one exact operation scope.
func (r *Runtime) DecideApproval(
	ctx context.Context,
	requestID string,
	decision approval.Decision,
) (approval.Receipt, error) {
	return r.Store.DecideApproval(ctx, requestID, decision, "local-user")
}

// ResolveUncertainOperation records the user's conclusion about an external
// side effect whose outcome could not be observed after a crash.
func (r *Runtime) ResolveUncertainOperation(
	ctx context.Context,
	operationID string,
	resolution operation.Resolution,
) (operation.ResolutionReceipt, error) {
	return r.Store.ResolveUncertainOperation(ctx, operationID, resolution, "local-user")
}

// Submit persists and schedules a task.
func (r *Runtime) Submit(ctx context.Context, title, goal string) (task.Task, error) {
	modelConfigID, err := r.defaultModelConfigID(ctx)
	if err != nil {
		return task.Task{}, err
	}
	return r.SubmitWithModel(ctx, title, goal, modelConfigID)
}

// SubmitWithModel creates and schedules a task pinned to the requested model config.
func (r *Runtime) SubmitWithModel(
	ctx context.Context,
	title string,
	goal string,
	modelConfigID string,
) (task.Task, error) {
	return r.SubmitWithOptions(ctx, TaskOptions{
		Title:         title,
		Goal:          goal,
		ModelConfigID: modelConfigID,
	})
}

// SubmitWithOptions creates a task with atomic model and plugin selection.
func (r *Runtime) SubmitWithOptions(ctx context.Context, options TaskOptions) (task.Task, error) {
	if err := validateTaskAttachments(options.Attachments); err != nil {
		return task.Task{}, err
	}
	if strings.TrimSpace(options.ModelConfigID) == "" {
		modelConfigID, err := r.defaultModelConfigID(ctx)
		if err != nil {
			return task.Task{}, err
		}
		options.ModelConfigID = modelConfigID
	}
	created, err := r.Store.CreateTaskWithOptions(
		ctx,
		options.Title,
		options.Goal,
		options.ModelConfigID,
		options.EnablePlugins,
		options.DisablePlugins,
	)
	if err != nil {
		return task.Task{}, err
	}
	if len(options.Attachments) > 0 {
		content := []model.ContentBlock{{Kind: model.ContentText, Text: created.Goal}}
		for _, attachment := range options.Attachments {
			stored, storeErr := r.Artifacts.Put(
				ctx,
				created.ID,
				created.ActiveAttemptID,
				attachment.Name,
				attachment.MediaType,
				"",
				bytes.NewReader(attachment.Data),
			)
			if storeErr != nil {
				return task.Task{}, r.failCreatedTask(ctx, created, storeErr)
			}
			content = append(content, model.ContentBlock{
				Kind:        model.ContentArtifactRef,
				ArtifactRef: stored.ID,
			})
		}
		if _, _, err := r.context.InitializeWithUserContent(
			ctx,
			created,
			agent.SystemPrompt(),
			content,
		); err != nil {
			return task.Task{}, r.failCreatedTask(ctx, created, err)
		}
	}
	if err := r.engine.Enqueue(ctx, created.ID); err != nil {
		transitionErr := r.Store.Transition(
			ctx,
			created.ID,
			task.StatusFailed,
			"",
			err.Error(),
		)
		return task.Task{}, errors.Join(err, transitionErr)
	}
	return created, nil
}

func (r *Runtime) failCreatedTask(ctx context.Context, created task.Task, cause error) error {
	transitionErr := r.Store.Transition(
		ctx,
		created.ID,
		task.StatusFailed,
		"",
		cause.Error(),
	)
	return errors.Join(cause, transitionErr)
}

func validateTaskAttachments(items []TaskAttachment) error {
	if len(items) > maxTaskAttachments {
		return fmt.Errorf("%w: attachments exceed %d files", ErrInvalidTaskAttachment, maxTaskAttachments)
	}
	total := 0
	for _, item := range items {
		mediaType := strings.ToLower(strings.TrimSpace(item.MediaType))
		switch mediaType {
		case "image/png", "image/jpeg", "image/webp", "image/gif":
		default:
			return fmt.Errorf("%w: unsupported media type %q", ErrInvalidTaskAttachment, item.MediaType)
		}
		name := strings.TrimSpace(item.Name)
		if name == "" || name != filepath.Base(name) || name == "." ||
			len([]rune(name)) > 255 || strings.IndexByte(name, 0) >= 0 {
			return fmt.Errorf("%w: name must be one safe file name", ErrInvalidTaskAttachment)
		}
		if len(item.Data) == 0 {
			return fmt.Errorf("%w: attachment data are required", ErrInvalidTaskAttachment)
		}
		if !matchesImageType(mediaType, item.Data) {
			return fmt.Errorf("%w: %q content does not match %s", ErrInvalidTaskAttachment, name, mediaType)
		}
		if len(item.Data) > maxTaskAttachmentSize {
			return fmt.Errorf("%w: %q exceeds %d bytes", ErrInvalidTaskAttachment, name, maxTaskAttachmentSize)
		}
		total += len(item.Data)
		if total > maxTaskAttachmentTotal {
			return fmt.Errorf("%w: attachments exceed %d total bytes", ErrInvalidTaskAttachment, maxTaskAttachmentTotal)
		}
	}
	return nil
}

func matchesImageType(mediaType string, data []byte) bool {
	switch mediaType {
	case "image/png":
		return len(data) >= 8 && bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n"))
	case "image/jpeg":
		return len(data) >= 3 && bytes.Equal(data[:3], []byte{0xff, 0xd8, 0xff})
	case "image/gif":
		return len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) ||
			bytes.Equal(data[:6], []byte("GIF89a")))
	case "image/webp":
		return len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) &&
			bytes.Equal(data[8:12], []byte("WEBP"))
	default:
		return false
	}
}

// SubmitWithPlugins is the transport-friendly task creation surface with
// explicit task-level plugin overrides.
func (r *Runtime) SubmitWithPlugins(
	ctx context.Context,
	title string,
	goal string,
	modelConfigID string,
	enablePlugins []string,
	disablePlugins []string,
) (task.Task, error) {
	var err error
	if strings.TrimSpace(modelConfigID) == "" {
		modelConfigID, err = r.defaultModelConfigID(ctx)
		if err != nil {
			return task.Task{}, err
		}
	}
	return r.SubmitWithOptions(ctx, TaskOptions{
		Title:          title,
		Goal:           goal,
		ModelConfigID:  modelConfigID,
		EnablePlugins:  enablePlugins,
		DisablePlugins: disablePlugins,
	})
}

func (r *Runtime) defaultModelConfigID(ctx context.Context) (string, error) {
	defaultConfig, err := r.Store.DefaultModelConfig(ctx)
	if errors.Is(err, modelconfig.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return defaultConfig.ID, nil
}

// TestModelConfig performs one bounded provider request without persisting its content.
func (r *Runtime) TestModelConfig(
	ctx context.Context,
	configID string,
) (modelconfig.TestResult, error) {
	config, err := r.Store.GetModelConfig(ctx, configID)
	if err != nil {
		return modelconfig.TestResult{}, err
	}
	if !config.Enabled {
		return modelconfig.TestResult{}, modelconfig.ErrDisabled
	}
	apiKey := ""
	if config.SecretRef != "" {
		apiKey, err = r.secrets.Resolve(ctx, config.SecretRef)
		if err != nil {
			return modelconfig.TestResult{}, fmt.Errorf("resolving model credential: %w", err)
		}
	}
	client, err := compatible.New(compatible.Config{
		BaseURL: config.BaseURL,
		APIKey:  apiKey,
		Model:   config.Model,
	})
	apiKey = ""
	if err != nil {
		return modelconfig.TestResult{}, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	started := time.Now()
	response, err := client.Generate(probeCtx, model.Request{
		Messages: []model.Message{{
			Role: model.RoleUser,
			Content: []model.ContentBlock{{
				Kind: model.ContentText,
				Text: "Reply with OK.",
			}},
		}},
		MaxTokens: 1,
	})
	if err != nil {
		return modelconfig.TestResult{}, err
	}
	capabilities := client.Capabilities()
	return modelconfig.TestResult{
		SchemaVersion: modelconfig.SchemaVersion,
		ConfigID:      config.ID,
		Provider:      config.Provider,
		Model:         config.Model,
		OK:            true,
		LatencyMS:     time.Since(started).Milliseconds(),
		RequestID:     response.RequestID,
		Capabilities: map[string]bool{
			"text_input":        capabilities.TextInput,
			"image_input":       capabilities.ImageInput,
			"tool_calling":      capabilities.ToolCalling,
			"streaming":         capabilities.Streaming,
			"structured_output": capabilities.StructuredOutput,
			"reasoning_summary": capabilities.ReasoningSummary,
			"usage":             capabilities.Usage,
		},
	}, nil
}

// Wait blocks until a task reaches a terminal status.
func (r *Runtime) Wait(ctx context.Context, taskID string) (task.Task, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		item, err := r.Store.GetTask(ctx, taskID)
		if err != nil {
			return task.Task{}, err
		}
		if item.Status.IsTerminal() {
			return item, nil
		}
		select {
		case <-ctx.Done():
			return task.Task{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

// WaitForBoundary returns when a task finishes or needs user interaction.
func (r *Runtime) WaitForBoundary(ctx context.Context, taskID string) (task.Task, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		item, err := r.Store.GetTask(ctx, taskID)
		if err != nil {
			return task.Task{}, err
		}
		if item.Status.IsTerminal() || item.Status == task.StatusWaitingApproval ||
			item.Status == task.StatusWaitingInput {
			return item, nil
		}
		select {
		case <-ctx.Done():
			return task.Task{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

// Close stops workers before closing storage.
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.engine.Close()
		r.closeErr = errors.Join(r.Plugins.Close(), r.Workspace.Close(), r.Store.Close())
	})
	return r.closeErr
}

func (r *Runtime) startNewAttempt(
	ctx context.Context,
	taskID string,
	reason string,
) (task.Task, error) {
	return r.startNewAttemptWithInput(ctx, taskID, reason, TaskInput{})
}

func (r *Runtime) startNewAttemptWithInput(
	ctx context.Context,
	taskID string,
	reason string,
	input TaskInput,
) (task.Task, error) {
	started, err := r.Store.CreateAttempt(ctx, taskID, reason)
	if err != nil {
		return task.Task{}, err
	}
	if input.Content != "" || len(input.Attachments) > 0 {
		if _, _, err := r.context.Initialize(ctx, started, agent.SystemPrompt()); err != nil {
			transitionErr := r.Store.Transition(ctx, taskID, task.StatusFailed, "", err.Error())
			return task.Task{}, errors.Join(err, transitionErr)
		}
		content := []model.ContentBlock{{Kind: model.ContentText, Text: input.Content}}
		for _, attachment := range input.Attachments {
			stored, storeErr := r.Artifacts.Put(
				ctx,
				started.ID,
				started.ActiveAttemptID,
				attachment.Name,
				attachment.MediaType,
				"",
				bytes.NewReader(attachment.Data),
			)
			if storeErr != nil {
				return task.Task{}, r.failCreatedTask(ctx, started, storeErr)
			}
			content = append(content, model.ContentBlock{
				Kind: model.ContentArtifactRef, ArtifactRef: stored.ID,
			})
		}
		if _, err := r.context.Append(ctx, started, model.Message{
			Role:    model.RoleUser,
			Content: content,
		}, contextbuilder.TrustUser, contextbuilder.SourceUserInput, started.ActiveAttemptID); err != nil {
			transitionErr := r.Store.Transition(ctx, taskID, task.StatusFailed, "", err.Error())
			return task.Task{}, errors.Join(err, transitionErr)
		}
	}
	if err := r.engine.Enqueue(ctx, taskID); err != nil {
		transitionErr := r.Store.Transition(
			ctx,
			taskID,
			task.StatusFailed,
			"",
			err.Error(),
		)
		return task.Task{}, errors.Join(err, transitionErr)
	}
	return started, nil
}

func (r *Runtime) recoverInterrupted(ctx context.Context) error {
	items, err := r.Store.RecoverableTasks(ctx, time.Now().UTC(), 200)
	if err != nil {
		return fmt.Errorf("finding interrupted tasks: %w", err)
	}
	for _, item := range items {
		if item.Status != task.StatusCreated {
			_, err := r.Store.MarkInterruptedOperationsUnknown(ctx, item.ID)
			if err != nil {
				return fmt.Errorf("recovering interrupted operations for %s: %w", item.ID, err)
			}
			uncertain, err := r.reconcileUncertainOperations(ctx, item.ID)
			if err != nil {
				return fmt.Errorf("reconciling interrupted operations for %s: %w", item.ID, err)
			}
			paused, err := r.Store.PauseTask(ctx, item.ID)
			if err != nil {
				return fmt.Errorf("closing interrupted attempt for %s: %w", item.ID, err)
			}
			if len(uncertain) > 0 {
				if err := r.Store.AppendEvent(
					ctx,
					item.ID,
					paused.ActiveAttemptID,
					"task.recovery_blocked",
					map[string]any{
						"reason":               "uncertain_side_effects",
						"uncertain_operations": len(uncertain),
					},
				); err != nil {
					return fmt.Errorf("recording blocked recovery for %s: %w", item.ID, err)
				}
				continue
			}
			if _, err := r.Store.CreateAttempt(ctx, item.ID, "recover after interrupted runtime"); err != nil {
				return fmt.Errorf("creating recovery attempt for %s: %w", item.ID, err)
			}
		}
		if err := r.engine.Enqueue(ctx, item.ID); err != nil {
			return fmt.Errorf("scheduling recovered task %s: %w", item.ID, err)
		}
	}
	return nil
}

func (r *Runtime) reconcileUncertainOperations(
	ctx context.Context,
	taskID string,
) ([]operation.Operation, error) {
	uncertain, err := r.Store.ListUncertainOperations(ctx, taskID)
	if err != nil {
		return nil, err
	}
	for _, op := range uncertain {
		result, supported, reconcileErr := r.tools.Reconcile(ctx, op.Tool, op.Input, op.Recovery)
		if reconcileErr != nil || !supported {
			if reconcileErr != nil {
				if err := r.Store.AppendEvent(ctx, op.TaskID, op.AttemptID, "operation.reconciliation_failed", map[string]any{
					"operation_id": op.ID,
					"tool":         op.Tool,
					"reason":       "tool_state_could_not_be_reconciled",
				}); err != nil {
					return nil, err
				}
			}
			continue
		}
		var resolution operation.Resolution
		switch result.Disposition {
		case tool.RecoverySucceeded:
			resolution = operation.ResolutionSucceeded
		case tool.RecoveryNotExecuted:
			resolution = operation.ResolutionNotExecuted
		case tool.RecoveryConflict:
			if err := r.Store.AppendEvent(ctx, op.TaskID, op.AttemptID, "operation.recovery_conflict", map[string]any{
				"operation_id": op.ID,
				"tool":         op.Tool,
				"summary":      result.Summary,
			}); err != nil {
				return nil, err
			}
			continue
		default:
			continue
		}
		if _, err := r.Store.ResolveUncertainOperation(
			ctx,
			op.ID,
			resolution,
			"kern-recovery:"+op.Tool,
		); err != nil {
			return nil, err
		}
	}
	return r.Store.ListUncertainOperations(ctx, taskID)
}

func configuredProcessor(
	store *sqlite.Store,
	tools *tool.Registry,
	authorizer agent.Authorizer,
	artifacts *artifact.Store,
	contextManager *contextbuilder.Builder,
	secrets secretResolver,
	modelGenerator model.Generator,
	agentConfig agent.Config,
) (processor, string, *agentConfigSource, error) {
	agentConfig.Authorizer = authorizer
	agentConfig.Artifacts = artifacts
	agentConfig.ArtifactReader = artifacts
	agentConfig.Context = contextManager
	var err error
	agentConfig, err = agent.NormalizeConfig(agentConfig)
	if err != nil {
		return nil, "", nil, err
	}
	configs := newAgentConfigSource(agentConfig)
	fallback, mode, err := configuredFallbackProcessor(store, tools, modelGenerator, configs)
	if err != nil {
		return nil, "", nil, err
	}
	router, err := newModelRouter(store, secrets, fallback, tools, configs)
	if err != nil {
		return nil, "", nil, err
	}
	return router, mode, configs, nil
}

func configuredFallbackProcessor(
	store *sqlite.Store,
	tools *tool.Registry,
	modelGenerator model.Generator,
	configs *agentConfigSource,
) (processor, string, error) {
	if modelGenerator != nil {
		return &dynamicAgentProcessor{
			generator: modelGenerator, store: store, tools: tools, configs: configs,
		}, "embedded:model-provider", nil
	}
	baseURL := strings.TrimSpace(os.Getenv("KERN_MODEL_BASE_URL"))
	model := strings.TrimSpace(os.Getenv("KERN_MODEL"))
	if baseURL == "" && model == "" {
		return baseline.New(), "offline-baseline", nil
	}
	if baseURL == "" || model == "" {
		return nil, "", errors.New("app: kern_model_base_url and kern_model must be set together")
	}
	client, err := compatible.New(compatible.Config{
		BaseURL: baseURL,
		APIKey:  os.Getenv("KERN_MODEL_API_KEY"),
		Model:   model,
	})
	if err != nil {
		return nil, "", err
	}
	return &dynamicAgentProcessor{
		generator: client, store: store, tools: tools, configs: configs,
	}, "model:" + model, nil
}

func builtinRegistry(
	workspaceRoot *workspace.Workspace,
	store *sqlite.Store,
	plugins *pluginmanager.Manager,
	wasmSandbox pluginruntime.WASMSandbox,
	hostCapabilities []builtin.HostCapability,
) (*tool.Registry, error) {
	inspect, err := builtin.NewInspect(workspaceRoot)
	if err != nil {
		return nil, err
	}
	change, err := builtin.NewChange(workspaceRoot)
	if err != nil {
		return nil, err
	}
	execute, err := builtin.NewExecute(workspaceRoot)
	if err != nil {
		return nil, err
	}
	capabilityRuntime := pluginruntime.NewWithConfig(pluginruntime.Config{WASM: wasmSandbox})
	capability, err := builtin.NewCapability(store, plugins, capabilityRuntime, hostCapabilities...)
	if err != nil {
		return nil, err
	}
	return tool.NewRegistry(inspect, change, execute, capability)
}
