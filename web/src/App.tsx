import { ChangeEvent, FormEvent, useCallback, useEffect, useMemo, useRef, useState } from 'react'

type Status =
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

type Task = {
  id: string
  trace_id: string
  title: string
  goal: string
  status: Status
  result: string
  error_message: string
  model_config_id?: string
  created_at: string
  updated_at: string
}

type TaskEvent = {
  id: number
  trace_id: string
  span_id: string
  parent_span_id?: string
  type: string
  payload: Record<string, unknown>
  created_at: string
}

type Health = {
  status: string
  mode: string
}

type ModelProvider = 'openai-compatible' | 'ollama'

type ModelConfig = {
  id: string
  name: string
  provider: ModelProvider
  base_url: string
  model: string
  has_api_key: boolean
  enabled: boolean
  is_default: boolean
}

type ModelConfigForm = {
  name: string
  provider: ModelProvider
  baseURL: string
  model: string
  apiKey: string
  apiKeyEnv: string
  setDefault: boolean
}

type PolicyProfile = 'local-safe' | 'confirm-all' | 'read-only'

type RuntimeSettings = {
  schema_version: string
  max_turns: number
  max_tool_calls: number
  max_tokens: number
  max_cost_usd: number
  task_timeout: string
  policy_profile: PolicyProfile
  retention_days: number
}

type CleanupPreview = {
  cutoff: string
  retention_days: number
  task_count: number
  artifact_count: number
  artifact_bytes: number
}

type CleanupResult = CleanupPreview & {
  removed_objects: number
  removed_bytes: number
}

const initialRuntimeSettings: RuntimeSettings = {
  schema_version: '1',
  max_turns: 12,
  max_tool_calls: 32,
  max_tokens: 200_000,
  max_cost_usd: 5,
  task_timeout: '10m',
  policy_profile: 'local-safe',
  retention_days: 90,
}

const emptyModelConfigForm: ModelConfigForm = {
  name: '',
  provider: 'openai-compatible',
  baseURL: '',
  model: '',
  apiKey: '',
  apiKeyEnv: '',
  setDefault: true,
}

type ApprovalRequest = {
  id: string
  operation_id: string
  scope: Record<string, unknown>
  risk: 'low' | 'medium' | 'high' | 'critical'
  explanation: string
  expires_at: string
}

type Artifact = {
  id: string
  name: string
  digest: string
  media_type: string
  size: number
  source_operation_id?: string
  created_at: string
}

type TaskAttachmentInput = {
  name: string
  media_type: 'image/png' | 'image/jpeg' | 'image/webp' | 'image/gif'
  data: string
}

type UncertainOperation = {
  id: string
  tool: string
  input: unknown
  input_hash: string
  idempotency_key: string
  effect: string
  created_at: string
}

type PlanStep = {
  id: string
  ordinal: number
  title: string
  description: string
  phase: 'prepare' | 'execute' | 'verify'
  required: boolean
  status: 'pending' | 'running' | 'completed' | 'failed' | 'skipped'
  failure: string
  retry_count: number
}

type ExecutionPlan = {
  id: string
  revision: number
  status: 'active' | 'completed' | 'failed' | 'superseded'
  rationale: string
  steps: PlanStep[]
}

type VerificationStatus = 'passed' | 'failed' | 'partial' | 'manual_required' | 'not_run' | 'verified'

type VerificationEvidence = {
  kind: string
  ref: string
  summary: string
  digest?: string
}

type VerificationRecord = {
  id: string
  verifier: string
  status: VerificationStatus
  evidence: {
    required?: boolean
    summary?: string
    evidence?: VerificationEvidence[]
    check_ids?: string[]
    checks?: number
  }
  created_at: string
}

type PluginEntrypoints = {
  knowledge?: string[]
  workflows?: string[]
  rules?: string[]
  tools?: string[]
  verifiers?: string[]
  evals?: string[]
}

type PluginManifest = {
  schema_version: string
  id: string
  name: string
  description?: string
  version: string
  core: string
  entrypoints: PluginEntrypoints
  activation: {
    signals?: string[]
    intents?: string[]
  }
  permissions: {
    filesystem?: string[]
    process?: string[]
  }
}

type InstalledPlugin = {
  id: string
  name: string
  description?: string
  version: string
  source: string
  digest: string
  enabled: boolean
  trust_status: 'local-unverified' | 'verified' | 'revoked'
  manifest: PluginManifest
  installed_at: string
  updated_at: string
}

type PluginResources = {
  knowledge?: string[]
  workflows?: string[]
  rules?: string[]
  verifiers?: string[]
}

type PluginUsage = {
  plugin_id: string
  version: string
  digest: string
  reason: string
  resources: PluginResources
  created_at: string
}

type PluginPreference = 'auto' | 'enable' | 'disable'

type EvaluationStatus = 'queued' | 'running' | 'paused' | 'completed' | 'failed' | 'cancelled'

type EvaluationRun = {
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

type EvaluationVariantMetrics = {
  variant_id: string
  cases: number
  passed: number
  success_rate: number
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

type EvaluationCaseResult = {
  case_id: string
  variant_id: string
  attempt: number
  passed: boolean
  score: number
  error?: string
}

type EvaluationReproducibility = {
  core_version: string
  go_version: string
  goos: string
  goarch: string
  defaults: {
    timeout_ms: number
    token_budget: number
    cost_budget_micros: number
    retries: number
    worker_count: number
    allowed_commands: string[]
    judge?: {
      provider: string
      base_url: string
      model: string
      api_key_env?: string
      max_tokens: number
    }
  }
  variants: Array<{
    id: string
    agent?: string
    agent_version?: string
    model?: string
    plugins: string[]
    plugin_digests?: Record<string, string>
  }>
  inputs: Array<{
    case_id: string
    prompt_sha256: string
    fixture_sha256: string
  }>
}

type EvaluationReport = {
  run_id: string
  config_digest: string
  variants: EvaluationVariantMetrics[]
  results: EvaluationCaseResult[]
  comparisons?: Array<{
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
  }>
  reproducibility: EvaluationReproducibility
}

const eventTypes = [
  'task.created',
  'task.status_changed',
  'task.plan_updated',
  'task.plan_skipped',
  'task.step_updated',
  'message.completed',
  'message.delta',
  'reasoning.delta',
  'model.usage',
  'model.selected',
  'model.retry',
  'task.budget_exhausted',
  'context.message_saved',
  'context.built',
  'context.summary_created',
  'operation.proposed',
  'operation.recovery_prepared',
  'operation.started',
  'operation.output',
  'operation.status_changed',
  'operation.completed',
  'operation.unknown',
  'operation.resolved',
  'operation.reconciliation_failed',
  'operation.recovery_conflict',
  'approval.requested',
  'approval.decided',
  'approval.expired',
  'approval.cancelled',
  'artifact.created',
  'verification.started',
  'verification.completed',
  'task.completed',
  'task.failed',
  'task.paused',
  'task.cancelled',
  'task.attempt_created',
  'task.checkpoint_saved',
  'task.recovery_blocked',
  'plugin.activated',
  'plugin.activation_skipped',
  'plugin.activation_failed',
  'plugin.signal_scan_failed',
]

const browserTokenStorageKey = 'kern.browser-session.v1'
let browserToken = ''

function readBrowserToken(): string {
  if (browserToken) return browserToken
  if (typeof document === 'undefined') return ''
  const bootstrap = document.querySelector<HTMLMetaElement>('meta[name="kern-session-token"]')
  const injected = bootstrap?.content.trim() ?? ''
  if (injected) {
    browserToken = injected
    bootstrap?.remove()
    try {
      window.sessionStorage.setItem(browserTokenStorageKey, injected)
    } catch {
      // Memory-only authentication remains available when storage is disabled.
    }
    return browserToken
  }
  try {
    browserToken = window.sessionStorage.getItem(browserTokenStorageKey) ?? ''
  } catch {
    browserToken = ''
  }
  return browserToken
}

export function authorizedRequestInit(token: string, init: RequestInit = {}): RequestInit {
  const headers = new Headers(init.headers)
  if (token) headers.set('Authorization', `Bearer ${token}`)
  return { ...init, credentials: 'omit', headers }
}

function apiFetch(input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> {
  return globalThis.fetch(input, authorizedRequestInit(readBrowserToken(), init))
}

export async function fetchAuthorizedDownload(
  input: RequestInfo | URL,
  token: string,
  fetcher: typeof globalThis.fetch = globalThis.fetch,
): Promise<Blob> {
  const response = await fetcher(input, authorizedRequestInit(token))
  if (!response.ok) throw new Error('文件下载失败')
  return response.blob()
}

async function downloadResource(input: RequestInfo | URL, filename: string): Promise<void> {
  const blob = await fetchAuthorizedDownload(input, readBrowserToken())
  const objectURL = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = objectURL
  link.download = filename
  link.hidden = true
  document.body.appendChild(link)
  try {
    link.click()
  } finally {
    link.remove()
    window.setTimeout(() => URL.revokeObjectURL(objectURL), 0)
  }
}

export type ParsedServerSentEvent = {
  id: string
  event: string
  data: string
}

export function parseSSEFrames(input: string): {
  events: ParsedServerSentEvent[]
  remainder: string
} {
  const events: ParsedServerSentEvent[] = []
  let remainder = input
  while (true) {
    const separator = /\r?\n\r?\n/.exec(remainder)
    if (!separator || separator.index === undefined) break
    const frame = remainder.slice(0, separator.index)
    remainder = remainder.slice(separator.index + separator[0].length)
    let id = ''
    let event = 'message'
    const data: string[] = []
    for (const line of frame.split(/\r?\n/)) {
      if (!line || line.startsWith(':')) continue
      const colon = line.indexOf(':')
      const field = colon < 0 ? line : line.slice(0, colon)
      let value = colon < 0 ? '' : line.slice(colon + 1)
      if (value.startsWith(' ')) value = value.slice(1)
      if (field === 'id' && !value.includes('\0')) id = value
      if (field === 'event') event = value
      if (field === 'data') data.push(value)
    }
    if (data.length > 0) events.push({ id, event, data: data.join('\n') })
  }
  return { events, remainder }
}

async function consumeEventStream(
  response: Response,
  signal: AbortSignal,
  onEvent: (event: ParsedServerSentEvent) => void,
): Promise<void> {
  if (!response.body) throw new Error('事件流不可用')
  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffered = ''
  try {
    while (!signal.aborted) {
      const { done, value } = await reader.read()
      buffered += decoder.decode(value, { stream: !done })
      if (done) {
        const parsed = parseSSEFrames(buffered + '\n\n')
        parsed.events.forEach(onEvent)
        return
      }
      const parsed = parseSSEFrames(buffered)
      buffered = parsed.remainder
      parsed.events.forEach(onEvent)
    }
  } finally {
    await reader.cancel().catch(() => undefined)
  }
}

function waitForReconnect(signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (signal.aborted) {
      resolve()
      return
    }
    const timer = window.setTimeout(resolve, 500)
    signal.addEventListener('abort', () => {
      window.clearTimeout(timer)
      resolve()
    }, { once: true })
  })
}

async function streamTaskEvents(
  taskID: string,
  signal: AbortSignal,
  cursor: () => number,
  onEvent: (event: ParsedServerSentEvent) => void,
): Promise<void> {
  while (!signal.aborted) {
    try {
      const response = await apiFetch(`/api/v1/tasks/${encodeURIComponent(taskID)}/events`, {
        headers: {
          Accept: 'text/event-stream',
          'Last-Event-ID': String(cursor()),
        },
        signal,
      })
      if (!response.ok) throw new Error('事件流连接失败')
      await consumeEventStream(response, signal, onEvent)
    } catch (cause) {
      if (signal.aborted) return
      if (cause instanceof DOMException && cause.name === 'AbortError') return
    }
    await waitForReconnect(signal)
  }
}

type TaskAction = 'pause' | 'cancel' | 'resume' | 'retry'

const actionLabels: Record<TaskAction, string> = {
  pause: '暂停',
  cancel: '取消',
  resume: '继续',
  retry: '重新尝试',
}

const statusLabels: Record<Status, string> = {
  created: '已入队',
  planning: '规划中',
  running: '执行中',
  waiting_approval: '等待授权',
  waiting_input: '等待输入',
  verifying: '验证中',
  completed: '已完成',
  partially_completed: '部分完成',
  failed: '失败',
  cancelled: '已取消',
}

function App() {
  const [health, setHealth] = useState<Health | null>(null)
  const [tasks, setTasks] = useState<Task[]>([])
  const [modelConfigs, setModelConfigs] = useState<ModelConfig[]>([])
  const [credentialStoreAvailable, setCredentialStoreAvailable] = useState(false)
  const [selectedModelID, setSelectedModelID] = useState('')
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [pluginsOpen, setPluginsOpen] = useState(false)
  const [evalsOpen, setEvalsOpen] = useState(false)
  const [modelForm, setModelForm] = useState<ModelConfigForm>(emptyModelConfigForm)
  const [savingModel, setSavingModel] = useState(false)
  const [deletingModelID, setDeletingModelID] = useState('')
  const [testingModelID, setTestingModelID] = useState('')
  const [modelTestResults, setModelTestResults] = useState<Record<string, string>>({})
  const [runtimeSettings, setRuntimeSettings] = useState<RuntimeSettings>(initialRuntimeSettings)
  const [settingsDraft, setSettingsDraft] = useState<RuntimeSettings>(initialRuntimeSettings)
  const [savingSettings, setSavingSettings] = useState(false)
  const [settingsNotice, setSettingsNotice] = useState('')
  const [cleanupPreview, setCleanupPreview] = useState<CleanupPreview | null>(null)
  const [cleaningData, setCleaningData] = useState(false)
  const [selectedID, setSelectedID] = useState<string>('')
  const [selected, setSelected] = useState<Task | null>(null)
  const [events, setEvents] = useState<TaskEvent[]>([])
  const [approvals, setApprovals] = useState<ApprovalRequest[]>([])
  const [artifacts, setArtifacts] = useState<Artifact[]>([])
  const [uncertainOperations, setUncertainOperations] = useState<UncertainOperation[]>([])
  const [currentPlan, setCurrentPlan] = useState<ExecutionPlan | null>(null)
  const [verifications, setVerifications] = useState<VerificationRecord[]>([])
  const [plugins, setPlugins] = useState<InstalledPlugin[]>([])
  const [taskPlugins, setTaskPlugins] = useState<PluginUsage[]>([])
  const [pluginPreferences, setPluginPreferences] = useState<Record<string, PluginPreference>>({})
  const [pluginSource, setPluginSource] = useState('')
  const [enableInstalledPlugin, setEnableInstalledPlugin] = useState(true)
  const [installingPlugin, setInstallingPlugin] = useState(false)
  const [mutatingPluginID, setMutatingPluginID] = useState('')
  const [evalRuns, setEvalRuns] = useState<EvaluationRun[]>([])
  const [selectedEvalID, setSelectedEvalID] = useState('')
  const [selectedEval, setSelectedEval] = useState<EvaluationRun | null>(null)
  const [evalReport, setEvalReport] = useState<EvaluationReport | null>(null)
  const [evalSuitePath, setEvalSuitePath] = useState('')
  const [evalVariants, setEvalVariants] = useState({ base: true, expert: true })
  const [startingEval, setStartingEval] = useState(false)
  const [evalAction, setEvalAction] = useState<'pause' | 'resume' | 'cancel' | ''>('')
  const [artifactDiffs, setArtifactDiffs] = useState<Record<string, string>>({})
  const [goal, setGoal] = useState('')
  const [attachments, setAttachments] = useState<File[]>([])
  const [isSubmitting, setIsSubmitting] = useState(false)
  const [activeAction, setActiveAction] = useState<TaskAction | null>(null)
  const [activeApproval, setActiveApproval] = useState<string>('')
  const [activeResolution, setActiveResolution] = useState<string>('')
  const [error, setError] = useState('')
  const lastEventID = useRef(0)

  const loadTasks = useCallback(async () => {
    const response = await apiFetch('/api/v1/tasks')
    if (!response.ok) throw new Error('无法读取任务列表')
    const body = (await response.json()) as { tasks: Task[] }
    setTasks(body.tasks)
    setSelectedID((current) => body.tasks.some((item) => item.id === current) ? current : body.tasks[0]?.id || '')
  }, [])

  const loadModels = useCallback(async () => {
    const response = await apiFetch('/api/v1/models')
    if (!response.ok) throw new Error('无法读取模型设置')
    const body = (await response.json()) as { configs: ModelConfig[], credential_store_available: boolean }
    setModelConfigs(body.configs)
    setCredentialStoreAvailable(body.credential_store_available)
    setSelectedModelID((current) => {
      if (body.configs.some((config) => config.id === current && config.enabled)) return current
      return body.configs.find((config) => config.is_default && config.enabled)?.id
        ?? body.configs.find((config) => config.enabled)?.id
        ?? ''
    })
  }, [])

  const loadSettings = useCallback(async () => {
    const response = await apiFetch('/api/v1/settings')
    if (!response.ok) throw new Error(await responseError(response, '无法读取运行设置'))
    const body = (await response.json()) as RuntimeSettings
    setRuntimeSettings(body)
    setSettingsDraft(body)
  }, [])

  const loadCleanupPreview = useCallback(async () => {
    const response = await apiFetch('/api/v1/settings/cleanup-preview')
    if (!response.ok) throw new Error(await responseError(response, '无法读取数据清理预览'))
    setCleanupPreview((await response.json()) as CleanupPreview)
  }, [])

  const loadPlugins = useCallback(async () => {
    const response = await apiFetch('/api/v1/plugins')
    if (!response.ok) throw new Error('无法读取插件列表')
    const body = (await response.json()) as { plugins: InstalledPlugin[] }
    setPlugins(body.plugins)
    setPluginPreferences((current) => Object.fromEntries(
      Object.entries(current).filter(([pluginID]) => body.plugins.some((item) => item.id === pluginID)),
    ))
  }, [])

  const loadEvalRuns = useCallback(async () => {
    const response = await apiFetch('/api/v1/evals/runs?limit=50')
    if (!response.ok) throw new Error('无法读取评测记录')
    const body = (await response.json()) as { runs: EvaluationRun[] }
    setEvalRuns(body.runs)
    setSelectedEvalID((current) => current || body.runs[0]?.id || '')
  }, [])

  const loadEval = useCallback(async (runID: string) => {
    const response = await apiFetch(`/api/v1/evals/runs/${encodeURIComponent(runID)}`)
    if (!response.ok) throw new Error('无法读取评测状态')
    const run = (await response.json()) as EvaluationRun
    setSelectedEval(run)
    if (run.status === 'completed') {
      const reportResponse = await apiFetch(`/api/v1/evals/runs/${encodeURIComponent(runID)}/report`)
      if (!reportResponse.ok) throw new Error('无法读取评测报告')
      setEvalReport((await reportResponse.json()) as EvaluationReport)
    } else {
      setEvalReport(null)
    }
  }, [])

  const loadTask = useCallback(async (taskID: string) => {
    const response = await apiFetch(`/api/v1/tasks/${taskID}`)
    if (!response.ok) throw new Error('无法读取任务')
    setSelected((await response.json()) as Task)
  }, [])

  const loadApprovals = useCallback(async (taskID: string) => {
    const response = await apiFetch(`/api/v1/tasks/${taskID}/approvals`)
    if (!response.ok) throw new Error('无法读取授权请求')
    const body = (await response.json()) as { approvals: ApprovalRequest[] }
    setApprovals(body.approvals)
  }, [])

  const loadArtifacts = useCallback(async (taskID: string) => {
    const response = await apiFetch(`/api/v1/tasks/${taskID}/artifacts`)
    if (!response.ok) throw new Error('无法读取任务产物')
    const body = (await response.json()) as { artifacts: Artifact[] }
    setArtifacts(body.artifacts)
		const diffArtifacts = body.artifacts.filter((item) => item.media_type === 'text/x-diff')
		const loadedDiffs = await Promise.all(diffArtifacts.map(async (item) => {
			const contentResponse = await apiFetch(`/api/v1/tasks/${taskID}/artifacts/${item.id}`)
			if (!contentResponse.ok) throw new Error(`无法读取差异产物 ${item.name}`)
			return [item.id, await contentResponse.text()] as const
		}))
		setArtifactDiffs(Object.fromEntries(loadedDiffs))
  }, [])

  const loadUncertainOperations = useCallback(async (taskID: string) => {
    const response = await apiFetch(`/api/v1/tasks/${taskID}/operations/uncertain`)
    if (!response.ok) throw new Error('无法读取待确认的中断操作')
    const body = (await response.json()) as { operations: UncertainOperation[] }
    setUncertainOperations(body.operations)
  }, [])

  const loadCurrentPlan = useCallback(async (taskID: string) => {
    const response = await apiFetch(`/api/v1/tasks/${taskID}/plan`)
    if (!response.ok) throw new Error('无法读取任务计划')
    const body = (await response.json()) as { plan: ExecutionPlan | null }
    setCurrentPlan(body.plan)
  }, [])

  const loadVerifications = useCallback(async (taskID: string) => {
    const response = await apiFetch(`/api/v1/tasks/${taskID}/verifications`)
    if (!response.ok) throw new Error('无法读取验证报告')
    const body = (await response.json()) as { verifications: VerificationRecord[] }
    setVerifications(body.verifications)
  }, [])

  const loadTaskPlugins = useCallback(async (taskID: string) => {
    const response = await apiFetch(`/api/v1/tasks/${taskID}/plugins`)
    if (!response.ok) throw new Error('无法读取任务插件证据')
    const body = (await response.json()) as { plugins: PluginUsage[] }
    setTaskPlugins(body.plugins)
  }, [])

  useEffect(() => {
    Promise.all([
      apiFetch('/api/v1/health').then((response) => response.json() as Promise<Health>),
      loadTasks(),
      loadModels(),
      loadSettings(),
      loadPlugins(),
      loadEvalRuns(),
    ])
      .then(([runtime]) => setHealth(runtime))
      .catch((cause: Error) => setError(cause.message))
  }, [loadEvalRuns, loadModels, loadPlugins, loadSettings, loadTasks])

  useEffect(() => {
    if (!settingsOpen) return
    Promise.all([loadSettings(), loadCleanupPreview()])
      .catch((cause: Error) => setError(cause.message))
  }, [loadCleanupPreview, loadSettings, settingsOpen])

  useEffect(() => {
    if (!selectedEvalID) {
      setSelectedEval(null)
      setEvalReport(null)
      return
    }
    loadEval(selectedEvalID).catch((cause: Error) => setError(cause.message))
  }, [loadEval, selectedEvalID])

  useEffect(() => {
    if (!evalRuns.some((run) => run.status === 'queued' || run.status === 'running')) return
    const timer = window.setInterval(() => {
      loadEvalRuns().catch(() => undefined)
      if (selectedEvalID) loadEval(selectedEvalID).catch(() => undefined)
    }, 1_000)
    return () => window.clearInterval(timer)
  }, [evalRuns, loadEval, loadEvalRuns, selectedEvalID])

  useEffect(() => {
    if (!selectedID) {
      setSelected(null)
      setEvents([])
      setApprovals([])
      setArtifacts([])
      setUncertainOperations([])
      setCurrentPlan(null)
      setVerifications([])
      setTaskPlugins([])
      setArtifactDiffs({})
      return
    }
    lastEventID.current = 0
    setEvents([])
    Promise.all([
      loadTask(selectedID),
      loadApprovals(selectedID),
      loadArtifacts(selectedID),
      loadUncertainOperations(selectedID),
      loadCurrentPlan(selectedID),
      loadVerifications(selectedID),
      loadTaskPlugins(selectedID),
    ])
      .catch((cause: Error) => setError(cause.message))

    const streamController = new AbortController()
    let refreshTimer: number | undefined
    const scheduleSnapshotRefresh = () => {
      window.clearTimeout(refreshTimer)
      refreshTimer = window.setTimeout(() => {
        loadTask(selectedID).catch(() => undefined)
        loadApprovals(selectedID).catch(() => undefined)
        loadArtifacts(selectedID).catch(() => undefined)
        loadUncertainOperations(selectedID).catch(() => undefined)
        loadCurrentPlan(selectedID).catch(() => undefined)
        loadVerifications(selectedID).catch(() => undefined)
        loadTaskPlugins(selectedID).catch(() => undefined)
        loadTasks().catch(() => undefined)
      }, 80)
    }
    const onEvent = (raw: ParsedServerSentEvent) => {
      if (!eventTypes.includes(raw.event)) return
      const event = JSON.parse(raw.data) as TaskEvent
      if (event.id <= lastEventID.current) return
      lastEventID.current = event.id
      setEvents((current) => [...current, event])
      scheduleSnapshotRefresh()
    }
    void streamTaskEvents(selectedID, streamController.signal, () => lastEventID.current, onEvent)
    return () => {
      window.clearTimeout(refreshTimer)
      streamController.abort()
    }
  }, [loadApprovals, loadArtifacts, loadCurrentPlan, loadTask, loadTaskPlugins, loadTasks, loadUncertainOperations, loadVerifications, selectedID])

  async function submit(event: FormEvent) {
    event.preventDefault()
    const trimmed = goal.trim()
    if (!trimmed || isSubmitting) return
    setError('')
    setIsSubmitting(true)
    try {
      const submitter = (event.nativeEvent as SubmitEvent).submitter as HTMLButtonElement | null
      const canContinue = selected !== null && (
        selected.status === 'waiting_input'
        || selected.status === 'completed'
        || selected.status === 'partially_completed'
        || selected.status === 'failed'
        || selected.status === 'cancelled'
      )
      const shouldContinue = taskSubmissionMode(canContinue, submitter?.dataset.mode) === 'continue'
      const encodedAttachments = await Promise.all(attachments.map(fileToTaskAttachment))
      const response = await apiFetch(
        shouldContinue ? `/api/v1/tasks/${selected!.id}/messages` : '/api/v1/tasks',
        {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(shouldContinue
          ? taskContinuationInput(trimmed, encodedAttachments)
          : {
              goal: trimmed,
              ...(selectedModelID ? { model_config_id: selectedModelID } : {}),
              plugins: {
                enable: pluginIDsFor(pluginPreferences, 'enable'),
                disable: pluginIDsFor(pluginPreferences, 'disable'),
              },
              ...(encodedAttachments.length ? { attachments: encodedAttachments } : {}),
            }),
        },
      )
      if (!response.ok) {
        throw new Error(await responseError(response, shouldContinue ? '继续任务失败' : '任务创建失败'))
      }
      const created = (await response.json()) as Task
      setGoal('')
      setAttachments([])
      setSelectedID(created.id)
      await loadTasks()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '任务创建失败')
    } finally {
      setIsSubmitting(false)
    }
  }

  function selectAttachments(event: ChangeEvent<HTMLInputElement>) {
    const selectedFiles = Array.from(event.target.files ?? [])
    event.target.value = ''
    const combined = [...attachments, ...selectedFiles]
    const accepted = new Set(['image/png', 'image/jpeg', 'image/webp', 'image/gif'])
    if (combined.length > 4) {
      setError('一次最多附加 4 张图片')
      return
    }
    if (combined.some((file) => !accepted.has(file.type))) {
      setError('仅支持 PNG、JPEG、WebP 和 GIF 图片')
      return
    }
    if (combined.some((file) => file.size > 8 * 1024 * 1024)) {
      setError('单张图片不能超过 8 MiB')
      return
    }
    if (combined.reduce((sum, file) => sum + file.size, 0) > 12 * 1024 * 1024) {
      setError('本次图片总大小不能超过 12 MiB')
      return
    }
    setError('')
    setAttachments(combined)
  }

  async function createModelConfig(event: FormEvent) {
    event.preventDefault()
    if (savingModel) return
    setError('')
    setSavingModel(true)
    try {
      const response = await apiFetch('/api/v1/model-configs', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          name: modelForm.name,
          provider: modelForm.provider,
          base_url: modelForm.baseURL,
          model: modelForm.model,
          api_key: modelForm.apiKey || undefined,
          api_key_env: modelForm.apiKeyEnv || undefined,
          set_default: modelForm.setDefault,
        }),
      })
      if (!response.ok) {
        const body = (await response.json().catch(() => null)) as { detail?: string } | null
        throw new Error(body?.detail || '模型连接保存失败')
      }
      const created = (await response.json()) as ModelConfig
      setModelForm(emptyModelConfigForm)
      await loadModels()
      setSelectedModelID(created.id)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '模型连接保存失败')
    } finally {
      setModelForm((current) => ({ ...current, apiKey: '' }))
      setSavingModel(false)
    }
  }

  async function makeDefaultModel(config: ModelConfig) {
    setError('')
    const response = await apiFetch(`/api/v1/model-configs/${config.id}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: config.name,
        provider: config.provider,
        base_url: config.base_url,
        model: config.model,
        enabled: config.enabled,
        set_default: true,
      }),
    })
    if (!response.ok) {
      const body = (await response.json().catch(() => null)) as { detail?: string } | null
      setError(body?.detail || '默认模型设置失败')
      return
    }
    await loadModels()
    setSelectedModelID(config.id)
  }

  async function deleteModelConfig(config: ModelConfig) {
    if (deletingModelID || !window.confirm(`删除模型连接“${config.name}”？历史任务仍保留使用快照。`)) return
    setError('')
    setDeletingModelID(config.id)
    try {
      const response = await apiFetch(`/api/v1/model-configs/${config.id}`, { method: 'DELETE' })
      if (!response.ok) throw new Error('模型连接删除失败')
      await loadModels()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '模型连接删除失败')
    } finally {
      setDeletingModelID('')
    }
  }

  async function testModelConfig(config: ModelConfig) {
    if (testingModelID) return
    setError('')
    setTestingModelID(config.id)
    setModelTestResults((current) => ({ ...current, [config.id]: '' }))
    try {
      const response = await apiFetch(`/api/v1/model-configs/${config.id}/test`, { method: 'POST' })
      if (!response.ok) throw new Error('连接检测失败，请检查地址、模型和密钥环境变量')
      const result = (await response.json()) as { latency_ms: number }
      setModelTestResults((current) => ({ ...current, [config.id]: `连接正常 · ${result.latency_ms} ms` }))
    } catch (cause) {
      const message = cause instanceof Error ? cause.message : '连接检测失败'
      setModelTestResults((current) => ({ ...current, [config.id]: message }))
    } finally {
      setTestingModelID('')
    }
  }

  async function saveRuntimeSettings(event: FormEvent) {
    event.preventDefault()
    if (savingSettings) return
    setError('')
    setSettingsNotice('')
    setSavingSettings(true)
    try {
      const response = await apiFetch('/api/v1/settings', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          max_turns: settingsDraft.max_turns,
          max_tool_calls: settingsDraft.max_tool_calls,
          max_tokens: settingsDraft.max_tokens,
          max_cost_usd: settingsDraft.max_cost_usd,
          task_timeout: settingsDraft.task_timeout,
          policy_profile: settingsDraft.policy_profile,
          retention_days: settingsDraft.retention_days,
        }),
      })
      if (!response.ok) throw new Error(await responseError(response, '运行设置保存失败'))
      const updated = (await response.json()) as RuntimeSettings
      setRuntimeSettings(updated)
      setSettingsDraft(updated)
      setSettingsNotice('已保存 · 后续开始的执行使用新设置')
      await loadCleanupPreview()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '运行设置保存失败')
    } finally {
      setSavingSettings(false)
    }
  }

  async function cleanupExpiredData() {
    if (cleaningData || !cleanupPreview || cleanupPreview.task_count === 0) return
    const confirmed = window.confirm(
      `永久删除 ${cleanupPreview.task_count} 个超过 ${cleanupPreview.retention_days} 天的终态任务，`
      + `以及 ${cleanupPreview.artifact_count} 条产物引用？此操作不可撤销。`,
    )
    if (!confirmed) return
    setError('')
    setSettingsNotice('')
    setCleaningData(true)
    try {
      const response = await apiFetch('/api/v1/settings/cleanup', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ confirm: true }),
      })
      if (!response.ok) throw new Error(await responseError(response, '数据清理失败'))
      const result = (await response.json()) as CleanupResult
      setSettingsNotice(`已清理 ${result.task_count} 个任务 · 释放 ${formatBytes(result.removed_bytes)}`)
      await Promise.all([loadCleanupPreview(), loadTasks()])
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '数据清理失败')
    } finally {
      setCleaningData(false)
    }
  }

  async function installPlugin(event: FormEvent) {
    event.preventDefault()
    const source = pluginSource.trim()
    if (!source || installingPlugin) return
    setError('')
    setInstallingPlugin(true)
    try {
      const response = await apiFetch('/api/v1/plugins/install', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ source, enable: enableInstalledPlugin }),
      })
      if (!response.ok) throw new Error(await responseError(response, '插件安装失败'))
      setPluginSource('')
      await loadPlugins()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '插件安装失败')
    } finally {
      setInstallingPlugin(false)
    }
  }

  async function setPluginEnabled(item: InstalledPlugin, enabled: boolean) {
    if (mutatingPluginID) return
    setError('')
    setMutatingPluginID(item.id)
    try {
      const response = await apiFetch(`/api/v1/plugins/${encodeURIComponent(item.id)}/${enabled ? 'enable' : 'disable'}`, {
        method: 'POST',
      })
      if (!response.ok) throw new Error(await responseError(response, enabled ? '插件启用失败' : '插件停用失败'))
      await loadPlugins()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '插件状态更新失败')
    } finally {
      setMutatingPluginID('')
    }
  }

  async function removePlugin(item: InstalledPlugin) {
    if (mutatingPluginID || !window.confirm(`卸载插件“${item.name}” ${item.version}？历史任务仍保留版本与摘要证据。`)) return
    setError('')
    setMutatingPluginID(item.id)
    try {
      const response = await apiFetch(`/api/v1/plugins/${encodeURIComponent(item.id)}`, { method: 'DELETE' })
      if (!response.ok) throw new Error(await responseError(response, '插件卸载失败'))
      await loadPlugins()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '插件卸载失败')
    } finally {
      setMutatingPluginID('')
    }
  }

  async function startEvaluation(event: FormEvent) {
    event.preventDefault()
    const variants = [
      ...(evalVariants.base ? ['general.base'] : []),
      ...(evalVariants.expert ? ['expert.go'] : []),
    ]
    if (startingEval || !evalSuitePath.trim() || variants.length === 0) return
    setError('')
    setStartingEval(true)
    try {
      const response = await apiFetch('/api/v1/evals/runs', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ suite_path: evalSuitePath.trim(), variants }),
      })
      if (!response.ok) throw new Error(await responseError(response, '评测启动失败'))
      const run = (await response.json()) as EvaluationRun
      setSelectedEvalID(run.id)
      setSelectedEval(run)
      setEvalReport(null)
      await loadEvalRuns()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '评测启动失败')
    } finally {
      setStartingEval(false)
    }
  }

  async function controlEvaluation(action: 'pause' | 'resume' | 'cancel') {
    if (!selectedEval || selectedEval.status === 'completed' || selectedEval.status === 'failed' ||
      selectedEval.status === 'cancelled' || evalAction ||
      (action === 'pause' && selectedEval.status === 'paused') ||
      (action === 'resume' && selectedEval.status !== 'paused')) return
    setError('')
    setEvalAction(action)
    try {
      const response = await apiFetch(`/api/v1/evals/runs/${encodeURIComponent(selectedEval.id)}/${action}`, { method: 'POST' })
      const fallback = action === 'pause' ? '评测暂停失败' : action === 'resume' ? '评测继续失败' : '评测取消失败'
      if (!response.ok) throw new Error(await responseError(response, fallback))
      await Promise.all([loadEvalRuns(), loadEval(selectedEval.id)])
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '评测控制失败')
    } finally {
      setEvalAction('')
    }
  }

  async function controlTask(action: TaskAction) {
    if (!selected || activeAction) return
    setError('')
    setActiveAction(action)
    try {
      const response = await apiFetch(`/api/v1/tasks/${selected.id}/${action}`, { method: 'POST' })
      if (!response.ok) {
        const body = (await response.json().catch(() => null)) as { detail?: string } | null
        throw new Error(body?.detail || `${actionLabels[action]}任务失败`)
      }
      const updated = (await response.json()) as Task
      setSelected(updated)
      await loadTasks()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : `${actionLabels[action]}任务失败`)
    } finally {
      setActiveAction(null)
    }
  }

  async function decideApproval(requestID: string, decision: 'approved' | 'denied') {
    if (!selected || activeApproval) return
    setError('')
    setActiveApproval(requestID)
    try {
      const response = await apiFetch(`/api/v1/approvals/${requestID}/decision`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ decision }),
      })
      if (!response.ok) throw new Error(decision === 'approved' ? '授权失败' : '拒绝失败')
      await Promise.all([loadApprovals(selected.id), loadTask(selected.id)])
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '授权操作失败')
    } finally {
      setActiveApproval('')
    }
  }

  async function resolveOperation(
    operationID: string,
    resolution: 'confirmed_succeeded' | 'confirmed_not_executed',
  ) {
    if (!selected || activeResolution) return
    setError('')
    setActiveResolution(operationID)
    try {
      const response = await apiFetch(`/api/v1/operations/${operationID}/resolution`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ resolution }),
      })
      if (!response.ok) {
        const body = (await response.json().catch(() => null)) as { detail?: string } | null
        throw new Error(body?.detail || '无法记录中断操作结论')
      }
      await Promise.all([
        loadUncertainOperations(selected.id),
        loadTask(selected.id),
        loadTasks(),
      ])
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '无法记录中断操作结论')
    } finally {
      setActiveResolution('')
    }
  }

  const modeLabel = health?.mode.startsWith('model:')
    ? health.mode.slice('model:'.length)
    : '离线基线'
  const verificationStatus = useMemo(() => (
    [...verifications].reverse().find((item) => item.verifier === 'core.aggregate')?.status
    ?? [...verifications].reverse()[0]?.status
  ), [verifications])
  const verificationChecks = useMemo(
    () => verifications.filter((item) => item.verifier !== 'core.aggregate'),
    [verifications],
  )
  const isVerified = verificationStatus === 'passed' || verificationStatus === 'verified'
  const canContinueSelected = selected !== null && (
    selected.status === 'waiting_input'
    || selected.status === 'completed'
    || selected.status === 'partially_completed'
    || selected.status === 'failed'
    || selected.status === 'cancelled'
  )

  return (
    <main className="shell">
      <header className="topbar">
        <div className="brand" aria-label="Kern home">
          <span className="brand-mark">K</span>
          <div>
            <strong>Kern</strong>
            <span>GENERAL AGENT RUNTIME</span>
          </div>
        </div>
        <div className="conversation-title">
          <strong>{selected?.title || 'Kern'}</strong>
          <small>{selected ? statusLabels[selected.status] : '新任务'}</small>
        </div>
        <div className="runtime-cluster">
          <div className="runtime">
            <span className="pulse" />
            <div>
              <small>执行模式</small>
              <strong>{modeLabel}</strong>
            </div>
          </div>
          <button className="settings-trigger eval-trigger" type="button" onClick={() => setEvalsOpen(true)}>
            评测 <span>{evalRuns.length.toString().padStart(2, '0')}</span>
          </button>
          <button className="settings-trigger plugin-trigger" type="button" onClick={() => setPluginsOpen(true)}>
            插件 <span>{plugins.length.toString().padStart(2, '0')}</span>
          </button>
          <button className="settings-trigger" type="button" onClick={() => setSettingsOpen(true)}>
            设置 <span>{modelConfigs.length.toString().padStart(2, '0')}</span>
          </button>
        </div>
      </header>

      <section className="workspace">
        <aside className="ledger">
          <div className="section-heading">
            <button
              className="sidebar-new-task"
              type="button"
              onClick={() => {
                setSelectedID('')
                setGoal('')
                setAttachments([])
              }}
            >
              <span>＋</span> 新任务
            </button>
          </div>
          <div className="task-list">
            {tasks.map((taskItem, index) => (
              <button
                className={`task-row ${selectedID === taskItem.id ? 'active' : ''}`}
                key={taskItem.id}
                onClick={() => setSelectedID(taskItem.id)}
              >
                <span className="task-index">{String(index + 1).padStart(2, '0')}</span>
                <span className="task-copy">
                  <strong>{taskItem.title}</strong>
                  <small>{relativeTime(taskItem.updated_at)}</small>
                </span>
                <i className={`status-dot ${taskItem.status}`} />
              </button>
            ))}
            {tasks.length === 0 && <p className="empty-ledger">尚无任务记录</p>}
          </div>
        </aside>

        <section className="stage">
          <div className="stage-grid" aria-hidden="true" />
          {selected ? (
            <article className="task-detail">
              <div className="task-kicker">
                <span>ACTIVE TASK</span>
                <span className={`status-pill ${selected.status}`}>
                  {statusLabels[selected.status]}
                </span>
              </div>
              <h1>{selected.title}</h1>
              <p className="goal">{selected.goal}</p>
              <div className="task-actions" aria-label="任务控制">
                {actionsFor(selected.status).map((action) => (
                  <button
                    type="button"
                    key={action}
                    className={action === 'cancel' ? 'danger' : ''}
                    disabled={activeAction !== null || (action === 'resume' && uncertainOperations.length > 0)}
                    onClick={() => controlTask(action)}
                  >
                    {activeAction === action ? '处理中…' : actionLabels[action]}
                  </button>
                ))}
              </div>

              {taskPlugins.length > 0 && (
                <details className="task-plugin-section">
                  <summary className="task-plugin-heading">
                    <span>本次激活插件</span>
                    <b>{String(taskPlugins.length).padStart(2, '0')} AUDITED</b>
                  </summary>
                  <div className="task-plugin-list">
                    {taskPlugins.map((usage) => {
                      const installed = plugins.find((item) => item.id === usage.plugin_id)
                      return (
                        <article key={`${usage.plugin_id}-${usage.version}`}>
                          <span className="plugin-usage-mark">P</span>
                          <div>
                            <strong>{installed?.name ?? usage.plugin_id}</strong>
                            <small>{usage.plugin_id} · v{usage.version}</small>
                            <p>{activationReasonLabel(usage.reason)} · {pluginResourceCount(usage.resources)} 项声明式资源</p>
                          </div>
                          <code>{usage.digest.replace('sha256:', '').slice(0, 10)}</code>
                        </article>
                      )
                    })}
                  </div>
                </details>
              )}

              {currentPlan && (
                <details className={`plan-card plan-${currentPlan.status}`} open={currentPlan.status === 'active'}>
                  <summary className="plan-heading">
                    <span>执行计划</span>
                    <b>R{currentPlan.revision} · {planStatusLabel(currentPlan.status)}</b>
                  </summary>
                  <p>{currentPlan.rationale}</p>
                  <ol className="plan-steps">
                    {currentPlan.steps.map((step) => (
                      <li className={`plan-step step-${step.status}`} key={step.id}>
                        <span className="step-marker">{stepStatusMarker(step.status)}</span>
                        <div>
                          <strong>{step.title}</strong>
                          <small>{step.description}</small>
                          {step.failure && <em>{step.failure}</em>}
                        </div>
                        <b>{stepStatusLabel(step.status)}</b>
                      </li>
                    ))}
                  </ol>
                </details>
              )}

              {approvals.map((request) => (
                <section className={`approval-card risk-${request.risk}`} key={request.id}>
                  <div className="approval-heading">
                    <span>需要你的授权</span>
                    <b>{riskLabel(request.risk)}</b>
                  </div>
                  <p>{approvalExplanation(request)}</p>
                  <pre>{JSON.stringify(request.scope, null, 2)}</pre>
                  <small>授权仅适用于当前显示的工具、参数和输入哈希，不会扩展到后续操作。</small>
                  <div className="approval-actions">
                    <button
                      type="button"
                      className="deny"
                      disabled={activeApproval !== ''}
                      onClick={() => decideApproval(request.id, 'denied')}
                    >拒绝</button>
                    <button
                      type="button"
                      className="approve"
                      disabled={activeApproval !== ''}
                      onClick={() => decideApproval(request.id, 'approved')}
                    >{activeApproval === request.id ? '处理中…' : '允许本次操作'}</button>
                  </div>
                </section>
              ))}

              {uncertainOperations.map((operation) => (
                <section className="recovery-card" key={operation.id}>
                  <div className="recovery-heading">
                    <span>中断操作需要确认</span>
                    <b>{operation.effect}</b>
                  </div>
                  <p>
                    Kern 在操作执行期间中断，无法安全判断外部副作用是否已经发生。
                    在你确认前，任务不会自动重放。
                  </p>
                  <pre>{JSON.stringify({
                    tool: operation.tool,
                    input: operation.input,
                    input_hash: operation.input_hash,
                    idempotency_key: operation.idempotency_key,
                  }, null, 2)}</pre>
                  <small>请在目标系统或工作区核对真实结果后选择。该决定会生成不可变审计记录。</small>
                  <div className="recovery-actions">
                    <button
                      type="button"
                      disabled={activeResolution !== ''}
                      onClick={() => resolveOperation(operation.id, 'confirmed_not_executed')}
                    >确认未执行</button>
                    <button
                      type="button"
                      className="confirmed"
                      disabled={activeResolution !== ''}
                      onClick={() => resolveOperation(operation.id, 'confirmed_succeeded')}
                    >{activeResolution === operation.id ? '记录中…' : '确认已执行'}</button>
                  </div>
                </section>
              ))}

              <div className="result-header">
                <span>Kern</span>
                <span className={isVerified ? 'verified' : 'unverified'}>
                  {isVerified
                    ? '✓ 自动验证通过'
                    : verificationStatus
                      ? '× 自动验证未通过'
                      : '○ 尚未验证'}
                </span>
              </div>
              <div className="result-card">
                {selected.result ? (
                  selected.result.split('\n').map((line, index) => <p key={index}>{line || '\u00a0'}</p>)
                ) : (
                  <div className="working">
                    <span />
                    <p>Kern 正在推进任务，所有状态变化会先落库再显示。</p>
                  </div>
                )}
                {selected.error_message && <p className="error-copy">{selected.error_message}</p>}
              </div>

              {verifications.length > 0 && (
                <details className={`verification-section verification-${verificationStatus ?? 'not_run'}`} open={!isVerified}>
                  <summary className="verification-heading">
                    <span>确定性验证</span>
                    <b>{verificationStatusLabel(verificationStatus)}</b>
                  </summary>
                  <p>以下结论来自任务账本、授权收据、命令退出码、文件哈希与产物，不采用模型自述作为证据。</p>
                  <div className="verification-list">
                    {verificationChecks.map((check) => (
                      <div className={`verification-item check-${check.status}`} key={check.id}>
                        <span className="verification-marker">{verificationStatusMarker(check.status)}</span>
                        <div>
                          <strong>{verifierLabel(check.verifier)}</strong>
                          <p>{check.evidence.summary ?? '检查已完成'}</p>
                          {(check.evidence.evidence ?? []).map((evidence, index) => (
                            <small key={`${check.id}-${index}`}>
                              {evidence.ref}
                              {evidence.digest ? ` · sha256:${evidence.digest.slice(0, 12)}` : ''}
                            </small>
                          ))}
                        </div>
                        <b>{verificationStatusLabel(check.status)}</b>
                      </div>
                    ))}
                  </div>
                </details>
              )}

              {artifacts.length > 0 && (
                <section className="artifact-section">
                  <div className="artifact-heading">
                    <span>任务产物</span>
                    <b>{String(artifacts.length).padStart(2, '0')}</b>
                  </div>
                  <div className="artifact-list">
                    {artifacts.map((item) => (
                      <div className="artifact-item" key={item.id}>
                        <button
                          type="button"
                          className="artifact-row"
                          onClick={() => {
                            setError('')
                            void downloadResource(
                              `/api/v1/tasks/${encodeURIComponent(selected.id)}/artifacts/${encodeURIComponent(item.id)}`,
                              item.name,
                            ).catch((cause: Error) => setError(cause.message))
                          }}
                        >
                          <span className="artifact-icon">↓</span>
                          <span className="artifact-copy">
                            <strong>{item.name}</strong>
                            <small>{item.media_type} · {formatBytes(item.size)}</small>
                          </span>
                          <code>{item.digest.slice(0, 10)}</code>
                        </button>
                        {item.media_type === 'text/x-diff' && artifactDiffs[item.id] && (
                          <details className="diff-card" open>
                            <summary>查看修改差异</summary>
                            <pre>{artifactDiffs[item.id].split('\n').map((line, index) => (
                              <span className={diffLineClass(line)} key={`${item.id}-${index}`}>
                                {line || ' '}{'\n'}
                              </span>
                            ))}</pre>
                          </details>
                        )}
                      </div>
                    ))}
                  </div>
                </section>
              )}

              {events.length > 0 && (
                <details className="evidence-details">
                  <summary>
                    <span>查看执行记录</span>
                    <small>{events.length} 条持久化事件</small>
                  </summary>
                  <div className="event-rail">
                    {events.map((event) => (
                      <div className="event" key={event.id}>
                        <span className="rail-node" />
                        <div>
                          <small>{formatTime(event.created_at)} · #{event.id}</small>
                          <strong>{eventLabel(event.type)}</strong>
                          <p>{eventSummary(event)}</p>
                        </div>
                      </div>
                    ))}
                  </div>
                </details>
              )}
            </article>
          ) : (
            <div className="welcome">
              <span className="welcome-mark">K</span>
              <h1>今天想完成什么？</h1>
              <p>Kern 可以检查项目、修改文件、运行命令并验证结果。需要权限时，它会先询问你。</p>
              <div className="principles">
                <button type="button" onClick={() => setGoal('检查当前项目并总结可以改进的地方')}>检查一个项目</button>
                <button type="button" onClick={() => setGoal('修复当前项目中的失败测试，并说明修改内容')}>修复失败测试</button>
                <button type="button" onClick={() => setGoal('解释当前代码结构和主要运行流程')}>理解代码结构</button>
              </div>
            </div>
          )}

          <form className="composer" onSubmit={submit}>
            <label htmlFor="goal">交给 Kern</label>
            <div className="composer-input">
              <textarea
                id="goal"
                placeholder="描述一个需要完成并验证的目标…"
                value={goal}
                onChange={(event) => setGoal(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === 'Enter' && !event.shiftKey) {
                    event.preventDefault()
                    event.currentTarget.form?.requestSubmit()
                  }
                }}
              />
              <div className="composer-options">
                <select
                  aria-label="本次任务使用的模型"
                  value={selectedModelID}
                  disabled={canContinueSelected}
                  onChange={(event) => setSelectedModelID(event.target.value)}
                >
                  <option value="">{modelConfigs.length ? '使用默认模型' : '离线基线'}</option>
                  {modelConfigs.filter((config) => config.enabled).map((config) => (
                    <option value={config.id} key={config.id}>
                      {config.name} · {config.model}{config.is_default ? '（默认）' : ''}
                    </option>
                  ))}
                </select>
                <button
                  className="composer-plugin-button"
                  type="button"
                  onClick={() => setPluginsOpen(true)}
                >
                  插件策略 · {pluginPreferenceSummary(pluginPreferences)}
                </button>
                <label className="composer-attachment-button">
                  附加图片 · {attachments.length || '无'}
                  <input
                    type="file"
                    accept="image/png,image/jpeg,image/webp,image/gif"
                    multiple
                    onChange={selectAttachments}
                  />
                </label>
              </div>
            </div>
            {attachments.length > 0 && (
              <div className="composer-attachments" aria-label="待上传图片">
                {attachments.map((file, index) => (
                  <button
                    type="button"
                    key={`${file.name}-${file.size}-${index}`}
                    title="移除图片"
                    onClick={() => setAttachments((current) => current.filter((_, itemIndex) => itemIndex !== index))}
                  >
                    {file.name} · {formatBytes(file.size)} <span>×</span>
                  </button>
                ))}
              </div>
            )}
            <div className="composer-actions">
              {canContinueSelected && (
                <button className="new-task" type="submit" data-mode="new" disabled={!goal.trim() || isSubmitting}>
                  新任务
                </button>
              )}
              <button type="submit" data-mode={canContinueSelected ? 'continue' : 'new'} disabled={!goal.trim() || isSubmitting}>
                {isSubmitting ? '入队中' : canContinueSelected ? '继续任务' : '运行任务'} <span>↗</span>
              </button>
            </div>
          </form>
          {error && <div className="toast" role="alert">{error}</div>}
        </section>

        <aside className="evidence">
          <div className="section-heading">
            <span>证据轨</span>
            <b>LIVE</b>
          </div>
          <div className="event-rail">
            {events.map((event) => (
              <div className="event" key={event.id}>
                <span className="rail-node" />
                <div>
                  <small>{formatTime(event.created_at)} · #{event.id}</small>
                  <strong>{eventLabel(event.type)}</strong>
                  <p>{eventSummary(event)}</p>
                </div>
              </div>
            ))}
            {events.length === 0 && (
              <p className="empty-events">选择或创建任务后，这里会显示已经持久化的执行事实。</p>
            )}
          </div>
        </aside>
      </section>
      {evalsOpen && (
        <div className="settings-layer" role="presentation" onMouseDown={(event) => {
          if (event.target === event.currentTarget) setEvalsOpen(false)
        }}>
          <section className="settings-panel eval-panel" role="dialog" aria-modal="true" aria-labelledby="eval-title">
            <header>
              <div>
                <small>REPRODUCIBLE EVALUATION</small>
                <h2 id="eval-title">能力评测</h2>
              </div>
              <button type="button" aria-label="关闭评测" onClick={() => setEvalsOpen(false)}>×</button>
            </header>
            <p className="settings-intro">
              同一任务集使用独立工作区、相同预算与确定性验证器，对比通用模式和专家插件带来的真实变化。
            </p>
            <div className="eval-summary-strip">
              <span><b>{evalRuns.length}</b> 历史运行</span>
              <span><b>{evalRuns.filter((run) => run.status === 'queued' || run.status === 'running').length}</b> 进行中</span>
              <span><b>{evalRuns.filter((run) => run.status === 'completed').length}</b> 已完成</span>
              <span><b>{evalRuns.filter((run) => run.status === 'failed').length}</b> 失败</span>
            </div>
            <form className="eval-start-form" onSubmit={startEvaluation}>
              <div className="form-heading">
                <span>发起对比</span>
                <b>ISOLATED WORKSPACES</b>
              </div>
              <label className="eval-suite-path">
                可信评测目录内的 Suite 路径
                <input
                  required
                  value={evalSuitePath}
                  onChange={(event) => setEvalSuitePath(event.target.value)}
                  placeholder="go"
                />
              </label>
              <fieldset>
                <legend>运行变体</legend>
                <label><input type="checkbox" checked={evalVariants.base} onChange={(event) => setEvalVariants((current) => ({ ...current, base: event.target.checked }))} />通用模式</label>
                <label><input type="checkbox" checked={evalVariants.expert} onChange={(event) => setEvalVariants((current) => ({ ...current, expert: event.target.checked }))} />Go Expert</label>
              </fieldset>
              <button type="submit" disabled={startingEval || !evalSuitePath.trim() || (!evalVariants.base && !evalVariants.expert)}>
                {startingEval ? '校验并入队中…' : '开始评测'}
              </button>
              <small>路径相对于启动时通过 --eval-root 配置的可信目录；该目录必须与 Agent 可写工作区隔离。Suite 会先做严格 Schema、路径、插件版本与命令允许列表检查，每个 Case/Variant/Attempt 都从干净夹具开始。</small>
            </form>
            <div className="eval-workbench">
              <section className="eval-run-list" aria-label="评测运行记录">
                <div className="eval-subheading"><span>运行记录</span><b>{String(evalRuns.length).padStart(2, '0')}</b></div>
                {evalRuns.map((run) => (
                  <button
                    type="button"
                    className={selectedEvalID === run.id ? 'active' : ''}
                    key={run.id}
                    onClick={() => setSelectedEvalID(run.id)}
                  >
                    <span className={`eval-status-mark ${run.status}`}>{evaluationStatusMark(run.status)}</span>
                    <span>
                      <strong>{run.suite_name}</strong>
                      <small>{run.variants.join(' × ')} · {relativeTime(run.created_at)}</small>
                    </span>
                    <b>{run.completed_cases}/{run.case_count}</b>
                  </button>
                ))}
                {evalRuns.length === 0 && <p className="empty-models">还没有评测记录。默认 Go Suite 包含 30 个独立缺陷任务。</p>}
              </section>
              <section className="eval-detail" aria-live="polite">
                {selectedEval ? (
                  <>
                    <header>
                      <div>
                        <small>{selectedEval.suite_id}@{selectedEval.suite_version}</small>
                        <strong>{evaluationStatusLabel(selectedEval.status)}</strong>
                      </div>
                      <span>{selectedEval.completed_cases}/{selectedEval.case_count}</span>
                    </header>
                    <div className="eval-progress" aria-label="评测进度">
                      <span style={{ width: `${selectedEval.case_count ? Math.min(100, selectedEval.completed_cases / selectedEval.case_count * 100) : 0}%` }} />
                    </div>
                    {selectedEval.error_message && <p className="eval-error">{selectedEval.error_message}</p>}
                    {(selectedEval.status === 'queued' || selectedEval.status === 'running' || selectedEval.status === 'paused') && (
                      <div className="eval-controls">
                        {selectedEval.status === 'paused' ? (
                          <button className="resume-eval" type="button" disabled={Boolean(evalAction)} onClick={() => controlEvaluation('resume')}>
                            {evalAction === 'resume' ? '继续中…' : '继续评测'}
                          </button>
                        ) : (
                          <button className="pause-eval" type="button" disabled={Boolean(evalAction)} onClick={() => controlEvaluation('pause')}>
                            {evalAction === 'pause' ? '暂停中…' : '暂停评测'}
                          </button>
                        )}
                        <button className="cancel-eval" type="button" disabled={Boolean(evalAction)} onClick={() => controlEvaluation('cancel')}>
                          {evalAction === 'cancel' ? '取消中…' : '取消本次评测'}
                        </button>
                      </div>
                    )}
                    {evalReport && (
                      <>
                        <div className="eval-reproducibility">
                          <div>
                            <span>运行身份</span>
                            <b>{evalReport.reproducibility.core_version}</b>
                            <small>{evalReport.reproducibility.go_version} · {evalReport.reproducibility.goos}/{evalReport.reproducibility.goarch}</small>
                          </div>
                          <div>
                            <span>输入锁定</span>
                            <b>{evalReport.reproducibility.inputs.length} CASES</b>
                            <small>{evalReport.config_digest}</small>
                          </div>
                          <div>
                            <span>变体身份</span>
                            <b>{evalReport.reproducibility.variants.length} VARIANTS</b>
                            <small>{evalReport.reproducibility.variants.map((variant) => `${variant.id}:${variant.model || variant.agent || 'unknown'}`).join(' · ')}</small>
                          </div>
                        </div>
                        <div className="eval-metrics">
                          {evalReport.variants.map((metrics) => (
                            <article key={metrics.variant_id}>
                              <small>{metrics.variant_id}</small>
                              <strong>{(metrics.success_rate * 100).toFixed(1)}%</strong>
                              <span>{metrics.passed}/{metrics.cases} 通过</span>
                              <dl>
                                <div><dt>得分</dt><dd>{metrics.mean_score.toFixed(3)}</dd></div>
                                <div><dt>Token</dt><dd>{formatNumber(metrics.total_input_tokens + metrics.total_output_tokens)}</dd></div>
                                <div><dt>费用</dt><dd>${(metrics.total_cost_micros / 1_000_000).toFixed(3)}</dd></div>
                                <div><dt>安全</dt><dd>{metrics.safety_violations}</dd></div>
                              </dl>
                            </article>
                          ))}
                        </div>
                        {(evalReport.comparisons ?? []).map((comparison) => (
                          <div className={`eval-comparison ${comparison.safety_regressed ? 'unsafe' : ''}`} key={comparison.candidate_variant}>
                            <span>{comparison.baseline_variant} → {comparison.candidate_variant}</span>
                            <b>{formatPercentagePoint(comparison.success_rate_delta)}</b>
                            <small>
                              {comparison.improvements.length} 改善 · {comparison.regressions.length} 回退 · {comparison.paired_cases} 配对
                              {' · '}p={comparison.success_rate_p_value < 0.001 ? '<0.001' : comparison.success_rate_p_value.toFixed(3)}
                              {' · '}{comparison.success_rate_improvement_significant ? '显著提升' : '尚无显著提升'}
                              {' · '}{comparison.safety_regressed ? '安全退化' : '安全无退化'}
                            </small>
                          </div>
                        ))}
                        <div className="eval-failures">
                          <div className="eval-subheading"><span>首轮失败案例</span><b>{evalReport.results.filter((result) => result.attempt === 1 && !result.passed).length}</b></div>
                          {evalReport.results.filter((result) => result.attempt === 1 && !result.passed).slice(0, 12).map((result) => (
                            <div key={`${result.variant_id}-${result.case_id}`}>
                              <span>{result.case_id}</span><small>{result.variant_id}</small><b>{result.score.toFixed(2)}</b>
                            </div>
                          ))}
                        </div>
                        <button
                          type="button"
                          className="download-report"
                          onClick={() => {
                            setError('')
                            void downloadResource(
                              `/api/v1/evals/runs/${encodeURIComponent(selectedEval.id)}/report`,
                              `kern-eval-${selectedEval.id}.json`,
                            ).catch((cause: Error) => setError(cause.message))
                          }}
                        >
                          导出完整 JSON 报告 ↗
                        </button>
                      </>
                    )}
                  </>
                ) : (
                  <p className="empty-models">选择一条评测记录查看进度、指标和回退案例。</p>
                )}
              </section>
            </div>
          </section>
        </div>
      )}
      {settingsOpen && (
        <div className="settings-layer" role="presentation" onMouseDown={(event) => {
          if (event.target === event.currentTarget) setSettingsOpen(false)
        }}>
          <section className="settings-panel runtime-settings-panel" role="dialog" aria-modal="true" aria-labelledby="runtime-settings-title">
            <header>
              <div>
                <small>CORE PREFERENCES</small>
                <h2 id="runtime-settings-title">运行设置</h2>
              </div>
              <button type="button" aria-label="关闭运行设置" onClick={() => setSettingsOpen(false)}>×</button>
            </header>
            <p className="settings-intro">设置会持久化，并从下一次开始执行的任务生效。正在运行的任务继续使用启动时锁定的预算和授权范围。</p>
            <form className="runtime-settings-form" onSubmit={saveRuntimeSettings}>
              <div className="form-heading">
                <span>任务默认值</span>
                <b>LIVE · NEXT RUN</b>
              </div>
              <div className="budget-grid">
                <label>最大模型轮次<input required type="number" min="1" max="10000" value={settingsDraft.max_turns} onChange={(event) => setSettingsDraft({ ...settingsDraft, max_turns: Number(event.target.value) })} /></label>
                <label>最大工具调用<input required type="number" min="1" max="100000" value={settingsDraft.max_tool_calls} onChange={(event) => setSettingsDraft({ ...settingsDraft, max_tool_calls: Number(event.target.value) })} /></label>
                <label>最大 Token<input required type="number" min="1" max="100000000" value={settingsDraft.max_tokens} onChange={(event) => setSettingsDraft({ ...settingsDraft, max_tokens: Number(event.target.value) })} /></label>
                <label>最大费用（USD）<input required type="number" min="0.000001" max="1000000" step="0.01" value={settingsDraft.max_cost_usd} onChange={(event) => setSettingsDraft({ ...settingsDraft, max_cost_usd: Number(event.target.value) })} /></label>
                <label>任务超时<input required value={settingsDraft.task_timeout} onChange={(event) => setSettingsDraft({ ...settingsDraft, task_timeout: event.target.value })} placeholder="例如 10m" /></label>
                <label>数据保留天数<input required type="number" min="1" max="3650" value={settingsDraft.retention_days} onChange={(event) => setSettingsDraft({ ...settingsDraft, retention_days: Number(event.target.value) })} /></label>
              </div>
              <label className="policy-select">
                <span>权限策略</span>
                <select value={settingsDraft.policy_profile} onChange={(event) => setSettingsDraft({ ...settingsDraft, policy_profile: event.target.value as PolicyProfile })}>
                  <option value="local-safe">本地安全 · 读取自动，写入与执行确认</option>
                  <option value="confirm-all">全部确认 · 连读取也逐次确认</option>
                  <option value="read-only">只读 · 禁止写入、命令与网络</option>
                </select>
                <small>{policyProfileDescription(settingsDraft.policy_profile)}</small>
              </label>
              <footer>
                <span>{settingsNotice || '保存不会改写已有任务、事件或授权收据。'}</span>
                <button type="button" disabled={savingSettings} onClick={() => setSettingsDraft(runtimeSettings)}>撤销修改</button>
                <button className="save-settings" type="submit" disabled={savingSettings}>{savingSettings ? '保存中…' : '保存运行设置'}</button>
              </footer>
            </form>
            <section className="data-retention-card" aria-labelledby="data-retention-title">
              <header>
                <div>
                  <small>RETENTION PREVIEW</small>
                  <strong id="data-retention-title">数据清理</strong>
                </div>
                <span>{cleanupPreview ? `截止 ${new Date(cleanupPreview.cutoff).toLocaleDateString('zh-CN')}` : '计算中'}</span>
              </header>
              <div className="cleanup-metrics">
                <span><b>{cleanupPreview?.task_count ?? '—'}</b>终态任务</span>
                <span><b>{cleanupPreview?.artifact_count ?? '—'}</b>产物引用</span>
                <span><b>{cleanupPreview ? formatBytes(cleanupPreview.artifact_bytes) : '—'}</b>引用体积</span>
              </div>
              <footer>
                <p>仅删除超过保留期的已完成、部分完成、失败或已取消任务。共享内容仍被引用时不会删除。</p>
                <button type="button" disabled={cleaningData || !cleanupPreview || cleanupPreview.task_count === 0} onClick={cleanupExpiredData}>
                  {cleaningData ? '清理中…' : cleanupPreview?.task_count ? '审核并清理' : '没有可清理数据'}
                </button>
              </footer>
            </section>
            <div className="settings-section-heading">
              <div>
                <small>MODEL CONNECTIONS</small>
                <strong>模型连接</strong>
              </div>
              <span>{modelConfigs.length} 个连接</span>
            </div>
            <p className="settings-intro model-settings-intro">任务会锁定所选连接。密钥值不会写入数据库、事件或任务上下文。</p>
            <div className="model-config-list">
              {modelConfigs.map((config) => (
                <article className={`model-config-card ${config.enabled ? '' : 'disabled'}`} key={config.id}>
                  <div>
                    <small>{config.provider}</small>
                    <strong>{config.name}</strong>
                    <span>{config.model}</span>
                  </div>
                  <p>{config.base_url}</p>
                  <footer>
                    <span>{config.is_default ? '默认连接' : config.has_api_key ? '密钥已引用' : '无需密钥'}</span>
                    <button type="button" disabled={testingModelID !== '' || !config.enabled} onClick={() => testModelConfig(config)}>
                      {testingModelID === config.id ? '检测中…' : '检测'}
                    </button>
                    {!config.is_default && config.enabled && (
                      <button type="button" onClick={() => makeDefaultModel(config)}>设为默认</button>
                    )}
                    <button
                      type="button"
                      className="remove-model"
                      disabled={deletingModelID !== ''}
                      onClick={() => deleteModelConfig(config)}
                    >{deletingModelID === config.id ? '删除中…' : '删除'}</button>
                  </footer>
                  {modelTestResults[config.id] && <small className="model-test-result">{modelTestResults[config.id]}</small>}
                </article>
              ))}
              {modelConfigs.length === 0 && <p className="empty-models">还没有模型连接。Kern 当前使用离线基线验证运行链路。</p>}
            </div>
            <form className="model-config-form" onSubmit={createModelConfig}>
              <div className="form-heading">
                <span>新增连接</span>
                <b>SECRET-SAFE</b>
              </div>
              <label>连接名称<input required maxLength={100} value={modelForm.name} onChange={(event) => setModelForm({ ...modelForm, name: event.target.value })} placeholder="例如：本地 Qwen" /></label>
              <label>提供方式<select value={modelForm.provider} onChange={(event) => setModelForm({ ...modelForm, provider: event.target.value as ModelProvider })}><option value="openai-compatible">OpenAI 兼容接口</option><option value="ollama">Ollama 本地模型</option></select></label>
              <label className="wide">服务地址<input required type="url" value={modelForm.baseURL} onChange={(event) => setModelForm({ ...modelForm, baseURL: event.target.value })} placeholder="http://127.0.0.1:11434" /></label>
              <label>模型名称<input required value={modelForm.model} onChange={(event) => setModelForm({ ...modelForm, model: event.target.value })} placeholder="qwen3" /></label>
              <label>API 密钥<input type="password" autoComplete="new-password" spellCheck={false} maxLength={2048} disabled={!credentialStoreAvailable} value={modelForm.apiKey} onChange={(event) => setModelForm({ ...modelForm, apiKey: event.target.value, apiKeyEnv: '' })} placeholder={credentialStoreAvailable ? '保存到系统安全存储 · 最多 2048 字节' : '当前系统安全存储不可用'} /></label>
              <label>或使用环境变量<input value={modelForm.apiKeyEnv} onChange={(event) => setModelForm({ ...modelForm, apiKey: '', apiKeyEnv: event.target.value.toUpperCase() })} placeholder="例如：OPENAI_API_KEY" /></label>
              <label className="default-check"><input type="checkbox" checked={modelForm.setDefault} onChange={(event) => setModelForm({ ...modelForm, setDefault: event.target.checked })} />设为默认连接</label>
              <button className="save-model" type="submit" disabled={savingModel}>{savingModel ? '保存中…' : '保存连接'}</button>
            </form>
          </section>
        </div>
      )}
      {pluginsOpen && (
        <div className="settings-layer" role="presentation" onMouseDown={(event) => {
          if (event.target === event.currentTarget) setPluginsOpen(false)
        }}>
          <section className="settings-panel plugin-panel" role="dialog" aria-modal="true" aria-labelledby="plugin-settings-title">
            <header>
              <div>
                <small>CAPABILITY REGISTRY</small>
                <h2 id="plugin-settings-title">插件能力</h2>
              </div>
              <button type="button" aria-label="关闭插件管理" onClick={() => setPluginsOpen(false)}>×</button>
            </header>
            <p className="settings-intro">
              插件只增加领域知识、流程和验证项，不会获得绕过 Core 权限的能力。任务策略仅作用于下一条新任务。
            </p>
            <div className="plugin-summary-strip">
              <span><b>{plugins.length}</b> 已安装</span>
              <span><b>{plugins.filter((item) => item.enabled).length}</b> 自动启用</span>
              <span><b>{pluginIDsFor(pluginPreferences, 'enable').length}</b> 下次强制</span>
              <span><b>{pluginIDsFor(pluginPreferences, 'disable').length}</b> 下次禁用</span>
            </div>
            <div className="plugin-config-list">
              {plugins.map((item) => (
                <article className={`plugin-config-card ${item.enabled ? '' : 'disabled'} trust-${item.trust_status}`} key={item.id}>
                  <header>
                    <div>
                      <small>{item.id}</small>
                      <strong>{item.name}</strong>
                      <span>v{item.version}</span>
                    </div>
                    <b>{trustStatusLabel(item.trust_status)}</b>
                  </header>
                  <p>{item.description || '该插件没有提供说明。'}</p>
                  <dl>
                    <div><dt>Core</dt><dd>{item.manifest.core}</dd></div>
                    <div><dt>资源</dt><dd>{entrypointCount(item.manifest.entrypoints)} 项</dd></div>
                    <div><dt>自动激活</dt><dd>{activationSummary(item)}</dd></div>
                    <div><dt>完整性</dt><dd title={item.digest}>{item.digest.replace('sha256:', '').slice(0, 12)}</dd></div>
                  </dl>
                  <div className="plugin-permissions" aria-label="插件声明权限">
                    {[...(item.manifest.permissions.filesystem ?? []), ...(item.manifest.permissions.process ?? []).map((name) => `process:${name}`)].map((permission) => (
                      <span key={permission}>{permissionLabel(permission)}</span>
                    ))}
                    {(item.manifest.permissions.filesystem?.length ?? 0) + (item.manifest.permissions.process?.length ?? 0) === 0 && (
                      <span>无额外权限</span>
                    )}
                  </div>
                  <footer>
                    <label>
                      下一条新任务
                      <select
                        value={pluginPreferences[item.id] ?? 'auto'}
                        onChange={(event) => setPluginPreferences((current) => ({
                          ...current,
                          [item.id]: event.target.value as PluginPreference,
                        }))}
                      >
                        <option value="auto">跟随自动策略</option>
                        <option value="enable">强制启用</option>
                        <option value="disable">强制禁用</option>
                      </select>
                    </label>
                    <button
                      type="button"
                      disabled={mutatingPluginID !== '' || item.trust_status === 'revoked'}
                      onClick={() => setPluginEnabled(item, !item.enabled)}
                    >
                      {mutatingPluginID === item.id ? '处理中…' : item.enabled ? '停用自动激活' : '启用自动激活'}
                    </button>
                    <button
                      className="remove-plugin"
                      type="button"
                      disabled={mutatingPluginID !== ''}
                      onClick={() => removePlugin(item)}
                    >卸载</button>
                  </footer>
                  <small className="plugin-source" title={item.source}>来源 · {item.source}</small>
                </article>
              ))}
              {plugins.length === 0 && (
                <p className="empty-models">尚未安装插件。Kern 的通用能力仍可独立完成全部受支持任务。</p>
              )}
            </div>
            <form className="plugin-install-form" onSubmit={installPlugin}>
              <div className="form-heading">
                <span>安装本地插件</span>
                <b>VERIFY BEFORE COPY</b>
              </div>
              <label>
                工作区内插件目录
                <input
                  required
                  value={pluginSource}
                  onChange={(event) => setPluginSource(event.target.value)}
                  placeholder="plugins/go-expert"
                />
              </label>
              <label className="install-enable-check">
                <input
                  type="checkbox"
                  checked={enableInstalledPlugin}
                  onChange={(event) => setEnableInstalledPlugin(event.target.checked)}
                />
                校验通过后启用自动激活
              </label>
              <button type="submit" disabled={installingPlugin || !pluginSource.trim()}>
                {installingPlugin ? '校验并安装中…' : '校验并安装'}
              </button>
              <small>目录必须位于当前工作区内；安装前会验证 Manifest、Core 兼容范围、文件摘要、路径与权限声明。</small>
            </form>
          </section>
        </div>
      )}
    </main>
  )
}

function eventLabel(type: string) {
  return {
    'task.created': '任务已创建',
    'task.status_changed': '状态迁移',
    'task.plan_updated': '计划已记录',
    'task.plan_skipped': '无需显式计划',
    'task.step_updated': '计划步骤更新',
    'message.completed': '结果已生成',
    'message.delta': '模型正在输出',
    'reasoning.delta': '推理摘要更新',
    'model.usage': '模型用量',
    'model.selected': '模型配置已锁定',
    'model.retry': '模型安全重试',
    'task.budget_exhausted': '任务预算已用尽',
    'context.message_saved': '上下文记录已保存',
    'context.built': '活动上下文已构建',
    'context.summary_created': '阶段摘要已生成',
    'operation.proposed': '工具操作已提出',
    'operation.recovery_prepared': '恢复依据已保存',
    'operation.started': '工具操作开始',
    'operation.output': '工具返回结果',
    'operation.status_changed': '工具状态变化',
    'operation.completed': '工具操作结束',
    'operation.unknown': '工具结果无法确认',
    'operation.resolved': '中断操作已人工确认',
    'operation.reconciliation_failed': '自动恢复无法核验',
    'operation.recovery_conflict': '文件恢复发现冲突',
    'approval.requested': '等待用户授权',
    'approval.decided': '授权决定已记录',
    'approval.expired': '授权请求已过期',
    'approval.cancelled': '授权请求已撤销',
    'artifact.created': '产物已保存',
    'verification.started': '开始验证',
    'verification.completed': '验证结束',
    'task.completed': '任务完成',
    'task.failed': '任务失败',
    'task.paused': '任务已暂停',
    'task.cancelled': '任务已取消',
    'task.attempt_created': '新执行尝试',
    'task.checkpoint_saved': '检查点已保存',
    'task.recovery_blocked': '恢复已安全暂停',
    'plugin.activated': '插件能力已激活',
    'plugin.activation_skipped': '插件能力已安全跳过',
    'plugin.activation_failed': '插件激活不可用',
    'plugin.signal_scan_failed': '插件信号扫描受限',
  }[type] ?? type
}

function evaluationStatusLabel(status: EvaluationStatus) {
  return {
    queued: '已入队',
    running: '评测中',
    paused: '已暂停',
    completed: '已完成',
    failed: '运行失败',
    cancelled: '已取消',
  }[status]
}

function evaluationStatusMark(status: EvaluationStatus) {
  return {
    queued: '○',
    running: '↻',
    paused: 'Ⅱ',
    completed: '✓',
    failed: '×',
    cancelled: '—',
  }[status]
}

function formatPercentagePoint(value: number) {
  const prefix = value > 0 ? '+' : ''
  return `${prefix}${(value * 100).toFixed(1)}pp`
}

function formatNumber(value: number) {
  return new Intl.NumberFormat('zh-CN', { notation: value >= 10_000 ? 'compact' : 'standard', maximumFractionDigits: 1 }).format(value)
}

function riskLabel(risk: ApprovalRequest['risk']) {
  return {
    low: '低风险',
    medium: '中风险',
    high: '高风险',
    critical: '关键风险',
  }[risk]
}

function planStatusLabel(status: ExecutionPlan['status']) {
  return {
    active: '进行中',
    completed: '已完成',
    failed: '未完成',
    superseded: '已替换',
  }[status]
}

function stepStatusLabel(status: PlanStep['status']) {
  return {
    pending: '待执行',
    running: '执行中',
    completed: '已完成',
    failed: '失败',
    skipped: '已跳过',
  }[status]
}

function stepStatusMarker(status: PlanStep['status']) {
  return {
    pending: '○',
    running: '→',
    completed: '✓',
    failed: '×',
    skipped: '–',
  }[status]
}

function verifierLabel(verifier: string) {
  return {
    'core.result': '最终结果',
    'core.operation_safety': '操作与授权',
    'core.operation_evidence': '工具证据',
    'core.file_integrity': '文件完整性',
    'core.command_exit': '命令与测试',
    'core.json_schema': 'JSON 格式',
    'core.external_effect': '外部副作用',
  }[verifier] ?? verifier
}

function verificationStatusLabel(status?: VerificationStatus) {
  if (!status) return '尚未运行'
  return {
    passed: '通过',
    verified: '已验证',
    failed: '失败',
    partial: '部分通过',
    manual_required: '需要人工确认',
    not_run: '未运行',
  }[status]
}

function verificationStatusMarker(status: VerificationStatus) {
  return {
    passed: '✓',
    verified: '✓',
    failed: '×',
    partial: '◐',
    manual_required: '!',
    not_run: '–',
  }[status]
}

export function approvalExplanation(request: ApprovalRequest) {
  const effect = request.scope.effect
  return {
    local_write: '将在当前任务工作区内创建或修改文件。',
    process: '将在当前任务工作区内运行下方显示的命令。',
    network_read: '将向下方显示的公开网络地址发送只读请求。',
    network_write: '将向下方显示的外部地址写入数据。',
    destructive: '将执行下方显示的破坏性操作，请逐项核对。',
  }[String(effect)] ?? request.explanation
}

export function actionsFor(status: Status): TaskAction[] {
  switch (status) {
    case 'planning':
    case 'running':
    case 'verifying':
      return ['pause', 'cancel']
    case 'created':
    case 'waiting_approval':
      return ['cancel']
    case 'waiting_input':
      return ['resume', 'cancel']
    case 'completed':
    case 'partially_completed':
    case 'failed':
    case 'cancelled':
      return ['retry']
  }
}

function eventSummary(event: TaskEvent) {
  if (event.type === 'task.status_changed') {
    return `${event.payload.from ?? ''} → ${event.payload.to ?? ''}`
  }
  if (event.type === 'verification.completed') {
    return String(event.payload.status ?? '已记录验证证据')
  }
  if (event.type === 'message.completed') return '完整输出已写入任务快照'
  if (event.type === 'task.plan_updated') {
    const steps = Array.isArray(event.payload.steps) ? event.payload.steps.length : 0
    return `计划修订 R${event.payload.revision ?? 1} · ${steps} 个可追踪步骤`
  }
  if (event.type === 'task.plan_skipped') return String(event.payload.reason ?? '单步任务无需计划')
  if (event.type === 'task.step_updated') {
    return `步骤 ${event.payload.ordinal ?? ''} · ${event.payload.from ?? ''} → ${event.payload.to ?? ''}`
  }
  if (event.type === 'approval.requested') return '请核对真实工具参数后决定是否执行'
  if (event.type === 'approval.decided') return String(event.payload.decision ?? '决定已持久化')
  if (event.type === 'operation.completed') return String(event.payload.to ?? '操作已结束')
  if (event.type === 'operation.unknown') return '执行期间发生中断；不会自动重放此操作'
  if (event.type === 'operation.resolved') {
    if (String(event.payload.actor ?? '').startsWith('kern-recovery:')) {
      return event.payload.resolution === 'confirmed_succeeded' ? '当前状态与目标哈希一致' : '当前状态仍与执行前哈希一致'
    }
    return event.payload.resolution === 'confirmed_succeeded' ? '用户确认操作已经执行' : '用户确认操作尚未执行'
  }
  if (event.type === 'operation.recovery_prepared') return '副作用开始前已持久化工具专用恢复依据'
  if (event.type === 'operation.reconciliation_failed') return '没有足够确定性，操作保持待人工确认'
  if (event.type === 'operation.recovery_conflict') return String(event.payload.summary ?? '当前文件与执行前、目标哈希均不一致')
  if (event.type === 'task.recovery_blocked') {
    return `${event.payload.uncertain_operations ?? 0} 个操作等待人工核对`
  }
  if (event.type === 'artifact.created') {
    return `${event.payload.name ?? '任务产物'} · ${formatBytes(Number(event.payload.size ?? 0))}`
  }
  if (event.type === 'model.usage') {
    const totalTokens = Number(event.payload.total_tokens ?? 0)
    const totalCostMicros = Number(event.payload.total_cost_micros ?? 0)
    const cost = totalCostMicros > 0 ? ` · 累计 ${formatUSD(totalCostMicros)}` : ''
    return `${event.payload.input_tokens ?? 0} 输入 / ${event.payload.output_tokens ?? 0} 输出 · 累计 ${totalTokens} Token${cost}`
  }
  if (event.type === 'model.selected') {
    return `${event.payload.provider ?? '模型提供方'} · ${event.payload.model ?? '未知模型'}`
  }
  if (event.type === 'task.budget_exhausted') {
    const dimension = {
      turns: '模型轮数',
      tool_calls: '工具调用',
      tokens: 'Token',
      cost_micros: '模型费用',
      duration_ms: '执行时间',
    }[String(event.payload.dimension)] ?? String(event.payload.dimension ?? '未知预算')
    if (event.payload.dimension === 'cost_micros') {
      return `${dimension}达到 ${formatUSD(Number(event.payload.observed ?? 0))}，上限 ${formatUSD(Number(event.payload.limit ?? 0))}`
    }
    if (event.payload.dimension === 'duration_ms') {
      return `${dimension}达到 ${formatDuration(Number(event.payload.observed ?? 0))}，上限 ${formatDuration(Number(event.payload.limit ?? 0))}`
    }
    return `${dimension}达到 ${event.payload.observed ?? 0}，上限 ${event.payload.limit ?? 0}`
  }
  if (event.type === 'context.message_saved') {
    return `${event.payload.role ?? '消息'} · ${event.payload.trust_level ?? '未知可信等级'} · ${event.payload.source ?? '未知来源'}`
  }
  if (event.type === 'context.built') {
    return `${event.payload.included_records ?? 0} 条已载入 · ${event.payload.omitted_records ?? 0} 条已压缩 · ${event.payload.characters ?? 0} 字符`
  }
  if (event.type === 'context.summary_created') {
    return '已保留来源引用和可信等级，原始记录保持不变'
  }
  if (event.type === 'plugin.activated') {
    return `${event.payload.plugin_id ?? '插件'} · v${event.payload.version ?? '?'} · ${activationReasonLabel(String(event.payload.reason ?? ''))}`
  }
  if (event.type === 'plugin.activation_skipped') {
    return `${event.payload.plugin_id ?? '插件'} · ${pluginSkipReasonLabel(String(event.payload.reason ?? ''))}`
  }
  if (event.type === 'plugin.activation_failed') {
    return `通用能力继续运行 · ${pluginSkipReasonLabel(String(event.payload.reason ?? ''))}`
  }
  if (event.type === 'plugin.signal_scan_failed') {
    return '未能读取工作区元数据；只保留手动选择，通用能力不受影响'
  }
  return '事件已持久化'
}

async function responseError(response: Response, fallback: string) {
  const body = (await response.json().catch(() => null)) as { error?: string; detail?: string } | null
  return body?.error || body?.detail || fallback
}

function fileToTaskAttachment(file: File): Promise<TaskAttachmentInput> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onerror = () => reject(new Error(`无法读取图片：${file.name}`))
    reader.onload = () => {
      const result = typeof reader.result === 'string' ? reader.result : ''
      const separator = result.indexOf(',')
      if (separator < 0) {
        reject(new Error(`无法编码图片：${file.name}`))
        return
      }
      resolve({
        name: file.name,
        media_type: file.type as TaskAttachmentInput['media_type'],
        data: result.slice(separator + 1),
      })
    }
    reader.readAsDataURL(file)
  })
}

export function taskSubmissionMode(
  canContinue: boolean,
  requestedMode: string | undefined,
): 'continue' | 'new' {
  return canContinue && (requestedMode ?? 'continue') === 'continue' ? 'continue' : 'new'
}

export function taskContinuationInput(
  content: string,
  attachments: TaskAttachmentInput[],
): { content: string; attachments?: TaskAttachmentInput[] } {
  return {
    content,
    ...(attachments.length ? { attachments } : {}),
  }
}

export function policyProfileDescription(profile: PolicyProfile) {
  switch (profile) {
    case 'confirm-all':
      return '适合演示、审计或首次接触新工作区；每次读取也会形成授权请求。'
    case 'read-only':
      return '只允许读取当前工作区；网络请求、文件修改、命令和插件副作用全部拒绝。'
    default:
      return '有界工作区读取自动放行；文件修改、命令、网络和高风险操作按真实影响确认。'
  }
}

function pluginIDsFor(preferences: Record<string, PluginPreference>, mode: Exclude<PluginPreference, 'auto'>) {
  return Object.entries(preferences)
    .filter(([, preference]) => preference === mode)
    .map(([pluginID]) => pluginID)
    .sort()
}

function pluginPreferenceSummary(preferences: Record<string, PluginPreference>) {
  const enabled = pluginIDsFor(preferences, 'enable').length
  const disabled = pluginIDsFor(preferences, 'disable').length
  if (enabled === 0 && disabled === 0) return '自动'
  return `+${enabled} / −${disabled}`
}

function pluginResourceCount(resources: PluginResources) {
  return Object.values(resources).reduce((total, values) => total + (values?.length ?? 0), 0)
}

function entrypointCount(entrypoints: PluginEntrypoints) {
  return Object.values(entrypoints).reduce((total, values) => total + (values?.length ?? 0), 0)
}

function trustStatusLabel(status: InstalledPlugin['trust_status']) {
  return {
    'local-unverified': '本地 · 未背书',
    verified: '已验证来源',
    revoked: '已撤销',
  }[status]
}

function activationSummary(item: InstalledPlugin) {
  const signals = item.manifest.activation.signals ?? []
  const intents = item.manifest.activation.intents ?? []
  if (signals.length === 0 && intents.length === 0) return '仅手动'
  return `${signals.length} 信号 / ${intents.length} 意图`
}

function activationReasonLabel(reason: string) {
  if (reason === 'manual_enable') return '任务手动启用'
  const parts = reason.split(';').filter(Boolean).map((part) => {
    if (part.startsWith('signal:')) return `工作区匹配 ${part.slice('signal:'.length)}`
    if (part.startsWith('intent:')) return `任务意图匹配 ${part.slice('intent:'.length)}`
    return part
  })
  return parts.join('；') || '自动激活'
}

function pluginSkipReasonLabel(reason: string) {
  if (reason.startsWith('conflict_with:')) return `与 ${reason.slice('conflict_with:'.length)} 冲突`
  return {
    resource_validation_failed: '资源校验失败，未载入',
    executable_runtime_unavailable: '当前版本尚无可执行插件运行时',
    context_bundle_failed: '上下文打包失败，未载入',
    context_append_failed: '上下文写入失败，未载入',
    audit_encoding_failed: '审计摘要生成失败，未载入',
    selection_unavailable: '插件选择不可用',
    context_initialization_failed: '任务上下文不可用',
    workspace_metadata_unavailable: '工作区元数据不可用',
  }[reason] ?? reason
}

function permissionLabel(permission: string) {
  if (permission === 'workspace:read') return '读取工作区'
  if (permission === 'workspace:write') return '修改工作区'
  if (permission.startsWith('process:')) return `运行 ${permission.slice('process:'.length)}`
  return permission
}

export function formatBytes(value: number) {
  if (!Number.isFinite(value) || value < 0) return '未知大小'
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`
  return `${(value / (1024 * 1024)).toFixed(1)} MB`
}

export function formatUSD(micros: number) {
  if (!Number.isFinite(micros) || micros < 0) return '未知费用'
  return `$${(micros / 1_000_000).toFixed(4)}`
}

export function formatDuration(milliseconds: number) {
  if (!Number.isFinite(milliseconds) || milliseconds < 0) return '未知时长'
  if (milliseconds < 1000) return `${milliseconds} ms`
  return `${(milliseconds / 1000).toFixed(1)} s`
}

export function diffLineClass(line: string) {
  if (line.startsWith('+++') || line.startsWith('---')) return 'diff-file'
  if (line.startsWith('+')) return 'diff-add'
  if (line.startsWith('-')) return 'diff-remove'
  if (line.startsWith('@@')) return 'diff-hunk'
  return 'diff-context'
}

function formatTime(value: string) {
  return new Intl.DateTimeFormat('zh-CN', {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  }).format(new Date(value))
}

function relativeTime(value: string) {
  const minutes = Math.max(0, Math.floor((Date.now() - new Date(value).getTime()) / 60000))
  if (minutes < 1) return '刚刚'
  if (minutes < 60) return `${minutes} 分钟前`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours} 小时前`
  return `${Math.floor(hours / 24)} 天前`
}

export default App
