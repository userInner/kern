package evaluation

import (
	"errors"
	"time"
)

var (
	ErrRunNotFound    = errors.New("evaluation: run not found")
	ErrReportNotReady = errors.New("evaluation: report not ready")
)

// RunConfig captures the caller-controlled inputs required to reproduce a run.
type RunConfig struct {
	SuitePath string   `json:"suite_path"`
	Variants  []string `json:"variants"`
}

// Run is the small durable snapshot used by list and progress APIs. The full
// immutable outcome is stored as a Report when the run reaches a terminal state.
type Run struct {
	SchemaVersion  string     `json:"schema_version"`
	ID             string     `json:"id"`
	SuiteID        string     `json:"suite_id"`
	SuiteName      string     `json:"suite_name"`
	SuiteVersion   string     `json:"suite_version"`
	Status         Status     `json:"status"`
	Variants       []string   `json:"variants"`
	CaseCount      int        `json:"case_count"`
	CompletedCases int        `json:"completed_cases"`
	ConfigDigest   string     `json:"config_digest,omitempty"`
	ErrorMessage   string     `json:"error_message,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
}

// Terminal reports whether a run will not advance again.
func (s Status) Terminal() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusCancelled
}
