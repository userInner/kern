package retry

import (
	"context"
	"errors"
	"testing"
)

func TestDoDoesNotCallAfterCancellation(t *testing.T) {
	cause := errors.New("shutdown")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(cause)
	calls := 0
	err := Do(ctx, 3, func() error { calls++; return errors.New("retry") })
	if !errors.Is(err, cause) || calls != 0 {
		t.Fatalf("Do() error=%v calls=%d", err, calls)
	}
}
