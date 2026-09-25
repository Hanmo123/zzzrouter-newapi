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
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useId, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Dialog } from '@/components/dialog'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { getServerErrorMessage } from '@/lib/server-error-message'

import { providersQueryKey, saveProvider } from '../api'
import { providerFormSchema, type Provider } from '../types'

export function ProviderEditor(props: {
  provider?: Provider
  onClose: () => void
}) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const formId = useId()
  const [confirmClose, setConfirmClose] = useState(false)
  const form = useForm({
    resolver: zodResolver(providerFormSchema),
    defaultValues: {
      name: props.provider?.name ?? '',
      description: props.provider?.description ?? '',
    },
  })
  const save = useMutation({
    mutationFn: (values: { name: string; description: string }) =>
      saveProvider(values, props.provider?.id),
    meta: { errorToast: false },
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: providersQueryKey })
      toast.success(t('Provider saved successfully'))
      props.onClose()
    },
  })
  const isDirty = form.formState.isDirty
  const close = () => {
    if (save.isPending) return
    if (isDirty) setConfirmClose(true)
    else props.onClose()
  }

  return (
    <>
      <Dialog
        open
        onOpenChange={(open) => {
          if (!open) close()
        }}
        title={props.provider ? t('Edit Provider') : t('Add Provider')}
        description={t(
          'Providers operate channels. Model vendors and channel protocol types are managed separately.'
        )}
        footer={
          <>
            <Button variant='outline' onClick={close} disabled={save.isPending}>
              {t('Cancel')}
            </Button>
            <Button type='submit' form={formId} disabled={save.isPending}>
              {save.isPending ? t('Saving...') : t('Save')}
            </Button>
          </>
        }
      >
        <Form {...form}>
          <form
            id={formId}
            className='space-y-4'
            onSubmit={form.handleSubmit((values) => save.mutate(values))}
          >
            <FormField
              control={form.control}
              name='name'
              render={({ field }) => (
                <FormItem>
                  <FormLabel required>{t('Provider Name')}</FormLabel>
                  <FormControl>
                    <Input
                      {...field}
                      maxLength={128}
                      disabled={save.isPending}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='description'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Description')}</FormLabel>
                  <FormControl>
                    <Textarea
                      {...field}
                      rows={4}
                      maxLength={4096}
                      disabled={save.isPending}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            {save.isError && (
              <Alert variant='destructive'>
                <AlertDescription>
                  {getServerErrorMessage(save.error)}
                </AlertDescription>
              </Alert>
            )}
          </form>
        </Form>
      </Dialog>
      <ConfirmDialog
        open={confirmClose}
        onOpenChange={setConfirmClose}
        title={t('Discard unsaved changes?')}
        desc={t('Your changes will be lost.')}
        confirmText={t('Discard')}
        destructive
        handleConfirm={props.onClose}
      />
    </>
  )
}
