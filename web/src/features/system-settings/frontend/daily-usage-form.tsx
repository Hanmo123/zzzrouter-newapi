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
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import * as z from 'zod'

import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'

import {
  MAX_DAILY_USAGE_COUNT,
  parseUsageCsv,
  validUsageCount,
  validUsageDate,
  type DailyUsageInput,
} from './daily-usage-data'

type DayForm = { date: string; count: string }

export function DailyUsageForm(props: {
  maxDate: string
  pending: boolean
  selected: DailyUsageInput | null
  onSave: (items: DailyUsageInput[]) => Promise<void>
}) {
  const { t } = useTranslation()
  const form = useForm<DayForm>({
    resolver: zodResolver(
      z.object({
        date: z
          .string()
          .refine(
            (value) => validUsageDate(value, props.maxDate),
            t('Choose today or an earlier valid UTC date')
          ),
        count: z
          .string()
          .refine(
            validUsageCount,
            t('Enter an integer from 0 to 1000000000000')
          ),
      })
    ),
    defaultValues: {
      date: props.selected?.date ?? props.maxDate,
      count: props.selected ? String(props.selected.request_count) : '',
    },
  })
  return (
    <Form {...form}>
      <form
        className='flex flex-col gap-4'
        onSubmit={form.handleSubmit(async (values) => {
          try {
            await props.onSave([
              { date: values.date, request_count: Number(values.count) },
            ])
            form.reset({ date: values.date, count: '' })
          } catch {
            // The mutation owns error reporting; keep the unsaved draft for retry.
          }
        })}
      >
        <FormField
          control={form.control}
          name='date'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Usage date (UTC)')}</FormLabel>
              <FormControl>
                <Input
                  type='date'
                  min='1970-01-01'
                  max={props.maxDate}
                  disabled={props.pending}
                  {...field}
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={form.control}
          name='count'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Request count')}</FormLabel>
              <FormControl>
                <Input
                  type='number'
                  min={0}
                  max={MAX_DAILY_USAGE_COUNT}
                  step={1}
                  disabled={props.pending}
                  {...field}
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <p className='text-muted-foreground text-sm'>
          {t(
            'Saving replaces that date only. Zero is a recorded count; missing dates remain unknown.'
          )}
        </p>
        <Button type='submit' disabled={props.pending} className='self-end'>
          {t('Save daily usage')}
        </Button>
      </form>
    </Form>
  )
}

export function DailyUsageCsvForm(props: {
  maxDate: string
  pending: boolean
  onSave: (items: DailyUsageInput[]) => Promise<void>
}) {
  const { t } = useTranslation()
  const form = useForm<{ csv: string }>({
    resolver: zodResolver(
      z.object({
        csv: z.string().refine((raw) => {
          try {
            parseUsageCsv(raw, props.maxDate)
            return true
          } catch {
            return false
          }
        }, t('Enter 1 to 366 unique dates and integer counts as date,request_count')),
      })
    ),
    defaultValues: { csv: '' },
  })
  return (
    <Form {...form}>
      <form
        className='flex flex-col gap-4'
        onSubmit={form.handleSubmit(async (values) => {
          try {
            await props.onSave(parseUsageCsv(values.csv, props.maxDate))
            form.reset({ csv: '' })
          } catch {
            // Preserve the full batch if validation or the server rejects it.
          }
        })}
      >
        <FormField
          control={form.control}
          name='csv'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Daily usage CSV')}</FormLabel>
              <FormControl>
                <Textarea
                  rows={8}
                  placeholder='date,request_count'
                  disabled={props.pending}
                  {...field}
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <p className='text-muted-foreground text-sm'>
          {t(
            'One date,count pair per line. The optional header is date,request_count. The entire batch saves atomically and replaces only its listed dates.'
          )}
        </p>
        <Button type='submit' disabled={props.pending} className='self-end'>
          {t('Save usage batch')}
        </Button>
      </form>
    </Form>
  )
}
