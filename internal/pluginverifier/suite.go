// Package pluginverifier adds deterministic plugin checks to the immutable
// Core verifier report. It consumes evidence already produced through Core
// tools and never executes plugin commands directly.
package pluginverifier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/userInner/kern/internal/operation"
	"github.com/userInner/kern/internal/plugin"
	"github.com/userInner/kern/internal/task"
	"github.com/userInner/kern/internal/verification"
	"github.com/userInner/kern/internal/workspace"
)

type coreVerifier interface {
	Verify(ctx context.Context, item task.Task, result string) (verification.Report, error)
}

type usageRepository interface {
	ListAttemptPlugins(ctx context.Context, taskID, attemptID string) ([]plugin.Usage, error)
	ListAttemptOperations(ctx context.Context, taskID, attemptID string) ([]operation.Record, error)
}

type pluginGetter interface {
	Get(ctx context.Context, pluginID string) (plugin.Installed, error)
}

type workspaceReader interface {
	ReadFile(ctx context.Context, path string) (workspace.File, error)
}

// Suite composes additive plugin checks after Core verification.
type Suite struct {
	core      coreVerifier
	repo      usageRepository
	plugins   pluginGetter
	workspace workspaceReader
}

// New constructs a composed verifier suite.
func New(
	core coreVerifier,
	repo usageRepository,
	plugins pluginGetter,
	workspace workspaceReader,
) (*Suite, error) {
	if core == nil || repo == nil || plugins == nil || workspace == nil {
		return nil, errors.New("pluginverifier: all dependencies are required")
	}
	return &Suite{core: core, repo: repo, plugins: plugins, workspace: workspace}, nil
}

// Verify preserves every Core check and appends deterministic checks declared
// by the exact plugin versions selected for this Attempt.
func (s *Suite) Verify(
	ctx context.Context,
	item task.Task,
	result string,
) (verification.Report, error) {
	report, err := s.core.Verify(ctx, item, result)
	if err != nil {
		return verification.Report{}, err
	}
	usages, err := s.repo.ListAttemptPlugins(ctx, item.ID, item.ActiveAttemptID)
	if err != nil {
		return verification.Report{}, fmt.Errorf("pluginverifier: loading plugin audit: %w", err)
	}
	if len(usages) == 0 {
		return report, nil
	}
	records, err := s.repo.ListAttemptOperations(ctx, item.ID, item.ActiveAttemptID)
	if err != nil {
		return verification.Report{}, fmt.Errorf("pluginverifier: loading operation evidence: %w", err)
	}
	for _, usage := range usages {
		installed, err := s.plugins.Get(ctx, usage.PluginID)
		if err != nil || installed.Version != usage.Version || installed.Digest != usage.Digest {
			report.Checks = append(report.Checks, unavailableCheck(usage))
			continue
		}
		bundle, err := plugin.LoadBundle(installed)
		if err != nil {
			report.Checks = append(report.Checks, unavailableCheck(usage))
			continue
		}
		for _, spec := range bundle.Verifiers {
			switch spec.Type {
			case "command":
				report.Checks = append(report.Checks, commandCheck(usage, spec, records))
			case "file_exists":
				report.Checks = append(report.Checks, s.fileCheck(ctx, usage, spec))
			}
		}
	}
	report.Status = verification.Aggregate(report.Checks)
	return report, nil
}

func commandCheck(
	usage plugin.Usage,
	spec plugin.VerifierSpec,
	records []operation.Record,
) verification.CheckResult {
	type executeInput struct {
		Argv []string `json:"argv"`
	}
	matched := false
	succeeded := false
	operationID := ""
	for _, record := range records {
		if record.Operation.Tool != "execute" {
			continue
		}
		var input executeInput
		if json.Unmarshal(record.Operation.Input, &input) != nil || !slices.Equal(input.Argv, spec.Command) {
			continue
		}
		matched = true
		operationID = record.Operation.ID
		if record.Operation.Status == operation.StatusSucceeded && record.Result != nil &&
			record.Result.ExitCode != nil && *record.Result.ExitCode == 0 {
			succeeded = true
			break
		}
	}
	status := verification.StatusPassed
	summary := "Required command completed successfully through the Core execute tool."
	ref := "operation:" + operationID
	if !matched {
		status = verification.StatusNotRun
		summary = "No exact Core execute operation was found for: " + strings.Join(spec.Command, " ")
		ref = "attempt:" + usage.AttemptID
	} else if !succeeded {
		status = verification.StatusFailed
		summary = "The exact verifier command did not complete successfully."
	}
	return verification.CheckResult{
		Verifier: pluginVerifierID(usage, spec.ID),
		Status:   status,
		Required: spec.Required,
		Summary:  summary,
		Evidence: []verification.Evidence{{
			Kind:    "operation_ledger",
			Ref:     ref,
			Summary: summary,
		}},
	}
}

func (s *Suite) fileCheck(
	ctx context.Context,
	usage plugin.Usage,
	spec plugin.VerifierSpec,
) verification.CheckResult {
	file, err := s.workspace.ReadFile(ctx, spec.Path)
	status := verification.StatusPassed
	summary := "Required workspace file exists and is readable within the Core sandbox."
	digest := file.SHA256
	if err != nil {
		status = verification.StatusFailed
		summary = "Required workspace file is unavailable within the Core sandbox."
		digest = ""
	}
	return verification.CheckResult{
		Verifier: pluginVerifierID(usage, spec.ID),
		Status:   status,
		Required: spec.Required,
		Summary:  summary,
		Evidence: []verification.Evidence{{
			Kind:    "workspace_file",
			Ref:     "workspace:" + spec.Path,
			Summary: summary,
			Digest:  digest,
		}},
	}
}

func unavailableCheck(usage plugin.Usage) verification.CheckResult {
	summary := "The activated plugin package is unavailable, changed, or failed integrity validation; Core verification continued without it."
	return verification.CheckResult{
		Verifier: "plugin:" + usage.PluginID + ":integrity",
		Status:   verification.StatusPartial,
		Required: false,
		Summary:  summary,
		Evidence: []verification.Evidence{{
			Kind:    "plugin_audit",
			Ref:     "plugin:" + usage.PluginID + "@" + usage.Version,
			Summary: summary,
			Digest:  usage.Digest,
		}},
	}
}

func pluginVerifierID(usage plugin.Usage, verifierID string) string {
	return "plugin:" + usage.PluginID + ":" + verifierID
}
