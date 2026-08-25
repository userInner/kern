package evaluation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const maxReportBytes = 64 << 20

// Status is the durable state of an evaluation run.
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusPaused    Status = "paused"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// GradeStatus is one deterministic grader conclusion.
type GradeStatus string

const (
	GradePassed         GradeStatus = "passed"
	GradeFailed         GradeStatus = "failed"
	GradeError          GradeStatus = "error"
	GradeManualRequired GradeStatus = "manual_required"
)

// GradeResult stores a normalized deterministic score and evidence.
type GradeResult struct {
	GraderID   string         `json:"grader_id"`
	Status     GradeStatus    `json:"status"`
	Score      float64        `json:"score"`
	Evidence   []string       `json:"evidence"`
	ReasonCode string         `json:"reason_code"`
	Details    map[string]any `json:"details,omitempty"`
	Usage      *UsageMetrics  `json:"usage,omitempty"`
}

// UsageMetrics are comparable resource and intervention measurements.
type UsageMetrics struct {
	InputTokens        int   `json:"input_tokens"`
	OutputTokens       int   `json:"output_tokens"`
	CostMicros         int64 `json:"cost_micros"`
	DurationMS         int64 `json:"duration_ms"`
	ToolCalls          int   `json:"tool_calls"`
	Retries            int   `json:"retries"`
	HumanInterventions int   `json:"human_interventions"`
	SafetyViolations   int   `json:"safety_violations"`
}

// CaseResult is one independent case/variant execution.
type CaseResult struct {
	CaseID      string        `json:"case_id"`
	VariantID   string        `json:"variant_id"`
	TaskID      string        `json:"task_id,omitempty"`
	Attempt     int           `json:"attempt"`
	TaskStatus  string        `json:"task_status"`
	Passed      bool          `json:"passed"`
	Score       float64       `json:"score"`
	Grades      []GradeResult `json:"grades"`
	Usage       UsageMetrics  `json:"usage"`
	Error       string        `json:"error,omitempty"`
	StartedAt   time.Time     `json:"started_at"`
	CompletedAt time.Time     `json:"completed_at"`
}

// ConfidenceInterval is a Wilson 95% interval for a success proportion.
type ConfidenceInterval struct {
	Low  float64 `json:"low"`
	High float64 `json:"high"`
}

// VariantMetrics summarize one fixed variant across first attempts.
type VariantMetrics struct {
	VariantID          string             `json:"variant_id"`
	Cases              int                `json:"cases"`
	Passed             int                `json:"passed"`
	SuccessRate        float64            `json:"success_rate"`
	Confidence95       ConfidenceInterval `json:"confidence_95"`
	MeanScore          float64            `json:"mean_score"`
	TotalInputTokens   int                `json:"total_input_tokens"`
	TotalOutputTokens  int                `json:"total_output_tokens"`
	TotalCostMicros    int64              `json:"total_cost_micros"`
	TotalDurationMS    int64              `json:"total_duration_ms"`
	ToolCalls          int                `json:"tool_calls"`
	Retries            int                `json:"retries"`
	HumanInterventions int                `json:"human_interventions"`
	SafetyViolations   int                `json:"safety_violations"`
}

// Comparison reports improvements and regressions between two variants.
type Comparison struct {
	BaselineVariant                   string   `json:"baseline_variant"`
	CandidateVariant                  string   `json:"candidate_variant"`
	PairedCases                       int      `json:"paired_cases"`
	SuccessRateDelta                  float64  `json:"success_rate_delta"`
	MeanScoreDelta                    float64  `json:"mean_score_delta"`
	SuccessRateTest                   string   `json:"success_rate_test"`
	SuccessRatePValue                 float64  `json:"success_rate_p_value"`
	SignificanceAlpha                 float64  `json:"significance_alpha"`
	SuccessRateImprovementSignificant bool     `json:"success_rate_improvement_significant"`
	Improvements                      []string `json:"improvements"`
	Regressions                       []string `json:"regressions"`
	SafetyRegressed                   bool     `json:"safety_regressed"`
}

// InputDigest identifies the exact prompt and initial fixture bytes for one
// case without embedding potentially large or sensitive fixture contents.
type InputDigest struct {
	CaseID        string `json:"case_id"`
	PromptSHA256  string `json:"prompt_sha256"`
	FixtureSHA256 string `json:"fixture_sha256"`
}

// Reproducibility records the execution identity covered by ConfigDigest.
type Reproducibility struct {
	CoreVersion string        `json:"core_version"`
	GoVersion   string        `json:"go_version"`
	GOOS        string        `json:"goos"`
	GOARCH      string        `json:"goarch"`
	Defaults    Defaults      `json:"defaults"`
	Variants    []Variant     `json:"variants"`
	Inputs      []InputDigest `json:"inputs"`
}

// Report is a reproducible evaluation outcome.
type Report struct {
	SchemaVersion   string           `json:"schema_version"`
	RunID           string           `json:"run_id"`
	SuiteID         string           `json:"suite_id"`
	SuiteVersion    string           `json:"suite_version"`
	Status          Status           `json:"status"`
	ConfigDigest    string           `json:"config_digest"`
	Variants        []VariantMetrics `json:"variants"`
	Results         []CaseResult     `json:"results"`
	Comparisons     []Comparison     `json:"comparisons,omitempty"`
	Reproducibility Reproducibility  `json:"reproducibility"`
	StartedAt       time.Time        `json:"started_at"`
	CompletedAt     time.Time        `json:"completed_at"`
}

// BuildReport deterministically aggregates first-attempt outcomes and retains
// all attempts in the detail section.
func BuildReport(runID, configDigest string, suite Suite, results []CaseResult, startedAt, completedAt time.Time) Report {
	return BuildReportWithReproducibility(
		runID,
		configDigest,
		suite,
		results,
		Reproducibility{Defaults: suite.Defaults, Variants: append([]Variant(nil), suite.Variants...)},
		startedAt,
		completedAt,
	)
}

// BuildReportWithReproducibility aggregates outcomes and attaches the exact
// environment and input digests used by Runner.
func BuildReportWithReproducibility(
	runID, configDigest string,
	suite Suite,
	results []CaseResult,
	reproducibility Reproducibility,
	startedAt, completedAt time.Time,
) Report {
	first := make(map[string]CaseResult)
	for _, result := range results {
		key := result.VariantID + "\x00" + result.CaseID
		current, ok := first[key]
		if !ok || result.Attempt < current.Attempt {
			first[key] = result
		}
	}
	metrics := make([]VariantMetrics, 0, len(suite.Variants))
	for _, variant := range suite.Variants {
		metric := VariantMetrics{VariantID: variant.ID}
		for _, evalCase := range suite.Cases {
			result, ok := first[variant.ID+"\x00"+evalCase.ID]
			if !ok {
				continue
			}
			metric.Cases++
			if result.Passed {
				metric.Passed++
			}
			metric.MeanScore += result.Score
			metric.TotalInputTokens += result.Usage.InputTokens
			metric.TotalOutputTokens += result.Usage.OutputTokens
			metric.TotalCostMicros += result.Usage.CostMicros
			metric.TotalDurationMS += result.Usage.DurationMS
			metric.ToolCalls += result.Usage.ToolCalls
			metric.Retries += result.Usage.Retries
			metric.HumanInterventions += result.Usage.HumanInterventions
			metric.SafetyViolations += result.Usage.SafetyViolations
		}
		if metric.Cases > 0 {
			metric.SuccessRate = float64(metric.Passed) / float64(metric.Cases)
			metric.MeanScore /= float64(metric.Cases)
			metric.Confidence95 = wilson95(metric.Passed, metric.Cases)
		}
		metrics = append(metrics, metric)
	}
	comparisons := make([]Comparison, 0)
	if len(metrics) > 1 {
		baseline := metrics[0]
		for _, candidate := range metrics[1:] {
			comparison := Comparison{
				BaselineVariant:   baseline.VariantID,
				CandidateVariant:  candidate.VariantID,
				SuccessRateDelta:  candidate.SuccessRate - baseline.SuccessRate,
				MeanScoreDelta:    candidate.MeanScore - baseline.MeanScore,
				SuccessRateTest:   "mcnemar_exact_two_sided",
				SignificanceAlpha: 0.05,
				Improvements:      []string{},
				Regressions:       []string{},
				SafetyRegressed:   candidate.SafetyViolations > baseline.SafetyViolations,
			}
			for _, evalCase := range suite.Cases {
				base, baseOK := first[baseline.VariantID+"\x00"+evalCase.ID]
				other, otherOK := first[candidate.VariantID+"\x00"+evalCase.ID]
				if !baseOK || !otherOK {
					continue
				}
				comparison.PairedCases++
				if !base.Passed && other.Passed {
					comparison.Improvements = append(comparison.Improvements, evalCase.ID)
				}
				if base.Passed && !other.Passed {
					comparison.Regressions = append(comparison.Regressions, evalCase.ID)
				}
			}
			comparison.SuccessRatePValue = exactMcNemarPValue(
				len(comparison.Improvements),
				len(comparison.Regressions),
			)
			comparison.SuccessRateImprovementSignificant =
				len(comparison.Improvements) > len(comparison.Regressions) &&
					comparison.SuccessRatePValue < comparison.SignificanceAlpha
			sort.Strings(comparison.Improvements)
			sort.Strings(comparison.Regressions)
			comparisons = append(comparisons, comparison)
		}
	}
	return Report{
		SchemaVersion:   SchemaVersion,
		RunID:           runID,
		SuiteID:         suite.ID,
		SuiteVersion:    suite.Version,
		Status:          StatusCompleted,
		ConfigDigest:    configDigest,
		Variants:        metrics,
		Results:         results,
		Comparisons:     comparisons,
		Reproducibility: reproducibility,
		StartedAt:       startedAt,
		CompletedAt:     completedAt,
	}
}

// exactMcNemarPValue returns the exact two-sided p-value for paired binary
// outcomes. Only discordant pairs carry evidence about a variant difference;
// under the null, either variant is equally likely to win each such pair.
func exactMcNemarPValue(improvements, regressions int) float64 {
	discordant := improvements + regressions
	if discordant == 0 {
		return 1
	}
	smaller := min(improvements, regressions)
	term := math.Ldexp(1, -discordant)
	cumulative := term
	for successes := 0; successes < smaller; successes++ {
		term *= float64(discordant-successes) / float64(successes+1)
		cumulative += term
	}
	return min(1, 2*cumulative)
}

// WriteReport atomically stores a report with private permissions.
func WriteReport(name string, report Report) error {
	if report.SchemaVersion != SchemaVersion || report.RunID == "" || report.SuiteID == "" {
		return errors.New("evaluation: invalid report identity")
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("evaluation: encoding report: %w", err)
	}
	data = append(data, '\n')
	if len(data) > maxReportBytes {
		return errors.New("evaluation: report exceeds 64 MiB")
	}
	directory := filepath.Dir(name)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("evaluation: creating report directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".report-*")
	if err != nil {
		return fmt.Errorf("evaluation: creating report: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	_, writeErr := temporary.Write(data)
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("evaluation: writing report: %w", err)
	}
	if err := os.Rename(temporaryName, name); err != nil {
		return fmt.Errorf("evaluation: publishing report: %w", err)
	}
	return nil
}

// ReadReport strictly loads one previously written report.
func ReadReport(name string) (Report, error) {
	file, err := os.Open(name)
	if err != nil {
		return Report{}, fmt.Errorf("evaluation: opening report: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxReportBytes+1))
	if err != nil {
		return Report{}, fmt.Errorf("evaluation: reading report: %w", err)
	}
	if len(data) > maxReportBytes {
		return Report{}, errors.New("evaluation: report exceeds 64 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var report Report
	if err := decoder.Decode(&report); err != nil {
		return Report{}, fmt.Errorf("evaluation: decoding report: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF || report.SchemaVersion != SchemaVersion ||
		report.RunID == "" || report.SuiteID == "" {
		return Report{}, errors.New("evaluation: invalid report")
	}
	return report, nil
}

func wilson95(successes, total int) ConfidenceInterval {
	if total <= 0 {
		return ConfidenceInterval{}
	}
	z := 1.959963984540054
	n := float64(total)
	p := float64(successes) / n
	denominator := 1 + z*z/n
	center := (p + z*z/(2*n)) / denominator
	margin := z * math.Sqrt((p*(1-p)+z*z/(4*n))/n) / denominator
	return ConfidenceInterval{Low: math.Max(0, center-margin), High: math.Min(1, center+margin)}
}
