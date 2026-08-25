package worker

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestProcessStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	err := Process(ctx, []string{"a", "b", "c"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Process() error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(started); elapsed > 20*time.Millisecond {
		t.Fatalf("Process() took %s after cancellation", elapsed)
	}
}
