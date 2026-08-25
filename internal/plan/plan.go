// Package plan defines durable, mutable execution plans without replacing the
// user's immutable task goal.
package plan

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const SchemaVersion = "1"

var (
	ErrNotFound          = errors.New("plan: not found")
	ErrInvalidDraft      = errors.New("plan: invalid draft")
	ErrInvalidTransition = errors.New("plan: invalid step transition")
)

// Status is the aggregate execution state of one plan revision.
type Status string

const (
	StatusUnknown    Status = ""
	StatusActive     Status = "active"
	StatusCompleted  Status = "completed"
	StatusFailed     Status = "failed"
	StatusSuperseded Status = "superseded"
)

// StepStatus is the observable state of one plan step.
type StepStatus string

const (
	StepStatusUnknown   StepStatus = ""
	StepStatusPending   StepStatus = "pending"
	StepStatusRunning   StepStatus = "running"
	StepStatusCompleted StepStatus = "completed"
	StepStatusFailed    StepStatus = "failed"
	StepStatusSkipped   StepStatus = "skipped"
)

// Phase maps variable task-specific wording onto the Core lifecycle.
type Phase string

const (
	PhaseUnknown Phase = ""
	PhasePrepare Phase = "prepare"
	PhaseExecute Phase = "execute"
	PhaseVerify  Phase = "verify"
)

// Draft is the planner's validated proposal before storage assigns identity.
type Draft struct {
	Rationale string      `json:"rationale"`
	Steps     []StepDraft `json:"steps"`
}

// StepDraft is one ordered, required or optional unit of a plan proposal.
type StepDraft struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Phase       Phase  `json:"phase"`
	Required    bool   `json:"required"`
}

// Plan is one durable revision for an Attempt.
type Plan struct {
	SchemaVersion string    `json:"schema_version"`
	ID            string    `json:"id"`
	TaskID        string    `json:"task_id"`
	AttemptID     string    `json:"attempt_id"`
	Revision      int       `json:"revision"`
	Status        Status    `json:"status"`
	Rationale     string    `json:"rationale"`
	Steps         []Step    `json:"steps"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Step is one durable, auditable unit of work.
type Step struct {
	ID          string     `json:"id"`
	PlanID      string     `json:"plan_id"`
	Ordinal     int        `json:"ordinal"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Phase       Phase      `json:"phase"`
	Required    bool       `json:"required"`
	Status      StepStatus `json:"status"`
	Failure     string     `json:"failure"`
	RetryCount  int        `json:"retry_count"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// ValidateDraft rejects plans that are too large, ambiguous, or missing a
// lifecycle phase Kern needs to execute safely.
func ValidateDraft(draft Draft) error {
	if strings.TrimSpace(draft.Rationale) == "" || len(draft.Steps) == 0 || len(draft.Steps) > 12 {
		return ErrInvalidDraft
	}
	seen := make(map[Phase]bool)
	for _, step := range draft.Steps {
		if strings.TrimSpace(step.Title) == "" || len([]rune(step.Title)) > 160 ||
			len([]rune(step.Description)) > 1000 || !validPhase(step.Phase) {
			return ErrInvalidDraft
		}
		seen[step.Phase] = true
	}
	for _, phase := range []Phase{PhasePrepare, PhaseExecute, PhaseVerify} {
		if !seen[phase] {
			return fmt.Errorf("%w: missing %s phase", ErrInvalidDraft, phase)
		}
	}
	return nil
}

// ValidateStepTransition enforces monotonic execution except for an explicit
// failed-to-pending retry, which increments RetryCount in storage.
func ValidateStepTransition(from, to StepStatus) error {
	allowed := map[StepStatus]map[StepStatus]bool{
		StepStatusPending: {
			StepStatusRunning: true,
			StepStatusSkipped: true,
		},
		StepStatusRunning: {
			StepStatusCompleted: true,
			StepStatusFailed:    true,
		},
		StepStatusFailed: {
			StepStatusPending: true,
		},
	}
	if allowed[from][to] {
		return nil
	}
	return fmt.Errorf("%w: %s to %s", ErrInvalidTransition, from, to)
}

func validPhase(phase Phase) bool {
	return phase == PhasePrepare || phase == PhaseExecute || phase == PhaseVerify
}
