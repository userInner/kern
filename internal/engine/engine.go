// Package engine coordinates durable task execution.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/userInner/kern/internal/id"
	"github.com/userInner/kern/internal/plan"
	"github.com/userInner/kern/internal/planner"
	"github.com/userInner/kern/internal/task"
	"github.com/userInner/kern/internal/tracecontext"
	"github.com/userInner/kern/internal/verification"
)

var ErrQueueFull = errors.New("engine: task queue is full")

const defaultLeaseTTL = 30 * time.Second

type taskStore interface {
	GetTask(ctx context.Context, taskID string) (task.Task, error)
	Transition(
		ctx context.Context,
		taskID string,
		to task.Status,
		result string,
		errorMessage string,
	) error
	AppendEvent(
		ctx context.Context,
		taskID string,
		attemptID string,
		eventType string,
		payload any,
	) error
	RecordVerification(ctx context.Context, verification task.Verification) error
	CreatePlan(ctx context.Context, taskID string, draft plan.Draft) (plan.Plan, error)
	TransitionPlanStep(
		ctx context.Context,
		stepID string,
		to plan.StepStatus,
		failure string,
	) (plan.Plan, error)
	AcquireLease(ctx context.Context, taskID, owner string, ttl time.Duration) error
	RenewLease(ctx context.Context, taskID, owner string, ttl time.Duration) error
	ReleaseLease(ctx context.Context, taskID, owner string) error
	SaveCheckpoint(
		ctx context.Context,
		taskID string,
		reason string,
		state any,
	) (task.Checkpoint, error)
}

type processor interface {
	Process(ctx context.Context, item task.Task) (string, error)
}

type verificationRunner interface {
	Verify(ctx context.Context, item task.Task, result string) (verification.Report, error)
}

type runningExecution struct {
	owner  string
	cancel context.CancelCauseFunc
}

// Engine owns a bounded worker queue and the task lifecycle.
type Engine struct {
	store     taskStore
	processor processor
	verifier  verificationRunner
	logger    *slog.Logger
	queue     chan string
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	owner     string
	leaseTTL  time.Duration
	runSeq    atomic.Uint64
	runningMu sync.Mutex
	running   map[string]runningExecution
}

// New starts a bounded task engine tied to parent.
func New(
	parent context.Context,
	store taskStore,
	processor processor,
	logger *slog.Logger,
	workerCount int,
) *Engine {
	return NewWithVerifier(
		parent,
		store,
		processor,
		resultVerifier{},
		logger,
		workerCount,
	)
}

// NewWithVerifier starts an engine with an independently composed verifier
// suite. New remains available for narrow engine tests and embedders that only
// require the deterministic non-empty-result baseline.
func NewWithVerifier(
	parent context.Context,
	store taskStore,
	processor processor,
	verifier verificationRunner,
	logger *slog.Logger,
	workerCount int,
) *Engine {
	if workerCount < 1 {
		workerCount = 1
	}
	if verifier == nil {
		verifier = resultVerifier{}
	}
	ctx, cancel := context.WithCancel(parent)
	engine := &Engine{
		store:     store,
		processor: processor,
		verifier:  verifier,
		logger:    logger,
		queue:     make(chan string, workerCount*8),
		cancel:    cancel,
		owner:     fmt.Sprintf("kern-%d-%d", os.Getpid(), time.Now().UnixNano()),
		leaseTTL:  defaultLeaseTTL,
		running:   make(map[string]runningExecution),
	}
	for range workerCount {
		engine.wg.Add(1)
		go engine.worker(ctx)
	}
	return engine
}

// CancelExecution asks a currently running task to stop at its next cancellation boundary.
// Durable task state must be changed by the caller before invoking this method.
func (e *Engine) CancelExecution(taskID string, cause error) bool {
	e.runningMu.Lock()
	execution, ok := e.running[taskID]
	e.runningMu.Unlock()
	if !ok {
		return false
	}
	execution.cancel(cause)
	return true
}

// Enqueue schedules an existing task for execution.
func (e *Engine) Enqueue(ctx context.Context, taskID string) error {
	select {
	case e.queue <- taskID:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return ErrQueueFull
	}
}

// Close stops workers and waits for their current work to observe cancellation.
func (e *Engine) Close() {
	e.cancel()
	e.wg.Wait()
}

func (e *Engine) worker(ctx context.Context) {
	defer e.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case taskID := <-e.queue:
			if err := e.run(ctx, taskID); err != nil && ctx.Err() == nil {
				e.logger.ErrorContext(ctx, "task execution failed", "task_id", taskID, "error", err)
			}
		}
	}
}

func (e *Engine) run(parent context.Context, taskID string) (runErr error) {
	runOwner := fmt.Sprintf("%s-%d", e.owner, e.runSeq.Add(1))
	if err := e.store.AcquireLease(parent, taskID, runOwner, e.leaseTTL); err != nil {
		return err
	}
	ctx, cancel := context.WithCancelCause(parent)
	e.registerExecution(taskID, runOwner, cancel)
	defer func() {
		cancel(nil)
		e.unregisterExecution(taskID, runOwner)
		releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(parent), 2*time.Second)
		defer releaseCancel()
		if err := e.store.ReleaseLease(releaseCtx, taskID, runOwner); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}()

	heartbeatDone := make(chan struct{})
	go e.heartbeat(ctx, cancel, taskID, runOwner, heartbeatDone)
	defer func() {
		cancel(nil)
		<-heartbeatDone
	}()

	item, err := e.store.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("loading task: %w", err)
	}
	if item.Status != task.StatusCreated {
		return fmt.Errorf("starting task in status %s: %w", item.Status, task.ErrInvalidTransition)
	}
	traceID := tracecontext.AttemptTraceID(item.ActiveAttemptID)
	ctx = tracecontext.With(ctx, tracecontext.Identity{
		TraceID: traceID,
		SpanID:  tracecontext.AttemptSpanID(item.ActiveAttemptID),
	})
	startedAt := time.Now()
	e.logger.InfoContext(
		ctx,
		"task attempt started",
		"task_id", item.ID,
		"attempt_id", item.ActiveAttemptID,
		"trace_id", traceID,
	)
	defer func() {
		e.logger.InfoContext(
			context.WithoutCancel(ctx),
			"task attempt finished",
			"task_id", item.ID,
			"attempt_id", item.ActiveAttemptID,
			"trace_id", traceID,
			"duration_ms", time.Since(startedAt).Milliseconds(),
			"error", runErr,
		)
	}()

	if err := e.store.Transition(ctx, taskID, task.StatusPlanning, "", ""); err != nil {
		return fmt.Errorf("starting planning: %w", err)
	}
	decision := planner.Build(item)
	var activePlan *plan.Plan
	if decision.Explicit {
		createdPlan, err := e.store.CreatePlan(ctx, taskID, decision.Draft)
		if err != nil {
			return e.fail(ctx, item, fmt.Errorf("persisting plan: %w", err))
		}
		activePlan = &createdPlan
		if err := e.transitionPlanPhase(ctx, activePlan, plan.PhasePrepare, plan.StepStatusRunning, ""); err != nil {
			return e.fail(ctx, item, err)
		}
		if err := e.transitionPlanPhase(ctx, activePlan, plan.PhasePrepare, plan.StepStatusCompleted, ""); err != nil {
			return e.fail(ctx, item, err)
		}
	} else if err := e.store.AppendEvent(
		ctx,
		taskID,
		item.ActiveAttemptID,
		"task.plan_skipped",
		map[string]string{"reason": decision.Reason},
	); err != nil {
		return e.fail(ctx, item, fmt.Errorf("persisting plan decision: %w", err))
	}
	if _, err := e.store.SaveCheckpoint(ctx, taskID, "plan persisted", map[string]any{
		"phase":         "planning",
		"explicit_plan": decision.Explicit,
		"plan":          activePlan,
		"reason":        decision.Reason,
	}); err != nil {
		return e.fail(ctx, item, fmt.Errorf("checkpointing plan: %w", err))
	}
	if err := e.store.Transition(ctx, taskID, task.StatusRunning, "", ""); err != nil {
		return e.fail(ctx, item, fmt.Errorf("starting execution: %w", err))
	}
	if activePlan != nil {
		if err := e.transitionPlanPhase(ctx, activePlan, plan.PhaseExecute, plan.StepStatusRunning, ""); err != nil {
			return e.fail(ctx, item, err)
		}
	}

	result, err := e.processor.Process(ctx, item)
	if err != nil {
		if parent.Err() != nil {
			return fmt.Errorf("processing task during engine shutdown: %w", err)
		}
		if activePlan != nil {
			if planErr := e.transitionPlanPhase(
				ctx,
				activePlan,
				plan.PhaseExecute,
				plan.StepStatusFailed,
				boundedFailure(err),
			); planErr != nil {
				err = errors.Join(err, planErr)
			}
		}
		return e.fail(ctx, item, fmt.Errorf("processing task: %w", err))
	}
	if activePlan != nil {
		if err := e.transitionPlanPhase(ctx, activePlan, plan.PhaseExecute, plan.StepStatusCompleted, ""); err != nil {
			return e.fail(ctx, item, err)
		}
	}
	if err := e.store.AppendEvent(
		ctx,
		taskID,
		item.ActiveAttemptID,
		"message.completed",
		map[string]string{"role": "assistant", "content": result},
	); err != nil {
		return e.fail(ctx, item, fmt.Errorf("persisting result message: %w", err))
	}
	if _, err := e.store.SaveCheckpoint(ctx, taskID, "processor result persisted", map[string]any{
		"phase":  "running",
		"result": result,
	}); err != nil {
		return e.fail(ctx, item, fmt.Errorf("checkpointing processor result: %w", err))
	}
	if err := e.store.Transition(ctx, taskID, task.StatusVerifying, result, ""); err != nil {
		return e.fail(ctx, item, fmt.Errorf("starting verification: %w", err))
	}
	if activePlan != nil {
		if err := e.transitionPlanPhase(ctx, activePlan, plan.PhaseVerify, plan.StepStatusRunning, ""); err != nil {
			return e.fail(ctx, item, err)
		}
	}
	if err := e.store.AppendEvent(
		ctx,
		taskID,
		item.ActiveAttemptID,
		"verification.started",
		map[string]string{"suite": "core"},
	); err != nil {
		return e.fail(ctx, item, fmt.Errorf("persisting verification start: %w", err))
	}

	report, err := e.verifier.Verify(ctx, item, result)
	if err != nil {
		return e.fail(ctx, item, fmt.Errorf("running deterministic verification: %w", err))
	}
	if err := verification.Validate(report); err != nil {
		return e.fail(ctx, item, err)
	}
	report.Status = verification.Aggregate(report.Checks)
	if report.CreatedAt.IsZero() {
		report.CreatedAt = time.Now().UTC()
	}
	verificationIDs, err := e.recordVerificationReport(ctx, item, report)
	if err != nil {
		return e.fail(ctx, item, err)
	}
	if report.Status == verification.StatusFailed {
		if activePlan != nil {
			if planErr := e.transitionPlanPhase(
				ctx,
				activePlan,
				plan.PhaseVerify,
				plan.StepStatusFailed,
				verificationFailure(report),
			); planErr != nil {
				return e.fail(ctx, item, errors.Join(errors.New(verificationFailure(report)), planErr))
			}
		}
		return e.fail(ctx, item, errors.New(verificationFailure(report)))
	}
	if activePlan != nil {
		if err := e.transitionPlanPhase(ctx, activePlan, plan.PhaseVerify, plan.StepStatusCompleted, ""); err != nil {
			return e.fail(ctx, item, err)
		}
	}
	if _, err := e.store.SaveCheckpoint(ctx, taskID, "verification completed", map[string]any{
		"phase":            "verifying",
		"verification_ids": verificationIDs,
		"status":           report.Status,
	}); err != nil {
		return e.fail(ctx, item, fmt.Errorf("checkpointing verification: %w", err))
	}
	finalStatus := task.StatusCompleted
	if report.Status != verification.StatusVerified {
		finalStatus = task.StatusPartiallyCompleted
	}
	if err := e.store.Transition(ctx, taskID, finalStatus, result, ""); err != nil {
		return fmt.Errorf("completing task: %w", err)
	}
	return nil
}

func (e *Engine) transitionPlanPhase(
	ctx context.Context,
	activePlan *plan.Plan,
	phase plan.Phase,
	to plan.StepStatus,
	failure string,
) error {
	for _, step := range activePlan.Steps {
		if step.Phase != phase {
			continue
		}
		updated, err := e.store.TransitionPlanStep(ctx, step.ID, to, failure)
		if err != nil {
			return fmt.Errorf("transitioning %s plan step to %s: %w", phase, to, err)
		}
		*activePlan = updated
		return nil
	}
	return fmt.Errorf("plan: missing %s phase step", phase)
}

func boundedFailure(err error) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	if len(value) > 512 {
		return value[:512]
	}
	return value
}

func (e *Engine) fail(ctx context.Context, item task.Task, cause error) error {
	current, err := e.store.GetTask(ctx, item.ID)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("loading task for failure: %w", err))
	}
	if current.Status.IsTerminal() || current.Status == task.StatusWaitingInput ||
		current.Status == task.StatusWaitingApproval {
		return nil
	}
	if err := e.store.Transition(
		ctx,
		item.ID,
		task.StatusFailed,
		current.Result,
		cause.Error(),
	); err != nil {
		return errors.Join(cause, fmt.Errorf("recording task failure: %w", err))
	}
	return cause
}

func (e *Engine) registerExecution(
	taskID string,
	owner string,
	cancel context.CancelCauseFunc,
) {
	e.runningMu.Lock()
	defer e.runningMu.Unlock()
	e.running[taskID] = runningExecution{owner: owner, cancel: cancel}
}

func (e *Engine) unregisterExecution(taskID, owner string) {
	e.runningMu.Lock()
	defer e.runningMu.Unlock()
	if current, ok := e.running[taskID]; ok && current.owner == owner {
		delete(e.running, taskID)
	}
}

func (e *Engine) heartbeat(
	ctx context.Context,
	cancel context.CancelCauseFunc,
	taskID string,
	owner string,
	done chan<- struct{},
) {
	defer close(done)
	interval := e.leaseTTL / 3
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := e.store.RenewLease(ctx, taskID, owner, e.leaseTTL); err != nil {
				cancel(fmt.Errorf("renewing execution lease: %w", err))
				return
			}
		}
	}
}

func (e *Engine) recordVerificationReport(
	ctx context.Context,
	item task.Task,
	report verification.Report,
) ([]string, error) {
	ids := make([]string, 0, len(report.Checks)+1)
	for _, check := range report.Checks {
		verificationID, err := id.New()
		if err != nil {
			return nil, err
		}
		evidence, err := json.Marshal(map[string]any{
			"required": check.Required,
			"summary":  check.Summary,
			"evidence": check.Evidence,
		})
		if err != nil {
			return nil, fmt.Errorf("encoding %s verification evidence: %w", check.Verifier, err)
		}
		if err := e.store.RecordVerification(ctx, task.Verification{
			ID:        verificationID,
			TaskID:    item.ID,
			AttemptID: item.ActiveAttemptID,
			Verifier:  check.Verifier,
			Status:    string(check.Status),
			Evidence:  evidence,
			CreatedAt: report.CreatedAt,
		}); err != nil {
			return nil, fmt.Errorf("persisting %s verification: %w", check.Verifier, err)
		}
		ids = append(ids, verificationID)
	}
	aggregateID, err := id.New()
	if err != nil {
		return nil, err
	}
	aggregateEvidence, err := json.Marshal(map[string]any{
		"check_ids": ids,
		"checks":    len(report.Checks),
	})
	if err != nil {
		return nil, fmt.Errorf("encoding aggregate verification evidence: %w", err)
	}
	if err := e.store.RecordVerification(ctx, task.Verification{
		ID:        aggregateID,
		TaskID:    item.ID,
		AttemptID: item.ActiveAttemptID,
		Verifier:  "core.aggregate",
		Status:    string(report.Status),
		Evidence:  aggregateEvidence,
		CreatedAt: report.CreatedAt,
	}); err != nil {
		return nil, fmt.Errorf("persisting aggregate verification: %w", err)
	}
	return append(ids, aggregateID), nil
}

func verificationFailure(report verification.Report) string {
	for _, check := range report.Checks {
		if check.Required && check.Status == verification.StatusFailed {
			return "verification failed: " + check.Summary
		}
	}
	return "verification failed"
}

type resultVerifier struct{}

func (resultVerifier) Verify(
	_ context.Context,
	item task.Task,
	result string,
) (verification.Report, error) {
	status := verification.StatusPassed
	summary := fmt.Sprintf("Final result contains %d characters.", len([]rune(strings.TrimSpace(result))))
	if strings.TrimSpace(result) == "" {
		status = verification.StatusFailed
		summary = "Final result is empty."
	}
	check := verification.CheckResult{
		Verifier: "core.result",
		Status:   status,
		Required: true,
		Summary:  summary,
		Evidence: []verification.Evidence{{
			Kind:    "task_result",
			Ref:     "task:" + item.ID,
			Summary: summary,
		}},
	}
	return verification.Report{
		Status:    verification.Aggregate([]verification.CheckResult{check}),
		Checks:    []verification.CheckResult{check},
		CreatedAt: time.Now().UTC(),
	}, nil
}
