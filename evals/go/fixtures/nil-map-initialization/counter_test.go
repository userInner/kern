package counter

import "testing"

func TestCounterFirstWrite(t *testing.T) {
	counter := New()
	counter.Add("jobs")
	if got := counter.Value("jobs"); got != 1 {
		t.Fatalf("Value(jobs) = %d, want 1", got)
	}
}

func TestCounterZeroValue(t *testing.T) {
	var counter Counter
	counter.Add("jobs")
	if got := counter.Value("jobs"); got != 1 {
		t.Fatalf("Value(jobs) = %d, want 1", got)
	}
}
