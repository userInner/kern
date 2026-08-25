package redact

import "testing"

func TestSecretRedactsEveryOccurrence(t *testing.T) {
	got := Secret("token=abc; retry=abc", "abc")
	if got != "token=[REDACTED]; retry=[REDACTED]" {
		t.Fatalf("Secret() = %q", got)
	}
}

func TestSecretIgnoresEmptySecret(t *testing.T) {
	const input = "unchanged"
	if got := Secret(input, ""); got != input {
		t.Fatalf("Secret(empty) = %q", got)
	}
}
