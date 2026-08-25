// Package observability defines Kern's bounded, low-cardinality operational metrics.
package observability

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

var durationBuckets = []float64{
	0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10,
	30, 60, 120, 300, 600, 1800, 3600,
}

// Histogram is a cumulative fixed-bucket histogram.
type Histogram struct {
	Buckets []float64
	Counts  []uint64
	Count   uint64
	Sum     float64
}

// NewDurationHistogram returns the shared latency bucket layout.
func NewDurationHistogram() Histogram {
	return Histogram{
		Buckets: append([]float64(nil), durationBuckets...),
		Counts:  make([]uint64, len(durationBuckets)),
	}
}

// Observe records one non-negative duration in seconds.
func (h *Histogram) Observe(seconds float64) {
	if h == nil || seconds < 0 {
		return
	}
	h.Count++
	h.Sum += seconds
	for index, upper := range h.Buckets {
		if seconds <= upper {
			h.Counts[index]++
		}
	}
}

// Database captures database/sql pool saturation without exposing query data.
type Database struct {
	MaxOpenConnections int
	OpenConnections    int
	InUse              int
	Idle               int
	WaitCount          int64
	WaitDuration       time.Duration
}

// Snapshot contains a restart-safe view derived from durable Core facts plus
// bounded process and database gauges. Composite map keys use NUL separators
// internally and are never exposed as labels without normalization.
type Snapshot struct {
	GeneratedAt time.Time

	TasksCreated uint64
	TaskCurrent  map[string]uint64
	Attempts     map[string]uint64
	TaskDuration Histogram

	ModelCalls        map[string]uint64
	ModelCallsStarted uint64
	ModelCallDuration Histogram
	ModelTokens       map[string]uint64
	ModelCostMicros   uint64
	ModelRetries      uint64

	Operations        map[string]uint64
	OperationDuration Histogram
	UnknownOperations uint64

	Approvals    map[string]uint64
	ApprovalWait Histogram

	RecoveryResolutions map[string]uint64
	PluginEvents        map[string]uint64
	Verifications       map[string]uint64
	EvalRuns            map[string]uint64
	EvalCases           map[string]uint64

	ActiveTasks int
	QueuedTasks int
	Goroutines  int
	Database    Database
}

// NewSnapshot initializes every map and histogram to a safe empty value.
func NewSnapshot() Snapshot {
	return Snapshot{
		GeneratedAt:         time.Now().UTC(),
		TaskCurrent:         make(map[string]uint64),
		Attempts:            make(map[string]uint64),
		TaskDuration:        NewDurationHistogram(),
		ModelCalls:          make(map[string]uint64),
		ModelCallDuration:   NewDurationHistogram(),
		ModelTokens:         make(map[string]uint64),
		Operations:          make(map[string]uint64),
		OperationDuration:   NewDurationHistogram(),
		Approvals:           make(map[string]uint64),
		ApprovalWait:        NewDurationHistogram(),
		RecoveryResolutions: make(map[string]uint64),
		PluginEvents:        make(map[string]uint64),
		Verifications:       make(map[string]uint64),
		EvalRuns:            make(map[string]uint64),
		EvalCases:           make(map[string]uint64),
	}
}

// WritePrometheus writes the Prometheus text exposition format. Every label
// dimension is drawn from a fixed Core-controlled vocabulary.
func WritePrometheus(w io.Writer, snapshot Snapshot) error {
	emitter := prometheusEmitter{w: w}
	emitter.gauge("kern_build_info", "Static Kern process identity.", nil, 1)
	emitter.gauge("kern_metrics_snapshot_timestamp_seconds", "Unix time when durable metrics were collected.", nil, float64(snapshot.GeneratedAt.UnixNano())/1e9)
	emitter.counter("kern_tasks_created_total", "Durable tasks created.", nil, snapshot.TasksCreated)
	emitter.mapMetric("kern_tasks_current", "Current durable tasks by state.", "gauge", "status", snapshot.TaskCurrent)
	emitter.mapMetric("kern_attempts_total", "Durable attempts by final or current state.", "counter", "status", snapshot.Attempts)
	emitter.histogram("kern_task_duration_seconds", "Finished Attempt duration.", snapshot.TaskDuration)
	emitter.mapMetric("kern_model_calls_total", "Model calls by outcome.", "counter", "status", snapshot.ModelCalls)
	emitter.counter("kern_model_calls_started_total", "Model calls durably recorded before provider I/O.", nil, snapshot.ModelCallsStarted)
	emitter.histogram("kern_model_call_duration_seconds", "Model call latency.", snapshot.ModelCallDuration)
	emitter.mapMetric("kern_model_tokens_total", "Provider-reported model tokens by kind.", "counter", "kind", snapshot.ModelTokens)
	emitter.counterFloat("kern_model_cost_usd_total", "Provider-reported model cost in US dollars.", nil, float64(snapshot.ModelCostMicros)/1_000_000)
	emitter.counter("kern_model_retries_total", "Retryable model calls retried.", nil, snapshot.ModelRetries)
	emitter.compositeMetric("kern_operations_total", "Durable operations by bounded tool, effect, and status.", "counter", []string{"tool", "effect", "status"}, snapshot.Operations)
	emitter.histogram("kern_operation_duration_seconds", "Finished operation execution duration.", snapshot.OperationDuration)
	emitter.gauge("kern_operations_unknown", "Operations whose external outcome is unresolved.", nil, float64(snapshot.UnknownOperations))
	emitter.compositeMetric("kern_approval_requests_total", "Approval requests by risk and outcome.", "counter", []string{"risk", "status"}, snapshot.Approvals)
	emitter.histogram("kern_approval_wait_seconds", "Time from approval request to an immutable decision.", snapshot.ApprovalWait)
	emitter.mapMetric("kern_recovery_resolutions_total", "Human recovery conclusions by result.", "counter", "resolution", snapshot.RecoveryResolutions)
	emitter.mapMetric("kern_plugin_events_total", "Plugin activation outcomes.", "counter", "outcome", snapshot.PluginEvents)
	emitter.mapMetric("kern_verifications_total", "Durable verification conclusions.", "counter", "status", snapshot.Verifications)
	emitter.mapMetric("kern_eval_runs_total", "Evaluation runs by state.", "counter", "status", snapshot.EvalRuns)
	emitter.mapMetric("kern_eval_cases_total", "First-attempt evaluation cases by outcome.", "counter", "outcome", snapshot.EvalCases)
	emitter.gauge("kern_tasks_active", "Current non-terminal tasks excluding queued tasks.", nil, float64(snapshot.ActiveTasks))
	emitter.gauge("kern_tasks_queued", "Current tasks waiting for a worker.", nil, float64(snapshot.QueuedTasks))
	emitter.gauge("kern_go_goroutines", "Current Go goroutines.", nil, float64(snapshot.Goroutines))
	emitter.gauge("kern_db_connections", "Current database connections by state.", map[string]string{"state": "open"}, float64(snapshot.Database.OpenConnections))
	emitter.gauge("kern_db_connections", "Current database connections by state.", map[string]string{"state": "in_use"}, float64(snapshot.Database.InUse))
	emitter.gauge("kern_db_connections", "Current database connections by state.", map[string]string{"state": "idle"}, float64(snapshot.Database.Idle))
	emitter.gauge("kern_db_max_open_connections", "Configured database connection limit.", nil, float64(snapshot.Database.MaxOpenConnections))
	emitter.counterFloat("kern_db_wait_total", "Database pool waits.", nil, float64(snapshot.Database.WaitCount))
	emitter.counterFloat("kern_db_wait_seconds_total", "Time spent waiting for a database connection.", nil, snapshot.Database.WaitDuration.Seconds())
	return emitter.err
}

type prometheusEmitter struct {
	w         io.Writer
	err       error
	described map[string]bool
}

func (e *prometheusEmitter) describe(name, help, metricType string) {
	if e.err != nil {
		return
	}
	if e.described == nil {
		e.described = make(map[string]bool)
	}
	if e.described[name] {
		return
	}
	e.described[name] = true
	_, e.err = fmt.Fprintf(e.w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, metricType)
}

func (e *prometheusEmitter) counter(name, help string, labels map[string]string, value uint64) {
	e.counterFloat(name, help, labels, float64(value))
}

func (e *prometheusEmitter) counterFloat(name, help string, labels map[string]string, value float64) {
	e.describe(name, help, "counter")
	e.sample(name, labels, value)
}

func (e *prometheusEmitter) gauge(name, help string, labels map[string]string, value float64) {
	e.describe(name, help, "gauge")
	e.sample(name, labels, value)
}

func (e *prometheusEmitter) mapMetric(name, help, metricType, label string, values map[string]uint64) {
	e.describe(name, help, metricType)
	for _, key := range sortedKeys(values) {
		e.sample(name, map[string]string{label: key}, float64(values[key]))
	}
}

func (e *prometheusEmitter) compositeMetric(name, help, metricType string, labels []string, values map[string]uint64) {
	e.describe(name, help, metricType)
	for _, key := range sortedKeys(values) {
		parts := strings.Split(key, "\x00")
		if len(parts) != len(labels) {
			continue
		}
		sampleLabels := make(map[string]string, len(labels))
		for index := range labels {
			sampleLabels[labels[index]] = parts[index]
		}
		e.sample(name, sampleLabels, float64(values[key]))
	}
}

func (e *prometheusEmitter) histogram(name, help string, histogram Histogram) {
	e.describe(name, help, "histogram")
	for index, upper := range histogram.Buckets {
		count := uint64(0)
		if index < len(histogram.Counts) {
			count = histogram.Counts[index]
		}
		e.sample(name+"_bucket", map[string]string{"le": strconv.FormatFloat(upper, 'g', -1, 64)}, float64(count))
	}
	e.sample(name+"_bucket", map[string]string{"le": "+Inf"}, float64(histogram.Count))
	e.sample(name+"_sum", nil, histogram.Sum)
	e.sample(name+"_count", nil, float64(histogram.Count))
}

func (e *prometheusEmitter) sample(name string, labels map[string]string, value float64) {
	if e.err != nil {
		return
	}
	labelText := ""
	if len(labels) > 0 {
		keys := make([]string, 0, len(labels))
		for key := range labels {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		pairs := make([]string, 0, len(keys))
		for _, key := range keys {
			pairs = append(pairs, key+"=\""+escapeLabel(labels[key])+"\"")
		}
		labelText = "{" + strings.Join(pairs, ",") + "}"
	}
	_, e.err = fmt.Fprintf(e.w, "%s%s %s\n", name, labelText, strconv.FormatFloat(value, 'g', -1, 64))
}

func sortedKeys(values map[string]uint64) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func escapeLabel(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\"", "\\\"")
	return replacer.Replace(value)
}
