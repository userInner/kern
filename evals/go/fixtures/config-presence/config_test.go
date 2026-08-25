package config

import (
	"strings"
	"testing"
)

func TestDecodeDefaultsMissingWorkers(t *testing.T) {
	got, err := Decode(strings.NewReader(`{}`))
	if err != nil || got.Workers != 4 {
		t.Fatalf("Decode({}) = %#v, %v", got, err)
	}
}

func TestDecodePreservesExplicitZero(t *testing.T) {
	got, err := Decode(strings.NewReader(`{"workers":0}`))
	if err != nil || got.Workers != 0 {
		t.Fatalf("Decode(explicit zero) = %#v, %v", got, err)
	}
}

func TestDecodeRejectsTrailingValue(t *testing.T) {
	if _, err := Decode(strings.NewReader(`{} {}`)); err == nil {
		t.Fatal("Decode accepted trailing JSON")
	}
}
