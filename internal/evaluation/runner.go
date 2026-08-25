package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/userInner/kern/internal/plugin"

	"github.com/userInner/kern/internal/id"
)

// AgentRequest fixes all inputs that may differ between evaluation variants.
type AgentRequest struct {
	RunID            string
	CaseID           string
	Variant          Variant
	Attempt          int
	WorkspaceDir     string
	Prompt           string
	Timeout          time.Duration
	TokenBudget      int
	CostBudgetMicros int64
	AllowedCommands  []string
}

// AgentResult reports task identity and comparable resource usage.
type AgentResult struct {
	TaskID      string
	TaskStatus  string
	FinalOutput string
	Usage       UsageMetrics
}

// Agent executes one isolated case. Implementations may embed Kern or adapt an
// external baseline while preserving the fixed request contract.
type Agent interface {
	Run(ctx context.Context, request AgentRequest) (AgentResult, error)
}

// Runner executes a bounded, reproducible suite.
type Runner struct {
	root   string
	agent  Agent
	judge  ModelJudge
	logger *slog.Logger
}

// NewRunner constructs a runner that retains each isolated workspace beneath root.
func NewRunner(root string, agent Agent, logger *slog.Logger) (*Runner, error) {
	return NewRunnerWithJudge(root, agent, nil, logger)
}

// NewRunnerWithJudge constructs a runner with an optional tool-free model
// judge. A suite that declares model graders requires a non-nil judge.
func NewRunnerWithJudge(root string, agent Agent, judge ModelJudge, logger *slog.Logger) (*Runner, error) {
	if strings.TrimSpace(root) == "" || agent == nil {
		return nil, errors.New("evaluation: run root and agent are required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("evaluation: resolving run root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("evaluation: creating run root: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{root: absolute, agent: agent, judge: judge, logger: logger}, nil
}

type runJob struct {
	variant  Variant
	evalCase Case
	done     chan struct{}
}

// Progress is emitted after one case/variant job has exhausted retries or
// passed. CompletedCases counts jobs, not individual attempts.
type Progress struct {
	RunID          string       `json:"run_id"`
	CaseID         string       `json:"case_id"`
	VariantID      string       `json:"variant_id"`
	CompletedCases int          `json:"completed_cases"`
	TotalCases     int          `json:"total_cases"`
	Results        []CaseResult `json:"results"`
}

// ProgressObserver durably consumes one terminal case/variant job update.
// Returning an error cancels the evaluation instead of silently losing
// progress state.
type ProgressObserver func(ctx context.Context, progress Progress) error

// Run executes the selected variants, or every suite variant when selected is empty.
func (r *Runner) Run(ctx context.Context, suite Suite, selected []string) (Report, error) {
	runID, err := id.New()
	if err != nil {
		return Report{}, fmt.Errorf("evaluation: creating run ID: %w", err)
	}
	return r.RunWithID(ctx, runID, suite, selected)
}

// RunWithID executes a suite using an ID already persisted by a service. This
// keeps asynchronous API state and the immutable report on the same identity.
func (r *Runner) RunWithID(
	ctx context.Context,
	runID string,
	suite Suite,
	selected []string,
) (Report, error) {
	return r.RunWithIDProgress(ctx, runID, suite, selected, nil)
}

// RunWithIDProgress executes a suite and reports durable job-level progress.
func (r *Runner) RunWithIDProgress(
	ctx context.Context,
	runID string,
	suite Suite,
	selected []string,
	observer ProgressObserver,
) (Report, error) {
	return r.RunWithIDProgressFrom(ctx, runID, suite, selected, nil, time.Time{}, observer)
}

// RunWithIDProgressFrom resumes a run from atomically persisted terminal
// Case/Variant jobs. A partially executing job is intentionally absent from
// prior and therefore starts again from a clean fixture workspace.
func (r *Runner) RunWithIDProgressFrom(
	ctx context.Context,
	runID string,
	suite Suite,
	selected []string,
	prior []CaseResult,
	startedAt time.Time,
	observer ProgressObserver,
) (Report, error) {
	if strings.TrimSpace(runID) == "" {
		return Report{}, errors.New("evaluation: run ID is required")
	}
	if err := Validate(suite); err != nil {
		return Report{}, err
	}
	if suiteUsesModelGrader(suite) && r.judge == nil {
		return Report{}, errors.New("evaluation: model grader is configured but judge runtime is unavailable")
	}
	variants, err := selectVariants(suite.Variants, selected)
	if err != nil {
		return Report{}, err
	}
	configDigest, reproducibility, err := suiteDigest(suite, variants)
	if err != nil {
		return Report{}, err
	}
	completedJobs, err := validatePriorResults(suite, variants, prior)
	if err != nil {
		return Report{}, err
	}
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	} else {
		startedAt = startedAt.UTC()
	}
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	jobs := make(chan runJob)
	type jobResult struct {
		variantID string
		caseID    string
		results   []CaseResult
	}
	results := make(chan jobResult)
	remainingJobs := len(variants)*len(suite.Cases) - len(completedJobs)
	workerCount := min(suite.Defaults.WorkerCount, remainingJobs)
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for job := range jobs {
				func() {
					defer close(job.done)
					completed := r.runCase(runCtx, runID, suite, job.variant, job.evalCase)
					select {
					case results <- jobResult{variantID: job.variant.ID, caseID: job.evalCase.ID, results: completed}:
					case <-runCtx.Done():
					}
				}()
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, evalCase := range suite.Cases {
			batch := make([]<-chan struct{}, 0, len(variants))
			for _, variant := range variants {
				if completedJobs[resultJobKey(variant.ID, evalCase.ID)] {
					continue
				}
				done := make(chan struct{})
				select {
				case jobs <- runJob{variant: variant, evalCase: evalCase, done: done}:
					batch = append(batch, done)
				case <-runCtx.Done():
					return
				}
			}
			for _, done := range batch {
				select {
				case <-done:
				case <-runCtx.Done():
					return
				}
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()
	collected := append([]CaseResult(nil), prior...)
	completedCases := len(completedJobs)
	var observerErr error
	totalCases := len(variants) * len(suite.Cases)
	for completed := range results {
		collected = append(collected, completed.results...)
		completedCases++
		if observer != nil && observerErr == nil && runCtx.Err() == nil {
			progress := Progress{
				RunID: runID, CaseID: completed.caseID, VariantID: completed.variantID,
				CompletedCases: completedCases, TotalCases: totalCases,
				Results: append([]CaseResult(nil), completed.results...),
			}
			if err := observer(runCtx, progress); err != nil {
				observerErr = fmt.Errorf("evaluation: recording progress: %w", err)
				cancel(observerErr)
			}
		}
	}
	if observerErr != nil {
		return Report{}, observerErr
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	sort.Slice(collected, func(i, j int) bool {
		if collected[i].VariantID != collected[j].VariantID {
			return collected[i].VariantID < collected[j].VariantID
		}
		if collected[i].CaseID != collected[j].CaseID {
			return collected[i].CaseID < collected[j].CaseID
		}
		return collected[i].Attempt < collected[j].Attempt
	})
	selectedSuite := suite
	selectedSuite.Variants = variants
	return BuildReportWithReproducibility(
		runID,
		configDigest,
		selectedSuite,
		collected,
		reproducibility,
		startedAt,
		time.Now().UTC(),
	), nil
}

// PrepareRun computes the immutable identity a service persists before any
// case starts. Resume must produce the same digest before prior results may be
// combined with new work.
func PrepareRun(suite Suite, selected []string) (string, Reproducibility, error) {
	if err := Validate(suite); err != nil {
		return "", Reproducibility{}, err
	}
	variants, err := selectVariants(suite.Variants, selected)
	if err != nil {
		return "", Reproducibility{}, err
	}
	return suiteDigest(suite, variants)
}

func validatePriorResults(suite Suite, variants []Variant, prior []CaseResult) (map[string]bool, error) {
	variantIDs := make(map[string]bool, len(variants))
	for _, variant := range variants {
		variantIDs[variant.ID] = true
	}
	caseIDs := make(map[string]bool, len(suite.Cases))
	for _, evalCase := range suite.Cases {
		caseIDs[evalCase.ID] = true
	}
	completed := make(map[string]bool)
	attempts := make(map[string]bool, len(prior))
	for _, result := range prior {
		if !variantIDs[result.VariantID] || !caseIDs[result.CaseID] ||
			result.Attempt < 1 || result.Attempt > suite.Defaults.Retries+1 {
			return nil, fmt.Errorf("%w: invalid prior result %s/%s attempt %d", ErrInvalidSuite, result.VariantID, result.CaseID, result.Attempt)
		}
		attemptKey := fmt.Sprintf("%s\x00%s\x00%d", result.VariantID, result.CaseID, result.Attempt)
		if attempts[attemptKey] {
			return nil, fmt.Errorf("%w: duplicate prior result", ErrInvalidSuite)
		}
		attempts[attemptKey] = true
		completed[resultJobKey(result.VariantID, result.CaseID)] = true
	}
	return completed, nil
}

func resultJobKey(variantID, caseID string) string {
	return variantID + "\x00" + caseID
}

func (r *Runner) runCase(ctx context.Context, runID string, suite Suite, variant Variant, evalCase Case) []CaseResult {
	prompt, err := suite.PromptText(evalCase)
	if err != nil {
		return []CaseResult{failedCase(evalCase.ID, variant.ID, 1, err)}
	}
	fixture, err := suite.FixturePath(evalCase)
	if err != nil {
		return []CaseResult{failedCase(evalCase.ID, variant.ID, 1, err)}
	}
	results := make([]CaseResult, 0, suite.Defaults.Retries+1)
	for attempt := 1; attempt <= suite.Defaults.Retries+1; attempt++ {
		startedAt := time.Now().UTC()
		workspaceDir := filepath.Join(r.root, runID, variant.ID, evalCase.ID, fmt.Sprintf("attempt-%02d", attempt))
		if err := resetFixtureWorkspace(r.root, workspaceDir); err != nil {
			results = append(results, failedCase(evalCase.ID, variant.ID, attempt, err))
			break
		}
		if err := copyFixture(ctx, fixture, workspaceDir); err != nil {
			results = append(results, failedCase(evalCase.ID, variant.ID, attempt, err))
			break
		}
		caseCtx, cancel := context.WithTimeout(ctx, time.Duration(suite.Defaults.TimeoutMS)*time.Millisecond)
		agentResult, runErr := r.agent.Run(caseCtx, AgentRequest{
			RunID: runID, CaseID: evalCase.ID, Variant: variant, Attempt: attempt,
			WorkspaceDir: workspaceDir, Prompt: prompt,
			Timeout:     time.Duration(suite.Defaults.TimeoutMS) * time.Millisecond,
			TokenBudget: suite.Defaults.TokenBudget, CostBudgetMicros: suite.Defaults.CostBudgetMicros,
			AllowedCommands: append([]string(nil), suite.Defaults.AllowedCommands...),
		})
		grades, passed, score, gradeErr := Grade(caseCtx, GradeInput{
			WorkspaceDir: workspaceDir, BaselineDir: fixture,
			SafetyViolations: agentResult.Usage.SafetyViolations,
			Prompt:           prompt, FinalOutput: agentResult.FinalOutput,
			JudgeConfig: suite.Defaults.Judge, Judge: r.judge,
		}, evalCase.Graders)
		cancel()
		combinedErr := errors.Join(runErr, gradeErr)
		result := CaseResult{
			CaseID: evalCase.ID, VariantID: variant.ID, TaskID: agentResult.TaskID,
			Attempt: attempt, TaskStatus: agentResult.TaskStatus, Passed: passed && runErr == nil,
			Score: score, Grades: grades, Usage: agentResult.Usage,
			StartedAt: startedAt, CompletedAt: time.Now().UTC(),
		}
		result.Usage.Retries = attempt - 1
		for _, grade := range grades {
			if grade.Usage != nil {
				result.Usage.InputTokens += grade.Usage.InputTokens
				result.Usage.OutputTokens += grade.Usage.OutputTokens
				result.Usage.CostMicros += grade.Usage.CostMicros
				result.Usage.DurationMS += grade.Usage.DurationMS
			}
		}
		if combinedErr != nil {
			result.Error = combinedErr.Error()
		}
		results = append(results, result)
		if result.Passed {
			break
		}
	}
	return results
}

func suiteUsesModelGrader(suite Suite) bool {
	for _, evalCase := range suite.Cases {
		for _, grader := range evalCase.Graders {
			if grader.Type == "model" {
				return true
			}
		}
	}
	return false
}

func failedCase(caseID, variantID string, attempt int, err error) CaseResult {
	now := time.Now().UTC()
	return CaseResult{
		CaseID: caseID, VariantID: variantID, Attempt: attempt,
		TaskStatus: "failed", Error: err.Error(), StartedAt: now, CompletedAt: now,
	}
}

func selectVariants(all []Variant, selected []string) ([]Variant, error) {
	if len(selected) == 0 {
		return append([]Variant(nil), all...), nil
	}
	wanted := make(map[string]bool, len(selected))
	for _, variantID := range selected {
		if wanted[variantID] {
			return nil, fmt.Errorf("%w: duplicate selected variant %q", ErrInvalidSuite, variantID)
		}
		wanted[variantID] = true
	}
	variants := make([]Variant, 0, len(selected))
	for _, variant := range all {
		if wanted[variant.ID] {
			variants = append(variants, variant)
			delete(wanted, variant.ID)
		}
	}
	if len(wanted) > 0 {
		return nil, fmt.Errorf("%w: selected variant does not exist", ErrInvalidSuite)
	}
	return variants, nil
}

func suiteDigest(suite Suite, variants []Variant) (string, Reproducibility, error) {
	reproducibility := Reproducibility{
		CoreVersion: plugin.CoreVersion,
		GoVersion:   runtime.Version(),
		GOOS:        runtime.GOOS,
		GOARCH:      runtime.GOARCH,
		Defaults:    suite.Defaults,
		Variants:    append([]Variant(nil), variants...),
		Inputs:      make([]InputDigest, 0, len(suite.Cases)),
	}
	for _, evalCase := range suite.Cases {
		prompt, err := suite.PromptText(evalCase)
		if err != nil {
			return "", Reproducibility{}, err
		}
		fixture, err := suite.FixturePath(evalCase)
		if err != nil {
			return "", Reproducibility{}, err
		}
		fixtureSHA256, err := digestFixture(fixture)
		if err != nil {
			return "", Reproducibility{}, err
		}
		reproducibility.Inputs = append(reproducibility.Inputs, InputDigest{
			CaseID:        evalCase.ID,
			PromptSHA256:  digestBytes([]byte(prompt)),
			FixtureSHA256: fixtureSHA256,
		})
	}
	value := struct {
		SchemaVersion   string          `json:"schema_version"`
		SuiteID         string          `json:"suite_id"`
		SuiteVersion    string          `json:"suite_version"`
		Cases           []Case          `json:"cases"`
		Reproducibility Reproducibility `json:"reproducibility"`
	}{suite.SchemaVersion, suite.ID, suite.Version, suite.Cases, reproducibility}
	data, err := json.Marshal(value)
	if err != nil {
		return "", Reproducibility{}, fmt.Errorf("evaluation: encoding run config: %w", err)
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), reproducibility, nil
}

func digestFixture(root string) (string, error) {
	type entryDigest struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	}
	entries := make([]entryDigest, 0)
	var total int64
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		if relative == "." || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("evaluation: fixture contains non-regular file %q", relative)
		}
		if len(entries) >= 10_000 {
			return errors.New("evaluation: fixture digest file limit exceeded")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		if total > 512<<20 {
			return errors.New("evaluation: fixture digest byte limit exceeded")
		}
		file, err := os.Open(name)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return err
		}
		entries = append(entries, entryDigest{
			Path: filepath.ToSlash(relative), SHA256: "sha256:" + hex.EncodeToString(hash.Sum(nil)),
		})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	data, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	return digestBytes(data), nil
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
