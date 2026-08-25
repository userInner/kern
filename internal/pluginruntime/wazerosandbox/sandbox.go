// Package wazerosandbox executes import-free Kern WASM plugins with wazero.
package wazerosandbox

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/userInner/kern/internal/pluginruntime"
)

const (
	wasmPageBytes = uint64(64 << 10)
	checkTimeout  = 2 * time.Second
	maxErrorBytes = 512
)

var wasmHeader = []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}

// Sandbox is the production implementation of Kern's import-free WASM ABI.
// A fresh wazero Runtime and module instance are created for every invocation.
type Sandbox struct{}

// New constructs a production WASM sandbox.
func New() *Sandbox {
	return &Sandbox{}
}

// Check compiles an empty module under a bounded production runtime config.
func (*Sandbox) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	checkCtx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	runtime := wazero.NewRuntimeWithConfig(checkCtx, runtimeConfig(1))
	defer runtime.Close(context.Background())
	compiled, err := runtime.CompileModule(checkCtx, wasmHeader)
	if err != nil {
		if checkCtx.Err() != nil {
			return checkCtx.Err()
		}
		return fmt.Errorf("%w: readiness compile: %s", pluginruntime.ErrWASMInvalidModule, boundedError(err))
	}
	return compiled.Close(context.Background())
}

// Invoke executes one kern.plugin.abi/v1 JSON-RPC exchange.
func (*Sandbox) Invoke(ctx context.Context, invocation pluginruntime.WASMInvocation) ([]byte, error) {
	if err := validateInvocation(ctx, invocation); err != nil {
		return nil, err
	}
	sections, err := decodeSections(invocation.Module)
	if err != nil {
		return nil, err
	}
	if hasImports(sections) {
		return nil, fmt.Errorf("%w: v1 modules must declare no imports", pluginruntime.ErrWASMIncompatibleABI)
	}
	pages, err := limitPages(invocation.Limits.MaxMemoryBytes)
	if err != nil {
		return nil, err
	}
	minimum, found, err := memoryMinimum(sections)
	if err != nil {
		return nil, err
	}
	if found && minimum > pages {
		return nil, fmt.Errorf(
			"%w: module requires %d pages, limit is %d",
			pluginruntime.ErrWASMLimitExceeded,
			minimum,
			pages,
		)
	}
	callCtx, cancel := context.WithTimeout(ctx, invocation.Limits.Timeout)
	defer cancel()
	runtime := wazero.NewRuntimeWithConfig(callCtx, runtimeConfig(pages))
	defer runtime.Close(context.Background())
	compiled, err := runtime.CompileModule(callCtx, invocation.Module)
	if err != nil {
		return nil, callError(callCtx, pluginruntime.ErrWASMInvalidModule, "compile", err)
	}
	defer compiled.Close(context.Background())
	if err := validateCompiled(compiled); err != nil {
		return nil, err
	}
	instance, err := runtime.InstantiateModule(
		callCtx,
		compiled,
		wazero.NewModuleConfig().WithName("").WithStartFunctions(),
	)
	if err != nil {
		return nil, callError(callCtx, pluginruntime.ErrWASMTrap, "instantiate", err)
	}
	defer instance.Close(context.Background())
	memory := instance.ExportedMemory(pluginruntime.WASMMemoryExport)
	allocate := instance.ExportedFunction(pluginruntime.WASMAllocateExport)
	invoke := instance.ExportedFunction(pluginruntime.WASMInvokeExport)
	allocated, err := allocate.Call(callCtx, uint64(len(invocation.Request)))
	if err != nil {
		return nil, callError(callCtx, pluginruntime.ErrWASMTrap, "allocate request", err)
	}
	requestPointer := uint32(allocated[0])
	if !memory.Write(requestPointer, invocation.Request) {
		return nil, fmt.Errorf("%w: request range is outside guest memory", pluginruntime.ErrWASMLimitExceeded)
	}
	result, err := invoke.Call(callCtx, uint64(requestPointer), uint64(len(invocation.Request)))
	if err != nil {
		return nil, callError(callCtx, pluginruntime.ErrWASMTrap, "invoke", err)
	}
	responsePointer, responseLength := pluginruntime.UnpackWASMResult(result[0])
	if responseLength == 0 {
		return nil, fmt.Errorf("%w: guest returned an empty response", pluginruntime.ErrProtocol)
	}
	if int64(responseLength) > invocation.Limits.MaxOutputBytes {
		return nil, fmt.Errorf("%w: response exceeds output limit", pluginruntime.ErrWASMLimitExceeded)
	}
	response, ok := memory.Read(responsePointer, responseLength)
	if !ok {
		return nil, fmt.Errorf("%w: response range is outside guest memory", pluginruntime.ErrWASMLimitExceeded)
	}
	return bytes.Clone(response), nil
}

func runtimeConfig(memoryPages uint32) wazero.RuntimeConfig {
	return wazero.NewRuntimeConfig().
		WithCoreFeatures(api.CoreFeaturesV2).
		WithMemoryLimitPages(memoryPages).
		WithDebugInfoEnabled(false).
		WithCloseOnContextDone(true)
}

func validateInvocation(ctx context.Context, invocation pluginruntime.WASMInvocation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if invocation.ABIVersion != pluginruntime.WASMABIVersion {
		return fmt.Errorf(
			"%w: got %q, want %q",
			pluginruntime.ErrWASMIncompatibleABI,
			invocation.ABIVersion,
			pluginruntime.WASMABIVersion,
		)
	}
	if invocation.Limits.Timeout <= 0 || invocation.Limits.MaxMemoryBytes == 0 ||
		invocation.Limits.MaxOutputBytes <= 0 {
		return fmt.Errorf("%w: invocation limits must be positive", pluginruntime.ErrWASMLimitExceeded)
	}
	if len(invocation.Request) == 0 || uint64(len(invocation.Request)) > math.MaxUint32 {
		return fmt.Errorf("%w: request length is invalid", pluginruntime.ErrWASMLimitExceeded)
	}
	if invocation.Limits.MaxOutputBytes > math.MaxUint32 {
		return fmt.Errorf("%w: output limit exceeds the v1 address space", pluginruntime.ErrWASMLimitExceeded)
	}
	return nil
}

func limitPages(maxMemoryBytes uint64) (uint32, error) {
	pages := (maxMemoryBytes + wasmPageBytes - 1) / wasmPageBytes
	if pages == 0 || pages > 65536 {
		return 0, fmt.Errorf("%w: memory limit is outside the v1 address space", pluginruntime.ErrWASMLimitExceeded)
	}
	return uint32(pages), nil
}

func validateCompiled(compiled wazero.CompiledModule) error {
	if len(compiled.ImportedFunctions()) != 0 || len(compiled.ImportedMemories()) != 0 {
		return fmt.Errorf("%w: v1 modules must declare no imports", pluginruntime.ErrWASMIncompatibleABI)
	}
	memory, ok := compiled.ExportedMemories()[pluginruntime.WASMMemoryExport]
	if !ok || memory == nil {
		return fmt.Errorf("%w: missing memory export", pluginruntime.ErrWASMIncompatibleABI)
	}
	functions := compiled.ExportedFunctions()
	if !functionSignature(
		functions[pluginruntime.WASMAllocateExport],
		[]api.ValueType{api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI32},
	) {
		return fmt.Errorf("%w: invalid %s signature", pluginruntime.ErrWASMIncompatibleABI, pluginruntime.WASMAllocateExport)
	}
	if !functionSignature(
		functions[pluginruntime.WASMInvokeExport],
		[]api.ValueType{api.ValueTypeI32, api.ValueTypeI32},
		[]api.ValueType{api.ValueTypeI64},
	) {
		return fmt.Errorf("%w: invalid %s signature", pluginruntime.ErrWASMIncompatibleABI, pluginruntime.WASMInvokeExport)
	}
	return nil
}

func functionSignature(
	definition api.FunctionDefinition,
	parameters []api.ValueType,
	results []api.ValueType,
) bool {
	return definition != nil && slicesEqual(definition.ParamTypes(), parameters) &&
		slicesEqual(definition.ResultTypes(), results)
}

func slicesEqual(first, second []api.ValueType) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}

type wasmSection struct {
	id      byte
	payload []byte
}

func decodeSections(module []byte) ([]wasmSection, error) {
	if len(module) < len(wasmHeader) || !bytes.Equal(module[:len(wasmHeader)], wasmHeader) {
		return nil, fmt.Errorf("%w: invalid header or version", pluginruntime.ErrWASMInvalidModule)
	}
	sections := make([]wasmSection, 0, 8)
	for offset := len(wasmHeader); offset < len(module); {
		sectionID := module[offset]
		offset++
		size, consumed, ok := readVarUint32(module[offset:])
		if !ok {
			return nil, fmt.Errorf("%w: malformed section length", pluginruntime.ErrWASMInvalidModule)
		}
		offset += consumed
		end := uint64(offset) + uint64(size)
		if end > uint64(len(module)) {
			return nil, fmt.Errorf("%w: truncated section", pluginruntime.ErrWASMInvalidModule)
		}
		sections = append(sections, wasmSection{id: sectionID, payload: module[offset:int(end)]})
		offset = int(end)
	}
	return sections, nil
}

func hasImports(sections []wasmSection) bool {
	for _, section := range sections {
		if section.id != 2 {
			continue
		}
		count, _, ok := readVarUint32(section.payload)
		return !ok || count != 0
	}
	return false
}

func memoryMinimum(sections []wasmSection) (uint32, bool, error) {
	for _, section := range sections {
		if section.id != 5 {
			continue
		}
		count, consumed, ok := readVarUint32(section.payload)
		if !ok || count != 1 {
			return 0, false, fmt.Errorf("%w: v1 requires one memory", pluginruntime.ErrWASMIncompatibleABI)
		}
		flags, flagBytes, ok := readVarUint32(section.payload[consumed:])
		if !ok || (flags != 0 && flags != 1) {
			return 0, false, fmt.Errorf("%w: unsupported memory type", pluginruntime.ErrWASMIncompatibleABI)
		}
		minimum, _, ok := readVarUint32(section.payload[consumed+flagBytes:])
		if !ok {
			return 0, false, fmt.Errorf("%w: malformed memory minimum", pluginruntime.ErrWASMInvalidModule)
		}
		return minimum, true, nil
	}
	return 0, false, nil
}

func readVarUint32(data []byte) (uint32, int, bool) {
	var value uint32
	for index := 0; index < 5 && index < len(data); index++ {
		current := data[index]
		if index == 4 && current&0xf0 != 0 {
			return 0, 0, false
		}
		value |= uint32(current&0x7f) << (7 * index)
		if current&0x80 == 0 {
			return value, index + 1, true
		}
	}
	return 0, 0, false
}

func callError(ctx context.Context, kind error, operation string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return fmt.Errorf("%w: %s: %s", kind, operation, boundedError(err))
}

func boundedError(err error) string {
	if err == nil {
		return "unknown error"
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > maxErrorBytes {
		message = message[:maxErrorBytes] + "…"
	}
	return message
}

var _ pluginruntime.WASMSandbox = (*Sandbox)(nil)
