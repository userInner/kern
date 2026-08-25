package window

import (
	"slices"
	"testing"
)

func TestLastN(t *testing.T) {
	tests := []struct {
		name   string
		values []int
		count  int
		want   []int
	}{
		{name: "tail", values: []int{1, 2, 3}, count: 2, want: []int{2, 3}},
		{name: "whole input", values: []int{1, 2, 3}, count: 3, want: []int{1, 2, 3}},
		{name: "zero", values: []int{1, 2, 3}, count: 0, want: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := LastN(test.values, test.count)
			if !slices.Equal(got, test.want) {
				t.Fatalf("LastN(%v, %d) = %v, want %v", test.values, test.count, got, test.want)
			}
		})
	}
}
