import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { resolve } from 'path'

const __dirname = process.cwd();

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@lib': resolve(__dirname, '../components-lib/src'),
      '@shared': resolve(__dirname, '../shared'),
    },
  },
  server: {
    proxy: {
      '/list': {
        target: 'http://localhost:8088',
        changeOrigin: true,
        secure: false,
      },
      '/get': {
        target: 'http://localhost:8088',
        changeOrigin: true,
        secure: false,
      },
      '/watch': {
        target: 'http://localhost:8088',
        changeOrigin: true,
        secure: false,
        timeout: 0,
      },
    },
  },
})
