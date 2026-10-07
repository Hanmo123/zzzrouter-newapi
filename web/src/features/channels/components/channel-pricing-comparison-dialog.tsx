/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'

import { previewChannelModelConfig } from '../api'
import {
  modelRatioLabel,
  type ChannelModelConfig,
} from '../lib/channel-token-pricing'

const priceCategories = [
  'p',
  'c',
  'cr',
  'cc',
  'cc1h',
  'img',
  'img_cr',
  'img_o',
  'ai',
  'ao',
] as const

function priceLabel(value: number | undefined) {
  if (value === undefined) return '—'
  return Number(value.toPrecision(8)).toString()
}

export function ChannelPricingComparisonDialog(props: {
  model: string
  config: ChannelModelConfig
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const preview = useQuery({
    queryKey: ['channel-model-price-comparison', props.model, props.config],
    enabled: props.open,
    retry: false,
    meta: { errorToast: false },
    queryFn: ({ signal }) =>
      previewChannelModelConfig(
        { model: props.model, config: props.config },
        signal
      ),
  })
  const data = preview.data
  const categories = priceCategories.filter((key) =>
    Boolean(
      data?.system_prices?.[key] !== undefined ||
      data?.upstream_prices?.[key] !== undefined ||
      data?.channel_prices?.[key] !== undefined
    )
  )
  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('Price comparison: {{model}}', { model: props.model })}
      description={t(
        'System prices are the current local reference. Upstream prices include the imported group multiplier.'
      )}
      contentClassName='sm:max-w-3xl'
    >
      {preview.isPending ? <LoadingState /> : null}
      {preview.isError ? (
        <ErrorState
          description={preview.error.message}
          onRetry={() => void preview.refetch()}
        />
      ) : null}
      {data ? (
        <div className='flex flex-col gap-4'>
          <div className='flex flex-wrap items-center gap-2 text-sm'>
            <span className='font-medium'>{t('Public ratio')}</span>
            <span>{modelRatioLabel(data.public_ratio)}</span>
            {data.upstream_group ? (
              <span className='text-muted-foreground'>
                {t('Upstream group')}: {data.upstream_group}
              </span>
            ) : null}
            {!data.schedulable ? (
              <span className='text-destructive'>{t('Not schedulable')}</span>
            ) : null}
          </div>
          {categories.length > 0 ? (
            <div className='overflow-x-auto rounded-lg border'>
              <table className='w-full text-sm'>
                <thead>
                  <tr className='bg-muted/50 border-b text-left'>
                    <th className='px-3 py-2 font-medium'>{t('Category')}</th>
                    <th className='px-3 py-2 font-medium'>
                      {t('System price')}
                    </th>
                    <th className='px-3 py-2 font-medium'>
                      {t('Upstream price')}
                    </th>
                    <th className='px-3 py-2 font-medium'>
                      {t('Channel price')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {categories.map((key) => (
                    <tr key={key} className='border-b last:border-0'>
                      <td className='px-3 py-2'>
                        <span className='font-mono'>{key}</span>
                      </td>
                      <td className='px-3 py-2 font-mono'>
                        {priceLabel(data.system_prices?.[key])}
                      </td>
                      <td className='px-3 py-2 font-mono'>
                        {priceLabel(data.upstream_prices?.[key])}
                      </td>
                      <td className='px-3 py-2 font-mono'>
                        {priceLabel(data.channel_prices?.[key])}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <p className='text-muted-foreground text-sm'>
              {t('No comparable token prices')}
            </p>
          )}
          <div className='grid gap-3'>
            {[
              ['System expression', data.system_expression],
              ['Upstream expression', data.upstream_expression],
              ['Channel expression', data.channel_expression],
            ].map(([label, expression]) =>
              expression ? (
                <div key={label} className='grid gap-1'>
                  <span className='text-sm font-medium'>{t(label)}</span>
                  <pre className='bg-muted max-h-40 overflow-auto rounded-lg p-3 text-xs break-all whitespace-pre-wrap'>
                    {expression}
                  </pre>
                </div>
              ) : null
            )}
          </div>
        </div>
      ) : null}
    </Dialog>
  )
}

export function ChannelModelNameButton(props: {
  model: string
  onClick: () => void
}) {
  return (
    <Button
      type='button'
      variant='link'
      className='h-auto justify-start p-0 text-start font-medium break-all'
      onClick={props.onClick}
      aria-label={props.model}
    >
      {props.model}
    </Button>
  )
}
