// Package approval defines explicit user authorization records.
package approval

import (
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrNotFound       = errors.New("approval: not found")
	ErrAlreadyDecided = errors.New("approval: already decided")
	ErrExpired        = errors.New("approval: expired")
)

// Risk is the user-facing risk classification.
type Risk string

const (
	RiskUnknown  Risk = ""
	RiskLow      Risk = "low"
	RiskMedium   Risk = "medium"
	RiskHigh     Risk = "high"
	RiskCritical Risk = "critical"
)

// Status is the request lifecycle.
type Status string

const (
	StatusUnknown   Status = ""
	StatusPending   Status = "pending"
	StatusApproved  Status = "approved"
	StatusDenied    Status = "denied"
	StatusExpired   Status = "expired"
	StatusCancelled Status = "cancelled"
)

// Decision is the actor's explicit choice.
type Decision string

const (
	DecisionUnknown  Decision = ""
	DecisionApproved Decision = "approved"
	DecisionDenied   Decision = "denied"
)

// Request describes the exact pending authorization scope.
type Request struct {
	ID          string          `json:"id"`
	TaskID      string          `json:"task_id"`
	AttemptID   string          `json:"attempt_id"`
	OperationID string          `json:"operation_id"`
	Scope       json.RawMessage `json:"scope"`
	Risk        Risk            `json:"risk"`
	Explanation string          `json:"explanation"`
	Status      Status          `json:"status"`
	ExpiresAt   time.Time       `json:"expires_at"`
	CreatedAt   time.Time       `json:"created_at"`
}

// Receipt is the immutable decision audit record.
type Receipt struct {
	ID        string          `json:"id"`
	RequestID string          `json:"request_id"`
	Decision  Decision        `json:"decision"`
	Actor     string          `json:"actor"`
	Scope     json.RawMessage `json:"scope"`
	DecidedAt time.Time       `json:"decided_at"`
}
