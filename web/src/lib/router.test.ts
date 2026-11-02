import { expect, it } from 'vitest'
import { match, navigate, router, safeNext, setQuery } from './router.svelte'

it('matches :name segments', () => {
  expect(match('/admin/users/:id', '/admin/users/12')).toEqual({ id: '12' })
  expect(match('/admin/users/:id', '/admin/users')).toBeNull()
  expect(match('/admin/*', '/admin/users/3')).toEqual({ rest: 'users/3' })
  expect(match('/a/:x', '/b/1')).toBeNull()
  expect(match('/a/:x', '/a/%E0%A4%A')).toBeNull()
})

it('safeNext blocks open redirects', () => {
  expect(safeNext('/browse/1.2')).toBe('/browse/1.2')
  for (const bad of ['//evil.com', '/\\evil.com', 'https://x', null, '', '/\t/evil.com', '/\n/evil.com', '/\t\\/evil.com']) expect(safeNext(bad)).toBe('/')
})

it('navigate updates path and query; setQuery edits only the query', () => {
  navigate('/browse/1.2?tab=top')
  expect(router.path).toBe('/browse/1.2')
  expect(router.query).toEqual({ tab: 'top' })
  setQuery({ tab: undefined, depth: '2' })
  expect(router.path).toBe('/browse/1.2')
  expect(router.query).toEqual({ depth: '2' })
})
