package wazerosandbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/userInner/kern/internal/plugin"
	"github.com/userInner/kern/internal/pluginruntime"
)

const successfulResponse = `{"jsonrpc":"2.0","id":"1","result":{"ok":true}}`

func TestCheck(t *testing.T) {
	if err := New().Check(t.Context()); err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := New().Check(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Check(cancelled) error = %v", err)
	}
}

func TestInvokeExecutesGuestABI(t *testing.T) {
	invocation := validInvocation(wasmGuest(1024, 1, responseBody(successfulResponse)))
	response, err := New().Invoke(t.Context(), invocation)
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if string(response) != successfulResponse {
		t.Fatalf("response = %s", response)
	}
	response[0] = 'x'
	again, err := New().Invoke(t.Context(), invocation)
	if err != nil || string(again) != successfulResponse {
		t.Fatalf("fresh Invoke() = %s, %v", again, err)
	}
}

func TestPluginRuntimeExecutesRealWazeroSandbox(t *testing.T) {
	module := wasmGuest(1024, 1, responseBody(successfulResponse))
	installed, spec := installedPlugin(t, module)
	result, err := pluginruntime.NewWithConfig(pluginruntime.Config{WASM: New()}).Invoke(
		t.Context(), installed, spec, json.RawMessage(`{"message":"hello"}`),
	)
	if err != nil {
		t.Fatalf("Runtime.Invoke() error = %v", err)
	}
	if string(result) != `{"ok":true}` {
		t.Fatalf("result = %s", result)
	}
}

func TestInvokeRejectsUnknownABIImportsAndMissingExports(t *testing.T) {
	tests := []struct {
		name       string
		invocation pluginruntime.WASMInvocation
		want       error
	}{
		{
			name: "unknown ABI", invocation: func() pluginruntime.WASMInvocation {
				invocation := validInvocation(wasmGuest(1024, 1, responseBody(successfulResponse)))
				invocation.ABIVersion = "kern.plugin.abi/v2"
				return invocation
			}(),
			want: pluginruntime.ErrWASMIncompatibleABI,
		},
		{
			name:       "import",
			invocation: validInvocation(importingModule()),
			want:       pluginruntime.ErrWASMIncompatibleABI,
		},
		{
			name:       "missing exports",
			invocation: validInvocation(append([]byte(nil), wasmHeader...)),
			want:       pluginruntime.ErrWASMIncompatibleABI,
		},
		{
			name:       "malformed section",
			invocation: validInvocation(append(append([]byte(nil), wasmHeader...), 1, 10, 0)),
			want:       pluginruntime.ErrWASMInvalidModule,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New().Invoke(t.Context(), test.invocation); !errors.Is(err, test.want) {
				t.Fatalf("Invoke() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestInvokeEnforcesMemoryAndOutputRanges(t *testing.T) {
	tests := []struct {
		name       string
		invocation pluginruntime.WASMInvocation
	}{
		{
			name:       "initial memory",
			invocation: validInvocation(wasmGuest(1024, 2, responseBody(successfulResponse))),
		},
		{
			name:       "request range",
			invocation: validInvocation(wasmGuest(65530, 1, responseBody(successfulResponse))),
		},
		{
			name: "output length",
			invocation: func() pluginruntime.WASMInvocation {
				invocation := validInvocation(wasmGuest(1024, 1, responseBody(successfulResponse)))
				invocation.Limits.MaxOutputBytes = 8
				return invocation
			}(),
		},
		{
			name: "response range",
			invocation: validInvocation(wasmGuest(
				1024,
				1,
				constantBody(pluginruntime.PackWASMResult(65530, uint32(len(successfulResponse)))),
			)),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New().Invoke(t.Context(), test.invocation); !errors.Is(err, pluginruntime.ErrWASMLimitExceeded) {
				t.Fatalf("Invoke() error = %v", err)
			}
		})
	}
}

func TestInvokeClassifiesTrapAndStopsInfiniteLoop(t *testing.T) {
	trap := validInvocation(wasmGuest(1024, 1, []byte{0x00, 0x42, 0x00, 0x0b}))
	if _, err := New().Invoke(t.Context(), trap); !errors.Is(err, pluginruntime.ErrWASMTrap) {
		t.Fatalf("Invoke(trap) error = %v", err)
	}
	loop := validInvocation(wasmGuest(1024, 1, loopBody()))
	loop.Limits.Timeout = 20 * time.Millisecond
	started := time.Now()
	_, err := New().Invoke(t.Context(), loop)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Invoke(loop) error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("infinite loop cancellation took %s", elapsed)
	}
}

func TestInvokeDeniesMemoryGrowthBeyondLimit(t *testing.T) {
	denied := `{"jsonrpc":"2.0","id":"1","result":{"grow":"denied"}}`
	allowed := `{"jsonrpc":"2.0","id":"1","result":{"grow":"allowed"}}`
	const allowedOffset = 256
	data := make([]byte, allowedOffset+len(allowed))
	copy(data, denied)
	copy(data[allowedOffset:], allowed)
	body := []byte{
		0x41, 0x01, // i32.const 1 page
		0x40, 0x00, // memory.grow memory 0
		0x41, 0x7f, // i32.const -1
		0x46,       // i32.eq
		0x04, 0x7e, // if (result i64)
		0x42,
	}
	body = append(body, signedLEB(int64(pluginruntime.PackWASMResult(0, uint32(len(denied)))))...)
	body = append(body, 0x05, 0x42)
	body = append(body, signedLEB(int64(pluginruntime.PackWASMResult(allowedOffset, uint32(len(allowed)))))...)
	body = append(body, 0x0b, 0x0b)
	response, err := New().Invoke(t.Context(), validInvocation(wasmGuestData(1024, 1, body, data)))
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if string(response) != denied {
		t.Fatalf("memory.grow response = %s", response)
	}
}

func validInvocation(module []byte) pluginruntime.WASMInvocation {
	return pluginruntime.WASMInvocation{
		ABIVersion: pluginruntime.WASMABIVersion,
		PluginID:   "dev.kern.wazero-test",
		Version:    "0.1.0",
		ToolID:     "echo",
		Module:     module,
		Request:    []byte(`{"jsonrpc":"2.0","id":"1","method":"invoke","params":{}}`),
		Limits: pluginruntime.WASMLimits{
			Timeout: 250 * time.Millisecond, MaxMemoryBytes: 64 << 10, MaxOutputBytes: 1024,
		},
	}
}

func wasmGuest(requestPointer, memoryPages uint32, invokeBody []byte) []byte {
	return wasmGuestData(requestPointer, memoryPages, invokeBody, []byte(successfulResponse))
}

func wasmGuestData(requestPointer, memoryPages uint32, invokeBody, dataBytes []byte) []byte {
	module := append([]byte(nil), wasmHeader...)
	types := []byte{
		2,
		0x60, 1, 0x7f, 1, 0x7f,
		0x60, 2, 0x7f, 0x7f, 1, 0x7e,
	}
	module = appendSection(module, 1, types)
	module = appendSection(module, 3, []byte{2, 0, 1})
	memory := append([]byte{1, 0}, unsignedLEB(uint64(memoryPages))...)
	module = appendSection(module, 5, memory)
	exports := []byte{3}
	exports = appendName(exports, pluginruntime.WASMMemoryExport)
	exports = append(exports, 2, 0)
	exports = appendName(exports, pluginruntime.WASMAllocateExport)
	exports = append(exports, 0, 0)
	exports = appendName(exports, pluginruntime.WASMInvokeExport)
	exports = append(exports, 0, 1)
	module = appendSection(module, 7, exports)
	allocateBody := []byte{0, 0x41}
	allocateBody = append(allocateBody, signedLEB(int64(requestPointer))...)
	allocateBody = append(allocateBody, 0x0b)
	code := []byte{2}
	code = appendVector(code, allocateBody)
	code = appendVector(code, append([]byte{0}, invokeBody...))
	module = appendSection(module, 10, code)
	if len(dataBytes) > 0 {
		data := []byte{1, 0, 0x41, 0, 0x0b}
		data = appendVector(data, dataBytes)
		module = appendSection(module, 11, data)
	}
	return module
}

func installedPlugin(t *testing.T, module []byte) (plugin.Installed, plugin.ToolSpec) {
	t.Helper()
	spec := plugin.ToolSpec{
		SchemaVersion: plugin.SchemaVersion,
		ID:            "echo",
		Description:   "Echo through the real WASM sandbox.",
		Runtime:       "wasm",
		Module:        "echo.wasm",
		InputSchema: json.RawMessage(
			`{"type":"object","additionalProperties":false,"required":["message"],"properties":{"message":{"type":"string"}}}`,
		),
		TimeoutMS:      250,
		MaxOutputBytes: 1024,
	}
	directory := t.TempDir()
	encodedSpec, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("Marshal(spec) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "tool.json"), encodedSpec, 0o600); err != nil {
		t.Fatalf("WriteFile(spec) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, spec.Module), module, 0o600); err != nil {
		t.Fatalf("WriteFile(module) error = %v", err)
	}
	digest, err := plugin.PackageDigest(directory)
	if err != nil {
		t.Fatalf("PackageDigest() error = %v", err)
	}
	manifest := plugin.Manifest{
		SchemaVersion: plugin.SchemaVersion,
		ID:            "dev.kern.wazero-integration-test",
		Name:          "wazero integration test",
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

func responseBody(response string) []byte {
	return constantBody(pluginruntime.PackWASMResult(0, uint32(len(response))))
}

func constantBody(result uint64) []byte {
	body := []byte{0x42}
	body = append(body, signedLEB(int64(result))...)
	return append(body, 0x0b)
}

func loopBody() []byte {
	return []byte{
		0x03, 0x40, // loop with empty block type
		0x0c, 0x00, // branch to the loop itself
		0x0b,       // end loop
		0x42, 0x00, // unreachable result used only for validation
		0x0b,
	}
}

func importingModule() []byte {
	module := append([]byte(nil), wasmHeader...)
	module = appendSection(module, 1, []byte{1, 0x60, 0, 0})
	imports := []byte{1}
	imports = appendName(imports, "env")
	imports = appendName(imports, "forbidden")
	imports = append(imports, 0, 0)
	return appendSection(module, 2, imports)
}

func appendSection(module []byte, sectionID byte, payload []byte) []byte {
	module = append(module, sectionID)
	module = append(module, unsignedLEB(uint64(len(payload)))...)
	return append(module, payload...)
}

func appendVector(target, value []byte) []byte {
	target = append(target, unsignedLEB(uint64(len(value)))...)
	return append(target, value...)
}

func appendName(target []byte, value string) []byte {
	return appendVector(target, []byte(value))
}

func unsignedLEB(value uint64) []byte {
	encoded := make([]byte, 0, 10)
	for {
		current := byte(value & 0x7f)
		value >>= 7
		if value != 0 {
			current |= 0x80
		}
		encoded = append(encoded, current)
		if value == 0 {
			return encoded
		}
	}
}

func signedLEB(value int64) []byte {
	encoded := make([]byte, 0, 10)
	for {
		current := byte(value & 0x7f)
		value >>= 7
		done := (value == 0 && current&0x40 == 0) || (value == -1 && current&0x40 != 0)
		if !done {
			current |= 0x80
		}
		encoded = append(encoded, current)
		if done {
			return encoded
		}
	}
}
