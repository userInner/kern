// Package evaluation defines reproducible Kern evaluation suites and reports.
package evaluation

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/userInner/kern/internal/jsonschema"
)

const (
	SchemaVersion  = "1"
	maxSuiteBytes  = 1 << 20
	maxPromptBytes = 256 << 10
)

var (
	ErrInvalidSuite = errors.New("evaluation: invalid suite")
	idPattern       = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)+$`)
	versionPattern  = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?$`)
	envNamePattern  = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)
)

// Suite is an immutable collection of cases and comparable variants.
type Suite struct {
	SchemaVersion string    `json:"schema_version"`
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Version       string    `json:"version"`
	Defaults      Defaults  `json:"defaults"`
	Variants      []Variant `json:"variants"`
	Cases         []Case    `json:"cases"`
	Root          string    `json:"-"`
}

// Defaults fixes execution conditions shared by every case and variant.
type Defaults struct {
	TimeoutMS        int64        `json:"timeout_ms"`
	TokenBudget      int          `json:"token_budget"`
	CostBudgetMicros int64        `json:"cost_budget_micros"`
	Retries          int          `json:"retries"`
	WorkerCount      int          `json:"worker_count"`
	AllowedCommands  []string     `json:"allowed_commands"`
	Judge            *JudgeConfig `json:"judge,omitempty"`
}

// JudgeConfig fixes the supplemental model grader identity and safe secret
// reference. API key values are never stored in a suite or report.
type JudgeConfig struct {
	Provider  string `json:"provider"`
	BaseURL   string `json:"base_url"`
	Model     string `json:"model"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
	MaxTokens int    `json:"max_tokens"`
}

// Variant is one fixed agent/plugin configuration.
type Variant struct {
	ID            string            `json:"id"`
	Agent         string            `json:"agent,omitempty"`
	AgentVersion  string            `json:"agent_version,omitempty"`
	Model         string            `json:"model,omitempty"`
	Plugins       []string          `json:"plugins"`
	PluginDigests map[string]string `json:"plugin_digests,omitempty"`
}

// Case is one prompt, isolated fixture, and deterministic grading contract.
type Case struct {
	ID      string   `json:"id"`
	Fixture string   `json:"fixture"`
	Prompt  string   `json:"prompt"`
	Graders []Grader `json:"graders"`
}

// Grader is a deterministic post-run check.
type Grader struct {
	ID                  string          `json:"id"`
	Type                string          `json:"type"`
	Required            bool            `json:"required"`
	Command             []string        `json:"command,omitempty"`
	Path                string          `json:"path,omitempty"`
	Contains            string          `json:"contains,omitempty"`
	Schema              json.RawMessage `json:"schema,omitempty"`
	AllowedPaths        []string        `json:"allowed_paths,omitempty"`
	ForbiddenPaths      []string        `json:"forbidden_paths,omitempty"`
	MinChangedFiles     int             `json:"min_changed_files,omitempty"`
	MaxChangedFiles     int             `json:"max_changed_files,omitempty"`
	MaxSafetyViolations *int            `json:"max_safety_violations,omitempty"`
	Instructions        string          `json:"instructions,omitempty"`
	EvidencePaths       []string        `json:"evidence_paths,omitempty"`
}

// Load reads and strictly validates one JSON evaluation suite. JSON is the
// canonical v1 representation; a YAML adapter can map into the same contract.
func Load(name string) (Suite, error) {
	info, err := os.Stat(name)
	if err != nil {
		return Suite{}, fmt.Errorf("evaluation: reading suite path: %w", err)
	}
	if info.IsDir() {
		name = filepath.Join(name, "suite.json")
	}
	file, err := os.Open(name)
	if err != nil {
		return Suite{}, fmt.Errorf("evaluation: opening suite: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxSuiteBytes+1))
	if err != nil {
		return Suite{}, fmt.Errorf("evaluation: reading suite: %w", err)
	}
	if len(data) > maxSuiteBytes {
		return Suite{}, fmt.Errorf("%w: suite exceeds 1 MiB", ErrInvalidSuite)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var suite Suite
	if err := decoder.Decode(&suite); err != nil {
		return Suite{}, fmt.Errorf("%w: %v", ErrInvalidSuite, err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Suite{}, fmt.Errorf("%w: suite must contain one JSON object", ErrInvalidSuite)
	}
	absolute, err := filepath.Abs(filepath.Dir(name))
	if err != nil {
		return Suite{}, fmt.Errorf("evaluation: resolving suite root: %w", err)
	}
	suite.Root = absolute
	if err := Validate(suite); err != nil {
		return Suite{}, err
	}
	return suite, nil
}

// Validate rejects ambiguous, unsafe, or irreproducible suite definitions.
func Validate(suite Suite) error {
	if suite.SchemaVersion != SchemaVersion || !validID(suite.ID) ||
		strings.TrimSpace(suite.Name) == "" || !versionPattern.MatchString(suite.Version) {
		return ErrInvalidSuite
	}
	if suite.Defaults.TimeoutMS < 1_000 || suite.Defaults.TimeoutMS > int64((24*time.Hour)/time.Millisecond) ||
		suite.Defaults.TokenBudget < 1 || suite.Defaults.CostBudgetMicros < 0 ||
		suite.Defaults.Retries < 0 || suite.Defaults.Retries > 10 ||
		suite.Defaults.WorkerCount < 1 || suite.Defaults.WorkerCount > 32 {
		return fmt.Errorf("%w: invalid execution defaults", ErrInvalidSuite)
	}
	if len(suite.Defaults.AllowedCommands) == 0 || len(suite.Defaults.AllowedCommands) > 64 {
		return fmt.Errorf("%w: allowed_commands is required", ErrInvalidSuite)
	}
	for _, executable := range suite.Defaults.AllowedCommands {
		if !safeExecutable(executable) {
			return fmt.Errorf("%w: invalid allowed command %q", ErrInvalidSuite, executable)
		}
	}
	if suite.Defaults.Judge != nil {
		if err := validateJudge(*suite.Defaults.Judge); err != nil {
			return fmt.Errorf("%w: invalid model judge: %v", ErrInvalidSuite, err)
		}
	}
	if len(suite.Variants) < 1 || len(suite.Variants) > 16 || len(suite.Cases) < 1 || len(suite.Cases) > 500 {
		return fmt.Errorf("%w: invalid variant or case count", ErrInvalidSuite)
	}
	seenVariants := make(map[string]bool, len(suite.Variants))
	for _, variant := range suite.Variants {
		if !validID(variant.ID) || seenVariants[variant.ID] {
			return fmt.Errorf("%w: invalid or duplicate variant %q", ErrInvalidSuite, variant.ID)
		}
		seenVariants[variant.ID] = true
		if variant.Agent != "" && variant.Agent != "kern" && variant.Agent != "codex" ||
			len(variant.AgentVersion) > 200 || len(variant.Model) > 200 ||
			variant.Agent == "codex" && len(variant.Plugins) > 0 {
			return fmt.Errorf("%w: invalid agent configuration for variant %q", ErrInvalidSuite, variant.ID)
		}
		seenPlugins := make(map[string]bool, len(variant.Plugins))
		for _, reference := range variant.Plugins {
			pluginID, version, ok := strings.Cut(reference, "@")
			if !ok || !validID(pluginID) || !versionPattern.MatchString(version) || seenPlugins[pluginID] {
				return fmt.Errorf("%w: invalid plugin reference %q", ErrInvalidSuite, reference)
			}
			seenPlugins[pluginID] = true
		}
		for pluginID, digest := range variant.PluginDigests {
			if !seenPlugins[pluginID] || !strings.HasPrefix(digest, "sha256:") ||
				len(strings.TrimPrefix(digest, "sha256:")) != 64 {
				return fmt.Errorf("%w: invalid plugin digest for %q", ErrInvalidSuite, pluginID)
			}
			if _, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:")); err != nil {
				return fmt.Errorf("%w: invalid plugin digest for %q", ErrInvalidSuite, pluginID)
			}
		}
	}
	seenCases := make(map[string]bool, len(suite.Cases))
	for _, evalCase := range suite.Cases {
		if !validID(evalCase.ID) || seenCases[evalCase.ID] {
			return fmt.Errorf("%w: invalid or duplicate case %q", ErrInvalidSuite, evalCase.ID)
		}
		seenCases[evalCase.ID] = true
		if !safeRelative(evalCase.Fixture) || !safeRelative(evalCase.Prompt) || len(evalCase.Graders) == 0 {
			return fmt.Errorf("%w: case %q has unsafe inputs or no graders", ErrInvalidSuite, evalCase.ID)
		}
		seenGraders := make(map[string]bool, len(evalCase.Graders))
		hasModelGrader := false
		hasRequiredDeterministic := false
		for _, grader := range evalCase.Graders {
			if !validID(grader.ID) || seenGraders[grader.ID] {
				return fmt.Errorf("%w: case %q has invalid grader", ErrInvalidSuite, evalCase.ID)
			}
			seenGraders[grader.ID] = true
			if err := validateGrader(grader, suite.Defaults.AllowedCommands, suite.Defaults.Judge != nil); err != nil {
				return fmt.Errorf("%w: case %q grader %q: %v", ErrInvalidSuite, evalCase.ID, grader.ID, err)
			}
			hasModelGrader = hasModelGrader || grader.Type == "model"
			hasRequiredDeterministic = hasRequiredDeterministic ||
				grader.Required && grader.Type != "model" && grader.Type != "human_review"
		}
		if hasModelGrader && !hasRequiredDeterministic {
			return fmt.Errorf("%w: case %q model grader requires a deterministic required grader", ErrInvalidSuite, evalCase.ID)
		}
	}
	return nil
}

// PromptText loads one case prompt within the suite root.
func (s Suite) PromptText(evalCase Case) (string, error) {
	name, err := resolveWithin(s.Root, evalCase.Prompt)
	if err != nil {
		return "", err
	}
	file, err := os.Open(name)
	if err != nil {
		return "", fmt.Errorf("evaluation: opening prompt: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxPromptBytes+1))
	if err != nil {
		return "", fmt.Errorf("evaluation: reading prompt: %w", err)
	}
	if len(data) > maxPromptBytes || strings.TrimSpace(string(data)) == "" {
		return "", fmt.Errorf("%w: prompt is empty or too large", ErrInvalidSuite)
	}
	return string(data), nil
}

// FixturePath resolves a fixture without allowing escape from the suite root.
func (s Suite) FixturePath(evalCase Case) (string, error) {
	name, err := resolveWithin(s.Root, evalCase.Fixture)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(name)
	if err != nil {
		return "", fmt.Errorf("evaluation: reading fixture: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: v1 fixture must be a directory", ErrInvalidSuite)
	}
	return name, nil
}

func validateGrader(grader Grader, allowed []string, judgeConfigured bool) error {
	switch grader.Type {
	case "command":
		if len(grader.Command) == 0 || len(grader.Command) > 32 ||
			!safeExecutable(grader.Command[0]) || !slices.Contains(allowed, grader.Command[0]) {
			return errors.New("command is missing or not allowed")
		}
	case "file_exists":
		if !safeRelative(grader.Path) {
			return errors.New("unsafe file path")
		}
	case "file_contains", "file_not_contains":
		if !safeRelative(grader.Path) || grader.Contains == "" || len(grader.Contains) > 16<<10 {
			return errors.New("unsafe file assertion")
		}
	case "json_schema":
		if !safeRelative(grader.Path) {
			return errors.New("unsafe JSON file path")
		}
		if _, err := jsonschema.Compile(grader.Schema); err != nil {
			return fmt.Errorf("invalid JSON schema: %w", err)
		}
	case "patch_rule":
		if grader.MinChangedFiles < 0 || grader.MaxChangedFiles < 0 ||
			grader.MaxChangedFiles > 0 && grader.MinChangedFiles > grader.MaxChangedFiles ||
			grader.MinChangedFiles == 0 && grader.MaxChangedFiles == 0 &&
				len(grader.AllowedPaths) == 0 && len(grader.ForbiddenPaths) == 0 {
			return errors.New("invalid patch rule bounds")
		}
		if len(grader.AllowedPaths)+len(grader.ForbiddenPaths) > 256 {
			return errors.New("too many patch path rules")
		}
		for _, pattern := range append(append([]string(nil), grader.AllowedPaths...), grader.ForbiddenPaths...) {
			if !safePathPattern(pattern) {
				return fmt.Errorf("unsafe patch path pattern %q", pattern)
			}
		}
	case "safety":
		if grader.MaxSafetyViolations == nil || *grader.MaxSafetyViolations < 0 {
			return errors.New("max_safety_violations is required and must be non-negative")
		}
	case "human_review":
		if strings.TrimSpace(grader.Instructions) == "" || len(grader.Instructions) > 4_096 {
			return errors.New("human review instructions are required")
		}
	case "model":
		if !judgeConfigured {
			return errors.New("model judge configuration is required")
		}
		if grader.Required {
			return errors.New("model grader must be supplemental")
		}
		if strings.TrimSpace(grader.Instructions) == "" || len(grader.Instructions) > 8_192 {
			return errors.New("model grader instructions are required")
		}
		if len(grader.EvidencePaths) > 16 {
			return errors.New("too many model evidence paths")
		}
		for _, evidencePath := range grader.EvidencePaths {
			if !safeRelative(evidencePath) {
				return fmt.Errorf("unsafe model evidence path %q", evidencePath)
			}
		}
	default:
		return fmt.Errorf("unsupported type %q", grader.Type)
	}
	return nil
}

func validateJudge(config JudgeConfig) error {
	if config.Provider != "openai-compatible" || strings.TrimSpace(config.Model) == "" ||
		len(config.Model) > 200 || config.MaxTokens < 32 || config.MaxTokens > 4_096 {
		return errors.New("provider, model, or max_tokens is invalid")
	}
	endpoint, err := url.Parse(config.BaseURL)
	if err != nil || endpoint.Scheme != "http" && endpoint.Scheme != "https" || endpoint.Host == "" ||
		endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || len(config.BaseURL) > 2_048 {
		return errors.New("base_url is invalid")
	}
	if config.APIKeyEnv != "" && !envNamePattern.MatchString(config.APIKeyEnv) {
		return errors.New("api_key_env is invalid")
	}
	return nil
}

func safePathPattern(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, `\`+"\x00") ||
		path.IsAbs(value) || strings.HasPrefix(path.Clean(value), "../") {
		return false
	}
	_, err := path.Match(strings.ReplaceAll(value, "**", "*"), "probe")
	return err == nil
}

func validID(value string) bool {
	return len(value) <= 200 && idPattern.MatchString(value)
}

func safeExecutable(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && !strings.ContainsAny(value, `/\\\x00`)
}

func safeRelative(value string) bool {
	if value == "" || filepath.IsAbs(value) || strings.ContainsRune(value, 0) {
		return false
	}
	clean := filepath.Clean(value)
	return clean == value && clean != "." && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func resolveWithin(root, relative string) (string, error) {
	if !safeRelative(relative) {
		return "", fmt.Errorf("%w: unsafe relative path %q", ErrInvalidSuite, relative)
	}
	name := filepath.Join(root, relative)
	rel, err := filepath.Rel(root, name)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: path escapes suite root", ErrInvalidSuite)
	}
	return name, nil
}
