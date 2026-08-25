package load

import (
	"errors"
	"strings"
	"testing"
)

func TestRecordPreservesCause(t *testing.T) {
	err := Record("missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Record() error = %v, want ErrNotFound cause", err)
	}
	if !strings.Contains(err.Error(), `load record "missing"`) {
		t.Fatalf("Record() error = %v, want operation context", err)
	}
}
