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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import {
  Field,
  FieldContent,
  FieldDescription,
  FieldLabel,
} from '@/components/ui/field'
import { Switch } from '@/components/ui/switch'

import {
  getChannelDegradationExclusion,
  setChannelDegradationExclusion,
} from '../api'

// The exclusion is saved independently from the unsaved channel configuration
// draft. A failed write leaves the last persisted setting visible.
export function ChannelDegradationExclusion(props: {
  channelId: number
  open: boolean
  canWrite: boolean
}) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const queryKey = ['channels', props.channelId, 'degradation-exclusion']
  const setting = useQuery({
    queryKey,
    queryFn: () => getChannelDegradationExclusion(props.channelId),
    enabled: props.open,
    refetchOnWindowFocus: 'always',
    meta: { errorToast: false },
  })
  const update = useMutation({
    mutationFn: (skip: boolean) =>
      setChannelDegradationExclusion(props.channelId, skip),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey })
    },
  })
  if (setting.isError) {
    return (
      <ErrorState
        className='min-h-32'
        title={t('Cannot load channel degradation settings')}
        onRetry={() => void setting.refetch()}
      />
    )
  }
  return (
    <Field
      orientation='horizontal'
      data-disabled={!props.canWrite || setting.isPending || update.isPending}
    >
      <FieldContent>
        <FieldLabel htmlFor={`degradation-exclusion-${props.channelId}`}>
          {t('Do not check degradation for this channel')}
        </FieldLabel>
        <FieldDescription>
          {t(
            'Off by default. When enabled, manual and scheduled checks skip this channel even if its models are enabled.'
          )}
        </FieldDescription>
      </FieldContent>
      <Switch
        id={`degradation-exclusion-${props.channelId}`}
        checked={setting.data ?? false}
        disabled={!props.canWrite || setting.isPending || update.isPending}
        onCheckedChange={(value) => update.mutate(value)}
      />
    </Field>
  )
}
