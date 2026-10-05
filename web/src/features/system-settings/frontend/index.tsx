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
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import * as z from 'zod'

import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'

import { useSystemOptions } from '../hooks/use-system-options'
import { useUpdateOption } from '../hooks/use-update-option'
import { DailyUsageSettings } from './daily-usage-settings'

function boardSchema(t: (key: string) => string) {
  return z
    .object({
      models: z.string(),
      defaultModel: z.string().trim().min(1).max(200),
    })
    .superRefine((values, context) => {
      const models = values.models
        .split('\n')
        .map((name) => name.trim())
        .filter(Boolean)
      if (
        models.length > 100 ||
        new Set(models).size !== models.length ||
        models.some(
          (name) =>
            name.length > 200 || name.includes(',') || name.includes('\0')
        )
      ) {
        context.addIssue({
          code: 'custom',
          path: ['models'],
          message: t('Enter up to 100 unique model names, one per line'),
        })
      }
      if (
        values.defaultModel.includes(',') ||
        values.defaultModel.includes('\n') ||
        values.defaultModel.includes('\0') ||
        (models.length > 0 && !models.includes(values.defaultModel))
      ) {
        context.addIssue({
          code: 'custom',
          path: ['defaultModel'],
          message: t('Default model must be one of the displayed models'),
        })
      }
    })
}

type BoardForm = { models: string; defaultModel: string }

function HomepageBoardForm(props: { raw?: string }) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  let initial: { models: string[]; default_model: string } = {
    models: [],
    default_model: 'gpt-6-astra',
  }
  if (props.raw) {
    try {
      const parsed: unknown = JSON.parse(props.raw)
      if (
        parsed &&
        typeof parsed === 'object' &&
        'models' in parsed &&
        'default_model' in parsed &&
        Array.isArray(parsed.models) &&
        parsed.models.every((name: unknown) => typeof name === 'string') &&
        typeof parsed.default_model === 'string'
      ) {
        initial = { models: parsed.models, default_model: parsed.default_model }
      }
    } catch {
      // Keep legacy defaults when an existing option is malformed.
    }
  }
  const form = useForm<BoardForm>({
    resolver: zodResolver(boardSchema(t)),
    defaultValues: {
      models: initial.models.join('\n'),
      defaultModel: initial.default_model,
    },
  })

  const onSubmit = (values: BoardForm) => {
    updateOption.mutate({
      key: 'frontend.homepage_board',
      value: JSON.stringify({
        models: values.models
          .split('\n')
          .map((name) => name.trim())
          .filter(Boolean),
        default_model: values.defaultModel.trim(),
      }),
    })
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('Homepage quality board')}</CardTitle>
        <CardDescription>
          {t(
            'Choose which models appear on the homepage. The full leaderboard remains available to everyone.'
          )}
        </CardDescription>
      </CardHeader>
      <Form {...form}>
        <form onSubmit={form.handleSubmit(onSubmit)}>
          <CardContent>
            <div className='flex flex-col gap-5'>
              <FormField
                control={form.control}
                name='models'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Displayed models')}</FormLabel>
                    <FormControl>
                      <Textarea
                        rows={8}
                        placeholder={'gpt-6-astra\nclaude-sonnet-4-5'}
                        {...field}
                      />
                    </FormControl>
                    <p className='text-muted-foreground text-sm'>
                      {t(
                        'One model per line, in display order. Leave blank to show the public model directory (up to 100 models).'
                      )}
                    </p>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='defaultModel'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Default selected model')}</FormLabel>
                    <FormControl>
                      <Input {...field} />
                    </FormControl>
                    <p className='text-muted-foreground text-sm'>
                      {t(
                        'When models are specified above, the default must be included in the list.'
                      )}
                    </p>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </div>
          </CardContent>
          <CardFooter className='mt-4 justify-end'>
            <Button type='submit' disabled={updateOption.isPending}>
              {t('Save settings')}
            </Button>
          </CardFooter>
        </form>
      </Form>
    </Card>
  )
}

export function FrontendSettings() {
  const { t } = useTranslation()
  const options = useSystemOptions()
  const raw = options.data?.data?.find(
    (item) => item.key === 'frontend.homepage_board'
  )?.value

  let content = (
    <Tabs defaultValue='homepage-board' className='max-w-5xl'>
      <TabsList>
        <TabsTrigger value='homepage-board'>
          {t('Homepage quality board')}
        </TabsTrigger>
        <TabsTrigger value='daily-usage'>
          {t('Daily usage display')}
        </TabsTrigger>
      </TabsList>
      <TabsContent value='homepage-board' className='pt-4'>
        <HomepageBoardForm key={raw ?? ''} raw={raw} />
      </TabsContent>
      <TabsContent value='daily-usage' className='pt-4'>
        <DailyUsageSettings />
      </TabsContent>
    </Tabs>
  )
  if (options.isLoading) {
    content = (
      <p className='text-muted-foreground'>{t('Loading settings...')}</p>
    )
  } else if (options.isError && !options.data) {
    content = (
      <ErrorState
        title={t('Failed to load settings')}
        onRetry={() => void options.refetch()}
      />
    )
  }

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>
        {t('Frontend Configuration')}
      </SectionPageLayout.Title>
      <SectionPageLayout.Content>{content}</SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
