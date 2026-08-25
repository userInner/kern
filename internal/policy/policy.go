// Package policy makes deterministic authorization decisions for proposed operations.
package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/userInner/kern/internal/approval"
	"github.com/userInner/kern/internal/operation"
)

// Profile is a bounded, user-selectable Core authorization posture.
type Profile string

const (
	ProfileLocalSafe  Profile = "local-safe"
	ProfileConfirmAll Profile = "confirm-all"
	ProfileReadOnly   Profile = "read-only"
)

// Action is the policy outcome before a tool can execute.
type Action string

const (
	ActionUnknown Action = ""
	ActionAllow   Action = "allow"
	ActionAsk     Action = "ask"
	ActionDeny    Action = "deny"
)

// Decision contains a deterministic policy outcome and its exact user-facing scope.
type Decision struct {
	Action      Action
	Risk        approval.Risk
	Explanation string
	Scope       json.RawMessage
}

// Evaluator applies Core policy to normalized operation envelopes. A profile
// update affects only operations evaluated after the update; already persisted
// approval scopes remain immutable.
type Evaluator struct {
	mu      sync.RWMutex
	profile Profile
}

// New constructs the built-in policy evaluator.
func New() *Evaluator { return &Evaluator{profile: ProfileLocalSafe} }

// ValidateProfile rejects unimplemented policy labels before persistence.
func ValidateProfile(profile Profile) error {
	switch profile {
	case ProfileLocalSafe, ProfileConfirmAll, ProfileReadOnly:
		return nil
	default:
		return errors.New("policy: profile must be local-safe, confirm-all, or read-only")
	}
}

// SetProfile changes the authorization posture for future evaluations.
func (e *Evaluator) SetProfile(profile Profile) error {
	if err := ValidateProfile(profile); err != nil {
		return err
	}
	e.mu.Lock()
	e.profile = profile
	e.mu.Unlock()
	return nil
}

// Profile returns the current authorization posture.
func (e *Evaluator) Profile() Profile {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.profile
}

// Evaluate classifies an operation without executing it.
func (e *Evaluator) Evaluate(item operation.Operation) (Decision, error) {
	if item.Tool == "" || item.Effect == operation.EffectUnknown || !json.Valid(item.Input) {
		return Decision{}, fmt.Errorf("policy: invalid operation envelope")
	}
	scope, err := json.Marshal(map[string]any{
		"tool":       item.Tool,
		"effect":     item.Effect,
		"input":      json.RawMessage(item.Input),
		"input_hash": item.InputHash,
	})
	if err != nil {
		return Decision{}, fmt.Errorf("policy: encoding operation scope: %w", err)
	}
	decision := Decision{Scope: scope}
	switch item.Effect {
	case operation.EffectRead:
		decision.Action = ActionAllow
		decision.Risk = approval.RiskLow
		decision.Explanation = "Read bounded information from the current task workspace."
	case operation.EffectLocalWrite:
		decision.Action = ActionAsk
		decision.Risk = approval.RiskMedium
		decision.Explanation = "Create or modify a file inside the current task workspace."
	case operation.EffectProcess:
		decision.Action = ActionAsk
		decision.Risk, decision.Explanation = processRisk(item.Input)
	case operation.EffectNetworkRead:
		decision.Action = ActionAsk
		decision.Risk = approval.RiskMedium
		decision.Explanation = "Send a read-only request to the displayed public network target."
	case operation.EffectNetworkWrite:
		decision.Action = ActionAsk
		decision.Risk = approval.RiskHigh
		decision.Explanation = "Write data to the displayed external network target."
	case operation.EffectDestructive:
		decision.Action = ActionAsk
		decision.Risk = approval.RiskCritical
		decision.Explanation = "Perform the displayed destructive operation."
	default:
		decision.Action = ActionDeny
		decision.Risk = approval.RiskCritical
		decision.Explanation = "The operation effect is not recognized by Core policy."
	}
	switch e.Profile() {
	case ProfileConfirmAll:
		if decision.Action == ActionAllow {
			decision.Action = ActionAsk
			decision.Explanation = "The confirm-all profile requires approval before this bounded read."
		}
	case ProfileReadOnly:
		if item.Effect != operation.EffectRead {
			decision.Action = ActionDeny
			decision.Explanation = "The read-only profile blocks every operation except bounded workspace reads."
		}
	}
	return decision, nil
}

func processRisk(input json.RawMessage) (approval.Risk, string) {
	var capability struct {
		Action   string `json:"action"`
		PluginID string `json:"plugin_id"`
		ToolID   string `json:"tool_id"`
	}
	if json.Unmarshal(input, &capability) == nil && capability.Action == "invoke" &&
		capability.PluginID != "" && capability.ToolID != "" {
		return approval.RiskHigh, fmt.Sprintf(
			"Run plugin capability %s/%s in an isolated, bounded child process.",
			capability.PluginID,
			capability.ToolID,
		)
	}
	var request struct {
		Argv []string `json:"argv"`
	}
	if json.Unmarshal(input, &request) != nil || len(request.Argv) == 0 {
		return approval.RiskCritical, "Run a command whose normalized argv could not be inspected."
	}
	command := strings.ToLower(filepath.Base(request.Argv[0]))
	switch command {
	case "rm", "rmdir", "shred", "dd", "mkfs", "shutdown", "reboot", "sudo", "su", "chown", "chmod":
		return approval.RiskCritical, "Run the displayed command, which can delete data or change system state."
	default:
		return approval.RiskHigh, "Run the displayed command inside the current task workspace."
	}
}
