package config

import (
	"strings"
	"testing"
)

func TestDecodeRejectsUnknownField(t *testing.T) {
	if _, err := Decode(strings.NewReader(`{"worker":4}`)); err == nil {
		t.Fatal("Decode accepted unknown field")
	}
}

func TestDecodeRejectsTrailingValue(t *testing.T) {
	if _, err := Decode(strings.NewReader(`{"workers":4} {"workers":5}`)); err == nil {
		t.Fatal("Decode accepted trailing JSON value")
	}
}

func TestDecodeValidConfig(t *testing.T) {
	got, err := Decode(strings.NewReader(`{"workers":4}`))
	if err != nil || got.Workers != 4 {
		t.Fatalf("Decode() = %#v, %v", got, err)
	}
}
