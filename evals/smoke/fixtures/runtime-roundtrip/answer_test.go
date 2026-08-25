package answer

import "testing"

func TestValue(t *testing.T) {
	t.Parallel()
	if got := Value(); got != 42 {
		t.Fatalf("Value() = %d, want 42", got)
	}
}
