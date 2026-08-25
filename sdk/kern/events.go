package kern

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// EventStream incrementally decodes one authenticated SSE response.
type EventStream struct {
	body    io.ReadCloser
	scanner *bufio.Scanner
}

// OpenEventStream starts a replayable stream after the supplied event ID.
func (c *Client) OpenEventStream(
	ctx context.Context,
	taskID string,
	after int64,
) (*EventStream, error) {
	if after < 0 {
		return nil, errors.New("kern: event cursor must be non-negative")
	}
	request, err := c.newRequest(ctx, http.MethodGet, taskPath(taskID, "events"), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Last-Event-ID", strconv.FormatInt(after, 10))
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("kern: opening event stream: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, decodeAPIError(response)
	}
	return &EventStream{body: response.Body, scanner: bufio.NewScanner(response.Body)}, nil
}

// Next blocks until the next event, context cancellation, or stream end.
func (s *EventStream) Next() (Event, error) {
	if s == nil || s.scanner == nil {
		return Event{}, errors.New("kern: event stream is not open")
	}
	var data strings.Builder
	for s.scanner.Scan() {
		line := s.scanner.Text()
		if line == "" {
			if data.Len() == 0 {
				continue
			}
			var event Event
			if err := json.Unmarshal([]byte(data.String()), &event); err != nil {
				return Event{}, fmt.Errorf("kern: decoding event: %w", err)
			}
			return event, nil
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(value, " "))
		}
	}
	if err := s.scanner.Err(); err != nil {
		return Event{}, fmt.Errorf("kern: reading event stream: %w", err)
	}
	return Event{}, io.EOF
}

// Close releases the event stream connection.
func (s *EventStream) Close() error {
	if s == nil || s.body == nil {
		return nil
	}
	return s.body.Close()
}
