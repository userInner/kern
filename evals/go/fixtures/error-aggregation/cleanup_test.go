package cleanup

import (
	"errors"
	"testing"
)

func TestRunPreservesEveryFailure(t *testing.T) {
	first := errors.New("first")
	second := errors.New("second")
	called := 0
	err := Run(
		func() error { called++; return first },
		func() error { called++; return second },
	)
	if called != 2 || !errors.Is(err, first) || !errors.Is(err, second) {
		t.Fatalf("Run() error=%v called=%d", err, called)
	}
}

func TestRunWithoutFailure(t *testing.T) {
	if err := Run(func() error { return nil }); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}
