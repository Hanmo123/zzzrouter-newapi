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
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import { afterEach, describe, expect, it, vi } from 'vitest'

import zhTW from '@/i18n/locales/zh-TW.json'
import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { Route } from '@/routes/_authenticated/providers'
import { useAuthStore } from '@/stores/auth-store'

import { Providers } from '..'
import type { Provider } from '../types'

const reseller: Provider = {
  id: 2,
  name: 'Example upstream',
  description: 'Standard channels',
  channel_count: 0,
  created_time: 1700000000,
  updated_time: 1700000000,
}

async function renderProviders(
  options: {
    write?: boolean
    read?: boolean
    role?: number
    path?: string
  } = {}
) {
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  useAuthStore.getState().auth.setUser({
    id: 8,
    username: 'operator',
    role: options.role ?? ROLE.ADMIN,
    permissions: {
      admin_permissions: {
        channel: { read: options.read ?? true, write: options.write ?? true },
      },
    },
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const root = createRootRoute({ component: Outlet })
  const layout = createRoute({
    getParentRoute: () => root,
    id: '_authenticated',
    component: Outlet,
  })
  const page = createRoute({
    getParentRoute: () => layout,
    path: 'providers/',
    component: Providers,
    validateSearch: Route.options.validateSearch,
    beforeLoad: (context) => Route.options.beforeLoad?.(context as never),
  })
  const forbidden = createRoute({
    getParentRoute: () => root,
    path: '/403',
    component: () => <p>Permission denied</p>,
  })
  const channels = createRoute({
    getParentRoute: () => root,
    path: '/channels',
    component: () => <p>Channel destination</p>,
  })
  const router = createRouter({
    routeTree: root.addChildren([
      layout.addChildren([page]),
      forbidden,
      channels,
    ]),
    history: createMemoryHistory({
      initialEntries: [options.path ?? '/providers'],
    }),
  })
  await router.load()
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  return { router, client }
}

afterEach(async () => {
  useAuthStore.getState().auth.reset()
  localStorage.clear()
  await i18next.changeLanguage('en')
  for (const language of ['zhCN', 'zhTW', 'fr']) {
    i18next.removeResourceBundle(language, 'translation')
  }
})

describe('provider console', () => {
  it('uses consistent provider terminology when switching between Chinese and English', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: { items: [], total: 0 } },
    })
    i18next.addResourceBundle('zhCN', 'translation', zh.translation)
    i18next.addResourceBundle('zhTW', 'translation', zhTW.translation)
    await renderProviders()
    const user = userEvent.setup()
    await user.click(
      await screen.findByRole('button', { name: 'Add Provider' })
    )
    for (const [language, title, label] of [
      ['zhCN', '添加服务商', '服务商名称 *'],
      ['zhTW', '新增服務商', '服務商名稱 *'],
      ['en', 'Add Provider', 'Provider Name *'],
    ]) {
      await act(async () => {
        await i18next.changeLanguage(language)
      })
      expect(screen.getByRole('dialog', { name: title })).toBeVisible()
      expect(screen.getByLabelText(label)).toBeVisible()
    }
  })

  it('creates and edits metadata, trims names, clears descriptions and refreshes the list', async () => {
    let items: Provider[] = []
    vi.spyOn(api, 'get').mockImplementation(async () => ({
      data: { success: true, data: { items, total: items.length } },
    }))
    const post = vi.spyOn(api, 'post').mockImplementation(async () => {
      items = [reseller]
      return { data: { success: true, data: reseller } }
    })
    const put = vi.spyOn(api, 'put').mockImplementation(async () => {
      items = [{ ...reseller, name: 'Updated upstream', description: '' }]
      return { data: { success: true, data: items[0] } }
    })
    await renderProviders()
    expect(await screen.findByText('No providers found')).toBeVisible()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Add Provider' }))
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText('Provider name is required')).toBeVisible()
    expect(post).not.toHaveBeenCalled()
    await user.type(
      screen.getByLabelText('Provider Name *'),
      '  Example upstream  '
    )
    await user.type(screen.getByLabelText('Description'), 'Standard channels')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/api/providers/', {
        name: 'Example upstream',
        description: 'Standard channels',
        skip_degradation_check: false,
      })
    )
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
    await user.click(
      await screen.findByRole('button', { name: 'Edit Example upstream' })
    )
    await user.clear(screen.getByLabelText('Provider Name *'))
    await user.type(
      screen.getByLabelText('Provider Name *'),
      'Updated upstream'
    )
    await user.clear(screen.getByLabelText('Description'))
    await user.click(
      screen.getByRole('switch', {
        name: 'Do not check degradation for this provider',
      })
    )
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(put).toHaveBeenCalledWith('/api/providers/2', {
        name: 'Updated upstream',
        description: '',
        skip_degradation_check: true,
      })
    )
    expect(await screen.findByText('Updated upstream')).toBeVisible()
  })

  it('disables duplicate submissions and closing while a save is in progress', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: { items: [], total: 0 } },
    })
    let finish!: (response: {
      data: { success: boolean; data: Provider }
    }) => void
    const post = vi.spyOn(api, 'post').mockImplementation(
      () =>
        new Promise((resolve) => {
          finish = resolve
        })
    )
    await renderProviders()
    const user = userEvent.setup()
    await user.click(
      await screen.findByRole('button', { name: 'Add Provider' })
    )
    await user.type(screen.getByLabelText('Provider Name *'), reseller.name)
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(
      await screen.findByRole('button', { name: 'Saving...' })
    ).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeDisabled()
    await user.keyboard('{Escape}')
    expect(screen.getByRole('dialog')).toBeVisible()
    await act(async () => finish({ data: { success: true, data: reseller } }))
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
    expect(post).toHaveBeenCalledTimes(1)
  })

  it.each([
    ['zhCN', '12,345'],
    ['zhTW', '12,345'],
    ['fr', '12\u202f345'],
  ])(
    'updates channel counts when switching to %s without invalid Intl locales',
    async (language, expected) => {
      vi.spyOn(api, 'get').mockResolvedValue({
        data: {
          success: true,
          data: { items: [{ ...reseller, channel_count: 12345 }], total: 1 },
        },
      })
      await renderProviders()
      const count = await screen.findByRole('link', {
        name: 'View channels for Example upstream',
      })
      expect(count).toHaveTextContent('12,345')
      i18next.addResourceBundle(language, 'translation', {
        Providers: 'Providers',
      })
      await act(async () => {
        await i18next.changeLanguage(language)
      })
      expect(
        screen
          .getByRole('link', { name: 'View channels for Example upstream' })
          .textContent?.trim()
      ).toBe(expected)
    }
  )

  it('retains entered values after save failure and requires confirmation before discarding changes', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: { items: [reseller], total: 1 } },
    })
    vi.spyOn(api, 'put').mockResolvedValue({
      data: { success: false, message: 'Provider update rejected' },
    })
    await renderProviders()
    const user = userEvent.setup()
    await user.click(
      await screen.findByRole('button', { name: 'Edit Example upstream' })
    )
    await user.type(screen.getByLabelText('Provider Name *'), ' changed')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText('Provider update rejected')).toBeVisible()
    expect(screen.getByLabelText('Provider Name *')).toHaveValue(
      'Example upstream changed'
    )
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(await screen.findByText('Discard unsaved changes?')).toBeVisible()
    await user.click(
      within(screen.getByRole('alertdialog')).getByRole('button', {
        name: 'Cancel',
      })
    )
    expect(screen.getByLabelText('Provider Name *')).toHaveValue(
      'Example upstream changed'
    )
  })

  it('blocks referenced deletions and preserves a failed deletion dialog for retry', async () => {
    let items = [
      { ...reseller, channel_count: 2 },
      { ...reseller, id: 3, name: 'Unused provider' },
    ]
    vi.spyOn(api, 'get').mockImplementation(async () => ({
      data: { success: true, data: { items, total: items.length } },
    }))
    const remove = vi
      .spyOn(api, 'delete')
      .mockResolvedValueOnce({
        data: {
          success: false,
          message: 'the default provider cannot be deleted',
        },
      })
      .mockImplementationOnce(async () => {
        items = items.slice(0, 1)
        return { data: { success: true } }
      })
    await renderProviders()
    const user = userEvent.setup()
    await user.click(
      await screen.findByRole('button', { name: 'Delete Example upstream' })
    )
    expect(
      within(screen.getByRole('alertdialog')).getByRole('button', {
        name: 'Delete',
      })
    ).toBeDisabled()
    expect(remove).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    await user.click(
      screen.getByRole('button', { name: 'Delete Unused provider' })
    )
    await user.click(
      within(screen.getByRole('alertdialog')).getByRole('button', {
        name: 'Delete',
      })
    )
    expect(
      await screen.findByText('the default provider cannot be deleted')
    ).toBeVisible()
    await user.click(
      within(screen.getByRole('alertdialog')).getByRole('button', {
        name: 'Delete',
      })
    )
    await waitFor(() =>
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    )
    expect(screen.queryByText('Unused provider')).not.toBeInTheDocument()
    expect(remove).toHaveBeenLastCalledWith('/api/providers/3')
  })

  it('keeps bookmarked pagination, searches on the server and links to all channels of a provider', async () => {
    const get = vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: { items: [{ ...reseller, channel_count: 12 }], total: 30 },
      },
    })
    const { router } = await renderProviders({
      path: '/providers?page=2&pageSize=20',
    })
    expect(await screen.findByText(reseller.name)).toBeVisible()
    expect(get).toHaveBeenCalledWith(
      '/api/providers/',
      expect.objectContaining({ params: { p: 2, page_size: 20, keyword: '' } })
    )
    const user = userEvent.setup()
    await user.type(
      screen.getByPlaceholderText('Search provider names'),
      'Example'
    )
    await waitFor(() =>
      expect(get).toHaveBeenLastCalledWith(
        '/api/providers/',
        expect.objectContaining({
          params: { p: 1, page_size: 20, keyword: 'Example' },
        })
      )
    )
    await user.click(
      screen.getByRole('link', { name: 'View channels for Example upstream' })
    )
    await waitFor(() =>
      expect(router.state.location.pathname).toBe('/channels')
    )
    expect(router.state.location.search).toMatchObject({
      provider_id: 2,
      status: ['all'],
      page: 1,
    })
  })

  it('shows errors rather than an empty list and reloads on retry', async () => {
    const get = vi
      .spyOn(api, 'get')
      .mockResolvedValueOnce({
        data: { success: false, message: 'Provider list unavailable' },
      })
      .mockResolvedValue({
        data: { success: true, data: { items: [], total: 0 } },
      })
    await renderProviders()
    expect(await screen.findByText('Provider list unavailable')).toBeVisible()
    expect(screen.queryByText('No providers found')).not.toBeInTheDocument()
    await userEvent.setup().click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('No providers found')).toBeVisible()
    expect(get).toHaveBeenCalledTimes(2)
  })

  it('keeps long provider names and management actions accessible on mobile', async () => {
    const matchMedia = window.matchMedia.bind(window)
    vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
      ...matchMedia(query),
      matches:
        query.includes('max-width: 640px') ||
        query.includes('prefers-reduced-motion'),
    }))
    const name =
      'Upstream with a deliberately long descriptive business name and multiple regional channels'
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: { items: [{ ...reseller, name }], total: 1 },
      },
    })
    await renderProviders()
    expect(await screen.findByText(name)).toBeVisible()
    expect(screen.getByText(name)).toHaveAttribute('title', name)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: `Edit ${name}` }))
    expect(await screen.findByLabelText('Provider Name *')).toHaveValue(name)
  })

  it('allows read-only admins to view providers but hides every write action', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: { items: [reseller], total: 1 } },
    })
    await renderProviders({ write: false })
    expect(await screen.findByText(reseller.name)).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'Add Provider' })
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Edit Example upstream' })
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Delete Example upstream' })
    ).not.toBeInTheDocument()
  })

  it.each([{ read: false }, { role: ROLE.USER }])(
    'rejects direct access without admin and read permissions (%j)',
    async (options) => {
      const get = vi.spyOn(api, 'get')
      await renderProviders(options)
      expect(await screen.findByText('Permission denied')).toBeVisible()
      expect(get).not.toHaveBeenCalled()
    }
  )
})
