package window

import (
	"testing"
	"time"
)

func TestWindowIsHalfOpen(t *testing.T) {
	start := time.Unix(100, 0)
	end := time.Unix(200, 0)
	window, err := New(start, end)
	if err != nil {
		t.Fatal(err)
	}
	if !window.Contains(start) || !window.Contains(time.Unix(150, 0)) {
		t.Fatal("Window excluded start or interior instant")
	}
	if window.Contains(end) {
		t.Fatal("Window included end instant")
	}
}

func TestNewRejectsEmptyWindow(t *testing.T) {
	now := time.Now()
	if _, err := New(now, now); err == nil {
		t.Fatal("New accepted empty window")
	}
}
