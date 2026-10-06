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
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useForm } from 'react-hook-form'
import { describe, expect, it, vi } from 'vitest'

import { Button } from '@/components/ui/button'
import { Form } from '@/components/ui/form'

import {
  CHANNEL_FORM_DEFAULT_VALUES,
  channelFormSchema,
  transformChannelToFormDefaults,
  transformFormDataToUpdatePayload,
  type ChannelFormValues,
} from '../../lib/channel-form'
import { channelTokenPricesSchema } from '../../lib/channel-token-pricing'
import { channelSchema } from '../../types'
import { ChannelTokenPricingFields } from '../channel-token-pricing-fields'

function PricingForm(props: {
  save: (values: ChannelFormValues) => void
  disabled?: boolean
  empty?: boolean
}) {
  const form = useForm<ChannelFormValues>({
    resolver: zodResolver(channelFormSchema),
    defaultValues: {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'channel',
      key: 'private',
      models: props.empty ? '' : 'public.model-a,model-b',
      model_token_prices: props.empty ? {} : { 'model-b': { p: 5, c: 8 } },
    },
  })
  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(props.save)}>
        <ChannelTokenPricingFields
          control={form.control}
          disabled={props.disabled}
        />
        <Button type='submit'>Save prices</Button>
      </form>
    </Form>
  )
}

describe('channel model token prices', () => {
  it('saves expensive cached input and explicit zero output independently without changing another model', async () => {
    const save = vi.fn()
    render(<PricingForm save={save} />)
    const user = userEvent.setup()
    expect(
      screen.queryByRole('textbox', { name: 'Input price' })
    ).not.toBeInTheDocument()
    await user.click(
      screen.getByRole('switch', { name: 'Override system model pricing' })
    )
    fireEvent.change(screen.getByRole('textbox', { name: 'Input price' }), {
      target: { value: '2' },
    })
    fireEvent.change(
      screen.getByRole('textbox', { name: 'Completion price' }),
      { target: { value: '0' } }
    )
    fireEvent.change(
      screen.getByRole('textbox', { name: 'Cache read price' }),
      { target: { value: '9' } }
    )
    fireEvent.change(
      screen.getByRole('textbox', { name: 'Cache create price' }),
      { target: { value: '3.75' } }
    )
    fireEvent.change(
      screen.getByRole('textbox', { name: 'Cache create (1h) price' }),
      { target: { value: '8' } }
    )
    await user.click(screen.getByRole('button', { name: 'Save prices' }))
    await waitFor(() => expect(save).toHaveBeenCalled())
    const payload = transformFormDataToUpdatePayload(save.mock.calls[0][0], 7)
    expect(JSON.parse(payload.settings ?? '{}')).toMatchObject({
      model_token_prices: {
        'public.model-a': { p: 2, c: 0, cr: 9, cc: 3.75, cc1h: 8 },
        'model-b': { p: 5, c: 8 },
      },
    })
    await user.click(screen.getByRole('combobox', { name: 'Model' }))
    await user.click(screen.getByRole('option', { name: 'model-b' }))
    expect(screen.getByRole('textbox', { name: 'Input price' })).toHaveValue(
      '5'
    )
    expect(
      screen.getByRole('textbox', { name: 'Completion price' })
    ).toHaveValue('8')
  })

  it('clears an optional category and disables one model without deleting other prices', async () => {
    const save = vi.fn()
    render(<PricingForm save={save} />)
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('switch', { name: 'Override system model pricing' })
    )
    fireEvent.change(screen.getByRole('textbox', { name: 'Input price' }), {
      target: { value: '0' },
    })
    fireEvent.change(
      screen.getByRole('textbox', { name: 'Completion price' }),
      { target: { value: '0' } }
    )
    const cache = screen.getByRole('textbox', { name: 'Cache read price' })
    fireEvent.change(cache, { target: { value: '0' } })
    await user.clear(cache)
    await user.click(screen.getByRole('button', { name: 'Save prices' }))
    await waitFor(() => expect(save).toHaveBeenCalled())
    expect(save.mock.calls[0][0].model_token_prices['public.model-a']).toEqual({
      p: 0,
      c: 0,
    })
    await user.click(
      screen.getByRole('switch', { name: 'Override system model pricing' })
    )
    await user.click(screen.getByRole('button', { name: 'Save prices' }))
    await waitFor(() => expect(save).toHaveBeenCalledTimes(2))
    expect(save.mock.calls[1][0].model_token_prices).toEqual({
      'model-b': { p: 5, c: 8 },
    })
  })

  it('marks missing required or excessive prices invalid and prevents saving', async () => {
    const save = vi.fn()
    render(<PricingForm save={save} />)
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('switch', { name: 'Override system model pricing' })
    )
    fireEvent.change(
      screen.getByRole('textbox', { name: 'Completion price' }),
      { target: { value: '0' } }
    )
    const input = screen.getByRole('textbox', { name: 'Input price' })
    fireEvent.change(input, { target: { value: '0' } })
    await user.clear(input)
    expect(input).toHaveAttribute('aria-invalid', 'true')
    fireEvent.change(input, { target: { value: '1000000001' } })
    await user.click(screen.getByRole('button', { name: 'Save prices' }))
    expect(input).toHaveAttribute('aria-invalid', 'true')
    expect(save).not.toHaveBeenCalled()
    fireEvent.change(input, { target: { value: '1' } })
    await user.click(screen.getByRole('button', { name: 'Save prices' }))
    await waitFor(() => expect(save).toHaveBeenCalled())
  })

  it('identifies invalid prices on another model when switching models before saving', async () => {
    const save = vi.fn()
    render(<PricingForm save={save} />)
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('switch', { name: 'Override system model pricing' })
    )
    await user.click(screen.getByRole('combobox', { name: 'Model' }))
    await user.click(screen.getByRole('option', { name: 'model-b' }))
    await user.click(screen.getByRole('button', { name: 'Save prices' }))
    expect(save).not.toHaveBeenCalled()
    expect(
      screen.getByText(/Token prices must be between.*public\.model-a/)
    ).toBeVisible()
  })

  it('keeps price controls read-only without sensitive channel write permission', async () => {
    render(<PricingForm save={vi.fn()} disabled />)
    expect(screen.getByRole('combobox', { name: 'Model' })).toBeDisabled()
    expect(
      screen.getByRole('switch', { name: 'Override system model pricing' })
    ).toHaveAttribute('aria-disabled', 'true')
  })

  it('disables overrides when no channel models or saved prices exist', () => {
    render(<PricingForm save={vi.fn()} empty />)
    expect(screen.getByRole('combobox', { name: 'Model' })).toBeDisabled()
    expect(
      screen.getByRole('switch', { name: 'Override system model pricing' })
    ).toHaveAttribute('aria-disabled', 'true')
    expect(
      screen.queryByRole('textbox', { name: 'Input price' })
    ).not.toBeInTheDocument()
  })

  it('round-trips saved model names with dots and preserves unrelated channel settings', () => {
    const channel = channelSchema.parse({
      id: 7,
      type: 1,
      name: 'channel',
      key: '',
      created_time: 0,
      test_time: 0,
      response_time: 0,
      balance_updated_time: 0,
      models: 'public.model-a',
      group: 'default',
      status: 1,
      channel_info: {},
      settings: JSON.stringify({
        allow_service_tier: true,
        model_token_prices: { 'public.model-a': { p: 2, c: 0, cr: 9 } },
      }),
    })
    const defaults = transformChannelToFormDefaults(channel)
    expect(defaults.model_token_prices).toEqual({
      'public.model-a': { p: 2, c: 0, cr: 9 },
    })
    expect(
      JSON.parse(transformFormDataToUpdatePayload(defaults, 7).settings ?? '{}')
    ).toMatchObject({
      allow_service_tier: true,
      model_token_prices: defaults.model_token_prices,
    })
    for (const invalid of [
      { p: -1, c: 2 },
      { p: Infinity, c: 2 },
      { p: 1 },
      { p: 1, c: 2, cr: null },
      { p: 1, c: 2, unknown: 1 },
    ]) {
      expect(channelTokenPricesSchema.safeParse({ m: invalid }).success).toBe(
        false
      )
    }
  })
})
