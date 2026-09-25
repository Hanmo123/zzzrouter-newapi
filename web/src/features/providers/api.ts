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
import { queryOptions } from '@tanstack/react-query'

import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

import type { Provider, ProviderFormValues, ProviderListParams } from './types'

interface ProviderPage {
  items: Provider[]
  total: number
}

export const providersQueryKey = ['providers'] as const

export async function listProviders(
  params: ProviderListParams,
  signal?: AbortSignal
): Promise<ProviderPage> {
  const response = await api.get<{ success: boolean; data: ProviderPage }>(
    '/api/providers/',
    { params, signal }
  )
  return requireServerSuccess(response.data).data
}

export async function saveProvider(
  values: ProviderFormValues,
  id?: number
): Promise<Provider> {
  const response = id
    ? await api.put<{ success: boolean; data: Provider }>(
        `/api/providers/${id}`,
        values
      )
    : await api.post<{ success: boolean; data: Provider }>(
        '/api/providers/',
        values
      )
  return requireServerSuccess(response.data).data
}

export async function deleteProvider(id: number): Promise<void> {
  const response = await api.delete<{ success: boolean }>(
    `/api/providers/${id}`
  )
  requireServerSuccess(response.data)
}

// Options must not silently truncate providers at the server's page-size limit.
export const providerOptionsQuery = queryOptions({
  queryKey: [...providersQueryKey, 'options'],
  queryFn: async ({ signal }): Promise<Provider[]> => {
    const providers: Provider[] = []
    for (let p = 1; ; p++) {
      const page = await listProviders({ p, page_size: 100 }, signal)
      providers.push(...page.items)
      if (page.items.length === 0 || providers.length >= page.total) {
        return providers
      }
    }
  },
  staleTime: 30_000,
  // Channel forms open management in a new tab to preserve the unsaved draft.
  refetchOnWindowFocus: 'always',
})
