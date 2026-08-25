package evalrunner

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/userInner/kern/internal/evaluation"
)

func TestCodexAgentRunsBoundedExternalBaseline(t *testing.T) {
	t.Setenv("KERN_CODEX_SECRET_TEST", "must-not-reach-codex")
	executable := buildFakeCodex(t)
	agent, err := NewCodexAgent(CodexConfig{Executable: executable})
	if err != nil {
		t.Fatalf("NewCodexAgent() error = %v", err)
	}
	version, err := agent.Version(t.Context())
	if err != nil || version != "codex-test 1.2.3" {
		t.Fatalf("Version() = %q, %v", version, err)
	}
	workspace := t.TempDir()
	result, err := agent.Run(t.Context(), evaluation.AgentRequest{
		Variant:      evaluation.Variant{ID: "external.codex", Agent: "codex", Model: "test-model"},
		WorkspaceDir: workspace, Prompt: "repair the fixture", Timeout: time.Minute, TokenBudget: 100,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.TaskID != "thread-test" || result.TaskStatus != "completed" ||
		result.Usage.InputTokens != 12 || result.Usage.OutputTokens != 4 {
		t.Fatalf("Run() = %#v", result)
	}
	data, err := os.ReadFile(filepath.Join(workspace, "codex-proof.txt"))
	if err != nil || string(data) != "repair the fixture" {
		t.Fatalf("proof = %q, %v", data, err)
	}
	arguments, err := os.ReadFile(filepath.Join(workspace, "codex-args.json"))
	if err != nil || !strings.Contains(string(arguments), `"--approve-for-me"`) ||
		strings.Contains(string(arguments), `"--sandbox"`) ||
		strings.Contains(string(arguments), "danger-full-access") ||
		strings.Contains(string(arguments), "dangerously-bypass") {
		t.Fatalf("Codex arguments = %s, %v", arguments, err)
	}
	leaked, err := os.ReadFile(filepath.Join(workspace, "codex-secret-env.txt"))
	if err != nil || string(leaked) != "absent" {
		t.Fatalf("Codex inherited caller secret = %q, %v", leaked, err)
	}
}

func TestParseCodexEventsUsesLatestCumulativeUsage(t *testing.T) {
	result := evaluation.AgentResult{}
	parseCodexEvents([]byte(
		`{"type":"thread.started","thread_id":"thread-1"}`+"\n"+
			`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":2}}`+"\n"+
			`{"type":"turn.completed","usage":{"input_tokens":20,"output_tokens":3}}`+"\n",
	), &result)
	if result.TaskID != "thread-1" || result.Usage.InputTokens != 20 || result.Usage.OutputTokens != 3 {
		t.Fatalf("result = %#v", result)
	}
}

func TestRouterRequiresConfiguredAgent(t *testing.T) {
	_, err := (Router{}).Run(t.Context(), evaluation.AgentRequest{
		Variant: evaluation.Variant{ID: "external.codex", Agent: "codex"},
	})
	if err == nil {
		t.Fatal("Router.Run() error = nil")
	}
}

func buildFakeCodex(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	source := `package main
import (
  "encoding/json"
  "fmt"
  "io"
  "os"
)
func main() {
  for _, arg := range os.Args[1:] {
    if arg == "--version" { fmt.Print("codex-test 1.2.3"); return }
  }
  var output, workspace string
  for i, arg := range os.Args[1:] {
    if arg == "--output-last-message" && i+2 < len(os.Args) { output = os.Args[i+2] }
    if arg == "--cd" && i+2 < len(os.Args) { workspace = os.Args[i+2] }
  }
  prompt, _ := io.ReadAll(os.Stdin)
  if output == "" || workspace == "" { os.Exit(2) }
  _ = os.WriteFile(output, []byte("completed"), 0600)
  _ = os.WriteFile(workspace+string(os.PathSeparator)+"codex-proof.txt", prompt, 0600)
	secretState := "absent"
	if _, present := os.LookupEnv("KERN_CODEX_SECRET_TEST"); present { secretState = "present" }
	_ = os.WriteFile(workspace+string(os.PathSeparator)+"codex-secret-env.txt", []byte(secretState), 0600)
  encodedArgs, _ := json.Marshal(os.Args[1:])
  _ = os.WriteFile(workspace+string(os.PathSeparator)+"codex-args.json", encodedArgs, 0600)
  events := []map[string]any{
    {"type":"thread.started", "thread_id":"thread-test"},
    {"type":"turn.completed", "usage":map[string]int{"input_tokens":12,"output_tokens":4}},
  }
  for _, event := range events { _ = json.NewEncoder(os.Stdout).Encode(event) }
}
`
	name := filepath.Join(root, "main.go")
	if err := os.WriteFile(name, []byte(source), 0o600); err != nil {
		t.Fatalf("WriteFile(helper) error = %v", err)
	}
	executableName := "codex-test"
	if runtime.GOOS == "windows" {
		executableName += ".exe"
	}
	executable := filepath.Join(root, executableName)
	command := exec.Command("go", "build", "-o", executable, name)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("go build helper: %v: %s", err, output)
	}
	return executable
}
