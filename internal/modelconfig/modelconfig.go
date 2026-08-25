// Package modelconfig defines durable, provider-neutral model connection settings.
package modelconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const SchemaVersion = "1"

var (
	ErrNotFound = errors.New("modelconfig: not found")
	ErrConflict = errors.New("modelconfig: name already exists")
	ErrDisabled = errors.New("modelconfig: disabled")
	ErrInvalid  = errors.New("modelconfig: invalid")
)

// Provider identifies a supported model connection protocol.
type Provider string

const (
	ProviderUnknown          Provider = ""
	ProviderOpenAICompatible Provider = "openai-compatible"
	ProviderOllama           Provider = "ollama"
)

// Config is the public-safe snapshot of one model connection. SecretRef is
// intentionally excluded from JSON; API clients only learn whether a key exists.
type Config struct {
	SchemaVersion string    `json:"schema_version"`
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Provider      Provider  `json:"provider"`
	BaseURL       string    `json:"base_url"`
	Model         string    `json:"model"`
	SecretRef     string    `json:"-"`
	HasAPIKey     bool      `json:"has_api_key"`
	Enabled       bool      `json:"enabled"`
	IsDefault     bool      `json:"is_default"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Draft contains validated non-secret settings and an opaque Secret Store reference.
type Draft struct {
	Name       string
	Provider   Provider
	BaseURL    string
	Model      string
	SecretRef  string
	Enabled    bool
	SetDefault bool
}

// Selection is the immutable model connection snapshot recorded for one Attempt.
// It deliberately contains no secret reference or secret value.
type Selection struct {
	SchemaVersion string          `json:"schema_version"`
	TaskID        string          `json:"task_id"`
	AttemptID     string          `json:"attempt_id"`
	ConfigID      string          `json:"config_id,omitempty"`
	Provider      Provider        `json:"provider"`
	BaseURL       string          `json:"base_url"`
	Model         string          `json:"model"`
	Parameters    json.RawMessage `json:"parameters"`
	CreatedAt     time.Time       `json:"created_at"`
}

// TestResult reports a live provider probe without exposing response content or credentials.
type TestResult struct {
	SchemaVersion string          `json:"schema_version"`
	ConfigID      string          `json:"config_id"`
	Provider      Provider        `json:"provider"`
	Model         string          `json:"model"`
	OK            bool            `json:"ok"`
	LatencyMS     int64           `json:"latency_ms"`
	RequestID     string          `json:"request_id,omitempty"`
	Capabilities  map[string]bool `json:"capabilities"`
}

// Normalize validates a draft and returns its canonical representation.
func Normalize(draft Draft) (Draft, error) {
	draft.Name = strings.TrimSpace(draft.Name)
	draft.Model = strings.TrimSpace(draft.Model)
	draft.SecretRef = strings.TrimSpace(draft.SecretRef)
	if draft.Name == "" || len([]rune(draft.Name)) > 100 {
		return Draft{}, fmt.Errorf("%w: name must contain 1 to 100 characters", ErrInvalid)
	}
	if draft.Provider != ProviderOpenAICompatible && draft.Provider != ProviderOllama {
		return Draft{}, fmt.Errorf("%w: unsupported provider %q", ErrInvalid, draft.Provider)
	}
	if draft.Model == "" || len(draft.Model) > 256 {
		return Draft{}, fmt.Errorf("%w: model must contain 1 to 256 bytes", ErrInvalid)
	}
	if len(draft.SecretRef) > 512 {
		return Draft{}, fmt.Errorf("%w: secret reference exceeds 512 bytes", ErrInvalid)
	}
	if draft.SetDefault && !draft.Enabled {
		return Draft{}, fmt.Errorf("%w: a disabled config cannot be the default", ErrInvalid)
	}

	baseURL, err := normalizeBaseURL(draft.BaseURL)
	if err != nil {
		return Draft{}, err
	}
	draft.BaseURL = baseURL
	return draft, nil
}

func normalizeBaseURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) > 2048 {
		return "", fmt.Errorf("%w: base url exceeds 2048 bytes", ErrInvalid)
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("%w: parsing base url: %w", ErrInvalid, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("%w: base url must use http or https", ErrInvalid)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("%w: base url must include a host", ErrInvalid)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("%w: base url must not include credentials", ErrInvalid)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: base url must not include a query or fragment", ErrInvalid)
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}
