import { render, screen } from '@testing-library/svelte'
import { expect, it } from 'vitest'
import type { MyUsage } from '../../lib/api/types'
import { t } from '../../lib/i18n/en'
import { session } from '../../lib/session.svelte'
import QuotaBar from './QuotaBar.svelte'

const usage = (alloc: number, quota: number | null): MyUsage => ({
  quota_bytes: quota,
  quota_basis: 'allocated',
  total: { files: 1, bytes_logical: 0, bytes_alloc: alloc },
  growth_bytes: null,
  mounts: [],
  paths: [],
})

it('shows the figure, the 80 % warning and 80/95 ticks', () => {
  session.usage = usage(1.82e12, 2e12)
  const { container } = render(QuotaBar)
  expect(screen.getByRole('progressbar').getAttribute('aria-valuenow')).toBe('91')
  expect(screen.getByText('1.82 TB / 2.00 TB')).toBeTruthy()
  expect(screen.getByText(t.quota.warn80)).toBeTruthy()
  expect(container.querySelectorAll('.tick').length).toBe(2)
})

it('shows the 95 % warning', () => {
  session.usage = usage(1.9e12, 2e12)
  render(QuotaBar)
  expect(screen.getByText(t.quota.warn95)).toBeTruthy()
  expect(screen.queryByText(t.quota.warn80)).toBeNull()
})

it('is calm without a quota', () => {
  session.usage = usage(1e12, null)
  render(QuotaBar)
  expect(screen.queryByRole('progressbar')).toBeNull()
  expect(screen.getByText(t.quota.none)).toBeTruthy()
})
