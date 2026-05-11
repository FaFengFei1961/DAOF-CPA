import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// https://vite.dev/config/
export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
  ],
  server: {
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:3000',
        changeOrigin: true,
      }
    }
  },
  build: {
    chunkSizeWarningLimit: 600,
    rollupOptions: {
      output: {
        // Rolldown 要求函数形式：根据 import 路径决定 chunk 归属
        manualChunks(id) {
          if (!id.includes('node_modules')) return undefined;
          if (id.includes('react-i18next') || id.includes('i18next') ||
              id.match(/[\\/]react[\\/]/) || id.includes('react-dom')) {
            return 'vendor-react';
          }
          if (id.includes('lucide-react') || id.includes('react-hot-toast')) {
            return 'vendor-ui';
          }
          if (id.includes('recharts') || id.includes('d3-')) {
            return 'vendor-charts';
          }
          return 'vendor-misc';
        },
      },
    },
  },
})
