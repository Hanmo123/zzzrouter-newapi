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
import type { Control } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Combobox } from '@/components/ui/combobox'
import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import dayjs from '@/lib/dayjs'

import type { ChannelFormValues } from '../lib/channel-form'

export function ChannelObservationFields(props: {
  control: Control<ChannelFormValues>
}) {
  const { t } = useTranslation()
  return (
    <div className='grid gap-4 sm:grid-cols-2'>
      <FormField
        control={props.control}
        name='public_ratio'
        render={({ field }) => (
          <FormItem>
            <FormLabel>{t('Public ratio')}</FormLabel>
            <FormControl>
              <Input
                {...field}
                type='number'
                min='0'
                step='any'
                value={Number.isFinite(field.value) ? field.value : ''}
                onChange={(event) =>
                  field.onChange(
                    event.target.value === ''
                      ? Number.NaN
                      : Number(event.target.value)
                  )
                }
              />
            </FormControl>
            <FormDescription>
              {t(
                'Public multiplier applied to customer billing after the group ratio and before the service fee.'
              )}
            </FormDescription>
            <FormMessage />
          </FormItem>
        )}
      />
      {(
        [
          {
            name: 'is_degraded',
            label: t('Is degraded'),
            yes: t('Degraded'),
            no: t('Not degraded'),
          },
          {
            name: 'is_online',
            label: t('Callable (online)'),
            yes: t('Online'),
            no: t('Offline'),
          },
        ] as const
      ).map((item) => (
        <FormField
          key={item.name}
          control={props.control}
          name={item.name}
          render={({ field }) => (
            <FormItem>
              <FormLabel>{item.label}</FormLabel>
              <FormControl>
                <Combobox
                  ref={field.ref}
                  name={field.name}
                  onBlur={field.onBlur}
                  value={field.value == null ? 'unknown' : String(field.value)}
                  onValueChange={(value) =>
                    field.onChange(
                      value === 'unknown' || value == null
                        ? null
                        : value === 'true'
                    )
                  }
                  options={[
                    { value: 'unknown', label: t('Unknown') },
                    { value: 'true', label: item.yes },
                    { value: 'false', label: item.no },
                  ]}
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
      ))}
      <FormField
        control={props.control}
        name='ttft_ms'
        render={({ field }) => (
          <FormItem>
            <FormLabel>{t('Time to first token (ms)')}</FormLabel>
            <FormControl>
              <Input
                {...field}
                type='number'
                min='0'
                step='1'
                value={field.value ?? ''}
                onChange={(event) =>
                  field.onChange(
                    event.target.value === ''
                      ? null
                      : Number(event.target.value)
                  )
                }
              />
            </FormControl>
            <FormDescription>
              {t('Blank means unknown; zero is a measured value.')}
            </FormDescription>
            <FormMessage />
          </FormItem>
        )}
      />
      <FormField
        control={props.control}
        name='last_checked_at'
        render={({ field }) => (
          <FormItem className='sm:col-span-2'>
            <FormLabel>{t('Last checked')}</FormLabel>
            <FormControl>
              {/* DateTimePicker supports minutes only; this input preserves observation seconds. */}
              <Input
                {...field}
                type='datetime-local'
                step='1'
                value={
                  field.value == null || !Number.isFinite(field.value)
                    ? ''
                    : dayjs.unix(field.value).format('YYYY-MM-DDTHH:mm:ss')
                }
                onChange={(event) =>
                  field.onChange(
                    event.target.value === ''
                      ? null
                      : Math.floor(
                          new Date(event.target.value).getTime() / 1000
                        )
                  )
                }
              />
            </FormControl>
            <FormDescription>
              {t(
                'Latest observation in local time. These fields do not enable or disable the channel and are not evaluation history.'
              )}
            </FormDescription>
            <FormMessage />
          </FormItem>
        )}
      />
    </div>
  )
}
