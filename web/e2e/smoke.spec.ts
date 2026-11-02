import { expect, test, type Page } from '@playwright/test'
import { readFile } from 'node:fs/promises'

// The accounts are seeded by internal/e2e/seed_test.go.
async function login(page: Page, user: string, password: string) {
  await page.goto('/login')
  await page.getByLabel('Username').fill(user)
  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page).not.toHaveURL(/\/login/)
}

test('a user browses hostile names safely, exports, and an admin sees the ranking', async ({ page }) => {
  // Any script run by a hostile name would raise an alert.
  const dialogs: string[] = []
  page.on('dialog', (d) => {
    dialogs.push(d.message())
    void d.dismiss()
  })

  await login(page, 'alice', 'e2e-password-1')
  await expect(page).toHaveURL(/\/browse\/\d+\.\d+/)
  const canvas = page.getByRole('img', { name: /Treemap of/ })
  await expect(canvas).toBeVisible()
  const box = await canvas.boundingBox()
  expect(box!.width).toBeGreaterThan(0)
  expect(box!.height).toBeGreaterThan(0)

  // The largest block (media) is laid out first, at the top left of the canvas.
  await canvas.click({ position: { x: 20, y: 20 } })
  await expect(page.getByRole('navigation', { name: 'Location' })).toContainText('media')

  // Back to the anchor, and read the hostile names in the List tab.
  // The treemap draws two levels, so the click lands on movies inside media: go up twice.
  const location = page.getByRole('navigation', { name: 'Location' })
  await page.getByRole('button', { name: 'Up one level' }).click()
  await expect(location).not.toContainText('movies')
  await page.getByRole('button', { name: 'Up one level' }).click()
  await expect(location).not.toContainText('media')
  const list = page.getByRole('list', { name: 'List' })
  await expect(list.getByText('a␊b')).toBeVisible()
  await expect(list.getByText('x\\u{202E}y')).toBeVisible()
  await expect(list.getByText('<img src=x onerror=alert(1)>')).toBeVisible()
  expect(await page.locator('img').count()).toBe(0)

  // Export: the CSV guards the formula name with a leading apostrophe.
  await page.getByRole('button', { name: /Export/ }).click()
  const [download] = await Promise.all([page.waitForEvent('download'), page.getByRole('button', { name: 'Tree CSV' }).click()])
  const csv = await readFile(await download.path(), 'utf8')
  const cmdRow = csv.split('\n').find((l) => l.includes('cmd'))
  expect(cmdRow, csv).toBeTruthy()
  expect(cmdRow!.startsWith("'=cmd") || cmdRow!.includes(",'=cmd") || cmdRow!.includes("\"'=cmd")).toBe(true)

  // Usage: root storage has a growth figure.
  await page.getByRole('link', { name: 'Usage' }).click()
  const storage = page.locator('li', { hasText: 'storage' }).first()
  await expect(storage).toContainText(/[+−]\d/)

  // A stale reference lands on the start folder with the notice.
  await page.goto('/browse/999.1')
  await expect(page.getByText(/changed in a newer scan/)).toBeVisible()

  // Admin.
  await page.getByRole('button', { name: /Account menu/ }).click()
  await page.getByRole('menuitem', { name: 'Sign out' }).click()
  await expect(page).toHaveURL(/\/login/)
  await login(page, 'admin', 'e2e-password-2')
  await page.goto('/admin')
  await expect(page).toHaveURL(/\/admin\/users$/)
  await expect(page.getByRole('heading', { name: 'Users' })).toBeVisible()
  await expect(page.getByRole('columnheader', { name: /Growth/ })).toBeVisible()
  await expect(page.getByRole('cell', { name: 'alice', exact: true })).toBeVisible()
  await expect(page.getByRole('cell', { name: 'admin', exact: true })).toBeVisible()

  expect(dialogs).toEqual([])
})
