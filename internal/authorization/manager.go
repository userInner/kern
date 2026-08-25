// Package authorization coordinates policy decisions with durable approvals.
package authorization

import (
	"context"
	"errors"
	"time"

	"github.com/userInner/kern/internal/approval"
	"github.com/userInner/kern/internal/operation"
	"github.com/userInner/kern/internal/policy"
	"github.com/userInner/kern/internal/task"
)

const (
	defaultApprovalTTL = 10 * time.Minute
	defaultPollPeriod  = 100 * time.Millisecond
)

var (
	ErrDenied  = errors.New("authorization: operation denied")
	ErrExpired = errors.New("authorization: approval expired")
)

type store interface {
	CreateApproval(
		ctx context.Context,
		operationID string,
		scope any,
		risk approval.Risk,
		explanation string,
		ttl time.Duration,
	) (approval.Request, error)
	GetOperation(ctx context.Context, operationID string) (operation.Operation, error)
	TransitionOperation(
		ctx context.Context,
		operationID string,
		to operation.Status,
		result *operation.Result,
		outputSummary string,
		errorCode string,
	) (operation.Operation, error)
	Transition(ctx context.Context, taskID string, to task.Status, result, errorMessage string) error
	GetTask(ctx context.Context, taskID string) (task.Task, error)
	DecideApproval(
		ctx context.Context,
		requestID string,
		decision approval.Decision,
		actor string,
	) (approval.Receipt, error)
	CancelApproval(ctx context.Context, requestID, actor string) error
}

// Config bounds approval waiting behavior.
type Config struct {
	ApprovalTTL time.Duration
	PollPeriod  time.Duration
}

// Manager applies policy and waits for durable user decisions.
type Manager struct {
	store  store
	policy *policy.Evaluator
	config Config
}

// New constructs an authorization manager.
func New(store store, evaluator *policy.Evaluator, config Config) (*Manager, error) {
	if store == nil || evaluator == nil {
		return nil, errors.New("authorization: store and policy are required")
	}
	if config.ApprovalTTL <= 0 {
		config.ApprovalTTL = defaultApprovalTTL
	}
	if config.PollPeriod <= 0 {
		config.PollPeriod = defaultPollPeriod
	}
	return &Manager{store: store, policy: evaluator, config: config}, nil
}

// Authorize returns an operation in prepared state or a fail-closed error.
func (m *Manager) Authorize(
	ctx context.Context,
	item task.Task,
	op operation.Operation,
) (operation.Operation, error) {
	decision, err := m.policy.Evaluate(op)
	if err != nil {
		return m.cancel(ctx, op, "policy_error", err)
	}
	switch decision.Action {
	case policy.ActionAllow:
		return m.store.TransitionOperation(ctx, op.ID, operation.StatusPrepared, nil, "", "")
	case policy.ActionDeny:
		return m.cancel(ctx, op, "policy_denied", ErrDenied)
	case policy.ActionAsk:
		return m.waitForDecision(ctx, item, op, decision)
	default:
		return m.cancel(ctx, op, "policy_invalid", ErrDenied)
	}
}

func (m *Manager) waitForDecision(
	ctx context.Context,
	item task.Task,
	op operation.Operation,
	decision policy.Decision,
) (operation.Operation, error) {
	request, err := m.store.CreateApproval(
		ctx,
		op.ID,
		decision.Scope,
		decision.Risk,
		decision.Explanation,
		m.config.ApprovalTTL,
	)
	if err != nil {
		return operation.Operation{}, err
	}

	ticker := time.NewTicker(m.config.PollPeriod)
	defer ticker.Stop()
	expires := time.NewTimer(time.Until(request.ExpiresAt))
	defer expires.Stop()
	for {
		current, err := m.store.GetOperation(ctx, op.ID)
		if err != nil {
			return operation.Operation{}, err
		}
		switch current.Status {
		case operation.StatusPrepared:
			if err := m.returnToRunning(ctx, item.ID); err != nil {
				return operation.Operation{}, err
			}
			return current, nil
		case operation.StatusCancelled:
			if err := m.returnToRunning(ctx, item.ID); err != nil && !errors.Is(err, task.ErrInvalidTransition) {
				return operation.Operation{}, err
			}
			return current, ErrDenied
		}
		select {
		case <-ctx.Done():
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			cancelErr := m.cancelInterruptedApproval(cleanupCtx, request.ID, op.ID)
			resumeErr := m.returnToRunning(cleanupCtx, item.ID)
			cancel()
			if errors.Is(resumeErr, task.ErrInvalidTransition) {
				resumeErr = nil
			}
			if cancelErr != nil || resumeErr != nil {
				return operation.Operation{}, errors.Join(ctx.Err(), cancelErr, resumeErr)
			}
			return operation.Operation{}, ctx.Err()
		case <-ticker.C:
		case <-expires.C:
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			_, expireErr := m.store.DecideApproval(
				cleanupCtx,
				request.ID,
				approval.DecisionDenied,
				"kern-expiry",
			)
			resumeErr := m.returnToRunning(cleanupCtx, item.ID)
			cancel()
			if expireErr != nil && !errors.Is(expireErr, approval.ErrExpired) {
				return operation.Operation{}, expireErr
			}
			if resumeErr != nil && !errors.Is(resumeErr, task.ErrInvalidTransition) {
				return operation.Operation{}, resumeErr
			}
			return operation.Operation{}, ErrExpired
		}
	}
}

// cancelInterruptedApproval closes the narrow race where a user approves an
// operation immediately before the task context is cancelled. A prepared
// operation has not started its side effect, so cancelling it is safe. An
// executing operation is deliberately left untouched for crash recovery to
// classify as uncertain instead of pretending it never ran.
func (m *Manager) cancelInterruptedApproval(ctx context.Context, requestID, operationID string) error {
	err := m.store.CancelApproval(ctx, requestID, "kern-context-cancelled")
	if err == nil {
		return nil
	}
	if !errors.Is(err, approval.ErrAlreadyDecided) {
		return err
	}

	current, err := m.store.GetOperation(ctx, operationID)
	if err != nil {
		return err
	}
	if current.Status != operation.StatusPrepared {
		return nil
	}
	_, err = m.store.TransitionOperation(
		ctx,
		operationID,
		operation.StatusCancelled,
		nil,
		"Authorization was approved, but execution was interrupted before the tool started.",
		"authorization_interrupted",
	)
	if !errors.Is(err, operation.ErrInvalidTransition) {
		return err
	}

	// A concurrent executor may have moved prepared -> executing between the
	// read and transition. That operation must be handled by recovery rather
	// than forcibly cancelled while its side effect may be in flight.
	current, readErr := m.store.GetOperation(ctx, operationID)
	if readErr != nil {
		return errors.Join(err, readErr)
	}
	if current.Status == operation.StatusExecuting || current.Status.IsTerminal() {
		return nil
	}
	return err
}

func (m *Manager) returnToRunning(ctx context.Context, taskID string) error {
	current, err := m.store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if current.Status != task.StatusWaitingApproval {
		return nil
	}
	return m.store.Transition(ctx, taskID, task.StatusRunning, current.Result, current.ErrorMessage)
}

func (m *Manager) cancel(
	ctx context.Context,
	op operation.Operation,
	errorCode string,
	cause error,
) (operation.Operation, error) {
	cancelled, err := m.store.TransitionOperation(
		ctx,
		op.ID,
		operation.StatusCancelled,
		nil,
		cause.Error(),
		errorCode,
	)
	return cancelled, errors.Join(cause, err)
}
