import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    host: '0.0.0.0',
    port: 5173,
    strictPort: true,
    allowedHosts: ['localhost', 'host.docker.internal'],
    ws: { protocol: 'ws', host: 'localhost', clientPort: 9080 },
  },
})
