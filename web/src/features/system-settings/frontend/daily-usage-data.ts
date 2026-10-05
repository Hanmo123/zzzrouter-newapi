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
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

export const MAX_DAILY_USAGE_COUNT = 1_000_000_000_000
export const dailyUsageQueryKey = ['managed-daily-usage'] as const

export type DailyUsageInput = { date: string; request_count: number }
export type DailyUsagePoint = { date: string; request_count: number | null }
export type DailyUsageSeries = {
  timezone: 'UTC'
  source: 'manual'
  from: string
  to: string
  days: number
  total: number
  recorded_days: number
  items: DailyUsagePoint[]
}

export function validUsageDate(date: string, maxDate: string): boolean {
  const time = Date.parse(`${date}T00:00:00Z`)
  return (
    /^\d{4}-\d{2}-\d{2}$/.test(date) &&
    Number.isFinite(time) &&
    new Date(time).toISOString().slice(0, 10) === date &&
    date >= '1970-01-01' &&
    date <= maxDate
  )
}

export function validUsageCount(value: string): boolean {
  return (
    /^\d+$/.test(value) &&
    Number.isSafeInteger(Number(value)) &&
    Number(value) <= MAX_DAILY_USAGE_COUNT
  )
}

export function parseUsageCsv(raw: string, maxDate: string): DailyUsageInput[] {
  const lines = raw
    .trim()
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean)
  if (/^date\s*,\s*request_count$/i.test(lines[0] ?? '')) lines.shift()
  if (!lines.length || lines.length > 366) throw new Error('invalid usage CSV')
  const seen = new Set<string>()
  return lines.map((line) => {
    const parts = line.split(',').map((part) => part.trim())
    const date = parts[0] ?? ''
    const count = parts[1] ?? ''
    if (
      parts.length !== 2 ||
      !validUsageDate(date, maxDate) ||
      !validUsageCount(count) ||
      seen.has(date)
    ) {
      throw new Error('invalid usage CSV')
    }
    seen.add(date)
    return { date, request_count: Number(count) }
  })
}

export async function getManagedDailyUsage(
  days: number
): Promise<DailyUsageSeries> {
  const response = await api.get<{ success: boolean; data: DailyUsageSeries }>(
    '/api/option/daily_usage',
    { params: { days } }
  )
  return requireServerSuccess(response.data).data
}

export async function saveDailyUsage(items: DailyUsageInput[]): Promise<void> {
  const response = await api.put<{ success: boolean }>(
    '/api/option/daily_usage',
    { items }
  )
  requireServerSuccess(response.data)
}

export async function deleteDailyUsage(date: string): Promise<void> {
  const response = await api.delete<{ success: boolean }>(
    `/api/option/daily_usage/${date}`
  )
  requireServerSuccess(response.data)
}
