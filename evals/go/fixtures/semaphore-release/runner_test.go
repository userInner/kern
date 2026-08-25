package runner

import (
	"errors"
	"testing"
	"time"
)

func TestRunnerReleasesSlotAfterFailure(t *testing.T) {
	runner := New(1)
	if err := runner.Run(true); !errors.Is(err, ErrFailed) {
		t.Fatalf("first Run() error = %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- runner.Run(false) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second Run() error = %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("second Run blocked because semaphore slot leaked")
	}
}
