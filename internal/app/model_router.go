package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/userInner/kern/internal/agent"
	"github.com/userInner/kern/internal/model/compatible"
	"github.com/userInner/kern/internal/modelconfig"
	"github.com/userInner/kern/internal/store/sqlite"
	"github.com/userInner/kern/internal/task"
)

type secretResolver interface {
	Resolve(ctx context.Context, reference string) (string, error)
}

type modelRouter struct {
	store    *sqlite.Store
	secrets  secretResolver
	fallback processor
	tools    agent.ToolSet
	configs  *agentConfigSource
}

func newModelRouter(
	store *sqlite.Store,
	secrets secretResolver,
	fallback processor,
	tools agent.ToolSet,
	configs *agentConfigSource,
) (*modelRouter, error) {
	if store == nil || secrets == nil || fallback == nil || configs == nil {
		return nil, errors.New("app: model router dependencies are required")
	}
	return &modelRouter{
		store: store, secrets: secrets, fallback: fallback, tools: tools, configs: configs,
	}, nil
}

func (r *modelRouter) Process(ctx context.Context, item task.Task) (string, error) {
	if item.ModelConfigID == "" {
		return r.fallback.Process(ctx, item)
	}
	config, err := r.store.GetModelConfig(ctx, item.ModelConfigID)
	if err != nil {
		return "", fmt.Errorf("loading task model config: %w", err)
	}
	if !config.Enabled {
		return "", modelconfig.ErrDisabled
	}
	apiKey := ""
	if config.SecretRef != "" {
		apiKey, err = r.secrets.Resolve(ctx, config.SecretRef)
		if err != nil {
			return "", fmt.Errorf("resolving model credential: %w", err)
		}
	}
	client, err := compatible.New(compatible.Config{
		BaseURL: config.BaseURL,
		APIKey:  apiKey,
		Model:   config.Model,
	})
	apiKey = ""
	if err != nil {
		return "", err
	}
	agentConfig := r.configs.Current()
	parameters, err := json.Marshal(map[string]any{
		"max_turns":       agentConfig.MaxTurns,
		"max_tool_calls":  agentConfig.MaxToolCalls,
		"max_tokens":      agentConfig.MaxTokens,
		"max_cost_micros": agentConfig.MaxCostMicros,
		"max_duration_ms": agentConfig.MaxDuration.Milliseconds(),
		"tool_choice":     "auto",
	})
	if err != nil {
		return "", fmt.Errorf("encoding model parameters: %w", err)
	}
	selection, created, err := r.store.RecordModelSelection(ctx, item, config, parameters)
	if err != nil {
		return "", err
	}
	if created {
		if err := r.store.AppendEvent(ctx, item.ID, item.ActiveAttemptID, "model.selected", map[string]any{
			"config_id":  config.ID,
			"provider":   config.Provider,
			"model":      config.Model,
			"parameters": selection.Parameters,
		}); err != nil {
			return "", fmt.Errorf("persisting model selection event: %w", err)
		}
	}
	runner, err := agent.New(client, r.store, r.store, r.tools, agentConfig)
	if err != nil {
		return "", err
	}
	return runner.Process(ctx, item)
}
