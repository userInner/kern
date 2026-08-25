package pluginruntime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/userInner/kern/internal/plugin"
)

type wasmSandboxFunc func(context.Context, WASMInvocation) ([]byte, error)

func (wasmSandboxFunc) Check(ctx context.Context) error {
	return ctx.Err()
}

func (f wasmSandboxFunc) Invoke(ctx context.Context, invocation WASMInvocation) ([]byte, error) {
	return f(ctx, invocation)
}

func TestInvokeSubprocessUsesVersionedJSONRPC(t *testing.T) {
	installed, spec := runtimePlugin(t, "echo", 10_000, 64<<10)
	result, err := New().Invoke(
		t.Context(),
		installed,
		spec,
		json.RawMessage(`{"message":"hello"}`),
	)
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	var response struct {
		PluginID string `json:"plugin_id"`
		Tool     string `json:"tool"`
		Message  string `json:"message"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if response.PluginID != installed.ID || response.Tool != spec.ID || response.Message != "hello" {
		t.Fatalf("result = %s", result)
	}
}

func TestInvokeRejectsInputBeforeStartingProcess(t *testing.T) {
	installed, spec := runtimePlugin(t, "echo", 10_000, 64<<10)
	_, err := New().Invoke(t.Context(), installed, spec, json.RawMessage(`{"message":"hello","unknown":true}`))
	if err == nil || !strings.Contains(err.Error(), "$.unknown is not allowed") {
		t.Fatalf("Invoke() error = %v", err)
	}
}

func TestInvokeRevalidatesExecutableDeclaration(t *testing.T) {
	installed, spec := wasmRuntimePlugin(t, 250, 1024)
	spec.Module = "../outside.wasm"
	called := false
	sandbox := wasmSandboxFunc(func(context.Context, WASMInvocation) ([]byte, error) {
		called = true
		return nil, nil
	})
	_, err := NewWithConfig(Config{WASM: sandbox}).Invoke(
		t.Context(), installed, spec, json.RawMessage(`{"message":"hello"}`),
	)
	if err == nil || !strings.Contains(err.Error(), "invalid capability declaration") {
		t.Fatalf("Invoke() error = %v", err)
	}
	if called {
		t.Fatal("sandbox was called for an invalid module path")
	}
}

func TestInvokeRejectsInvalidWASMBeforeSandbox(t *testing.T) {
	installed, spec := wasmRuntimePlugin(t, 250, 1024)
	if err := os.WriteFile(filepath.Join(installed.InstallPath, spec.Module), []byte("not-wasm"), 0o600); err != nil {
		t.Fatalf("WriteFile(module) error = %v", err)
	}
	digest, err := plugin.PackageDigest(installed.InstallPath)
	if err != nil {
		t.Fatalf("PackageDigest() error = %v", err)
	}
	installed.Digest = digest
	installed.Manifest.Integrity.Files = digest
	called := false
	sandbox := wasmSandboxFunc(func(context.Context, WASMInvocation) ([]byte, error) {
		called = true
		return nil, nil
	})
	_, err = NewWithConfig(Config{WASM: sandbox}).Invoke(
		t.Context(), installed, spec, json.RawMessage(`{"message":"hello"}`),
	)
	if !errors.Is(err, ErrWASMInvalidModule) {
		t.Fatalf("Invoke() error = %v", err)
	}
	if called {
		t.Fatal("sandbox was called for an invalid WASM module")
	}
}

func TestInvokeRejectsProtocolTimeoutAndOversizedOutput(t *testing.T) {
	tests := []struct {
		name       string
		mode       string
		timeoutMS  int
		outputSize int
		want       error
	}{
		{name: "protocol", mode: "protocol", timeoutMS: 10_000, outputSize: 64 << 10, want: ErrProtocol},
		{name: "timeout", mode: "sleep", timeoutMS: 100, outputSize: 64 << 10, want: context.DeadlineExceeded},
		{name: "output", mode: "oversized", timeoutMS: 10_000, outputSize: 256, want: errors.New("output limit")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installed, spec := runtimePlugin(t, tt.mode, tt.timeoutMS, tt.outputSize)
			_, err := New().Invoke(t.Context(), installed, spec, json.RawMessage(`{"message":"hello"}`))
			if tt.want == ErrProtocol || tt.want == context.DeadlineExceeded {
				if !errors.Is(err, tt.want) {
					t.Fatalf("Invoke() error = %v, want %v", err, tt.want)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want.Error()) {
				t.Fatalf("Invoke() error = %v, want text %q", err, tt.want)
			}
		})
	}
}

func TestInvokeOpensCircuitAfterRepeatedFailures(t *testing.T) {
	installed, spec := runtimePlugin(t, "protocol", 10_000, 64<<10)
	runtime := New()
	for attempt := 0; attempt < circuitFailures; attempt++ {
		if _, err := runtime.Invoke(t.Context(), installed, spec, json.RawMessage(`{"message":"hello"}`)); !errors.Is(err, ErrProtocol) {
			t.Fatalf("Invoke(%d) error = %v", attempt+1, err)
		}
	}
	if _, err := runtime.Invoke(t.Context(), installed, spec, json.RawMessage(`{"message":"hello"}`)); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("Invoke(after failures) error = %v", err)
	}
}

func TestInvokeRejectsCapabilityThatMutatesInstalledPackage(t *testing.T) {
	installed, spec := runtimePlugin(t, "tamper", 10_000, 64<<10)
	_, err := New().Invoke(t.Context(), installed, spec, json.RawMessage(`{"message":"hello"}`))
	if err == nil || !strings.Contains(err.Error(), "modified its installed package") ||
		!errors.Is(err, plugin.ErrIntegrity) {
		t.Fatalf("Invoke() error = %v", err)
	}
}

func TestInvokeWASMUsesBoundedHostABI(t *testing.T) {
	installed, spec := wasmRuntimePlugin(t, 250, 1024)
	var observed WASMInvocation
	sandbox := wasmSandboxFunc(func(_ context.Context, invocation WASMInvocation) ([]byte, error) {
		observed = invocation
		return []byte(`{"jsonrpc":"2.0","id":"1","result":{"ok":true}}`), nil
	})
	result, err := NewWithConfig(Config{WASM: sandbox}).Invoke(
		t.Context(), installed, spec, json.RawMessage(`{"message":"hello"}`),
	)
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if string(result) != `{"ok":true}` {
		t.Fatalf("result = %s", result)
	}
	if observed.ABIVersion != WASMABIVersion || observed.PluginID != installed.ID || observed.Version != installed.Version ||
		observed.ToolID != spec.ID || !bytes.Equal(observed.Module, wasmModuleHeader) {
		t.Fatalf("invocation identity = %#v", observed)
	}
	if observed.Limits.Timeout != 250*time.Millisecond || observed.Limits.MaxMemoryBytes != wasmMemoryBytes ||
		observed.Limits.MaxOutputBytes != 1024 {
		t.Fatalf("invocation limits = %#v", observed.Limits)
	}
	var request struct {
		JSONRPC string `json:"jsonrpc"`
		ID      string `json:"id"`
		Method  string `json:"method"`
		Params  struct {
			SchemaVersion string          `json:"schema_version"`
			Tool          string          `json:"tool"`
			Input         json.RawMessage `json:"input"`
		} `json:"params"`
	}
	if err := json.Unmarshal(observed.Request, &request); err != nil || request.JSONRPC != "2.0" ||
		request.ID != "1" || request.Method != "invoke" || request.Params.SchemaVersion != plugin.SchemaVersion ||
		request.Params.Tool != spec.ID || string(request.Params.Input) != `{"message":"hello"}` {
		t.Fatalf("request = %#v, %v", request, err)
	}
}

func TestWASMResultPointerPacking(t *testing.T) {
	tests := []struct {
		pointer uint32
		length  uint32
	}{
		{},
		{pointer: 1, length: 2},
		{pointer: ^uint32(0), length: ^uint32(0)},
		{pointer: 0x10203040, length: 0x50607080},
	}
	for _, test := range tests {
		packed := PackWASMResult(test.pointer, test.length)
		pointer, length := UnpackWASMResult(packed)
		if pointer != test.pointer || length != test.length {
			t.Fatalf("UnpackWASMResult(%#x) = (%#x, %#x)", packed, pointer, length)
		}
	}
}

func TestInvokeWASMRejectsUnavailableTimeoutOutputAndProtocol(t *testing.T) {
	installed, spec := wasmRuntimePlugin(t, 50, 128)
	if _, err := New().Invoke(t.Context(), installed, spec, json.RawMessage(`{"message":"hello"}`)); !errors.Is(err, ErrRuntimeUnavailable) {
		t.Fatalf("Invoke(no sandbox) error = %v", err)
	}
	tests := []struct {
		name    string
		sandbox WASMSandbox
		want    error
	}{
		{
			name: "timeout",
			sandbox: wasmSandboxFunc(func(ctx context.Context, _ WASMInvocation) ([]byte, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			}),
			want: context.DeadlineExceeded,
		},
		{
			name: "output",
			sandbox: wasmSandboxFunc(func(context.Context, WASMInvocation) ([]byte, error) {
				return bytes.Repeat([]byte("x"), 129), nil
			}),
			want: ErrWASMLimitExceeded,
		},
		{
			name: "protocol",
			sandbox: wasmSandboxFunc(func(context.Context, WASMInvocation) ([]byte, error) {
				return []byte("not-json"), nil
			}),
			want: ErrProtocol,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewWithConfig(Config{WASM: test.sandbox}).Invoke(
				t.Context(), installed, spec, json.RawMessage(`{"message":"hello"}`),
			)
			if test.want == context.DeadlineExceeded || test.want == ErrProtocol || test.want == ErrWASMLimitExceeded {
				if !errors.Is(err, test.want) {
					t.Fatalf("Invoke() error = %v, want %v", err, test.want)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want.Error()) {
				t.Fatalf("Invoke() error = %v, want text %q", err, test.want)
			}
		})
	}
}

func wasmRuntimePlugin(t *testing.T, timeoutMS, outputBytes int) (plugin.Installed, plugin.ToolSpec) {
	t.Helper()
	spec := plugin.ToolSpec{
		SchemaVersion: plugin.SchemaVersion,
		ID:            "echo",
		Description:   "Echo validated input.",
		Runtime:       "wasm",
		Module:        "echo.wasm",
		InputSchema: json.RawMessage(`{
		  "type":"object",
		  "additionalProperties":false,
		  "required":["message"],
		  "properties":{"message":{"type":"string"}}
		}`),
		TimeoutMS:      timeoutMS,
		MaxOutputBytes: outputBytes,
	}
	directory := t.TempDir()
	encodedSpec, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("Marshal(spec) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "tool.json"), encodedSpec, 0o600); err != nil {
		t.Fatalf("WriteFile(tool) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, spec.Module), wasmModuleHeader, 0o600); err != nil {
		t.Fatalf("WriteFile(module) error = %v", err)
	}
	digest, err := plugin.PackageDigest(directory)
	if err != nil {
		t.Fatalf("PackageDigest() error = %v", err)
	}
	manifest := plugin.Manifest{
		SchemaVersion: plugin.SchemaVersion,
		ID:            "dev.kern.wasm-runtime-test",
		Name:          "WASM Runtime Test",
		Version:       "0.1.0",
		Core:          ">=0.1.0 <0.2.0",
		Entrypoints:   plugin.Entrypoints{Tools: []string{"tool.json"}},
		Integrity:     plugin.Integrity{Files: digest},
	}
	return plugin.Installed{
		SchemaVersion: plugin.SchemaVersion,
		ID:            manifest.ID,
		Name:          manifest.Name,
		Version:       manifest.Version,
		Digest:        digest,
		Manifest:      manifest,
		InstallPath:   directory,
	}, spec
}

func runtimePlugin(t *testing.T, mode string, timeoutMS, outputBytes int) (plugin.Installed, plugin.ToolSpec) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	t.Setenv("PATH", filepath.Dir(executable)+string(os.PathListSeparator)+os.Getenv("PATH"))
	spec := plugin.ToolSpec{
		SchemaVersion: plugin.SchemaVersion,
		ID:            "echo",
		Description:   "Echo validated input.",
		Runtime:       "subprocess",
		Command: []string{
			filepath.Base(executable),
			"-test.run=^TestPluginRuntimeHelperProcess$",
			"--",
			mode,
		},
		InputSchema: json.RawMessage(`{
		  "type":"object",
		  "additionalProperties":false,
		  "required":["message"],
		  "properties":{"message":{"type":"string"}}
		}`),
		TimeoutMS:      timeoutMS,
		MaxOutputBytes: outputBytes,
	}
	directory := t.TempDir()
	encodedSpec, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("Marshal(spec) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "tool.json"), encodedSpec, 0o600); err != nil {
		t.Fatalf("WriteFile(tool) error = %v", err)
	}
	digest, err := plugin.PackageDigest(directory)
	if err != nil {
		t.Fatalf("PackageDigest() error = %v", err)
	}
	manifest := plugin.Manifest{
		SchemaVersion: plugin.SchemaVersion,
		ID:            "dev.kern.runtime-test",
		Name:          "Runtime Test",
		Version:       "0.1.0",
		Core:          ">=0.1.0 <0.2.0",
		Entrypoints:   plugin.Entrypoints{Tools: []string{"tool.json"}},
		Permissions:   plugin.Permissions{Process: []string{filepath.Base(executable)}},
		Integrity:     plugin.Integrity{Files: digest},
	}
	return plugin.Installed{
		SchemaVersion: plugin.SchemaVersion,
		ID:            manifest.ID,
		Name:          manifest.Name,
		Version:       manifest.Version,
		Digest:        digest,
		Manifest:      manifest,
		InstallPath:   directory,
	}, spec
}

func TestPluginRuntimeHelperProcess(t *testing.T) {
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	mode := os.Args[separator+1]
	if mode == "sleep" {
		time.Sleep(5 * time.Second)
		return
	}
	if mode == "protocol" {
		fmt.Print("not-json")
		return
	}
	if mode == "oversized" {
		fmt.Printf(`{"jsonrpc":"2.0","id":"1","result":{"value":"%s"}}`, strings.Repeat("x", 1<<10))
		return
	}
	if mode == "tamper" {
		if err := os.WriteFile("tool.json", []byte("tampered"), 0o600); err != nil {
			os.Exit(4)
		}
		fmt.Print(`{"jsonrpc":"2.0","id":"1","result":{"ok":true}}`)
		os.Exit(0)
	}
	var request struct {
		JSONRPC string `json:"jsonrpc"`
		ID      string `json:"id"`
		Method  string `json:"method"`
		Params  struct {
			Tool  string `json:"tool"`
			Input struct {
				Message string `json:"message"`
			} `json:"input"`
		} `json:"params"`
	}
	if err := json.NewDecoder(bufio.NewReader(os.Stdin)).Decode(&request); err != nil {
		os.Exit(2)
	}
	response := map[string]any{
		"jsonrpc": "2.0",
		"id":      request.ID,
		"result": map[string]string{
			"plugin_id": os.Getenv("KERN_PLUGIN_ID"),
			"tool":      request.Params.Tool,
			"message":   request.Params.Input.Message,
		},
	}
	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}
