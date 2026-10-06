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
import { useId, useState } from 'react'
import { useWatch, type Control } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Combobox } from '@/components/ui/combobox'
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
  FieldError,
  FieldSet,
  FieldLegend,
} from '@/components/ui/field'
import { FormField, FormItem, FormMessage } from '@/components/ui/form'
import { Switch } from '@/components/ui/switch'
import { PricingAmountInput } from '@/features/model-pricing/pricing-amount-input'
import { BILLING_PRICING_VARS } from '@/features/pricing/lib/billing-expr'

import type { ChannelFormValues } from '../lib/channel-form'
import {
  channelTokenPricesSchema,
  type ChannelTokenPrices,
} from '../lib/channel-token-pricing'

// Channel scope is not supported by the system model-pricing panel. Compose its
// shared currency input and variable registry, keeping edits in the channel form.
export function ChannelTokenPricingFields(props: {
  control: Control<ChannelFormValues>
  disabled?: boolean
}) {
  const { t } = useTranslation()
  const id = useId()
  const models = useWatch({ control: props.control, name: 'models' })
  const [selected, setSelected] = useState<string | null>(null)

  return (
    <FormField
      control={props.control}
      name='model_token_prices'
      render={({ field }) => {
        const prices = field.value ?? {}
        const validation = channelTokenPricesSchema.safeParse(prices)
        const invalidModels = validation.success
          ? []
          : [
              ...new Set(
                validation.error.issues.map((issue) =>
                  String(issue.path[0] ?? '')
                )
              ),
            ]
        const names = [
          ...new Set([
            ...(models ?? '')
              .split(',')
              .map((name) => name.trim())
              .filter(Boolean),
            ...Object.keys(prices),
          ]),
        ]
        const model = selected && names.includes(selected) ? selected : names[0]
        const record =
          model && Object.hasOwn(prices, model) ? prices[model] : undefined
        return (
          <FormItem>
            <FieldSet>
              <FieldLegend>{t('Channel model token prices')}</FieldLegend>
              <FieldGroup>
                <Field>
                  <FieldLabel htmlFor={`${id}-model`}>{t('Model')}</FieldLabel>
                  <Combobox
                    id={`${id}-model`}
                    value={model ?? null}
                    options={names.map((name) => ({
                      value: name,
                      label: name,
                    }))}
                    onValueChange={setSelected}
                    placeholder={t('Select model')}
                    disabled={props.disabled || names.length === 0}
                  />
                  <FieldDescription>
                    {t(
                      'Customer billing base prices in USD per million tokens, before group ratio, public ratio and service fee. Exact public model names are used, not upstream mappings.'
                    )}
                  </FieldDescription>
                </Field>
                <Field orientation='horizontal' data-disabled={props.disabled}>
                  <FieldLabel htmlFor={`${id}-enabled`}>
                    {t('Override system model pricing')}
                  </FieldLabel>
                  <Switch
                    id={`${id}-enabled`}
                    checked={Boolean(record)}
                    disabled={props.disabled || !model}
                    onCheckedChange={(checked) => {
                      if (!model) return
                      if (checked) {
                        field.onChange({
                          ...prices,
                          [model]: { p: Number.NaN, c: Number.NaN },
                        })
                      } else {
                        const next = { ...prices }
                        delete next[model]
                        field.onChange(next)
                      }
                      field.onBlur()
                    }}
                  />
                </Field>
                {record && model && (
                  <FieldGroup className='grid sm:grid-cols-2'>
                    {BILLING_PRICING_VARS.map((variable) => {
                      const key =
                        variable.key as keyof ChannelTokenPrices[string]
                      const value = record[key]
                      const invalid =
                        (variable.isBase && value === undefined) ||
                        (value !== undefined &&
                          (!Number.isFinite(value) ||
                            value < 0 ||
                            value > 1_000_000_000))
                      return (
                        <Field
                          key={`${model}:${key}`}
                          data-invalid={invalid}
                          data-disabled={props.disabled}
                        >
                          <FieldLabel htmlFor={`${id}-${key}`}>
                            {t(variable.label)}
                          </FieldLabel>
                          <PricingAmountInput
                            id={`${id}-${key}`}
                            value={
                              value !== undefined && Number.isFinite(value)
                                ? String(value)
                                : ''
                            }
                            aria-invalid={invalid || undefined}
                            aria-describedby={
                              invalid ? `${id}-${key}-error` : undefined
                            }
                            disabled={props.disabled}
                            required={variable.isBase}
                            placeholder={
                              variable.isBase
                                ? '0'
                                : t('Use input/output price')
                            }
                            onBlur={field.onBlur}
                            onChange={(usd) => {
                              const next = { ...record }
                              if (usd === '' && !variable.isBase) {
                                delete next[key]
                              } else {
                                next[key] =
                                  usd === '' ? Number.NaN : Number(usd)
                              }
                              field.onChange({ ...prices, [model]: next })
                            }}
                          />
                          {invalid && (
                            <FieldError id={`${id}-${key}-error`}>
                              {t(
                                'Token prices must be between 0 and 1000000000 USD per million tokens.'
                              )}
                            </FieldError>
                          )}
                        </Field>
                      )
                    })}
                    <FieldDescription className='sm:col-span-2'>
                      {t(
                        'Input and output prices are required. Blank optional categories use the input/output price, not system prices. Explicit zero is free. Cache writes (5m/default) and 1h can be priced independently. Disable the override to restore system pricing.'
                      )}
                    </FieldDescription>
                  </FieldGroup>
                )}
              </FieldGroup>
            </FieldSet>
            {invalidModels.length > 0 && (
              <FieldError>
                {t(
                  'Token prices must be between 0 and 1000000000 USD per million tokens.'
                )}{' '}
                {invalidModels.join(', ')}
              </FieldError>
            )}
            <FormMessage />
          </FormItem>
        )
      }}
    />
  )
}
