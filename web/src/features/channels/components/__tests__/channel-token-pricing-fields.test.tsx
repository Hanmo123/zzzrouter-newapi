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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useForm } from 'react-hook-form'
import { describe, it, expect, vi } from 'vitest'

import { Button } from '@/components/ui/button'
import { Form } from '@/components/ui/form'
import { api } from '@/lib/api'

import {
  CHANNEL_FORM_DEFAULT_VALUES,
  channelFormSchema,
  transformFormDataToUpdatePayload,
  type ChannelFormValues,
} from '../../lib/channel-form'
import { channelModelConfigsSchema } from '../../lib/channel-token-pricing'
import { ChannelTokenPricingFields } from '../channel-token-pricing-fields'

function PricingForm(props: {
  save: (data: ChannelFormValues) => void
  empty?: boolean
  disabled?: boolean
}) {
  const form = useForm<ChannelFormValues>({
    resolver: zodResolver(channelFormSchema),
    defaultValues: {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'channel',
      key: 'key',
      models: props.empty ? '' : 'public.model-a,model-b',
      model_configs: props.empty
        ? {}
        : {
            'model-b': {
              upstream_discount: 0.6,
              public_ratio: 0.8,
              is_online: false,
            },
          },
    },
  })
  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(props.save)}>
        <ChannelTokenPricingFields
          control={form.control}
          disabled={props.disabled}
        />
        <Button type='submit'>Save models</Button>
      </form>
    </Form>
  )
}
function renderForm(
  save = vi.fn(),
  options: { empty?: boolean; disabled?: boolean } = {}
) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <PricingForm save={save} {...options} />
    </QueryClientProvider>
  )
  return save
}

describe('channel model configuration', () => {
  it('edits one model discount, ratio and observations without changing another model', async () => {
    const save = renderForm()
    const user = userEvent.setup()
    expect(
      screen.queryByRole('spinbutton', { name: 'Public ratio' })
    ).not.toBeInTheDocument()
    await user.click(
      screen.getByRole('button', { name: 'Configure model: public.model-a' })
    )
    fireEvent.change(screen.getByRole('spinbutton', { name: 'Public ratio' }), {
      target: { value: '.2' },
    })
    fireEvent.change(
      screen.getByRole('spinbutton', { name: 'Upstream discount' }),
      { target: { value: '.8' } }
    )
    fireEvent.change(
      screen.getByRole('spinbutton', { name: 'Time to first token (ms)' }),
      { target: { value: '0' } }
    )
    await user.click(
      screen.getByRole('combobox', { name: 'Callable (online)' })
    )
    await user.click(screen.getByRole('option', { name: 'Online' }))
    await user.click(screen.getByRole('button', { name: 'Save models' }))
    await waitFor(() => expect(save).toHaveBeenCalled())
    const payload = transformFormDataToUpdatePayload(save.mock.calls[0][0], 1)
    expect(payload.model_configs).toEqual({
      'public.model-a': {
        public_ratio: 0.2,
        upstream_discount: 0.8,
        ttft_ms: 0,
        is_online: true,
      },
      'model-b': {
        public_ratio: 0.8,
        upstream_discount: 0.6,
        is_online: false,
      },
    })
    expect(payload).not.toHaveProperty('public_ratio')
    expect(payload).not.toHaveProperty('upstream_ratio')
  })
  it('uses expression preview as a read-only ratio and preserves the full expression', async () => {
    vi.spyOn(api, 'post').mockResolvedValue({
      data: {
        success: true,
        data: { public_ratio: { min: 0.13, max: 0.15 }, schedulable: true },
      },
    })
    const save = renderForm()
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('button', { name: 'Configure model: public.model-a' })
    )
    await user.click(
      screen.getByRole('switch', { name: 'Use model billing expression' })
    )
    expect(
      screen.queryByRole('spinbutton', { name: 'Public ratio' })
    ).not.toBeInTheDocument()
    expect(await screen.findByText('0.13x~0.15x')).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Save models' }))
    await waitFor(() => expect(save).toHaveBeenCalled())
    expect(save.mock.calls[0][0].model_configs['public.model-a']).toEqual({
      billing_expression: 'tier("base", p * 0 + c * 0)',
    })
    await user.click(
      screen.getByRole('switch', { name: 'Use model billing expression' })
    )
    expect(
      screen.getByRole('spinbutton', { name: 'Public ratio' })
    ).toHaveValue(1)
  })
  it('reports uncomparable pricing and server validation failures without inventing a ratio', async () => {
    vi.spyOn(api, 'post').mockResolvedValue({
      data: { success: true, data: { public_ratio: null, schedulable: false } },
    })
    renderForm()
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('button', { name: 'Configure model: public.model-a' })
    )
    await user.click(
      screen.getByRole('switch', { name: 'Use model billing expression' })
    )
    expect(
      await screen.findByText(
        'No comparable system token prices. This model is excluded from dispatch.'
      )
    ).toBeVisible()
  })
  it('rejects mixed manual/expression pricing, invalid discounts and invalid expressions', () => {
    for (const cfg of [
      { upstream_discount: 1.1 },
      { upstream_discount: -1 },
      { public_ratio: -1 },
      { billing_expression: 'tier("x",p)', public_ratio: 1 },
      { billing_expression: 'tier(' },
      { ttft_ms: -1 },
    ]) {
      expect(channelModelConfigsSchema.safeParse({ m: cfg }).success).toBe(
        false
      )
    }
    expect(
      channelModelConfigsSchema.safeParse({
        m: { public_ratio: 0, upstream_discount: 0, ttft_ms: 0 },
      }).success
    ).toBe(true)
  })
  it('provides search and an empty state without channel-level price inputs', () => {
    renderForm(vi.fn(), { empty: true })
    expect(screen.getByText('No models')).toBeVisible()
    expect(screen.queryByRole('spinbutton')).not.toBeInTheDocument()
  })
})
