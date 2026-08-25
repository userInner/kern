package quota

import (
	"math"
	"testing"
)

func TestAddDetectsOverflow(t *testing.T) {
	for _, values := range [][2]int64{{math.MaxInt64, 1}, {math.MinInt64, -1}} {
		if _, err := Add(values[0], values[1]); err == nil {
			t.Errorf("Add(%d, %d) accepted overflow", values[0], values[1])
		}
	}
}

func TestAddNormalValues(t *testing.T) {
	got, err := Add(20, 22)
	if err != nil || got != 42 {
		t.Fatalf("Add(20, 22) = %d, %v", got, err)
	}
}
