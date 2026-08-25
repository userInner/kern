// Package embedded runs Kern Core inside a Go host process and exposes the
// same typed client contract used by remote integrations.
package embedded

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/userInner/kern/internal/app"
	"github.com/userInner/kern/internal/configuration"
	"github.com/userInner/kern/internal/evalservice"
	"github.com/userInner/kern/internal/pluginruntime/wazerosandbox"
	"github.com/userInner/kern/internal/secret/systemvault"
	"github.com/userInner/kern/internal/transport/httpapi"
	"github.com/userInner/kern/sdk/kern"
)

const shutdownTimeout = 5 * time.Second

// Config controls one embedded runtime. DataDir is required; every other zero
// value receives the same bounded default used by the standalone Core.
type Config struct {
	DataDir              string
	WorkspaceDir         string
	WorkerCount          int
	MaxTurns             int
	MaxToolCalls         int
	MaxTokens            int
	MaxCostMicros        int64
	MaxTaskDuration      time.Duration
	MaxActiveEvaluations int
	AutoActivatePlugins  bool
	ModelProvider        ModelProvider
	Capabilities         []Capability
	MetricsEnabled       bool
	Logger               *slog.Logger
	SettingsPath         string
	PolicyProfile        string
	RetentionDays        int
}

// Runtime owns an in-process Core, a loopback-only transport, and its typed
// client. Close must be called when the host no longer needs the runtime.
type Runtime struct {
	core      *app.Runtime
	evals     *evalservice.Service
	server    *http.Server
	listener  net.Listener
	client    *kern.Client
	baseURL   string
	errors    chan error
	serveDone chan struct{}
	closed    chan struct{}

	closeOnce sync.Once
	closeErr  error
}

// Open starts an embedded Kern instance on an ephemeral IPv4 loopback port.
// Cancelling ctx closes the runtime; caller cancellation is never used as a
// task request context after startup.
func Open(ctx context.Context, config Config) (*Runtime, error) {
	if ctx == nil {
		return nil, errors.New("embedded: context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.DataDir) == "" {
		return nil, errors.New("embedded: data directory is required")
	}
	settingsPath := strings.TrimSpace(config.SettingsPath)
	if settingsPath == "" {
		settingsPath = filepath.Join(config.DataDir, "runtime-config.json")
	}
	config, err := applyPersistedSettings(config, settingsPath)
	if err != nil {
		return nil, err
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	hostCapabilities, err := adaptCapabilities(config.Capabilities)
	if err != nil {
		return nil, err
	}
	core, err := app.Open(ctx, app.Config{
		DataDir:             config.DataDir,
		WorkspaceDir:        config.WorkspaceDir,
		WorkerCount:         config.WorkerCount,
		Logger:              logger,
		MaxTurns:            config.MaxTurns,
		MaxToolCalls:        config.MaxToolCalls,
		MaxTokens:           config.MaxTokens,
		MaxCostMicros:       config.MaxCostMicros,
		MaxTaskDuration:     config.MaxTaskDuration,
		AutoActivatePlugins: config.AutoActivatePlugins,
		ModelGenerator:      config.ModelProvider,
		HostCapabilities:    hostCapabilities,
		WASMSandbox:         wazerosandbox.New(),
		SecretVault:         systemvault.New(),
		SettingsPath:        settingsPath,
		PolicyProfile:       config.PolicyProfile,
		RetentionDays:       config.RetentionDays,
	})
	if err != nil {
		return nil, err
	}
	cleanupCore := true
	defer func() {
		if cleanupCore {
			_ = core.Close()
		}
	}()

	maxActiveEvals := config.MaxActiveEvaluations
	if maxActiveEvals < 1 {
		maxActiveEvals = 1
	}
	evals, err := evalservice.New(ctx, evalservice.Config{
		DataRoot:  filepath.Join(config.DataDir, "evals"),
		Store:     core.Store,
		MaxActive: maxActiveEvals,
		MaxTurns:  config.MaxTurns, MaxToolCalls: config.MaxToolCalls,
		Logger: logger,
	})
	if err != nil {
		return nil, err
	}
	cleanupEvals := true
	defer func() {
		if cleanupEvals {
			evals.Close()
		}
	}()

	token, err := sessionToken()
	if err != nil {
		return nil, err
	}
	handler, err := httpapi.New(httpapi.Config{
		Store:          core.Store,
		Artifacts:      core.Artifacts,
		Submitter:      core,
		Models:         core,
		Plugins:        core.Plugins,
		Evals:          evals,
		MetricsEnabled: config.MetricsEnabled,
		Token:          token,
		Mode:           core.Mode,
		Logger:         logger,
	})
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("embedded: opening loopback listener: %w", err)
	}
	cleanupListener := true
	defer func() {
		if cleanupListener {
			_ = listener.Close()
		}
	}()
	baseURL := "http://" + listener.Addr().String()
	client, err := kern.NewClient(kern.Config{BaseURL: baseURL, Token: token})
	if err != nil {
		return nil, err
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       60 * time.Second,
	}
	runtime := &Runtime{
		core: core, evals: evals, server: server, listener: listener,
		client: client, baseURL: baseURL,
		errors: make(chan error, 1), serveDone: make(chan struct{}), closed: make(chan struct{}),
	}
	go runtime.serve()
	go func() {
		select {
		case <-ctx.Done():
			_ = runtime.Close()
		case <-runtime.closed:
		}
	}()
	cleanupCore = false
	cleanupEvals = false
	cleanupListener = false
	return runtime, nil
}

func applyPersistedSettings(config Config, path string) (Config, error) {
	persisted, err := configuration.Load(path)
	if err != nil {
		return Config{}, fmt.Errorf("embedded: loading settings: %w", err)
	}
	if config.MaxTurns == 0 {
		config.MaxTurns = persisted.Runtime.MaxTurns
	}
	if config.MaxToolCalls == 0 {
		config.MaxToolCalls = persisted.Runtime.MaxToolCalls
	}
	if config.MaxTokens == 0 {
		config.MaxTokens = persisted.Runtime.MaxTokens
	}
	if config.MaxCostMicros == 0 {
		config.MaxCostMicros = int64(math.Round(persisted.Runtime.MaxCostUSD * 1_000_000))
	}
	if config.MaxTaskDuration == 0 {
		config.MaxTaskDuration = persisted.Runtime.TaskTimeout.Duration
	}
	if strings.TrimSpace(config.PolicyProfile) == "" {
		config.PolicyProfile = persisted.Policy.Profile
	}
	if config.RetentionDays == 0 {
		config.RetentionDays = persisted.Storage.RetentionDays
	}
	return config, nil
}

// Client returns the concurrency-safe typed client for this embedded Core.
func (r *Runtime) Client() *kern.Client {
	if r == nil {
		return nil
	}
	return r.client
}

// BaseURL returns the ephemeral loopback origin. It contains no credential.
func (r *Runtime) BaseURL() string {
	if r == nil {
		return ""
	}
	return r.baseURL
}

// Errors reports an unexpected transport failure. Normal shutdown closes the
// channel without sending a value.
func (r *Runtime) Errors() <-chan error {
	if r == nil {
		closed := make(chan error)
		close(closed)
		return closed
	}
	return r.errors
}

// Close stops accepting requests, persists evaluation cancellation, and then
// closes the Core stores. It is safe to call concurrently and repeatedly.
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		serverErr := r.server.Shutdown(shutdownCtx)
		if errors.Is(serverErr, context.DeadlineExceeded) {
			serverErr = r.server.Close()
		}
		r.evals.Close()
		coreErr := r.core.Close()
		<-r.serveDone
		r.closeErr = errors.Join(serverErr, coreErr)
		close(r.closed)
	})
	return r.closeErr
}

func (r *Runtime) serve() {
	defer close(r.serveDone)
	defer close(r.errors)
	if err := r.server.Serve(r.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		r.errors <- fmt.Errorf("embedded: serving Core: %w", err)
	}
}

func sessionToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("embedded: generating session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
