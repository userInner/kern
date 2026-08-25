import { describe, expect, it } from 'vitest'

import { extractBootstrapToken } from './vite.config'

describe('Vite Kern bootstrap', () => {
  it('copies only the signed browser token from Kern HTML', () => {
    expect(extractBootstrapToken(
      '<html><head><meta name="kern-session-token" content="ks1.payload.signature"></head></html>',
    )).toBe('ks1.payload.signature')
  })

  it('fails closed when Kern did not inject a token', () => {
    expect(() => extractBootstrapToken('<html><head></head></html>')).toThrow(
      'Kern did not return a browser bootstrap token',
    )
  })
})
