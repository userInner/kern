package modelconfig

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	draft, err := Normalize(Draft{
		Name:      " Local Qwen ",
		Provider:  ProviderOllama,
		BaseURL:   "http://127.0.0.1:11434/",
		Model:     " qwen3 ",
		SecretRef: " keyring:model/config ",
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if draft.Name != "Local Qwen" || draft.BaseURL != "http://127.0.0.1:11434" ||
		draft.Model != "qwen3" || draft.SecretRef != "keyring:model/config" {
		t.Fatalf("Normalize() = %#v", draft)
	}
}

func TestNormalizeRejectsUnsafeBaseURL(t *testing.T) {
	for _, value := range []string{
		"file:///tmp/model",
		"https://user:secret@example.com",
		"https://example.com?api_key=secret",
		"https://example.com#secret",
	} {
		t.Run(value, func(t *testing.T) {
			_, err := Normalize(Draft{
				Name:     "model",
				Provider: ProviderOpenAICompatible,
				BaseURL:  value,
				Model:    "test",
			})
			if err == nil {
				t.Fatal("Normalize() error = nil")
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("errors.Is(%v, ErrInvalid) = false", err)
			}
		})
	}
}

func TestNormalizeInvalidDraftRetainsLocalDetail(t *testing.T) {
	_, err := Normalize(Draft{
		Name:     "model",
		Provider: ProviderOpenAICompatible,
		BaseURL:  "https://user:secret@example.com",
		Model:    "test",
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("errors.Is(%v, ErrInvalid) = false", err)
	}
	if !strings.Contains(err.Error(), "base url must not include credentials") {
		t.Fatalf("Normalize() error = %q, want local validation detail", err)
	}
}

func TestConfigJSONNeverIncludesSecretReference(t *testing.T) {
	encoded, err := json.Marshal(Config{SecretRef: "keyring:private", HasAPIKey: true})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if strings.Contains(string(encoded), "private") || !strings.Contains(string(encoded), `"has_api_key":true`) {
		t.Fatalf("JSON = %s", encoded)
	}
}
