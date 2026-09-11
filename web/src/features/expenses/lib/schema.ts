import { z } from 'zod'

export const teamNameSchema = z.object({
  value: z.string().trim().min(1).max(128),
})
export const inviteNameSchema = z.object({
  value: z
    .string()
    .min(1)
    .max(128)
    .refine(
      (v) =>
        v === v.trim() &&
        !/[\\/]/.test(v) &&
        !v.includes(String.fromCharCode(0))
    ),
})
export const topupSchema = z.object({
  amount: z.number().int().positive().max(Number.MAX_SAFE_INTEGER),
})
