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
import { zodResolver } from '@hookform/resolvers/zod'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  Outlet,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useForm } from 'react-hook-form'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { Button } from '@/components/ui/button'
import { Form } from '@/components/ui/form'
import { api } from '@/lib/api'
import { Route as ChannelsRoute } from '@/routes/_authenticated/channels'
import { useAuthStore } from '@/stores/auth-store'

import {
  CHANNEL_FORM_DEFAULT_VALUES,
  channelFormSchema,
  transformChannelToFormDefaults,
  transformFormDataToCreatePayload,
  transformFormDataToUpdatePayload,
} from '../../lib/channel-form'
import { channelSchema } from '../../types'
import { ChannelObservationFields } from '../channel-observation-fields'
import { ChannelProviderFields } from '../channel-provider-fields'
import { ChannelsProvider } from '../channels-provider'
import { ChannelsTable } from '../channels-table'

function ChannelForm(props: { onSave: (data: unknown) => void }) {
  const form = useForm({
    resolver: zodResolver(channelFormSchema),
    defaultValues: {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'channel',
      models: 'model-a',
      key: 'key-one\nkey-two',
      provider_id: 2,
      upstream_ratio: 0.75,
    },
  })
  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(props.onSave)}>
        <ChannelProviderFields control={form.control} />
        <ChannelObservationFields control={form.control} />
        <Button type='submit'>Save channel</Button>
      </form>
    </Form>
  )
}

async function renderFields(onSave = vi.fn()) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const root = createRootRoute({
    component: () => <ChannelForm onSave={onSave} />,
  })
  const router = createRouter({
    routeTree: root,
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  await router.load()
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  return onSave
}

const first = { id: 2, name: 'Upstream A' }
const second = { id: 3, name: 'Upstream B' }

afterEach(() => {
  localStorage.clear()
  useAuthStore.getState().auth.reset()
})

describe('channel provider configuration', () => {
  it('edits tri-state observations, preserves zero TTFT, and clears unknown measurements', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: { items: [first], total: 1 } },
    })
    const save = await renderFields()
    const user = userEvent.setup()
    const ratio = screen.getByRole('spinbutton', { name: 'Public ratio' })
    await user.clear(ratio)
    await user.type(ratio, '0')
    await user.click(screen.getByRole('button', { name: 'Save channel' }))
    expect(
      await screen.findByText('Public ratio must be greater than zero')
    ).toBeVisible()
    expect(save).not.toHaveBeenCalled()
    await user.clear(ratio)
    await user.type(ratio, '0.0125')
    const online = screen.getByRole('combobox', { name: 'Callable (online)' })
    expect(online).toHaveValue('Unknown')
    await user.click(online)
    await user.click(screen.getByRole('option', { name: 'Offline' }))
    await user.click(screen.getByRole('combobox', { name: 'Is degraded' }))
    await user.click(screen.getByRole('option', { name: 'Not degraded' }))
    const ttft = screen.getByRole('spinbutton', {
      name: 'Time to first token (ms)',
    })
    await user.type(ttft, '0')
    const checked = screen.getByLabelText('Last checked')
    fireEvent.change(checked, { target: { value: '2026-09-24T12:34:56' } })
    await user.click(screen.getByRole('button', { name: 'Save channel' }))
    await waitFor(() =>
      expect(save).toHaveBeenLastCalledWith(
        expect.objectContaining({
          public_ratio: 0.0125,
          is_online: false,
          is_degraded: false,
          ttft_ms: 0,
          last_checked_at: new Date('2026-09-24T12:34:56').getTime() / 1000,
        }),
        expect.anything()
      )
    )
    await user.clear(ttft)
    fireEvent.change(checked, { target: { value: '' } })
    await user.click(online)
    await user.click(screen.getByRole('option', { name: 'Unknown' }))
    await user.click(screen.getByRole('button', { name: 'Save channel' }))
    await waitFor(() =>
      expect(save).toHaveBeenLastCalledWith(
        expect.objectContaining({
          is_online: null,
          is_degraded: false,
          ttft_ms: null,
          last_checked_at: null,
        }),
        expect.anything()
      )
    )
  })

  it.each([
    { public_ratio: Number.POSITIVE_INFINITY },
    { public_ratio: -1 },
    { ttft_ms: -1 },
    { ttft_ms: 0.5 },
    { last_checked_at: -1 },
    { last_checked_at: 253402300800 },
  ])(
    'rejects invalid observation values %j before building a payload',
    (invalid) => {
      expect(
        channelFormSchema.safeParse({
          ...CHANNEL_FORM_DEFAULT_VALUES,
          name: 'channel',
          models: 'model-a',
          key: 'key',
          ...invalid,
        }).success
      ).toBe(false)
    }
  )

  it.each([false, true])(
    'displays observation fields and retains provider filtering in mobile=%s',
    async (mobile) => {
      const matchMedia = window.matchMedia.bind(window)
      vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
        ...matchMedia(query),
        matches: query.includes('max-width')
          ? mobile
          : matchMedia(query).matches,
      }))
      const channel = channelSchema.parse({
        id: 1,
        name: 'Observed channel',
        key: '',
        type: 1,
        status: 2,
        created_time: 0,
        test_time: 0,
        response_time: 0,
        balance_updated_time: 0,
        models: 'model-a',
        provider_id: 3,
        public_ratio: 0.0125,
        is_online: false,
        is_degraded: null,
        ttft_ms: 0,
        last_checked_at: 1700000123,
      })
      useAuthStore
        .getState()
        .auth.setUser({ id: 1, username: 'root', role: 100 })
      localStorage.setItem('channel-status-filter', 'disabled')
      const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
        if (url === '/api/providers/') {
          return {
            data: { success: true, data: { items: [first, second], total: 2 } },
          }
        }
        if (url === '/api/group/') {
          return { data: { success: true, data: ['default'] } }
        }
        return {
          data: {
            success: true,
            data: { items: [channel], total: 1, type_counts: { '1': 1 } },
          },
        }
      })
      const client = new QueryClient({
        defaultOptions: { queries: { retry: false } },
      })
      const root = createRootRoute({ component: Outlet })
      const layout = createRoute({
        getParentRoute: () => root,
        id: '_authenticated',
        component: Outlet,
      })
      const page = createRoute({
        getParentRoute: () => layout,
        path: 'channels/',
        validateSearch: ChannelsRoute.options.validateSearch,
        component: () => (
          <ChannelsProvider>
            <ChannelsTable />
          </ChannelsProvider>
        ),
      })
      const router = createRouter({
        routeTree: root.addChildren([layout.addChildren([page])]),
        history: createMemoryHistory({
          initialEntries: ['/channels?provider_id=3&status=%5B%22all%22%5D'],
        }),
      })
      await router.load()
      render(
        <QueryClientProvider client={client}>
          <RouterProvider router={router} />
        </QueryClientProvider>
      )
      await waitFor(() =>
        expect(get).toHaveBeenCalledWith(
          '/api/channel',
          expect.objectContaining({
            params: expect.objectContaining({
              provider_id: 3,
              status: undefined,
            }),
          })
        )
      )
      const user = userEvent.setup()
      expect(await screen.findByText('Offline')).toBeVisible()
      expect(screen.getByText('0.0125')).toBeVisible()
      expect(screen.getAllByText('Unknown').length).toBeGreaterThan(0)
      expect(screen.getByText('Time to first token (ms)')).toBeVisible()
      if (mobile) {
        expect(
          screen.getByText('Time to first token (ms)').nextElementSibling
        ).toHaveTextContent('0')
      }
      await user.type(
        screen.getByPlaceholderText('Filter by name, ID, or key...'),
        'test'
      )
      await waitFor(() =>
        expect(get).toHaveBeenCalledWith(
          '/api/channel/search',
          expect.objectContaining({
            params: expect.objectContaining({
              provider_id: 3,
              keyword: 'test',
            }),
          })
        )
      )
      await user.click(screen.getByRole('button', { name: 'Reset' }))
      await waitFor(() =>
        expect(router.state.location.search).not.toHaveProperty('provider_id')
      )
      await waitFor(() =>
        expect(get).toHaveBeenCalledWith(
          '/api/channel',
          expect.objectContaining({
            params: expect.objectContaining({ provider_id: undefined }),
          })
        )
      )
    }
  )

  it('loads every provider page, supports keyboard selection and preserves multi-key data', async () => {
    const get = vi
      .spyOn(api, 'get')
      .mockResolvedValueOnce({
        data: { success: true, data: { items: [first], total: 2 } },
      })
      .mockResolvedValueOnce({
        data: { success: true, data: { items: [second], total: 2 } },
      })
    const save = await renderFields()
    const input = await screen.findByRole('combobox', { name: 'Provider' })
    await waitFor(() => expect(input).toBeEnabled())
    expect(input).toHaveValue('Upstream A (#2)')
    expect(get).toHaveBeenLastCalledWith(
      '/api/providers/',
      expect.objectContaining({ params: { p: 2, page_size: 100 } })
    )
    const user = userEvent.setup()
    await user.click(input)
    await user.type(input, 'Upstream B')
    await user.keyboard('{ArrowDown}{Enter}')
    await waitFor(() => expect(input).toHaveValue('Upstream B (#3)'))
    const ratio = screen.getByRole('spinbutton', { name: 'Upstream ratio' })
    await user.clear(ratio)
    await user.type(ratio, '0')
    await user.click(screen.getByRole('button', { name: 'Save channel' }))
    expect(
      await screen.findByText('Upstream ratio must be greater than zero')
    ).toBeVisible()
    expect(ratio).toHaveAttribute('aria-invalid', 'true')
    expect(save).not.toHaveBeenCalled()
    await user.clear(ratio)
    await user.type(ratio, '0.0125')
    await user.click(screen.getByRole('button', { name: 'Save channel' }))
    await waitFor(() =>
      expect(save).toHaveBeenCalledWith(
        expect.objectContaining({
          provider_id: 3,
          upstream_ratio: 0.0125,
          key: 'key-one\nkey-two',
        }),
        expect.anything()
      )
    )
  })

  it('disables selection on load failure without silently resetting the saved provider and offers retry', async () => {
    vi.spyOn(api, 'get')
      .mockResolvedValueOnce({
        data: { success: false, message: 'Provider options unavailable' },
      })
      .mockResolvedValue({
        data: { success: true, data: { items: [first], total: 1 } },
      })
    await renderFields()
    expect(
      await screen.findByText('Provider options unavailable')
    ).toBeVisible()
    const input = screen.getByRole('combobox', { name: 'Provider' })
    expect(input).toBeDisabled()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(input).toHaveValue('Upstream A (#2)'))
    expect(input).toBeEnabled()
  })

  it('retains provider and precise upstream ratio through channel edit and create payloads', () => {
    const channel = channelSchema.parse({
      id: 5,
      name: 'multi-key channel',
      type: 1,
      key: '',
      status: 1,
      created_time: 1,
      test_time: 0,
      response_time: 0,
      balance_updated_time: 0,
      models: 'model-a',
      provider_id: 7,
      upstream_ratio: 0.0125,
      public_ratio: 0.125,
      is_degraded: false,
      is_online: null,
      ttft_ms: 0,
      last_checked_at: 1700000123,
      channel_info: {
        is_multi_key: true,
        multi_key_size: 2,
        multi_key_mode: 'polling',
      },
    })
    const defaults = transformChannelToFormDefaults(channel)
    const observations = {
      public_ratio: 0.125,
      is_degraded: false,
      is_online: null,
      ttft_ms: 0,
      last_checked_at: 1700000123,
    }
    expect(defaults).toMatchObject(observations)
    expect(transformFormDataToUpdatePayload(defaults, 5)).toMatchObject(
      observations
    )
    expect(
      transformFormDataToCreatePayload({ ...defaults, key: 'key-a' }).channel
    ).toMatchObject(observations)
    expect(defaults).toMatchObject({ provider_id: 7, upstream_ratio: 0.0125 })
    expect(transformFormDataToUpdatePayload(defaults, 5)).toMatchObject({
      id: 5,
      provider_id: 7,
      upstream_ratio: 0.0125,
    })
    expect(transformFormDataToUpdatePayload(defaults, 5)).not.toHaveProperty(
      'key'
    )
    const create = transformFormDataToCreatePayload({
      ...defaults,
      key: 'key-one\nkey-two',
      multi_key_mode: 'multi_to_single',
      multi_key_type: 'polling',
    })
    expect(create).toMatchObject({
      mode: 'multi_to_single',
      multi_key_mode: 'polling',
      channel: {
        provider_id: 7,
        upstream_ratio: 0.0125,
        key: 'key-one\nkey-two',
      },
    })
  })
})
