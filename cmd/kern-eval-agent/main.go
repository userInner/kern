// Command kern-eval-agent exposes Kern Core to container-native evaluation
// harnesses. It is intentionally separate from the user-facing kern CLI
// because benchmark containers require unattended write and process approval.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/userInner/kern/internal/evalrunner"
	"github.com/userInner/kern/internal/evaluation"
)

const maxInstructionBytes = 1 << 20

var containerAllowedCommands = []string{"*"}

type response struct {
	Result evaluation.AgentResult `json:"result"`
	Error  string                 `json:"error,omitempty"`
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "kern-eval-agent: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("kern-eval-agent", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", "/tmp/kern-eval-agent", "isolated runtime data directory")
	workspaceDir := flags.String("workspace", "", "benchmark workspace directory")
	runID := flags.String("run-id", "harbor-run", "evaluation run identity")
	caseID := flags.String("case-id", "harbor-case", "evaluation case identity")
	model := flags.String("model", strings.TrimSpace(os.Getenv("KERN_MODEL")), "model identity recorded for the run")
	timeout := flags.Duration("timeout", 29*time.Minute, "maximum agent runtime")
	tokenBudget := flags.Int("token-budget", 300_000, "maximum model tokens")
	costBudgetMicros := flags.Int64("cost-budget-micros", 0, "maximum cost in millionths of a dollar; zero disables the limit")
	maxTurns := flags.Int("max-turns", 64, "maximum model turns")
	maxToolCalls := flags.Int("max-tool-calls", 256, "maximum tool calls")
	containerMarker := flags.String("container-marker", "/.dockerenv", "file proving execution is inside an isolated container")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments; provide the instruction on stdin")
	}
	if *timeout <= 0 || *tokenBudget < 1 || *maxTurns < 1 || *maxToolCalls < 1 || *costBudgetMicros < 0 {
		return errors.New("timeout and budgets must be positive")
	}
	if _, err := os.Stat(*containerMarker); err != nil {
		return fmt.Errorf("refusing unattended approvals outside an isolated container: %w", err)
	}
	workspace := strings.TrimSpace(*workspaceDir)
	if workspace == "" {
		current, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolving workspace: %w", err)
		}
		workspace = current
	}
	absoluteWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return fmt.Errorf("resolving workspace: %w", err)
	}
	instruction, err := readInstruction(stdin)
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	agent, err := evalrunner.NewKernAgent(evalrunner.Config{
		DataRoot:     *dataDir,
		MaxTurns:     *maxTurns,
		MaxToolCalls: *maxToolCalls,
		Logger:       logger,
	})
	if err != nil {
		return err
	}
	result, runErr := agent.Run(ctx, evaluation.AgentRequest{
		RunID:            strings.TrimSpace(*runID),
		CaseID:           strings.TrimSpace(*caseID),
		Variant:          evaluation.Variant{ID: "harbor.kern", Agent: "kern", Model: strings.TrimSpace(*model), Plugins: []string{}},
		Attempt:          1,
		WorkspaceDir:     absoluteWorkspace,
		Prompt:           instruction,
		Timeout:          *timeout,
		TokenBudget:      *tokenBudget,
		CostBudgetMicros: *costBudgetMicros,
		AllowedCommands:  append([]string(nil), containerAllowedCommands...),
	})
	payload := response{Result: result}
	if runErr != nil {
		payload.Error = runErr.Error()
	}
	if err := json.NewEncoder(stdout).Encode(payload); err != nil {
		return fmt.Errorf("encoding result: %w", err)
	}
	return runErr
}

func readInstruction(input io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(input, maxInstructionBytes+1))
	if err != nil {
		return "", fmt.Errorf("reading instruction: %w", err)
	}
	if len(data) > maxInstructionBytes {
		return "", errors.New("instruction exceeds 1 MiB")
	}
	instruction := strings.TrimSpace(string(data))
	if instruction == "" {
		return "", errors.New("instruction is empty")
	}
	return instruction, nil
}
