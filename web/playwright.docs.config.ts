import { defineConfig } from '@playwright/test'

// `make docs-shots` regenerates docs/public/images from the e2e seed. The server
// is already running on 127.0.0.1:18080 (HW_E2E_EXTERNAL-style); this only drives the browser.
export default defineConfig({
  testDir: 'e2e',
  testMatch: 'docs-shots.spec.ts',
  fullyParallel: false,
  workers: 1,
  reporter: [['list']],
  use: {
    baseURL: 'http://127.0.0.1:18080',
    viewport: { width: 1440, height: 900 },
    deviceScaleFactor: 2,
    colorScheme: 'light',
  },
})
