package name

import "testing"

func TestSafeRejectsPathForms(t *testing.T) {
	for _, value := range []string{"../secret", `..\secret`, "/tmp/secret", "folder/file"} {
		if _, err := Safe(value); err == nil {
			t.Errorf("Safe(%q) accepted path form", value)
		}
	}
}

func TestSafeAcceptsPortableBasename(t *testing.T) {
	got, err := Safe("report-2026.json")
	if err != nil || got != "report-2026.json" {
		t.Fatalf("Safe() = %q, %v", got, err)
	}
}
