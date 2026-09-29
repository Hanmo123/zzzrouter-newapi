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
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { FrontendSettings } from '../index'

vi.mock('@/lib/api', () => ({ api: { get: vi.fn(), put: vi.fn() } }))

afterEach(() => vi.clearAllMocks())

function renderSettings(raw?: string) {
  vi.mocked(api.get).mockResolvedValue({
    data: {
      success: true,
      data: raw ? [{ key: 'frontend.homepage_board', value: raw }] : [],
    },
  })
  vi.mocked(api.put).mockResolvedValue({ data: { success: true } })
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
