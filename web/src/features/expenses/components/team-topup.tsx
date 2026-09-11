import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Button } from '@/components/ui/button'
import { Field, FieldLabel, FieldError } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { getTopupInfo } from '@/features/wallet/api'
import { submitPaymentForm } from '@/features/wallet/lib/payment'
import { requireServerSuccess } from '@/lib/server-error-message'

import { teamPayment } from '../api'
import { topupSchema } from '../lib/schema'
import type { Team } from '../types'

export function TeamTopup(props: { team: Team }) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const [quote, setQuote] = useState<{ amount: number; money: string } | null>(
    null
  )
  const config = useQuery({
    queryKey: ['expenses', 'topup-info'],
    queryFn: async () => requireServerSuccess(await getTopupInfo()).data,
  })
  const form = useForm<{ amount: number }>({
    resolver: zodResolver(topupSchema),
    defaultValues: { amount: 1 },
  })
  const preview = useMutation({
    mutationFn: async (amount: number) => {
      const result = await teamPayment<string>(props.team.id, true, amount)
      return { amount, money: result.data }
    },
    onSuccess: setQuote,
  })
  const pay = useMutation({
    mutationFn: (amount: number) =>
      teamPayment<Record<string, unknown>>(props.team.id, false, amount),
    onSuccess: (result) => {
      if (result.url) submitPaymentForm(result.url, result.data)
      setQuote(null)
      void client.invalidateQueries({ queryKey: ['expenses'] })
    },
  })
  const available =
    config.data?.enable_online_topup &&
    config.data.pay_methods.some((m) => m.type === 'alipay')
  if (!available) {
    return (
      <p className='text-muted-foreground'>
        {t('Team Alipay top-up is currently unavailable.')}
      </p>
    )
  }
  return (
    <>
      <form
        className='space-y-3'
        onSubmit={form.handleSubmit((values) => preview.mutate(values.amount))}
      >
        <Field>
          <FieldLabel htmlFor='team-topup'>{t('Top-up amount')}</FieldLabel>
          <Input
            id='team-topup'
            type='number'
            min={config.data?.min_topup ?? 1}
            step='1'
            {...form.register('amount', { valueAsNumber: true })}
            aria-invalid={!!form.formState.errors.amount}
          />
          {form.formState.errors.amount && (
            <FieldError>{t('Enter a positive whole amount.')}</FieldError>
          )}
        </Field>
        <Button type='submit' disabled={preview.isPending || pay.isPending}>
          {t('Top up team with Alipay')}
        </Button>
      </form>
      <ConfirmDialog
        open={quote !== null}
        onOpenChange={(open) => {
          if (!open) setQuote(null)
        }}
        title={t('Confirm team top-up')}
        desc={t(
          'Pay ¥{{money}} into {{team}}. This balance belongs to the team and is not credited to your personal wallet.',
          { money: quote?.money, team: props.team.name }
        )}
        isLoading={pay.isPending}
        handleConfirm={() => {
          if (quote) pay.mutate(quote.amount)
        }}
      />
    </>
  )
}
