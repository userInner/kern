import { describe, expect, it } from 'vitest'

import {
  actionsFor,
  approvalExplanation,
  authorizedRequestInit,
  diffLineClass,
  fetchAuthorizedDownload,
  formatBytes,
  formatDuration,
  formatUSD,
  parseSSEFrames,
  taskContinuationInput,
  taskSubmissionMode,
} from './App'

describe('browser API authentication', () => {
  it('adds the in-memory bearer token without sending cookies', () => {
    const init = authorizedRequestInit('short-session', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
    })
    const headers = new Headers(init.headers)
    expect(headers.get('Authorization')).toBe('Bearer short-session')
    expect(headers.get('Content-Type')).toBe('application/json')
    expect(init.credentials).toBe('omit')
  })

  it('downloads protected files with the bearer token and without cookies', async () => {
    let observed: RequestInit | undefined
    const fetcher = async (_input: RequestInfo | URL, init?: RequestInit) => {
      observed = init
      return new Response('protected artifact', { status: 200 })
    }

    const blob = await fetchAuthorizedDownload('/api/v1/artifact', 'download-session', fetcher)
    const headers = new Headers(observed?.headers)
    expect(headers.get('Authorization')).toBe('Bearer download-session')
    expect(observed?.credentials).toBe('omit')
    expect(await blob.text()).toBe('protected artifact')
  })

  it('parses chunk-ready SSE frames and preserves an incomplete tail', () => {
    const parsed = parseSSEFrames(
      ': keep-alive\r\nid: 7\r\nevent: task.completed\r\ndata: {"id":7}\r\n\r\n'
      + 'id: 8\nevent: task.failed\ndata: first\ndata: second',
    )
    expect(parsed.events).toEqual([{
      id: '7',
      event: 'task.completed',
      data: '{"id":7}',
    }])
    expect(parsed.remainder).toBe('id: 8\nevent: task.failed\ndata: first\ndata: second')
  })
})

describe('task action presentation', () => {
  it.each([
    ['running', ['pause', 'cancel']],
    ['waiting_approval', ['cancel']],
    ['waiting_input', ['resume', 'cancel']],
    ['completed', ['retry']],
    ['failed', ['retry']],
  ] as const)('maps %s to only valid controls', (status, actions) => {
    expect(actionsFor(status)).toEqual(actions)
  })

  it('keeps an explicit continuation independent of attached images', () => {
    const attachment = {
      name: 'follow-up.png',
      media_type: 'image/png' as const,
      data: 'iVBORw0KGgo=',
    }

    expect(taskSubmissionMode(true, 'continue')).toBe('continue')
    expect(taskContinuationInput('compare images', [attachment])).toEqual({
      content: 'compare images',
      attachments: [attachment],
    })
    expect(taskSubmissionMode(true, 'new')).toBe('new')
  })
})

describe('approval presentation', () => {
  it('describes the real effect instead of trusting model prose', () => {
    expect(approvalExplanation({
      id: 'approval-1',
      operation_id: 'operation-1',
      scope: { effect: 'destructive' },
      risk: 'critical',
      explanation: 'harmless',
      expires_at: '2026-08-24T00:00:00Z',
    })).toBe('将执行下方显示的破坏性操作，请逐项核对。')
  })
})

describe('evidence formatting', () => {
  it.each([
    ['+++ b/main.go', 'diff-file'],
    ['+added', 'diff-add'],
    ['-removed', 'diff-remove'],
    ['@@ -1 +1 @@', 'diff-hunk'],
    [' context', 'diff-context'],
  ])('classifies diff line %s', (line, className) => {
    expect(diffLineClass(line)).toBe(className)
  })

  it('bounds invalid measurements', () => {
    expect(formatBytes(-1)).toBe('未知大小')
    expect(formatUSD(Number.NaN)).toBe('未知费用')
    expect(formatDuration(Number.POSITIVE_INFINITY)).toBe('未知时长')
  })

  it('formats valid measurements deterministically', () => {
    expect(formatBytes(1536)).toBe('1.5 KB')
    expect(formatUSD(1_234_500)).toBe('$1.2345')
    expect(formatDuration(1500)).toBe('1.5 s')
  })
})
