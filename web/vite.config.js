import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The Go server embeds dist/. In development, `npm run dev` proxies the API
// to the Go server on :8080.
export default defineConfig({
  plugins: [react()],
  build: { outDir: 'dist', emptyOutDir: false },
  server: { proxy: { '/api': 'http://localhost:8080' } },
})
