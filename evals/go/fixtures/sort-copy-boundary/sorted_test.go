package sorted

import (
	"slices"
	"testing"
)

func TestStringsDoesNotMutateInput(t *testing.T) {
	input := []string{"c", "a", "b"}
	got := Strings(input)
	if !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("Strings() = %v", got)
	}
	if !slices.Equal(input, []string{"c", "a", "b"}) {
		t.Fatalf("Strings mutated input: %v", input)
	}
}

func TestStringsPreservesNil(t *testing.T) {
	if Strings(nil) != nil {
		t.Fatal("Strings(nil) did not return nil")
	}
}
