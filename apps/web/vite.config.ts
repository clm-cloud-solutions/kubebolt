import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import path from 'path'

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: { '@': path.resolve(__dirname, './src') },
  },
  server: {
    port: 5173,
    proxy: {
      '/api/v1/ws': { target: 'http://localhost:8080', ws: true },
      '/ws/exec': { target: 'http://localhost:8080', ws: true },
      '/pf': 'http://localhost:8080',
      '/api': 'http://localhost:8080',
    },
  },
})
