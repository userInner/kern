// Package verifier implements Kern's deterministic completion checks.
package verifier

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/userInner/kern/internal/artifact"
	"github.com/userInner/kern/internal/operation"
	"github.com/userInner/kern/internal/task"
	"github.com/userInner/kern/internal/verification"
	"github.com/userInner/kern/internal/workspace"
)

const maxEvidenceArtifactBytes = 2 << 20

type ledger interface {
	ListAttemptOperations(ctx context.Context, taskID, attemptID string) ([]operation.Record, error)
}

type artifactReader interface {
	List(ctx context.Context, taskID string) ([]artifact.Artifact, error)
	ReadContent(ctx context.Context, taskID, artifactID string, maxBytes int64) ([]byte, error)
}

type workspaceReader interface {
	ReadFile(ctx context.Context, path string) (workspace.File, error)
}

// Suite verifies durable facts produced by one task Attempt. It never accepts
// the Agent's natural-language self-assessment as evidence.
type Suite struct {
	ledger    ledger
	artifacts artifactReader
	workspace workspaceReader
}

// New constructs the Core verifier suite.
func New(ledger ledger, artifacts artifactReader, workspace workspaceReader) (*Suite, error) {
	if ledger == nil || artifacts == nil || workspace == nil {
		return nil, errors.New("verifier: ledger, artifact store, and workspace are required")
	}
	return &Suite{ledger: ledger, artifacts: artifacts, workspace: workspace}, nil
}

// Verify evaluates the final result, operation ledger, command outcomes,
// workspace hashes, artifacts, requested schema, and external side effects.
func (s *Suite) Verify(ctx context.Context, item task.Task, result string) (verification.Report, error) {
	records, err := s.ledger.ListAttemptOperations(ctx, item.ID, item.ActiveAttemptID)
	if err != nil {
		return verification.Report{}, fmt.Errorf("verifier: loading operation ledger: %w", err)
	}
	artifacts, err := s.artifacts.List(ctx, item.ID)
	if err != nil {
		return verification.Report{}, fmt.Errorf("verifier: loading artifacts: %w", err)
	}
	artifacts = attemptArtifacts(artifacts, item.ActiveAttemptID)

	checks := []verification.CheckResult{
		checkResult(item, result),
		checkOperationSafety(item, records),
	}
	if len(records) > 0 {
		checks = append(checks, checkOperationEvidence(item, records, artifacts))
	}
	if hasTool(records, "change") {
		checks = append(checks, s.checkFileIntegrity(ctx, item, records, artifacts))
	}
	if hasEffect(records, operation.EffectProcess) || requiresCommandEvidence(item.Goal) {
		checks = append(checks, checkCommands(item, records))
	}
	if requestsJSON(item.Goal) {
		checks = append(checks, checkJSONResult(item, result))
	}
	if hasExternalWrite(records) {
		checks = append(checks, checkExternalEffects(item, records))
	}

	return verification.Report{
		Status:    verification.Aggregate(checks),
		Checks:    checks,
		CreatedAt: time.Now().UTC(),
	}, nil
}

func checkResult(item task.Task, result string) verification.CheckResult {
	trimmed := strings.TrimSpace(result)
	status := verification.StatusPassed
	summary := fmt.Sprintf("Final result contains %d characters.", len([]rune(trimmed)))
	if trimmed == "" {
		status = verification.StatusFailed
		summary = "Final result is empty."
	}
	return verification.CheckResult{
		Verifier: "core.result",
		Status:   status,
		Required: true,
		Summary:  summary,
		Evidence: []verification.Evidence{{
			Kind:    "task_result",
			Ref:     "task:" + item.ID,
			Summary: summary,
			Digest:  digestString(result),
		}},
	}
}

func checkOperationSafety(item task.Task, records []operation.Record) verification.CheckResult {
	status := verification.StatusPassed
	issues := make([]string, 0)
	for _, record := range records {
		op := record.Operation
		if op.Status == operation.StatusUncertain || !op.Status.IsTerminal() {
			status = verification.StatusFailed
			issues = append(issues, op.ID+" has unresolved status "+string(op.Status))
		}
		if op.Status == operation.StatusSucceeded && op.Effect != operation.EffectRead &&
			record.ApprovalReceiptID == "" {
			status = verification.StatusFailed
			issues = append(issues, op.ID+" has no approval receipt")
		}
	}
	summary := fmt.Sprintf("%d operations are terminal and authorized.", len(records))
	if len(issues) > 0 {
		summary = strings.Join(issues, "; ")
	}
	return verification.CheckResult{
		Verifier: "core.operation_safety",
		Status:   status,
		Required: true,
		Summary:  summary,
		Evidence: []verification.Evidence{{
			Kind:    "operation_ledger",
			Ref:     "attempt:" + item.ActiveAttemptID,
			Summary: summary,
		}},
	}
}

func checkOperationEvidence(
	item task.Task,
	records []operation.Record,
	artifacts []artifact.Artifact,
) verification.CheckResult {
	status := verification.StatusPassed
	missing := make([]string, 0)
	succeeded := 0
	for _, record := range records {
		if record.Operation.Status != operation.StatusSucceeded {
			continue
		}
		succeeded++
		if record.Result == nil {
			status = verification.StatusFailed
			missing = append(missing, record.Operation.ID)
			continue
		}
		if strings.HasPrefix(record.Result.OutputRef, "artifact:") {
			artifactID := strings.TrimPrefix(record.Result.OutputRef, "artifact:")
			stored, ok := findArtifactByID(artifacts, artifactID)
			if !ok || stored.SourceOperationID != record.Operation.ID {
				status = verification.StatusFailed
				missing = append(missing, record.Operation.ID)
			}
			continue
		}
		if record.Result.OutputRef == "" && record.Result.ExitCode == nil {
			status = verification.StatusFailed
			missing = append(missing, record.Operation.ID)
		}
	}
	summary := fmt.Sprintf("%d successful operations have durable output evidence.", succeeded)
	if len(missing) > 0 {
		summary = "Missing durable output evidence for operations: " + strings.Join(missing, ", ")
	}
	return verification.CheckResult{
		Verifier: "core.operation_evidence",
		Status:   status,
		Required: true,
		Summary:  summary,
		Evidence: []verification.Evidence{{
			Kind:    "operation_results",
			Ref:     "attempt:" + item.ActiveAttemptID,
			Summary: summary,
		}},
	}
}

func (s *Suite) checkFileIntegrity(
	ctx context.Context,
	item task.Task,
	records []operation.Record,
	artifacts []artifact.Artifact,
) verification.CheckResult {
	type observedChange struct {
		operationID string
		change      workspace.Change
	}
	latest := make(map[string]observedChange)
	issues := make([]string, 0)
	evidence := make([]verification.Evidence, 0)
	for _, record := range records {
		if record.Operation.Tool != "change" || record.Operation.Status != operation.StatusSucceeded {
			continue
		}
		raw, ok := findArtifact(artifacts, record.Operation.ID, "application/json")
		if !ok {
			issues = append(issues, record.Operation.ID+" has no JSON output artifact")
			continue
		}
		data, err := s.artifacts.ReadContent(ctx, item.ID, raw.ID, maxEvidenceArtifactBytes)
		if err != nil {
			issues = append(issues, record.Operation.ID+" output cannot be read: "+err.Error())
			continue
		}
		var change workspace.Change
		if err := json.Unmarshal(data, &change); err != nil || change.Path == "" || change.AfterSHA256 == "" {
			issues = append(issues, record.Operation.ID+" has invalid change metadata")
			continue
		}
		if change.Diff != "" {
			if _, ok := findArtifact(artifacts, record.Operation.ID, "text/x-diff"); !ok {
				issues = append(issues, record.Operation.ID+" has no diff artifact")
			}
		}
		latest[change.Path] = observedChange{operationID: record.Operation.ID, change: change}
	}
	paths := make([]string, 0, len(latest))
	for path := range latest {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		observed := latest[path]
		file, err := s.workspace.ReadFile(ctx, path)
		if err != nil {
			issues = append(issues, path+" cannot be read: "+err.Error())
			continue
		}
		if file.SHA256 != observed.change.AfterSHA256 {
			issues = append(issues, path+" no longer matches operation "+observed.operationID)
			continue
		}
		evidence = append(evidence, verification.Evidence{
			Kind:    "workspace_file",
			Ref:     "workspace:" + path,
			Summary: "Current file matches the last successful atomic change.",
			Digest:  file.SHA256,
		})
	}
	status := verification.StatusPassed
	summary := fmt.Sprintf("%d changed files match their recorded SHA-256 values.", len(latest))
	if len(latest) == 0 {
		status = verification.StatusFailed
		issues = append(issues, "no successful change metadata was found")
	}
	if len(issues) > 0 {
		status = verification.StatusFailed
		summary = strings.Join(issues, "; ")
	}
	if len(evidence) == 0 {
		evidence = append(evidence, verification.Evidence{
			Kind:    "artifact_set",
			Ref:     "attempt:" + item.ActiveAttemptID,
			Summary: summary,
		})
	}
	return verification.CheckResult{
		Verifier: "core.file_integrity",
		Status:   status,
		Required: true,
		Summary:  summary,
		Evidence: evidence,
	}
}

func checkCommands(item task.Task, records []operation.Record) verification.CheckResult {
	seen := 0
	failed := 0
	invalid := make([]string, 0)
	for _, record := range records {
		if record.Operation.Effect != operation.EffectProcess {
			continue
		}
		seen++
		if record.Operation.Status != operation.StatusSucceeded {
			failed++
			continue
		}
		if record.Result == nil || record.Result.ExitCode == nil || *record.Result.ExitCode != 0 {
			invalid = append(invalid, record.Operation.ID)
		}
	}
	status := verification.StatusPassed
	summary := fmt.Sprintf("%d command operations completed with recorded zero exit codes.", seen)
	if seen == 0 {
		status = verification.StatusNotRun
		summary = "The task requested command or test evidence, but no command was run."
	} else if len(invalid) > 0 {
		status = verification.StatusFailed
		summary = "Successful commands missing a recorded zero exit code: " + strings.Join(invalid, ", ")
	} else if failed > 0 {
		status = verification.StatusPartial
		summary = fmt.Sprintf("%d of %d command operations failed or were cancelled.", failed, seen)
	}
	return verification.CheckResult{
		Verifier: "core.command_exit",
		Status:   status,
		Required: true,
		Summary:  summary,
		Evidence: []verification.Evidence{{
			Kind:    "operation_results",
			Ref:     "attempt:" + item.ActiveAttemptID,
			Summary: summary,
		}},
	}
}

func checkJSONResult(item task.Task, result string) verification.CheckResult {
	valid := json.Valid([]byte(strings.TrimSpace(result)))
	status := verification.StatusPassed
	summary := "The final result is valid JSON."
	if !valid {
		status = verification.StatusFailed
		summary = "The task requested JSON, but the final result is not valid JSON."
	}
	return verification.CheckResult{
		Verifier: "core.json_schema",
		Status:   status,
		Required: true,
		Summary:  summary,
		Evidence: []verification.Evidence{{
			Kind:    "task_result",
			Ref:     "task:" + item.ID,
			Summary: summary,
			Digest:  digestString(result),
		}},
	}
}

func checkExternalEffects(item task.Task, records []operation.Record) verification.CheckResult {
	ids := make([]string, 0)
	for _, record := range records {
		if record.Operation.Status == operation.StatusSucceeded &&
			(record.Operation.Effect == operation.EffectNetworkWrite ||
				record.Operation.Effect == operation.EffectDestructive) {
			ids = append(ids, record.Operation.ID)
		}
	}
	summary := "External side effects require confirmation in the real target environment: " + strings.Join(ids, ", ")
	return verification.CheckResult{
		Verifier: "core.external_effect",
		Status:   verification.StatusManualRequired,
		Required: true,
		Summary:  summary,
		Evidence: []verification.Evidence{{
			Kind:    "operation_ledger",
			Ref:     "attempt:" + item.ActiveAttemptID,
			Summary: summary,
		}},
	}
}

func attemptArtifacts(items []artifact.Artifact, attemptID string) []artifact.Artifact {
	result := make([]artifact.Artifact, 0, len(items))
	for _, item := range items {
		if item.AttemptID == attemptID {
			result = append(result, item)
		}
	}
	return result
}

func findArtifact(items []artifact.Artifact, operationID, mediaType string) (artifact.Artifact, bool) {
	for _, item := range items {
		if item.SourceOperationID == operationID && item.MediaType == mediaType {
			return item, true
		}
	}
	return artifact.Artifact{}, false
}

func findArtifactByID(items []artifact.Artifact, artifactID string) (artifact.Artifact, bool) {
	for _, item := range items {
		if item.ID == artifactID {
			return item, true
		}
	}
	return artifact.Artifact{}, false
}

func hasTool(records []operation.Record, name string) bool {
	for _, record := range records {
		if record.Operation.Tool == name {
			return true
		}
	}
	return false
}

func hasEffect(records []operation.Record, effect operation.Effect) bool {
	for _, record := range records {
		if record.Operation.Effect == effect {
			return true
		}
	}
	return false
}

func hasExternalWrite(records []operation.Record) bool {
	for _, record := range records {
		if record.Operation.Status == operation.StatusSucceeded &&
			(record.Operation.Effect == operation.EffectNetworkWrite ||
				record.Operation.Effect == operation.EffectDestructive) {
			return true
		}
	}
	return false
}

func requiresCommandEvidence(goal string) bool {
	goal = strings.ToLower(goal)
	for _, marker := range []string{
		"run tests", "run the tests", "go test", "npm test", "pnpm test",
		"运行测试", "执行测试", "跑测试", "编译项目", "构建项目",
	} {
		if strings.Contains(goal, marker) {
			return true
		}
	}
	return false
}

func requestsJSON(goal string) bool {
	goal = strings.ToLower(goal)
	for _, marker := range []string{"return json", "output json", "valid json", "返回 json", "输出 json", "json 格式"} {
		if strings.Contains(goal, marker) {
			return true
		}
	}
	return false
}

func digestString(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
