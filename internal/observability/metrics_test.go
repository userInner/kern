package observability

import (
	"bytes"
	"strings"
	"testing"
)

func TestWritePrometheusUsesCumulativeHistogramsAndStableLabels(t *testing.T) {
	snapshot := NewSnapshot()
	snapshot.TaskCurrent["running"] = 2
	snapshot.Operations["inspect\x00read\x00succeeded"] = 3
	snapshot.TaskDuration.Observe(0.02)
	snapshot.TaskDuration.Observe(2)
	var output bytes.Buffer
	if err := WritePrometheus(&output, snapshot); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{
		`kern_tasks_current{status="running"} 2`,
		`kern_operations_total{effect="read",status="succeeded",tool="inspect"} 3`,
		`kern_task_duration_seconds_bucket{le="0.025"} 1`,
		`kern_task_duration_seconds_bucket{le="2.5"} 2`,
		`kern_task_duration_seconds_count 2`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics missing %q\n%s", want, text)
		}
	}
}
