import { expect, test, type Page } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { resolve } from 'node:path'

// Regenerates the documentation screenshots from the seeded mock data
// (internal/e2e/seed_test.go). Run with `make docs-shots`.
const out = resolve(process.cwd(), '..', 'docs', 'public', 'images')

async function login(page: Page, user: string, password: string) {
  await page.goto('/login')
  await page.getByLabel('Username').fill(user)
  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page).not.toHaveURL(/\/login/)
}

async function shot(page: Page, name: string) {
  // Let the canvas and charts finish drawing before the capture.
  await page.waitForLoadState('networkidle')
  await page.waitForTimeout(600)
  await page.screenshot({ path: resolve(out, `${name}.png`) })
}

test.beforeAll(async () => {
  await mkdir(out, { recursive: true })
})

test.beforeEach(async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.addStyleTag({ content: '*{animation:none!important;transition:none!important}' }).catch(() => {})
})

test('user screens', async ({ page }) => {
  await page.goto('/login')
  await shot(page, 'login')

  await login(page, 'alice', 'e2e-password-1')
  await expect(page.getByRole('img', { name: /Treemap of/ })).toBeVisible()
  await shot(page, 'browse-treemap')

  const tab = (name: string) => page.getByRole('tab', { name })
  await tab('Top files').click()
  await shot(page, 'tab-top-files')
  await tab('Extensions').click()
  await shot(page, 'tab-extensions')
  await tab('List').click()
  await shot(page, 'tab-list')

  await page.getByRole('button', { name: /Export/ }).click()
  await shot(page, 'export-menu')
  await page.keyboard.press('Escape')

  await page.getByRole('link', { name: 'Usage' }).click()
  await expect(page).toHaveURL(/\/usage/)
  await shot(page, 'usage')

  await page.getByRole('link', { name: 'Listings' }).click().catch(() => {})
  await shot(page, 'listings')
})

test('admin screens', async ({ page }) => {
  await login(page, 'admin', 'e2e-password-2')
  for (const [path, name] of [
    ['users', 'admin-users'],
    ['mounts', 'admin-mounts'],
    ['scans', 'admin-scans'],
    ['issues', 'admin-issues'],
    ['categories', 'admin-categories'],
    ['settings', 'admin-settings'],
    ['audit', 'admin-audit'],
  ] as const) {
    await page.goto(`/admin/${path}`)
    await shot(page, name)
  }
})
