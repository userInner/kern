// Package pluginactivation selects and injects safe declarative plugin
// enhancements without making plugins a prerequisite for task execution.
package pluginactivation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/userInner/kern/internal/contextbuilder"
	"github.com/userInner/kern/internal/executionphase"
	"github.com/userInner/kern/internal/model"
	"github.com/userInner/kern/internal/plugin"
	"github.com/userInner/kern/internal/task"
)

const maxWorkspaceSignalEntries = 5_000

type pluginLister interface {
	List(ctx context.Context) ([]plugin.Installed, error)
}

type repository interface {
	TaskPluginPreferences(ctx context.Context, taskID string) ([]plugin.Preference, error)
	RecordAttemptPlugin(ctx context.Context, usage plugin.Usage) (bool, error)
	AppendEvent(ctx context.Context, taskID, attemptID, eventType string, payload any) error
}

type contextManager interface {
	Initialize(ctx context.Context, item task.Task, systemPrompt string) ([]model.Message, contextbuilder.BuildReport, error)
	Append(
		ctx context.Context,
		item task.Task,
		message model.Message,
		trust contextbuilder.TrustLevel,
		source contextbuilder.Source,
		sourceRef string,
	) (contextbuilder.Record, error)
}

type workspace interface {
	Root() string
}

// Config controls automatic selection. Explicit task preferences remain active
// when AutoActivate is false.
type Config struct {
	AutoActivate       bool
	SystemPrompt       string
	ExecutableRuntimes []string
}

// Selection is one validated plugin enhancement selected for an Attempt.
type Selection struct {
	Plugin plugin.Installed
	Bundle plugin.Bundle
	Reason string
}

// Activator performs deterministic, bounded plugin selection.
type Activator struct {
	plugins   pluginLister
	repo      repository
	workspace workspace
	config    Config
	runtimes  map[string]struct{}
}

// New constructs an activator.
func New(plugins pluginLister, repo repository, workspace workspace, config Config) (*Activator, error) {
	if plugins == nil || repo == nil || workspace == nil {
		return nil, errors.New("pluginactivation: plugin lister, repository, and workspace are required")
	}
	if strings.TrimSpace(config.SystemPrompt) == "" {
		return nil, errors.New("pluginactivation: Core system prompt is required")
	}
	runtimes := map[string]struct{}{"subprocess": {}}
	for _, runtimeName := range config.ExecutableRuntimes {
		switch runtimeName {
		case "subprocess", "wasm":
			runtimes[runtimeName] = struct{}{}
		default:
			return nil, fmt.Errorf("pluginactivation: unsupported executable runtime %q", runtimeName)
		}
	}
	return &Activator{
		plugins: plugins, repo: repo, workspace: workspace, config: config, runtimes: runtimes,
	}, nil
}

// Activate loads all applicable declarative plugins. Any individual plugin
// failure is recorded and skipped so the general Agent remains usable.
func (a *Activator) Activate(ctx context.Context, item task.Task) ([]Selection, error) {
	installed, err := a.plugins.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("pluginactivation: listing plugins: %w", err)
	}
	preferences, err := a.repo.TaskPluginPreferences(ctx, item.ID)
	if err != nil {
		return nil, fmt.Errorf("pluginactivation: loading preferences: %w", err)
	}
	preferenceByID := make(map[string]plugin.PreferenceMode, len(preferences))
	for _, preference := range preferences {
		preferenceByID[preference.PluginID] = preference.Mode
	}
	intents := detectIntents(item.Goal)
	workspaceFiles, scanErr := scanWorkspace(ctx, a.workspace.Root())
	if scanErr != nil {
		_ = a.repo.AppendEvent(ctx, item.ID, item.ActiveAttemptID, "plugin.signal_scan_failed", map[string]string{
			"reason": "workspace_metadata_unavailable",
		})
		workspaceFiles = nil
	}
	type candidate struct {
		item     plugin.Installed
		reason   string
		priority int
	}
	candidates := make([]candidate, 0)
	for _, installedPlugin := range installed {
		preference := preferenceByID[installedPlugin.ID]
		if preference == plugin.PreferenceDisable {
			continue
		}
		if preference == plugin.PreferenceEnable {
			candidates = append(candidates, candidate{item: installedPlugin, reason: "manual_enable", priority: 3})
			continue
		}
		if !a.config.AutoActivate || !installedPlugin.Enabled {
			continue
		}
		matchedIntents := intersect(installedPlugin.Manifest.Activation.Intents, intents)
		matchedSignals := matchSignals(installedPlugin.Manifest.Activation.Signals, workspaceFiles)
		if len(matchedIntents) == 0 && len(matchedSignals) == 0 {
			continue
		}
		reason := activationReason(matchedIntents, matchedSignals)
		priority := 1
		if installedPlugin.TrustStatus == "verified" {
			priority = 2
		}
		candidates = append(candidates, candidate{item: installedPlugin, reason: reason, priority: priority})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority > candidates[j].priority
		}
		return candidates[i].item.ID < candidates[j].item.ID
	})

	workflowOwners := make(map[string]string)
	verifierOwners := make(map[string]string)
	selections := make([]Selection, 0, len(candidates))
	for _, candidate := range candidates {
		bundle, err := plugin.LoadBundle(candidate.item)
		if err != nil {
			a.recordSkipped(ctx, item, candidate.item, "resource_validation_failed")
			continue
		}
		if onlyUnavailableExecutableResources(bundle, a.runtimes) {
			a.recordSkipped(ctx, item, candidate.item, "executable_runtime_unavailable")
			continue
		}
		if owner, conflict := bundleConflict(bundle, workflowOwners, verifierOwners); conflict {
			a.recordSkipped(ctx, item, candidate.item, "conflict_with:"+owner)
			continue
		}
		for _, workflow := range bundle.Workflows {
			workflowOwners[workflow.ID] = candidate.item.ID
		}
		for _, verifier := range bundle.Verifiers {
			verifierOwners[verifier.ID] = candidate.item.ID
		}
		selections = append(selections, Selection{Plugin: candidate.item, Bundle: bundle, Reason: candidate.reason})
	}
	return selections, nil
}

func (a *Activator) recordSkipped(ctx context.Context, item task.Task, installed plugin.Installed, reason string) {
	_ = a.repo.AppendEvent(ctx, item.ID, item.ActiveAttemptID, "plugin.activation_skipped", map[string]string{
		"plugin_id": installed.ID,
		"version":   installed.Version,
		"reason":    reason,
	})
}

// Processor injects selected plugin bundles before delegating to the general
// processor. Selection/injection failures are visible events and safely fall
// back to the delegate.
type Processor struct {
	delegate interface {
		Process(context.Context, task.Task) (string, error)
	}
	activator *Activator
	context   contextManager
}

// NewProcessor wraps a general processor with additive plugin context.
func NewProcessor(
	delegate interface {
		Process(context.Context, task.Task) (string, error)
	},
	activator *Activator,
	contextManager contextManager,
) (*Processor, error) {
	if delegate == nil || activator == nil || contextManager == nil {
		return nil, errors.New("pluginactivation: processor dependencies are required")
	}
	return &Processor{delegate: delegate, activator: activator, context: contextManager}, nil
}

// Process enriches this Attempt and always preserves the general fallback.
func (p *Processor) Process(ctx context.Context, item task.Task) (string, error) {
	startedAt := time.Now()
	_ = p.activator.repo.AppendEvent(ctx, item.ID, item.ActiveAttemptID, "plugin.activation_started", map[string]any{})
	selections, err := p.activator.Activate(ctx, item)
	if err != nil {
		_ = p.activator.repo.AppendEvent(ctx, item.ID, item.ActiveAttemptID, "plugin.activation_failed", map[string]any{
			"reason":      "selection_unavailable",
			"duration_ms": time.Since(startedAt).Milliseconds(),
		})
		return p.delegate.Process(ctx, item)
	}
	if len(selections) == 0 {
		_ = p.activator.repo.AppendEvent(ctx, item.ID, item.ActiveAttemptID, "plugin.activation_completed", map[string]any{
			"selected":    0,
			"duration_ms": time.Since(startedAt).Milliseconds(),
		})
		return p.delegate.Process(ctx, item)
	}
	if _, _, err := p.context.Initialize(ctx, item, p.activator.config.SystemPrompt); err != nil {
		_ = p.activator.repo.AppendEvent(ctx, item.ID, item.ActiveAttemptID, "plugin.activation_failed", map[string]any{
			"reason":      "context_initialization_failed",
			"duration_ms": time.Since(startedAt).Milliseconds(),
		})
		return p.delegate.Process(ctx, item)
	}
	intents := detectIntents(item.Goal)
	for _, selection := range selections {
		resources, err := resourceEvidence(selection.Bundle, intents)
		if err != nil {
			p.activator.recordSkipped(ctx, item, selection.Plugin, "audit_encoding_failed")
			continue
		}
		_, err = p.activator.repo.RecordAttemptPlugin(ctx, plugin.Usage{
			SchemaVersion: plugin.SchemaVersion,
			TaskID:        item.ID,
			AttemptID:     item.ActiveAttemptID,
			PluginID:      selection.Plugin.ID,
			Version:       selection.Plugin.Version,
			Digest:        selection.Plugin.Digest,
			Reason:        selection.Reason,
			Resources:     resources,
			CreatedAt:     time.Now().UTC(),
		})
		if err != nil {
			return "", fmt.Errorf("pluginactivation: persisting activation audit: %w", err)
		}
		pluginRef := selection.Plugin.ID + "@" + selection.Plugin.Version
		for _, current := range executionphase.All() {
			content, err := selection.Bundle.ContextJSONForPhase(current, intents)
			if err != nil {
				p.activator.recordSkipped(ctx, item, selection.Plugin, "phase_context_bundle_failed:"+string(current))
				continue
			}
			sourceRef, err := contextbuilder.PluginSourceRef(pluginRef, current)
			if err != nil {
				return "", fmt.Errorf("pluginactivation: building phase source reference: %w", err)
			}
			if _, err := p.context.Append(ctx, item, model.Message{
				Role: model.RoleAssistant,
				Content: []model.ContentBlock{{
					Kind: model.ContentText,
					Text: string(content),
				}},
			}, contextbuilder.TrustPluginUntrusted, contextbuilder.SourcePlugin, sourceRef); err != nil {
				p.activator.recordSkipped(ctx, item, selection.Plugin, "phase_context_append_failed:"+string(current))
			}
		}
	}
	_ = p.activator.repo.AppendEvent(ctx, item.ID, item.ActiveAttemptID, "plugin.activation_completed", map[string]any{
		"selected":    len(selections),
		"duration_ms": time.Since(startedAt).Milliseconds(),
	})
	return p.delegate.Process(ctx, item)
}

func resourceEvidence(bundle plugin.Bundle, intents []string) (json.RawMessage, error) {
	type toolResource struct {
		ID      string `json:"id"`
		Runtime string `json:"runtime"`
	}
	type resourceSet struct {
		Knowledge []string       `json:"knowledge,omitempty"`
		Workflows []string       `json:"workflows,omitempty"`
		Rules     []string       `json:"rules,omitempty"`
		Verifiers []string       `json:"verifiers,omitempty"`
		Tools     []toolResource `json:"tools,omitempty"`
	}
	collect := func(view plugin.Bundle) resourceSet {
		var result resourceSet
		for _, item := range view.Knowledge {
			result.Knowledge = append(result.Knowledge, item.Path)
		}
		for _, item := range view.Workflows {
			result.Workflows = append(result.Workflows, item.ID)
		}
		for _, set := range view.Rules {
			for _, item := range set.Rules {
				result.Rules = append(result.Rules, item.ID)
			}
		}
		for _, item := range view.Verifiers {
			result.Verifiers = append(result.Verifiers, item.ID)
		}
		for _, item := range view.Tools {
			result.Tools = append(result.Tools, toolResource{ID: item.ID, Runtime: item.Runtime})
		}
		return result
	}
	evidence := struct {
		resourceSet
		Phases map[string]resourceSet `json:"phases"`
	}{
		resourceSet: collect(bundle),
		Phases:      make(map[string]resourceSet, len(executionphase.All())),
	}
	for _, current := range executionphase.All() {
		evidence.Phases[string(current)] = collect(bundle.ForPhase(current, intents))
	}
	return json.Marshal(evidence)
}

func onlyUnavailableExecutableResources(bundle plugin.Bundle, runtimes map[string]struct{}) bool {
	if len(bundle.Knowledge)+len(bundle.Workflows)+len(bundle.Rules)+len(bundle.Verifiers) > 0 || len(bundle.Tools) == 0 {
		return false
	}
	for _, item := range bundle.Tools {
		if _, available := runtimes[item.Runtime]; available {
			return false
		}
	}
	return true
}

func detectIntents(goal string) []string {
	lower := strings.ToLower(goal)
	intents := make([]string, 0, 4)
	if containsAny(lower, "修复", "bug", "fix", "错误", "故障") {
		intents = append(intents, "code.fix")
	}
	if containsAny(lower, "审查", "检查代码", "review", "audit") {
		intents = append(intents, "code.review")
	}
	if containsAny(lower, "测试", "验证", "test", "verify", "check") {
		intents = append(intents, "code.test")
	}
	if containsAny(lower, "实现", "开发", "创建", "implement", "build", "create") {
		intents = append(intents, "code.implement")
	}
	return intents
}

func scanWorkspace(ctx context.Context, root string) ([]string, error) {
	files := make([]string, 0)
	err := fs.WalkDir(osDirFS(root), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name != "." && entry.IsDir() && skipDirectory(entry.Name()) {
			return fs.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		files = append(files, strings.TrimPrefix(path.Clean(name), "./"))
		if len(files) > maxWorkspaceSignalEntries {
			return errors.New("pluginactivation: workspace signal scan limit exceeded")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// osDirFS is a variable to keep workspace scanning replaceable in tests.
var osDirFS = func(root string) fs.FS { return os.DirFS(root) }

func skipDirectory(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", "node_modules", "vendor", "dist", "build", ".cache":
		return true
	default:
		return strings.HasPrefix(name, ".")
	}
}

func matchSignals(signals, files []string) []string {
	matched := make([]string, 0)
	for _, signal := range signals {
		for _, file := range files {
			if matchSignal(signal, file) {
				matched = append(matched, signal)
				break
			}
		}
	}
	return matched
}

func matchSignal(pattern, name string) bool {
	if pattern == name {
		return true
	}
	if strings.HasPrefix(pattern, "**/") {
		trimmed := strings.TrimPrefix(pattern, "**/")
		matched, _ := path.Match(trimmed, path.Base(name))
		if matched {
			return true
		}
		for index := strings.IndexByte(name, '/'); index >= 0; index = strings.IndexByte(name, '/') {
			name = name[index+1:]
			matched, _ = path.Match(trimmed, name)
			if matched {
				return true
			}
		}
		return false
	}
	matched, _ := path.Match(pattern, name)
	return matched
}

func intersect(left, right []string) []string {
	set := make(map[string]bool, len(right))
	for _, value := range right {
		set[value] = true
	}
	result := make([]string, 0)
	for _, value := range left {
		if set[value] {
			result = append(result, value)
		}
	}
	return result
}

func activationReason(intents, signals []string) string {
	parts := make([]string, 0, 2)
	if len(intents) > 0 {
		parts = append(parts, "intent:"+strings.Join(intents, ","))
	}
	if len(signals) > 0 {
		parts = append(parts, "signal:"+strings.Join(signals, ","))
	}
	return strings.Join(parts, ";")
}

func bundleConflict(bundle plugin.Bundle, workflows, verifiers map[string]string) (string, bool) {
	for _, item := range bundle.Workflows {
		if owner := workflows[item.ID]; owner != "" {
			return owner, true
		}
	}
	for _, item := range bundle.Verifiers {
		if owner := verifiers[item.ID]; owner != "" {
			return owner, true
		}
	}
	return "", false
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
