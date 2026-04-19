import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    proxy: {
      '/topology': 'http://198.18.133.102:30080',
      '/paths': 'http://198.18.133.102:30080',
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})
