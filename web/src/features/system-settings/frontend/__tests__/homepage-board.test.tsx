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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { parseUsageCsv } from '../daily-usage-data'
import { FrontendSettings } from '../index'

vi.mock('@/lib/api', () => ({
  api: { get: vi.fn(), put: vi.fn(), delete: vi.fn() },
}))

const animations = Object.getOwnPropertyDescriptor(
  Element.prototype,
  'getAnimations'
)
beforeAll(() => {
  Object.defineProperty(Element.prototype, 'getAnimations', {
    configurable: true,
    value: () => [],
  })
})
afterAll(() => {
  if (animations) {
    Object.defineProperty(Element.prototype, 'getAnimations', animations)
  } else {
    delete (Element.prototype as { getAnimations?: () => unknown[] })
      .getAnimations
  }
})
afterEach(() => vi.clearAllMocks())

function renderSettings(raw?: string) {
  vi.mocked(api.get).mockImplementation(async (url) => {
    if (url === '/api/option/daily_usage') {
      return {
        data: {
          success: true,
          data: {
            source: 'manual',
            timezone: 'UTC',
            from: '2026-09-02',
            to: '2026-10-01',
            days: 30,
            total: 12,
            recorded_days: 2,
            items: [
              { date: '2026-09-30', request_count: 12 },
              { date: '2026-09-29', request_count: 0 },
            ],
          },
        },
      }
    }
    return {
      data: {
        success: true,
        data: raw ? [{ key: 'frontend.homepage_board', value: raw }] : [],
      },
    }
  })
  vi.mocked(api.put).mockResolvedValue({ data: { success: true } })
  vi.mocked(api.delete).mockResolvedValue({ data: { success: true } })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <FrontendSettings />
    </QueryClientProvider>
  )
}

it('loads the saved homepage models and submits their new order with the selected default', async () => {
  const user = userEvent.setup()
  renderSettings('{"models":["model-a","model-b"],"default_model":"model-a"}')
  const models = await screen.findByRole('textbox', {
    name: 'Displayed models',
  })
  expect(models).toHaveValue('model-a\nmodel-b')
  await user.clear(models)
  await user.type(models, 'model-b{enter}model-a')
  const defaultModel = screen.getByRole('textbox', {
    name: 'Default selected model',
  })
  await user.clear(defaultModel)
  await user.type(defaultModel, 'model-b')
  await user.click(screen.getByRole('button', { name: 'Save settings' }))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'frontend.homepage_board',
      value: JSON.stringify({
        models: ['model-b', 'model-a'],
        default_model: 'model-b',
      }),
    })
  )
})

it('CSV validation rejects future dates, invalid leap dates, non-integers and out-of-range counts before saving', () => {
  for (const csv of [
    '2026-10-02,1',
    '2025-02-29,1',
    '2026-09-30,-1',
    '2026-09-30,1.5',
    '2026-09-30,1e3',
    '2026-09-30,1000000000001',
  ]) {
    expect(() => parseUsageCsv(csv, '2026-10-01')).toThrow()
  }
  expect(
    parseUsageCsv('date,request_count\n2024-02-29,0', '2026-10-01')
  ).toEqual([{ date: '2024-02-29', request_count: 0 }])
})

it('daily usage tab saves a zero count for one UTC date without altering homepage options', async () => {
  const user = userEvent.setup()
  renderSettings()
  await user.click(
    await screen.findByRole('tab', { name: 'Daily usage display' })
  )
  const date = await screen.findByLabelText('Usage date (UTC)', {
    selector: 'input',
  })
  expect(date).toHaveValue('2026-10-01')
  fireEvent.change(date, { target: { value: '2026-09-30' } })
  await user.type(
    screen.getByRole('spinbutton', { name: 'Request count' }),
    '0'
  )
  await user.click(screen.getByRole('button', { name: 'Save daily usage' }))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/daily_usage', {
      items: [{ date: '2026-09-30', request_count: 0 }],
    })
  )
  expect(api.put).not.toHaveBeenCalledWith('/api/option/', expect.anything())
})

it('bulk usage accepts a CSV header and sends every date as a single atomic update', async () => {
  const user = userEvent.setup()
  renderSettings()
  await user.click(
    await screen.findByRole('tab', { name: 'Daily usage display' })
  )
  await user.click(await screen.findByRole('tab', { name: 'Batch CSV' }))
  await user.type(
    screen.getByRole('textbox', { name: 'Daily usage CSV' }),
    'date,request_count{enter}2026-09-30,10{enter}2026-09-29,0'
  )
  await user.click(screen.getByRole('button', { name: 'Save usage batch' }))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/daily_usage', {
      items: [
        { date: '2026-09-30', request_count: 10 },
        { date: '2026-09-29', request_count: 0 },
      ],
    })
  )
})

it('duplicate CSV dates are rejected and switching modes preserves the unsaved draft', async () => {
  const user = userEvent.setup()
  renderSettings()
  await user.click(
    await screen.findByRole('tab', { name: 'Daily usage display' })
  )
  await user.click(await screen.findByRole('tab', { name: 'Batch CSV' }))
  const csv = screen.getByRole('textbox', { name: 'Daily usage CSV' })
  await user.type(csv, '2026-09-30,10{enter}2026-09-30,20')
  await user.click(screen.getByRole('button', { name: 'Save usage batch' }))
  expect(
    await screen.findByText(
      'Enter 1 to 366 unique dates and integer counts as date,request_count'
    )
  ).toBeVisible()
  expect(api.put).not.toHaveBeenCalled()
  await user.click(screen.getByRole('tab', { name: 'Single date' }))
  await user.click(screen.getByRole('tab', { name: 'Batch CSV' }))
  expect(screen.getByRole('textbox', { name: 'Daily usage CSV' })).toHaveValue(
    '2026-09-30,10\n2026-09-30,20'
  )
})

it('deleting a daily record requires confirmation and edit switches back to the date form', async () => {
  const user = userEvent.setup()
  renderSettings()
  await user.click(
    await screen.findByRole('tab', { name: 'Daily usage display' })
  )
  await user.click(await screen.findByRole('tab', { name: 'Batch CSV' }))
  await user.click(
    screen.getByRole('button', { name: 'Edit usage for 2026-09-29' })
  )
  expect(screen.getByRole('spinbutton', { name: 'Request count' })).toHaveValue(
    0
  )
  await user.click(
    screen.getByRole('button', { name: 'Delete usage for 2026-09-29' })
  )
  expect(api.delete).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: 'Delete daily record' }))
  await waitFor(() =>
    expect(api.delete).toHaveBeenCalledWith(
      '/api/option/daily_usage/2026-09-29'
    )
  )
})

it('failed business responses keep the count draft and do not claim a successful save', async () => {
  const user = userEvent.setup()
  renderSettings()
  vi.mocked(api.put).mockResolvedValue({
    data: { success: false, message: 'daily usage rejected' },
  })
  await user.click(
    await screen.findByRole('tab', { name: 'Daily usage display' })
  )
  const count = await screen.findByRole('spinbutton', { name: 'Request count' })
  await user.type(count, '123')
  await user.click(screen.getByRole('button', { name: 'Save daily usage' }))
  await waitFor(() => expect(api.put).toHaveBeenCalled())
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Save daily usage' })
    ).toBeEnabled()
  )
  expect(count).toHaveValue(123)
})

it('rejects a default outside the displayed models without saving', async () => {
  const user = userEvent.setup()
  renderSettings()
  const models = await screen.findByRole('textbox', {
    name: 'Displayed models',
  })
  await user.type(models, 'model-a')
  await user.click(screen.getByRole('button', { name: 'Save settings' }))
  expect(
    await screen.findByText('Default model must be one of the displayed models')
  ).toBeVisible()
  expect(api.put).not.toHaveBeenCalled()
})
