package compatible

import (
	"strings"
	"testing"
)

func FuzzRollingSecretRedactor(f *testing.F) {
	f.Add("sk-provider-secret", "before sk-provider-secret after", []byte{1, 2, 3, 5})
	f.Add("aba", "ababa", []byte{2, 3})
	f.Add("E", "E", []byte{1})
	f.Add("", "ordinary output", []byte{4})
	f.Fuzz(func(t *testing.T, secret, value string, partitionHints []byte) {
		if len(secret) > 256 || len(value) > 64<<10 || len(partitionHints) > 1_024 {
			t.Skip()
		}
		redactor := newRollingSecretRedactor(secret)
		var output strings.Builder
		for offset, hintIndex := 0, 0; offset < len(value); hintIndex++ {
			step := 1
			if len(partitionHints) > 0 {
				step += int(partitionHints[hintIndex%len(partitionHints)] % 31)
			}
			end := min(offset+step, len(value))
			output.WriteString(redactor.Push(value[offset:end]))
			offset = end
		}
		output.WriteString(redactor.Flush())

		want := redactKnownSecret(value, secret)
		if output.String() != want {
			t.Fatalf("redacted output mismatch: got %q, want %q", output.String(), want)
		}
		if secret != "" && strings.Contains(output.String(), secret) {
			t.Fatalf("redacted output still contains the known secret")
		}
	})
}
