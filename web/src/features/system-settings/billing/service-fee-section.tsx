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
import { zodResolver } from '@hookform/resolvers/zod'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'

import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'

import { FormDirtyIndicator } from '../components/form-dirty-indicator'
import { FormNavigationGuard } from '../components/form-navigation-guard'
import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useSettingsForm } from '../hooks/use-settings-form'
import { useUpdateOption } from '../hooks/use-update-option'

const schema = z.object({
  enabled: z.boolean(),
  percent: z.number().min(0).max(100),
})
const storedSchema = z.object({
  enabled: z.boolean(),
  rate: z.number().min(0).max(1),
})

export function ServiceFeeSection(props: { value: string }) {
  const { t } = useTranslation()
  const update = useUpdateOption()
  let defaults = { enabled: true, percent: 5 }
  try {
    const stored = storedSchema.parse(JSON.parse(props.value))
    defaults = { enabled: stored.enabled, percent: stored.rate * 100 }
  } catch {
    /* Invalid saved values are rejected by the server. */
  }
  const { form, handleSubmit, isDirty, isSubmitting } = useSettingsForm({
    defaultValues: defaults,
    resolver: zodResolver(schema),
    onSubmit: async (values) => {
      await update.mutateAsync({
        key: 'RelayServiceFee',
        value: JSON.stringify({
          enabled: values.enabled,
          rate: values.percent / 100,
        }),
      })
    },
  })
  return (
    <SettingsSection title={t('Relay service fee')}>
      <FormNavigationGuard when={isDirty} />
      <Form {...form}>
        <SettingsForm onSubmit={handleSubmit}>
          <SettingsPageFormActions
            onSave={handleSubmit}
            isSaving={isSubmitting || update.isPending}
          />
          <FormDirtyIndicator isDirty={isDirty} />
          <FormField
            control={form.control}
            name='enabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Charge relay service fee')}</FormLabel>
                  <FormDescription>
                    {t(
                      'Final charge = model base charge × (1 + service fee rate) × group ratio. Procurement cost = model base charge × upstream discount.'
                    )}
                  </FormDescription>
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                  />
                </FormControl>
              </SettingsSwitchItem>
            )}
          />
          <FormField
            control={form.control}
            name='percent'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Service fee (%)')}</FormLabel>
                <FormControl>
                  <Input
                    type='number'
                    min={0}
                    max={100}
                    step='any'
                    value={Number.isNaN(field.value) ? '' : field.value}
                    onChange={(event) =>
                      field.onChange(event.currentTarget.valueAsNumber)
                    }
                    onBlur={field.onBlur}
                    name={field.name}
                    ref={field.ref}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Changes apply to new requests. In-flight requests and tasks retain their billing snapshot.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
