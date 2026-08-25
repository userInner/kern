package evalrunner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/userInner/kern/internal/evaluation"
)

const (
	maxCodexEventBytes = 16 << 20
	maxCodexStderr     = 1 << 20
)

// CodexConfig records the explicitly selected local Codex executable.
type CodexConfig struct {
	Executable string
}

// CodexAgent adapts `codex exec` as an optional, externally authenticated
// evaluation baseline. It is never selected unless a variant says agent=codex.
type CodexAgent struct {
	executable string
}

// NewCodexAgent resolves a local executable without running a task.
func NewCodexAgent(config CodexConfig) (*CodexAgent, error) {
	executable := strings.TrimSpace(config.Executable)
	if executable == "" {
		executable = "codex"
	}
	resolved, err := exec.LookPath(executable)
	if err != nil {
		return nil, fmt.Errorf("evalrunner: locating Codex CLI: %w", err)
	}
	return &CodexAgent{executable: resolved}, nil
}

// Version returns the exact CLI build recorded in the evaluation suite.
func (a *CodexAgent) Version(ctx context.Context) (string, error) {
	command := exec.CommandContext(ctx, a.executable, "--version")
	command.Env = codexEnvironment()
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("evalrunner: reading Codex version: %w", err)
	}
	version := strings.TrimSpace(string(output))
	if version == "" || len(version) > 200 {
		return "", errors.New("evalrunner: invalid Codex version output")
	}
	return version, nil
}

// Run executes Codex in the isolated case workspace. Approval requests use
// Codex's automatic review inside the workspace-write sandbox so unattended
// evaluation can proceed; sandbox and hook-trust bypass flags are never used.
func (a *CodexAgent) Run(ctx context.Context, request evaluation.AgentRequest) (evaluation.AgentResult, error) {
	startedAt := time.Now()
	outputDir, err := os.MkdirTemp("", "kern-codex-eval-*")
	if err != nil {
		return evaluation.AgentResult{}, fmt.Errorf("evalrunner: creating Codex output directory: %w", err)
	}
	defer os.RemoveAll(outputDir)
	lastMessage := filepath.Join(outputDir, "last-message.txt")
	arguments := []string{
		"exec",
		"--approve-for-me",
		"--skip-git-repo-check",
		"--ephemeral",
		"--ignore-user-config",
		"--ignore-rules",
		"--json",
		"--color", "never",
		"--cd", request.WorkspaceDir,
		"--output-last-message", lastMessage,
	}
	if request.Variant.Model != "" {
		arguments = append(arguments, "--model", request.Variant.Model)
	}
	arguments = append(arguments, "-")
	command := exec.CommandContext(ctx, a.executable, arguments...)
	configureProcessTree(command)
	command.Dir = request.WorkspaceDir
	command.Env = codexEnvironment()
	command.Stdin = strings.NewReader(request.Prompt)
	var stdout, stderr limitedBuffer
	stdout.limit = maxCodexEventBytes
	stderr.limit = maxCodexStderr
	command.Stdout = &stdout
	command.Stderr = &stderr
	err = command.Run()
	result := evaluation.AgentResult{
		TaskStatus: "completed",
		Usage: evaluation.UsageMetrics{
			DurationMS: time.Since(startedAt).Milliseconds(),
		},
	}
	parseCodexEvents(stdout.Bytes(), &result)
	if stdout.exceeded || stderr.exceeded {
		return result, errors.New("evalrunner: Codex output exceeded limit")
	}
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, fmt.Errorf("evalrunner: Codex exited unsuccessfully: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	message, err := os.ReadFile(lastMessage)
	if err != nil || strings.TrimSpace(string(message)) == "" {
		return result, errors.New("evalrunner: Codex produced no final message")
	}
	result.FinalOutput = strings.TrimSpace(string(message))
	if request.TokenBudget > 0 && result.Usage.InputTokens+result.Usage.OutputTokens > request.TokenBudget {
		return result, fmt.Errorf(
			"evalrunner: Codex used %d tokens, exceeding budget %d",
			result.Usage.InputTokens+result.Usage.OutputTokens,
			request.TokenBudget,
		)
	}
	return result, nil
}

// codexEnvironment keeps the operating-system and persisted-login context
// needed by Codex while preventing unrelated caller secrets from reaching the
// CLI or model-generated child processes. Codex authentication must be
// established with `codex login`; ambient API keys are intentionally omitted.
func codexEnvironment() []string {
	// Windows variable lookup is case-insensitive, so retain only one spelling
	// of each name even if the host exposes aliases such as Path and PATH.
	keys := []string{
		"PATH", "PATHEXT", "SystemRoot", "ComSpec", "WINDIR",
		"HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH",
		"APPDATA", "LOCALAPPDATA", "CODEX_HOME",
		"TMPDIR", "TMP", "TEMP",
		"LANG", "LC_ALL", "LC_CTYPE", "TERM", "TZ",
		"SSL_CERT_FILE", "SSL_CERT_DIR",
	}
	environment := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		canonical := strings.ToUpper(key)
		if _, duplicate := seen[canonical]; duplicate {
			continue
		}
		value, ok := os.LookupEnv(key)
		if !ok {
			continue
		}
		seen[canonical] = struct{}{}
		environment = append(environment, key+"="+value)
	}
	return environment
}

type limitedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.exceeded = true
		return original, nil
	}
	if len(data) > remaining {
		data = data[:remaining]
		b.exceeded = true
	}
	_, _ = b.buffer.Write(data)
	return original, nil
}

func (b *limitedBuffer) Bytes() []byte  { return b.buffer.Bytes() }
func (b *limitedBuffer) String() string { return b.buffer.String() }

func parseCodexEvents(data []byte, result *evaluation.AgentResult) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64<<10), maxCodexEventBytes)
	for scanner.Scan() {
		var event map[string]any
		decoder := json.NewDecoder(strings.NewReader(scanner.Text()))
		decoder.UseNumber()
		if decoder.Decode(&event) != nil {
			continue
		}
		if threadID := findString(event, "thread_id"); result.TaskID == "" && threadID != "" {
			result.TaskID = threadID
		}
		input, output := findTokenUsage(event)
		result.Usage.InputTokens = max(result.Usage.InputTokens, input)
		result.Usage.OutputTokens = max(result.Usage.OutputTokens, output)
		eventType, _ := event["type"].(string)
		if strings.HasSuffix(eventType, ".completed") &&
			(strings.Contains(eventType, "tool") || strings.Contains(eventType, "command")) {
			result.Usage.ToolCalls++
		}
	}
}

func findTokenUsage(value any) (int, int) {
	bestInput, bestOutput := 0, 0
	var walk func(any)
	walk = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			input := integerValue(typed["input_tokens"])
			output := integerValue(typed["output_tokens"])
			if input > bestInput {
				bestInput = input
			}
			if output > bestOutput {
				bestOutput = output
			}
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
	return bestInput, bestOutput
}

func findString(value any, key string) string {
	switch typed := value.(type) {
	case map[string]any:
		if found, ok := typed[key].(string); ok {
			return found
		}
		for _, child := range typed {
			if found := findString(child, key); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range typed {
			if found := findString(child, key); found != "" {
				return found
			}
		}
	}
	return ""
}

func integerValue(value any) int {
	number, ok := value.(json.Number)
	if !ok {
		return 0
	}
	integer, err := number.Int64()
	if err != nil || integer < 0 || integer > int64(^uint(0)>>1) {
		return 0
	}
	return int(integer)
}

var _ evaluation.Agent = (*CodexAgent)(nil)

// Keep io imported in generated cross-platform builds where command output
// plumbing may be specialized later.
var _ io.Writer = (*limitedBuffer)(nil)
