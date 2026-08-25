package snapshot

import "testing"

func TestSnapshotIsIndependent(t *testing.T) {
	source := []string{"queued", "running"}
	got := Snapshot(source)
	got[0] = "failed"
	if source[0] != "queued" {
		t.Fatalf("Snapshot result mutated source: %v", source)
	}
}

func TestSnapshotPreservesNil(t *testing.T) {
	if got := Snapshot(nil); got != nil {
		t.Fatalf("Snapshot(nil) = %#v, want nil", got)
	}
}
