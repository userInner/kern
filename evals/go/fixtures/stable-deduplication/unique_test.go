package unique

import (
	"slices"
	"testing"
)

func TestStringsPreservesFirstSeenOrder(t *testing.T) {
	input := []string{"zeta", "alpha", "zeta", "beta", "alpha"}
	want := []string{"zeta", "alpha", "beta"}
	if got := Strings(input); !slices.Equal(got, want) {
		t.Fatalf("Strings() = %v, want %v", got, want)
	}
}

func TestStringsPreservesNil(t *testing.T) {
	if Strings(nil) != nil {
		t.Fatal("Strings(nil) did not return nil")
	}
}
