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
import { lazy, Suspense, useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Alert, AlertDescription } from '@/components/ui/alert'
import {
  Field,
  FieldGroup,
  FieldLabel,
  FieldDescription,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import {
  combineBillingExpr,
  splitBillingExprAndRequestRules,
} from '@/features/pricing/lib/billing-expr'
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

import {
  modelRatioLabel,
  type ChannelModelConfig,
  type ModelRatioRange,
} from '../lib/channel-token-pricing'
import { ChannelObservationFields } from './channel-observation-fields'

const ExpressionEditor = lazy(() =>
  import('@/features/system-settings/models/tiered-pricing-editor').then(
    (module) => ({ default: module.TieredPricingEditor })
  )
)

export function ChannelModelEditor(props: {
  model: string
  value: ChannelModelConfig
  onChange: (value: ChannelModelConfig) => void
  disabled?: boolean
}) {
  const { t } = useTranslation()
  const id = useId()
  const expression = props.value.billing_expression ?? ''
  const [expressionMode, setExpressionMode] = useState(Boolean(expression))
  const [source, setSource] = useState(expression)
  useEffect(() => {
    const timer = setTimeout(() => setSource(expression), 300)
    return () => clearTimeout(timer)
  }, [expression])
  const preview = useQuery({
    queryKey: ['channel-model-preview', props.model, source],
    enabled: Boolean(source),
    retry: false,
    meta: { errorToast: false },
    queryFn: async ({ signal }) => {
      const response = await api.post(
        '/api/channel/model_config/preview',
        { model: props.model, config: { billing_expression: source } },
        { signal }
      )
      requireServerSuccess(response.data)
      return response.data.data as {
        public_ratio: ModelRatioRange | null
        schedulable: boolean
      }
    },
  })
  const parts = splitBillingExprAndRequestRules(expression)
  return (
    <section
      aria-label={props.model}
      className='flex flex-col gap-4 rounded-lg border p-4'
    >
      <h3 className='font-medium break-all'>{props.model}</h3>
      <FieldGroup className='grid sm:grid-cols-2'>
        <Field>
          <FieldLabel htmlFor={`${id}-discount`}>
            {t('Upstream discount')}
          </FieldLabel>
          <Input
            id={`${id}-discount`}
            type='number'
            min={0}
            max={1}
            step='any'
            value={props.value.upstream_discount ?? 1}
            disabled={props.disabled}
            onChange={(event) =>
              props.onChange({
                ...props.value,
                upstream_discount:
                  event.target.value === ''
                    ? Number.NaN
                    : Number(event.target.value),
              })
            }
          />
          <FieldDescription>
            {t('Cost = base charge × upstream discount. 0.8 means paying 80%.')}
          </FieldDescription>
        </Field>
        <Field>
          <FieldLabel htmlFor={`${id}-ratio`}>{t('Public ratio')}</FieldLabel>
          {expressionMode ? (
            <output id={`${id}-ratio`} aria-live='polite'>
              {source === expression && preview.data
                ? modelRatioLabel(preview.data.public_ratio)
                : t('Loading...')}
            </output>
          ) : (
            <Input
              id={`${id}-ratio`}
              type='number'
              min={0}
              step='any'
              value={props.value.public_ratio ?? 1}
              disabled={props.disabled}
              onChange={(event) =>
                props.onChange({
                  ...props.value,
                  public_ratio:
                    event.target.value === ''
                      ? Number.NaN
                      : Number(event.target.value),
                })
              }
            />
          )}
        </Field>
        <Field orientation='horizontal'>
          <FieldLabel htmlFor={`${id}-expression`}>
            {t('Use model billing expression')}
          </FieldLabel>
          <Switch
            id={`${id}-expression`}
            checked={expressionMode}
            disabled={props.disabled}
            onCheckedChange={(checked) => {
              setExpressionMode(checked)
              if (checked) {
                props.onChange({
                  ...props.value,
                  public_ratio: undefined,
                  billing_expression: 'tier("base", p * 0 + c * 0)',
                })
              } else {
                props.onChange({
                  ...props.value,
                  public_ratio: 1,
                  billing_expression: '',
                })
              }
            }}
          />
        </Field>
      </FieldGroup>
      {expressionMode && (
        <>
          <FieldDescription>
            {t(
              'Public ratio is calculated from base-tier token prices. It is not applied again to expression charges.'
            )}
          </FieldDescription>
          {preview.isError && source === expression && (
            <ErrorState
              description={preview.error.message}
              onRetry={() => void preview.refetch()}
            />
          )}
          {preview.data &&
            !preview.data.schedulable &&
            source === expression && (
              <Alert>
                <AlertDescription>
                  {t(
                    'No comparable system token prices. This model is excluded from dispatch.'
                  )}
                </AlertDescription>
              </Alert>
            )}
          <fieldset disabled={props.disabled} className='min-w-0'>
            <Suspense fallback={<LoadingState />}>
              <ExpressionEditor
                modelName={props.model}
                billingExpr={parts.billingExpr}
                requestRuleExpr={parts.requestRuleExpr}
                onBillingExprChange={(value) =>
                  props.onChange({
                    ...props.value,
                    public_ratio: undefined,
                    billing_expression: combineBillingExpr(
                      value,
                      parts.requestRuleExpr
                    ),
                  })
                }
                onRequestRuleExprChange={(value) =>
                  props.onChange({
                    ...props.value,
                    public_ratio: undefined,
                    billing_expression: combineBillingExpr(
                      parts.billingExpr,
                      value
                    ),
                  })
                }
              />
            </Suspense>
          </fieldset>
        </>
      )}
      <ChannelObservationFields
        value={props.value}
        onChange={props.onChange}
        disabled={props.disabled}
      />
    </section>
  )
}
