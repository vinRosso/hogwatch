import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../lib/api/client', async (orig) => {
  const m = await orig<typeof import('../lib/api/client')>()
  return { ...m, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } }
})

vi.mock('../lib/router.svelte', async (orig) => ({ ...(await orig<typeof import('../lib/router.svelte')>()), hardNavigate: vi.fn() }))

import { api, ApiError } from '../lib/api/client'
import { t } from '../lib/i18n/en'
import { hardNavigate, navigate } from '../lib/router.svelte'
import { session } from '../lib/session.svelte'
import Login from './Login.svelte'

function config(local: boolean, oidc: boolean) {
  vi.mocked(api.get).mockImplementation(async (path: string) => {
    if (path === '/auth/config') return { local, oidc } as never
    if (path === '/setup/status') return { needed: false } as never
    return { items: [] } as never
  })
}

beforeEach(() => {
  vi.mocked(api.get).mockReset()
  vi.mocked(api.post).mockReset()
  vi.mocked(hardNavigate).mockReset()
  session.authConfig = null
  navigate('/login')
})

async function fill() {
  await fireEvent.input(await screen.findByLabelText(t.login.username), { target: { value: 'alice' } })
  await fireEvent.input(screen.getByLabelText(t.login.password), { target: { value: 'pw' } })
  await fireEvent.submit(screen.getByRole('button', { name: t.login.submit }))
}

describe('Login', () => {
  it('local only shows the form and no single sign-on link', async () => {
    config(true, false)
    render(Login)
    expect(await screen.findByLabelText(t.login.username)).toBeTruthy()
    expect(screen.queryByText(t.login.sso)).toBeNull()
  })

  it('oidc only shows just the single sign-on link', async () => {
    config(false, true)
    render(Login)
    const a = await screen.findByText(t.login.sso)
    expect(a.getAttribute('href')).toBe('/api/v1/auth/oidc/start')
    expect(screen.queryByLabelText(t.login.username)).toBeNull()
  })

  it('shows the text for an oidc_error code', async () => {
    config(true, true)
    navigate('/login?oidc_error=ask_admin')
    render(Login)
    expect(await screen.findByText(t.oidcError.ask_admin)).toBeTruthy()
  })

  it('shows the retry delay on a 429', async () => {
    config(true, false)
    vi.mocked(api.post).mockRejectedValue(new ApiError(429, 'Too many', '', 12))
    render(Login)
    await fill()
    expect(await screen.findByText('Too many attempts. Try again in 12 s.')).toBeTruthy()
  })

  it('shows one generic message on a 401', async () => {
    config(true, false)
    vi.mocked(api.post).mockRejectedValue(new ApiError(401, 'Unauthorized', 'no such user', null))
    render(Login)
    await fill()
    expect(await screen.findByText(t.login.invalid)).toBeTruthy()
    expect(screen.queryByText('no such user')).toBeNull()
  })

  it('ignores a hostile next parameter after signing in', async () => {
    config(true, false)
    vi.mocked(api.post).mockResolvedValue(undefined as never)
    navigate('/login?next=%2F%2Fevil.com')
    render(Login)
    await fill()
    await waitFor(() => expect(hardNavigate).toHaveBeenCalledWith('/'))
  })
})

describe('Login redirect', () => {
  it('leaves through a full page load so nothing from a previous account survives', async () => {
    config(true, false)
    vi.mocked(api.post).mockResolvedValue(undefined as never)
    navigate('/login?next=%2Fbrowse%2F5.10')
    render(Login)
    await fill()
    await waitFor(() => expect(hardNavigate).toHaveBeenCalledWith('/browse/5.10'))
  })
})
