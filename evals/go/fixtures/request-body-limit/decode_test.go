package decode

import (
	"strings"
	"testing"
)

func TestDecodeRequestRejectsOversizedBody(t *testing.T) {
	body := `{"message":"` + strings.Repeat("x", MaxBodyBytes) + `"}`
	if _, err := DecodeRequest(strings.NewReader(body)); err == nil {
		t.Fatal("DecodeRequest accepted oversized body")
	}
}

func TestDecodeRequestAcceptsSmallBody(t *testing.T) {
	request, err := DecodeRequest(strings.NewReader(`{"message":"ok"}`))
	if err != nil || request.Message != "ok" {
		t.Fatalf("DecodeRequest() = %#v, %v", request, err)
	}
}
