//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExecuteTimeoutTerminatesDescendantProcess(t *testing.T) {
	root := t.TempDir()
	ws := openBuiltinWorkspace(t, root)
	execute, err := NewExecute(ws)
	if err != nil {
		t.Fatalf("NewExecute() error = %v", err)
	}
	input, err := json.Marshal(map[string]any{
		"argv": []string{
			"sh",
			"-c",
			"sleep 30 & child=$!; echo $child > child.pid; wait $child",
		},
		"timeout_ms": 100,
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if _, err := execute.Execute(t.Context(), input); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(root, "child.pid"))
	if err != nil {
		t.Fatalf("ReadFile(child.pid) error = %v", err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("Atoi(child.pid) error = %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		err = syscall.Kill(childPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("descendant process %d still exists after command timeout", childPID)
}
