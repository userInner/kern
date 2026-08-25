import { defineConfig } from 'vite'
import type { Plugin } from 'vite'
import react from '@vitejs/plugin-react'

const kernTarget = 'http://127.0.0.1:8787'

export function extractBootstrapToken(page: string): string {
  const match = page.match(/<meta name="kern-session-token" content="([A-Za-z0-9._-]+)">/)
  if (!match?.[1]) {
    throw new Error('Kern did not return a browser bootstrap token; start `kern web --open=false` first')
  }
  return match[1]
}

function kernBrowserBootstrap(): Plugin {
  return {
    name: 'kern-browser-bootstrap',
    apply: 'serve',
    async transformIndexHtml() {
      const response = await fetch(kernTarget + '/', { signal: AbortSignal.timeout(3_000) })
      if (!response.ok) {
        throw new Error(`Kern browser bootstrap failed with HTTP ${response.status}`)
      }
      return [{
        tag: 'meta',
        attrs: { name: 'kern-session-token', content: extractBootstrapToken(await response.text()) },
        injectTo: 'head',
      }]
    },
  }
}

export default defineConfig({
  plugins: [react(), kernBrowserBootstrap()],
  build: {
    outDir: '../internal/transport/httpapi/web',
    emptyOutDir: true,
  },
  server: {
    host: '127.0.0.1',
    proxy: {
      '/api': {
        target: kernTarget,
        changeOrigin: true,
        headers: { Origin: kernTarget },
      },
    },
  },
})
