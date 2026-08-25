package plugin

import (
	"encoding/json"
	"testing"
)

func FuzzValidateManifest(f *testing.F) {
	valid, err := json.Marshal(validManifest(
		"sha256:0000000000000000000000000000000000000000000000000000000000000000",
	))
	if err != nil {
		f.Fatalf("Marshal(seed) error = %v", err)
	}
	f.Add(valid)
	f.Add([]byte(`{"schema_version":"1","id":"../escape"}`))
	f.Add([]byte(`{"entrypoints":{"knowledge":["../secret"]}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > MaxManifestBytes {
			t.Skip()
		}
		var manifest Manifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return
		}
		_ = ValidateManifest(manifest)
	})
}
