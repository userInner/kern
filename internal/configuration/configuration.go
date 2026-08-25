// Package configuration loads and atomically stores Kern's local configuration.
package configuration

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/userInner/kern/internal/policy"
)

const (
	SchemaVersion = "1"
	maxFileBytes  = 1 << 20
)

// Duration is a human-readable JSON duration such as "10m".
type Duration struct {
	time.Duration
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

func (d *Duration) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return errors.New("configuration: duration must be a string")
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("configuration: parsing duration: %w", err)
	}
	d.Duration = parsed
	return nil
}

// File is the strongly typed local configuration document.
type File struct {
	SchemaVersion string        `json:"schema_version"`
	Server        Server        `json:"server"`
	Storage       Storage       `json:"storage"`
	Runtime       Runtime       `json:"runtime"`
	Policy        Policy        `json:"policy"`
	Plugins       Plugins       `json:"plugins"`
	Eval          Eval          `json:"eval"`
	Observability Observability `json:"observability"`
}

type Server struct {
	Address string `json:"address"`
}

type Storage struct {
	DataDir       string `json:"data_dir"`
	WorkspaceDir  string `json:"workspace_dir"`
	RetentionDays int    `json:"retention_days"`
}

type Runtime struct {
	MaxActiveTasks int      `json:"max_active_tasks"`
	TaskTimeout    Duration `json:"task_timeout"`
	MaxTurns       int      `json:"max_turns"`
	MaxToolCalls   int      `json:"max_tool_calls"`
	MaxTokens      int      `json:"max_tokens"`
	MaxCostUSD     float64  `json:"max_cost_usd"`
}

type Policy struct {
	Profile string `json:"profile"`
}

type Plugins struct {
	AutoActivate bool `json:"auto_activate"`
}

type Eval struct {
	Workers int `json:"workers"`
}

type Observability struct {
	LogLevel       string `json:"log_level"`
	MetricsEnabled bool   `json:"metrics_enabled"`
}

// Defaults returns a complete safe local configuration.
func Defaults() File {
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = "."
	}
	return File{
		SchemaVersion: SchemaVersion,
		Server:        Server{Address: "127.0.0.1:8787"},
		Storage: Storage{
			DataDir:       filepath.Join(configDir, "kern"),
			RetentionDays: 90,
		},
		Runtime: Runtime{
			MaxActiveTasks: 2,
			TaskTimeout:    Duration{Duration: 10 * time.Minute},
			MaxTurns:       12,
			MaxToolCalls:   32,
			MaxTokens:      200_000,
			MaxCostUSD:     5,
		},
		Policy:        Policy{Profile: "local-safe"},
		Plugins:       Plugins{AutoActivate: true},
		Eval:          Eval{Workers: 2},
		Observability: Observability{LogLevel: "info"},
	}
}

// DefaultPath returns KERN_CONFIG or the operating system config location.
func DefaultPath() string {
	if path := strings.TrimSpace(os.Getenv("KERN_CONFIG")); path != "" {
		return path
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return filepath.Join(".", "kern-config.json")
	}
	return filepath.Join(configDir, "kern", "config.json")
}

// Load reads path over Defaults. A missing file returns Defaults without error.
func Load(path string) (File, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return File{}, errors.New("configuration: path is required")
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Defaults(), nil
	}
	if err != nil {
		return File{}, fmt.Errorf("configuration: opening file: %w", err)
	}
	defer file.Close()
	config := Defaults()
	decoder := json.NewDecoder(io.LimitReader(file, maxFileBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return File{}, fmt.Errorf("configuration: decoding file: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return File{}, errors.New("configuration: file must contain one JSON object")
	}
	if info, err := file.Stat(); err != nil {
		return File{}, fmt.Errorf("configuration: reading file metadata: %w", err)
	} else if info.Size() > maxFileBytes {
		return File{}, errors.New("configuration: file exceeds 1 MiB")
	}
	if err := Validate(config); err != nil {
		return File{}, err
	}
	return config, nil
}

// LoadEffective applies environment overrides after loading the config file.
func LoadEffective(path string) (File, error) {
	config, err := Load(path)
	if err != nil {
		return File{}, err
	}
	if value := env("KERN_SERVER_ADDRESS"); value != "" {
		config.Server.Address = value
	}
	if value := env("KERN_DATA_DIR"); value != "" {
		config.Storage.DataDir = value
	}
	if value := env("KERN_WORKSPACE"); value != "" {
		config.Storage.WorkspaceDir = value
	}
	if err := applyIntEnv("KERN_MAX_ACTIVE_TASKS", &config.Runtime.MaxActiveTasks); err != nil {
		return File{}, err
	}
	if value := env("KERN_TASK_TIMEOUT"); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return File{}, fmt.Errorf("configuration: KERN_TASK_TIMEOUT: %w", err)
		}
		config.Runtime.TaskTimeout = Duration{Duration: parsed}
	}
	if err := applyIntEnv("KERN_MAX_TURNS", &config.Runtime.MaxTurns); err != nil {
		return File{}, err
	}
	if err := applyIntEnv("KERN_MAX_TOOL_CALLS", &config.Runtime.MaxToolCalls); err != nil {
		return File{}, err
	}
	if err := applyIntEnv("KERN_MAX_TOKENS", &config.Runtime.MaxTokens); err != nil {
		return File{}, err
	}
	if value := env("KERN_MAX_COST_USD"); value != "" {
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return File{}, fmt.Errorf("configuration: KERN_MAX_COST_USD: %w", err)
		}
		config.Runtime.MaxCostUSD = parsed
	}
	if value := env("KERN_LOG_LEVEL"); value != "" {
		config.Observability.LogLevel = value
	}
	if value := env("KERN_METRICS_ENABLED"); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return File{}, fmt.Errorf("configuration: KERN_METRICS_ENABLED: %w", err)
		}
		config.Observability.MetricsEnabled = parsed
	}
	if err := Validate(config); err != nil {
		return File{}, err
	}
	return config, nil
}

func env(name string) string {
	return strings.TrimSpace(os.Getenv(name))
}

func applyIntEnv(name string, target *int) error {
	value := env(name)
	if value == "" {
		return nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("configuration: %s: %w", name, err)
	}
	*target = parsed
	return nil
}

// Validate checks every value before it reaches the application layer.
func Validate(config File) error {
	if config.SchemaVersion != SchemaVersion {
		return fmt.Errorf("configuration: unsupported schema version %q", config.SchemaVersion)
	}
	host, port, err := net.SplitHostPort(config.Server.Address)
	if err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") || port == "" {
		return errors.New("configuration: server.address must be a loopback host and port")
	}
	if strings.TrimSpace(config.Storage.DataDir) == "" {
		return errors.New("configuration: storage.data_dir is required")
	}
	if config.Storage.RetentionDays < 1 || config.Storage.RetentionDays > 3650 {
		return errors.New("configuration: storage.retention_days must be between 1 and 3650")
	}
	if config.Runtime.MaxActiveTasks < 1 || config.Runtime.MaxActiveTasks > 32 {
		return errors.New("configuration: runtime.max_active_tasks must be between 1 and 32")
	}
	if config.Runtime.TaskTimeout.Duration <= 0 || config.Runtime.TaskTimeout.Duration > 24*time.Hour {
		return errors.New("configuration: runtime.task_timeout must be between 1ns and 24h")
	}
	if config.Runtime.MaxTurns < 1 || config.Runtime.MaxTurns > 10_000 ||
		config.Runtime.MaxToolCalls < 1 || config.Runtime.MaxToolCalls > 100_000 ||
		config.Runtime.MaxTokens < 1 || config.Runtime.MaxTokens > 100_000_000 {
		return errors.New("configuration: runtime budgets are outside safe bounds")
	}
	if math.IsNaN(config.Runtime.MaxCostUSD) || math.IsInf(config.Runtime.MaxCostUSD, 0) ||
		config.Runtime.MaxCostUSD <= 0 || config.Runtime.MaxCostUSD > 1_000_000 {
		return errors.New("configuration: runtime.max_cost_usd is outside safe bounds")
	}
	if err := policy.ValidateProfile(policy.Profile(config.Policy.Profile)); err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	if config.Eval.Workers < 1 || config.Eval.Workers > 32 {
		return errors.New("configuration: eval.workers must be between 1 and 32")
	}
	switch config.Observability.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return errors.New("configuration: observability.log_level must be debug, info, warn, or error")
	}
	return nil
}

// Save validates and atomically replaces path with user-only permissions.
func Save(path string, config File) error {
	if err := Validate(config); err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("configuration: creating directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".kern-config-*")
	if err != nil {
		return fmt.Errorf("configuration: creating temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("configuration: restricting temporary file: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	writeErr := encoder.Encode(config)
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("configuration: writing temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("configuration: publishing file: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("configuration: restricting file: %w", err)
	}
	dir, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("configuration: opening directory for sync: %w", err)
	}
	syncErr = dir.Sync()
	closeErr = dir.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("configuration: syncing directory: %w", err)
	}
	return nil
}
