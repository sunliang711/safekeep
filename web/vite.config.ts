import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/admin/api': 'http://localhost:8333',
      '/api': 'http://localhost:8333',
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})
