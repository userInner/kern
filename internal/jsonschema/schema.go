// Package jsonschema implements the strict, dependency-free JSON Schema
// subset used at Kern trust boundaries. Unsupported keywords are rejected
// instead of being silently ignored.
package jsonschema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	maxDepth = 64
	maxNodes = 4_096
)

// Schema is an immutable compiled validation tree.
type Schema struct {
	typeName             string
	required             map[string]bool
	additionalProperties bool
	properties           map[string]*Schema
	items                *Schema
	enum                 []any
	minLength            *int
	maxLength            *int
	pattern              *regexp.Regexp
	minItems             *int
	maxItems             *int
	minProperties        *int
	maxProperties        *int
	minimum              *big.Rat
	maximum              *big.Rat
}

type rawSchema struct {
	Dialect              string                     `json:"$schema,omitempty"`
	Title                string                     `json:"title,omitempty"`
	Description          string                     `json:"description,omitempty"`
	Type                 string                     `json:"type"`
	Required             []string                   `json:"required,omitempty"`
	AdditionalProperties *bool                      `json:"additionalProperties,omitempty"`
	Properties           map[string]json.RawMessage `json:"properties,omitempty"`
	Items                json.RawMessage            `json:"items,omitempty"`
	Enum                 []json.RawMessage          `json:"enum,omitempty"`
	MinLength            *int                       `json:"minLength,omitempty"`
	MaxLength            *int                       `json:"maxLength,omitempty"`
	Pattern              string                     `json:"pattern,omitempty"`
	MinItems             *int                       `json:"minItems,omitempty"`
	MaxItems             *int                       `json:"maxItems,omitempty"`
	MinProperties        *int                       `json:"minProperties,omitempty"`
	MaxProperties        *int                       `json:"maxProperties,omitempty"`
	Minimum              *json.Number               `json:"minimum,omitempty"`
	Maximum              *json.Number               `json:"maximum,omitempty"`
}

// Compile strictly parses one schema and all nested schemas.
func Compile(data json.RawMessage) (*Schema, error) {
	nodes := 0
	return compile(data, 0, &nodes)
}

// Validate compiles schemaData and validates exactly one JSON document.
func Validate(schemaData, document json.RawMessage) error {
	schema, err := Compile(schemaData)
	if err != nil {
		return err
	}
	return schema.Validate(document)
}

// RootType reports the schema's required top-level JSON type.
func (s *Schema) RootType() string {
	if s == nil {
		return ""
	}
	return s.typeName
}

// Validate checks exactly one JSON document against the compiled schema.
func (s *Schema) Validate(document json.RawMessage) error {
	if s == nil {
		return errors.New("jsonschema: nil schema")
	}
	var value any
	if err := decodeStrict(document, &value, true); err != nil {
		return fmt.Errorf("jsonschema: invalid document: %w", err)
	}
	return s.validate(value, "$")
}

func compile(data json.RawMessage, depth int, nodes *int) (*Schema, error) {
	if depth > maxDepth || *nodes >= maxNodes {
		return nil, errors.New("jsonschema: schema exceeds structural limits")
	}
	*nodes++
	var raw rawSchema
	if err := decodeStrict(data, &raw, true); err != nil {
		return nil, fmt.Errorf("jsonschema: invalid schema: %w", err)
	}
	schema := &Schema{
		typeName:             raw.Type,
		required:             make(map[string]bool, len(raw.Required)),
		additionalProperties: true,
		properties:           make(map[string]*Schema, len(raw.Properties)),
		minLength:            raw.MinLength,
		maxLength:            raw.MaxLength,
		minItems:             raw.MinItems,
		maxItems:             raw.MaxItems,
		minProperties:        raw.MinProperties,
		maxProperties:        raw.MaxProperties,
	}
	if raw.AdditionalProperties != nil {
		schema.additionalProperties = *raw.AdditionalProperties
	}
	switch raw.Type {
	case "object", "array", "string", "integer", "number", "boolean", "null":
	default:
		return nil, fmt.Errorf("jsonschema: unsupported or missing type %q", raw.Type)
	}
	if err := validateBounds(raw); err != nil {
		return nil, err
	}
	for _, name := range raw.Required {
		if name == "" || schema.required[name] {
			return nil, errors.New("jsonschema: required properties must be unique and non-empty")
		}
		schema.required[name] = true
	}
	for name, childData := range raw.Properties {
		if name == "" {
			return nil, errors.New("jsonschema: property names must be non-empty")
		}
		child, err := compile(childData, depth+1, nodes)
		if err != nil {
			return nil, fmt.Errorf("jsonschema: property %q: %w", name, err)
		}
		schema.properties[name] = child
	}
	for name := range schema.required {
		if _, ok := schema.properties[name]; !ok {
			return nil, fmt.Errorf("jsonschema: required property %q has no schema", name)
		}
	}
	if len(raw.Items) > 0 {
		child, err := compile(raw.Items, depth+1, nodes)
		if err != nil {
			return nil, fmt.Errorf("jsonschema: items: %w", err)
		}
		schema.items = child
	}
	if raw.Pattern != "" {
		compiled, err := regexp.Compile(raw.Pattern)
		if err != nil {
			return nil, fmt.Errorf("jsonschema: invalid pattern: %w", err)
		}
		schema.pattern = compiled
	}
	for _, enumData := range raw.Enum {
		var candidate any
		if err := decodeStrict(enumData, &candidate, false); err != nil {
			return nil, fmt.Errorf("jsonschema: invalid enum: %w", err)
		}
		schema.enum = append(schema.enum, candidate)
	}
	var err error
	if schema.minimum, err = rational(raw.Minimum); err != nil {
		return nil, fmt.Errorf("jsonschema: invalid minimum: %w", err)
	}
	if schema.maximum, err = rational(raw.Maximum); err != nil {
		return nil, fmt.Errorf("jsonschema: invalid maximum: %w", err)
	}
	if schema.minimum != nil && schema.maximum != nil && schema.minimum.Cmp(schema.maximum) > 0 {
		return nil, errors.New("jsonschema: minimum exceeds maximum")
	}
	if err := validateKeywordApplicability(raw); err != nil {
		return nil, err
	}
	return schema, nil
}

func (s *Schema) validate(value any, location string) error {
	if len(s.enum) > 0 {
		matched := false
		for _, candidate := range s.enum {
			if equalValue(value, candidate) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("jsonschema: %s is outside enum", location)
		}
	}
	switch s.typeName {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("jsonschema: %s must be an object", location)
		}
		if s.minProperties != nil && len(object) < *s.minProperties ||
			s.maxProperties != nil && len(object) > *s.maxProperties {
			return fmt.Errorf("jsonschema: %s property count is outside bounds", location)
		}
		for name := range s.required {
			if _, ok := object[name]; !ok {
				return fmt.Errorf("jsonschema: %s.%s is required", location, name)
			}
		}
		for name, child := range object {
			childSchema, known := s.properties[name]
			if !known {
				if !s.additionalProperties {
					return fmt.Errorf("jsonschema: %s.%s is not allowed", location, name)
				}
				continue
			}
			if err := childSchema.validate(child, location+"."+safeName(name)); err != nil {
				return err
			}
		}
	case "array":
		array, ok := value.([]any)
		if !ok {
			return fmt.Errorf("jsonschema: %s must be an array", location)
		}
		if s.minItems != nil && len(array) < *s.minItems || s.maxItems != nil && len(array) > *s.maxItems {
			return fmt.Errorf("jsonschema: %s item count is outside bounds", location)
		}
		if s.items != nil {
			for index, item := range array {
				if err := s.items.validate(item, fmt.Sprintf("%s[%d]", location, index)); err != nil {
					return err
				}
			}
		}
	case "string":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("jsonschema: %s must be a string", location)
		}
		length := utf8.RuneCountInString(text)
		if s.minLength != nil && length < *s.minLength || s.maxLength != nil && length > *s.maxLength {
			return fmt.Errorf("jsonschema: %s length is outside bounds", location)
		}
		if s.pattern != nil && !s.pattern.MatchString(text) {
			return fmt.Errorf("jsonschema: %s does not match pattern", location)
		}
	case "integer", "number":
		number, ok := value.(json.Number)
		if !ok {
			return fmt.Errorf("jsonschema: %s must be a number", location)
		}
		valueRat, ok := new(big.Rat).SetString(number.String())
		if !ok || s.typeName == "integer" && !valueRat.IsInt() {
			return fmt.Errorf("jsonschema: %s must be %s", location, s.typeName)
		}
		if s.minimum != nil && valueRat.Cmp(s.minimum) < 0 || s.maximum != nil && valueRat.Cmp(s.maximum) > 0 {
			return fmt.Errorf("jsonschema: %s is outside numeric bounds", location)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("jsonschema: %s must be a boolean", location)
		}
	case "null":
		if value != nil {
			return fmt.Errorf("jsonschema: %s must be null", location)
		}
	}
	return nil
}

func decodeStrict(data []byte, target any, disallowUnknown bool) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if disallowUnknown {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("expected exactly one JSON value")
	}
	return nil
}

func validateBounds(raw rawSchema) error {
	for _, bound := range []*int{
		raw.MinLength, raw.MaxLength, raw.MinItems, raw.MaxItems, raw.MinProperties, raw.MaxProperties,
	} {
		if bound != nil && *bound < 0 {
			return errors.New("jsonschema: bounds must be non-negative")
		}
	}
	if raw.MinLength != nil && raw.MaxLength != nil && *raw.MinLength > *raw.MaxLength ||
		raw.MinItems != nil && raw.MaxItems != nil && *raw.MinItems > *raw.MaxItems ||
		raw.MinProperties != nil && raw.MaxProperties != nil && *raw.MinProperties > *raw.MaxProperties {
		return errors.New("jsonschema: minimum bound exceeds maximum")
	}
	return nil
}

func validateKeywordApplicability(raw rawSchema) error {
	objectKeywords := len(raw.Required) > 0 || raw.AdditionalProperties != nil || len(raw.Properties) > 0 ||
		raw.MinProperties != nil || raw.MaxProperties != nil
	arrayKeywords := len(raw.Items) > 0 || raw.MinItems != nil || raw.MaxItems != nil
	stringKeywords := raw.MinLength != nil || raw.MaxLength != nil || raw.Pattern != ""
	numberKeywords := raw.Minimum != nil || raw.Maximum != nil
	if objectKeywords && raw.Type != "object" || arrayKeywords && raw.Type != "array" ||
		stringKeywords && raw.Type != "string" || numberKeywords && raw.Type != "integer" && raw.Type != "number" {
		return errors.New("jsonschema: keyword does not apply to declared type")
	}
	return nil
}

func rational(number *json.Number) (*big.Rat, error) {
	if number == nil {
		return nil, nil
	}
	value, ok := new(big.Rat).SetString(number.String())
	if !ok {
		return nil, errors.New("not a finite JSON number")
	}
	return value, nil
}

func equalValue(left, right any) bool {
	leftNumber, leftIsNumber := left.(json.Number)
	rightNumber, rightIsNumber := right.(json.Number)
	if leftIsNumber || rightIsNumber {
		if !leftIsNumber || !rightIsNumber {
			return false
		}
		leftRat, leftOK := new(big.Rat).SetString(leftNumber.String())
		rightRat, rightOK := new(big.Rat).SetString(rightNumber.String())
		return leftOK && rightOK && leftRat.Cmp(rightRat) == 0
	}
	return reflect.DeepEqual(left, right)
}

// Ensure error paths never contain control characters from property names.
func safeName(value string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 {
			return -1
		}
		return r
	}, value)
}
