package plan

import (
	"errors"
	"testing"
)

func TestValidateDraftAndStepTransitions(t *testing.T) {
	draft := Draft{
		Rationale: "complex task",
		Steps: []StepDraft{
			{Title: "Prepare", Phase: PhasePrepare, Required: true},
			{Title: "Execute", Phase: PhaseExecute, Required: true},
			{Title: "Verify", Phase: PhaseVerify, Required: true},
		},
	}
	if err := ValidateDraft(draft); err != nil {
		t.Fatalf("ValidateDraft() error = %v", err)
	}
	draft.Steps = draft.Steps[:2]
	if err := ValidateDraft(draft); !errors.Is(err, ErrInvalidDraft) {
		t.Fatalf("ValidateDraft(incomplete) error = %v", err)
	}
	for _, transition := range [][2]StepStatus{
		{StepStatusPending, StepStatusRunning},
		{StepStatusRunning, StepStatusCompleted},
		{StepStatusRunning, StepStatusFailed},
		{StepStatusFailed, StepStatusPending},
	} {
		if err := ValidateStepTransition(transition[0], transition[1]); err != nil {
			t.Fatalf("ValidateStepTransition(%s, %s) error = %v", transition[0], transition[1], err)
		}
	}
	if err := ValidateStepTransition(StepStatusPending, StepStatusCompleted); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("ValidateStepTransition(illegal) error = %v", err)
	}
}
