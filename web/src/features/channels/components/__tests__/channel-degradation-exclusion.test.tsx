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

import { ChannelDegradationExclusion } from '../channel-degradation-exclusion'

function renderSetting(canWrite = true) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <ChannelDegradationExclusion channelId={7} canWrite={canWrite} open />
    </QueryClientProvider>
  )
}

afterEach(() => vi.restoreAllMocks())

it('defaults to not excluding the channel and saves the reverse switch independently', async () => {
  let skip = false
  vi.spyOn(api, 'get').mockImplementation(async () => ({
    data: { success: true, data: { skip_degradation_check: skip } },
  }))
  const put = vi.spyOn(api, 'put').mockImplementation(async () => {
    skip = true
    return { data: { success: true } }
  })
  renderSetting()
  const control = await screen.findByRole('switch', {
    name: 'Do not check degradation for this channel',
  })
  expect(control).not.toBeChecked()
  await userEvent.setup().click(control)
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith('/api/channel/7/degradation-exclusion', {
      skip_degradation_check: true,
    })
  )
  await waitFor(() => expect(control).toBeChecked())
})

it('shows the persisted policy read-only when the administrator lacks write permission', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { skip_degradation_check: true } },
  })
  const put = vi.spyOn(api, 'put')
  renderSetting(false)
  const control = await screen.findByRole('switch', {
    name: 'Do not check degradation for this channel',
  })
  await waitFor(() => expect(control).toBeChecked())
  expect(control).toHaveAttribute('data-disabled')
  await userEvent.setup().click(control)
  expect(put).not.toHaveBeenCalled()
})

it('leaves the previous value visible when the server rejects a change', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { skip_degradation_check: false } },
  })
  vi.spyOn(api, 'put').mockResolvedValue({
    data: { success: false, message: 'Cannot update policy' },
  })
  renderSetting()
  const control = await screen.findByRole('switch', {
    name: 'Do not check degradation for this channel',
  })
  await userEvent.setup().click(control)
  await waitFor(() => expect(control).not.toBeChecked())
})
