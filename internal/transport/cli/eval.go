package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/userInner/kern/internal/configuration"
	"github.com/userInner/kern/internal/evalrunner"
	"github.com/userInner/kern/internal/evaluation"
	"github.com/userInner/kern/internal/id"
	"github.com/userInner/kern/internal/plugin"
	"github.com/userInner/kern/internal/store/sqlite"
)

type pluginSourceFlag map[string]string

func (values *pluginSourceFlag) String() string {
	if values == nil {
		return ""
	}
	parts := make([]string, 0, len(*values))
	for pluginID, source := range *values {
		parts = append(parts, pluginID+"="+source)
	}
	return strings.Join(parts, ",")
}

func (values *pluginSourceFlag) Set(value string) error {
	pluginID, source, ok := strings.Cut(value, "=")
	pluginID = strings.TrimSpace(pluginID)
	source = strings.TrimSpace(source)
	if !ok || !plugin.ValidID(pluginID) || source == "" {
		return errors.New("plugin source must be plugin.id=/local/directory")
	}
	if *values == nil {
		*values = make(map[string]string)
	}
	if _, duplicate := (*values)[pluginID]; duplicate {
		return fmt.Errorf("duplicate plugin source for %s", pluginID)
	}
	(*values)[pluginID] = source
	return nil
}

func runEval(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	logger *slog.Logger,
	defaults configuration.File,
) error {
	if len(args) == 0 {
		return errors.New("eval requires validate, run, compare, or show")
	}
	switch args[0] {
	case "validate":
		return validateEvalSuite(args[1:], stdout, stderr)
	case "run", "compare":
		return runEvalSuite(ctx, args[0], args[1:], stdout, stderr, logger, defaults)
	case "show":
		return showEval(args[1:], stdout, stderr, defaults)
	default:
		return fmt.Errorf("unknown eval command %q", args[0])
	}
}

type evalValidationReport struct {
	Valid           bool                       `json:"valid"`
	SuiteID         string                     `json:"suite_id"`
	SuiteName       string                     `json:"suite_name"`
	SuiteVersion    string                     `json:"suite_version"`
	CaseCount       int                        `json:"case_count"`
	VariantCount    int                        `json:"variant_count"`
	ConfigDigest    string                     `json:"config_digest"`
	Reproducibility evaluation.Reproducibility `json:"reproducibility"`
}

func validateEvalSuite(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("eval validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	output := flags.String("output", "text", "output format: text or json")
	variantList := flags.String("variants", "", "comma-separated variant IDs; defaults to every suite variant")
	var pluginSources pluginSourceFlag
	flags.Var(&pluginSources, "plugin-source", "local plugin package as plugin.id=/directory (repeatable)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("eval validate requires exactly one suite file or directory")
	}
	suite, err := evaluation.Load(flags.Arg(0))
	if err != nil {
		return err
	}
	selected := splitCommaList(*variantList)
	if pluginSources == nil {
		pluginSources = make(pluginSourceFlag)
	}
	if err := resolveBundledPluginSources(suite, selected, pluginSources); err != nil {
		return err
	}
	if err := evalrunner.EnrichSuite(&suite, selected, pluginSources); err != nil {
		return err
	}
	digest, reproducibility, err := evaluation.PrepareRun(suite, selected)
	if err != nil {
		return err
	}
	variantCount := len(selected)
	if variantCount == 0 {
		variantCount = len(suite.Variants)
	}
	report := evalValidationReport{
		Valid:           true,
		SuiteID:         suite.ID,
		SuiteName:       suite.Name,
		SuiteVersion:    suite.Version,
		CaseCount:       len(suite.Cases),
		VariantCount:    variantCount,
		ConfigDigest:    digest,
		Reproducibility: reproducibility,
	}
	switch *output {
	case "json":
		return json.NewEncoder(stdout).Encode(report)
	case "text":
		_, err := fmt.Fprintf(
			stdout,
			"valid · %s@%s · %d cases · %d variants · %s\n",
			report.SuiteID,
			report.SuiteVersion,
			report.CaseCount,
			report.VariantCount,
			report.ConfigDigest,
		)
		return err
	default:
		return fmt.Errorf("unsupported output format %q", *output)
	}
}

func runEvalSuite(
	ctx context.Context,
	action string,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	logger *slog.Logger,
	defaults configuration.File,
) error {
	flags := flag.NewFlagSet("eval "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", defaults.Storage.DataDir, "local Kern data directory")
	output := flags.String("output", "text", "output format: text or json")
	variantList := flags.String("variants", "", "comma-separated variant IDs; defaults to every suite variant")
	codexBaseline := flags.Bool("codex", false, "include an isolated local Codex CLI baseline")
	codexExecutable := flags.String("codex-bin", "codex", "Codex executable used with --codex")
	codexModel := flags.String("codex-model", "", "optional exact model for the Codex baseline")
	var pluginSources pluginSourceFlag
	flags.Var(&pluginSources, "plugin-source", "local plugin package as plugin.id=/directory (repeatable)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("eval %s requires exactly one suite file or directory", action)
	}
	suite, err := evaluation.Load(flags.Arg(0))
	if err != nil {
		return err
	}
	selected := splitCommaList(*variantList)
	hasExplicitVariants := len(selected) > 0
	if *codexModel != "" && !*codexBaseline {
		return errors.New("--codex-model requires --codex")
	}
	if *codexBaseline {
		for _, variant := range suite.Variants {
			if variant.ID == "external.codex" {
				return errors.New("--codex cannot add duplicate variant external.codex")
			}
		}
		suite.Variants = append(suite.Variants, evaluation.Variant{
			ID: "external.codex", Agent: "codex", Model: strings.TrimSpace(*codexModel), Plugins: []string{},
		})
		if hasExplicitVariants {
			selected = append(selected, "external.codex")
		}
	}
	if err := evaluation.Validate(suite); err != nil {
		return err
	}
	effectiveVariantCount := len(selected)
	if effectiveVariantCount == 0 {
		effectiveVariantCount = len(suite.Variants)
	}
	if action == "compare" && effectiveVariantCount < 2 {
		return errors.New("eval compare requires at least two variants")
	}
	if pluginSources == nil {
		pluginSources = make(pluginSourceFlag)
	}
	if err := resolveBundledPluginSources(suite, selected, pluginSources); err != nil {
		return err
	}
	if err := evalrunner.EnrichSuite(&suite, selected, pluginSources); err != nil {
		return err
	}
	kernAgent, err := evalrunner.NewKernAgent(evalrunner.Config{
		DataRoot:      filepath.Join(*dataDir, "evals", "runtime"),
		PluginSources: pluginSources,
		MaxTurns:      defaults.Runtime.MaxTurns,
		MaxToolCalls:  defaults.Runtime.MaxToolCalls,
		Logger:        logger,
	})
	if err != nil {
		return err
	}
	var runAgent evaluation.Agent = kernAgent
	if suiteUsesAgent(suite, selected, "codex") {
		codexAgent, err := evalrunner.NewCodexAgent(evalrunner.CodexConfig{Executable: *codexExecutable})
		if err != nil {
			return err
		}
		versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		version, err := codexAgent.Version(versionCtx)
		cancel()
		if err != nil {
			return err
		}
		for index := range suite.Variants {
			if suite.Variants[index].Agent == "codex" {
				suite.Variants[index].AgentVersion = version
			}
		}
		if err := evaluation.Validate(suite); err != nil {
			return err
		}
		runAgent = evalrunner.Router{Kern: kernAgent, Codex: codexAgent}
	}
	var judge evaluation.ModelJudge
	if suite.Defaults.Judge != nil {
		judge, err = evalrunner.NewCompatibleJudge(*suite.Defaults.Judge)
		if err != nil {
			return err
		}
	}
	runner, err := evaluation.NewRunnerWithJudge(filepath.Join(*dataDir, "evals", "workspaces"), runAgent, judge, logger)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		return fmt.Errorf("creating evaluation data directory: %w", err)
	}
	store, err := sqlite.Open(ctx, filepath.Join(*dataDir, "kern.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	runID, err := id.New()
	if err != nil {
		return err
	}
	effectiveVariants := selected
	if len(effectiveVariants) == 0 {
		effectiveVariants = make([]string, 0, len(suite.Variants))
		for _, variant := range suite.Variants {
			effectiveVariants = append(effectiveVariants, variant.ID)
		}
	}
	now := time.Now().UTC()
	configDigest, _, err := evaluation.PrepareRunContext(ctx, suite, selected)
	if err != nil {
		return err
	}
	run := evaluation.Run{
		SchemaVersion: evaluation.SchemaVersion,
		ID:            runID, SuiteID: suite.ID, SuiteName: suite.Name, SuiteVersion: suite.Version,
		Status: evaluation.StatusQueued, Variants: effectiveVariants, ConfigDigest: configDigest,
		CaseCount: len(suite.Cases) * len(effectiveVariants), CreatedAt: now,
	}
	absoluteSuitePath, err := filepath.Abs(flags.Arg(0))
	if err != nil {
		return fmt.Errorf("resolving evaluation suite path: %w", err)
	}
	if err := store.UpsertEvalSuite(ctx, suite); err != nil {
		return err
	}
	if err := store.CreateEvalRun(ctx, run, evaluation.RunConfig{SuitePath: absoluteSuitePath, Variants: effectiveVariants}); err != nil {
		return err
	}
	if err := store.StartEvalRun(ctx, run.ID, now); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "Kern Eval: %s@%s · %d cases · %d variants\n", suite.ID, suite.Version, len(suite.Cases), len(suite.Variants))
	report, err := runner.RunWithIDProgress(ctx, run.ID, suite, selected, func(
		progressCtx context.Context,
		progress evaluation.Progress,
	) error {
		if err := store.RecordEvalProgress(
			progressCtx,
			progress.RunID,
			progress.CompletedCases,
			progress.Results,
		); err != nil {
			return err
		}
		_, err := fmt.Fprintf(stderr, "Eval progress: %d/%d · %s/%s\n",
			progress.CompletedCases, progress.TotalCases, progress.VariantID, progress.CaseID)
		return err
	})
	if err != nil {
		status := evaluation.StatusFailed
		if ctx.Err() != nil {
			status = evaluation.StatusCancelled
		}
		_ = store.FinishEvalRun(context.WithoutCancel(ctx), run.ID, status, err.Error(), time.Now().UTC())
		return err
	}
	if err := store.CompleteEvalRun(ctx, report); err != nil {
		return err
	}
	reportPath := filepath.Join(*dataDir, "evals", "reports", report.RunID+".json")
	if err := evaluation.WriteReport(reportPath, report); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "Eval report: %s\n", reportPath)
	return writeEvalReport(stdout, *output, report)
}

func suiteUsesAgent(suite evaluation.Suite, selected []string, agent string) bool {
	wanted := make(map[string]bool, len(selected))
	for _, variantID := range selected {
		wanted[variantID] = true
	}
	for _, variant := range suite.Variants {
		if len(wanted) > 0 && !wanted[variant.ID] {
			continue
		}
		if variant.Agent == agent {
			return true
		}
	}
	return false
}

func showEval(args []string, stdout, stderr io.Writer, defaults configuration.File) error {
	flags := flag.NewFlagSet("eval show", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", defaults.Storage.DataDir, "local Kern data directory")
	output := flags.String("output", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("eval show requires one run ID or report path")
	}
	name := flags.Arg(0)
	if !strings.ContainsRune(name, filepath.Separator) && filepath.Ext(name) == "" {
		name = filepath.Join(*dataDir, "evals", "reports", name+".json")
	}
	report, err := evaluation.ReadReport(name)
	if err != nil {
		return err
	}
	return writeEvalReport(stdout, *output, report)
}

func writeEvalReport(output io.Writer, format string, report evaluation.Report) error {
	switch format {
	case "json":
		return json.NewEncoder(output).Encode(report)
	case "text":
		if _, err := fmt.Fprintf(output, "%s@%s · run %s\n", report.SuiteID, report.SuiteVersion, report.RunID); err != nil {
			return err
		}
		for _, metrics := range report.Variants {
			if _, err := fmt.Fprintf(
				output,
				"%-20s %d/%d passed (%5.1f%%) · score %.3f · tokens %d · cost $%.4f · %d ms\n",
				metrics.VariantID,
				metrics.Passed,
				metrics.Cases,
				metrics.SuccessRate*100,
				metrics.MeanScore,
				metrics.TotalInputTokens+metrics.TotalOutputTokens,
				float64(metrics.TotalCostMicros)/1_000_000,
				metrics.TotalDurationMS,
			); err != nil {
				return err
			}
		}
		for _, comparison := range report.Comparisons {
			if _, err := fmt.Fprintf(
				output,
				"%s → %s · success %+0.1fpp · score %+.3f · improved %d · regressed %d · safety_regressed=%t\n",
				comparison.BaselineVariant,
				comparison.CandidateVariant,
				comparison.SuccessRateDelta*100,
				comparison.MeanScoreDelta,
				len(comparison.Improvements),
				len(comparison.Regressions),
				comparison.SafetyRegressed,
			); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported output format %q", format)
	}
}

func splitCommaList(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func resolveBundledPluginSources(suite evaluation.Suite, selected []string, sources map[string]string) error {
	selectedSet := make(map[string]bool, len(selected))
	for _, variantID := range selected {
		selectedSet[variantID] = true
	}
	workingDirectory, _ := os.Getwd()
	candidates := []string{
		filepath.Join(workingDirectory, "plugins", "go-expert"),
		filepath.Join(suite.Root, "plugins", "go-expert"),
		filepath.Join(suite.Root, "..", "..", "plugins", "go-expert"),
	}
	for _, variant := range suite.Variants {
		if len(selectedSet) > 0 && !selectedSet[variant.ID] {
			continue
		}
		for _, reference := range variant.Plugins {
			pluginID, _, _ := strings.Cut(reference, "@")
			if sources[pluginID] != "" {
				continue
			}
			for _, candidate := range candidates {
				manifest, _, err := plugin.LoadManifest(candidate)
				if err == nil && manifest.ID == pluginID {
					sources[pluginID] = candidate
					break
				}
			}
			if sources[pluginID] == "" {
				return fmt.Errorf("eval: plugin %s requires --plugin-source %s=/local/directory", reference, pluginID)
			}
		}
	}
	return nil
}
