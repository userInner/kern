package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/userInner/kern/internal/executionphase"
	"github.com/userInner/kern/internal/operation"
	"github.com/userInner/kern/internal/plugin"
	"github.com/userInner/kern/internal/tool"
	"github.com/userInner/kern/internal/workspace"
)

func TestInspectReadsSearchesAndRejectsUnknownInput(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "README.md"), []byte("Kern evidence\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	workspace := openBuiltinWorkspace(t, rootPath)
	inspect, err := NewInspect(workspace)
	if err != nil {
		t.Fatalf("NewInspect() error = %v", err)
	}
	result, err := inspect.Execute(
		t.Context(),
		json.RawMessage(`{"action":"read_file","path":"README.md"}`),
	)
	if err != nil {
		t.Fatalf("Execute(read_file) error = %v", err)
	}
	if !strings.Contains(result.Content, "Kern evidence") || !strings.Contains(result.Content, "sha256") {
		t.Fatalf("Execute(read_file) = %s", result.Content)
	}
	result, err = inspect.Execute(
		t.Context(),
		json.RawMessage(`{"action":"search","path":".","query":"evidence"}`),
	)
	if err != nil || !strings.Contains(result.Content, "README.md") {
		t.Fatalf("Execute(search) = %s, %v", result.Content, err)
	}
	if _, err := inspect.Effect(json.RawMessage(`{"action":"list_dir","extra":true}`)); !errors.Is(err, tool.ErrInvalidInput) {
		t.Fatalf("Effect(unknown field) error = %v", err)
	}
}

func TestChangeUsesWorkspaceHashInvariant(t *testing.T) {
	ws := openBuiltinWorkspace(t, t.TempDir())
	change, err := NewChange(ws)
	if err != nil {
		t.Fatalf("NewChange() error = %v", err)
	}
	effect, err := change.Effect(json.RawMessage(`{"action":"write_file","path":"note.txt","content":"first"}`))
	if err != nil || effect != operation.EffectLocalWrite {
		t.Fatalf("Effect() = %q, %v", effect, err)
	}
	result, err := change.Execute(
		t.Context(),
		json.RawMessage(`{"action":"write_file","path":"note.txt","content":"first"}`),
	)
	if err != nil {
		t.Fatalf("Execute(create) error = %v", err)
	}
	var created workspace.Change
	if err := json.Unmarshal([]byte(result.Content), &created); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].MediaType != "text/x-diff" ||
		!strings.Contains(string(result.Artifacts[0].Content), "+first") {
		t.Fatalf("Execute(create) artifacts = %#v", result.Artifacts)
	}
	input, err := json.Marshal(map[string]string{
		"action":          "replace",
		"path":            "note.txt",
		"old":             "first",
		"new":             "second",
		"expected_sha256": created.AfterSHA256,
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	result, err = change.Execute(t.Context(), input)
	if err != nil {
		t.Fatalf("Execute(replace) error = %v", err)
	}
	if len(result.Artifacts) != 1 || !strings.Contains(string(result.Artifacts[0].Content), "-first") ||
		!strings.Contains(string(result.Artifacts[0].Content), "+second") {
		t.Fatalf("Execute(replace) artifacts = %#v", result.Artifacts)
	}
}

func TestChangeReconcilesInterruptedAtomicWriteFromHashes(t *testing.T) {
	root := t.TempDir()
	ws := openBuiltinWorkspace(t, root)
	change, err := NewChange(ws)
	if err != nil {
		t.Fatalf("NewChange() error = %v", err)
	}
	input := json.RawMessage(`{"action":"write_file","path":"note.txt","content":"target"}`)
	metadata, err := change.PrepareRecovery(t.Context(), input)
	if err != nil {
		t.Fatalf("PrepareRecovery() error = %v", err)
	}
	result, err := change.Reconcile(t.Context(), input, metadata)
	if err != nil || result.Disposition != tool.RecoveryNotExecuted {
		t.Fatalf("Reconcile(before) = %#v, %v", result, err)
	}
	if _, err := change.Execute(t.Context(), input); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	result, err = change.Reconcile(t.Context(), input, metadata)
	if err != nil || result.Disposition != tool.RecoverySucceeded {
		t.Fatalf("Reconcile(after) = %#v, %v", result, err)
	}
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("external edit"), 0o644); err != nil {
		t.Fatalf("WriteFile(tamper) error = %v", err)
	}
	result, err = change.Reconcile(t.Context(), input, metadata)
	if err != nil || result.Disposition != tool.RecoveryConflict {
		t.Fatalf("Reconcile(conflict) = %#v, %v", result, err)
	}
}

func TestChangeMovesFileAndReconcilesBothCompleteStates(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("move evidence"), 0o600); err != nil {
		t.Fatalf("WriteFile(source) error = %v", err)
	}
	ws := openBuiltinWorkspace(t, root)
	changeTool, err := NewChange(ws)
	if err != nil {
		t.Fatalf("NewChange() error = %v", err)
	}
	source, err := ws.ReadFile(t.Context(), "source.txt")
	if err != nil {
		t.Fatalf("ReadFile(source) error = %v", err)
	}
	input, err := json.Marshal(map[string]string{
		"action":           "move_file",
		"path":             "source.txt",
		"destination_path": "destination.txt",
		"expected_sha256":  source.SHA256,
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	metadata, err := changeTool.PrepareRecovery(t.Context(), input)
	if err != nil {
		t.Fatalf("PrepareRecovery() error = %v", err)
	}
	reconciled, err := changeTool.Reconcile(t.Context(), input, metadata)
	if err != nil || reconciled.Disposition != tool.RecoveryNotExecuted {
		t.Fatalf("Reconcile(before) = %#v, %v", reconciled, err)
	}
	result, err := changeTool.Execute(t.Context(), input)
	if err != nil {
		t.Fatalf("Execute(move_file) error = %v", err)
	}
	var moved workspace.Change
	if err := json.Unmarshal([]byte(result.Content), &moved); err != nil {
		t.Fatalf("Unmarshal(result) error = %v", err)
	}
	if moved.MovedFrom != "source.txt" || moved.Path != "destination.txt" ||
		len(result.Artifacts) != 1 || result.Artifacts[0].Name != "destination.txt.diff" ||
		!strings.Contains(string(result.Artifacts[0].Content), "similarity index 100%") {
		t.Fatalf("Execute(move_file) = %#v, artifacts = %#v", moved, result.Artifacts)
	}
	reconciled, err = changeTool.Reconcile(t.Context(), input, metadata)
	if err != nil || reconciled.Disposition != tool.RecoverySucceeded {
		t.Fatalf("Reconcile(after) = %#v, %v", reconciled, err)
	}
}

func TestChangeMoveRecoveryDetectsConflictAndInputMismatch(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("approved"), 0o600); err != nil {
		t.Fatalf("WriteFile(source) error = %v", err)
	}
	ws := openBuiltinWorkspace(t, root)
	changeTool, err := NewChange(ws)
	if err != nil {
		t.Fatalf("NewChange() error = %v", err)
	}
	source, err := ws.ReadFile(t.Context(), "source.txt")
	if err != nil {
		t.Fatalf("ReadFile(source) error = %v", err)
	}
	input, err := json.Marshal(map[string]string{
		"action":           "move_file",
		"path":             "source.txt",
		"destination_path": "destination.txt",
		"expected_sha256":  source.SHA256,
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	metadata, err := changeTool.PrepareRecovery(t.Context(), input)
	if err != nil {
		t.Fatalf("PrepareRecovery() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "destination.txt"), []byte("external"), 0o600); err != nil {
		t.Fatalf("WriteFile(destination) error = %v", err)
	}
	reconciled, err := changeTool.Reconcile(t.Context(), input, metadata)
	if err != nil || reconciled.Disposition != tool.RecoveryConflict {
		t.Fatalf("Reconcile(conflict) = %#v, %v", reconciled, err)
	}
	mismatched := json.RawMessage(strings.Replace(string(input), "destination.txt", "other.txt", 1))
	if _, err := changeTool.Reconcile(t.Context(), mismatched, metadata); err == nil {
		t.Fatal("Reconcile(mismatched input) error = nil")
	}
}

func TestChangeMoveRejectsIncompleteOrMixedInputs(t *testing.T) {
	changeTool, err := NewChange(openBuiltinWorkspace(t, t.TempDir()))
	if err != nil {
		t.Fatalf("NewChange() error = %v", err)
	}
	inputs := []string{
		`{"action":"move_file","path":"source.txt","expected_sha256":"abc"}`,
		`{"action":"move_file","path":"source.txt","destination_path":"destination.txt"}`,
		`{"action":"move_file","path":"source.txt","destination_path":"destination.txt","expected_sha256":"abc","content":"unexpected"}`,
	}
	for _, input := range inputs {
		if _, err := changeTool.Effect(json.RawMessage(input)); !errors.Is(err, tool.ErrInvalidInput) {
			t.Errorf("Effect(%s) error = %v, want ErrInvalidInput", input, err)
		}
	}
}

func TestExecuteRunsArgvWithoutShell(t *testing.T) {
	ws := openBuiltinWorkspace(t, t.TempDir())
	execute, err := NewExecute(ws)
	if err != nil {
		t.Fatalf("NewExecute() error = %v", err)
	}
	effect, err := execute.Effect(json.RawMessage(`{"argv":["go","version"]}`))
	if err != nil || effect != operation.EffectProcess {
		t.Fatalf("Effect() = %q, %v", effect, err)
	}
	result, err := execute.Execute(t.Context(), json.RawMessage(`{"argv":["go","version"]}`))
	if err != nil {
		t.Fatalf("Execute() error = %v; result = %s", err, result.Content)
	}
	if !strings.Contains(result.Content, "go version") || result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("Execute() = %#v", result)
	}
}

func TestExecuteProvidesIsolatedGoBuildEnvironment(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/execute-test\n\ngo 1.26.6\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(go.mod) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "answer.go"), []byte("package answer\n\nfunc Value() int { return 42 }\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(answer.go) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "answer_test.go"), []byte("package answer\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) { if Value() != 42 { t.Fatal(\"unexpected value\") } }\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(answer_test.go) error = %v", err)
	}

	ws := openBuiltinWorkspace(t, root)
	execute, err := NewExecute(ws)
	if err != nil {
		t.Fatalf("NewExecute() error = %v", err)
	}
	result, err := execute.Execute(t.Context(), json.RawMessage(`{"argv":["go","test","./..."]}`))
	if err != nil {
		t.Fatalf("Execute(go test) error = %v; result = %s", err, result.Content)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 || !strings.Contains(result.Content, "ok") {
		t.Fatalf("Execute(go test) = %#v", result)
	}
}

func TestExecuteDoesNotInheritCallerSecrets(t *testing.T) {
	t.Setenv("KERN_EXECUTE_TEST_SECRET", "must-not-reach-child")
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	ws := openBuiltinWorkspace(t, t.TempDir())
	execute, err := NewExecute(ws)
	if err != nil {
		t.Fatalf("NewExecute() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{
		"argv": []string{executable, "-test.run=^TestExecuteEnvironmentHelper$"},
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	result, err := execute.Execute(t.Context(), input)
	if err != nil {
		t.Fatalf("Execute(helper) error = %v; result = %s", err, result.Content)
	}
	if strings.Contains(result.Content, "must-not-reach-child") || !strings.Contains(result.Content, "secret=;home=isolated") {
		t.Fatalf("Execute(helper) leaked or omitted isolation evidence: %s", result.Content)
	}
}

func TestExecuteEnvironmentHelper(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" || !strings.Contains(filepath.Base(filepath.Dir(home)), "kern-execute-") {
		fmt.Printf("secret=%s;home=unexpected", os.Getenv("KERN_EXECUTE_TEST_SECRET"))
		return
	}
	fmt.Printf("secret=%s;home=isolated", os.Getenv("KERN_EXECUTE_TEST_SECRET"))
}

func TestNetworkRejectsLocalAndCredentialTargets(t *testing.T) {
	network := NewNetwork()
	tests := []string{
		`{"url":"http://127.0.0.1/private"}`,
		`{"url":"http://localhost/private"}`,
		`{"url":"http://user:pass@example.com/private"}`,
		`{"url":"https://example.com:8443/private"}`,
		`{"url":"file:///etc/passwd"}`,
	}
	for _, input := range tests {
		if _, err := network.Effect(json.RawMessage(input)); err == nil {
			t.Errorf("Effect(%s) error = nil", input)
		}
	}
}

func TestNetworkAllowsOnlyStandardExplicitPorts(t *testing.T) {
	network := NewNetwork()
	for _, input := range []string{
		`{"url":"http://example.com:80/resource"}`,
		`{"url":"https://example.com:443/resource"}`,
	} {
		if _, err := network.Effect(json.RawMessage(input)); err != nil {
			t.Errorf("Effect(%s) error = %v", input, err)
		}
	}
}

func TestInspectClassifiesPublicFetchAsNetworkRead(t *testing.T) {
	inspect, err := NewInspect(openBuiltinWorkspace(t, t.TempDir()))
	if err != nil {
		t.Fatalf("NewInspect() error = %v", err)
	}
	effect, err := inspect.Effect(json.RawMessage(`{"action":"fetch_url","url":"https://example.com/resource"}`))
	if err != nil || effect != operation.EffectNetworkRead {
		t.Fatalf("Effect(fetch_url) = %q, %v", effect, err)
	}
	if _, err := inspect.Effect(json.RawMessage(`{"action":"fetch_url","url":"http://127.0.0.1/private"}`)); err == nil {
		t.Fatal("Effect(private fetch_url) error = nil")
	}
}

func TestCapabilityOnlyExposesAttemptAuditedPlugin(t *testing.T) {
	installed := builtinPluginPackage(t)
	usage := plugin.Usage{
		SchemaVersion: plugin.SchemaVersion,
		TaskID:        "task-1", AttemptID: "attempt-1", PluginID: installed.ID,
		Version: installed.Version, Digest: installed.Digest, Reason: "manual_enable",
		Resources: json.RawMessage(`{"knowledge":["knowledge.md"],"phases":{"prepare":{"knowledge":["knowledge.md"]},"execute":{"knowledge":["knowledge.md"]},"verify":{"knowledge":["knowledge.md"]}}}`),
	}
	runtime := &fakePluginRuntime{result: json.RawMessage(`{"ok":true}`)}
	capability, err := NewCapability(
		fakeCapabilityUsage{items: []plugin.Usage{usage}},
		fakeInstalledPlugin{item: installed},
		runtime,
	)
	if err != nil {
		t.Fatalf("NewCapability() error = %v", err)
	}
	if _, err := capability.Execute(t.Context(), json.RawMessage(`{"action":"list"}`)); err == nil {
		t.Fatal("Execute(without task scope) error = nil")
	}
	ctx := tool.WithTaskScope(t.Context(), "task-1", "attempt-1")
	result, err := capability.Execute(ctx, json.RawMessage(`{"action":"list"}`))
	if err != nil || !strings.Contains(result.Content, installed.ID) {
		t.Fatalf("Execute(list) = %s, %v", result.Content, err)
	}
	result, err = capability.Execute(
		ctx,
		json.RawMessage(`{"action":"get_bundle","plugin_id":"dev.kern.go-expert"}`),
	)
	if err != nil || !strings.Contains(result.Content, "Prefer table-driven tests") {
		t.Fatalf("Execute(get_bundle) = %s, %v", result.Content, err)
	}
	prepareCtx := tool.WithTaskScopePhase(
		t.Context(), "task-1", "attempt-1", executionphase.Prepare,
	)
	result, err = capability.Execute(
		prepareCtx,
		json.RawMessage(`{"action":"get_bundle","plugin_id":"dev.kern.go-expert"}`),
	)
	if err != nil || strings.Contains(result.Content, "package_report") ||
		!strings.Contains(result.Content, `"phase":"prepare"`) {
		t.Fatalf("Execute(get_bundle prepare) = %s, %v", result.Content, err)
	}
	if _, err := capability.Execute(
		ctx,
		json.RawMessage(`{"action":"get_bundle","plugin_id":"dev.kern.other"}`),
	); err == nil {
		t.Fatal("Execute(inactive plugin) error = nil")
	}
	invokeInput := json.RawMessage(`{"action":"invoke","plugin_id":"dev.kern.go-expert","tool_id":"package_report","input":{"package":"./..."}}`)
	effect, err := capability.Effect(invokeInput)
	if err != nil || effect != operation.EffectProcess {
		t.Fatalf("Effect(invoke) = %q, %v", effect, err)
	}
	if _, err := capability.Execute(ctx, invokeInput); err == nil {
		t.Fatal("Execute(unaudited tool) error = nil")
	}
	usage.Resources = json.RawMessage(`{"knowledge":["knowledge.md"],"tools":[{"id":"package_report","runtime":"subprocess"}],"phases":{"prepare":{"knowledge":["knowledge.md"]},"execute":{"knowledge":["knowledge.md"],"tools":[{"id":"package_report","runtime":"subprocess"}]},"verify":{"knowledge":["knowledge.md"],"tools":[{"id":"package_report","runtime":"subprocess"}]}}}`)
	capability, err = NewCapability(
		fakeCapabilityUsage{items: []plugin.Usage{usage}},
		fakeInstalledPlugin{item: installed},
		runtime,
	)
	if err != nil {
		t.Fatalf("NewCapability(with tool) error = %v", err)
	}
	if _, err := capability.Execute(prepareCtx, invokeInput); err == nil {
		t.Fatal("Execute(invoke prepare) error = nil")
	}
	result, err = capability.Execute(ctx, invokeInput)
	if err != nil || result.Content != `{"ok":true}` || runtime.calls != 1 || runtime.toolID != "package_report" {
		t.Fatalf("Execute(invoke) = %#v, %v; runtime=%#v", result, err, runtime)
	}
}

func TestCapabilityRunsRegisteredHostToolThroughPhaseAndSchemaBoundary(t *testing.T) {
	var calls int
	host := HostCapability{
		ProviderID:   "host.example",
		ProviderName: "Example Host",
		ID:           "lookup",
		Description:  "Look up one value.",
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"required":["key"],"properties":{"key":{"type":"string","minLength":1}}}`),
		Effect:       operation.EffectNetworkRead,
		Handler: func(_ context.Context, input json.RawMessage) (tool.Result, error) {
			calls++
			return tool.Result{Content: `{"value":"found"}`}, nil
		},
	}
	capability, err := NewCapability(
		fakeCapabilityUsage{},
		fakeInstalledPlugin{},
		&fakePluginRuntime{},
		host,
	)
	if err != nil {
		t.Fatalf("NewCapability() error = %v", err)
	}
	executeCtx := tool.WithTaskScopePhase(t.Context(), "task-1", "attempt-1", executionphase.Execute)
	list, err := capability.Execute(executeCtx, json.RawMessage(`{"action":"list"}`))
	if err != nil || !strings.Contains(list.Content, "host.example") || strings.Contains(list.Content, "lookup") {
		t.Fatalf("Execute(list) = %s, %v", list.Content, err)
	}
	bundle, err := capability.Execute(
		executeCtx,
		json.RawMessage(`{"action":"get_bundle","plugin_id":"host.example"}`),
	)
	if err != nil || !strings.Contains(bundle.Content, `"id":"lookup"`) ||
		!strings.Contains(bundle.Content, `"effect":"network_read"`) {
		t.Fatalf("Execute(get_bundle) = %s, %v", bundle.Content, err)
	}
	prepareCtx := tool.WithTaskScopePhase(t.Context(), "task-1", "attempt-1", executionphase.Prepare)
	bundle, err = capability.Execute(
		prepareCtx,
		json.RawMessage(`{"action":"get_bundle","plugin_id":"host.example"}`),
	)
	if err != nil || strings.Contains(bundle.Content, `"id":"lookup"`) {
		t.Fatalf("Execute(get_bundle prepare) = %s, %v", bundle.Content, err)
	}
	invoke := json.RawMessage(`{"action":"invoke","plugin_id":"host.example","tool_id":"lookup","input":{"key":"x"}}`)
	effect, err := capability.Effect(invoke)
	if err != nil || effect != operation.EffectNetworkRead {
		t.Fatalf("Effect(invoke) = %q, %v", effect, err)
	}
	if _, err := capability.Execute(prepareCtx, invoke); err == nil {
		t.Fatal("Execute(invoke prepare) error = nil")
	}
	if _, err := capability.Execute(
		executeCtx,
		json.RawMessage(`{"action":"invoke","plugin_id":"host.example","tool_id":"lookup","input":{}}`),
	); err == nil {
		t.Fatal("Execute(invalid input) error = nil")
	}
	result, err := capability.Execute(executeCtx, invoke)
	if err != nil || result.Content != `{"value":"found"}` || calls != 1 {
		t.Fatalf("Execute(invoke) = %#v, %v; calls=%d", result, err, calls)
	}
}

func TestCapabilityRejectsInvalidHostRegistrations(t *testing.T) {
	valid := HostCapability{
		ProviderID: "host.example", ProviderName: "Example", ID: "lookup",
		Description: "Lookup", InputSchema: json.RawMessage(`{"type":"object"}`),
		Effect:  operation.EffectRead,
		Handler: func(context.Context, json.RawMessage) (tool.Result, error) { return tool.Result{}, nil },
	}
	invalidPrefix := valid
	invalidPrefix.ProviderID = "dev.example"
	invalidSchema := valid
	invalidSchema.InputSchema = json.RawMessage(`{"type":"string"}`)
	invalidEffect := valid
	invalidEffect.Effect = operation.EffectUnknown
	missingHandler := valid
	missingHandler.Handler = nil
	tests := []struct {
		name  string
		items []HostCapability
	}{
		{name: "reserved prefix", items: []HostCapability{invalidPrefix}},
		{name: "schema", items: []HostCapability{invalidSchema}},
		{name: "effect", items: []HostCapability{invalidEffect}},
		{name: "handler", items: []HostCapability{missingHandler}},
		{name: "duplicate", items: []HostCapability{valid, valid}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewCapability(
				fakeCapabilityUsage{}, fakeInstalledPlugin{}, &fakePluginRuntime{}, test.items...,
			); err == nil {
				t.Fatal("NewCapability() error = nil")
			}
		})
	}
}

type fakeCapabilityUsage struct{ items []plugin.Usage }

func (f fakeCapabilityUsage) ListAttemptPlugins(context.Context, string, string) ([]plugin.Usage, error) {
	return f.items, nil
}

type fakeInstalledPlugin struct{ item plugin.Installed }

func (f fakeInstalledPlugin) Get(context.Context, string) (plugin.Installed, error) {
	return f.item, nil
}

type fakePluginRuntime struct {
	result json.RawMessage
	calls  int
	toolID string
}

func (f *fakePluginRuntime) Invoke(
	_ context.Context,
	_ plugin.Installed,
	spec plugin.ToolSpec,
	_ json.RawMessage,
) (json.RawMessage, error) {
	f.calls++
	f.toolID = spec.ID
	return f.result, nil
}

func builtinPluginPackage(t *testing.T) plugin.Installed {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(directory, "knowledge.md"),
		[]byte("Prefer table-driven tests.\n"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(knowledge) error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(directory, "tool.json"),
		[]byte(`{"schema_version":"1","id":"package_report","description":"Inspect packages.","runtime":"subprocess","command":["go","version"],"input_schema":{"type":"object","additionalProperties":false,"required":["package"],"properties":{"package":{"type":"string"}}}}`),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(tool) error = %v", err)
	}
	digest, err := plugin.PackageDigest(directory)
	if err != nil {
		t.Fatalf("PackageDigest() error = %v", err)
	}
	manifest := plugin.Manifest{
		SchemaVersion: plugin.SchemaVersion,
		ID:            "dev.kern.go-expert", Name: "Go Expert", Version: "0.1.0", Core: ">=0.1.0 <0.2.0",
		Entrypoints: plugin.Entrypoints{Knowledge: []string{"knowledge.md"}, Tools: []string{"tool.json"}},
		Permissions: plugin.Permissions{Process: []string{"go"}},
		Integrity:   plugin.Integrity{Files: digest},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("Marshal(manifest) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, plugin.ManifestFile), encoded, 0o600); err != nil {
		t.Fatalf("WriteFile(manifest) error = %v", err)
	}
	return plugin.Installed{
		SchemaVersion: plugin.SchemaVersion,
		ID:            manifest.ID, Name: manifest.Name, Version: manifest.Version,
		Digest: digest, Manifest: manifest, InstallPath: directory,
		InstalledAt: time.Now(), UpdatedAt: time.Now(),
	}
}

func openBuiltinWorkspace(t *testing.T, path string) *workspace.Workspace {
	t.Helper()
	opened, err := workspace.Open(path)
	if err != nil {
		t.Fatalf("workspace.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	return opened
}
