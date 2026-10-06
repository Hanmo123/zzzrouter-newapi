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
import { useWatch, type Control } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { Button } from '@/components/ui/button'
import {
  Field,
  FieldDescription,
  FieldError,
  FieldLabel,
} from '@/components/ui/field'
import { FormField, FormItem } from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import dayjs from '@/lib/dayjs'

import type { ChannelFormValues } from '../lib/channel-form'
import {
  channelModelConfigsSchema,
  modelRatioLabel,
} from '../lib/channel-token-pricing'
import type { Channel } from '../types'
import { ChannelModelEditor } from './channel-model-editor'

// Channel models share a compact table; the selected row opens the existing
// expression editor. Changes stay in the channel draft until its Save action.
export function ChannelTokenPricingFields(props: {
  control: Control<ChannelFormValues>
  disabled?: boolean
  saved?: Pick<Channel, 'model_configs' | 'model_ratios'>
}) {
  const { t } = useTranslation()
  const models = useWatch({ control: props.control, name: 'models' })
  const [selected, setSelected] = useState<string | null>(null)
  const [search, setSearch] = useState('')
  return (
    <FormField
      control={props.control}
      name='model_configs'
      render={({ field }) => {
        const configs = field.value ?? {}
        const names = [
          ...new Set([
            ...(models ?? '')
              .split(',')
              .map((name) => name.trim())
              .filter(Boolean),
            ...Object.keys(configs),
          ]),
        ]
        const validation = channelModelConfigsSchema.safeParse(configs)
        const selectedConfig =
          selected && Object.hasOwn(configs, selected) ? configs[selected] : {}
        const status = (
          value: boolean | null | undefined,
          yes: string,
          no: string
        ) => (value == null ? t('Unknown') : t(value ? yes : no))
        return (
          <FormItem>
            <Field>
              <FieldLabel>{t('Channel models and billing')}</FieldLabel>
              <Input
                aria-label={t('Search models')}
                placeholder={t('Search models')}
                value={search}
                onChange={(event) => setSearch(event.target.value)}
              />
              <FieldDescription>
                {t(
                  'Configure prices and observations independently for each public model. Group ratio applies last to customer charges; cost excludes service fees and group ratios.'
                )}
              </FieldDescription>
            </Field>
            <StaticDataTable
              tableProps={{ 'aria-label': t('Channel models and billing') }}
              data={names.filter((name) =>
                name.toLowerCase().includes(search.toLowerCase())
              )}
              getRowKey={(name) => name}
              emptyContent={t('No models')}
              columns={[
                {
                  id: 'model',
                  header: t('Model'),
                  cell: (name) => (
                    <Button
                      type='button'
                      variant='link'
                      aria-label={t('Configure model: {{model}}', {
                        model: name,
                      })}
                      aria-expanded={selected === name}
                      onClick={() =>
                        setSelected(selected === name ? null : name)
                      }
                    >
                      {name}
                    </Button>
                  ),
                },
                {
                  id: 'pricing',
                  header: t('Pricing'),
                  cell: (name) => {
                    const config = configs[name]
                    if (config?.billing_expression) {
                      if (
                        props.saved?.model_configs?.[name]
                          ?.billing_expression === config.billing_expression
                      ) {
                        return modelRatioLabel(props.saved.model_ratios?.[name])
                      }
                      return t('Expression')
                    }
                    return modelRatioLabel({
                      min: config?.public_ratio ?? 1,
                      max: config?.public_ratio ?? 1,
                    })
                  },
                },
                {
                  id: 'discount',
                  header: t('Upstream discount'),
                  cell: (name) => String(configs[name]?.upstream_discount ?? 1),
                },
                {
                  id: 'online',
                  header: t('Callable (online)'),
                  cell: (name) =>
                    status(configs[name]?.is_online, 'Online', 'Offline'),
                },
                {
                  id: 'degraded',
                  header: t('Is degraded'),
                  cell: (name) =>
                    status(
                      configs[name]?.is_degraded,
                      'Degraded',
                      'Not degraded'
                    ),
                },
                {
                  id: 'ttft',
                  header: t('Time to first token (ms)'),
                  cell: (name) =>
                    configs[name]?.ttft_ms == null
                      ? t('Unknown')
                      : String(configs[name].ttft_ms),
                },
                {
                  id: 'checked',
                  header: t('Last checked'),
                  cell: (name) =>
                    configs[name]?.last_checked_at == null
                      ? t('Unknown')
                      : dayjs
                          .unix(configs[name].last_checked_at)
                          .format('YYYY-MM-DD HH:mm:ss'),
                },
              ]}
            />
            {selected && names.includes(selected) && (
              <ChannelModelEditor
                key={`${selected}:${selectedConfig.pricing_source?.source_hash ?? ''}:${selectedConfig.pricing_source?.applied_expression ?? ''}:${selectedConfig.pricing_source?.applied_ratio ?? ''}`}
                model={selected}
                value={selectedConfig}
                disabled={props.disabled}
                onChange={(value) =>
                  field.onChange({ ...configs, [selected]: value })
                }
              />
            )}
            {!validation.success && (
              <FieldError>
                {validation.error.issues
                  .map(
                    (issue) => `${String(issue.path[0])}: ${t(issue.message)}`
                  )
                  .join('; ')}
              </FieldError>
            )}
          </FormItem>
        )
      }}
    />
  )
}
