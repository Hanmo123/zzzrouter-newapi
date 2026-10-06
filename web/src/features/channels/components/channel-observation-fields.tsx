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
import { useId } from 'react'
import { useTranslation } from 'react-i18next'

import { Combobox } from '@/components/ui/combobox'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import dayjs from '@/lib/dayjs'

import type { ChannelModelConfig } from '../lib/channel-token-pricing'

export function ChannelObservationFields(props: {
  value: ChannelModelConfig
  onChange: (value: ChannelModelConfig) => void
  disabled?: boolean
}) {
  const { t } = useTranslation()
  const id = useId()
  return (
    <FieldGroup className='grid sm:grid-cols-2'>
      {(
        [
          {
            name: 'is_degraded',
            label: 'Is degraded',
            yes: 'Degraded',
            no: 'Not degraded',
          },
          {
            name: 'is_online',
            label: 'Callable (online)',
            yes: 'Online',
            no: 'Offline',
          },
        ] as const
      ).map((item) => (
        <Field key={item.name}>
          <FieldLabel htmlFor={`${id}-${item.name}`}>
            {t(item.label)}
          </FieldLabel>
          <Combobox
            id={`${id}-${item.name}`}
            disabled={props.disabled}
            value={
              props.value[item.name] == null
                ? 'unknown'
                : String(props.value[item.name])
            }
            onValueChange={(value) =>
              props.onChange({
                ...props.value,
                [item.name]:
                  value === 'unknown' || value == null
                    ? null
                    : value === 'true',
              })
            }
            options={[
              { value: 'unknown', label: t('Unknown') },
              { value: 'true', label: t(item.yes) },
              { value: 'false', label: t(item.no) },
            ]}
          />
        </Field>
      ))}
      <Field>
        <FieldLabel htmlFor={`${id}-ttft`}>
          {t('Time to first token (ms)')}
        </FieldLabel>
        <Input
          id={`${id}-ttft`}
          type='number'
          min={0}
          step={1}
          disabled={props.disabled}
          value={props.value.ttft_ms ?? ''}
          onChange={(event) =>
            props.onChange({
              ...props.value,
              ttft_ms:
                event.target.value === '' ? null : Number(event.target.value),
            })
          }
        />
      </Field>
      <Field>
        <FieldLabel htmlFor={`${id}-checked`}>{t('Last checked')}</FieldLabel>
        <Input
          id={`${id}-checked`}
          type='datetime-local'
          step={1}
          disabled={props.disabled}
          value={
            props.value.last_checked_at == null
              ? ''
              : dayjs
                  .unix(props.value.last_checked_at)
                  .format('YYYY-MM-DDTHH:mm:ss')
          }
          onChange={(event) =>
            props.onChange({
              ...props.value,
              last_checked_at:
                event.target.value === ''
                  ? null
                  : Math.floor(new Date(event.target.value).getTime() / 1000),
            })
          }
        />
      </Field>
    </FieldGroup>
  )
}
