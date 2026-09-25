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
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import type { Control } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
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
import { providerOptionsQuery } from '@/features/providers/api'
import { getServerErrorMessage } from '@/lib/server-error-message'

import type { ChannelFormValues } from '../lib/channel-form'

export function ChannelProviderFields(props: {
  control: Control<ChannelFormValues>
}) {
  const { t } = useTranslation()
  const query = useQuery({
    ...providerOptionsQuery,
    meta: { errorToast: false },
  })
  return (
    <div className='grid gap-4 sm:grid-cols-2'>
      <FormField
        control={props.control}
        name='provider_id'
        render={({ field }) => (
          <FormItem>
            <FormLabel>{t('Provider')}</FormLabel>
            <FormControl>
              <Combobox
                value={field.value ? String(field.value) : null}
                onValueChange={(value) => {
                  if (value) field.onChange(Number(value))
                }}
                onBlur={field.onBlur}
                name={field.name}
                ref={field.ref}
                options={(query.data ?? []).map((provider) => ({
                  value: String(provider.id),
                  label: `${provider.name} (#${provider.id})`,
                }))}
                placeholder={
                  query.isPending
                    ? t('Loading...')
                    : t('Default provider (automatic)')
                }
                emptyText={t('No providers found')}
                disabled={query.isPending || query.isError}
              />
            </FormControl>
            <FormDescription>
              <Link
                to='/providers'
                target='_blank'
                rel='noopener noreferrer'
                className='text-primary underline'
              >
                {t('Manage providers')}
              </Link>
            </FormDescription>
            <FormMessage />
            {query.isError && (
              <div role='alert' className='text-destructive text-sm'>
                {getServerErrorMessage(query.error)}
                <Button
                  type='button'
                  variant='link'
                  size='sm'
                  onClick={() => void query.refetch()}
                >
                  {t('Retry')}
                </Button>
              </div>
            )}
          </FormItem>
        )}
      />
      <FormField
        control={props.control}
        name='upstream_ratio'
        render={({ field }) => (
          <FormItem>
            <FormLabel>{t('Upstream ratio')}</FormLabel>
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
                'Channel-level upstream multiplier. Does not change customer billing or routing.'
              )}
            </FormDescription>
            <FormMessage />
          </FormItem>
        )}
      />
    </div>
  )
}
