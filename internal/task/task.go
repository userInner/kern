// Package task contains task state and transition rules.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const SchemaVersion = "1"

var (
	ErrNotFound          = errors.New("task: not found")
	ErrInvalidTransition = errors.New("task: invalid transition")
	ErrLeaseHeld         = errors.New("task: execution lease is held")
	ErrLeaseLost         = errors.New("task: execution lease is lost")
)

// Status is the durable state of a task.
type Status string

const (
	StatusUnknown            Status = ""
	StatusCreated            Status = "created"
	StatusPlanning           Status = "planning"
	StatusRunning            Status = "running"
	StatusWaitingApproval    Status = "waiting_approval"
	StatusWaitingInput       Status = "waiting_input"
	StatusVerifying          Status = "verifying"
	StatusCompleted          Status = "completed"
	StatusPartiallyCompleted Status = "partially_completed"
	StatusFailed             Status = "failed"
	StatusCancelled          Status = "cancelled"
)

// Task is the query snapshot served by the API.
type Task struct {
	SchemaVersion   string     `json:"schema_version"`
	ID              string     `json:"id"`
	TraceID         string     `json:"trace_id"`
	Title           string     `json:"title"`
	Goal            string     `json:"goal"`
	Status          Status     `json:"status"`
	Result          string     `json:"result"`
	ErrorMessage    string     `json:"error_message"`
	ActiveAttemptID string     `json:"active_attempt_id"`
	ModelConfigID   string     `json:"model_config_id,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	LeaseOwner      string     `json:"lease_owner"`
	LeaseExpiresAt  *time.Time `json:"lease_expires_at,omitempty"`
	HeartbeatAt     *time.Time `json:"heartbeat_at,omitempty"`
	PausedFrom      Status     `json:"paused_from_status,omitempty"`
}

// Attempt is one immutable execution history segment for a task.
type Attempt struct {
	ID         string     `json:"id"`
	TaskID     string     `json:"task_id"`
	TraceID    string     `json:"trace_id"`
	Status     Status     `json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// Checkpoint captures resumable application state after a durable boundary.
type Checkpoint struct {
	ID        string          `json:"id"`
	TaskID    string          `json:"task_id"`
	AttemptID string          `json:"attempt_id"`
	Ordinal   int             `json:"ordinal"`
	Reason    string          `json:"reason"`
	State     json.RawMessage `json:"state"`
	CreatedAt time.Time       `json:"created_at"`
}

// Event is an immutable task fact. ID is globally monotonic within a store.
type Event struct {
	SchemaVersion string          `json:"schema_version"`
	ID            int64           `json:"id"`
	TaskID        string          `json:"task_id"`
	AttemptID     string          `json:"attempt_id"`
	TraceID       string          `json:"trace_id"`
	SpanID        string          `json:"span_id"`
	ParentSpanID  string          `json:"parent_span_id,omitempty"`
	Type          string          `json:"type"`
	Payload       json.RawMessage `json:"payload"`
	CreatedAt     time.Time       `json:"created_at"`
}

// Verification records a deterministic completion check.
type Verification struct {
	ID        string          `json:"id"`
	TaskID    string          `json:"task_id"`
	AttemptID string          `json:"attempt_id"`
	Verifier  string          `json:"verifier"`
	Status    string          `json:"status"`
	Evidence  json.RawMessage `json:"evidence"`
	CreatedAt time.Time       `json:"created_at"`
}

// IsTerminal reports whether no more transitions are allowed for this attempt.
func (s Status) IsTerminal() bool {
	switch s {
	case StatusCompleted, StatusPartiallyCompleted, StatusFailed, StatusCancelled:
		return true
	default:
		return false
	}
}

// CanTransition reports whether a state transition is legal.
func CanTransition(from, to Status) bool {
	allowed := map[Status]map[Status]bool{
		StatusCreated: {
			StatusPlanning:  true,
			StatusRunning:   true,
			StatusCancelled: true,
			StatusFailed:    true,
		},
		StatusPlanning: {
			StatusRunning:      true,
			StatusWaitingInput: true,
			StatusFailed:       true,
			StatusCancelled:    true,
		},
		StatusRunning: {
			StatusWaitingApproval: true,
			StatusWaitingInput:    true,
			StatusVerifying:       true,
			StatusFailed:          true,
			StatusCancelled:       true,
		},
		StatusWaitingApproval: {
			StatusRunning:   true,
			StatusCancelled: true,
			StatusFailed:    true,
		},
		StatusWaitingInput: {
			StatusPlanning:  true,
			StatusRunning:   true,
			StatusCancelled: true,
		},
		StatusVerifying: {
			StatusRunning:            true,
			StatusCompleted:          true,
			StatusPartiallyCompleted: true,
			StatusFailed:             true,
			StatusCancelled:          true,
		},
	}

	return allowed[from][to]
}

// ValidateTransition returns a domain error for an illegal transition.
func ValidateTransition(from, to Status) error {
	if CanTransition(from, to) {
		return nil
	}

	return fmt.Errorf("%w: %s to %s", ErrInvalidTransition, from, to)
}
