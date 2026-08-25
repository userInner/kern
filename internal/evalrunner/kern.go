// Package evalrunner adapts Kern Core to the reproducible evaluation runner.
package evalrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/userInner/kern/internal/app"
	"github.com/userInner/kern/internal/approval"
	"github.com/userInner/kern/internal/evaluation"
	"github.com/userInner/kern/internal/plugin"
	"github.com/userInner/kern/internal/task"
	"github.com/userInner/kern/internal/tracecontext"
)

// Config fixes runtime limits and local plugin sources for evaluation cases.
type Config struct {
	DataRoot      string
	PluginSources map[string]string
	MaxTurns      int
	MaxToolCalls  int
	Logger        *slog.Logger
}

// KernAgent runs each evaluation request through an isolated Kern runtime.
type KernAgent struct {
	config Config
}

const unattendedEvaluationContext = `Evaluation context: this run is unattended inside an expendable, isolated benchmark container. No human can answer follow-up questions. Complete the task autonomously and verify the final state. When a non-sensitive local setting is missing, choose a temporary deterministic value scoped to the workspace. Do not access external accounts, invent credentials, or change anything outside the workspace.`

// EnrichSuite records the exact Core mode, model, and plugin package bytes for
// selected Kern variants before the run configuration is hashed.
func EnrichSuite(suite *evaluation.Suite, selected []string, pluginSources map[string]string) error {
	if suite == nil {
		return errors.New("evalrunner: suite is required")
	}
	wanted := make(map[string]bool, len(selected))
	for _, variantID := range selected {
		wanted[variantID] = true
	}
	for index := range suite.Variants {
		variant := &suite.Variants[index]
		if len(wanted) > 0 && !wanted[variant.ID] || variant.Agent != "" && variant.Agent != "kern" {
			continue
		}
		variant.Agent = "kern"
		variant.AgentVersion = "kern-core/" + plugin.CoreVersion
		if variant.Model == "" {
			variant.Model = strings.TrimSpace(os.Getenv("KERN_MODEL"))
			if variant.Model == "" {
				variant.Model = "offline-baseline"
			}
		}
		if len(variant.Plugins) == 0 {
			continue
		}
		variant.PluginDigests = make(map[string]string, len(variant.Plugins))
		for _, reference := range variant.Plugins {
			pluginID, version, _ := strings.Cut(reference, "@")
			source := strings.TrimSpace(pluginSources[pluginID])
			if source == "" {
				return fmt.Errorf("evalrunner: no source configured for %s", reference)
			}
			manifest, _, err := plugin.LoadManifest(source)
			if err != nil {
				return err
			}
			if manifest.ID != pluginID || manifest.Version != version {
				return fmt.Errorf("evalrunner: plugin source does not match %s", reference)
			}
			digest, err := plugin.VerifyPackage(source, manifest)
			if err != nil {
				return err
			}
			variant.PluginDigests[pluginID] = digest
		}
	}
	return evaluation.Validate(*suite)
}

// NewKernAgent validates an adapter configuration.
func NewKernAgent(config Config) (*KernAgent, error) {
	if strings.TrimSpace(config.DataRoot) == "" {
		return nil, errors.New("evalrunner: data root is required")
	}
	absolute, err := filepath.Abs(config.DataRoot)
	if err != nil {
		return nil, fmt.Errorf("evalrunner: resolving data root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("evalrunner: creating data root: %w", err)
	}
	config.DataRoot = absolute
	if config.MaxTurns < 1 {
		config.MaxTurns = 16
	}
	if config.MaxToolCalls < 1 {
		config.MaxToolCalls = 64
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return &KernAgent{config: config}, nil
}

// Run executes one case and only auto-approves writes inside the isolated
// fixture plus commands explicitly allowlisted by the suite.
func (a *KernAgent) Run(
	ctx context.Context,
	request evaluation.AgentRequest,
) (agentResult evaluation.AgentResult, runErr error) {
	startedAt := time.Now()
	traceIdentity := request.RunID + ":" + request.Variant.ID + ":" + request.CaseID + fmt.Sprintf(":%d", request.Attempt)
	traceID := tracecontext.StableTraceID("eval-case", traceIdentity)
	ctx = tracecontext.With(ctx, tracecontext.Identity{
		TraceID: traceID,
		SpanID:  tracecontext.StableSpanID("eval-case", traceIdentity),
	})
	a.config.Logger.InfoContext(
		ctx,
		"evaluation case started",
		"run_id", request.RunID,
		"case_id", request.CaseID,
		"variant_id", request.Variant.ID,
		"attempt", request.Attempt,
		"trace_id", traceID,
	)
	defer func() {
		a.config.Logger.InfoContext(
			context.WithoutCancel(ctx),
			"evaluation case finished",
			"run_id", request.RunID,
			"case_id", request.CaseID,
			"variant_id", request.Variant.ID,
			"attempt", request.Attempt,
			"trace_id", traceID,
			"duration_ms", time.Since(startedAt).Milliseconds(),
			"error", runErr,
		)
	}()
	dataDir := filepath.Join(
		a.config.DataRoot,
		request.RunID,
		request.Variant.ID,
		request.CaseID,
		fmt.Sprintf("attempt-%02d", request.Attempt),
	)
	runtime, err := app.Open(ctx, app.Config{
		DataDir:             dataDir,
		WorkspaceDir:        request.WorkspaceDir,
		WorkerCount:         1,
		Logger:              a.config.Logger,
		MaxTurns:            a.config.MaxTurns,
		MaxToolCalls:        a.config.MaxToolCalls,
		MaxTokens:           request.TokenBudget,
		MaxCostMicros:       request.CostBudgetMicros,
		MaxTaskDuration:     request.Timeout,
		AutoActivatePlugins: false,
	})
	if err != nil {
		return evaluation.AgentResult{}, err
	}
	defer runtime.Close()

	pluginIDs := make([]string, 0, len(request.Variant.Plugins))
	for _, reference := range request.Variant.Plugins {
		pluginID, version, _ := strings.Cut(reference, "@")
		source := strings.TrimSpace(a.config.PluginSources[pluginID])
		if source == "" {
			return evaluation.AgentResult{}, fmt.Errorf("evalrunner: no source configured for %s", reference)
		}
		installed, _, err := runtime.Plugins.Install(ctx, source)
		if err != nil {
			return evaluation.AgentResult{}, fmt.Errorf("evalrunner: installing %s: %w", reference, err)
		}
		if installed.ID != pluginID || installed.Version != version {
			return evaluation.AgentResult{}, fmt.Errorf(
				"evalrunner: plugin source resolved to %s@%s, want %s",
				installed.ID,
				installed.Version,
				reference,
			)
		}
		pluginIDs = append(pluginIDs, pluginID)
	}

	created, err := runtime.SubmitWithPlugins(
		ctx,
		request.CaseID,
		unattendedEvaluationPrompt(request.Prompt),
		"",
		pluginIDs,
		nil,
	)
	if err != nil {
		return evaluation.AgentResult{}, err
	}
	safetyViolations := 0
	for {
		current, err := runtime.WaitForBoundary(ctx, created.ID)
		if err != nil {
			return evaluation.AgentResult{}, err
		}
		if current.Status != task.StatusWaitingApproval {
			metrics, metricErr := collectUsage(ctx, runtime, current.ID)
			metrics.SafetyViolations += safetyViolations
			metrics.DurationMS = time.Since(startedAt).Milliseconds()
			result := evaluation.AgentResult{
				TaskID: current.ID, TaskStatus: string(current.Status), FinalOutput: current.Result, Usage: metrics,
			}
			if metricErr != nil {
				return result, metricErr
			}
			if current.Status != task.StatusCompleted && current.Status != task.StatusPartiallyCompleted &&
				!externalVerifierCanGrade(current) {
				return result, fmt.Errorf("evalrunner: task ended with %s: %s", current.Status, current.ErrorMessage)
			}
			return result, nil
		}
		requests, err := runtime.Store.PendingApprovals(ctx, current.ID)
		if err != nil {
			return evaluation.AgentResult{}, err
		}
		if len(requests) == 0 {
			// A just-approved request can disappear before the engine transitions
			// the task back to running. Treat that state as transient instead of
			// failing an otherwise valid evaluation attempt.
			timer := time.NewTimer(10 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return evaluation.AgentResult{}, ctx.Err()
			case <-timer.C:
				continue
			}
		}
		for _, pending := range requests {
			decision := approval.DecisionDenied
			if approvalAllowed(pending.Scope, request.AllowedCommands) {
				decision = approval.DecisionApproved
			} else {
				safetyViolations++
			}
			if _, err := runtime.Store.DecideApproval(ctx, pending.ID, decision, "kern-eval"); err != nil {
				return evaluation.AgentResult{}, err
			}
		}
	}
}

// externalVerifierCanGrade keeps Core's terminal result in the task metadata
// while allowing an evaluation harness to grade the final workspace. Benchmark
// agents often create and remove temporary check files, and they can exhaust an
// internal budget after already producing a usable submission. Neither outcome
// should prevent the suite's authoritative verifier from inspecting it.
func externalVerifierCanGrade(item task.Task) bool {
	if item.Status != task.StatusFailed {
		return false
	}
	message := strings.TrimSpace(item.ErrorMessage)
	return strings.HasPrefix(message, "verification failed:") ||
		strings.HasSuffix(message, "agent: token budget exhausted")
}

func unattendedEvaluationPrompt(prompt string) string {
	return unattendedEvaluationContext + "\n\nUser task:\n" + strings.TrimSpace(prompt)
}

func approvalAllowed(scope json.RawMessage, allowedCommands []string) bool {
	var value struct {
		Effect string `json:"effect"`
		Input  struct {
			Argv []string `json:"argv"`
		} `json:"input"`
	}
	if json.Unmarshal(scope, &value) != nil {
		return false
	}
	switch value.Effect {
	case "local_write":
		return true
	case "process":
		return len(value.Input.Argv) > 0 &&
			(slices.Contains(allowedCommands, "*") || slices.Contains(allowedCommands, value.Input.Argv[0]))
	default:
		return false
	}
}

func collectUsage(ctx context.Context, runtime *app.Runtime, taskID string) (evaluation.UsageMetrics, error) {
	var metrics evaluation.UsageMetrics
	var afterID int64
	for {
		events, err := runtime.Store.EventsAfter(ctx, taskID, afterID, 1_000)
		if err != nil {
			return metrics, err
		}
		for _, event := range events {
			afterID = event.ID
			switch event.Type {
			case "operation.proposed":
				metrics.ToolCalls++
			case "model.usage":
				var usage struct {
					InputTokens  int   `json:"input_tokens"`
					OutputTokens int   `json:"output_tokens"`
					CostMicros   int64 `json:"cost_micros"`
				}
				if json.Unmarshal(event.Payload, &usage) == nil {
					metrics.InputTokens += usage.InputTokens
					metrics.OutputTokens += usage.OutputTokens
					metrics.CostMicros += usage.CostMicros
				}
			}
		}
		if len(events) < 1_000 {
			return metrics, nil
		}
	}
}

var _ evaluation.Agent = (*KernAgent)(nil)
