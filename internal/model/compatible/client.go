// Package compatible connects Kern to an OpenAI-compatible chat endpoint.
package compatible

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/userInner/kern/internal/model"
	"github.com/userInner/kern/internal/task"
)

const (
	maxResponseBytes = 4 << 20
	maxStreamBytes   = 16 << 20
	maxSSEEventBytes = 1 << 20
	redactedSecret   = "[REDACTED_SECRET]"
)

// Config defines an OpenAI-compatible model endpoint.
type Config struct {
	BaseURL string
	APIKey  string
	Model   string
}

// Client calls a configured chat completion endpoint.
type Client struct {
	httpClient *http.Client
	endpoint   string
	apiKey     string
	model      string
}

// New validates config and returns a model client.
func New(config Config) (*Client, error) {
	baseURL, err := url.Parse(strings.TrimSpace(config.BaseURL))
	if err != nil {
		return nil, fmt.Errorf("parsing model base url: %w", err)
	}
	if baseURL.Scheme != "http" && baseURL.Scheme != "https" {
		return nil, errors.New("model: base url must use http or https")
	}
	if baseURL.Host == "" {
		return nil, errors.New("model: base url must include a host")
	}
	if baseURL.User != nil {
		return nil, errors.New("model: base url must not include credentials")
	}
	if strings.TrimSpace(config.Model) == "" {
		return nil, errors.New("model: model name is required")
	}

	endpointURL, err := url.Parse(strings.TrimRight(baseURL.String(), "/") + "/v1/chat/completions")
	if err != nil {
		return nil, fmt.Errorf("building model endpoint: %w", err)
	}
	httpClient := &http.Client{Timeout: 2 * time.Minute}
	httpClient.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("model: too many redirects")
		}
		if request.URL.Scheme != endpointURL.Scheme || request.URL.Host != endpointURL.Host {
			return errors.New("model: cross-origin redirect is not allowed")
		}
		return nil
	}
	return &Client{
		httpClient: httpClient,
		endpoint:   endpointURL.String(),
		apiKey:     strings.TrimSpace(config.APIKey),
		model:      strings.TrimSpace(config.Model),
	}, nil
}

// Capabilities reports the normalized features exposed by this adapter.
func (c *Client) Capabilities() model.Capabilities {
	return model.Capabilities{
		TextInput:        true,
		ImageInput:       true,
		ToolCalling:      true,
		Streaming:        true,
		StructuredOutput: true,
		ReasoningSummary: true,
		Usage:            true,
	}
}

// Generate returns a complete provider-neutral response.
func (c *Client) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	body, err := c.requestBody(request, false)
	if err != nil {
		return model.Response{}, err
	}
	response, err := c.do(ctx, body)
	if err != nil {
		return model.Response{}, err
	}
	defer response.Body.Close()
	encoded, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return model.Response{}, classifyTransportError("reading model response", err)
	}
	if len(encoded) > maxResponseBytes {
		return model.Response{}, &model.Error{
			Kind:  model.ErrorResponseTooLarge,
			Cause: errors.New("response exceeds size limit"),
		}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return model.Response{}, classifyHTTPError(response.StatusCode, encoded, c.apiKey)
	}
	var decoded chatResponse
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return model.Response{}, &model.Error{Kind: model.ErrorProtocol, Cause: err}
	}
	if len(decoded.Choices) == 0 {
		return model.Response{}, &model.Error{
			Kind:  model.ErrorProtocol,
			Cause: errors.New("response has no choices"),
		}
	}
	choice := decoded.Choices[0]
	message, err := fromChatMessage(choice.Message, c.apiKey)
	if err != nil {
		return model.Response{}, err
	}
	return model.Response{
		Message:      message,
		FinishReason: redactKnownSecret(choice.FinishReason, c.apiKey),
		Usage:        normalizeUsage(decoded.Usage),
		RequestID: redactKnownSecret(
			firstNonEmpty(response.Header.Get("x-request-id"), decoded.ID),
			c.apiKey,
		),
	}, nil
}

// Stream opens a bounded normalized event stream.
func (c *Client) Stream(ctx context.Context, request model.Request) (model.EventStream, error) {
	body, err := c.requestBody(request, true)
	if err != nil {
		return nil, err
	}
	response, err := c.do(ctx, body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		defer response.Body.Close()
		encoded, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
		if readErr != nil {
			return nil, classifyTransportError("reading model error response", readErr)
		}
		return nil, classifyHTTPError(response.StatusCode, encoded, c.apiKey)
	}
	return &eventStream{
		body:              response.Body,
		reader:            bufio.NewReaderSize(response.Body, 64<<10),
		requestID:         redactKnownSecret(response.Header.Get("x-request-id"), c.apiKey),
		secret:            c.apiKey,
		textRedactor:      newRollingSecretRedactor(c.apiKey),
		reasoningRedactor: newRollingSecretRedactor(c.apiKey),
		toolRedactors:     make(map[int]*toolCallRedactors),
	}, nil
}

// Process preserves the initial processor contract while the Agent runner is wired in.
func (c *Client) Process(ctx context.Context, item task.Task) (string, error) {
	response, err := c.Generate(ctx, model.Request{Messages: []model.Message{
		{
			Role: model.RoleSystem,
			Content: []model.ContentBlock{{
				Kind: model.ContentText,
				Text: "You are the reasoning component of Kern Core. Answer the user's goal directly. " +
					"Do not claim to have used tools or changed external state.",
			}},
		},
		{
			Role:    model.RoleUser,
			Content: []model.ContentBlock{{Kind: model.ContentText, Text: item.Goal}},
		},
	}})
	if err != nil {
		return "", err
	}
	var text strings.Builder
	for _, block := range response.Message.Content {
		if block.Kind == model.ContentText {
			text.WriteString(block.Text)
		}
	}
	if strings.TrimSpace(text.String()) == "" {
		return "", &model.Error{Kind: model.ErrorProtocol, Cause: errors.New("response content is empty")}
	}
	return strings.TrimSpace(text.String()), nil
}

func (c *Client) requestBody(request model.Request, stream bool) ([]byte, error) {
	convertedMessages := make([]chatMessage, 0, len(request.Messages))
	for _, message := range request.Messages {
		converted, err := toChatMessages(message)
		if err != nil {
			return nil, err
		}
		convertedMessages = append(convertedMessages, converted...)
	}
	tools := make([]chatTool, 0, len(request.Tools))
	for _, definition := range request.Tools {
		if definition.Name == "" || !json.Valid(definition.InputSchema) {
			return nil, &model.Error{
				Kind:  model.ErrorConfiguration,
				Cause: errors.New("tool definition is invalid"),
			}
		}
		tools = append(tools, chatTool{
			Type: "function",
			Function: chatFunctionDefinition{
				Name:        definition.Name,
				Description: definition.Description,
				Parameters:  definition.InputSchema,
			},
		})
	}
	modelName := strings.TrimSpace(request.Model)
	if modelName == "" {
		modelName = c.model
	}
	toolChoice := request.ToolChoice
	if len(tools) > 0 && toolChoice == "" {
		toolChoice = model.ToolChoiceAuto
	}
	payload := chatRequest{
		Model:       modelName,
		Messages:    convertedMessages,
		Tools:       tools,
		ToolChoice:  toolChoice,
		Temperature: request.Temperature,
		MaxTokens:   request.MaxTokens,
		Stream:      stream,
	}
	if stream {
		payload.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encoding model request: %w", err)
	}
	return encoded, nil
}

func (c *Client) do(ctx context.Context, body []byte) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, &model.Error{Kind: model.ErrorConfiguration, Cause: err}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if c.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, classifyTransportError("calling model", err)
	}
	return response, nil
}

func toChatMessages(message model.Message) ([]chatMessage, error) {
	if message.Role == model.RoleUnknown {
		return nil, &model.Error{Kind: model.ErrorConfiguration, Cause: errors.New("message role is required")}
	}
	converted := chatMessage{Role: string(message.Role)}
	parts := make([]chatContentPart, 0, len(message.Content))
	for _, block := range message.Content {
		switch block.Kind {
		case model.ContentText, model.ContentReasoningSummary:
			parts = append(parts, chatContentPart{Type: "text", Text: block.Text})
		case model.ContentImage:
			if block.Image == nil || block.Image.URL == "" {
				return nil, &model.Error{Kind: model.ErrorConfiguration, Cause: errors.New("image block is invalid")}
			}
			parts = append(parts, chatContentPart{
				Type:     "image_url",
				ImageURL: &chatImageURL{URL: block.Image.URL, Detail: block.Image.Detail},
			})
		case model.ContentArtifactRef:
			parts = append(parts, chatContentPart{
				Type: "text",
				Text: "[artifact:" + block.ArtifactRef + "]",
			})
		case model.ContentToolCall:
			if block.ToolCall == nil || block.ToolCall.Name == "" || !json.Valid(block.ToolCall.Arguments) {
				return nil, &model.Error{Kind: model.ErrorConfiguration, Cause: errors.New("tool call block is invalid")}
			}
			converted.ToolCalls = append(converted.ToolCalls, chatToolCall{
				ID:   block.ToolCall.ID,
				Type: "function",
				Function: chatFunctionCall{
					Name:      block.ToolCall.Name,
					Arguments: string(block.ToolCall.Arguments),
				},
			})
		case model.ContentToolResult:
			if block.ToolResult == nil || block.ToolResult.CallID == "" {
				return nil, &model.Error{Kind: model.ErrorConfiguration, Cause: errors.New("tool result block is invalid")}
			}
			content := block.ToolResult.Content
			if block.ToolResult.IsError {
				content = "ERROR: " + content
			}
			return []chatMessage{{
				Role:       string(model.RoleTool),
				Content:    content,
				ToolCallID: block.ToolResult.CallID,
			}}, nil
		default:
			return nil, &model.Error{Kind: model.ErrorConfiguration, Cause: errors.New("unknown content block")}
		}
	}
	if len(parts) == 1 && parts[0].Type == "text" {
		converted.Content = parts[0].Text
	} else if len(parts) > 0 {
		converted.Content = parts
	}
	return []chatMessage{converted}, nil
}

func fromChatMessage(message chatMessage, secret string) (model.Message, error) {
	converted := model.Message{Role: model.Role(redactKnownSecret(message.Role, secret))}
	reasoning := redactKnownSecret(firstNonEmpty(message.ReasoningContent, message.Reasoning), secret)
	if reasoning != "" {
		converted.Content = append(converted.Content, model.ContentBlock{
			Kind: model.ContentReasoningSummary,
			Text: reasoning,
		})
	}
	if text, ok := message.Content.(string); ok && text != "" {
		text = redactKnownSecret(text, secret)
		converted.Content = append(converted.Content, model.ContentBlock{Kind: model.ContentText, Text: text})
	}
	for _, call := range message.ToolCalls {
		name := redactKnownSecret(call.Function.Name, secret)
		arguments := json.RawMessage(redactKnownSecret(call.Function.Arguments, secret))
		if name == "" || !json.Valid(arguments) {
			return model.Message{}, &model.Error{Kind: model.ErrorProtocol, Cause: errors.New("invalid tool call")}
		}
		converted.Content = append(converted.Content, model.ContentBlock{
			Kind: model.ContentToolCall,
			ToolCall: &model.ToolCall{
				ID:        redactKnownSecret(call.ID, secret),
				Name:      name,
				Arguments: arguments,
			},
		})
	}
	return converted, nil
}

func normalizeUsage(usage chatUsage) model.Usage {
	return model.Usage{
		InputTokens:     usage.PromptTokens,
		OutputTokens:    usage.CompletionTokens,
		ReasoningTokens: usage.CompletionTokensDetails.ReasoningTokens,
		CachedTokens:    usage.PromptTokensDetails.CachedTokens,
	}
}

func classifyHTTPError(status int, body []byte, secrets ...string) error {
	message := strings.TrimSpace(string(body))
	for _, secret := range secrets {
		message = redactKnownSecret(message, secret)
	}
	if len(message) > 512 {
		message = message[:512]
	}
	if message == "" {
		message = http.StatusText(status)
	}
	kind := model.ErrorProtocol
	retryable := false
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		kind = model.ErrorAuthentication
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		kind = model.ErrorTimeout
		retryable = true
	case http.StatusTooManyRequests:
		kind = model.ErrorRateLimited
		retryable = true
	case http.StatusBadRequest:
		if strings.Contains(strings.ToLower(message), "context") {
			kind = model.ErrorContextLimit
		}
	default:
		if status >= http.StatusInternalServerError {
			kind = model.ErrorTemporary
			retryable = true
		}
	}
	return &model.Error{
		Kind:       kind,
		StatusCode: status,
		Retryable:  retryable,
		Cause:      errors.New(message),
	}
}

func classifyTransportError(action string, err error) error {
	kind := model.ErrorTemporary
	retryable := true
	if errors.Is(err, context.Canceled) {
		kind = model.ErrorCancelled
		retryable = false
	} else if errors.Is(err, context.DeadlineExceeded) {
		kind = model.ErrorTimeout
	} else if netErr, ok := errors.AsType[net.Error](err); ok && netErr.Timeout() {
		kind = model.ErrorTimeout
	}
	return &model.Error{Kind: kind, Retryable: retryable, Cause: fmt.Errorf("%s: %w", action, err)}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

type eventStream struct {
	body              io.ReadCloser
	reader            *bufio.Reader
	queue             []model.StreamEvent
	requestID         string
	currentRequestID  string
	secret            string
	textRedactor      *rollingSecretRedactor
	reasoningRedactor *rollingSecretRedactor
	toolRedactors     map[int]*toolCallRedactors
	toolOrder         []int
	totalBytes        int64
	isDone            bool
	hasCompleted      bool
}

func (s *eventStream) Next(ctx context.Context) (model.StreamEvent, error) {
	for len(s.queue) == 0 {
		if s.isDone {
			return model.StreamEvent{}, io.EOF
		}
		if err := ctx.Err(); err != nil {
			return model.StreamEvent{}, classifyTransportError("reading model stream", err)
		}
		data, done, err := s.readEvent()
		if err != nil {
			return model.StreamEvent{}, err
		}
		if done {
			s.flushRedactors(s.currentRequestID)
			s.isDone = true
			if len(s.queue) == 0 {
				return model.StreamEvent{}, io.EOF
			}
			break
		}
		if len(data) == 0 {
			continue
		}
		var chunk streamChunk
		if err := json.Unmarshal(data, &chunk); err != nil {
			return model.StreamEvent{}, &model.Error{Kind: model.ErrorProtocol, Cause: err}
		}
		requestID := firstNonEmpty(s.requestID, redactKnownSecret(chunk.ID, s.secret))
		s.currentRequestID = requestID
		finishReason := ""
		for _, choice := range chunk.Choices {
			reasoning := firstNonEmpty(choice.Delta.ReasoningContent, choice.Delta.Reasoning)
			if reasoning != "" {
				s.enqueueText(model.StreamEventReasoningDelta, reasoning, requestID)
			}
			if choice.Delta.Content != "" {
				s.enqueueText(model.StreamEventTextDelta, choice.Delta.Content, requestID)
			}
			for _, call := range choice.Delta.ToolCalls {
				s.enqueueToolCall(call, requestID)
			}
			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}
		}
		if finishReason != "" {
			s.flushRedactors(requestID)
			s.hasCompleted = true
			s.queue = append(s.queue, model.StreamEvent{
				Kind:         model.StreamEventCompleted,
				FinishReason: redactKnownSecret(finishReason, s.secret),
				RequestID:    requestID,
			})
		}
		if chunk.Usage.PromptTokens != 0 || chunk.Usage.CompletionTokens != 0 {
			usage := normalizeUsage(chunk.Usage)
			s.queue = append(s.queue, model.StreamEvent{
				Kind:      model.StreamEventUsage,
				Usage:     &usage,
				RequestID: requestID,
			})
		}
	}
	event := s.queue[0]
	s.queue = s.queue[1:]
	return event, nil
}

func (s *eventStream) enqueueText(kind model.StreamEventKind, value, requestID string) {
	redactor := s.textRedactor
	if kind == model.StreamEventReasoningDelta {
		redactor = s.reasoningRedactor
	}
	value = redactor.Push(value)
	if value == "" {
		return
	}
	s.queue = append(s.queue, model.StreamEvent{Kind: kind, Text: value, RequestID: requestID})
}

func (s *eventStream) enqueueToolCall(call streamToolCall, requestID string) {
	redactors := s.toolRedactors[call.Index]
	if redactors == nil {
		redactors = &toolCallRedactors{
			id:        newRollingSecretRedactor(s.secret),
			name:      newRollingSecretRedactor(s.secret),
			arguments: newRollingSecretRedactor(s.secret),
		}
		s.toolRedactors[call.Index] = redactors
		s.toolOrder = append(s.toolOrder, call.Index)
	}
	delta := &model.ToolCallDelta{
		Index:          call.Index,
		ID:             redactors.id.Push(call.ID),
		Name:           redactors.name.Push(call.Function.Name),
		ArgumentsDelta: redactors.arguments.Push(call.Function.Arguments),
	}
	if delta.ID == "" && delta.Name == "" && delta.ArgumentsDelta == "" {
		return
	}
	s.queue = append(s.queue, model.StreamEvent{
		Kind:          model.StreamEventToolCallDelta,
		ToolCallDelta: delta,
		RequestID:     requestID,
	})
}

func (s *eventStream) flushRedactors(requestID string) {
	if value := s.reasoningRedactor.Flush(); value != "" {
		s.queue = append(s.queue, model.StreamEvent{
			Kind: model.StreamEventReasoningDelta, Text: value, RequestID: requestID,
		})
	}
	if value := s.textRedactor.Flush(); value != "" {
		s.queue = append(s.queue, model.StreamEvent{
			Kind: model.StreamEventTextDelta, Text: value, RequestID: requestID,
		})
	}
	for _, index := range s.toolOrder {
		redactors := s.toolRedactors[index]
		delta := &model.ToolCallDelta{
			Index:          index,
			ID:             redactors.id.Flush(),
			Name:           redactors.name.Flush(),
			ArgumentsDelta: redactors.arguments.Flush(),
		}
		if delta.ID == "" && delta.Name == "" && delta.ArgumentsDelta == "" {
			continue
		}
		s.queue = append(s.queue, model.StreamEvent{
			Kind: model.StreamEventToolCallDelta, ToolCallDelta: delta, RequestID: requestID,
		})
	}
}

type toolCallRedactors struct {
	id        *rollingSecretRedactor
	name      *rollingSecretRedactor
	arguments *rollingSecretRedactor
}

type rollingSecretRedactor struct {
	secret  string
	marker  string
	pending string
}

func newRollingSecretRedactor(secret string) *rollingSecretRedactor {
	return &rollingSecretRedactor{secret: secret, marker: secretRedactionMarker(secret)}
}

// Push emits every byte that cannot still become part of a cross-delta secret.
// At most len(secret)-1 bytes remain buffered, so streaming remains bounded.
func (r *rollingSecretRedactor) Push(value string) string {
	if r.secret == "" {
		return value
	}
	r.pending += value
	var safe strings.Builder
	for {
		index := strings.Index(r.pending, r.secret)
		if index < 0 {
			break
		}
		safe.WriteString(r.pending[:index])
		safe.WriteString(r.marker)
		r.pending = r.pending[index+len(r.secret):]
	}

	keep := longestSecretPrefixSuffix(r.pending, r.secret)
	safe.WriteString(r.pending[:len(r.pending)-keep])
	r.pending = r.pending[len(r.pending)-keep:]
	return safe.String()
}

func (r *rollingSecretRedactor) Flush() string {
	if r.secret == "" || r.pending == "" {
		value := r.pending
		r.pending = ""
		return value
	}
	value := redactKnownSecret(r.pending, r.secret)
	r.pending = ""
	return value
}

func longestSecretPrefixSuffix(value, secret string) int {
	limit := min(len(value), len(secret)-1)
	for length := limit; length > 0; length-- {
		if strings.HasSuffix(value, secret[:length]) {
			return length
		}
	}
	return 0
}

func redactKnownSecret(value, secret string) string {
	if secret == "" || value == "" {
		return value
	}
	return strings.ReplaceAll(value, secret, secretRedactionMarker(secret))
}

func secretRedactionMarker(secret string) string {
	if !strings.Contains(secret, "[") && !strings.Contains(secret, "]") &&
		!strings.Contains(redactedSecret, secret) {
		return redactedSecret
	}
	// A readable fixed marker can itself contain a very short or unusual secret.
	// Choose a boundary rune absent from the secret so the replacement neither
	// contains the secret nor recreates it across a replacement boundary.
	for markerRune := '\u2588'; ; markerRune++ {
		if !strings.ContainsRune(secret, markerRune) {
			return strings.Repeat(string(markerRune), 3)
		}
	}
}

func (s *eventStream) Close() error {
	s.isDone = true
	return s.body.Close()
}

func (s *eventStream) readEvent() ([]byte, bool, error) {
	var data bytes.Buffer
	for {
		line, err := s.readLine()
		if err != nil {
			var modelErr *model.Error
			if errors.As(err, &modelErr) {
				return nil, false, err
			}
			if errors.Is(err, io.EOF) && data.Len() > 0 {
				break
			}
			if errors.Is(err, io.EOF) && s.hasCompleted {
				return nil, true, nil
			}
			return nil, false, classifyTransportError("reading model stream", err)
		}
		if len(line) == 0 {
			if data.Len() == 0 {
				continue
			}
			break
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			part := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.Write(part)
			if data.Len() > maxSSEEventBytes {
				return nil, false, &model.Error{
					Kind:  model.ErrorResponseTooLarge,
					Cause: errors.New("stream event exceeds size limit"),
				}
			}
		}
	}
	if bytes.Equal(bytes.TrimSpace(data.Bytes()), []byte("[DONE]")) {
		return nil, true, nil
	}
	return data.Bytes(), false, nil
}

func (s *eventStream) readLine() ([]byte, error) {
	var line bytes.Buffer
	for {
		fragment, isPrefix, err := s.reader.ReadLine()
		s.totalBytes += int64(len(fragment))
		if s.totalBytes > maxStreamBytes {
			return nil, &model.Error{
				Kind:  model.ErrorResponseTooLarge,
				Cause: errors.New("stream exceeds size limit"),
			}
		}
		line.Write(fragment)
		if line.Len() > maxSSEEventBytes {
			return nil, &model.Error{
				Kind:  model.ErrorResponseTooLarge,
				Cause: errors.New("stream line exceeds size limit"),
			}
		}
		if err != nil {
			return nil, err
		}
		if !isPrefix {
			return line.Bytes(), nil
		}
	}
}

type chatRequest struct {
	Model         string           `json:"model"`
	Messages      []chatMessage    `json:"messages"`
	Tools         []chatTool       `json:"tools,omitempty"`
	ToolChoice    model.ToolChoice `json:"tool_choice,omitempty"`
	Temperature   *float64         `json:"temperature,omitempty"`
	MaxTokens     int              `json:"max_tokens,omitempty"`
	Stream        bool             `json:"stream,omitempty"`
	StreamOptions *streamOptions   `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatResponse struct {
	ID      string       `json:"id"`
	Choices []chatChoice `json:"choices"`
	Usage   chatUsage    `json:"usage"`
}

type streamChunk struct {
	ID      string         `json:"id"`
	Choices []streamChoice `json:"choices"`
	Usage   chatUsage      `json:"usage"`
}

type chatChoice struct {
	Message      chatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type streamChoice struct {
	Delta        streamDelta `json:"delta"`
	FinishReason string      `json:"finish_reason"`
}

type streamDelta struct {
	Content          string           `json:"content"`
	ReasoningContent string           `json:"reasoning_content"`
	Reasoning        string           `json:"reasoning"`
	ToolCalls        []streamToolCall `json:"tool_calls"`
}

type streamToolCall struct {
	Index    int              `json:"index"`
	ID       string           `json:"id"`
	Function chatFunctionCall `json:"function"`
}

type chatMessage struct {
	Role             string         `json:"role"`
	Content          any            `json:"content,omitempty"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	Reasoning        string         `json:"reasoning,omitempty"`
	ToolCalls        []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
}

type chatContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *chatImageURL `json:"image_url,omitempty"`
}

type chatImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type chatTool struct {
	Type     string                 `json:"type"`
	Function chatFunctionDefinition `json:"function"`
}

type chatFunctionDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatFunctionCall `json:"function"`
}

type chatFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatUsage struct {
	PromptTokens            int                    `json:"prompt_tokens"`
	CompletionTokens        int                    `json:"completion_tokens"`
	PromptTokensDetails     promptTokenDetails     `json:"prompt_tokens_details"`
	CompletionTokensDetails completionTokenDetails `json:"completion_tokens_details"`
}

type promptTokenDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type completionTokenDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

var (
	_ model.Generator          = (*Client)(nil)
	_ model.StreamGenerator    = (*Client)(nil)
	_ model.CapabilityProvider = (*Client)(nil)
)
