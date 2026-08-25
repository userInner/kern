package plugin

import (
	"encoding/json"
	"strings"
	"time"
)

// PreferenceMode is a task-level override. Disabled always wins over automatic
// or explicit enablement.
type PreferenceMode string

const (
	PreferenceEnable  PreferenceMode = "enable"
	PreferenceDisable PreferenceMode = "disable"
)

// Preference is one durable task-level plugin selection override.
type Preference struct {
	TaskID    string         `json:"task_id"`
	PluginID  string         `json:"plugin_id"`
	Mode      PreferenceMode `json:"mode"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// Usage is the immutable plugin version and resource set actually selected for
// one Attempt.
type Usage struct {
	SchemaVersion string          `json:"schema_version"`
	TaskID        string          `json:"task_id"`
	AttemptID     string          `json:"attempt_id"`
	PluginID      string          `json:"plugin_id"`
	Version       string          `json:"version"`
	Digest        string          `json:"digest"`
	Reason        string          `json:"reason"`
	Resources     json.RawMessage `json:"resources"`
	CreatedAt     time.Time       `json:"created_at"`
}

// ValidID reports whether value is a valid stable plugin identifier.
func ValidID(value string) bool {
	value = strings.TrimSpace(value)
	return len(value) <= 200 && pluginIDPattern.MatchString(value)
}
