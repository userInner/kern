package secret

import (
	"errors"
	"testing"
)

func TestEnvironmentResolve(t *testing.T) {
	t.Setenv("KERN_TEST_SECRET", "private-value")
	reference, err := Ref("KERN_TEST_SECRET")
	if err != nil {
		t.Fatalf("Ref() error = %v", err)
	}
	value, err := (Environment{}).Resolve(t.Context(), reference)
	if err != nil || value != "private-value" {
		t.Fatalf("Resolve() = %q, %v", value, err)
	}
}

func TestEnvironmentRejectsUnsupportedReference(t *testing.T) {
	_, err := (Environment{}).Resolve(t.Context(), "file:/tmp/secret")
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Resolve() error = %v", err)
	}
}
