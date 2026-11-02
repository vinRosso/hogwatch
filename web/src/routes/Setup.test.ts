import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import { beforeEach, expect, it, vi } from 'vitest'

vi.mock('../lib/api/client', async (orig) => {
  const m = await orig<typeof import('../lib/api/client')>()
  return { ...m, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } }
})

vi.mock('../lib/router.svelte', async (orig) => ({ ...(await orig<typeof import('../lib/router.svelte')>()), hardNavigate: vi.fn() }))

import { api, ApiError } from '../lib/api/client'
import { t } from '../lib/i18n/en'
import { hardNavigate } from '../lib/router.svelte'
import Setup from './Setup.svelte'

beforeEach(() => {
  vi.mocked(api.get).mockReset().mockResolvedValue({ needed: true } as never)
  vi.mocked(api.post).mockReset()
  vi.mocked(hardNavigate).mockReset()
})

async function submit() {
  await fireEvent.input(await screen.findByLabelText(t.setup.username), { target: { value: 'root' } })
  await fireEvent.input(screen.getByLabelText(t.setup.password), { target: { value: 'pw-long-enough' } })
  await fireEvent.submit(screen.getByRole('button', { name: t.setup.submit }))
}

it('posts the first admin without a token, signs in and goes home', async () => {
  vi.mocked(api.post).mockResolvedValue(undefined as never)
  render(Setup)
  await submit()
  await waitFor(() => expect(api.post).toHaveBeenCalled())
  expect(vi.mocked(api.post).mock.calls[0][0]).toBe('/setup')
  expect(vi.mocked(api.post).mock.calls[0][1]).toMatchObject({ username: 'root' })
  expect(vi.mocked(api.post).mock.calls[0][1]).not.toHaveProperty('token')
  await waitFor(() => expect(hardNavigate).toHaveBeenCalledWith('/'))
})

it('says setup is done on a 404', async () => {
  vi.mocked(api.post).mockRejectedValue(new ApiError(404, 'Not Found', '', null))
  render(Setup)
  await submit()
  expect(await screen.findByText(t.setup.done)).toBeTruthy()
})

it('says setup is done when an admin already exists', async () => {
  vi.mocked(api.get).mockResolvedValue({ needed: false } as never)
  render(Setup)
  expect(await screen.findByText(t.setup.done)).toBeTruthy()
})
