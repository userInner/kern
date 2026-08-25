package id

import (
	"regexp"
	"testing"
)

func TestNew(t *testing.T) {
	t.Parallel()

	first, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	second, err := New()
	if err != nil {
		t.Fatalf("New() second error = %v", err)
	}
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !pattern.MatchString(first) {
		t.Fatalf("New() = %q, want UUIDv7", first)
	}
	if first == second {
		t.Fatal("New() returned duplicate identifiers")
	}
}
