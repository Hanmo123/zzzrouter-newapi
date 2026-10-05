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
import { render, screen, waitFor } from '@testing-library/react'
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
            unit: 'tokens',
            source: 'manual',
            timezone: 'UTC',
            from: '2026-09-01',
            to: '2026-09-30',
            days: 30,
            total: 12,
            recorded_days: 2,
            items: [
              { date: '2026-09-28', token_count: null },
              { date: '2026-09-29', token_count: 0 },
              { date: '2026-09-30', token_count: 12 },
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
  const selected = screen.getByRole('textbox', {
    name: 'Default selected model',
  })
  await user.clear(selected)
  await user.type(selected, 'model-b')
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

it('CSV validates token integers and ended UTC dates rather than request counts', () => {
  for (const csv of [
    '2026-10-01,1',
    '2025-02-29,1',
    '2026-09-30,-1',
    '2026-09-30,1.5',
    '2026-09-30,1e3',
    '2026-09-30,1000000000001',
    'date,request_count\n2026-09-30,1',
  ]) {
    expect(() => parseUsageCsv(csv, '2026-09-30')).toThrow()
  }
  expect(parseUsageCsv('date,token_count\n2024-02-29,0', '2026-09-30')).toEqual(
    [{ date: '2024-02-29', token_count: 0 }]
  )
})

it('the default token editor exposes daily bars and only saves edited days atomically', async () => {
  const user = userEvent.setup()
  renderSettings()
  await user.click(
    await screen.findByRole('tab', { name: 'Daily usage display' })
  )
  const missing = await screen.findByRole('slider', {
    name: 'Tokens for 2026-09-28',
  })
  const zero = screen.getByRole('slider', { name: 'Tokens for 2026-09-29' })
  expect(
    screen.getByRole('button', { name: 'Save token chart' })
  ).toBeDisabled()
  missing.focus()
  await user.keyboard('{ArrowUp}')
  expect(missing).toHaveAttribute('aria-valuenow', '1')
  zero.focus()
  await user.keyboard('{ArrowUp}{ArrowUp}')
  expect(api.put).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: 'Save token chart' }))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/daily_usage', {
      items: [
        { date: '2026-09-28', token_count: 1 },
        { date: '2026-09-29', token_count: 2 },
      ],
    })
  )
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Save token chart' })
    ).toBeDisabled()
  )
})

it('exact token input updates a bar, supports zero and reset restores the persisted value', async () => {
  const user = userEvent.setup()
  renderSettings()
  await user.click(
    await screen.findByRole('tab', { name: 'Daily usage display' })
  )
  const input = await screen.findByRole('spinbutton', {
    name: 'Tokens for 2026-09-30',
  })
  await user.clear(input)
  await user.type(input, '0')
  expect(
    screen.getByRole('slider', { name: 'Tokens for 2026-09-30' })
  ).toHaveAttribute('aria-valuenow', '0')
  expect(screen.getByLabelText('Daily history range')).toBeDisabled()
  await user.click(screen.getByRole('button', { name: 'Reset changes' }))
  expect(screen.getByLabelText('Daily history range')).toBeEnabled()
  expect(input).toHaveValue(12)
  expect(
    screen.getByRole('button', { name: 'Save token chart' })
  ).toBeDisabled()
})

it('a rejected bar update preserves edits and enables a deliberate retry', async () => {
  const user = userEvent.setup()
  renderSettings()
  vi.mocked(api.put).mockResolvedValue({
    data: { success: false, message: 'rejected' },
  })
  await user.click(
    await screen.findByRole('tab', { name: 'Daily usage display' })
  )
  const input = await screen.findByRole('spinbutton', {
    name: 'Tokens for 2026-09-30',
  })
  await user.clear(input)
  await user.type(input, '123')
  await user.click(screen.getByRole('button', { name: 'Save token chart' }))
  await waitFor(() => expect(api.put).toHaveBeenCalled())
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Save token chart' })
    ).toBeEnabled()
  )
  expect(input).toHaveValue(123)
})

it('single-date entry defaults to yesterday so saving is inside the public range', async () => {
  const user = userEvent.setup()
  renderSettings()
  await user.click(
    await screen.findByRole('tab', { name: 'Daily usage display' })
  )
  await user.click(await screen.findByRole('tab', { name: 'Single date' }))
  const date = await screen.findByLabelText('Usage date (UTC)', {
    selector: 'input',
  })
  expect(date).toHaveValue('2026-09-30')
  expect(date).toHaveAttribute('max', '2026-09-30')
  await user.type(
    screen.getByRole('spinbutton', { name: 'Daily token count' }),
    '0'
  )
  await user.click(screen.getByRole('button', { name: 'Save daily usage' }))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/daily_usage', {
      items: [{ date: '2026-09-30', token_count: 0 }],
    })
  )
})

it('bulk usage sends all CSV token values in one update', async () => {
  const user = userEvent.setup()
  renderSettings()
  await user.click(
    await screen.findByRole('tab', { name: 'Daily usage display' })
  )
  await user.click(await screen.findByRole('tab', { name: 'Batch CSV' }))
  await user.type(
    screen.getByRole('textbox', { name: 'Daily usage CSV' }),
    'date,token_count{enter}2026-09-30,10{enter}2026-09-29,0'
  )
  await user.click(screen.getByRole('button', { name: 'Save usage batch' }))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/daily_usage', {
      items: [
        { date: '2026-09-30', token_count: 10 },
        { date: '2026-09-29', token_count: 0 },
      ],
    })
  )
})

it('duplicate CSV dates are rejected and switching modes preserves the draft', async () => {
  const user = userEvent.setup()
  renderSettings()
  await user.click(
    await screen.findByRole('tab', { name: 'Daily usage display' })
  )
  await user.click(await screen.findByRole('tab', { name: 'Batch CSV' }))
  await user.type(
    screen.getByRole('textbox', { name: 'Daily usage CSV' }),
    '2026-09-30,10{enter}2026-09-30,20'
  )
  await user.click(screen.getByRole('button', { name: 'Save usage batch' }))
  expect(
    await screen.findByText(
      'Enter 1 to 366 unique dates and integer counts as date,token_count'
    )
  ).toBeVisible()
  expect(api.put).not.toHaveBeenCalled()
  await user.click(screen.getByRole('tab', { name: 'Drag token bars' }))
  await user.click(screen.getByRole('tab', { name: 'Batch CSV' }))
  expect(screen.getByRole('textbox', { name: 'Daily usage CSV' })).toHaveValue(
    '2026-09-30,10\n2026-09-30,20'
  )
})

it('deletion requires confirmation and edit opens the precise token form', async () => {
  const user = userEvent.setup()
  renderSettings()
  await user.click(
    await screen.findByRole('tab', { name: 'Daily usage display' })
  )
  await user.click(
    await screen.findByRole('button', { name: 'Edit usage for 2026-09-29' })
  )
  expect(
    screen.getByRole('spinbutton', { name: 'Daily token count' })
  ).toHaveValue(0)
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

it('a failed single-day response keeps the draft for retry', async () => {
  const user = userEvent.setup()
  renderSettings()
  vi.mocked(api.put).mockResolvedValue({
    data: { success: false, message: 'daily usage rejected' },
  })
  await user.click(
    await screen.findByRole('tab', { name: 'Daily usage display' })
  )
  await user.click(await screen.findByRole('tab', { name: 'Single date' }))
  const count = await screen.findByRole('spinbutton', {
    name: 'Daily token count',
  })
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

it('rejects a homepage default outside the displayed models without saving', async () => {
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
