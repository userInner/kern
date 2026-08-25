package jsonschema

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateStrictNestedSchema(t *testing.T) {
	schema := json.RawMessage(`{
	  "type":"object",
	  "additionalProperties":false,
	  "required":["name","count","tags"],
	  "properties":{
	    "name":{"type":"string","minLength":2,"pattern":"^[a-z]+$"},
	    "count":{"type":"integer","minimum":1,"maximum":5},
	    "tags":{"type":"array","minItems":1,"items":{"type":"string","enum":["safe","fast"]}}
	  }
	}`)
	if err := Validate(schema, json.RawMessage(`{"name":"kern","count":1e0,"tags":["safe"]}`)); err != nil {
		t.Fatalf("Validate(valid) error = %v", err)
	}
	for _, document := range []string{
		`{"name":"K","count":1,"tags":["safe"]}`,
		`{"name":"kern","count":6,"tags":["safe"]}`,
		`{"name":"kern","count":1,"tags":["other"]}`,
		`{"name":"kern","count":1,"tags":["safe"],"extra":true}`,
	} {
		if err := Validate(schema, json.RawMessage(document)); err == nil {
			t.Errorf("Validate(%s) error = nil", document)
		}
	}
}

func TestCompileRejectsUnknownOrMisappliedKeywords(t *testing.T) {
	for _, schema := range []string{
		`{"type":"object","unknown":true}`,
		`{"type":"string","properties":{"x":{"type":"string"}}}`,
		`{"type":"object","required":["missing"]}`,
		`{"type":"array","minItems":2,"maxItems":1}`,
	} {
		if _, err := Compile(json.RawMessage(schema)); err == nil {
			t.Errorf("Compile(%s) error = nil", schema)
		}
	}
}

func TestValidateReturnsUsefulLocation(t *testing.T) {
	err := Validate(
		json.RawMessage(`{"type":"object","properties":{"enabled":{"type":"boolean"}}}`),
		json.RawMessage(`{"enabled":"yes"}`),
	)
	if err == nil || !strings.Contains(err.Error(), "$.enabled") {
		t.Fatalf("Validate() error = %v", err)
	}
}
