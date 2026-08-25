package policy

import (
	"encoding/json"
	"testing"

	"github.com/userInner/kern/internal/approval"
	"github.com/userInner/kern/internal/operation"
)

func TestEvaluatorClassifiesEffects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		effect     operation.Effect
		wantAction Action
		wantRisk   approval.Risk
	}{
		{name: "read", effect: operation.EffectRead, wantAction: ActionAllow, wantRisk: approval.RiskLow},
		{name: "workspace write", effect: operation.EffectLocalWrite, wantAction: ActionAsk, wantRisk: approval.RiskMedium},
		{name: "process", effect: operation.EffectProcess, wantAction: ActionAsk, wantRisk: approval.RiskHigh},
		{name: "network read", effect: operation.EffectNetworkRead, wantAction: ActionAsk, wantRisk: approval.RiskMedium},
		{name: "network write", effect: operation.EffectNetworkWrite, wantAction: ActionAsk, wantRisk: approval.RiskHigh},
		{name: "destructive", effect: operation.EffectDestructive, wantAction: ActionAsk, wantRisk: approval.RiskCritical},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := json.RawMessage(`{"target":"value"}`)
			if tt.effect == operation.EffectProcess {
				input = json.RawMessage(`{"argv":["go","test","./..."]}`)
			}
			decision, err := New().Evaluate(operation.Operation{
				Tool:      "test",
				Input:     input,
				InputHash: "digest",
				Effect:    tt.effect,
			})
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if decision.Action != tt.wantAction || decision.Risk != tt.wantRisk {
				t.Fatalf("Evaluate() = %#v", decision)
			}
			if !json.Valid(decision.Scope) || len(decision.Explanation) == 0 {
				t.Fatalf("Evaluate() scope or explanation is invalid: %#v", decision)
			}
		})
	}
}

func TestEvaluatorRejectsInvalidEnvelope(t *testing.T) {
	t.Parallel()
	if _, err := New().Evaluate(operation.Operation{Tool: "inspect", Effect: operation.EffectRead}); err == nil {
		t.Fatal("Evaluate() error = nil")
	}
}

func TestEvaluatorEscalatesDestructiveProcessCommand(t *testing.T) {
	t.Parallel()
	decision, err := New().Evaluate(operation.Operation{
		Tool:      "execute",
		Input:     json.RawMessage(`{"argv":["rm","-rf","build"]}`),
		InputHash: "digest",
		Effect:    operation.EffectProcess,
	})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if decision.Action != ActionAsk || decision.Risk != approval.RiskCritical {
		t.Fatalf("Evaluate() = %#v", decision)
	}
}

func TestEvaluatorProfilesChangeOnlyFutureDecisions(t *testing.T) {
	t.Parallel()
	evaluator := New()
	read := operation.Operation{
		Tool: "inspect", Input: json.RawMessage(`{"path":"README.md"}`),
		InputHash: "read-digest", Effect: operation.EffectRead,
	}
	write := operation.Operation{
		Tool: "change", Input: json.RawMessage(`{"path":"README.md"}`),
		InputHash: "write-digest", Effect: operation.EffectLocalWrite,
	}
	if err := evaluator.SetProfile(ProfileConfirmAll); err != nil {
		t.Fatalf("SetProfile(confirm-all) error = %v", err)
	}
	decision, err := evaluator.Evaluate(read)
	if err != nil || decision.Action != ActionAsk || decision.Risk != approval.RiskLow {
		t.Fatalf("Evaluate(confirm-all read) = %#v, %v", decision, err)
	}
	if err := evaluator.SetProfile(ProfileReadOnly); err != nil {
		t.Fatalf("SetProfile(read-only) error = %v", err)
	}
	decision, err = evaluator.Evaluate(read)
	if err != nil || decision.Action != ActionAllow {
		t.Fatalf("Evaluate(read-only read) = %#v, %v", decision, err)
	}
	decision, err = evaluator.Evaluate(write)
	if err != nil || decision.Action != ActionDeny || decision.Risk != approval.RiskMedium {
		t.Fatalf("Evaluate(read-only write) = %#v, %v", decision, err)
	}
	if err := evaluator.SetProfile(Profile("unsafe")); err == nil {
		t.Fatal("SetProfile(unknown) error = nil")
	}
	if evaluator.Profile() != ProfileReadOnly {
		t.Fatalf("Profile() = %q, want %q", evaluator.Profile(), ProfileReadOnly)
	}
}
