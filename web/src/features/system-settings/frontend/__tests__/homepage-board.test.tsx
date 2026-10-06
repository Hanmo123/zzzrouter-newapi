/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { render, screen } from '@testing-library/react'
import i18next from 'i18next'
import { afterEach, expect, it, vi } from 'vitest'

import zhMessages from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'

import { parseUsageCsv } from '../daily-usage-data'
import { FrontendSettings } from '../index'

vi.mock('@/lib/api', () => ({
  api: { get: vi.fn(), put: vi.fn(), delete: vi.fn() },
}))
afterEach(() => vi.clearAllMocks())

it('public platform configuration is read-only and never queries or writes manual display data', () => {
  render(<FrontendSettings />)
  expect(screen.getByText('Automatic public platform data')).toBeVisible()
  expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
  expect(screen.queryByRole('spinbutton')).not.toBeInTheDocument()
  expect(screen.queryByRole('slider')).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /Save/ })).not.toBeInTheDocument()
  expect(api.get).not.toHaveBeenCalled()
  expect(api.put).not.toHaveBeenCalled()
  expect(api.delete).not.toHaveBeenCalled()
})

it('the automatic data notice responds to interface language changes', async () => {
  i18next.addResourceBundle('zh', 'translation', zhMessages.translation)
  await i18next.changeLanguage('zh')
  try {
    render(<FrontendSettings />)
    expect(
      screen.getByText(zhMessages.translation['Automatic public platform data'])
    ).toBeVisible()
  } finally {
    await i18next.changeLanguage('en')
  }
})

it('retained legacy CSV validation still rejects invalid token values and dates', () => {
  expect(parseUsageCsv('date,token_count\n2999-01-01,1234')).toEqual([
    { date: '2999-01-01', token_count: 1234 },
  ])
  for (const value of [
    '2026-02-30,1',
    '2026-09-30,-1',
    '2026-09-30,1.5',
    '2026-09-30,1e3',
    '2026-09-30,1000000000001',
    '2026-09-30,1\n2026-09-30,2',
  ]) {
    expect(() => parseUsageCsv(value)).toThrow()
  }
})
