// Package operation defines durable tool side-effect records.
package operation

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var (
	ErrNotFound          = errors.New("operation: not found")
	ErrDuplicate         = errors.New("operation: duplicate idempotency key")
	ErrInvalidTransition = errors.New("operation: invalid transition")
	ErrInvalidResolution = errors.New("operation: invalid uncertain-operation resolution")
	ErrAlreadyResolved   = errors.New("operation: uncertain operation already resolved")
	ErrUnresolvedEffects = errors.New("operation: unresolved uncertain side effects")
)

// Effect classifies the maximum external impact of an operation.
type Effect string

const (
	EffectUnknown      Effect = ""
	EffectRead         Effect = "read"
	EffectLocalWrite   Effect = "local_write"
	EffectProcess      Effect = "process"
	EffectNetworkRead  Effect = "network_read"
	EffectNetworkWrite Effect = "network_write"
	EffectDestructive  Effect = "destructive"
)

// Status describes the durable operation lifecycle.
type Status string

const (
	StatusUnknown          Status = ""
	StatusProposed         Status = "proposed"
	StatusAwaitingApproval Status = "awaiting_approval"
	StatusPrepared         Status = "prepared"
	StatusExecuting        Status = "executing"
	StatusSucceeded        Status = "succeeded"
	StatusFailed           Status = "failed"
	StatusUncertain        Status = "unknown"
	StatusCancelled        Status = "cancelled"
)

// Operation is the durable envelope around a tool call.
type Operation struct {
	ID             string          `json:"id"`
	TaskID         string          `json:"task_id"`
	AttemptID      string          `json:"attempt_id"`
	Tool           string          `json:"tool"`
	Input          json.RawMessage `json:"input"`
	InputHash      string          `json:"input_hash"`
	IdempotencyKey string          `json:"idempotency_key"`
	Effect         Effect          `json:"effect"`
	Status         Status          `json:"status"`
	OutputSummary  string          `json:"output_summary"`
	ErrorCode      string          `json:"error_code"`
	Recovery       json.RawMessage `json:"recovery,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

// Result stores bounded output metadata separately from the operation.
type Result struct {
	OperationID string          `json:"operation_id"`
	OutputRef   string          `json:"output_ref"`
	ExitCode    *int            `json:"exit_code,omitempty"`
	ErrorCode   string          `json:"error_code"`
	Timing      json.RawMessage `json:"timing"`
	CreatedAt   time.Time       `json:"created_at"`
}

// Record joins an operation with its durable execution result and the exact
// approval receipt, if Core policy required one.
type Record struct {
	Operation         Operation `json:"operation"`
	Result            *Result   `json:"result,omitempty"`
	ApprovalReceiptID string    `json:"approval_receipt_id,omitempty"`
}

// Resolution is an explicit human conclusion about an operation whose outcome
// could not be observed after a runtime interruption.
type Resolution string

const (
	ResolutionUnknown     Resolution = ""
	ResolutionSucceeded   Resolution = "confirmed_succeeded"
	ResolutionNotExecuted Resolution = "confirmed_not_executed"
)

// ResolutionReceipt is immutable audit evidence for a human recovery choice.
type ResolutionReceipt struct {
	ID          string     `json:"id"`
	OperationID string     `json:"operation_id"`
	TaskID      string     `json:"task_id"`
	AttemptID   string     `json:"attempt_id"`
	Resolution  Resolution `json:"resolution"`
	Actor       string     `json:"actor"`
	DecidedAt   time.Time  `json:"decided_at"`
}

// Valid reports whether the resolution has an unambiguous recovery meaning.
func (r Resolution) Valid() bool {
	return r == ResolutionSucceeded || r == ResolutionNotExecuted
}

// IsTerminal reports whether an operation can no longer execute.
func (s Status) IsTerminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusUncertain, StatusCancelled:
		return true
	default:
		return false
	}
}

// ValidateTransition enforces the operation state machine.
func ValidateTransition(from, to Status) error {
	allowed := map[Status]map[Status]bool{
		StatusProposed: {
			StatusAwaitingApproval: true,
			StatusPrepared:         true,
			StatusCancelled:        true,
		},
		StatusAwaitingApproval: {
			StatusPrepared:  true,
			StatusCancelled: true,
			StatusFailed:    true,
		},
		StatusPrepared: {
			StatusExecuting: true,
			StatusFailed:    true,
			StatusCancelled: true,
		},
		StatusExecuting: {
			StatusSucceeded: true,
			StatusFailed:    true,
			StatusUncertain: true,
		},
		StatusUncertain: {
			StatusSucceeded: true,
			StatusFailed:    true,
			StatusPrepared:  true,
			StatusCancelled: true,
		},
	}
	if allowed[from][to] {
		return nil
	}
	return fmt.Errorf("%w: %s to %s", ErrInvalidTransition, from, to)
}
