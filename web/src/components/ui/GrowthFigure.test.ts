import { render, screen } from '@testing-library/svelte'
import { expect, it } from 'vitest'
import GrowthFigure from './GrowthFigure.svelte'

it('shows a negative figure with the minus sign in the dn class', () => {
  render(GrowthFigure, { bytes: -3e9 })
  const el = screen.getByText('−3.00 GB')
  expect(el.classList.contains('dn')).toBe(true)
})

it('shows a positive figure with a plus sign in the up class', () => {
  render(GrowthFigure, { bytes: 1500 })
  expect(screen.getByText('+1.50 kB').classList.contains('up')).toBe(true)
})

it('shows a dash for null', () => {
  render(GrowthFigure, { bytes: null })
  expect(screen.getByText('—')).toBeTruthy()
})
