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
import { useMutation } from '@tanstack/react-query'
import { useEffect, useId, useRef, useState } from 'react'
import { useFormContext, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { FormField, FormItem } from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import { handleServerError } from '@/lib/handle-server-error'

import { previewChannelPricingImport } from '../api'
import { CHANNEL_TYPE_NEW_API, CHANNEL_TYPE_SUB2API } from '../constants'
import type { ChannelFormValues } from '../lib/channel-form'
import {
  ChannelModelNameButton,
  ChannelPricingComparisonDialog,
} from './channel-pricing-comparison-dialog'

// This fills the existing channel draft; the channel Save owns persistence.
export function ChannelPricingImportFields(props: {
  sourceLocked?: boolean
  disabled?: boolean
}) {
  const { t } = useTranslation()
  const disabledReasonId = useId()
  const form = useFormContext<ChannelFormValues>()
  const [comparisonModel, setComparisonModel] = useState<string | null>(null)
  const type = useWatch({ control: form.control, name: 'type' })
  const group = useWatch({
    control: form.control,
    name: 'upstream_pricing_group',
  })
  const baseURL = useWatch({ control: form.control, name: 'base_url' })
  const abort = useRef<AbortController | null>(null)
  useEffect(() => () => abort.current?.abort(), [])
  const snapshot = () => ({
    type: form.getValues('type'),
    base_url: form.getValues('base_url') ?? '',
    group: form.getValues('upstream_pricing_group')?.trim() ?? '',
    models: form.getValues('models') ?? '',
    model_configs: form.getValues('model_configs') ?? {},
  })
  const mutation = useMutation({
    mutationFn: async (request: ReturnType<typeof snapshot>) => {
      abort.current?.abort()
      const controller = new AbortController()
      abort.current = controller
      const result = await previewChannelPricingImport(
        request,
        controller.signal
      )
      if (controller.signal.aborted) {
        throw new DOMException('Aborted', 'AbortError')
      }
      if (JSON.stringify(snapshot()) !== JSON.stringify(request)) {
        throw new Error(
          t('The channel draft changed during import. Fetch prices again.')
        )
      }
      form.setValue('model_configs', result.model_configs, {
        shouldDirty: true,
        shouldValidate: true,
      })
      form.setValue('models', result.models.join(','), {
        shouldDirty: true,
        shouldValidate: true,
      })
      return result
    },
    onError: (error) => handleServerError(error),
  })
  if (type !== CHANNEL_TYPE_NEW_API && type !== CHANNEL_TYPE_SUB2API) {
    return null
  }
  const isSub2API = type === CHANNEL_TYPE_SUB2API
  let disabledReason = ''
  if (props.disabled) {
    disabledReason = t('No permission to perform this action')
  } else if (mutation.isPending) {
    disabledReason = t('Loading...')
  } else if (!group?.trim()) {
    disabledReason = t('Group is required')
  } else if (!baseURL?.trim()) {
    disabledReason = t('Base URL is required for this channel type')
  }
  const result = mutation.data
  const comparisonConfig = comparisonModel
    ? form.getValues('model_configs')?.[comparisonModel]
    : undefined
  const actionLabels = {
    added: t('Added'),
    updated: t('Updated'),
    unchanged: t('Unchanged'),
    conflict: t('Conflict'),
    unsupported: t('Unsupported'),
  }
  return (
    <div className='flex flex-col gap-3'>
      <FormField
        control={form.control}
        name='upstream_pricing_group'
        render={({ field }) => (
          <FormItem>
            <Field>
              <FieldLabel htmlFor='upstream-pricing-group'>
                {t('Upstream pricing group')}
              </FieldLabel>
              <Input
                {...field}
                value={field.value ?? ''}
                id='upstream-pricing-group'
                disabled={
                  props.sourceLocked || props.disabled || mutation.isPending
                }
                placeholder={isSub2API ? '12' : 'default'}
              />
              <FieldDescription>
                {isSub2API
                  ? t(
                      'Use a fixed Sub2API group ID (recommended); group names are also accepted. Prices come from the public model plaza; account-specific discounts are not included.'
                    )
                  : t(
                      'Use a fixed upstream token group matching this name. Prices come from the public New API catalog; account-specific discounts are not included.'
                    )}
              </FieldDescription>
            </Field>
          </FormItem>
        )}
      />
      <Button
        type='button'
        variant='outline'
        aria-label={t('Import group models and prices')}
        aria-busy={mutation.isPending}
        aria-describedby={disabledReason ? disabledReasonId : undefined}
        disabled={Boolean(disabledReason)}
        onClick={() => mutation.mutate(snapshot())}
      >
        {mutation.isPending && <Spinner data-icon='inline-start' />}
        {t('Import group models and prices')}
      </Button>
      {disabledReason && (
        <FieldDescription id={disabledReasonId} aria-live='polite'>
          {disabledReason}
        </FieldDescription>
      )}
      {group?.trim() && (
        <FieldDescription>
          {t(
            'Price imports preserve local edits and observations. Automatic model-only imports are disabled for this source.'
          )}
        </FieldDescription>
      )}
      {result && (
        <>
          <Alert>
            <AlertDescription>
              {t(
                'Import results are in the channel draft. Save the channel to apply them.'
              )}
            </AlertDescription>
          </Alert>
          <StaticDataTable
            tableProps={{ 'aria-label': t('Price import results') }}
            data={result.items}
            getRowKey={(item) => item.model}
            emptyContent={t('No models')}
            columns={[
              {
                id: 'model',
                header: t('Model'),
                cell: (item) => (
                  <ChannelModelNameButton
                    model={item.model}
                    onClick={() => setComparisonModel(item.model)}
                  />
                ),
              },
              {
                id: 'action',
                header: t('Status'),
                cell: (item) => actionLabels[item.action],
              },
              {
                id: 'reason',
                header: t('Details'),
                cell: (item) => (item.reason ? t(item.reason) : '—'),
              },
            ]}
          />
          {result.removed.length > 0 && (
            <Alert>
              <AlertDescription>
                {t(
                  'Models missing from the upstream catalog were preserved: {{models}}',
                  { models: result.removed.join(', ') }
                )}
              </AlertDescription>
            </Alert>
          )}
          {comparisonConfig ? (
            <ChannelPricingComparisonDialog
              model={comparisonModel ?? ''}
              config={comparisonConfig}
              open
              onOpenChange={(open) => {
                if (!open) setComparisonModel(null)
              }}
            />
          ) : null}
        </>
      )}
    </div>
  )
}
