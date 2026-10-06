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
import {
  keepPreviousData,
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'

import { DailyUsageBarEditor } from './daily-usage-bar-editor'
import {
  dailyUsageQueryKey,
  deleteDailyUsage,
  getManagedDailyUsage,
  saveDailyUsage,
  type DailyUsageInput,
} from './daily-usage-data'
import { DailyUsageCsvForm, DailyUsageForm } from './daily-usage-form'

export function DailyUsageSettings() {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const queryClient = useQueryClient()
  const [days, setDays] = useState(30)
  const [chartDirty, setChartDirty] = useState(false)
  const [mode, setMode] = useState('chart')
  const [editing, setEditing] = useState<DailyUsageInput | null>(null)
  const [editRevision, setEditRevision] = useState(0)
  const [deleting, setDeleting] = useState<string | null>(null)
  const data = useQuery({
    queryKey: [...dailyUsageQueryKey, days],
    queryFn: () => getManagedDailyUsage(days),
    placeholderData: keepPreviousData,
    meta: { errorToast: false },
  })
  const save = useMutation({
    mutationFn: saveDailyUsage,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: dailyUsageQueryKey })
      toast.success(t('Token usage saved and published'))
    },
    onError: (error) =>
      handleServerError(error, t('Failed to save daily usage')),
  })
  const remove = useMutation({
    mutationFn: deleteDailyUsage,
    onSuccess: () => {
      setDeleting(null)
      void queryClient.invalidateQueries({ queryKey: dailyUsageQueryKey })
      toast.success(t('Daily usage deleted'))
    },
    onError: (error) =>
      handleServerError(error, t('Failed to delete daily usage')),
  })
  const pending = save.isPending || remove.isPending
  if (data.isPending) return <LoadingState />
  if (data.isError && !data.data) {
    return (
      <ErrorState
        title={t('Failed to load daily usage')}
        onRetry={() => void data.refetch()}
      />
    )
  }
  if (!data.data) return <LoadingState />
  const records = [...data.data.items]
    .reverse()
    .filter((item) => item.token_count !== null)

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('Daily usage display')}</CardTitle>
        <CardDescription>
          {t(
            'Maintain daily platform token totals. Changes to ended UTC dates are published to the public usage page.'
          )}
        </CardDescription>
        <CardDescription>
          {t(
            'The editor and public chart share the same dates, ending yesterday (UTC). The current day remains excluded.'
          )}
        </CardDescription>
      </CardHeader>
      <CardContent className='flex flex-col gap-6'>
        {data.isError && (
          <ErrorState
            title={t('Failed to load daily usage')}
            onRetry={() => void data.refetch()}
          />
        )}
        <Tabs value={mode} onValueChange={(value) => setMode(String(value))}>
          <TabsList>
            <TabsTrigger value='chart'>{t('Drag token bars')}</TabsTrigger>
            <TabsTrigger value='single'>{t('Single date')}</TabsTrigger>
            <TabsTrigger value='csv'>{t('Batch CSV')}</TabsTrigger>
          </TabsList>
          <TabsContent value='chart' keepMounted className='pt-4'>
            <DailyUsageBarEditor
              series={data.data}
              pending={pending || data.isFetching}
              onDirtyChange={setChartDirty}
              onSave={(items) => save.mutateAsync(items)}
            />
          </TabsContent>
          <TabsContent value='single' keepMounted className='pt-4'>
            <DailyUsageForm
              key={editRevision}
              maxDate={data.data.to}
              pending={pending}
              selected={editing}
              onSave={(items) => save.mutateAsync(items)}
            />
          </TabsContent>
          <TabsContent value='csv' keepMounted className='pt-4'>
            <DailyUsageCsvForm
              maxDate={data.data.to}
              pending={pending}
              onSave={(items) => save.mutateAsync(items)}
            />
          </TabsContent>
        </Tabs>
        <div className='flex flex-wrap items-center justify-between gap-3'>
          <h3 className='font-medium'>{t('Daily token data')}</h3>
          <div className='flex items-center gap-2'>
            <Select
              value={String(days)}
              onValueChange={(value) => {
                if (value) setDays(Number(value))
              }}
              disabled={pending || chartDirty}
            >
              <SelectTrigger aria-label={t('Daily history range')}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  {[7, 30, 90, 365].map((value) => (
                    <SelectItem key={value} value={String(value)}>
                      {t('Last {{days}} days', { days: value })}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            <Button
              variant='outline'
              disabled={data.isFetching || pending || chartDirty}
              onClick={() => void data.refetch()}
            >
              {t('Refresh')}
            </Button>
          </div>
        </div>
        {records.length === 0 ? (
          <EmptyState title={t('No token data yet')} />
        ) : (
          <ScrollArea className='h-80'>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('Usage date (UTC)')}</TableHead>
                  <TableHead>{t('Daily token count')}</TableHead>
                  <TableHead>{t('Actions')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {records.map((record) => (
                  <TableRow key={record.date}>
                    <TableCell>{record.date}</TableCell>
                    <TableCell>
                      {formatNumber(record.token_count, locale)}
                    </TableCell>
                    <TableCell>
                      <div className='flex gap-2'>
                        <Button
                          variant='outline'
                          size='sm'
                          disabled={pending}
                          aria-label={t('Edit usage for {{date}}', {
                            date: record.date,
                          })}
                          onClick={() => {
                            setEditing({
                              date: record.date,
                              token_count: record.token_count ?? 0,
                            })
                            setEditRevision((value) => value + 1)
                            setMode('single')
                          }}
                        >
                          {t('Edit')}
                        </Button>
                        <Button
                          variant='outline'
                          size='sm'
                          disabled={pending}
                          aria-label={t('Delete usage for {{date}}', {
                            date: record.date,
                          })}
                          onClick={() => setDeleting(record.date)}
                        >
                          {t('Delete')}
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </ScrollArea>
        )}
        <p className='text-muted-foreground text-sm'>
          {t(
            'Dates with data are listed. Use the chart, date form or CSV to update values; deleting a record restores no data, not zero.'
          )}
        </p>
      </CardContent>
      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => {
          if (!open && !remove.isPending) setDeleting(null)
        }}
        title={t('Delete daily usage?')}
        desc={t(
          'Delete the token data for {{date}}? This restores no data instead of setting the count to zero.',
          { date: deleting }
        )}
        confirmText={t('Delete daily record')}
        destructive
        isLoading={remove.isPending}
        handleConfirm={() => {
          if (deleting) remove.mutate(deleting)
        }}
      />
    </Card>
  )
}
