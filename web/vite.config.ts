import { svelte } from '@sveltejs/vite-plugin-svelte'
import { svelteTesting } from '@testing-library/svelte/vite'
import { defineConfig } from 'vitest/config'

// https://vite.dev/config/
export default defineConfig({
  plugins: [svelte(), svelteTesting()],
  define: {
    __HW_VERSION__: JSON.stringify(process.env.HOGWATCH_VERSION ?? 'dev'),
    __HW_COMMIT__: JSON.stringify(process.env.HOGWATCH_COMMIT ?? ''),
  },
  server: {
    proxy: { '/api': process.env.HOGWATCH_API ?? 'http://127.0.0.1:8080' },
    // inotify does not see Windows-side writes under WSL's /mnt/c; polling does.
    watch: { usePolling: process.env.HOGWATCH_WATCH_POLL === '1', interval: 300 },
  },
  test: {
    environment: 'jsdom',
    include: ['src/**/*.test.ts'],
    setupFiles: ['src/test-setup.ts'],
  },
})
