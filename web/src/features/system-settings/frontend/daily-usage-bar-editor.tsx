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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { ScrollArea, ScrollBar } from '@/components/ui/scroll-area'
import { Slider } from '@/components/ui/slider'
import { formatCompactNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

import {
  MAX_DAILY_USAGE_COUNT,
  validUsageCount,
  type DailyUsageInput,
  type DailyUsageSeries,
} from './daily-usage-data'

// Compose vertical sliders so drag, touch capture and keyboard interactions
// remain owned by the shared primitive. Missing days stay null until edited.
export function DailyUsageBarEditor(props: {
  series: DailyUsageSeries
  pending: boolean
  onDirtyChange: (dirty: boolean) => void
  onSave: (items: DailyUsageInput[]) => Promise<void>
}) {
  const { t } = useTranslation()
  const [drafts, setDrafts] = useState<Record<string, number>>({})
  const [selected, setSelected] = useState(props.series.to)
  const [ceiling, setCeiling] = useState(() =>
    String(
      Math.ceil(
        Math.min(
          MAX_DAILY_USAGE_COUNT,
          Math.max(
            1000,
            ...props.series.items.map(
              (point) => (point.token_count ?? 0) * 1.25
            )
          )
        )
      )
    )
  )
  const [exact, setExact] = useState<string | null>(null)
  const dirty = Object.keys(drafts)
  const greatest = Math.max(
    0,
    ...props.series.items.map(
      (point) => drafts[point.date] ?? point.token_count ?? 0
    )
  )
  const plotMax = Math.min(
    MAX_DAILY_USAGE_COUNT,
    Math.max(greatest, validUsageCount(ceiling) ? Number(ceiling) : 1000, 1)
  )
  const selectedPoint =
    props.series.items.find((point) => point.date === selected) ??
    props.series.items.find((point) => point.date === props.series.to)
  const selectedDate = selectedPoint?.date ?? props.series.to
  const selectedCount =
    drafts[selectedDate] ?? selectedPoint?.token_count ?? null
  const selectDate = (date: string) => {
    setSelected(date)
    setExact(null)
  }
  const update = (date: string, value: number) => {
    props.onDirtyChange(true)
    setDrafts((previous) => ({
      ...previous,
      [date]: Math.round(Math.min(MAX_DAILY_USAGE_COUNT, Math.max(0, value))),
    }))
  }

  return (
    <section
      aria-label={t('Daily token bar editor')}
      className='flex flex-col gap-4'
    >
      <p className='text-muted-foreground text-sm'>
        {t(
          'Drag a bar to set its daily tokens. Select a day to enter an exact value; save when finished.'
        )}
      </p>
      <FieldGroup>
        <Field>
          <FieldLabel htmlFor='daily-token-chart-maximum'>
            {t('Chart maximum (tokens)')}
          </FieldLabel>
          <Input
            id='daily-token-chart-maximum'
            type='number'
            min={1}
            max={MAX_DAILY_USAGE_COUNT}
            step={1}
            value={ceiling}
            disabled={props.pending}
            onChange={(event) => setCeiling(event.target.value)}
          />
        </Field>
      </FieldGroup>
      <div className='flex gap-3'>
        <div
          aria-hidden='true'
          className='text-muted-foreground flex h-64 w-16 shrink-0 flex-col justify-between text-right text-xs'
        >
          {[1, 0.75, 0.5, 0.25, 0].map((fraction) => (
            <span key={fraction}>
              {formatCompactNumber(plotMax * fraction, 'en-US')}
            </span>
          ))}
        </div>
        <ScrollArea className='min-w-0 flex-1'>
          <div
            className='flex gap-2 pb-4'
            style={{ minWidth: props.series.items.length * 28 }}
          >
            {props.series.items.map((point) => {
              const value = drafts[point.date] ?? point.token_count
              return (
                <div
                  key={point.date}
                  className='flex min-w-5 flex-1 flex-col gap-2'
                >
                  <div className='h-64 px-1'>
                    <Slider
                      variant='bar'
                      orientation='vertical'
                      min={0}
                      max={plotMax}
                      step={1}
                      locale='en-US'
                      value={[value ?? 0]}
                      disabled={props.pending}
                      className={cn(value === null && 'opacity-40')}
                      thumbLabel={t('Tokens for {{date}}', {
                        date: point.date,
                      })}
                      onFocus={() => selectDate(point.date)}
                      onValueChange={(values) => {
                        selectDate(point.date)
                        update(
                          point.date,
                          Array.isArray(values) ? (values[0] ?? 0) : values
                        )
                      }}
                      onValueCommitted={(values) => {
                        selectDate(point.date)
                        update(
                          point.date,
                          Array.isArray(values) ? (values[0] ?? 0) : values
                        )
                      }}
                      format={{ notation: 'compact', maximumFractionDigits: 1 }}
                    />
                  </div>
                  <Button
                    variant={
                      selectedDate === point.date ? 'secondary' : 'ghost'
                    }
                    size='sm'
                    disabled={props.pending}
                    className='px-1'
                    aria-label={t('Select token date {{date}}', {
                      date: point.date,
                    })}
                    onClick={() => selectDate(point.date)}
                  >
                    {point.date.slice(8)}
                  </Button>
                  <span className='text-muted-foreground text-center text-[10px] tabular-nums'>
                    {value === null ? '—' : formatCompactNumber(value, 'en-US')}
                  </span>
                </div>
              )
            })}
          </div>
          <ScrollBar orientation='horizontal' />
        </ScrollArea>
      </div>
      <FieldGroup>
        <Field>
          <FieldLabel htmlFor='daily-token-exact'>
            {t('Tokens for {{date}}', { date: selectedDate })}
          </FieldLabel>
          <Input
            id='daily-token-exact'
            type='number'
            min={0}
            max={MAX_DAILY_USAGE_COUNT}
            step={1}
            disabled={props.pending}
            value={
              exact ?? (selectedCount === null ? '' : String(selectedCount))
            }
            aria-invalid={exact !== null && !validUsageCount(exact)}
            onChange={(event) => {
              const value = event.target.value
              setExact(value)
              if (validUsageCount(value)) update(selectedDate, Number(value))
            }}
          />
        </Field>
      </FieldGroup>
      <p role='status' className='text-muted-foreground text-sm'>
        {t('{{count}} dates changed', { count: dirty.length })} ·{' '}
        {selectedCount === null
          ? t('No Data')
          : `${formatCompactNumber(selectedCount, 'en-US')} Tokens`}
      </p>
      <div className='flex flex-wrap justify-end gap-2'>
        <Button
          variant='outline'
          disabled={props.pending || !dirty.length}
          onClick={() => {
            setDrafts({})
            setExact(null)
            props.onDirtyChange(false)
          }}
        >
          {t('Reset changes')}
        </Button>
        <Button
          disabled={
            props.pending ||
            !dirty.length ||
            (exact !== null && !validUsageCount(exact))
          }
          onClick={async () => {
            try {
              await props.onSave(
                Object.entries(drafts).map(([date, tokenCount]) => ({
                  date,
                  token_count: tokenCount,
                }))
              )
              setDrafts({})
              setExact(null)
              props.onDirtyChange(false)
            } catch {
              // Keep all unsaved bars when the server rejects the update.
            }
          }}
        >
          {t('Save token chart')}
        </Button>
      </div>
    </section>
  )
}
