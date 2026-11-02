import { fireEvent, render, screen, waitFor } from '@testing-library/svelte'
import { beforeEach, expect, it, vi } from 'vitest'

vi.mock('../../lib/api/client', async (orig) => {
  const m = await orig<typeof import('../../lib/api/client')>()
  return { ...m, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } }
})

import { api, ApiError } from '../../lib/api/client'
import UserPaths from './UserPaths.svelte'

const roots = [{ id: 1, name: 'storage' }, { id: 2, name: 'backup' }]
const path = (id: number, root_id: number, rel_path: string, more = {}) => ({ id, user_id: 5, root_id, rel_path, found: true, redundant: false, created_at: 1, ...more })

function serve(paths: unknown[], rootItems: unknown[] = roots) {
  vi.mocked(api.get).mockImplementation(async (p: string) => ({ items: p === '/admin/roots' ? rootItems : paths }) as never)
}

beforeEach(() => {
  for (const f of [api.get, api.post, api.del]) vi.mocked(f).mockReset()
  vi.mocked(api.post).mockResolvedValue({} as never)
  vi.mocked(api.del).mockResolvedValue(undefined as never)
})

const open = async (root: string) => fireEvent.click(await screen.findByRole('button', { name: `Add a folder of ${root}` }))
const folder = () => screen.getByLabelText(/^Folder/) as HTMLInputElement
const assignFolder = () => screen.getByRole('button', { name: 'Assign folder' }) as HTMLButtonElement

it('groups folders by root and says when a root has none', async () => {
  serve([path(1, 1, 'carol')])
  render(UserPaths, { userId: 5 })
  expect(await screen.findByText('/carol')).toBeTruthy()
  expect(screen.getByRole('heading', { name: 'storage' })).toBeTruthy()
  expect(screen.getByRole('heading', { name: 'backup' })).toBeTruthy()
  expect(screen.getByText('Nothing assigned.')).toBeTruthy()
})

it('does not claim there are no roots while loading', async () => {
  vi.mocked(api.get).mockReturnValue(new Promise(() => {}) as never)
  render(UserPaths, { userId: 5 })
  expect(screen.getByText('Loading…')).toBeTruthy()
  expect(screen.queryByText('No roots exist yet.')).toBeNull()
})

it('says so when no roots exist', async () => {
  serve([], [])
  render(UserPaths, { userId: 5 })
  expect(await screen.findByText('No roots exist yet.')).toBeTruthy()
})

it('still shows a folder whose root is not listed', async () => {
  serve([path(1, 9, 'lost')], roots)
  render(UserPaths, { userId: 5 })
  expect(await screen.findByText('/lost')).toBeTruthy()
})

it('assigns a folder to the root whose form is open', async () => {
  serve([])
  render(UserPaths, { userId: 5 })
  await open('backup')
  await fireEvent.input(folder(), { target: { value: 'weekly' } })
  await fireEvent.click(assignFolder())
  await waitFor(() => expect(api.post).toHaveBeenCalledWith('/admin/users/5/paths', { root_id: 2, rel_path: 'weekly' }))
  await waitFor(() => expect(screen.queryByLabelText(/^Folder/)).toBeNull())
})

it('needs the explicit button to assign a whole root', async () => {
  serve([])
  render(UserPaths, { userId: 5 })
  await open('storage')
  expect(assignFolder().disabled).toBe(true)
  await fireEvent.submit(folder().form!)
  expect(api.post).not.toHaveBeenCalled()
  await fireEvent.click(screen.getByRole('button', { name: 'Assign whole root' }))
  await waitFor(() => expect(api.post).toHaveBeenCalledWith('/admin/users/5/paths', { root_id: 1, rel_path: '' }))
})

it('labels a whole-root assignment and does not offer it again', async () => {
  serve([path(1, 1, '')])
  render(UserPaths, { userId: 5 })
  expect(await screen.findByText('Whole root')).toBeTruthy()
  await open('storage')
  expect(screen.queryByRole('button', { name: 'Assign whole root' })).toBeNull()
})

it('rejects a path with .. inline and does not post', async () => {
  serve([])
  render(UserPaths, { userId: 5 })
  await open('storage')
  await fireEvent.input(folder(), { target: { value: 'a/../b' } })
  expect(screen.getByRole('alert').textContent).toMatch(/\. or \.\. parts/)
  expect(assignFolder().disabled).toBe(true)
  await fireEvent.submit(folder().form!)
  expect(api.post).not.toHaveBeenCalled()
})

it('keeps the form and the typed folder when the server refuses', async () => {
  serve([])
  vi.mocked(api.post).mockRejectedValue(new ApiError(422, 'Unprocessable', 'overlaps a folder of alice', null))
  render(UserPaths, { userId: 5 })
  await open('storage')
  await fireEvent.input(folder(), { target: { value: 'shared' } })
  await fireEvent.click(assignFolder())
  expect(await screen.findByText('overlaps a folder of alice')).toBeTruthy()
  expect(folder().value).toBe('shared')
})

it('closes the form on Escape and on Cancel, and starts empty when reopened', async () => {
  serve([])
  render(UserPaths, { userId: 5 })
  await open('storage')
  await fireEvent.input(folder(), { target: { value: 'x' } })
  await fireEvent.keyDown(folder(), { key: 'Escape' })
  expect(screen.queryByLabelText(/^Folder/)).toBeNull()
  await open('storage')
  expect(folder().value).toBe('')
  await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
  expect(screen.queryByLabelText(/^Folder/)).toBeNull()
})

it('removes a folder after confirming', async () => {
  serve([path(7, 1, 'carol')])
  render(UserPaths, { userId: 5 })
  await fireEvent.click(await screen.findByRole('button', { name: 'Remove' }))
  expect(screen.getByText('Remove carol from this user?')).toBeTruthy()
  const buttons = screen.getAllByRole('button', { name: 'Remove' })
  await fireEvent.click(buttons[buttons.length - 1])
  await waitFor(() => expect(api.del).toHaveBeenCalledWith('/admin/users/5/paths/7'))
})
