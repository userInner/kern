package workspace

import (
	"path/filepath"
	"strings"
	"testing"
)

func FuzzCleanPathConfinement(f *testing.F) {
	for _, seed := range []string{
		".", "README.md", "src/main.go", "../secret", "/etc/passwd", `..\\secret`, "\x00", ".env", "nested/.git/config",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 4_096 {
			t.Skip()
		}
		clean, err := cleanPath(value)
		if err != nil {
			return
		}
		if filepath.IsAbs(clean) || !filepath.IsLocal(clean) || strings.ContainsRune(clean, 0) {
			t.Fatalf("cleanPath accepted non-local path %q as %q", value, clean)
		}
		_ = allowPath(clean)
	})
}
