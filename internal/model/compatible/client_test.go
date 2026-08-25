package compatible

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/userInner/kern/internal/model"
	"github.com/userInner/kern/internal/task"
)

func TestClientGenerateNormalizesResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		var input chatRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("Decode() error = %v", err)
		}
		if input.Model != "test-model" || input.Stream {
			t.Errorf("request model = %q, stream = %v", input.Model, input.Stream)
		}
		if len(input.Tools) != 1 || input.ToolChoice != model.ToolChoiceAuto {
			t.Errorf("request tools = %#v, choice = %q", input.Tools, input.ToolChoice)
		}
		w.Header().Set("x-request-id", "request-header")
		_ = json.NewEncoder(w).Encode(chatResponse{
			ID: "request-body",
			Choices: []chatChoice{{
				FinishReason: "tool_calls",
				Message: chatMessage{
					Role:             "assistant",
					Content:          "I will inspect it.",
					ReasoningContent: "Need repository evidence.",
					ToolCalls: []chatToolCall{{
						ID:   "call-1",
						Type: "function",
						Function: chatFunctionCall{
							Name:      "inspect",
							Arguments: `{"path":"README.md"}`,
						},
					}},
				},
			}},
			Usage: chatUsage{
				PromptTokens:     12,
				CompletionTokens: 7,
				PromptTokensDetails: promptTokenDetails{
					CachedTokens: 3,
				},
				CompletionTokensDetails: completionTokenDetails{
					ReasoningTokens: 2,
				},
			},
		})
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	response, err := client.Generate(t.Context(), model.Request{
		Messages: []model.Message{{
			Role:    model.RoleUser,
			Content: []model.ContentBlock{{Kind: model.ContentText, Text: "inspect the repo"}},
		}},
		Tools: []model.ToolDefinition{{
			Name:        "inspect",
			Description: "Inspect workspace files",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if response.RequestID != "request-header" || response.FinishReason != "tool_calls" {
		t.Fatalf("Generate() metadata = %#v", response)
	}
	if response.Usage.InputTokens != 12 || response.Usage.ReasoningTokens != 2 ||
		response.Usage.CachedTokens != 3 {
		t.Fatalf("Generate() usage = %#v", response.Usage)
	}
	if len(response.Message.Content) != 3 {
		t.Fatalf("Generate() content count = %d, want 3", len(response.Message.Content))
	}
	call := response.Message.Content[2].ToolCall
	if call == nil || call.Name != "inspect" || string(call.Arguments) != `{"path":"README.md"}` {
		t.Fatalf("Generate() tool call = %#v", call)
	}
}

func TestClientStreamCollectsTextReasoningToolsAndUsage(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var input chatRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("Decode() error = %v", err)
		}
		if !input.Stream || input.StreamOptions == nil || !input.StreamOptions.IncludeUsage {
			t.Errorf("stream request = %#v", input)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-request-id", "stream-request")
		chunks := []string{
			`{"id":"body-id","choices":[{"delta":{"reasoning_content":"check "}}]}`,
			`{"id":"body-id","choices":[{"delta":{"content":"Hello "}}]}`,
			`{"id":"body-id","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"ins","arguments":"{\"pa"}}]}}]}`,
			`{"id":"body-id","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"pect","arguments":"th\":\"README.md\"}"}}]},"finish_reason":"tool_calls"}]}`,
			`{"id":"body-id","choices":[],"usage":{"prompt_tokens":8,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":1}}}`,
		}
		for _, chunk := range chunks {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	stream, err := client.Stream(t.Context(), model.Request{Messages: []model.Message{{
		Role:    model.RoleUser,
		Content: []model.ContentBlock{{Kind: model.ContentText, Text: "hello"}},
	}}})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	response, err := model.Collect(t.Context(), stream)
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if response.RequestID != "stream-request" || response.FinishReason != "tool_calls" {
		t.Fatalf("Collect() metadata = %#v", response)
	}
	if len(response.Message.Content) != 3 {
		t.Fatalf("Collect() content = %#v", response.Message.Content)
	}
	if response.Message.Content[0].Text != "check " || response.Message.Content[1].Text != "Hello " {
		t.Fatalf("Collect() text blocks = %#v", response.Message.Content)
	}
	call := response.Message.Content[2].ToolCall
	if call == nil || call.ID != "call-1" || call.Name != "inspect" ||
		string(call.Arguments) != `{"path":"README.md"}` {
		t.Fatalf("Collect() tool call = %#v", call)
	}
	if response.Usage.InputTokens != 8 || response.Usage.OutputTokens != 5 ||
		response.Usage.ReasoningTokens != 1 {
		t.Fatalf("Collect() usage = %#v", response.Usage)
	}
}

func TestClientGenerateRedactsKnownAPIKeyFromProviderOutput(t *testing.T) {
	t.Parallel()

	const apiKey = "sk-known-provider-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+apiKey {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		w.Header().Set("x-request-id", "request-"+apiKey)
		_ = json.NewEncoder(w).Encode(chatResponse{
			Choices: []chatChoice{{
				FinishReason: "stop-" + apiKey,
				Message: chatMessage{
					Role:             "assistant",
					ReasoningContent: "provider reasoned with " + apiKey,
					Content:          "provider echoed " + apiKey,
					ToolCalls: []chatToolCall{{
						ID: "call-" + apiKey,
						Function: chatFunctionCall{
							Name:      "inspect",
							Arguments: `{"token":"` + apiKey + `","copy":"prefix-` + apiKey + `-suffix"}`,
						},
					}},
				},
			}},
		})
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, APIKey: apiKey, Model: "test-model"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	response, err := client.Generate(t.Context(), model.Request{})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if strings.Contains(string(encoded), apiKey) {
		t.Fatalf("Generate() leaked API key: %s", encoded)
	}
	if count := strings.Count(string(encoded), redactedSecret); count != 7 {
		t.Fatalf("redaction count = %d, want 7: %s", count, encoded)
	}
	call := response.Message.Content[2].ToolCall
	if call == nil || !json.Valid(call.Arguments) {
		t.Fatalf("redacted tool arguments are invalid: %#v", call)
	}
}

func TestClientStreamRedactsAPIKeyAcrossProviderDeltas(t *testing.T) {
	t.Parallel()

	const apiKey = "sk-known-provider-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-request-id", "stream-"+apiKey)
		chunks := []string{
			`{"choices":[{"delta":{"reasoning_content":"reason sk-known-provider-"}}]}`,
			`{"choices":[{"delta":{"reasoning_content":"secret complete"}}]}`,
			`{"choices":[{"delta":{"content":"before sk-known-"}}]}`,
			`{"choices":[{"delta":{"content":"provider-secret after "}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"inspect","arguments":"{\"token\":\"sk-known-provider-"}}]}}]}`,
			`{"choices":[{"delta":{"content":"tail sk-","tool_calls":[{"index":0,"function":{"arguments":"secret\",\"path\":\"README.md\"}"}}]},"finish_reason":"stop-sk-known-provider-secret"}]}`,
		}
		for _, chunk := range chunks {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, APIKey: apiKey, Model: "test-model"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	stream, err := client.Stream(t.Context(), model.Request{})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	recorded := &recordingEventStream{EventStream: stream}
	response, err := model.Collect(t.Context(), recorded)
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	for index, event := range recorded.events {
		encoded, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			t.Fatalf("Marshal(event %d) error = %v", index, marshalErr)
		}
		if strings.Contains(string(encoded), apiKey) {
			t.Fatalf("stream event %d leaked API key: %s", index, encoded)
		}
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Marshal(response) error = %v", err)
	}
	if strings.Contains(string(encoded), apiKey) {
		t.Fatalf("Collect() leaked API key: %s", encoded)
	}
	if !strings.Contains(string(encoded), "before "+redactedSecret+" after tail sk-") {
		t.Fatalf("redacted text or flushed suffix missing: %s", encoded)
	}
	if !strings.Contains(string(encoded), "reason "+redactedSecret+" complete") {
		t.Fatalf("redacted reasoning missing: %s", encoded)
	}
	call := response.Message.Content[2].ToolCall
	if call == nil || string(call.Arguments) != `{"token":"`+redactedSecret+`","path":"README.md"}` {
		t.Fatalf("redacted tool call = %#v", call)
	}
	if response.RequestID != "stream-"+redactedSecret ||
		response.FinishReason != "stop-"+redactedSecret {
		t.Fatalf("redacted metadata = %#v", response)
	}
}

func TestClientRedactsAPIKeyFromHTTPError(t *testing.T) {
	t.Parallel()

	const apiKey = "sk-known-provider-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "credential rejected: "+apiKey, http.StatusUnauthorized)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, APIKey: apiKey, Model: "test-model"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = client.Generate(t.Context(), model.Request{})
	if err == nil {
		t.Fatal("Generate() error = nil")
	}
	if strings.Contains(err.Error(), apiKey) || !strings.Contains(err.Error(), redactedSecret) {
		t.Fatalf("Generate() error was not safely redacted: %v", err)
	}
}

func TestRollingSecretRedactorPreservesSafeTextAndRedactsOverlaps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		secret string
		parts  []string
		want   string
	}{
		{name: "empty secret", parts: []string{"safe", " text"}, want: "safe text"},
		{name: "split secret", secret: "secret", parts: []string{"a se", "cr", "et b"}, want: "a " + redactedSecret + " b"},
		{name: "overlap", secret: "aba", parts: []string{"ab", "aba"}, want: redactedSecret + "ba"},
		{name: "one byte", secret: "x", parts: []string{"xaxb"}, want: redactedSecret + "a" + redactedSecret + "b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			redactor := newRollingSecretRedactor(tt.secret)
			var got strings.Builder
			for _, part := range tt.parts {
				got.WriteString(redactor.Push(part))
			}
			got.WriteString(redactor.Flush())
			if got.String() != tt.want {
				t.Fatalf("redacted = %q, want %q", got.String(), tt.want)
			}
		})
	}
}

func TestClientEncodesMultimodalInput(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var input chatRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("Decode() error = %v", err)
		}
		parts, ok := input.Messages[0].Content.([]any)
		if !ok || len(parts) != 2 {
			t.Errorf("multimodal content = %#v", input.Messages[0].Content)
		}
		_ = json.NewEncoder(w).Encode(chatResponse{Choices: []chatChoice{{
			FinishReason: "stop",
			Message:      chatMessage{Role: "assistant", Content: "described"},
		}}})
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	_, err := client.Generate(t.Context(), model.Request{Messages: []model.Message{{
		Role: model.RoleUser,
		Content: []model.ContentBlock{
			{Kind: model.ContentText, Text: "describe"},
			{Kind: model.ContentImage, Image: &model.Image{URL: "data:image/png;base64,AA==", Detail: "low"}},
		},
	}}})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
}

func TestClientClassifiesRateLimit(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	_, err := client.Generate(t.Context(), model.Request{})
	var modelErr *model.Error
	if !errors.As(err, &modelErr) || modelErr.Kind != model.ErrorRateLimited || !model.IsRetryable(err) {
		t.Fatalf("Generate() error = %#v", err)
	}
}

func TestClientProcess(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(chatResponse{Choices: []chatChoice{{
			FinishReason: "stop",
			Message:      chatMessage{Role: "assistant", Content: "model result"},
		}}})
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	result, err := client.Process(context.Background(), task.Task{Goal: "do work"})
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if result != "model result" {
		t.Fatalf("Process() = %q", result)
	}
}

func TestNewRejectsUnsafeConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL string
	}{
		{name: "unsafe scheme", baseURL: "file:///tmp/model"},
		{name: "embedded credentials", baseURL: "https://user:pass@example.com"},
		{name: "missing host", baseURL: "https:///path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(Config{BaseURL: tt.baseURL, Model: "test"})
			if err == nil {
				t.Fatal("New() error = nil, want configuration error")
			}
		})
	}
}

func TestEventStreamRejectsOversizedEvent(t *testing.T) {
	t.Parallel()

	body := "data: " + strings.Repeat("x", maxSSEEventBytes+1) + "\n\n"
	reader := strings.NewReader(body)
	stream := &eventStream{
		body:   io.NopCloser(reader),
		reader: bufio.NewReaderSize(reader, 64<<10),
	}
	_, err := stream.Next(t.Context())
	var modelErr *model.Error
	if !errors.As(err, &modelErr) || modelErr.Kind != model.ErrorResponseTooLarge {
		t.Fatalf("Next() error = %#v", err)
	}
}

func newTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	client, err := New(Config{BaseURL: baseURL, APIKey: "secret", Model: "test-model"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return client
}

type recordingEventStream struct {
	model.EventStream
	events []model.StreamEvent
}

func (s *recordingEventStream) Next(ctx context.Context) (model.StreamEvent, error) {
	event, err := s.EventStream.Next(ctx)
	if err == nil {
		s.events = append(s.events, event)
	}
	return event, err
}
