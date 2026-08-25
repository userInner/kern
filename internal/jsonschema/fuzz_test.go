package jsonschema

import (
	"encoding/json"
	"testing"
)

func FuzzCompileAndValidate(f *testing.F) {
	f.Add(
		[]byte(`{"type":"object","additionalProperties":false,"properties":{"name":{"type":"string"}}}`),
		[]byte(`{"name":"kern"}`),
	)
	f.Add([]byte(`{"type":"array","items":{"type":"integer"}}`), []byte(`[1,2,3]`))
	f.Add([]byte(`{"type":"string","pattern":"^[a-z]+$"}`), []byte(`"kern"`))
	f.Fuzz(func(t *testing.T, schemaData, document []byte) {
		if len(schemaData) > 256<<10 || len(document) > 256<<10 {
			t.Skip()
		}
		schema, err := Compile(json.RawMessage(schemaData))
		if err != nil {
			return
		}
		_ = schema.Validate(json.RawMessage(document))
	})
}
