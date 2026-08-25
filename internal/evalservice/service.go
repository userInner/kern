// Package evalservice owns asynchronous, durable evaluation runs.
package evalservice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/userInner/kern/internal/evalrunner"
	"github.com/userInner/kern/internal/evaluation"
	"github.com/userInner/kern/internal/id"
	"github.com/userInner/kern/internal/plugin"
)

var (
	errCancelledByUser = errors.New("evaluation cancelled by user")
	errPausedByUser    = errors.New("evaluation paused by user")
	// ErrClosed identifies work rejected after service shutdown begins.
	ErrClosed = errors.New("evalservice: service is closed")
)

// Store is the durable evaluation persistence required by Service.
type Store interface {
	UpsertEvalSuite(ctx context.Context, suite evaluation.Suite) error
	CreateEvalRun(ctx context.Context, run evaluation.Run, config evaluation.RunConfig) error
	StartEvalRun(ctx context.Context, runID string, startedAt time.Time) error
	PauseEvalRun(ctx context.Context, runID, message string) error
	ResumeEvalRun(ctx context.Context, runID string) error
	RecordEvalProgress(ctx context.Context, runID string, completedCases int, results []evaluation.CaseResult) error
	CompleteEvalRun(ctx context.Context, report evaluation.Report) error
	FinishEvalRun(ctx context.Context, runID string, status evaluation.Status, message string, completedAt time.Time) error
	GetEvalRun(ctx context.Context, runID string) (evaluation.Run, error)
	GetEvalRunConfig(ctx context.Context, runID string) (evaluation.RunConfig, error)
	ListEvalCaseResults(ctx context.Context, runID string) ([]evaluation.CaseResult, error)
	ListEvalRuns(ctx context.Context, limit int) ([]evaluation.Run, error)
	GetEvalReport(ctx context.Context, runID string) (evaluation.Report, error)
}

type runControl struct {
	cancel context.CancelCauseFunc
	done   chan struct{}
}

// PluginResolver returns an already installed, trusted plugin package.
type PluginResolver interface {
	Get(ctx context.Context, pluginID string) (plugin.Installed, error)
}

// Config fixes service-owned storage, concurrency, and agent runtime limits.
type Config struct {
	DataRoot string
	EvalRoot string
	// EvalRootHandle is a stable, pre-opened evaluation root. The caller keeps
	// ownership and must keep it open until Service.Close returns.
	EvalRootHandle *os.Root
	Store          Store
	PluginSources  map[string]string
	PluginResolver PluginResolver
	// WritableRoots are host workspaces writable by a primary agent. When
	// evaluation is enabled, neither trusted suite inputs nor frozen run data
	// may overlap any of these roots.
	WritableRoots []string
	MaxActive     int
	MaxTurns      int
	MaxToolCalls  int
	Logger        *slog.Logger
}

// StartInput selects one local suite and optional subset of its variants.
type StartInput struct {
	SuitePath string   `json:"suite_path"`
	Variants  []string `json:"variants,omitempty"`
}

// Service runs evaluations outside request lifetimes and persists every state.
type Service struct {
	root           string
	inputRoot      string
	workspaceRoot  string
	runtimeRoot    string
	evalRoot       string
	evalRootHandle *os.Root
	ownsEvalRoot   bool
	store          Store
	pluginSources  map[string]string
	pluginResolver PluginResolver
	maxTurns       int
	maxToolCalls   int
	logger         *slog.Logger
	ctx            context.Context
	cancel         context.CancelFunc
	sem            chan struct{}
	lifecycleMu    sync.Mutex
	closed         bool
	closeOnce      sync.Once
	closeDone      chan struct{}
	mu             sync.Mutex
	controls       map[string]*runControl
	wg             sync.WaitGroup
}

// New creates a service and marks runs interrupted by an earlier process as failed.
func New(ctx context.Context, config Config) (*Service, error) {
	if config.Store == nil || strings.TrimSpace(config.DataRoot) == "" {
		return nil, errors.New("evalservice: data root and store are required")
	}
	absolute, err := filepath.Abs(config.DataRoot)
	if err != nil {
		return nil, fmt.Errorf("evalservice: resolving data root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("evalservice: creating data root: %w", err)
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("evalservice: resolving data root links: %w", err)
	}
	if err := os.Chmod(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("evalservice: restricting data root: %w", err)
	}
	evalRoot := ""
	evalRootHandle := config.EvalRootHandle
	ownsEvalRoot := false
	if evalRootHandle != nil {
		evalRoot = evalRootHandle.Name()
	} else if strings.TrimSpace(config.EvalRoot) != "" {
		evalRoot, err = filepath.Abs(config.EvalRoot)
		if err != nil {
			return nil, fmt.Errorf("evalservice: resolving evaluation root: %w", err)
		}
		evalRootHandle, err = os.OpenRoot(evalRoot)
		if err != nil {
			return nil, fmt.Errorf("evalservice: opening evaluation root: %w", err)
		}
		ownsEvalRoot = true
	}
	if evalRootHandle != nil {
		if err := requireSeparatedRoots(evalRootHandle, absolute); err != nil {
			if ownsEvalRoot {
				_ = evalRootHandle.Close()
			}
			return nil, err
		}
		if err := requireWritableRootSeparation(evalRootHandle, absolute, config.WritableRoots); err != nil {
			if ownsEvalRoot {
				_ = evalRootHandle.Close()
			}
			return nil, err
		}
	}
	inputRoot := filepath.Join(absolute, "inputs")
	workspaceRoot := filepath.Join(absolute, "workspaces")
	runtimeRoot := filepath.Join(absolute, "runtime")
	for _, directory := range []string{inputRoot, workspaceRoot, runtimeRoot} {
		if err := ensurePrivateDirectory(directory); err != nil {
			if ownsEvalRoot {
				_ = evalRootHandle.Close()
			}
			return nil, err
		}
	}
	if err := requirePairwiseSeparated(inputRoot, workspaceRoot, runtimeRoot); err != nil {
		if ownsEvalRoot {
			_ = evalRootHandle.Close()
		}
		return nil, err
	}
	if config.MaxActive < 1 {
		config.MaxActive = 1
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	serviceCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	service := &Service{
		root: absolute, inputRoot: inputRoot, workspaceRoot: workspaceRoot, runtimeRoot: runtimeRoot,
		evalRoot: evalRoot, evalRootHandle: evalRootHandle, ownsEvalRoot: ownsEvalRoot,
		store:          config.Store,
		pluginSources:  copySources(config.PluginSources),
		pluginResolver: config.PluginResolver,
		maxTurns:       config.MaxTurns, maxToolCalls: config.MaxToolCalls,
		logger: config.Logger, ctx: serviceCtx, cancel: cancel,
		sem: make(chan struct{}, config.MaxActive), closeDone: make(chan struct{}),
		controls: make(map[string]*runControl),
	}
	if err := service.recoverInterrupted(ctx); err != nil {
		cancel()
		if service.ownsEvalRoot {
			_ = service.evalRootHandle.Close()
		}
		return nil, err
	}
	return service, nil
}

// Close cancels active runs and waits until their terminal state is persisted.
func (s *Service) Close() {
	s.closeOnce.Do(func() {
		s.lifecycleMu.Lock()
		s.closed = true
		s.cancel()
		s.mu.Lock()
		for _, control := range s.controls {
			control.cancel(context.Canceled)
		}
		s.mu.Unlock()
		s.lifecycleMu.Unlock()

		s.wg.Wait()
		if s.ownsEvalRoot && s.evalRootHandle != nil {
			_ = s.evalRootHandle.Close()
			s.evalRootHandle = nil
		}
		close(s.closeDone)
	})
	<-s.closeDone
}

// Start validates and durably queues a new asynchronous evaluation.
func (s *Service) Start(ctx context.Context, input StartInput) (evaluation.Run, error) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.closed {
		return evaluation.Run{}, ErrClosed
	}
	input.SuitePath = strings.TrimSpace(input.SuitePath)
	if input.SuitePath == "" || len(input.SuitePath) > 4_096 {
		return evaluation.Run{}, errors.New("evalservice: suite_path is required")
	}
	if s.evalRootHandle == nil {
		return evaluation.Run{}, errors.New("evalservice: evaluation root is not configured")
	}
	suite, err := evaluation.LoadFromRootHandle(s.evalRootHandle, input.SuitePath)
	if err != nil {
		return evaluation.Run{}, err
	}
	variants, err := selectedVariantIDs(suite, input.Variants)
	if err != nil {
		return evaluation.Run{}, err
	}
	runner, err := s.prepareRunner(ctx, &suite, variants)
	if err != nil {
		return evaluation.Run{}, err
	}
	runID, err := id.New()
	if err != nil {
		return evaluation.Run{}, err
	}
	snapshotPath := filepath.Join(s.inputRoot, runID)
	snapshot, err := evaluation.Snapshot(
		ctx,
		suite,
		snapshotPath,
	)
	if err != nil {
		return evaluation.Run{}, err
	}
	closeSnapshot := true
	defer func() {
		if closeSnapshot {
			_ = snapshot.Close()
			_ = os.RemoveAll(snapshotPath)
		}
	}()
	configDigest, _, err := evaluation.PrepareRunContext(ctx, snapshot, variants)
	if err != nil {
		return evaluation.Run{}, err
	}
	now := time.Now().UTC()
	run := evaluation.Run{
		SchemaVersion: evaluation.SchemaVersion,
		ID:            runID, SuiteID: suite.ID, SuiteName: suite.Name, SuiteVersion: suite.Version,
		Status: evaluation.StatusQueued, Variants: variants, ConfigDigest: configDigest,
		CaseCount: len(suite.Cases) * len(variants), CreatedAt: now,
	}
	config := evaluation.RunConfig{SuitePath: input.SuitePath, Variants: variants}
	if err := s.store.UpsertEvalSuite(ctx, snapshot); err != nil {
		return evaluation.Run{}, err
	}
	if err := s.store.CreateEvalRun(ctx, run, config); err != nil {
		return evaluation.Run{}, err
	}

	s.launch(run, snapshot, variants, runner, nil)
	closeSnapshot = false
	return run, nil
}

// Pause cooperatively stops active workers after preserving all terminal jobs.
func (s *Service) Pause(ctx context.Context, runID string) (evaluation.Run, error) {
	current, err := s.store.GetEvalRun(ctx, runID)
	if err != nil || current.Status == evaluation.StatusPaused {
		return current, err
	}
	if current.Status.Terminal() {
		return evaluation.Run{}, fmt.Errorf("evalservice: cannot pause %s evaluation", current.Status)
	}
	control := s.control(runID)
	if control == nil {
		if err := s.store.PauseEvalRun(ctx, runID, errPausedByUser.Error()); err != nil {
			return evaluation.Run{}, err
		}
		return s.store.GetEvalRun(ctx, runID)
	}
	control.cancel(errPausedByUser)
	if err := waitControl(ctx, control); err != nil {
		return evaluation.Run{}, err
	}
	return s.store.GetEvalRun(ctx, runID)
}

// Resume validates the original execution identity and queues only jobs that
// do not already have atomically persisted results.
func (s *Service) Resume(ctx context.Context, runID string) (evaluation.Run, error) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.closed {
		return evaluation.Run{}, ErrClosed
	}
	if s.evalRootHandle == nil {
		return evaluation.Run{}, errors.New("evalservice: evaluation root is not configured")
	}
	current, err := s.store.GetEvalRun(ctx, runID)
	if err != nil {
		return evaluation.Run{}, err
	}
	if current.Status != evaluation.StatusPaused {
		return evaluation.Run{}, fmt.Errorf("evalservice: cannot resume %s evaluation", current.Status)
	}
	config, err := s.store.GetEvalRunConfig(ctx, runID)
	if err != nil {
		return evaluation.Run{}, err
	}
	prior, err := s.store.ListEvalCaseResults(ctx, runID)
	if err != nil {
		return evaluation.Run{}, err
	}
	suite, err := evaluation.LoadFromRoot(filepath.Join(s.inputRoot, runID), "suite.json")
	if err != nil {
		return evaluation.Run{}, err
	}
	closeSnapshot := true
	defer func() {
		if closeSnapshot {
			_ = suite.Close()
		}
	}()
	if suite.ID != current.SuiteID || suite.Version != current.SuiteVersion {
		return evaluation.Run{}, errors.New("evalservice: suite identity changed while paused")
	}
	runner, err := s.prepareRunner(ctx, &suite, config.Variants)
	if err != nil {
		return evaluation.Run{}, err
	}
	digest, _, err := evaluation.PrepareRunContext(ctx, suite, config.Variants)
	if err != nil {
		return evaluation.Run{}, err
	}
	if current.ConfigDigest == "" || digest != current.ConfigDigest {
		return evaluation.Run{}, errors.New("evalservice: suite, model, plugin, or fixture changed while paused")
	}
	if err := s.store.ResumeEvalRun(ctx, runID); err != nil {
		return evaluation.Run{}, err
	}
	current.Status = evaluation.StatusQueued
	current.ErrorMessage = ""
	s.launch(current, suite, config.Variants, runner, prior)
	closeSnapshot = false
	return s.store.GetEvalRun(ctx, runID)
}

// Cancel requests cancellation and returns the latest durable snapshot.
func (s *Service) Cancel(ctx context.Context, runID string) (evaluation.Run, error) {
	current, err := s.store.GetEvalRun(ctx, runID)
	if err != nil || current.Status.Terminal() {
		return current, err
	}
	control := s.control(runID)
	if control != nil {
		control.cancel(errCancelledByUser)
		if err := waitControl(ctx, control); err != nil {
			return evaluation.Run{}, err
		}
	} else if err := s.store.FinishEvalRun(ctx, runID, evaluation.StatusCancelled, errCancelledByUser.Error(), time.Now().UTC()); err != nil {
		return evaluation.Run{}, err
	}
	return s.store.GetEvalRun(ctx, runID)
}

func (s *Service) Get(ctx context.Context, runID string) (evaluation.Run, error) {
	return s.store.GetEvalRun(ctx, runID)
}

func (s *Service) List(ctx context.Context, limit int) ([]evaluation.Run, error) {
	return s.store.ListEvalRuns(ctx, limit)
}

func (s *Service) Report(ctx context.Context, runID string) (evaluation.Report, error) {
	return s.store.GetEvalReport(ctx, runID)
}

func (s *Service) execute(
	ctx context.Context,
	run evaluation.Run,
	suite evaluation.Suite,
	variants []string,
	runner *evaluation.Runner,
	prior []evaluation.CaseResult,
	control *runControl,
) {
	defer s.wg.Done()
	defer suite.Close()
	defer func() {
		s.mu.Lock()
		if s.controls[run.ID] == control {
			delete(s.controls, run.ID)
		}
		s.mu.Unlock()
		close(control.done)
	}()
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		s.finishStopped(run.ID, context.Cause(ctx))
		return
	}
	startedAt := time.Now().UTC()
	if err := s.store.StartEvalRun(context.WithoutCancel(ctx), run.ID, startedAt); err != nil {
		s.logger.Error("evaluation start persistence failed", "run_id", run.ID, "error", err)
		return
	}
	if run.StartedAt != nil {
		startedAt = *run.StartedAt
	}
	report, err := runner.RunWithIDProgressFrom(ctx, run.ID, suite, variants, prior, startedAt, func(
		_ context.Context,
		progress evaluation.Progress,
	) error {
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return s.store.RecordEvalProgress(
			persistCtx,
			progress.RunID,
			progress.CompletedCases,
			progress.Results,
		)
	})
	if err != nil {
		if ctx.Err() != nil {
			s.finishStopped(run.ID, context.Cause(ctx))
			return
		}
		if finishErr := s.store.FinishEvalRun(
			context.WithoutCancel(ctx), run.ID, evaluation.StatusFailed, err.Error(), time.Now().UTC(),
		); finishErr != nil {
			s.logger.Error("evaluation failure persistence failed", "run_id", run.ID, "error", finishErr)
		}
		return
	}
	if report.ConfigDigest != run.ConfigDigest {
		if finishErr := s.store.FinishEvalRun(
			context.WithoutCancel(ctx),
			run.ID,
			evaluation.StatusFailed,
			"evaluation input snapshot changed during execution",
			time.Now().UTC(),
		); finishErr != nil {
			s.logger.Error("evaluation snapshot mismatch persistence failed", "run_id", run.ID, "error", finishErr)
		}
		return
	}
	if err := s.store.CompleteEvalRun(context.WithoutCancel(ctx), report); err != nil {
		s.logger.Error("evaluation report persistence failed", "run_id", run.ID, "error", err)
	}
}

func (s *Service) launch(
	run evaluation.Run,
	suite evaluation.Suite,
	variants []string,
	runner *evaluation.Runner,
	prior []evaluation.CaseResult,
) {
	runCtx, cancel := context.WithCancelCause(s.ctx)
	control := &runControl{cancel: cancel, done: make(chan struct{})}
	s.mu.Lock()
	s.controls[run.ID] = control
	s.mu.Unlock()
	s.wg.Add(1)
	go s.execute(runCtx, run, suite, variants, runner, prior, control)
}

func (s *Service) control(runID string) *runControl {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.controls[runID]
}

func waitControl(ctx context.Context, control *runControl) error {
	select {
	case <-control.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) finishStopped(runID string, cause error) {
	if errors.Is(cause, errPausedByUser) {
		if err := s.store.PauseEvalRun(context.Background(), runID, errPausedByUser.Error()); err != nil &&
			!errors.Is(err, evaluation.ErrRunNotFound) {
			s.logger.Error("evaluation pause persistence failed", "run_id", runID, "error", err)
		}
		return
	}
	s.finishCancelled(runID, cause)
}

func (s *Service) finishCancelled(runID string, cause error) {
	message := errCancelledByUser.Error()
	if cause != nil && !errors.Is(cause, context.Canceled) {
		message = cause.Error()
	}
	if err := s.store.FinishEvalRun(
		context.Background(), runID, evaluation.StatusCancelled, message, time.Now().UTC(),
	); err != nil && !errors.Is(err, evaluation.ErrRunNotFound) {
		s.logger.Error("evaluation cancellation persistence failed", "run_id", runID, "error", err)
	}
}

func (s *Service) prepareRunner(
	ctx context.Context,
	suite *evaluation.Suite,
	variants []string,
) (*evaluation.Runner, error) {
	sources, err := s.resolveSources(ctx, *suite, variants)
	if err != nil {
		return nil, err
	}
	if err := evalrunner.EnrichSuite(suite, variants, sources); err != nil {
		return nil, err
	}
	agent, err := evalrunner.NewKernAgent(evalrunner.Config{
		DataRoot: s.runtimeRoot, PluginSources: sources,
		MaxTurns: s.maxTurns, MaxToolCalls: s.maxToolCalls, Logger: s.logger,
	})
	if err != nil {
		return nil, err
	}
	var judge evaluation.ModelJudge
	if suite.Defaults.Judge != nil {
		judge, err = evalrunner.NewCompatibleJudge(*suite.Defaults.Judge)
		if err != nil {
			return nil, err
		}
	}
	return evaluation.NewRunnerWithJudge(s.workspaceRoot, agent, judge, s.logger)
}

func (s *Service) recoverInterrupted(ctx context.Context) error {
	runs, err := s.store.ListEvalRuns(ctx, 200)
	if err != nil {
		return fmt.Errorf("evalservice: listing interrupted runs: %w", err)
	}
	for _, run := range runs {
		if run.Status != evaluation.StatusQueued && run.Status != evaluation.StatusRunning {
			continue
		}
		if err := s.store.FinishEvalRun(
			ctx, run.ID, evaluation.StatusFailed, "evaluation interrupted by process restart", time.Now().UTC(),
		); err != nil {
			return fmt.Errorf("evalservice: recovering run %s: %w", run.ID, err)
		}
	}
	return nil
}

func (s *Service) resolveSources(
	ctx context.Context,
	suite evaluation.Suite,
	selected []string,
) (map[string]string, error) {
	sources := copySources(s.pluginSources)
	wanted := make(map[string]bool, len(selected))
	for _, variantID := range selected {
		wanted[variantID] = true
	}
	for _, variant := range suite.Variants {
		if !wanted[variant.ID] {
			continue
		}
		for _, reference := range variant.Plugins {
			pluginID, _, _ := strings.Cut(reference, "@")
			if sources[pluginID] == "" && s.pluginResolver != nil {
				installed, err := s.pluginResolver.Get(ctx, pluginID)
				if err != nil && !errors.Is(err, plugin.ErrNotFound) {
					return nil, fmt.Errorf("evalservice: resolving installed plugin %s: %w", pluginID, err)
				}
				if err == nil {
					sources[pluginID] = strings.TrimSpace(installed.InstallPath)
				}
			}
			if sources[pluginID] == "" {
				return nil, fmt.Errorf("evalservice: no trusted source configured for plugin %s", reference)
			}
		}
	}
	return sources, nil
}

func selectedVariantIDs(suite evaluation.Suite, selected []string) ([]string, error) {
	if len(selected) == 0 {
		result := make([]string, 0, len(suite.Variants))
		for _, variant := range suite.Variants {
			result = append(result, variant.ID)
		}
		return result, nil
	}
	wanted := make(map[string]bool, len(selected))
	for _, variantID := range selected {
		variantID = strings.TrimSpace(variantID)
		if variantID == "" || wanted[variantID] {
			return nil, fmt.Errorf("%w: invalid or duplicate selected variant", evaluation.ErrInvalidSuite)
		}
		wanted[variantID] = true
	}
	result := make([]string, 0, len(selected))
	for _, variant := range suite.Variants {
		if wanted[variant.ID] {
			result = append(result, variant.ID)
			delete(wanted, variant.ID)
		}
	}
	if len(wanted) != 0 {
		return nil, fmt.Errorf("%w: selected variant does not exist", evaluation.ErrInvalidSuite)
	}
	return result, nil
}

func copySources(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for pluginID, directory := range source {
		result[pluginID] = directory
	}
	return result
}

func ensurePrivateDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return fmt.Errorf("evalservice: creating private directory: %w", err)
		}
		info, err = os.Lstat(directory)
	}
	if err != nil {
		return fmt.Errorf("evalservice: inspecting private directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("evalservice: evaluation storage contains an unsafe directory")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("evalservice: restricting private directory: %w", err)
	}
	return nil
}

func requireSeparatedRoots(evalRoot *os.Root, dataRoot string) error {
	evalPath, err := stableRootPath(evalRoot)
	if err != nil {
		return err
	}
	overlap, err := rootsOverlap(evalPath, dataRoot)
	if err != nil {
		return err
	}
	if overlap {
		return errors.New("evalservice: evaluation and data roots must not overlap")
	}
	return nil
}

func requireWritableRootSeparation(evalRoot *os.Root, dataRoot string, writableRoots []string) error {
	evalPath, err := stableRootPath(evalRoot)
	if err != nil {
		return err
	}
	for _, writableRoot := range writableRoots {
		writableRoot = strings.TrimSpace(writableRoot)
		if writableRoot == "" {
			return errors.New("evalservice: writable root is required")
		}
		absolute, err := filepath.Abs(writableRoot)
		if err != nil {
			return fmt.Errorf("evalservice: resolving writable root: %w", err)
		}
		overlap, err := rootsOverlap(evalPath, absolute)
		if err != nil {
			return fmt.Errorf("evalservice: comparing evaluation and writable roots: %w", err)
		}
		if overlap {
			return errors.New("evalservice: evaluation root and writable agent roots must not overlap")
		}
		overlap, err = rootsOverlap(dataRoot, absolute)
		if err != nil {
			return fmt.Errorf("evalservice: comparing data and writable roots: %w", err)
		}
		if overlap {
			return errors.New("evalservice: data root and writable agent roots must not overlap")
		}
	}
	return nil
}

func stableRootPath(root *os.Root) (string, error) {
	rootInfo, err := root.Stat(".")
	if err != nil {
		return "", fmt.Errorf("evalservice: inspecting evaluation root: %w", err)
	}
	rootPath, err := filepath.EvalSymlinks(root.Name())
	if err != nil {
		return "", fmt.Errorf("evalservice: resolving evaluation root links: %w", err)
	}
	pathInfo, err := os.Stat(rootPath)
	if err != nil {
		return "", fmt.Errorf("evalservice: inspecting evaluation root path: %w", err)
	}
	if !os.SameFile(rootInfo, pathInfo) {
		return "", errors.New("evalservice: evaluation root identity changed before initialization")
	}
	return rootPath, nil
}

func requirePairwiseSeparated(directories ...string) error {
	for left := range directories {
		for right := left + 1; right < len(directories); right++ {
			overlap, err := rootsOverlap(directories[left], directories[right])
			if err != nil {
				return err
			}
			if overlap {
				return errors.New("evalservice: snapshot, workspace, and runtime roots must be separate")
			}
		}
	}
	return nil
}

func rootsOverlap(left, right string) (bool, error) {
	leftPath, err := filepath.EvalSymlinks(left)
	if err != nil {
		return false, fmt.Errorf("evalservice: resolving root links: %w", err)
	}
	rightPath, err := filepath.EvalSymlinks(right)
	if err != nil {
		return false, fmt.Errorf("evalservice: resolving root links: %w", err)
	}
	leftInfo, err := os.Stat(leftPath)
	if err != nil {
		return false, fmt.Errorf("evalservice: inspecting root: %w", err)
	}
	rightInfo, err := os.Stat(rightPath)
	if err != nil {
		return false, fmt.Errorf("evalservice: inspecting root: %w", err)
	}
	if os.SameFile(leftInfo, rightInfo) || pathContains(leftPath, rightPath) || pathContains(rightPath, leftPath) {
		return true, nil
	}
	return ancestorHasIdentity(leftPath, rightInfo) || ancestorHasIdentity(rightPath, leftInfo), nil
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && (relative == "." || filepath.IsLocal(relative))
}

func ancestorHasIdentity(start string, target os.FileInfo) bool {
	for current := filepath.Clean(start); ; current = filepath.Dir(current) {
		if info, err := os.Stat(current); err == nil && os.SameFile(info, target) {
			return true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
	}
}
