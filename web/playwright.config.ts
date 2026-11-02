import { defineConfig } from '@playwright/test'

// `make test-e2e` seeds HW_E2E_DIR, builds bin/hogwatch and runs this against it.
// With HW_E2E_EXTERNAL=1 the server is already running on 127.0.0.1:18080 (for example
// in WSL while the browser runs on the host) and Playwright only drives the browser.
const external = process.env.HW_E2E_EXTERNAL === '1'
const dir = process.env.HW_E2E_DIR
if (!external && !dir) throw new Error('HW_E2E_DIR is not set; run `make test-e2e`')

export default defineConfig({
  testDir: 'e2e',
  testIgnore: 'docs-shots.spec.ts', // run by `make docs-shots`
  fullyParallel: false,
  workers: 1,
  reporter: [['list']],
  use: { baseURL: 'http://127.0.0.1:18080', acceptDownloads: true },
  webServer: external
    ? undefined
    : {
        command: '../bin/hogwatch serve',
        url: 'http://127.0.0.1:18080/healthz',
        reuseExistingServer: false,
        timeout: 30_000,
        env: {
          HOGWATCH_DATA_DIR: dir!,
          HOGWATCH_LISTEN: '127.0.0.1:18080',
          HOGWATCH_BASE_URL: 'http://127.0.0.1:18080',
        },
      },
})
