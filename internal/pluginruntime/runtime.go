// Package pluginruntime executes plugin capabilities outside the Core process.
package pluginruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/userInner/kern/internal/jsonschema"
	"github.com/userInner/kern/internal/plugin"
)

const (
	maxInputBytes      = 256 << 10
	maxWASMModuleBytes = 16 << 20
	defaultOutputBytes = 64 << 10
	maxStderrBytes     = 64 << 10
	defaultTimeout     = 5 * time.Second
	wasmMemoryBytes    = 64 << 20
	circuitFailures    = 3
	circuitCooldown    = time.Minute

	// WASMABIVersion identifies the stable Core/guest contract described in
	// docs/wasm-abi-v1.md.
	WASMABIVersion = "kern.plugin.abi/v1"
	// WASMMemoryExport is the guest linear-memory export used for request and
	// response transfer.
	WASMMemoryExport = "memory"
	// WASMAllocateExport allocates request bytes in guest memory.
	WASMAllocateExport = "kern_alloc"
	// WASMInvokeExport executes one JSON-RPC request and returns a packed
	// response pointer and length.
	WASMInvokeExport = "kern_invoke"
)

var wasmModuleHeader = []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}

var (
	ErrRuntimeUnavailable  = errors.New("pluginruntime: executable runtime unavailable")
	ErrCircuitOpen         = errors.New("pluginruntime: capability circuit is open")
	ErrProtocol            = errors.New("pluginruntime: invalid JSON-RPC response")
	ErrWASMInvalidModule   = errors.New("pluginruntime: invalid WASM module")
	ErrWASMIncompatibleABI = errors.New("pluginruntime: incompatible WASM ABI")
	ErrWASMLimitExceeded   = errors.New("pluginruntime: WASM limit exceeded")
	ErrWASMTrap            = errors.New("pluginruntime: WASM trap")
)

type breaker struct {
	failures int
	openedAt time.Time
}

// WASMLimits are Core-owned ceilings that every sandbox adapter must enforce.
// The adapter receives module bytes rather than a host path so WASI filesystem,
// environment, network, clock, and random capabilities remain absent by
// default.
type WASMLimits struct {
	Timeout        time.Duration
	MaxMemoryBytes uint64
	MaxOutputBytes int64
}

// WASMInvocation is one self-contained, secret-free sandbox request.
type WASMInvocation struct {
	ABIVersion string
	PluginID   string
	Version    string
	ToolID     string
	Module     []byte
	Request    []byte
	Limits     WASMLimits
}

// PackWASMResult combines a response pointer and length into the i64 returned
// by the v1 kern_invoke guest export.
func PackWASMResult(pointer, length uint32) uint64 {
	return uint64(pointer)<<32 | uint64(length)
}

// UnpackWASMResult separates the v1 kern_invoke result into its response
// pointer and length.
func UnpackWASMResult(result uint64) (pointer, length uint32) {
	return uint32(result >> 32), uint32(result)
}

// WASMSandbox executes a module without granting ambient host capabilities.
// Check performs a bounded, non-mutating readiness probe. Implementations must
// stop on ctx cancellation and enforce every invocation limit.
type WASMSandbox interface {
	Check(ctx context.Context) error
	Invoke(ctx context.Context, invocation WASMInvocation) ([]byte, error)
}

// Config supplies optional isolated execution adapters.
type Config struct {
	WASM WASMSandbox
}

// Runtime owns failure isolation across capability invocations.
type Runtime struct {
	mu       sync.Mutex
	breakers map[string]breaker
	wasm     WASMSandbox
}

// New constructs an isolated plugin runtime host.
func New() *Runtime {
	return NewWithConfig(Config{})
}

// NewWithConfig constructs a runtime with explicit optional sandboxes.
func NewWithConfig(config Config) *Runtime {
	return &Runtime{breakers: make(map[string]breaker), wasm: config.WASM}
}

// Invoke runs one validated capability with a bounded JSON input envelope.
func (r *Runtime) Invoke(
	ctx context.Context,
	installed plugin.Installed,
	spec plugin.ToolSpec,
	input json.RawMessage,
) (json.RawMessage, error) {
	key := installed.ID + "@" + installed.Version + ":" + spec.ID
	if err := r.checkCircuit(key); err != nil {
		return nil, err
	}
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	if len(input) > maxInputBytes || !json.Valid(input) {
		return nil, errors.New("pluginruntime: input is invalid or exceeds 256 KiB")
	}
	if err := plugin.ValidateToolSpec(spec, installed.Manifest.Permissions.Process); err != nil {
		return nil, fmt.Errorf("pluginruntime: invalid capability declaration: %w", err)
	}
	if err := jsonschema.Validate(spec.InputSchema, input); err != nil {
		return nil, fmt.Errorf("pluginruntime: validating input: %w", err)
	}
	if _, err := plugin.VerifyPackage(installed.InstallPath, installed.Manifest); err != nil {
		return nil, fmt.Errorf("pluginruntime: package integrity check failed: %w", err)
	}
	var result json.RawMessage
	var err error
	switch spec.Runtime {
	case "subprocess":
		result, err = invokeSubprocess(ctx, installed, spec, input)
	case "wasm":
		if r.wasm == nil {
			err = ErrRuntimeUnavailable
		} else {
			result, err = invokeWASM(ctx, r.wasm, installed, spec, input)
		}
	default:
		err = ErrRuntimeUnavailable
	}
	if err != nil {
		r.recordFailure(key)
		return nil, err
	}
	if _, err := plugin.VerifyPackage(installed.InstallPath, installed.Manifest); err != nil {
		r.recordFailure(key)
		return nil, fmt.Errorf("pluginruntime: capability modified its installed package: %w", err)
	}
	r.recordSuccess(key)
	return result, nil
}

func invokeSubprocess(
	ctx context.Context,
	installed plugin.Installed,
	spec plugin.ToolSpec,
	input json.RawMessage,
) (json.RawMessage, error) {
	requestData, err := encodeRequest(spec.ID, input)
	if err != nil {
		return nil, err
	}
	timeout := invocationTimeout(spec)
	outputLimit := invocationOutputLimit(spec)
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(commandCtx, spec.Command[0], spec.Command[1:]...)
	configureProcessTree(command)
	command.Dir = installed.InstallPath
	command.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"KERN_PLUGIN_ID=" + installed.ID,
		"KERN_PLUGIN_VERSION=" + installed.Version,
	}
	command.Stdin = bytes.NewReader(append(requestData, '\n'))
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("pluginruntime: opening stdout: %w", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("pluginruntime: opening stderr: %w", err)
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("pluginruntime: starting capability: %w", err)
	}
	type streamResult struct {
		name string
		data []byte
		err  error
	}
	streams := make(chan streamResult, 2)
	read := func(name string, reader io.Reader, limit int64) {
		data, err := io.ReadAll(io.LimitReader(reader, limit+1))
		if int64(len(data)) > limit {
			err = errors.New("pluginruntime: output limit exceeded")
			cancel()
		}
		streams <- streamResult{name: name, data: data, err: err}
	}
	go read("stdout", stdout, outputLimit)
	go read("stderr", stderr, maxStderrBytes)
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
	if streamErr != nil {
		return nil, streamErr
	}
	if waitErr != nil {
		if commandCtx.Err() != nil {
			return nil, commandCtx.Err()
		}
		return nil, fmt.Errorf("pluginruntime: capability exited unsuccessfully: %w: %s", waitErr, boundedText(stderrData))
	}
	return decodeResponse(stdoutData)
}

func encodeRequest(toolID string, input json.RawMessage) ([]byte, error) {
	requestData, err := json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		ID      string `json:"id"`
		Method  string `json:"method"`
		Params  struct {
			SchemaVersion string          `json:"schema_version"`
			Tool          string          `json:"tool"`
			Input         json.RawMessage `json:"input"`
		} `json:"params"`
	}{
		JSONRPC: "2.0",
		ID:      "1",
		Method:  "invoke",
		Params: struct {
			SchemaVersion string          `json:"schema_version"`
			Tool          string          `json:"tool"`
			Input         json.RawMessage `json:"input"`
		}{SchemaVersion: plugin.SchemaVersion, Tool: toolID, Input: input},
	})
	if err != nil {
		return nil, fmt.Errorf("pluginruntime: encoding request: %w", err)
	}
	return requestData, nil
}

func invocationTimeout(spec plugin.ToolSpec) time.Duration {
	timeout := defaultTimeout
	if spec.TimeoutMS > 0 {
		timeout = time.Duration(spec.TimeoutMS) * time.Millisecond
	}
	return timeout
}

func invocationOutputLimit(spec plugin.ToolSpec) int64 {
	outputLimit := int64(defaultOutputBytes)
	if spec.MaxOutputBytes > 0 {
		outputLimit = int64(spec.MaxOutputBytes)
	}
	return outputLimit
}

func invokeWASM(
	ctx context.Context,
	sandbox WASMSandbox,
	installed plugin.Installed,
	spec plugin.ToolSpec,
	input json.RawMessage,
) (json.RawMessage, error) {
	requestData, err := encodeRequest(spec.ID, input)
	if err != nil {
		return nil, err
	}
	module, err := readWASMModule(installed.InstallPath, spec.Module)
	if err != nil {
		return nil, err
	}
	timeout := invocationTimeout(spec)
	outputLimit := invocationOutputLimit(spec)
	invocationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	encoded, err := sandbox.Invoke(invocationCtx, WASMInvocation{
		ABIVersion: WASMABIVersion,
		PluginID:   installed.ID,
		Version:    installed.Version,
		ToolID:     spec.ID,
		Module:     module,
		Request:    requestData,
		Limits: WASMLimits{
			Timeout: timeout, MaxMemoryBytes: wasmMemoryBytes,
			MaxOutputBytes: outputLimit,
		},
	})
	if err != nil {
		if invocationCtx.Err() != nil {
			return nil, invocationCtx.Err()
		}
		return nil, err
	}
	if int64(len(encoded)) > outputLimit {
		return nil, fmt.Errorf("%w: output limit exceeded", ErrWASMLimitExceeded)
	}
	return decodeResponse(encoded)
}

func readWASMModule(directory, modulePath string) ([]byte, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, fmt.Errorf("pluginruntime: opening package root: %w", err)
	}
	defer root.Close()
	module, err := root.Open(modulePath)
	if err != nil {
		return nil, fmt.Errorf("pluginruntime: opening WASM module: %w", err)
	}
	defer module.Close()
	data, err := io.ReadAll(io.LimitReader(module, maxWASMModuleBytes+1))
	if err != nil {
		return nil, fmt.Errorf("pluginruntime: reading WASM module: %w", err)
	}
	if len(data) > maxWASMModuleBytes {
		return nil, fmt.Errorf("%w: module exceeds 16 MiB", ErrWASMLimitExceeded)
	}
	if len(data) < len(wasmModuleHeader) || !bytes.Equal(data[:len(wasmModuleHeader)], wasmModuleHeader) {
		return nil, fmt.Errorf("%w: invalid header or version", ErrWASMInvalidModule)
	}
	return data, nil
}

func decodeResponse(data []byte) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var response struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      string          `json:"id"`
		Result  json.RawMessage `json:"result,omitempty"`
		Error   *struct {
			Code    int             `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data,omitempty"`
		} `json:"error,omitempty"`
	}
	if err := decoder.Decode(&response); err != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		response.JSONRPC != "2.0" || response.ID != "1" || (len(response.Result) == 0) == (response.Error == nil) {
		return nil, ErrProtocol
	}
	if response.Error != nil {
		if strings.TrimSpace(response.Error.Message) == "" {
			return nil, ErrProtocol
		}
		return nil, fmt.Errorf("pluginruntime: capability error %d: %s", response.Error.Code, response.Error.Message)
	}
	if !json.Valid(response.Result) {
		return nil, ErrProtocol
	}
	return append(json.RawMessage(nil), response.Result...), nil
}

func (r *Runtime) checkCircuit(key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.breakers[key]
	if state.failures < circuitFailures {
		return nil
	}
	if time.Since(state.openedAt) >= circuitCooldown {
		delete(r.breakers, key)
		return nil
	}
	return ErrCircuitOpen
}

func (r *Runtime) recordFailure(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.breakers[key]
	state.failures++
	if state.failures >= circuitFailures && state.openedAt.IsZero() {
		state.openedAt = time.Now()
	}
	r.breakers[key] = state
}

func (r *Runtime) recordSuccess(key string) {
	r.mu.Lock()
	delete(r.breakers, key)
	r.mu.Unlock()
}

func boundedText(data []byte) string {
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "no stderr"
	}
	return value
}
