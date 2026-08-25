package app

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/userInner/kern/internal/agent"
	"github.com/userInner/kern/internal/configuration"
	"github.com/userInner/kern/internal/model"
	"github.com/userInner/kern/internal/policy"
	"github.com/userInner/kern/internal/store/sqlite"
	"github.com/userInner/kern/internal/task"
)

const SettingsSchemaVersion = "1"

// Settings is the public-safe runtime configuration exposed by the local API.
type Settings struct {
	SchemaVersion string  `json:"schema_version"`
	MaxTurns      int     `json:"max_turns"`
	MaxToolCalls  int     `json:"max_tool_calls"`
	MaxTokens     int     `json:"max_tokens"`
	MaxCostUSD    float64 `json:"max_cost_usd"`
	TaskTimeout   string  `json:"task_timeout"`
	PolicyProfile string  `json:"policy_profile"`
	RetentionDays int     `json:"retention_days"`
}

// SettingsDraft replaces every user-editable setting as one validated unit.
type SettingsDraft struct {
	MaxTurns      int     `json:"max_turns"`
	MaxToolCalls  int     `json:"max_tool_calls"`
	MaxTokens     int     `json:"max_tokens"`
	MaxCostUSD    float64 `json:"max_cost_usd"`
	TaskTimeout   string  `json:"task_timeout"`
	PolicyProfile string  `json:"policy_profile"`
	RetentionDays int     `json:"retention_days"`
}

// CleanupPreview is a non-mutating retention calculation.
type CleanupPreview struct {
	Cutoff        time.Time `json:"cutoff"`
	RetentionDays int       `json:"retention_days"`
	TaskCount     int       `json:"task_count"`
	ArtifactCount int       `json:"artifact_count"`
	ArtifactBytes int64     `json:"artifact_bytes"`
}

// CleanupResult reports committed task deletion and pruned content objects.
type CleanupResult struct {
	CleanupPreview
	RemovedObjects int   `json:"removed_objects"`
	RemovedBytes   int64 `json:"removed_bytes"`
}

type agentConfigSource struct {
	mu     sync.RWMutex
	config agent.Config
}

func newAgentConfigSource(config agent.Config) *agentConfigSource {
	return &agentConfigSource{config: config}
}

func (s *agentConfigSource) Current() agent.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config
}

func (s *agentConfigSource) Set(config agent.Config) {
	s.mu.Lock()
	s.config = config
	s.mu.Unlock()
}

type settingsState struct {
	mu            sync.RWMutex
	path          string
	retentionDays int
}

func newSettingsState(path string, retentionDays int) (*settingsState, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("app: settings path is required")
	}
	if retentionDays < 1 || retentionDays > 3650 {
		return nil, errors.New("app: retention days must be between 1 and 3650")
	}
	return &settingsState{path: path, retentionDays: retentionDays}, nil
}

func (s *settingsState) snapshot() (string, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.path, s.retentionDays
}

func (s *settingsState) setRetentionDays(value int) {
	s.mu.Lock()
	s.retentionDays = value
	s.mu.Unlock()
}

type dynamicAgentProcessor struct {
	generator model.Generator
	store     *sqlite.Store
	tools     agent.ToolSet
	configs   *agentConfigSource
}

func (p *dynamicAgentProcessor) Process(ctx context.Context, item task.Task) (string, error) {
	runner, err := agent.New(p.generator, p.store, p.store, p.tools, p.configs.Current())
	if err != nil {
		return "", err
	}
	return runner.Process(ctx, item)
}

// CurrentSettings returns the budgets and policy used for new executions.
func (r *Runtime) CurrentSettings() Settings {
	config := r.configs.Current()
	_, retentionDays := r.settings.snapshot()
	return Settings{
		SchemaVersion: SettingsSchemaVersion,
		MaxTurns:      config.MaxTurns, MaxToolCalls: config.MaxToolCalls,
		MaxTokens: config.MaxTokens, MaxCostUSD: float64(config.MaxCostMicros) / 1_000_000,
		TaskTimeout: config.MaxDuration.String(), PolicyProfile: string(r.policy.Profile()),
		RetentionDays: retentionDays,
	}
}

// UpdateSettings atomically persists a complete draft before applying it to
// future task executions. A runner that already started keeps its immutable
// configuration copy.
func (r *Runtime) UpdateSettings(_ context.Context, draft SettingsDraft) (Settings, error) {
	duration, err := time.ParseDuration(strings.TrimSpace(draft.TaskTimeout))
	if err != nil {
		return Settings{}, errors.New("app: task timeout must be a duration such as 10m")
	}
	if math.IsNaN(draft.MaxCostUSD) || math.IsInf(draft.MaxCostUSD, 0) ||
		draft.MaxCostUSD > float64(math.MaxInt64)/1_000_000 {
		return Settings{}, errors.New("app: max cost is invalid")
	}
	path, _ := r.settings.snapshot()
	file, err := configuration.Load(path)
	if err != nil {
		return Settings{}, err
	}
	file.Runtime.MaxTurns = draft.MaxTurns
	file.Runtime.MaxToolCalls = draft.MaxToolCalls
	file.Runtime.MaxTokens = draft.MaxTokens
	file.Runtime.MaxCostUSD = draft.MaxCostUSD
	file.Runtime.TaskTimeout = configuration.Duration{Duration: duration}
	file.Policy.Profile = strings.TrimSpace(draft.PolicyProfile)
	file.Storage.RetentionDays = draft.RetentionDays
	if err := configuration.Validate(file); err != nil {
		return Settings{}, err
	}
	current := r.configs.Current()
	next, err := agent.NormalizeConfig(agent.Config{
		MaxTurns: draft.MaxTurns, MaxToolCalls: draft.MaxToolCalls,
		MaxTokens: draft.MaxTokens, MaxCostMicros: int64(math.Round(draft.MaxCostUSD * 1_000_000)),
		MaxDuration: duration, MaxRetries: current.MaxRetries,
		Authorizer: current.Authorizer, Artifacts: current.Artifacts,
		ArtifactReader: current.ArtifactReader, Context: current.Context,
	})
	if err != nil {
		return Settings{}, err
	}
	profile := policy.Profile(file.Policy.Profile)
	if err := policy.ValidateProfile(profile); err != nil {
		return Settings{}, err
	}
	if err := configuration.Save(path, file); err != nil {
		return Settings{}, err
	}
	r.configs.Set(next)
	if err := r.policy.SetProfile(profile); err != nil {
		return Settings{}, err
	}
	r.settings.setRetentionDays(draft.RetentionDays)
	return r.CurrentSettings(), nil
}

// PreviewCleanup computes the current retention boundary without deleting data.
func (r *Runtime) PreviewCleanup(ctx context.Context) (CleanupPreview, error) {
	_, retentionDays := r.settings.snapshot()
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays)
	summary, err := r.Store.PreviewTerminalTaskCleanup(ctx, cutoff)
	if err != nil {
		return CleanupPreview{}, err
	}
	return CleanupPreview{
		Cutoff: cutoff, RetentionDays: retentionDays, TaskCount: summary.TaskCount,
		ArtifactCount: summary.ArtifactCount, ArtifactBytes: summary.ArtifactBytes,
	}, nil
}

// CleanupExpired deletes only terminal tasks older than the configured cutoff,
// then prunes content objects proven to have no remaining database references.
func (r *Runtime) CleanupExpired(ctx context.Context) (CleanupResult, error) {
	_, retentionDays := r.settings.snapshot()
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays)
	summary, err := r.Store.DeleteTerminalTasksBefore(ctx, cutoff)
	if err != nil {
		return CleanupResult{}, err
	}
	objects, bytes, err := r.Artifacts.PruneUnreferenced(ctx, summary.ArtifactPaths)
	if err != nil {
		return CleanupResult{}, err
	}
	return CleanupResult{
		CleanupPreview: CleanupPreview{
			Cutoff: cutoff, RetentionDays: retentionDays, TaskCount: summary.TaskCount,
			ArtifactCount: summary.ArtifactCount, ArtifactBytes: summary.ArtifactBytes,
		},
		RemovedObjects: objects, RemovedBytes: bytes,
	}, nil
}
