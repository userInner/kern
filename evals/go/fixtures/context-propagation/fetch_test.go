package fetch

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

type cancelClient struct{}

var errCancellationNotPropagated = errors.New("request context was not canceled")

func (cancelClient) Do(request *http.Request) (*http.Response, error) {
	select {
	case <-request.Context().Done():
		return nil, request.Context().Err()
	case <-time.After(100 * time.Millisecond):
		return nil, errCancellationNotPropagated
	}
}

func TestFetchPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Fetch(ctx, cancelClient{}, "https://example.test")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Fetch() error = %v, want context.Canceled", err)
	}
}
