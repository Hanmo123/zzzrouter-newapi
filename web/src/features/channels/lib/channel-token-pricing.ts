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
import { z } from 'zod'

import { compileBillingExpression } from '@/features/pricing/lib/billing-expression/parser'

export const channelModelConfigSchema = z
  .object({
    pricing_source: z
      .object({
        provider: z.union([z.literal('newapi'), z.literal('sub2api')]),
        base_url: z.string().max(2048),
        group: z.string().max(128),
        expression: z.string().max(16384),
        source_hash: z.string(),
        fetched_at: z.number().int().nonnegative(),
        applied_expression: z.string().optional(),
        applied_ratio: z.number().nonnegative().optional(),
      })
      .optional(),
    upstream_discount: z.number().min(0).max(1).optional(),
    public_ratio: z.number().nonnegative().optional(),
    billing_expression: z.string().max(16384).optional(),
    is_degraded: z.boolean().nullish(),
    is_online: z.boolean().nullish(),
    ttft_ms: z.number().int().nonnegative().nullish(),
    last_checked_at: z.number().int().min(0).max(253402300799).nullish(),
  })
  .superRefine((config, ctx) => {
    if (config.billing_expression && config.public_ratio !== undefined) {
      ctx.addIssue({
        code: 'custom',
        message: 'Public ratio is automatic with an expression.',
      })
    }
    if (config.billing_expression) {
      if (
        compileBillingExpression(config.billing_expression).status !== 'ready'
      ) {
        ctx.addIssue({ code: 'custom', message: 'Invalid billing expression' })
      }
    }
  })
export const channelModelConfigsSchema = z.record(
  z
    .string()
    .min(1)
    .max(255)
    .refine((name) => name.trim() === name),
  channelModelConfigSchema
)
export type ChannelModelConfig = z.infer<typeof channelModelConfigSchema>
export type ChannelModelConfigs = z.infer<typeof channelModelConfigsSchema>
export type ModelRatioRange = { min: number; max: number }

export function modelRatioLabel(
  ratio: ModelRatioRange | null | undefined
): string {
  if (!ratio) return '—'
  const lower = Number(ratio.min.toPrecision(8))
  const upper = Number(ratio.max.toPrecision(8))
  return lower === upper ? `${lower}x` : `${lower}x~${upper}x`
}
