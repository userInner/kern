package embedded

import (
	"context"

	coremodel "github.com/userInner/kern/internal/model"
)

// ModelProvider is the minimum provider contract accepted by embedded Core.
// Returning a classified Error lets Kern apply only explicitly retryable
// provider failures.
type ModelProvider interface {
	Generate(ctx context.Context, request ModelRequest) (ModelResponse, error)
}

// StreamingModelProvider is detected automatically when implemented in
// addition to ModelProvider.
type StreamingModelProvider interface {
	Stream(ctx context.Context, request ModelRequest) (ModelEventStream, error)
}

type (
	ModelRole            = coremodel.Role
	ModelContentKind     = coremodel.ContentKind
	ModelImage           = coremodel.Image
	ModelToolCall        = coremodel.ToolCall
	ModelToolResult      = coremodel.ToolResult
	ModelContentBlock    = coremodel.ContentBlock
	ModelMessage         = coremodel.Message
	ModelToolDefinition  = coremodel.ToolDefinition
	ModelToolChoice      = coremodel.ToolChoice
	ModelRequest         = coremodel.Request
	ModelUsage           = coremodel.Usage
	ModelResponse        = coremodel.Response
	ModelCapabilities    = coremodel.Capabilities
	ModelStreamEventKind = coremodel.StreamEventKind
	ModelToolCallDelta   = coremodel.ToolCallDelta
	ModelStreamEvent     = coremodel.StreamEvent
	ModelEventStream     = coremodel.EventStream
	ModelErrorKind       = coremodel.ErrorKind
	ModelError           = coremodel.Error
)

const (
	ModelRoleSystem    = coremodel.RoleSystem
	ModelRoleUser      = coremodel.RoleUser
	ModelRoleAssistant = coremodel.RoleAssistant
	ModelRoleTool      = coremodel.RoleTool

	ModelContentText             = coremodel.ContentText
	ModelContentImage            = coremodel.ContentImage
	ModelContentArtifactRef      = coremodel.ContentArtifactRef
	ModelContentToolCall         = coremodel.ContentToolCall
	ModelContentToolResult       = coremodel.ContentToolResult
	ModelContentReasoningSummary = coremodel.ContentReasoningSummary

	ModelToolChoiceAuto     = coremodel.ToolChoiceAuto
	ModelToolChoiceNone     = coremodel.ToolChoiceNone
	ModelToolChoiceRequired = coremodel.ToolChoiceRequired

	ModelStreamTextDelta      = coremodel.StreamEventTextDelta
	ModelStreamReasoningDelta = coremodel.StreamEventReasoningDelta
	ModelStreamToolCallDelta  = coremodel.StreamEventToolCallDelta
	ModelStreamUsage          = coremodel.StreamEventUsage
	ModelStreamCompleted      = coremodel.StreamEventCompleted

	ModelErrorConfiguration    = coremodel.ErrorConfiguration
	ModelErrorAuthentication   = coremodel.ErrorAuthentication
	ModelErrorRateLimited      = coremodel.ErrorRateLimited
	ModelErrorTimeout          = coremodel.ErrorTimeout
	ModelErrorTemporary        = coremodel.ErrorTemporary
	ModelErrorContextLimit     = coremodel.ErrorContextLimit
	ModelErrorProtocol         = coremodel.ErrorProtocol
	ModelErrorCancelled        = coremodel.ErrorCancelled
	ModelErrorResponseTooLarge = coremodel.ErrorResponseTooLarge
)

// ModelErrorIsRetryable reports whether err explicitly opts into Core retry.
func ModelErrorIsRetryable(err error) bool {
	return coremodel.IsRetryable(err)
}
