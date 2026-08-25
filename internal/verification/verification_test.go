package verification

import (
	"errors"
	"testing"
)

func TestAggregatePrecedence(t *testing.T) {
	tests := []struct {
		name   string
		checks []CheckResult
		want   Status
	}{
		{name: "none", want: StatusNotRun},
		{name: "verified", checks: []CheckResult{{Status: StatusPassed, Required: true}}, want: StatusVerified},
		{name: "optional failure", checks: []CheckResult{{Status: StatusPassed, Required: true}, {Status: StatusFailed}}, want: StatusPartial},
		{name: "optional not run", checks: []CheckResult{{Status: StatusPassed, Required: true}, {Status: StatusNotRun}}, want: StatusVerified},
		{name: "required not run", checks: []CheckResult{{Status: StatusPassed, Required: true}, {Status: StatusNotRun, Required: true}}, want: StatusPartial},
		{name: "manual", checks: []CheckResult{{Status: StatusPassed, Required: true}, {Status: StatusManualRequired}}, want: StatusManualRequired},
		{name: "required failure wins", checks: []CheckResult{{Status: StatusManualRequired}, {Status: StatusFailed, Required: true}}, want: StatusFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Aggregate(tt.checks); got != tt.want {
				t.Fatalf("Aggregate() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateRequiresAuditableEvidence(t *testing.T) {
	valid := Report{Checks: []CheckResult{{
		Verifier: "core.result",
		Status:   StatusPassed,
		Summary:  "result exists",
		Evidence: []Evidence{{Kind: "task_result", Ref: "task:1", Summary: "digest recorded"}},
	}}}
	if err := Validate(valid); err != nil {
		t.Fatalf("Validate(valid) error = %v", err)
	}
	valid.Checks[0].Evidence = nil
	if err := Validate(valid); !errors.Is(err, ErrInvalidReport) {
		t.Fatalf("Validate(no evidence) error = %v", err)
	}
}
