package verifier

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/userInner/kern/internal/artifact"
	"github.com/userInner/kern/internal/operation"
	"github.com/userInner/kern/internal/task"
	"github.com/userInner/kern/internal/verification"
	"github.com/userInner/kern/internal/workspace"
)

func TestSuiteAggregateOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		goal    string
		result  string
		records []operation.Record
		want    verification.Status
	}{
		{name: "no operation result", goal: "answer the question", result: "done", want: verification.StatusVerified},
		{
			name:   "missing approval",
			goal:   "change a file",
			result: "done",
			records: []operation.Record{successfulRecord(
				"op-write", "custom-write", operation.EffectLocalWrite, "",
			)},
			want: verification.StatusFailed,
		},
		{
			name:   "failed command is partial",
			goal:   "run tests",
			result: "tests failed",
			records: []operation.Record{{Operation: operation.Operation{
				ID: "op-test", Tool: "execute", Effect: operation.EffectProcess, Status: operation.StatusFailed,
			}}},
			want: verification.StatusPartial,
		},
		{name: "missing requested command", goal: "运行测试", result: "done", want: verification.StatusPartial},
		{name: "invalid requested json", goal: "return JSON", result: "not json", want: verification.StatusFailed},
		{
			name:   "external write needs a human",
			goal:   "publish the result",
			result: "published",
			records: []operation.Record{successfulRecord(
				"op-publish", "publish", operation.EffectNetworkWrite, "receipt-publish",
			)},
			want: verification.StatusManualRequired,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			suite := openTestSuite(t, tt.records, nil, nil, nil)
			report, err := suite.Verify(t.Context(), testTask(tt.goal), tt.result)
			if err != nil {
				t.Fatalf("Verify() error = %v", err)
			}
			if report.Status != tt.want {
				t.Fatalf("Verify().Status = %q, want %q; checks = %#v", report.Status, tt.want, report.Checks)
			}
			for _, check := range report.Checks {
				if len(check.Evidence) == 0 || check.Evidence[0].Ref == "" {
					t.Fatalf("check has no durable evidence: %#v", check)
				}
			}
		})
	}
}

func TestSuiteVerifiesLatestChangedFileAndDiff(t *testing.T) {
	data := []byte("verified file\n")
	digest := digestString(string(data))
	change := workspace.Change{
		Path:        "note.txt",
		AfterSHA256: digest,
		Created:     true,
		Bytes:       int64(len(data)),
		Diff:        "+verified file\n",
		DiffStatus:  "complete",
	}
	raw, err := json.Marshal(change)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	record := successfulRecord("op-change", "change", operation.EffectLocalWrite, "receipt-change")
	items := []artifact.Artifact{
		{ID: "raw", TaskID: "task-1", AttemptID: "attempt-1", MediaType: "application/json", SourceOperationID: "op-change"},
		{ID: "diff", TaskID: "task-1", AttemptID: "attempt-1", MediaType: "text/x-diff", SourceOperationID: "op-change"},
	}
	suite := openTestSuite(
		t,
		[]operation.Record{record},
		items,
		map[string][]byte{"raw": raw, "diff": []byte(change.Diff)},
		map[string]workspace.File{"note.txt": {Path: "note.txt", Data: data, SHA256: digest, Size: int64(len(data))}},
	)
	report, err := suite.Verify(t.Context(), testTask("create note.txt"), "done")
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if report.Status != verification.StatusVerified {
		t.Fatalf("Verify().Status = %q; checks = %#v", report.Status, report.Checks)
	}
	check := findCheck(t, report, "core.file_integrity")
	if check.Status != verification.StatusPassed || len(check.Evidence) != 1 || check.Evidence[0].Digest != digest {
		t.Fatalf("file integrity check = %#v", check)
	}
}

func TestSuiteRejectsChangedFileAfterOperation(t *testing.T) {
	recorded := digestString("original")
	raw, _ := json.Marshal(workspace.Change{
		Path: "note.txt", AfterSHA256: recorded, Diff: "+original", DiffStatus: "complete",
	})
	record := successfulRecord("op-change", "change", operation.EffectLocalWrite, "receipt-change")
	items := []artifact.Artifact{
		{ID: "raw", TaskID: "task-1", AttemptID: "attempt-1", MediaType: "application/json", SourceOperationID: "op-change"},
		{ID: "diff", TaskID: "task-1", AttemptID: "attempt-1", MediaType: "text/x-diff", SourceOperationID: "op-change"},
	}
	suite := openTestSuite(
		t,
		[]operation.Record{record},
		items,
		map[string][]byte{"raw": raw},
		map[string]workspace.File{"note.txt": {Path: "note.txt", SHA256: digestString("tampered")}},
	)
	report, err := suite.Verify(t.Context(), testTask("change note.txt"), "done")
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if report.Status != verification.StatusFailed {
		t.Fatalf("Verify().Status = %q; checks = %#v", report.Status, report.Checks)
	}
}

func successfulRecord(id, tool string, effect operation.Effect, receipt string) operation.Record {
	exitCode := 0
	return operation.Record{
		Operation: operation.Operation{ID: id, Tool: tool, Effect: effect, Status: operation.StatusSucceeded},
		Result: &operation.Result{
			OperationID: id,
			OutputRef:   "artifact:" + id,
			ExitCode:    &exitCode,
		},
		ApprovalReceiptID: receipt,
	}
}

func testTask(goal string) task.Task {
	return task.Task{ID: "task-1", ActiveAttemptID: "attempt-1", Goal: goal}
}

func findCheck(t *testing.T, report verification.Report, name string) verification.CheckResult {
	t.Helper()
	for _, check := range report.Checks {
		if check.Verifier == name {
			return check
		}
	}
	t.Fatalf("check %q not found in %#v", name, report.Checks)
	return verification.CheckResult{}
}

type fakeLedger struct{ records []operation.Record }

func (f fakeLedger) ListAttemptOperations(context.Context, string, string) ([]operation.Record, error) {
	return f.records, nil
}

type fakeArtifacts struct {
	items   []artifact.Artifact
	content map[string][]byte
}

func (f fakeArtifacts) List(context.Context, string) ([]artifact.Artifact, error) {
	return f.items, nil
}

func (f fakeArtifacts) ReadContent(_ context.Context, _ string, artifactID string, maxBytes int64) ([]byte, error) {
	data, ok := f.content[artifactID]
	if !ok {
		return nil, artifact.ErrNotFound
	}
	if int64(len(data)) > maxBytes {
		return nil, artifact.ErrTooLarge
	}
	return data, nil
}

type fakeWorkspace map[string]workspace.File

func (f fakeWorkspace) ReadFile(_ context.Context, path string) (workspace.File, error) {
	file, ok := f[path]
	if !ok {
		return workspace.File{}, errors.New("file not found")
	}
	return file, nil
}

func openTestSuite(
	t *testing.T,
	records []operation.Record,
	items []artifact.Artifact,
	content map[string][]byte,
	files map[string]workspace.File,
) *Suite {
	t.Helper()
	if content == nil {
		content = make(map[string][]byte)
	}
	if files == nil {
		files = make(map[string]workspace.File)
	}
	for _, record := range records {
		if record.Result == nil || !strings.HasPrefix(record.Result.OutputRef, "artifact:") {
			continue
		}
		artifactID := strings.TrimPrefix(record.Result.OutputRef, "artifact:")
		found := false
		for _, item := range items {
			found = found || item.ID == artifactID
		}
		if !found {
			items = append(items, artifact.Artifact{
				ID: artifactID, TaskID: "task-1", AttemptID: "attempt-1",
				SourceOperationID: record.Operation.ID,
			})
		}
	}
	suite, err := New(fakeLedger{records: records}, fakeArtifacts{items: items, content: content}, fakeWorkspace(files))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return suite
}
