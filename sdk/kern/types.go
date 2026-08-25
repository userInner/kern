// Package kern provides a dependency-free Go client for the Kern Core HTTP API.
package kern

import (
	"encoding/json"
	"time"
)

// Status is the durable task lifecycle state.
type Status string

const (
	StatusCreated            Status = "created"
	StatusPlanning           Status = "planning"
	StatusRunning            Status = "running"
	StatusWaitingApproval    Status = "waiting_approval"
	StatusWaitingInput       Status = "waiting_input"
	StatusVerifying          Status = "verifying"
	StatusCompleted          Status = "completed"
	StatusPartiallyCompleted Status = "partially_completed"
	StatusFailed             Status = "failed"
	StatusCancelled          Status = "cancelled"
)

// Terminal reports whether a task will not advance without a new attempt.
func (s Status) Terminal() bool {
	switch s {
	case StatusCompleted, StatusPartiallyCompleted, StatusFailed, StatusCancelled:
		return true
	default:
		return false
	}
}

// Task is the public task snapshot returned by Kern.
type Task struct {
	SchemaVersion   string     `json:"schema_version"`
	ID              string     `json:"id"`
	TraceID         string     `json:"trace_id"`
	Title           string     `json:"title"`
	Goal            string     `json:"goal"`
	Status          Status     `json:"status"`
	Result          string     `json:"result"`
	ErrorMessage    string     `json:"error_message"`
	ActiveAttemptID string     `json:"active_attempt_id"`
	ModelConfigID   string     `json:"model_config_id,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	LeaseOwner      string     `json:"lease_owner"`
	LeaseExpiresAt  *time.Time `json:"lease_expires_at,omitempty"`
	HeartbeatAt     *time.Time `json:"heartbeat_at,omitempty"`
	PausedFrom      Status     `json:"paused_from_status,omitempty"`
}

// Event is one immutable event from the task SSE stream.
type Event struct {
	SchemaVersion string          `json:"schema_version"`
	ID            int64           `json:"id"`
	TaskID        string          `json:"task_id"`
	AttemptID     string          `json:"attempt_id"`
	TraceID       string          `json:"trace_id"`
	SpanID        string          `json:"span_id"`
	ParentSpanID  string          `json:"parent_span_id,omitempty"`
	Type          string          `json:"type"`
	Payload       json.RawMessage `json:"payload"`
	CreatedAt     time.Time       `json:"created_at"`
}

// Approval is a pending exact-scope authorization request.
type Approval struct {
	ID          string          `json:"id"`
	TaskID      string          `json:"task_id"`
	AttemptID   string          `json:"attempt_id"`
	OperationID string          `json:"operation_id"`
	Scope       json.RawMessage `json:"scope"`
	Risk        string          `json:"risk"`
	Explanation string          `json:"explanation"`
	Status      string          `json:"status"`
	ExpiresAt   time.Time       `json:"expires_at"`
	CreatedAt   time.Time       `json:"created_at"`
}

// ApprovalReceipt is the immutable result of an authorization decision.
type ApprovalReceipt struct {
	ID        string          `json:"id"`
	RequestID string          `json:"request_id"`
	Decision  string          `json:"decision"`
	Actor     string          `json:"actor"`
	Scope     json.RawMessage `json:"scope"`
	DecidedAt time.Time       `json:"decided_at"`
}

// Artifact describes an immutable task output. Content is downloaded separately.
type Artifact struct {
	SchemaVersion     string    `json:"schema_version"`
	ID                string    `json:"id"`
	TaskID            string    `json:"task_id"`
	AttemptID         string    `json:"attempt_id"`
	Name              string    `json:"name"`
	Digest            string    `json:"digest"`
	MediaType         string    `json:"media_type"`
	Size              int64     `json:"size"`
	SourceOperationID string    `json:"source_operation_id,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}

// Verification is one durable deterministic verification result.
type Verification struct {
	ID        string          `json:"id"`
	TaskID    string          `json:"task_id"`
	AttemptID string          `json:"attempt_id"`
	Verifier  string          `json:"verifier"`
	Status    string          `json:"status"`
	Evidence  json.RawMessage `json:"evidence"`
	CreatedAt time.Time       `json:"created_at"`
}

// CreateTaskInput selects the goal and optionally a persisted model connection.
type CreateTaskInput struct {
	Title         string               `json:"title,omitempty"`
	Goal          string               `json:"goal"`
	ModelConfigID string               `json:"model_config_id,omitempty"`
	Plugins       *TaskPluginSelection `json:"plugins,omitempty"`
	Attachments   []TaskAttachment     `json:"attachments,omitempty"`
}

// TaskAttachment is one image uploaded with a task. Encoding/json serializes
// Data as base64; Core validates its declared type, signature, and size.
type TaskAttachment struct {
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Data      []byte `json:"data"`
}

// SubmitTaskInput is one follow-up message and its optional new images. The
// prior task context and artifact references are inherited by the new Attempt.
type SubmitTaskInput struct {
	Content     string           `json:"content"`
	Attachments []TaskAttachment `json:"attachments,omitempty"`
}

// TaskPluginSelection contains explicit task-level activation overrides.
type TaskPluginSelection struct {
	Enable  []string `json:"enable,omitempty"`
	Disable []string `json:"disable,omitempty"`
}

// PolicyProfile selects the Core-owned authorization policy for executions
// that start after a settings update.
type PolicyProfile string

const (
	PolicyLocalSafe  PolicyProfile = "local-safe"
	PolicyConfirmAll PolicyProfile = "confirm-all"
	PolicyReadOnly   PolicyProfile = "read-only"
)

// RuntimeSettings is the public, secret-free configuration used by new task
// executions. Existing Attempts retain the settings captured at startup.
type RuntimeSettings struct {
	SchemaVersion string        `json:"schema_version"`
	MaxTurns      int           `json:"max_turns"`
	MaxToolCalls  int           `json:"max_tool_calls"`
	MaxTokens     int           `json:"max_tokens"`
	MaxCostUSD    float64       `json:"max_cost_usd"`
	TaskTimeout   string        `json:"task_timeout"`
	PolicyProfile PolicyProfile `json:"policy_profile"`
	RetentionDays int           `json:"retention_days"`
}

// UpdateSettingsInput replaces every user-editable runtime setting as one
// validated unit.
type UpdateSettingsInput struct {
	MaxTurns      int           `json:"max_turns"`
	MaxToolCalls  int           `json:"max_tool_calls"`
	MaxTokens     int           `json:"max_tokens"`
	MaxCostUSD    float64       `json:"max_cost_usd"`
	TaskTimeout   string        `json:"task_timeout"`
	PolicyProfile PolicyProfile `json:"policy_profile"`
	RetentionDays int           `json:"retention_days"`
}

// CleanupPreview reports terminal task data older than the active retention
// boundary without mutating it.
type CleanupPreview struct {
	Cutoff        time.Time `json:"cutoff"`
	RetentionDays int       `json:"retention_days"`
	TaskCount     int       `json:"task_count"`
	ArtifactCount int       `json:"artifact_count"`
	ArtifactBytes int64     `json:"artifact_bytes"`
}

// CleanupResult reports committed task deletion and unreferenced content
// objects removed from the artifact store.
type CleanupResult struct {
	CleanupPreview
	RemovedObjects int   `json:"removed_objects"`
	RemovedBytes   int64 `json:"removed_bytes"`
}

// Health is the public liveness or readiness response.
type Health struct {
	SchemaVersion string `json:"schema_version"`
	Status        string `json:"status"`
	Mode          string `json:"mode"`
}

// ModelProvider identifies a supported model connection protocol.
type ModelProvider string

const (
	ModelProviderOpenAICompatible ModelProvider = "openai-compatible"
	ModelProviderOllama           ModelProvider = "ollama"
)

// ModelConfig is a public-safe model connection. Secret values and references
// never enter this response.
type ModelConfig struct {
	SchemaVersion string        `json:"schema_version"`
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Provider      ModelProvider `json:"provider"`
	BaseURL       string        `json:"base_url"`
	Model         string        `json:"model"`
	HasAPIKey     bool          `json:"has_api_key"`
	Enabled       bool          `json:"enabled"`
	IsDefault     bool          `json:"is_default"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
}

// ModelConfigInput creates or replaces one model connection. APIKey is
// write-only and is transferred directly to the local system credential store;
// it is mutually exclusive with APIKeyEnv and ClearAPIKey.
type ModelConfigInput struct {
	Name        string        `json:"name"`
	Provider    ModelProvider `json:"provider"`
	BaseURL     string        `json:"base_url"`
	Model       string        `json:"model"`
	APIKeyEnv   string        `json:"api_key_env,omitempty"`
	APIKey      string        `json:"api_key,omitempty"`
	ClearAPIKey bool          `json:"clear_api_key,omitempty"`
	Enabled     *bool         `json:"enabled,omitempty"`
	SetDefault  bool          `json:"set_default,omitempty"`
}

// ModelConfigs contains supported providers and saved public connections.
type ModelConfigs struct {
	SchemaVersion            string          `json:"schema_version"`
	CredentialStoreAvailable bool            `json:"credential_store_available"`
	Providers                []ModelProvider `json:"providers"`
	Configs                  []ModelConfig   `json:"configs"`
}

// ModelConfigTestResult is a content-free live provider probe.
type ModelConfigTestResult struct {
	SchemaVersion string          `json:"schema_version"`
	ConfigID      string          `json:"config_id"`
	Provider      ModelProvider   `json:"provider"`
	Model         string          `json:"model"`
	OK            bool            `json:"ok"`
	LatencyMS     int64           `json:"latency_ms"`
	RequestID     string          `json:"request_id,omitempty"`
	Capabilities  map[string]bool `json:"capabilities"`
}

// PlanStatus is the lifecycle state of one durable plan revision.
type PlanStatus string

const (
	PlanActive     PlanStatus = "active"
	PlanCompleted  PlanStatus = "completed"
	PlanFailed     PlanStatus = "failed"
	PlanSuperseded PlanStatus = "superseded"
)

// PlanStepStatus is the lifecycle state of one auditable plan step.
type PlanStepStatus string

const (
	PlanStepPending   PlanStepStatus = "pending"
	PlanStepRunning   PlanStepStatus = "running"
	PlanStepCompleted PlanStepStatus = "completed"
	PlanStepFailed    PlanStepStatus = "failed"
	PlanStepSkipped   PlanStepStatus = "skipped"
)

// PlanPhase is one Core-owned execution phase.
type PlanPhase string

const (
	PlanPrepare PlanPhase = "prepare"
	PlanExecute PlanPhase = "execute"
	PlanVerify  PlanPhase = "verify"
)

// Plan is the current durable plan for an Attempt.
type Plan struct {
	SchemaVersion string     `json:"schema_version"`
	ID            string     `json:"id"`
	TaskID        string     `json:"task_id"`
	AttemptID     string     `json:"attempt_id"`
	Revision      int        `json:"revision"`
	Status        PlanStatus `json:"status"`
	Rationale     string     `json:"rationale"`
	Steps         []PlanStep `json:"steps"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// PlanStep is one ordered, durable unit of planned work.
type PlanStep struct {
	ID          string         `json:"id"`
	PlanID      string         `json:"plan_id"`
	Ordinal     int            `json:"ordinal"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Phase       PlanPhase      `json:"phase"`
	Required    bool           `json:"required"`
	Status      PlanStepStatus `json:"status"`
	Failure     string         `json:"failure"`
	RetryCount  int            `json:"retry_count"`
	StartedAt   *time.Time     `json:"started_at,omitempty"`
	CompletedAt *time.Time     `json:"completed_at,omitempty"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// OperationEffect is the maximum impact class of one operation.
type OperationEffect string

const (
	OperationRead         OperationEffect = "read"
	OperationLocalWrite   OperationEffect = "local_write"
	OperationProcess      OperationEffect = "process"
	OperationNetworkRead  OperationEffect = "network_read"
	OperationNetworkWrite OperationEffect = "network_write"
	OperationDestructive  OperationEffect = "destructive"
)

// OperationStatus is the recovery state returned by the uncertain-operation
// endpoint.
type OperationStatus string

const OperationUncertain OperationStatus = "unknown"

// Operation is an interrupted side effect requiring an explicit human
// conclusion before the task may continue.
type Operation struct {
	ID             string          `json:"id"`
	TaskID         string          `json:"task_id"`
	AttemptID      string          `json:"attempt_id"`
	Tool           string          `json:"tool"`
	Input          json.RawMessage `json:"input"`
	InputHash      string          `json:"input_hash"`
	IdempotencyKey string          `json:"idempotency_key"`
	Effect         OperationEffect `json:"effect"`
	Status         OperationStatus `json:"status"`
	OutputSummary  string          `json:"output_summary"`
	ErrorCode      string          `json:"error_code"`
	Recovery       json.RawMessage `json:"recovery,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

// OperationResolution is a human conclusion about an unobservable side
// effect outcome.
type OperationResolution string

const (
	OperationConfirmedSucceeded   OperationResolution = "confirmed_succeeded"
	OperationConfirmedNotExecuted OperationResolution = "confirmed_not_executed"
)

// OperationResolutionReceipt is immutable recovery audit evidence.
type OperationResolutionReceipt struct {
	ID          string              `json:"id"`
	OperationID string              `json:"operation_id"`
	TaskID      string              `json:"task_id"`
	AttemptID   string              `json:"attempt_id"`
	Resolution  OperationResolution `json:"resolution"`
	Actor       string              `json:"actor"`
	DecidedAt   time.Time           `json:"decided_at"`
}

// PluginEntrypoints declares the versioned resources shipped by a plugin.
type PluginEntrypoints struct {
	Knowledge []string `json:"knowledge,omitempty"`
	Workflows []string `json:"workflows,omitempty"`
	Rules     []string `json:"rules,omitempty"`
	Tools     []string `json:"tools,omitempty"`
	Verifiers []string `json:"verifiers,omitempty"`
	Evals     []string `json:"evals,omitempty"`
}

// PluginActivation declares signals used by Kern's task-stage activator.
type PluginActivation struct {
	Signals []string `json:"signals,omitempty"`
	Intents []string `json:"intents,omitempty"`
}

// PluginPermissions is the plugin's maximum requested capability envelope.
type PluginPermissions struct {
	Filesystem []string `json:"filesystem,omitempty"`
	Process    []string `json:"process,omitempty"`
}

// PluginIntegrity contains the package payload digest.
type PluginIntegrity struct {
	Files string `json:"files"`
}

// PluginManifest is the immutable package contract stored by Kern.
type PluginManifest struct {
	SchemaVersion string            `json:"schema_version"`
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Description   string            `json:"description,omitempty"`
	Version       string            `json:"version"`
	Core          string            `json:"core"`
	Entrypoints   PluginEntrypoints `json:"entrypoints"`
	Activation    PluginActivation  `json:"activation"`
	Permissions   PluginPermissions `json:"permissions"`
	Integrity     PluginIntegrity   `json:"integrity"`
}

// Plugin is a public-safe installed plugin snapshot. Kern never exposes the
// private installation directory through the API.
type Plugin struct {
	SchemaVersion string         `json:"schema_version"`
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Description   string         `json:"description,omitempty"`
	Version       string         `json:"version"`
	Source        string         `json:"source"`
	Digest        string         `json:"digest"`
	Enabled       bool           `json:"enabled"`
	TrustStatus   string         `json:"trust_status"`
	Manifest      PluginManifest `json:"manifest"`
	InstalledAt   time.Time      `json:"installed_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

// PluginUsage records the exact package and declarative resources selected for
// one task Attempt.
type PluginUsage struct {
	SchemaVersion string          `json:"schema_version"`
	TaskID        string          `json:"task_id"`
	AttemptID     string          `json:"attempt_id"`
	PluginID      string          `json:"plugin_id"`
	Version       string          `json:"version"`
	Digest        string          `json:"digest"`
	Reason        string          `json:"reason"`
	Resources     json.RawMessage `json:"resources"`
	CreatedAt     time.Time       `json:"created_at"`
}

// InstallPluginInput installs a package from a local directory visible to the
// Kern server and optionally enables it after integrity verification.
type InstallPluginInput struct {
	Source string `json:"source"`
	Enable bool   `json:"enable,omitempty"`
}

// EvaluationStatus is the durable lifecycle of one suite run.
type EvaluationStatus string

const (
	EvaluationQueued    EvaluationStatus = "queued"
	EvaluationRunning   EvaluationStatus = "running"
	EvaluationPaused    EvaluationStatus = "paused"
	EvaluationCompleted EvaluationStatus = "completed"
	EvaluationFailed    EvaluationStatus = "failed"
	EvaluationCancelled EvaluationStatus = "cancelled"
)

// Terminal reports whether an evaluation will no longer advance.
func (s EvaluationStatus) Terminal() bool {
	return s == EvaluationCompleted || s == EvaluationFailed || s == EvaluationCancelled
}

// StartEvaluationInput selects a local suite visible to Kern and its variants.
type StartEvaluationInput struct {
	SuitePath string   `json:"suite_path"`
	Variants  []string `json:"variants,omitempty"`
}

// EvaluationRun is the lightweight durable progress snapshot.
type EvaluationRun struct {
	SchemaVersion  string           `json:"schema_version"`
	ID             string           `json:"id"`
	SuiteID        string           `json:"suite_id"`
	SuiteName      string           `json:"suite_name"`
	SuiteVersion   string           `json:"suite_version"`
	Status         EvaluationStatus `json:"status"`
	Variants       []string         `json:"variants"`
	CaseCount      int              `json:"case_count"`
	CompletedCases int              `json:"completed_cases"`
	ConfigDigest   string           `json:"config_digest,omitempty"`
	ErrorMessage   string           `json:"error_message,omitempty"`
	CreatedAt      time.Time        `json:"created_at"`
	StartedAt      *time.Time       `json:"started_at,omitempty"`
	CompletedAt    *time.Time       `json:"completed_at,omitempty"`
}

type EvaluationUsage struct {
	InputTokens        int   `json:"input_tokens"`
	OutputTokens       int   `json:"output_tokens"`
	CostMicros         int64 `json:"cost_micros"`
	DurationMS         int64 `json:"duration_ms"`
	ToolCalls          int   `json:"tool_calls"`
	Retries            int   `json:"retries"`
	HumanInterventions int   `json:"human_interventions"`
	SafetyViolations   int   `json:"safety_violations"`
}

type EvaluationGrade struct {
	GraderID   string           `json:"grader_id"`
	Status     string           `json:"status"`
	Score      float64          `json:"score"`
	Evidence   []string         `json:"evidence"`
	ReasonCode string           `json:"reason_code"`
	Details    json.RawMessage  `json:"details,omitempty"`
	Usage      *EvaluationUsage `json:"usage,omitempty"`
}

type EvaluationCaseResult struct {
	CaseID      string            `json:"case_id"`
	VariantID   string            `json:"variant_id"`
	TaskID      string            `json:"task_id,omitempty"`
	Attempt     int               `json:"attempt"`
	TaskStatus  string            `json:"task_status"`
	Passed      bool              `json:"passed"`
	Score       float64           `json:"score"`
	Grades      []EvaluationGrade `json:"grades"`
	Usage       EvaluationUsage   `json:"usage"`
	Error       string            `json:"error,omitempty"`
	StartedAt   time.Time         `json:"started_at"`
	CompletedAt time.Time         `json:"completed_at"`
}

type EvaluationConfidence struct {
	Low  float64 `json:"low"`
	High float64 `json:"high"`
}

type EvaluationVariantMetrics struct {
	VariantID          string               `json:"variant_id"`
	Cases              int                  `json:"cases"`
	Passed             int                  `json:"passed"`
	SuccessRate        float64              `json:"success_rate"`
	Confidence95       EvaluationConfidence `json:"confidence_95"`
	MeanScore          float64              `json:"mean_score"`
	TotalInputTokens   int                  `json:"total_input_tokens"`
	TotalOutputTokens  int                  `json:"total_output_tokens"`
	TotalCostMicros    int64                `json:"total_cost_micros"`
	TotalDurationMS    int64                `json:"total_duration_ms"`
	ToolCalls          int                  `json:"tool_calls"`
	Retries            int                  `json:"retries"`
	HumanInterventions int                  `json:"human_interventions"`
	SafetyViolations   int                  `json:"safety_violations"`
}

type EvaluationComparison struct {
	BaselineVariant                   string   `json:"baseline_variant"`
	CandidateVariant                  string   `json:"candidate_variant"`
	PairedCases                       int      `json:"paired_cases"`
	SuccessRateDelta                  float64  `json:"success_rate_delta"`
	MeanScoreDelta                    float64  `json:"mean_score_delta"`
	SuccessRateTest                   string   `json:"success_rate_test"`
	SuccessRatePValue                 float64  `json:"success_rate_p_value"`
	SignificanceAlpha                 float64  `json:"significance_alpha"`
	SuccessRateImprovementSignificant bool     `json:"success_rate_improvement_significant"`
	Improvements                      []string `json:"improvements"`
	Regressions                       []string `json:"regressions"`
	SafetyRegressed                   bool     `json:"safety_regressed"`
}

// EvaluationDefaults fixes the execution conditions shared by every case and
// variant in a reproducible evaluation run.
type EvaluationDefaults struct {
	TimeoutMS        int64                      `json:"timeout_ms"`
	TokenBudget      int                        `json:"token_budget"`
	CostBudgetMicros int64                      `json:"cost_budget_micros"`
	Retries          int                        `json:"retries"`
	WorkerCount      int                        `json:"worker_count"`
	AllowedCommands  []string                   `json:"allowed_commands"`
	Judge            *EvaluationJudgeDefinition `json:"judge,omitempty"`
}

// EvaluationJudgeDefinition identifies the fixed supplemental model grader.
// APIKeyEnv is a secret reference; its value is never returned by Kern.
type EvaluationJudgeDefinition struct {
	Provider  string `json:"provider"`
	BaseURL   string `json:"base_url"`
	Model     string `json:"model"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
	MaxTokens int    `json:"max_tokens"`
}

// EvaluationVariantDefinition identifies the exact agent, model, and plugin
// package bytes used by one evaluation variant.
type EvaluationVariantDefinition struct {
	ID            string            `json:"id"`
	Agent         string            `json:"agent,omitempty"`
	AgentVersion  string            `json:"agent_version,omitempty"`
	Model         string            `json:"model,omitempty"`
	Plugins       []string          `json:"plugins"`
	PluginDigests map[string]string `json:"plugin_digests,omitempty"`
}

// EvaluationInputDigest identifies the exact prompt and initial fixture bytes
// without embedding their potentially large or sensitive contents.
type EvaluationInputDigest struct {
	CaseID        string `json:"case_id"`
	PromptSHA256  string `json:"prompt_sha256"`
	FixtureSHA256 string `json:"fixture_sha256"`
}

// EvaluationReproducibility is the execution identity covered by ConfigDigest.
type EvaluationReproducibility struct {
	CoreVersion string                        `json:"core_version"`
	GoVersion   string                        `json:"go_version"`
	GOOS        string                        `json:"goos"`
	GOARCH      string                        `json:"goarch"`
	Defaults    EvaluationDefaults            `json:"defaults"`
	Variants    []EvaluationVariantDefinition `json:"variants"`
	Inputs      []EvaluationInputDigest       `json:"inputs"`
}

// EvaluationReport is the immutable reproducible outcome of one run.
type EvaluationReport struct {
	SchemaVersion   string                     `json:"schema_version"`
	RunID           string                     `json:"run_id"`
	SuiteID         string                     `json:"suite_id"`
	SuiteVersion    string                     `json:"suite_version"`
	Status          EvaluationStatus           `json:"status"`
	ConfigDigest    string                     `json:"config_digest"`
	Variants        []EvaluationVariantMetrics `json:"variants"`
	Results         []EvaluationCaseResult     `json:"results"`
	Comparisons     []EvaluationComparison     `json:"comparisons,omitempty"`
	Reproducibility EvaluationReproducibility  `json:"reproducibility"`
	StartedAt       time.Time                  `json:"started_at"`
	CompletedAt     time.Time                  `json:"completed_at"`
}
