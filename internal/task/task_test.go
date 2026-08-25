package task

import (
	"errors"
	"testing"
)

func TestCanTransition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		from    Status
		to      Status
		allowed bool
	}{
		{name: "created to planning", from: StatusCreated, to: StatusPlanning, allowed: true},
		{name: "running to verifying", from: StatusRunning, to: StatusVerifying, allowed: true},
		{name: "verifying to completed", from: StatusVerifying, to: StatusCompleted, allowed: true},
		{name: "created cannot complete", from: StatusCreated, to: StatusCompleted, allowed: false},
		{name: "terminal cannot restart", from: StatusCompleted, to: StatusRunning, allowed: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := CanTransition(test.from, test.to); got != test.allowed {
				t.Fatalf("CanTransition(%q, %q) = %v, want %v", test.from, test.to, got, test.allowed)
			}
		})
	}
}

func TestValidateTransition(t *testing.T) {
	t.Parallel()

	err := ValidateTransition(StatusCreated, StatusCompleted)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("ValidateTransition() error = %v, want ErrInvalidTransition", err)
	}
}
