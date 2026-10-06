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

const price = z.number().min(0).max(1_000_000_000)

export const channelTokenPricesSchema = z.record(
  z
    .string()
    .min(1)
    .refine((name) => name.trim() === name),
  z.strictObject({
    p: price,
    c: price,
    cr: price.optional(),
    cc: price.optional(),
    cc1h: price.optional(),
    img: price.optional(),
    img_cr: price.optional(),
    img_o: price.optional(),
    ai: price.optional(),
    ao: price.optional(),
  })
)

export type ChannelTokenPrices = z.infer<typeof channelTokenPricesSchema>
