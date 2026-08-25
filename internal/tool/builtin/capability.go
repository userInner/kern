package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/userInner/kern/internal/executionphase"
	"github.com/userInner/kern/internal/jsonschema"
	"github.com/userInner/kern/internal/model"
	"github.com/userInner/kern/internal/operation"
	"github.com/userInner/kern/internal/plugin"
	"github.com/userInner/kern/internal/tool"
)

var capabilitySchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["action"],
  "properties": {
    "action": {"type": "string", "enum": ["list", "get_bundle", "invoke"]},
    "plugin_id": {"type": "string", "description": "Required for get_bundle and invoke."},
    "tool_id": {"type": "string", "description": "Required for invoke."},
    "input": {"type": "object", "description": "Plugin-tool input validated against its installed schema."}
  }
}`)

type pluginUsageReader interface {
	ListAttemptPlugins(ctx context.Context, taskID, attemptID string) ([]plugin.Usage, error)
}

type installedPluginReader interface {
	Get(ctx context.Context, pluginID string) (plugin.Installed, error)
}

type pluginToolRuntime interface {
	Invoke(
		ctx context.Context,
		installed plugin.Installed,
		spec plugin.ToolSpec,
		input json.RawMessage,
	) (json.RawMessage, error)
}

// HostCapability is trusted embedding code registered at Core construction.
// Its declared effect is still authorized and audited before Handler runs.
type HostCapability struct {
	ProviderID   string
	ProviderName string
	ID           string
	Description  string
	InputSchema  json.RawMessage
	Effect       operation.Effect
	Handler      func(context.Context, json.RawMessage) (tool.Result, error)
}

type registeredHostCapability struct {
	spec   HostCapability
	schema *jsonschema.Schema
}

type hostProvider struct {
	name  string
	tools map[string]registeredHostCapability
}

// Capability exposes only plugins already selected and audited for the current
// Attempt. Executable capabilities remain subject to the ordinary Operation,
// policy, approval, and audit path before Execute is reached.
type Capability struct {
	usage   pluginUsageReader
	plugins installedPluginReader
	runtime pluginToolRuntime
	host    map[string]hostProvider
}

// NewCapability constructs the task-scoped plugin capability tool.
func NewCapability(
	usage pluginUsageReader,
	plugins installedPluginReader,
	runtime pluginToolRuntime,
	hostCapabilities ...HostCapability,
) (*Capability, error) {
	if usage == nil || plugins == nil || runtime == nil {
		return nil, errors.New("tool: plugin capability dependencies are required")
	}
	host, err := registerHostCapabilities(hostCapabilities)
	if err != nil {
		return nil, err
	}
	return &Capability{usage: usage, plugins: plugins, runtime: runtime, host: host}, nil
}

func registerHostCapabilities(items []HostCapability) (map[string]hostProvider, error) {
	if len(items) > 64 {
		return nil, errors.New("tool: at most 64 host capabilities may be registered")
	}
	providers := make(map[string]hostProvider)
	for _, item := range items {
		if !strings.HasPrefix(item.ProviderID, plugin.HostIDPrefix) || !plugin.ValidID(item.ProviderID) ||
			strings.TrimSpace(item.ProviderName) == "" || len([]rune(item.ProviderName)) > 100 ||
			!plugin.ValidToolID(item.ID) || strings.TrimSpace(item.Description) == "" ||
			len([]rune(item.Description)) > 1_000 || item.Handler == nil || !validHostEffect(item.Effect) {
			return nil, errors.New("tool: invalid host capability registration")
		}
		schema, err := jsonschema.Compile(item.InputSchema)
		if err != nil || schema.RootType() != "object" {
			return nil, fmt.Errorf("tool: host capability %s/%s requires an object input schema", item.ProviderID, item.ID)
		}
		provider := providers[item.ProviderID]
		if provider.tools == nil {
			provider = hostProvider{name: item.ProviderName, tools: make(map[string]registeredHostCapability)}
		} else if provider.name != item.ProviderName {
			return nil, fmt.Errorf("tool: host provider %s has conflicting names", item.ProviderID)
		}
		if _, duplicate := provider.tools[item.ID]; duplicate {
			return nil, fmt.Errorf("tool: duplicate host capability %s/%s", item.ProviderID, item.ID)
		}
		item.InputSchema = append(json.RawMessage(nil), item.InputSchema...)
		provider.tools[item.ID] = registeredHostCapability{spec: item, schema: schema}
		providers[item.ProviderID] = provider
	}
	return providers, nil
}

func validHostEffect(effect operation.Effect) bool {
	switch effect {
	case operation.EffectRead, operation.EffectLocalWrite, operation.EffectProcess,
		operation.EffectNetworkRead, operation.EffectNetworkWrite, operation.EffectDestructive:
		return true
	default:
		return false
	}
}

func (c *Capability) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name: "capability",
		Description: "List, inspect, or invoke validated capabilities from plugins active for this Attempt or trusted host integrations. " +
			"Every invocation remains subject to Core policy, user authorization, and operation audit.",
		InputSchema: capabilitySchema,
	}
}

func (c *Capability) Effect(input json.RawMessage) (operation.Effect, error) {
	request, err := decodeCapability(input)
	if err != nil {
		return operation.EffectUnknown, err
	}
	if request.Action == "invoke" {
		if provider, ok := c.host[request.PluginID]; ok {
			registered, ok := provider.tools[request.ToolID]
			if !ok {
				return operation.EffectUnknown, errors.New("tool: host capability is unavailable")
			}
			return registered.spec.Effect, nil
		}
		return operation.EffectProcess, nil
	}
	return operation.EffectRead, nil
}

func (c *Capability) Execute(ctx context.Context, input json.RawMessage) (tool.Result, error) {
	request, err := decodeCapability(input)
	if err != nil {
		return tool.Result{}, err
	}
	scope, ok := tool.TaskScopeFromContext(ctx)
	if !ok {
		return tool.Result{}, errors.New("tool: capability requires a task Attempt scope")
	}
	usages, err := c.usage.ListAttemptPlugins(ctx, scope.TaskID, scope.AttemptID)
	if err != nil {
		return tool.Result{}, fmt.Errorf("tool: loading active capabilities: %w", err)
	}
	if request.Action == "list" {
		type summary struct {
			PluginID string `json:"plugin_id"`
			Version  string `json:"version"`
			Digest   string `json:"digest"`
			Reason   string `json:"reason"`
		}
		items := make([]summary, 0, len(usages))
		for _, usage := range usages {
			items = append(items, summary{
				PluginID: usage.PluginID,
				Version:  usage.Version,
				Digest:   usage.Digest,
				Reason:   usage.Reason,
			})
		}
		type hostSummary struct {
			ProviderID string `json:"provider_id"`
			Name       string `json:"name"`
		}
		hostIDs := make([]string, 0, len(c.host))
		for providerID := range c.host {
			hostIDs = append(hostIDs, providerID)
		}
		sort.Strings(hostIDs)
		hosts := make([]hostSummary, 0, len(hostIDs))
		for _, providerID := range hostIDs {
			hosts = append(hosts, hostSummary{ProviderID: providerID, Name: c.host[providerID].name})
		}
		content, err := json.Marshal(map[string]any{
			"schema_version": plugin.SchemaVersion,
			"phase":          scope.Phase,
			"plugins":        items,
			"host_providers": hosts,
		})
		if err != nil {
			return tool.Result{}, fmt.Errorf("tool: encoding active capabilities: %w", err)
		}
		return tool.Result{Content: string(content)}, nil
	}
	if provider, found := c.host[request.PluginID]; found {
		return c.executeHost(ctx, scope.Phase, request, provider)
	}
	usage, found := findUsage(usages, request.PluginID)
	if !found {
		return tool.Result{}, errors.New("tool: plugin is not active for this Attempt")
	}
	installed, err := c.plugins.Get(ctx, request.PluginID)
	if err != nil {
		return tool.Result{}, errors.New("tool: active plugin package is unavailable")
	}
	if installed.Version != usage.Version || installed.Digest != usage.Digest {
		return tool.Result{}, errors.New("tool: active plugin package no longer matches its audit record")
	}
	bundle, err := plugin.LoadBundle(installed)
	if err != nil {
		return tool.Result{}, errors.New("tool: active plugin package failed integrity validation")
	}
	if request.Action == "invoke" {
		if scope.Phase == executionphase.Prepare {
			return tool.Result{}, errors.New("tool: executable plugin capabilities are unavailable during prepare")
		}
		if !usageIncludesTool(usage, request.ToolID) {
			return tool.Result{}, errors.New("tool: plugin capability was not audited for this Attempt")
		}
		var spec plugin.ToolSpec
		for _, candidate := range bundle.Tools {
			if candidate.ID == request.ToolID {
				spec = candidate
				break
			}
		}
		if spec.ID == "" {
			return tool.Result{}, errors.New("tool: plugin capability is unavailable")
		}
		result, err := c.runtime.Invoke(ctx, installed, spec, request.Input)
		if err != nil {
			return tool.Result{}, fmt.Errorf("tool: invoking plugin capability: %w", err)
		}
		return tool.Result{Content: string(result)}, nil
	}
	audited, err := auditedPhaseBundle(bundle, usage, scope.Phase)
	if err != nil {
		return tool.Result{}, err
	}
	content, err := audited.ContextJSONForPhase(scope.Phase, nil)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Content: string(content)}, nil
}

func (c *Capability) executeHost(
	ctx context.Context,
	current executionphase.Phase,
	request capabilityInput,
	provider hostProvider,
) (tool.Result, error) {
	if request.Action == "get_bundle" {
		type descriptor struct {
			ID          string           `json:"id"`
			Description string           `json:"description"`
			Effect      operation.Effect `json:"effect"`
			InputSchema json.RawMessage  `json:"input_schema"`
		}
		ids := make([]string, 0, len(provider.tools))
		if current != executionphase.Prepare {
			for id := range provider.tools {
				ids = append(ids, id)
			}
			sort.Strings(ids)
		}
		tools := make([]descriptor, 0, len(ids))
		for _, id := range ids {
			registered := provider.tools[id]
			tools = append(tools, descriptor{
				ID: id, Description: registered.spec.Description,
				Effect: registered.spec.Effect, InputSchema: registered.spec.InputSchema,
			})
		}
		content, err := json.Marshal(map[string]any{
			"notice": "Trusted host capability declarations. Core policy and user authorization still apply.",
			"phase":  current,
			"provider": map[string]any{
				"id": request.PluginID, "name": provider.name, "tools": tools,
			},
		})
		if err != nil {
			return tool.Result{}, fmt.Errorf("tool: encoding host capabilities: %w", err)
		}
		return tool.Result{Content: string(content)}, nil
	}
	if current == executionphase.Prepare {
		return tool.Result{}, errors.New("tool: executable host capabilities are unavailable during prepare")
	}
	registered, found := provider.tools[request.ToolID]
	if !found {
		return tool.Result{}, errors.New("tool: host capability is unavailable")
	}
	if err := registered.schema.Validate(request.Input); err != nil {
		return tool.Result{}, fmt.Errorf("tool: validating host capability input: %w", err)
	}
	result, err := registered.spec.Handler(ctx, request.Input)
	if err != nil {
		return result, fmt.Errorf("tool: invoking host capability: %w", err)
	}
	return result, nil
}

func auditedPhaseBundle(
	bundle plugin.Bundle,
	usage plugin.Usage,
	current executionphase.Phase,
) (plugin.Bundle, error) {
	type phaseResources struct {
		Knowledge []string `json:"knowledge"`
		Workflows []string `json:"workflows"`
		Rules     []string `json:"rules"`
		Verifiers []string `json:"verifiers"`
		Tools     []struct {
			ID string `json:"id"`
		} `json:"tools"`
	}
	var evidence struct {
		Phases map[string]phaseResources `json:"phases"`
	}
	if json.Unmarshal(usage.Resources, &evidence) != nil || evidence.Phases == nil {
		return plugin.Bundle{}, errors.New("tool: active plugin is missing phase-scoped audit evidence")
	}
	resources, ok := evidence.Phases[string(current)]
	if !ok {
		return plugin.Bundle{}, errors.New("tool: active plugin has no audit evidence for this phase")
	}
	knowledge := stringSet(resources.Knowledge)
	workflows := stringSet(resources.Workflows)
	rules := stringSet(resources.Rules)
	verifiers := stringSet(resources.Verifiers)
	tools := make(map[string]bool, len(resources.Tools))
	for _, item := range resources.Tools {
		tools[item.ID] = true
	}
	view := plugin.Bundle{PluginID: bundle.PluginID, Version: bundle.Version}
	for _, item := range bundle.Knowledge {
		if knowledge[item.Path] {
			view.Knowledge = append(view.Knowledge, item)
		}
	}
	for _, item := range bundle.Workflows {
		if workflows[item.ID] {
			view.Workflows = append(view.Workflows, item)
		}
	}
	for _, set := range bundle.Rules {
		filtered := plugin.RuleSet{SchemaVersion: set.SchemaVersion}
		for _, item := range set.Rules {
			if rules[item.ID] {
				filtered.Rules = append(filtered.Rules, item)
			}
		}
		if len(filtered.Rules) > 0 {
			view.Rules = append(view.Rules, filtered)
		}
	}
	for _, item := range bundle.Verifiers {
		if verifiers[item.ID] {
			view.Verifiers = append(view.Verifiers, item)
		}
	}
	for _, item := range bundle.Tools {
		if tools[item.ID] {
			view.Tools = append(view.Tools, item)
		}
	}
	return view, nil
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

type capabilityInput struct {
	Action   string          `json:"action"`
	PluginID string          `json:"plugin_id"`
	ToolID   string          `json:"tool_id"`
	Input    json.RawMessage `json:"input"`
}

func decodeCapability(input json.RawMessage) (capabilityInput, error) {
	var request capabilityInput
	if err := decodeStrict(input, &request); err != nil {
		return capabilityInput{}, err
	}
	switch request.Action {
	case "list":
		if request.PluginID != "" || request.ToolID != "" || len(request.Input) != 0 {
			return capabilityInput{}, fmt.Errorf("%w: list accepts only action", tool.ErrInvalidInput)
		}
	case "get_bundle":
		if !plugin.ValidID(request.PluginID) || request.ToolID != "" || len(request.Input) != 0 {
			return capabilityInput{}, fmt.Errorf("%w: valid plugin_id is required", tool.ErrInvalidInput)
		}
	case "invoke":
		if !plugin.ValidID(request.PluginID) || !plugin.ValidToolID(request.ToolID) ||
			len(request.Input) == 0 || !json.Valid(request.Input) {
			return capabilityInput{}, fmt.Errorf("%w: plugin_id, tool_id, and JSON input are required", tool.ErrInvalidInput)
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(request.Input, &object) != nil || object == nil {
			return capabilityInput{}, fmt.Errorf("%w: plugin input must be an object", tool.ErrInvalidInput)
		}
	default:
		return capabilityInput{}, fmt.Errorf("%w: unsupported capability action", tool.ErrInvalidInput)
	}
	return request, nil
}

func findUsage(usages []plugin.Usage, pluginID string) (plugin.Usage, bool) {
	for _, item := range usages {
		if item.PluginID == pluginID {
			return item, true
		}
	}
	return plugin.Usage{}, false
}

func usageIncludesTool(usage plugin.Usage, toolID string) bool {
	var resources struct {
		Tools []struct {
			ID string `json:"id"`
		} `json:"tools"`
	}
	if json.Unmarshal(usage.Resources, &resources) != nil {
		return false
	}
	for _, item := range resources.Tools {
		if item.ID == toolID {
			return true
		}
	}
	return false
}

var _ tool.Handler = (*Capability)(nil)
