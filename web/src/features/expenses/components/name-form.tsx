import { zodResolver } from '@hookform/resolvers/zod'
import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Field,
  FieldGroup,
  FieldLabel,
  FieldError,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'

import { useExpenseMutation } from '../api'
import { teamNameSchema, inviteNameSchema } from '../lib/schema'

export function NameForm(props: { teamId?: number }) {
  const { t } = useTranslation()
  const [requestKey, setRequestKey] = useState(() => crypto.randomUUID())
  const mutation = useExpenseMutation()
  const form = useForm<{ value: string }>({
    resolver: zodResolver(props.teamId ? inviteNameSchema : teamNameSchema),
    defaultValues: { value: '' },
  })
  const label = props.teamId ? t('Invite by username') : t('Team name')
  return (
    <form
      className='space-y-3'
      onSubmit={form.handleSubmit((values) => {
        const path = props.teamId
          ? `billing-units/${props.teamId}/invitations`
          : 'billing-units/self-service'
        const data = props.teamId
          ? { username: values.value }
          : { name: values.value, request_key: requestKey }
        mutation.mutate(
          { method: 'POST', path, data },
          {
            onSuccess: () => {
              form.reset()
              setRequestKey(crypto.randomUUID())
            },
          }
        )
      })}
    >
      <FieldGroup>
        <Field data-invalid={!!form.formState.errors.value}>
          <FieldLabel htmlFor={`name-${props.teamId ?? 'create'}`}>
            {label}
          </FieldLabel>
          <Input
            id={`name-${props.teamId ?? 'create'}`}
            {...form.register('value')}
            aria-invalid={!!form.formState.errors.value}
            maxLength={128}
            disabled={mutation.isPending}
          />
          {form.formState.errors.value && (
            <FieldError>
              {t('Enter a valid name (1–128 characters).')}
            </FieldError>
          )}
        </Field>
      </FieldGroup>
      <Button type='submit' disabled={mutation.isPending}>
        {props.teamId ? t('Send invitation') : t('Create team')}
      </Button>
      {mutation.isSuccess && (
        <p role='status'>
          {props.teamId
            ? t('Invitation sent. Membership changes only after acceptance.')
            : t('Team created with a zero balance.')}
        </p>
      )}
    </form>
  )
}
