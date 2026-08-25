package builtin

import (
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

	"github.com/userInner/kern/internal/model"
	"github.com/userInner/kern/internal/operation"
	"github.com/userInner/kern/internal/tool"
	"github.com/userInner/kern/internal/workspace"
)

const (
	defaultCommandTimeout = 2 * time.Minute
	maxCommandOutput      = 2 << 20
)

var executeSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["argv"],
  "properties": {
    "argv": {"type": "array", "minItems": 1, "maxItems": 64, "items": {"type": "string"}},
    "cwd": {"type": "string", "description": "Workspace-relative directory; defaults to ."},
    "timeout_ms": {"type": "integer", "minimum": 1, "maximum": 120000}
  }
}`)

// Execute runs an argv command without shell expansion in a confined working directory.
type Execute struct {
	workspace *workspace.Workspace
}

// NewExecute constructs the execute tool.
func NewExecute(workspace *workspace.Workspace) (*Execute, error) {
	if workspace == nil {
		return nil, errors.New("tool: workspace is required")
	}
	return &Execute{workspace: workspace}, nil
}

func (e *Execute) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name: "execute",
		Description: "Run a bounded command as an explicit argv array inside the workspace. " +
			"Shell syntax, inherited secrets, and background execution are not supported.",
		InputSchema: executeSchema,
	}
}

func (e *Execute) Effect(input json.RawMessage) (operation.Effect, error) {
	_, err := decodeExecute(input)
	return operation.EffectProcess, err
}

func (e *Execute) Execute(
	ctx context.Context,
	input json.RawMessage,
) (tool.Result, error) {
	request, err := decodeExecute(input)
	if err != nil {
		return tool.Result{}, err
	}
	directory, err := e.workspace.ResolveDir(request.Cwd)
	if err != nil {
		return tool.Result{}, err
	}
	timeout := defaultCommandTimeout
	if request.TimeoutMS > 0 {
		timeout = time.Duration(request.TimeoutMS) * time.Millisecond
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	runtimeDir, err := os.MkdirTemp("", "kern-execute-")
	if err != nil {
		return tool.Result{}, fmt.Errorf("creating isolated command environment: %w", err)
	}
	defer os.RemoveAll(runtimeDir)
	for _, directory := range []string{
		filepath.Join(runtimeDir, "home"),
		filepath.Join(runtimeDir, "tmp"),
		filepath.Join(runtimeDir, "go-build"),
		filepath.Join(runtimeDir, "go-mod"),
		filepath.Join(runtimeDir, "go-path"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return tool.Result{}, fmt.Errorf("preparing isolated command environment: %w", err)
		}
	}
	command := exec.CommandContext(commandCtx, request.Argv[0], request.Argv[1:]...)
	configureProcessTree(command)
	command.Dir = directory
	command.Env = isolatedCommandEnvironment(runtimeDir)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return tool.Result{}, fmt.Errorf("opening command stdout: %w", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return tool.Result{}, fmt.Errorf("opening command stderr: %w", err)
	}
	if err := command.Start(); err != nil {
		return tool.Result{}, fmt.Errorf("starting command: %w", err)
	}
	type streamResult struct {
		name string
		data []byte
		err  error
	}
	streams := make(chan streamResult, 2)
	read := func(name string, reader io.Reader) {
		data, err := io.ReadAll(io.LimitReader(reader, maxCommandOutput+1))
		if len(data) > maxCommandOutput {
			err = workspace.ErrTooLarge
			cancel()
		}
		streams <- streamResult{name: name, data: data, err: err}
	}
	go read("stdout", stdout)
	go read("stderr", stderr)
	var stdoutData, stderrData []byte
	var streamErr error
	for range 2 {
		result := <-streams
		if result.name == "stdout" {
			stdoutData = result.data
		} else {
			stderrData = result.data
		}
		streamErr = errors.Join(streamErr, result.err)
	}
	waitErr := command.Wait()
	exitCode := command.ProcessState.ExitCode()
	content, encodeErr := json.Marshal(map[string]any{
		"argv":      request.Argv,
		"cwd":       request.Cwd,
		"stdout":    string(stdoutData),
		"stderr":    string(stderrData),
		"exit_code": exitCode,
	})
	if encodeErr != nil {
		return tool.Result{}, fmt.Errorf("encoding command result: %w", encodeErr)
	}
	result := tool.Result{Content: string(content), ExitCode: &exitCode}
	if streamErr != nil {
		return result, fmt.Errorf("reading command output: %w", streamErr)
	}
	if waitErr != nil {
		if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
			return result, context.DeadlineExceeded
		}
		return result, fmt.Errorf("command exited unsuccessfully: %w", waitErr)
	}
	return result, nil
}

func isolatedCommandEnvironment(root string) []string {
	environment := []string{
		"PATH=" + os.Getenv("PATH"),
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"HOME=" + filepath.Join(root, "home"),
		"USERPROFILE=" + filepath.Join(root, "home"),
		"TMPDIR=" + filepath.Join(root, "tmp"),
		"TMP=" + filepath.Join(root, "tmp"),
		"TEMP=" + filepath.Join(root, "tmp"),
		"GOCACHE=" + filepath.Join(root, "go-build"),
		"GOMODCACHE=" + filepath.Join(root, "go-mod"),
		"GOPATH=" + filepath.Join(root, "go-path"),
	}
	for _, name := range []string{"SYSTEMROOT", "WINDIR", "PATHEXT"} {
		if value := os.Getenv(name); value != "" {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}

type executeInput struct {
	Argv      []string `json:"argv"`
	Cwd       string   `json:"cwd"`
	TimeoutMS int      `json:"timeout_ms"`
}

func decodeExecute(input json.RawMessage) (executeInput, error) {
	var request executeInput
	if err := decodeStrict(input, &request); err != nil {
		return executeInput{}, err
	}
	if len(request.Argv) == 0 || len(request.Argv) > 64 {
		return executeInput{}, fmt.Errorf("%w: argv must contain 1 to 64 values", tool.ErrInvalidInput)
	}
	for _, argument := range request.Argv {
		if argument == "" || strings.IndexByte(argument, 0) >= 0 {
			return executeInput{}, fmt.Errorf("%w: argv contains an invalid value", tool.ErrInvalidInput)
		}
	}
	if request.Cwd == "" {
		request.Cwd = "."
	}
	if request.TimeoutMS < 0 || request.TimeoutMS > int(defaultCommandTimeout.Milliseconds()) {
		return executeInput{}, fmt.Errorf("%w: timeout is outside the allowed range", tool.ErrInvalidInput)
	}
	return request, nil
}

var _ tool.Handler = (*Execute)(nil)
