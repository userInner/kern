package clone

import "testing"

func TestCloneDoesNotShareSlices(t *testing.T) {
	source := map[string][]string{"roles": {"reader", "writer"}}
	result := Clone(source)
	result["roles"][0] = "admin"
	if source["roles"][0] != "reader" {
		t.Fatalf("Clone result mutated source: %#v", source)
	}
}

func TestClonePreservesNilValues(t *testing.T) {
	if Clone(nil) != nil {
		t.Fatal("Clone(nil) did not return nil")
	}
	result := Clone(map[string][]string{"empty": nil})
	if result["empty"] != nil {
		t.Fatalf("Clone changed nil slice: %#v", result["empty"])
	}
}
