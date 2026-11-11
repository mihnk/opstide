import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

// The Go server serves the built app at /. In development, `npm run dev` proxies
// the API to a collector running locally (make run).
export default defineConfig({
  plugins: [react()],
  // Built into internal/webui/dist and embedded in the Go binary.
  build: { outDir: '../internal/webui/dist', emptyOutDir: true, assetsDir: 'assets', sourcemap: false },
  server: { proxy: { '/api': 'http://127.0.0.1:8080' } },
  test: { environment: 'node', setupFiles: ['src/test-setup.ts'] },
})
