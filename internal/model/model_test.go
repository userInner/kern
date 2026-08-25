package model

import (
	"context"
	"errors"
	"io"
	"testing"
)

func TestCollectAssemblesInterleavedBlocks(t *testing.T) {
	stream := &sliceStream{events: []StreamEvent{
		{Kind: StreamEventReasoningDelta, Text: "first "},
		{Kind: StreamEventReasoningDelta, Text: "reason"},
		{Kind: StreamEventTextDelta, Text: "answer"},
		{Kind: StreamEventToolCallDelta, ToolCallDelta: &ToolCallDelta{
			Index:          0,
			ID:             "call-1",
			Name:           "inspect",
			ArgumentsDelta: `{"path":`,
		}},
		{Kind: StreamEventToolCallDelta, ToolCallDelta: &ToolCallDelta{
			Index:          0,
			ArgumentsDelta: `"README.md"}`,
		}},
		{Kind: StreamEventCompleted, FinishReason: "tool_calls"},
	}}
	response, err := Collect(t.Context(), stream)
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(response.Message.Content) != 3 {
		t.Fatalf("content = %#v", response.Message.Content)
	}
	if response.Message.Content[0].Text != "first reason" ||
		response.Message.Content[1].Text != "answer" {
		t.Fatalf("text blocks = %#v", response.Message.Content)
	}
	call := response.Message.Content[2].ToolCall
	if call == nil || call.Name != "inspect" || string(call.Arguments) != `{"path":"README.md"}` {
		t.Fatalf("tool call = %#v", call)
	}
	if !stream.isClosed {
		t.Fatal("Collect() did not close stream")
	}
}

func TestCollectRejectsIncompleteToolCall(t *testing.T) {
	stream := &sliceStream{events: []StreamEvent{{
		Kind: StreamEventToolCallDelta,
		ToolCallDelta: &ToolCallDelta{
			Index:          0,
			Name:           "inspect",
			ArgumentsDelta: `{"path":`,
		},
	}}}
	_, err := Collect(t.Context(), stream)
	var modelErr *Error
	if !errors.As(err, &modelErr) || modelErr.Kind != ErrorProtocol {
		t.Fatalf("Collect() error = %#v", err)
	}
}

type sliceStream struct {
	events   []StreamEvent
	index    int
	isClosed bool
}

func (s *sliceStream) Next(context.Context) (StreamEvent, error) {
	if s.index >= len(s.events) {
		return StreamEvent{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *sliceStream) Close() error {
	s.isClosed = true
	return nil
}
