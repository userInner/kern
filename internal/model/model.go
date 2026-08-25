// Package model defines provider-neutral model requests, responses, and streams.
package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Role identifies the author of a message.
type Role string

const (
	RoleUnknown   Role = ""
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ContentKind identifies a message content block.
type ContentKind string

const (
	ContentUnknown          ContentKind = ""
	ContentText             ContentKind = "text"
	ContentImage            ContentKind = "image"
	ContentArtifactRef      ContentKind = "artifact_ref"
	ContentToolCall         ContentKind = "tool_call"
	ContentToolResult       ContentKind = "tool_result"
	ContentReasoningSummary ContentKind = "reasoning_summary"
)

// Image is an image input referenced by URL or data URL.
type Image struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// ToolCall is a complete model-requested tool invocation.
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ToolResult returns one tool invocation result to the model.
type ToolResult struct {
	CallID  string `json:"call_id"`
	Content string `json:"content"`
	IsError bool   `json:"is_error,omitempty"`
}

// ContentBlock is one explicitly typed piece of message content.
type ContentBlock struct {
	Kind        ContentKind `json:"kind"`
	Text        string      `json:"text,omitempty"`
	Image       *Image      `json:"image,omitempty"`
	ArtifactRef string      `json:"artifact_ref,omitempty"`
	ToolCall    *ToolCall   `json:"tool_call,omitempty"`
	ToolResult  *ToolResult `json:"tool_result,omitempty"`
}

// Message is a provider-neutral conversation message.
type Message struct {
	Role    Role           `json:"role"`
	Content []ContentBlock `json:"content"`
}

// ToolDefinition describes a callable tool to a model.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ToolChoice controls whether the model may call tools.
type ToolChoice string

const (
	ToolChoiceAuto     ToolChoice = "auto"
	ToolChoiceNone     ToolChoice = "none"
	ToolChoiceRequired ToolChoice = "required"
)

// Request is the provider-neutral generation request.
type Request struct {
	Model       string            `json:"model,omitempty"`
	Messages    []Message         `json:"messages"`
	Tools       []ToolDefinition  `json:"tools,omitempty"`
	ToolChoice  ToolChoice        `json:"tool_choice,omitempty"`
	Temperature *float64          `json:"temperature,omitempty"`
	MaxTokens   int               `json:"max_tokens,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// Usage contains normalized token and optional provider cost data.
type Usage struct {
	InputTokens     int   `json:"input_tokens"`
	OutputTokens    int   `json:"output_tokens"`
	ReasoningTokens int   `json:"reasoning_tokens,omitempty"`
	CachedTokens    int   `json:"cached_tokens,omitempty"`
	CostMicros      int64 `json:"cost_micros,omitempty"`
}

// Response is one complete normalized model response.
type Response struct {
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
	Usage        Usage   `json:"usage"`
	RequestID    string  `json:"request_id,omitempty"`
}

// Capabilities advertises provider features used for safe engine degradation.
type Capabilities struct {
	TextInput        bool `json:"text_input"`
	ImageInput       bool `json:"image_input"`
	ToolCalling      bool `json:"tool_calling"`
	Streaming        bool `json:"streaming"`
	StructuredOutput bool `json:"structured_output"`
	ReasoningSummary bool `json:"reasoning_summary"`
	Usage            bool `json:"usage"`
	ContextTokens    int  `json:"context_tokens,omitempty"`
}

// StreamEventKind identifies one normalized streaming update.
type StreamEventKind string

const (
	StreamEventUnknown        StreamEventKind = ""
	StreamEventTextDelta      StreamEventKind = "text_delta"
	StreamEventReasoningDelta StreamEventKind = "reasoning_delta"
	StreamEventToolCallDelta  StreamEventKind = "tool_call_delta"
	StreamEventUsage          StreamEventKind = "usage"
	StreamEventCompleted      StreamEventKind = "completed"
)

// ToolCallDelta is an indexed partial tool call from a streaming provider.
type ToolCallDelta struct {
	Index          int    `json:"index"`
	ID             string `json:"id,omitempty"`
	Name           string `json:"name,omitempty"`
	ArgumentsDelta string `json:"arguments_delta,omitempty"`
}

// StreamEvent is one normalized provider stream update.
type StreamEvent struct {
	Kind          StreamEventKind `json:"kind"`
	Text          string          `json:"text,omitempty"`
	ToolCallDelta *ToolCallDelta  `json:"tool_call_delta,omitempty"`
	Usage         *Usage          `json:"usage,omitempty"`
	FinishReason  string          `json:"finish_reason,omitempty"`
	RequestID     string          `json:"request_id,omitempty"`
}

// EventStream produces events until it returns io.EOF.
type EventStream interface {
	Next(ctx context.Context) (StreamEvent, error)
	Close() error
}

// Generator produces a complete response.
type Generator interface {
	Generate(ctx context.Context, request Request) (Response, error)
}

// StreamGenerator produces a normalized response stream.
type StreamGenerator interface {
	Stream(ctx context.Context, request Request) (EventStream, error)
}

// CapabilityProvider advertises supported provider features.
type CapabilityProvider interface {
	Capabilities() Capabilities
}

// ErrorKind classifies failures so callers only retry known transient errors.
type ErrorKind string

const (
	ErrorUnknown          ErrorKind = ""
	ErrorConfiguration    ErrorKind = "configuration"
	ErrorAuthentication   ErrorKind = "authentication"
	ErrorRateLimited      ErrorKind = "rate_limited"
	ErrorTimeout          ErrorKind = "timeout"
	ErrorTemporary        ErrorKind = "temporary"
	ErrorContextLimit     ErrorKind = "context_limit"
	ErrorProtocol         ErrorKind = "protocol"
	ErrorCancelled        ErrorKind = "cancelled"
	ErrorResponseTooLarge ErrorKind = "response_too_large"
)

// Error is a classified model provider failure.
type Error struct {
	Kind       ErrorKind
	StatusCode int
	Retryable  bool
	Cause      error
}

func (e *Error) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("model: %s (status %d): %v", e.Kind, e.StatusCode, e.Cause)
	}
	return fmt.Sprintf("model: %s: %v", e.Kind, e.Cause)
}

func (e *Error) Unwrap() error {
	return e.Cause
}

// IsRetryable reports whether an error is explicitly classified as transient.
func IsRetryable(err error) bool {
	var modelErr *Error
	return errors.As(err, &modelErr) && modelErr.Retryable
}

// Collect consumes a stream into one complete response.
func Collect(ctx context.Context, stream EventStream) (Response, error) {
	defer stream.Close()
	var response Response
	toolCalls := make(map[int]*ToolCall)
	for {
		event, err := stream.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Response{}, err
		}
		if event.RequestID != "" {
			response.RequestID = event.RequestID
		}
		switch event.Kind {
		case StreamEventTextDelta:
			appendTextBlock(&response.Message, ContentText, event.Text)
		case StreamEventReasoningDelta:
			appendTextBlock(&response.Message, ContentReasoningSummary, event.Text)
		case StreamEventToolCallDelta:
			if event.ToolCallDelta == nil {
				return Response{}, &Error{Kind: ErrorProtocol, Cause: errors.New("tool call delta is missing")}
			}
			delta := event.ToolCallDelta
			call := toolCalls[delta.Index]
			if call == nil {
				call = &ToolCall{}
				toolCalls[delta.Index] = call
			}
			if delta.ID != "" {
				call.ID = delta.ID
			}
			if delta.Name != "" {
				call.Name += delta.Name
			}
			call.Arguments = append(call.Arguments, delta.ArgumentsDelta...)
		case StreamEventUsage:
			if event.Usage != nil {
				response.Usage = *event.Usage
			}
		case StreamEventCompleted:
			response.FinishReason = event.FinishReason
		}
	}
	response.Message.Role = RoleAssistant
	for index := 0; index < len(toolCalls); index++ {
		call, ok := toolCalls[index]
		if !ok {
			return Response{}, &Error{Kind: ErrorProtocol, Cause: errors.New("tool call indexes are not contiguous")}
		}
		if call.Name == "" || !json.Valid(call.Arguments) {
			return Response{}, &Error{Kind: ErrorProtocol, Cause: errors.New("tool call is incomplete")}
		}
		response.Message.Content = append(response.Message.Content, ContentBlock{
			Kind:     ContentToolCall,
			ToolCall: call,
		})
	}
	return response, nil
}

func appendTextBlock(message *Message, kind ContentKind, delta string) {
	if delta == "" {
		return
	}
	last := len(message.Content) - 1
	if last >= 0 && message.Content[last].Kind == kind {
		message.Content[last].Text += delta
		return
	}
	message.Content = append(message.Content, ContentBlock{Kind: kind, Text: delta})
}
