import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { after, before, test } from 'node:test'

import { KernAPIError, KernClient } from '../dist/index.js'

let baseURL
let server
let submittedTask
let submittedInput

before(async () => {
  server = createServer(async (request, response) => {
    if (request.headers.authorization !== 'Bearer test-token') {
      response.writeHead(401, { 'content-type': 'application/json' })
      response.end('{"error":"authentication required"}')
      return
    }
    if (request.url === '/api/v1/health' && request.method === 'GET') {
      response.writeHead(200, { 'content-type': 'application/json' })
      response.end('{"schema_version":"1","status":"ok","mode":"offline-baseline"}')
      return
    }
    if (request.url === '/api/v1/ready' && request.method === 'GET') {
      response.writeHead(200, { 'content-type': 'application/json' })
      response.end('{"schema_version":"1","status":"ready","mode":"offline-baseline"}')
      return
    }
    if (request.url === '/api/v1/settings' && request.method === 'GET') {
      response.writeHead(200, { 'content-type': 'application/json' })
      response.end('{"schema_version":"1","max_turns":12,"max_tool_calls":32,"max_tokens":200000,"max_cost_usd":5,"task_timeout":"10m0s","policy_profile":"local-safe","retention_days":90}')
      return
    }
    if (request.url === '/api/v1/settings' && request.method === 'PUT') {
      const body = await readJSON(request)
      assert.equal(body.policy_profile, 'read-only')
      assert.equal(body.retention_days, 45)
      response.writeHead(200, { 'content-type': 'application/json' })
      response.end('{"schema_version":"1","max_turns":8,"max_tool_calls":24,"max_tokens":100000,"max_cost_usd":3,"task_timeout":"5m0s","policy_profile":"read-only","retention_days":45}')
      return
    }
    if (request.url === '/api/v1/settings/cleanup-preview' && request.method === 'GET') {
      response.writeHead(200, { 'content-type': 'application/json' })
      response.end('{"cutoff":"2026-05-26T00:00:00Z","retention_days":90,"task_count":2,"artifact_count":3,"artifact_bytes":1024}')
      return
    }
    if (request.url === '/api/v1/settings/cleanup' && request.method === 'POST') {
      const body = await readJSON(request)
      assert.equal(body.confirm, true)
      response.writeHead(200, { 'content-type': 'application/json' })
      response.end('{"cutoff":"2026-05-26T00:00:00Z","retention_days":90,"task_count":2,"artifact_count":3,"artifact_bytes":1024,"removed_objects":2,"removed_bytes":768}')
      return
    }
    const modelConfig = '{"schema_version":"1","id":"model-1","name":"Local Qwen","provider":"ollama","base_url":"http://127.0.0.1:11434","model":"qwen3","has_api_key":false,"enabled":true,"is_default":true,"created_at":"2026-08-24T00:00:00Z","updated_at":"2026-08-24T00:00:00Z"}'
    if (request.url === '/api/v1/models' && request.method === 'GET') {
      response.writeHead(200, { 'content-type': 'application/json' })
      response.end(`{"schema_version":"1","credential_store_available":true,"providers":["openai-compatible","ollama"],"configs":[${modelConfig}]}`)
      return
    }
    if (request.url === '/api/v1/model-configs' && request.method === 'POST') {
      const body = await readJSON(request)
      assert.equal(body.api_key, 'write-only-test-key')
      response.writeHead(201, { 'content-type': 'application/json' })
      response.end(modelConfig)
      return
    }
    if (request.url === '/api/v1/model-configs/model-1' && request.method === 'PUT') {
      const body = await readJSON(request)
      assert.equal(body.name, 'Updated Qwen')
      response.writeHead(200, { 'content-type': 'application/json' })
      response.end(modelConfig.replace('Local Qwen', 'Updated Qwen'))
      return
    }
    if (request.url === '/api/v1/model-configs/model-1' && request.method === 'DELETE') {
      response.writeHead(204)
      response.end()
      return
    }
    if (request.url === '/api/v1/model-configs/model-1/test' && request.method === 'POST') {
      response.writeHead(200, { 'content-type': 'application/json' })
      response.end('{"schema_version":"1","config_id":"model-1","provider":"ollama","model":"qwen3","ok":true,"latency_ms":12,"capabilities":{"tools":true}}')
      return
    }
    if (request.url === '/api/v1/tasks' && request.method === 'POST') {
      submittedTask = await readJSON(request)
      response.writeHead(202, { 'content-type': 'application/json' })
      response.end('{"schema_version":"1","id":"task-1","status":"created"}')
      return
    }
    if (request.url === '/metrics' && request.method === 'GET') {
      assert.equal(request.headers.accept, 'text/plain')
      response.writeHead(200, { 'content-type': 'text/plain' })
      response.end('kern_tasks_created_total 1\n')
      return
    }
    if (request.url === '/api/v1/tasks/task-1/events') {
      assert.equal(request.headers['last-event-id'], '7')
      response.writeHead(200, { 'content-type': 'text/event-stream' })
      response.end('id: 8\nevent: task.completed\ndata: {"schema_version":"1","id":8,"task_id":"task-1","attempt_id":"attempt-1","type":"task.completed","payload":{},"created_at":"2026-08-24T00:00:00Z"}\n\n')
      return
    }
    if (request.url === '/api/v1/tasks/task-1/messages' && request.method === 'POST') {
      submittedInput = await readJSON(request)
      response.writeHead(202, { 'content-type': 'application/json' })
      response.end('{"schema_version":"1","id":"task-1","status":"created","active_attempt_id":"attempt-2"}')
      return
    }
    if (request.url === '/api/v1/tasks/task-1/plugins' && request.method === 'GET') {
      response.writeHead(200, { 'content-type': 'application/json' })
      response.end('{"schema_version":"1","attempt_id":"attempt-1","plugins":[{"schema_version":"1","task_id":"task-1","attempt_id":"attempt-1","plugin_id":"dev.kern.go","version":"0.1.0","digest":"sha256:test","reason":"manual_enable","resources":{"knowledge":["go.md"]},"created_at":"2026-08-24T00:00:00Z"}]}')
      return
    }
    if (request.url === '/api/v1/tasks/task-1/plan' && request.method === 'GET') {
      response.writeHead(200, { 'content-type': 'application/json' })
      response.end('{"schema_version":"1","plan":{"schema_version":"1","id":"plan-1","task_id":"task-1","attempt_id":"attempt-1","revision":1,"status":"active","rationale":"Verify work","steps":[{"id":"step-1","plan_id":"plan-1","ordinal":1,"title":"Inspect","description":"Inspect inputs","phase":"prepare","required":true,"status":"completed","failure":"","retry_count":0,"updated_at":"2026-08-24T00:00:00Z"}],"created_at":"2026-08-24T00:00:00Z","updated_at":"2026-08-24T00:00:00Z"}}')
      return
    }
    if (request.url === '/api/v1/tasks/task-1/operations/uncertain' && request.method === 'GET') {
      response.writeHead(200, { 'content-type': 'application/json' })
      response.end('{"schema_version":"1","operations":[{"id":"operation-1","task_id":"task-1","attempt_id":"attempt-1","tool":"execute","input":{"argv":["tool"]},"input_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","idempotency_key":"attempt-1:1:0","effect":"process","status":"unknown","output_summary":"","error_code":"interrupted","created_at":"2026-08-24T00:00:00Z","updated_at":"2026-08-24T00:00:00Z"}]}')
      return
    }
    if (request.url === '/api/v1/operations/operation-1/resolution' && request.method === 'POST') {
      const body = await readJSON(request)
      assert.equal(body.resolution, 'confirmed_not_executed')
      response.writeHead(200, { 'content-type': 'application/json' })
      response.end('{"id":"receipt-1","operation_id":"operation-1","task_id":"task-1","attempt_id":"attempt-1","resolution":"confirmed_not_executed","actor":"local-user","decided_at":"2026-08-24T00:00:00Z"}')
      return
    }
    if (request.url === '/api/v1/plugins' && request.method === 'GET') {
      response.writeHead(200, { 'content-type': 'application/json' })
      response.end('{"schema_version":"1","plugins":[{"schema_version":"1","id":"dev.kern.go","name":"Go Expert","version":"0.1.0","source":"local","digest":"sha256:test","enabled":true,"trust_status":"local","manifest":{"schema_version":"1","id":"dev.kern.go","name":"Go Expert","version":"0.1.0","core":">=0.1.0 <0.2.0","entrypoints":{"knowledge":["go.md"]},"activation":{},"permissions":{},"integrity":{"files":"sha256:test"}},"installed_at":"2026-08-24T00:00:00Z","updated_at":"2026-08-24T00:00:00Z"}]}')
      return
    }
    if (request.url === '/api/v1/plugins/install' && request.method === 'POST') {
      response.writeHead(201, { 'content-type': 'application/json' })
      response.end('{"schema_version":"1","id":"dev.kern.go","name":"Go Expert","version":"0.1.0","source":"local","digest":"sha256:test","enabled":false,"trust_status":"local","manifest":{"schema_version":"1","id":"dev.kern.go","name":"Go Expert","version":"0.1.0","core":">=0.1.0 <0.2.0","entrypoints":{"knowledge":["go.md"]},"activation":{},"permissions":{},"integrity":{"files":"sha256:test"}},"installed_at":"2026-08-24T00:00:00Z","updated_at":"2026-08-24T00:00:00Z"}')
      return
    }
    if (request.url === '/api/v1/plugins/dev.kern.go/enable' && request.method === 'POST') {
      response.writeHead(200, { 'content-type': 'application/json' })
      response.end('{"schema_version":"1","id":"dev.kern.go","name":"Go Expert","version":"0.1.0","source":"local","digest":"sha256:test","enabled":true,"trust_status":"local","manifest":{"schema_version":"1","id":"dev.kern.go","name":"Go Expert","version":"0.1.0","core":">=0.1.0 <0.2.0","entrypoints":{"knowledge":["go.md"]},"activation":{},"permissions":{},"integrity":{"files":"sha256:test"}},"installed_at":"2026-08-24T00:00:00Z","updated_at":"2026-08-24T00:00:00Z"}')
      return
    }
    if (request.url === '/api/v1/plugins/dev.kern.go' && request.method === 'DELETE') {
      response.writeHead(204)
      response.end()
      return
    }
    const evaluationRun = '{"schema_version":"1","id":"eval-1","suite_id":"kern.go","suite_name":"Go","suite_version":"1.0.0","status":"completed","variants":["general.base"],"case_count":30,"completed_cases":30,"created_at":"2026-08-24T00:00:00Z"}'
    if (request.url === '/api/v1/evals/runs' && request.method === 'POST') {
      response.writeHead(202, { 'content-type': 'application/json' })
      response.end(evaluationRun.replace('"completed"', '"queued"').replace('"completed_cases":30', '"completed_cases":0'))
      return
    }
    if (request.url === '/api/v1/evals/runs?limit=20' && request.method === 'GET') {
      response.writeHead(200, { 'content-type': 'application/json' })
      response.end(`{"runs":[${evaluationRun}]}`)
      return
    }
    if (request.url === '/api/v1/evals/runs/eval-1' && request.method === 'GET') {
      response.writeHead(200, { 'content-type': 'application/json' })
      response.end(evaluationRun)
      return
    }
    if (request.url === '/api/v1/evals/runs/eval-1/cancel' && request.method === 'POST') {
      response.writeHead(202, { 'content-type': 'application/json' })
      response.end(evaluationRun.replace('"completed"', '"cancelled"'))
      return
    }
    if (request.url === '/api/v1/evals/runs/eval-1/pause' && request.method === 'POST') {
      response.writeHead(202, { 'content-type': 'application/json' })
      response.end(evaluationRun.replace('"completed"', '"paused"').replace('"completed_cases":30', '"completed_cases":10'))
      return
    }
    if (request.url === '/api/v1/evals/runs/eval-1/resume' && request.method === 'POST') {
      response.writeHead(202, { 'content-type': 'application/json' })
      response.end(evaluationRun.replace('"completed"', '"queued"').replace('"completed_cases":30', '"completed_cases":10'))
      return
    }
    if (request.url === '/api/v1/evals/runs/eval-1/report' && request.method === 'GET') {
      response.writeHead(200, { 'content-type': 'application/json' })
      response.end('{"schema_version":"1","run_id":"eval-1","suite_id":"kern.go","suite_version":"1.0.0","status":"completed","config_digest":"sha256:test","variants":[],"results":[],"reproducibility":{"core_version":"0.1.0","go_version":"go1.26.6","goos":"darwin","goarch":"arm64","defaults":{"timeout_ms":60000,"token_budget":10000,"cost_budget_micros":0,"retries":1,"worker_count":2,"allowed_commands":["go"]},"variants":[{"id":"general.base","agent":"kern","agent_version":"kern-core/0.1.0","model":"offline-baseline","plugins":[]}],"inputs":[{"case_id":"go.test","prompt_sha256":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","fixture_sha256":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]},"started_at":"2026-08-24T00:00:00Z","completed_at":"2026-08-24T00:01:00Z"}')
      return
    }
    response.writeHead(404, { 'content-type': 'application/json' })
    response.end('{"error":"not found"}')
  })
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
  const address = server.address()
  baseURL = `http://127.0.0.1:${address.port}`
})

after(async () => {
  await new Promise((resolve, reject) => server.close((error) => (error ? reject(error) : resolve())))
})

test('creates tasks and authenticates requests', async () => {
  const client = new KernClient({ baseURL, token: 'test-token' })
  const task = await client.createTask({ goal: 'prove TypeScript SDK' })
  assert.equal(task.id, 'task-1')
  assert.equal(task.status, 'created')
  const continued = await client.submitInput(task.id, 'continue')
  assert.equal(continued.active_attempt_id, 'attempt-2')
  assert.deepEqual(submittedInput, { content: 'continue' })
  const health = await client.health()
  assert.equal(health.status, 'ok')
  const ready = await client.ready()
  assert.equal(ready.status, 'ready')
})

test('sends bounded image attachment inputs', async () => {
  const client = new KernClient({ baseURL, token: 'test-token' })
  await client.createTask({
    goal: 'describe image',
    attachments: [{ name: 'screen.png', media_type: 'image/png', data: 'cG5n' }],
  })
  assert.equal(submittedTask.attachments[0].name, 'screen.png')
  assert.equal(submittedTask.attachments[0].data, 'cG5n')

  await client.submitTaskInput('task-1', {
    content: 'compare images',
    attachments: [{ name: 'follow-up.png', media_type: 'image/png', data: 'cG5nLTI=' }],
  })
  assert.deepEqual(submittedInput, {
    content: 'compare images',
    attachments: [{ name: 'follow-up.png', media_type: 'image/png', data: 'cG5nLTI=' }],
  })
})

test('reads authenticated Prometheus metrics', async () => {
  const client = new KernClient({ baseURL, token: 'test-token' })
  assert.equal(await client.metrics(), 'kern_tasks_created_total 1\n')
})

test('manages runtime settings and retention cleanup', async () => {
  const client = new KernClient({ baseURL, token: 'test-token' })
  const current = await client.getSettings()
  assert.equal(current.policy_profile, 'local-safe')
  assert.equal(current.retention_days, 90)
  const updated = await client.updateSettings({
    max_turns: 8,
    max_tool_calls: 24,
    max_tokens: 100000,
    max_cost_usd: 3,
    task_timeout: '5m',
    policy_profile: 'read-only',
    retention_days: 45,
  })
  assert.equal(updated.max_turns, 8)
  assert.equal(updated.policy_profile, 'read-only')
  const preview = await client.previewCleanup()
  assert.equal(preview.task_count, 2)
  assert.equal(preview.artifact_bytes, 1024)
  const cleaned = await client.cleanupExpired()
  assert.equal(cleaned.removed_objects, 2)
  assert.equal(cleaned.removed_bytes, 768)
})

test('manages model connections and recovery state', async () => {
  const client = new KernClient({ baseURL, token: 'test-token' })
  const models = await client.listModelConfigs()
  assert.equal(models.credential_store_available, true)
  assert.equal(models.configs[0].provider, 'ollama')
  const input = {
    name: 'Local Qwen',
    provider: 'ollama',
    base_url: 'http://127.0.0.1:11434',
    model: 'qwen3',
    api_key: 'write-only-test-key',
    set_default: true,
  }
  const created = await client.createModelConfig(input)
  assert.equal(created.id, 'model-1')
  const updated = await client.updateModelConfig(created.id, { ...input, name: 'Updated Qwen' })
  assert.equal(updated.name, 'Updated Qwen')
  const probe = await client.testModelConfig(created.id)
  assert.equal(probe.ok, true)
  assert.equal(probe.capabilities.tools, true)
  await client.deleteModelConfig(created.id)

  const plan = await client.getPlan('task-1')
  assert.equal(plan.steps[0].phase, 'prepare')
  const operations = await client.listUncertainOperations('task-1')
  assert.equal(operations[0].status, 'unknown')
  const receipt = await client.resolveUncertainOperation(
    operations[0].id,
    'confirmed_not_executed',
  )
  assert.equal(receipt.resolution, 'confirmed_not_executed')
})

test('replays events after the supplied cursor', async () => {
  const client = new KernClient({ baseURL, token: 'test-token' })
  const controller = new AbortController()
  for await (const event of client.watchEvents('task-1', {
    after: 7,
    signal: controller.signal,
  })) {
    assert.equal(event.id, 8)
    assert.equal(event.type, 'task.completed')
    controller.abort()
    break
  }
})

test('returns typed API errors', async () => {
  const client = new KernClient({ baseURL, token: 'test-token' })
  await assert.rejects(
    client.getTask('missing'),
    (error) => error instanceof KernAPIError && error.status === 404 && /not found/.test(error.message),
  )
})

test('manages plugin lifecycle', async () => {
  const client = new KernClient({ baseURL, token: 'test-token' })
  const plugins = await client.listPlugins()
  assert.equal(plugins.length, 1)
  assert.equal(plugins[0].manifest.entrypoints.knowledge[0], 'go.md')
  const installed = await client.installPlugin({ source: '/plugins/go' })
  assert.equal(installed.enabled, false)
  const enabled = await client.enablePlugin(installed.id)
  assert.equal(enabled.enabled, true)
  await client.removePlugin(enabled.id)
  const usage = await client.listTaskPlugins('task-1')
  assert.equal(usage[0].plugin_id, 'dev.kern.go')
})

test('runs and inspects evaluations', async () => {
  const client = new KernClient({ baseURL, token: 'test-token' })
  const started = await client.startEvaluation({ suite_path: '/evals/go', variants: ['general.base'] })
  assert.equal(started.id, 'eval-1')
  assert.equal(started.status, 'queued')
  const runs = await client.listEvaluationRuns(20)
  assert.equal(runs.length, 1)
  assert.equal(runs[0].completed_cases, 30)
  const current = await client.getEvaluationRun('eval-1')
  assert.equal(current.status, 'completed')
  const paused = await client.pauseEvaluation('eval-1')
  assert.equal(paused.status, 'paused')
  assert.equal(paused.completed_cases, 10)
  const resumed = await client.resumeEvaluation('eval-1')
  assert.equal(resumed.status, 'queued')
  assert.equal(resumed.completed_cases, 10)
  const cancelled = await client.cancelEvaluation('eval-1')
  assert.equal(cancelled.status, 'cancelled')
  const report = await client.getEvaluationReport('eval-1')
  assert.equal(report.config_digest, 'sha256:test')
  assert.equal(report.reproducibility.core_version, '0.1.0')
  assert.equal(report.reproducibility.variants[0].model, 'offline-baseline')
  assert.equal(report.reproducibility.inputs.length, 1)
  await assert.rejects(() => client.listEvaluationRuns(0), RangeError)
})

async function readJSON(request) {
  const chunks = []
  for await (const chunk of request) chunks.push(chunk)
  return JSON.parse(Buffer.concat(chunks).toString('utf8'))
}
