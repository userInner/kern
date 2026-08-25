export type TaskStatus =
  | 'created'
  | 'planning'
  | 'running'
  | 'waiting_approval'
  | 'waiting_input'
  | 'verifying'
  | 'completed'
  | 'partially_completed'
  | 'failed'
  | 'cancelled'

export interface Task {
  schema_version: string
  id: string
  trace_id: string
  title: string
  goal: string
  status: TaskStatus
  result: string
  error_message: string
  active_attempt_id: string
  model_config_id?: string
  created_at: string
  updated_at: string
  lease_owner: string
  lease_expires_at?: string
  heartbeat_at?: string
  paused_from_status?: TaskStatus
}

export interface TaskEvent {
  schema_version: string
  id: number
  task_id: string
  attempt_id: string
  trace_id: string
  span_id: string
  parent_span_id?: string
  type: string
  payload: unknown
  created_at: string
}

export interface Approval {
  id: string
  task_id: string
  attempt_id: string
  operation_id: string
  scope: unknown
  risk: 'low' | 'medium' | 'high' | 'critical'
  explanation: string
  status: string
  expires_at: string
  created_at: string
}

export interface ApprovalReceipt {
  id: string
  request_id: string
  decision: 'approved' | 'denied'
  actor: string
  scope: unknown
  decided_at: string
}

export interface Artifact {
  schema_version: string
  id: string
  task_id: string
  attempt_id: string
  name: string
  digest: string
  media_type: string
  size: number
  source_operation_id?: string
  created_at: string
}

export interface Verification {
  id: string
  task_id: string
  attempt_id: string
  verifier: string
  status: string
  evidence: unknown
  created_at: string
}

export interface CreateTaskInput {
  goal: string
  title?: string
  model_config_id?: string
  plugins?: TaskPluginSelection
  attachments?: TaskAttachment[]
}

export interface TaskAttachment {
  name: string
  media_type: 'image/png' | 'image/jpeg' | 'image/webp' | 'image/gif'
  /** Raw file bytes encoded as standard base64, without a data-URL prefix. */
  data: string
}

export interface SubmitTaskInput {
  content: string
  attachments?: TaskAttachment[]
}

export interface TaskPluginSelection {
  enable?: string[]
  disable?: string[]
}

export type PolicyProfile = 'local-safe' | 'confirm-all' | 'read-only'

export interface RuntimeSettings {
  schema_version: string
  max_turns: number
  max_tool_calls: number
  max_tokens: number
  max_cost_usd: number
  task_timeout: string
  policy_profile: PolicyProfile
  retention_days: number
}

export interface UpdateSettingsInput {
  max_turns: number
  max_tool_calls: number
  max_tokens: number
  max_cost_usd: number
  task_timeout: string
  policy_profile: PolicyProfile
  retention_days: number
}

export interface CleanupPreview {
  cutoff: string
  retention_days: number
  task_count: number
  artifact_count: number
  artifact_bytes: number
}

export interface CleanupResult extends CleanupPreview {
  removed_objects: number
  removed_bytes: number
}

export interface Health {
  schema_version: string
  status: 'ok' | 'ready'
  mode: string
}

export type ModelProvider = 'openai-compatible' | 'ollama'

export interface ModelConfig {
  schema_version: string
  id: string
  name: string
  provider: ModelProvider
  base_url: string
  model: string
  has_api_key: boolean
  enabled: boolean
  is_default: boolean
  created_at: string
  updated_at: string
}

export interface ModelConfigInput {
  name: string
  provider: ModelProvider
  base_url: string
  model: string
  api_key_env?: string
  /** Write-only value stored directly in the local system credential store. */
  api_key?: string
  clear_api_key?: boolean
  enabled?: boolean
  set_default?: boolean
}

export interface ModelConfigs {
  schema_version: string
  credential_store_available: boolean
  providers: ModelProvider[]
  configs: ModelConfig[]
}

export interface ModelConfigTestResult {
  schema_version: string
  config_id: string
  provider: ModelProvider
  model: string
  ok: boolean
  latency_ms: number
  request_id?: string
  capabilities: Record<string, boolean>
}

export type PlanStatus = 'active' | 'completed' | 'failed' | 'superseded'
export type PlanStepStatus = 'pending' | 'running' | 'completed' | 'failed' | 'skipped'
export type PlanPhase = 'prepare' | 'execute' | 'verify'

export interface PlanStep {
  id: string
  plan_id: string
  ordinal: number
  title: string
  description: string
  phase: PlanPhase
  required: boolean
  status: PlanStepStatus
  failure: string
  retry_count: number
  started_at?: string
  completed_at?: string
  updated_at: string
}

export interface Plan {
  schema_version: string
  id: string
  task_id: string
  attempt_id: string
  revision: number
  status: PlanStatus
  rationale: string
  steps: PlanStep[]
  created_at: string
  updated_at: string
}

export interface Operation {
  id: string
  task_id: string
  attempt_id: string
  tool: string
  input: unknown
  input_hash: string
  idempotency_key: string
  effect: 'read' | 'local_write' | 'process' | 'network_read' | 'network_write' | 'destructive'
  status: 'unknown'
  output_summary: string
  error_code: string
  recovery?: unknown
  created_at: string
  updated_at: string
}

export type OperationResolution = 'confirmed_succeeded' | 'confirmed_not_executed'

export interface OperationResolutionReceipt {
  id: string
  operation_id: string
  task_id: string
  attempt_id: string
  resolution: OperationResolution
  actor: string
  decided_at: string
}

export interface PluginEntrypoints {
  knowledge?: string[]
  workflows?: string[]
  rules?: string[]
  tools?: string[]
  verifiers?: string[]
  evals?: string[]
}

export interface PluginActivation {
  signals?: string[]
  intents?: string[]
}

export interface PluginPermissions {
  filesystem?: string[]
  process?: string[]
}

export interface PluginManifest {
  schema_version: string
  id: string
  name: string
  description?: string
  version: string
  core: string
  entrypoints: PluginEntrypoints
  activation: PluginActivation
  permissions: PluginPermissions
  integrity: { files: string }
}

export interface Plugin {
  schema_version: string
  id: string
  name: string
  description?: string
  version: string
  source: string
  digest: string
  enabled: boolean
  trust_status: string
  manifest: PluginManifest
  installed_at: string
  updated_at: string
}

export interface PluginUsage {
  schema_version: string
  task_id: string
  attempt_id: string
  plugin_id: string
  version: string
  digest: string
  reason: string
  resources: unknown
  created_at: string
}

export interface InstallPluginInput {
  source: string
  enable?: boolean
}

export type EvaluationStatus = 'queued' | 'running' | 'paused' | 'completed' | 'failed' | 'cancelled'

export interface StartEvaluationInput {
  suite_path: string
  variants?: string[]
}

export interface EvaluationRun {
  schema_version: string
  id: string
  suite_id: string
  suite_name: string
  suite_version: string
  status: EvaluationStatus
  variants: string[]
  case_count: number
  completed_cases: number
  config_digest?: string
  error_message?: string
  created_at: string
  started_at?: string
  completed_at?: string
}

export interface EvaluationUsage {
  input_tokens: number
  output_tokens: number
  cost_micros: number
  duration_ms: number
  tool_calls: number
  retries: number
  human_interventions: number
  safety_violations: number
}

export interface EvaluationGrade {
  grader_id: string
  status: 'passed' | 'failed' | 'error' | 'manual_required'
  score: number
  evidence: string[]
  reason_code: string
  details?: unknown
  usage?: EvaluationUsage
}

export interface EvaluationCaseResult {
  case_id: string
  variant_id: string
  task_id?: string
  attempt: number
  task_status: string
  passed: boolean
  score: number
  grades: EvaluationGrade[]
  usage: EvaluationUsage
  error?: string
  started_at: string
  completed_at: string
}

export interface EvaluationVariantMetrics {
  variant_id: string
  cases: number
  passed: number
  success_rate: number
  confidence_95: { low: number; high: number }
  mean_score: number
  total_input_tokens: number
  total_output_tokens: number
  total_cost_micros: number
  total_duration_ms: number
  tool_calls: number
  retries: number
  human_interventions: number
  safety_violations: number
}

export interface EvaluationComparison {
  baseline_variant: string
  candidate_variant: string
  paired_cases: number
  success_rate_delta: number
  mean_score_delta: number
  success_rate_test: 'mcnemar_exact_two_sided'
  success_rate_p_value: number
  significance_alpha: number
  success_rate_improvement_significant: boolean
  improvements: string[]
  regressions: string[]
  safety_regressed: boolean
}

export interface EvaluationDefaults {
  timeout_ms: number
  token_budget: number
  cost_budget_micros: number
  retries: number
  worker_count: number
  allowed_commands: string[]
  judge?: EvaluationJudgeDefinition
}

export interface EvaluationJudgeDefinition {
  provider: 'openai-compatible'
  base_url: string
  model: string
  api_key_env?: string
  max_tokens: number
}

export interface EvaluationVariantDefinition {
  id: string
  agent?: 'kern' | 'codex'
  agent_version?: string
  model?: string
  plugins: string[]
  plugin_digests?: Record<string, string>
}

export interface EvaluationInputDigest {
  case_id: string
  prompt_sha256: string
  fixture_sha256: string
}

export interface EvaluationReproducibility {
  core_version: string
  go_version: string
  goos: string
  goarch: string
  defaults: EvaluationDefaults
  variants: EvaluationVariantDefinition[]
  inputs: EvaluationInputDigest[]
}

export interface EvaluationReport {
  schema_version: string
  run_id: string
  suite_id: string
  suite_version: string
  status: EvaluationStatus
  config_digest: string
  variants: EvaluationVariantMetrics[]
  results: EvaluationCaseResult[]
  comparisons?: EvaluationComparison[]
  reproducibility: EvaluationReproducibility
  started_at: string
  completed_at: string
}

export interface KernClientOptions {
  baseURL: string
  token?: string
  fetch?: typeof globalThis.fetch
}

export interface WatchOptions {
  after?: number
  signal?: AbortSignal
  reconnectDelayMs?: number
}

export interface DownloadedArtifact {
  content: ArrayBuffer
  contentType: string
  disposition: string
  etag: string
}

export class KernAPIError extends Error {
  readonly status: number

  constructor(status: number, message: string) {
    super(`Kern API returned ${status}: ${message}`)
    this.name = 'KernAPIError'
    this.status = status
  }
}

export class KernClient {
  readonly #baseURL: string
  readonly #token: string
  readonly #fetch: typeof globalThis.fetch

  constructor(options: KernClientOptions) {
    const parsed = new URL(options.baseURL)
    if ((parsed.protocol !== 'http:' && parsed.protocol !== 'https:') || !parsed.host) {
      throw new TypeError('Kern baseURL must be an HTTP or HTTPS origin')
    }
    if (parsed.username || parsed.password || parsed.search || parsed.hash) {
      throw new TypeError('Kern baseURL must not contain credentials, query, or fragment')
    }
    this.#baseURL = parsed.toString().replace(/\/$/, '')
    this.#token = options.token?.trim() ?? ''
    this.#fetch = options.fetch ?? globalThis.fetch
    if (!this.#fetch) throw new TypeError('KernClient requires a Fetch implementation')
  }

  createTask(input: CreateTaskInput, signal?: AbortSignal): Promise<Task> {
    return this.#json('/api/v1/tasks', { method: 'POST', body: JSON.stringify(input), signal: signal ?? null })
  }

  async listTasks(signal?: AbortSignal): Promise<Task[]> {
    const response = await this.#json<{ tasks: Task[] }>('/api/v1/tasks', { signal: signal ?? null })
    return response.tasks
  }

  getTask(taskID: string, signal?: AbortSignal): Promise<Task> {
    return this.#json(this.#taskPath(taskID), { signal: signal ?? null })
  }

  health(signal?: AbortSignal): Promise<Health> {
    return this.#json('/api/v1/health', { signal: signal ?? null })
  }

  ready(signal?: AbortSignal): Promise<Health> {
    return this.#json('/api/v1/ready', { signal: signal ?? null })
  }

  getSettings(signal?: AbortSignal): Promise<RuntimeSettings> {
    return this.#json('/api/v1/settings', { signal: signal ?? null })
  }

  updateSettings(input: UpdateSettingsInput, signal?: AbortSignal): Promise<RuntimeSettings> {
    return this.#json('/api/v1/settings', {
      method: 'PUT',
      body: JSON.stringify(input),
      signal: signal ?? null,
    })
  }

  previewCleanup(signal?: AbortSignal): Promise<CleanupPreview> {
    return this.#json('/api/v1/settings/cleanup-preview', { signal: signal ?? null })
  }

  cleanupExpired(signal?: AbortSignal): Promise<CleanupResult> {
    return this.#json('/api/v1/settings/cleanup', {
      method: 'POST',
      body: JSON.stringify({ confirm: true }),
      signal: signal ?? null,
    })
  }

  listModelConfigs(signal?: AbortSignal): Promise<ModelConfigs> {
    return this.#json('/api/v1/models', { signal: signal ?? null })
  }

  createModelConfig(input: ModelConfigInput, signal?: AbortSignal): Promise<ModelConfig> {
    return this.#json('/api/v1/model-configs', {
      method: 'POST',
      body: JSON.stringify(input),
      signal: signal ?? null,
    })
  }

  updateModelConfig(configID: string, input: ModelConfigInput, signal?: AbortSignal): Promise<ModelConfig> {
    return this.#json(this.#modelConfigPath(configID), {
      method: 'PUT',
      body: JSON.stringify(input),
      signal: signal ?? null,
    })
  }

  async deleteModelConfig(configID: string, signal?: AbortSignal): Promise<void> {
    await this.#request(this.#modelConfigPath(configID), { method: 'DELETE', signal: signal ?? null })
  }

  testModelConfig(configID: string, signal?: AbortSignal): Promise<ModelConfigTestResult> {
    return this.#json(`${this.#modelConfigPath(configID)}/test`, {
      method: 'POST',
      signal: signal ?? null,
    })
  }

  pause(taskID: string, signal?: AbortSignal): Promise<Task> {
    return this.#taskAction(taskID, 'pause', signal)
  }

  resume(taskID: string, signal?: AbortSignal): Promise<Task> {
    return this.#taskAction(taskID, 'resume', signal)
  }

  cancel(taskID: string, signal?: AbortSignal): Promise<Task> {
    return this.#taskAction(taskID, 'cancel', signal)
  }

  retry(taskID: string, signal?: AbortSignal): Promise<Task> {
    return this.#taskAction(taskID, 'retry', signal)
  }

  submitInput(taskID: string, content: string, signal?: AbortSignal): Promise<Task> {
    return this.submitTaskInput(taskID, { content }, signal)
  }

  submitTaskInput(taskID: string, input: SubmitTaskInput, signal?: AbortSignal): Promise<Task> {
    return this.#json(`${this.#taskPath(taskID)}/messages`, {
      method: 'POST',
      body: JSON.stringify(input),
      signal: signal ?? null,
    })
  }

  async pendingApprovals(taskID: string, signal?: AbortSignal): Promise<Approval[]> {
    const response = await this.#json<{ approvals: Approval[] }>(
      `${this.#taskPath(taskID)}/approvals`,
      { signal: signal ?? null },
    )
    return response.approvals
  }

  decideApproval(
    requestID: string,
    decision: 'approved' | 'denied',
    signal?: AbortSignal,
  ): Promise<ApprovalReceipt> {
    return this.#json(`/api/v1/approvals/${encodeURIComponent(requestID)}/decision`, {
      method: 'POST',
      body: JSON.stringify({ decision }),
      signal: signal ?? null,
    })
  }

  async listArtifacts(taskID: string, signal?: AbortSignal): Promise<Artifact[]> {
    const response = await this.#json<{ artifacts: Artifact[] }>(
      `${this.#taskPath(taskID)}/artifacts`,
      { signal: signal ?? null },
    )
    return response.artifacts
  }

  async downloadArtifact(
    taskID: string,
    artifactID: string,
    signal?: AbortSignal,
  ): Promise<DownloadedArtifact> {
    const response = await this.#request(
      `${this.#taskPath(taskID)}/artifacts/${encodeURIComponent(artifactID)}`,
      { signal: signal ?? null },
    )
    return {
      content: await response.arrayBuffer(),
      contentType: response.headers.get('content-type') ?? 'application/octet-stream',
      disposition: response.headers.get('content-disposition') ?? '',
      etag: response.headers.get('etag') ?? '',
    }
  }

  async listVerifications(taskID: string, signal?: AbortSignal): Promise<Verification[]> {
    const response = await this.#json<{ verifications: Verification[] }>(
      `${this.#taskPath(taskID)}/verifications`,
      { signal: signal ?? null },
    )
    return response.verifications
  }

  async listTaskPlugins(taskID: string, signal?: AbortSignal): Promise<PluginUsage[]> {
    const response = await this.#json<{ plugins: PluginUsage[] }>(
      `${this.#taskPath(taskID)}/plugins`,
      { signal: signal ?? null },
    )
    return response.plugins
  }

  async getPlan(taskID: string, signal?: AbortSignal): Promise<Plan | null> {
    const response = await this.#json<{ plan: Plan | null }>(
      `${this.#taskPath(taskID)}/plan`,
      { signal: signal ?? null },
    )
    return response.plan
  }

  async listUncertainOperations(taskID: string, signal?: AbortSignal): Promise<Operation[]> {
    const response = await this.#json<{ operations: Operation[] }>(
      `${this.#taskPath(taskID)}/operations/uncertain`,
      { signal: signal ?? null },
    )
    return response.operations
  }

  resolveUncertainOperation(
    operationID: string,
    resolution: OperationResolution,
    signal?: AbortSignal,
  ): Promise<OperationResolutionReceipt> {
    return this.#json(`${this.#operationPath(operationID)}/resolution`, {
      method: 'POST',
      body: JSON.stringify({ resolution }),
      signal: signal ?? null,
    })
  }

  async listPlugins(signal?: AbortSignal): Promise<Plugin[]> {
    const response = await this.#json<{ plugins: Plugin[] }>(
      '/api/v1/plugins',
      { signal: signal ?? null },
    )
    return response.plugins
  }

  getPlugin(pluginID: string, signal?: AbortSignal): Promise<Plugin> {
    return this.#json(this.#pluginPath(pluginID), { signal: signal ?? null })
  }

  installPlugin(input: InstallPluginInput, signal?: AbortSignal): Promise<Plugin> {
    return this.#json('/api/v1/plugins/install', {
      method: 'POST',
      body: JSON.stringify(input),
      signal: signal ?? null,
    })
  }

  enablePlugin(pluginID: string, signal?: AbortSignal): Promise<Plugin> {
    return this.#pluginAction(pluginID, 'enable', signal)
  }

  disablePlugin(pluginID: string, signal?: AbortSignal): Promise<Plugin> {
    return this.#pluginAction(pluginID, 'disable', signal)
  }

  async removePlugin(pluginID: string, signal?: AbortSignal): Promise<void> {
    await this.#request(this.#pluginPath(pluginID), {
      method: 'DELETE',
      signal: signal ?? null,
    })
  }

  startEvaluation(input: StartEvaluationInput, signal?: AbortSignal): Promise<EvaluationRun> {
    return this.#json('/api/v1/evals/runs', {
      method: 'POST',
      body: JSON.stringify(input),
      signal: signal ?? null,
    })
  }

  async listEvaluationRuns(limit = 50, signal?: AbortSignal): Promise<EvaluationRun[]> {
    if (!Number.isSafeInteger(limit) || limit < 1 || limit > 200) {
      throw new RangeError('evaluation limit must be between 1 and 200')
    }
    const response = await this.#json<{ runs: EvaluationRun[] }>(
      `/api/v1/evals/runs?limit=${limit}`,
      { signal: signal ?? null },
    )
    return response.runs
  }

  getEvaluationRun(runID: string, signal?: AbortSignal): Promise<EvaluationRun> {
    return this.#json(this.#evaluationPath(runID), { signal: signal ?? null })
  }

  pauseEvaluation(runID: string, signal?: AbortSignal): Promise<EvaluationRun> {
    return this.#evaluationAction(runID, 'pause', signal)
  }

  resumeEvaluation(runID: string, signal?: AbortSignal): Promise<EvaluationRun> {
    return this.#evaluationAction(runID, 'resume', signal)
  }

  cancelEvaluation(runID: string, signal?: AbortSignal): Promise<EvaluationRun> {
    return this.#evaluationAction(runID, 'cancel', signal)
  }

  #evaluationAction(runID: string, action: string, signal?: AbortSignal): Promise<EvaluationRun> {
    return this.#json(`${this.#evaluationPath(runID)}/${action}`, {
      method: 'POST',
      signal: signal ?? null,
    })
  }

  getEvaluationReport(runID: string, signal?: AbortSignal): Promise<EvaluationReport> {
    return this.#json(`${this.#evaluationPath(runID)}/report`, { signal: signal ?? null })
  }

  async metrics(signal?: AbortSignal): Promise<string> {
    const response = await this.#request('/metrics', {
      headers: { Accept: 'text/plain' },
      signal: signal ?? null,
    })
    return response.text()
  }

  async *watchEvents(taskID: string, options: WatchOptions = {}): AsyncGenerator<TaskEvent> {
    let after = options.after ?? 0
    if (!Number.isSafeInteger(after) || after < 0) throw new RangeError('event cursor must be non-negative')
    const delay = options.reconnectDelayMs ?? 250
    if (!Number.isFinite(delay) || delay < 0) throw new RangeError('reconnect delay must be non-negative')
    while (!options.signal?.aborted) {
      const response = await this.#request(`${this.#taskPath(taskID)}/events`, {
        headers: { Accept: 'text/event-stream', 'Last-Event-ID': String(after) },
        signal: options.signal ?? null,
      })
      if (!response.body) throw new Error('Kern event response has no body')
      for await (const event of parseEventStream(response.body)) {
        if (event.id <= after) continue
        after = event.id
        yield event
      }
      await abortableDelay(delay, options.signal)
    }
  }

  #taskAction(taskID: string, action: string, signal?: AbortSignal): Promise<Task> {
    return this.#json(`${this.#taskPath(taskID)}/${action}`, {
      method: 'POST',
      signal: signal ?? null,
    })
  }

  #taskPath(taskID: string): string {
    if (!taskID) throw new TypeError('taskID is required')
    return `/api/v1/tasks/${encodeURIComponent(taskID)}`
  }

  #pluginAction(pluginID: string, action: 'enable' | 'disable', signal?: AbortSignal): Promise<Plugin> {
    return this.#json(`${this.#pluginPath(pluginID)}/${action}`, {
      method: 'POST',
      signal: signal ?? null,
    })
  }

  #pluginPath(pluginID: string): string {
    if (!pluginID) throw new TypeError('pluginID is required')
    return `/api/v1/plugins/${encodeURIComponent(pluginID)}`
  }

  #modelConfigPath(configID: string): string {
    if (!configID) throw new TypeError('configID is required')
    return `/api/v1/model-configs/${encodeURIComponent(configID)}`
  }

  #operationPath(operationID: string): string {
    if (!operationID) throw new TypeError('operationID is required')
    return `/api/v1/operations/${encodeURIComponent(operationID)}`
  }

  #evaluationPath(runID: string): string {
    if (!runID) throw new TypeError('runID is required')
    return `/api/v1/evals/runs/${encodeURIComponent(runID)}`
  }

  async #json<T>(path: string, init: RequestInit = {}): Promise<T> {
    const response = await this.#request(path, init)
    return (await response.json()) as T
  }

  async #request(path: string, init: RequestInit): Promise<Response> {
    const headers = new Headers(init.headers)
    headers.set('Accept', headers.get('Accept') ?? 'application/json')
    if (init.body) headers.set('Content-Type', 'application/json')
    if (this.#token) headers.set('Authorization', `Bearer ${this.#token}`)
    const response = await this.#fetch(`${this.#baseURL}${path}`, { ...init, headers })
    if (!response.ok) {
      let message = response.statusText
      try {
        const body = (await response.json()) as { error?: string }
        if (body.error) message = body.error
      } catch {
        // The status text is the safe fallback for non-JSON responses.
      }
      throw new KernAPIError(response.status, message)
    }
    return response
  }
}

async function* parseEventStream(stream: ReadableStream<Uint8Array>): AsyncGenerator<TaskEvent> {
  const reader = stream.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  try {
    while (true) {
      const { done, value } = await reader.read()
      buffer += decoder.decode(value, { stream: !done })
      let boundary = buffer.indexOf('\n\n')
      while (boundary >= 0) {
        const frame = buffer.slice(0, boundary)
        buffer = buffer.slice(boundary + 2)
        const data = frame
          .split('\n')
          .filter((line) => line.startsWith('data:'))
          .map((line) => line.slice(5).replace(/^ /, ''))
          .join('\n')
        if (data) yield JSON.parse(data) as TaskEvent
        boundary = buffer.indexOf('\n\n')
      }
      if (done) return
    }
  } finally {
    reader.releaseLock()
  }
}

function abortableDelay(milliseconds: number, signal?: AbortSignal): Promise<void> {
  if (signal?.aborted) return Promise.reject(signal.reason)
  return new Promise((resolve, reject) => {
    const timeout = setTimeout(resolve, milliseconds)
    signal?.addEventListener(
      'abort',
      () => {
        clearTimeout(timeout)
        reject(signal.reason)
      },
      { once: true },
    )
  })
}
