package embedded

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/userInner/kern/internal/operation"
	"github.com/userInner/kern/internal/tool"
	"github.com/userInner/kern/internal/tool/builtin"
)

// CapabilityEffect declares the maximum external impact of one host
// capability. Core authorizes this effect before calling Handler.
type CapabilityEffect string

const (
	CapabilityRead         CapabilityEffect = "read"
	CapabilityLocalWrite   CapabilityEffect = "local_write"
	CapabilityProcess      CapabilityEffect = "process"
	CapabilityNetworkRead  CapabilityEffect = "network_read"
	CapabilityNetworkWrite CapabilityEffect = "network_write"
	CapabilityDestructive  CapabilityEffect = "destructive"
)

// Capability is trusted host integration code. ProviderID must use the
// reserved `host.` prefix. Capabilities remain behind the single model-facing
// `capability` tool and do not bypass Operation, Policy, or Approval.
type Capability struct {
	ProviderID   string
	ProviderName string
	ID           string
	Description  string
	InputSchema  json.RawMessage
	Effect       CapabilityEffect
	Handler      func(context.Context, json.RawMessage) (CapabilityResult, error)
}

// CapabilityResult is bounded by the ordinary Core tool and artifact limits.
type CapabilityResult struct {
	Content   string
	OutputRef string
	ExitCode  *int
	Artifacts []CapabilityArtifact
}

// CapabilityArtifact is immutable evidence emitted by host integration code.
type CapabilityArtifact struct {
	Name      string
	MediaType string
	Content   []byte
}

func adaptCapabilities(items []Capability) ([]builtin.HostCapability, error) {
	result := make([]builtin.HostCapability, 0, len(items))
	for _, item := range items {
		effect, err := internalEffect(item.Effect)
		if err != nil {
			return nil, err
		}
		capability := item
		var handler func(context.Context, json.RawMessage) (tool.Result, error)
		if capability.Handler != nil {
			handler = func(ctx context.Context, input json.RawMessage) (tool.Result, error) {
				value, err := capability.Handler(ctx, input)
				artifacts := make([]tool.OutputArtifact, 0, len(value.Artifacts))
				for _, artifact := range value.Artifacts {
					artifacts = append(artifacts, tool.OutputArtifact{
						Name: artifact.Name, MediaType: artifact.MediaType, Content: artifact.Content,
					})
				}
				return tool.Result{
					Content: value.Content, OutputRef: value.OutputRef,
					ExitCode: value.ExitCode, Artifacts: artifacts,
				}, err
			}
		}
		result = append(result, builtin.HostCapability{
			ProviderID: capability.ProviderID, ProviderName: capability.ProviderName,
			ID: capability.ID, Description: capability.Description,
			InputSchema: append(json.RawMessage(nil), capability.InputSchema...),
			Effect:      effect, Handler: handler,
		})
	}
	return result, nil
}

func internalEffect(effect CapabilityEffect) (operation.Effect, error) {
	switch effect {
	case CapabilityRead, CapabilityLocalWrite, CapabilityProcess,
		CapabilityNetworkRead, CapabilityNetworkWrite, CapabilityDestructive:
		return operation.Effect(effect), nil
	default:
		return operation.EffectUnknown, errors.New("embedded: invalid capability effect")
	}
}
