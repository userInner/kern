package page

import "testing"

func TestNextPageBoundaries(t *testing.T) {
	tests := []struct {
		current, size, total int
		want                 int
		ok                   bool
	}{
		{current: 0, size: 10, total: 11, want: 1, ok: true},
		{current: 0, size: 10, total: 10, want: 0, ok: false},
		{current: 1, size: 10, total: 20, want: 0, ok: false},
		{current: -1, size: 10, total: 20, want: 0, ok: false},
	}
	for _, test := range tests {
		got, ok := Next(test.current, test.size, test.total)
		if got != test.want || ok != test.ok {
			t.Errorf("Next(%d,%d,%d) = %d,%t, want %d,%t", test.current, test.size, test.total, got, ok, test.want, test.ok)
		}
	}
}
