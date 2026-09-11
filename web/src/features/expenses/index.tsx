import { Link } from '@tanstack/react-router'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { ErrorState } from '@/components/error-state'
import { Main } from '@/components/layout'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { formatQuota } from '@/lib/format'

import { useBillingSelf, useExpenseMutation } from './api'
import { Invitations } from './components/invitations'
import { NameForm } from './components/name-form'
import { PagedTable } from './components/paged-table'
import { TeamDetail } from './components/team-detail'
import type { Team } from './types'

export function Expenses() {
  const { t } = useTranslation()
  const query = useBillingSelf()
  const mutation = useExpenseMutation()
  const [teamId, setTeamId] = useState<number | null>(null)
  const [expectedTeamId, setExpectedTeamId] = useState<number | null>(null)
  const [targetFallback, setTargetFallback] = useState(false)
  const [confirm, setConfirm] = useState<'leave' | 'fallback' | null>(null)
  if (query.isPending) return <LoadingState />
  if (query.isError) return <ErrorState onRetry={() => void query.refetch()} />
  const self = query.data
  return (
    <Main className='mx-auto max-w-5xl space-y-8'>
      <div>
        <h1 className='text-2xl font-semibold'>{t('Expenses')}</h1>
        <p className='text-muted-foreground'>
          {t('Manage personal spending and shared team balances.')}
        </p>
      </div>
      <section className='space-y-3 rounded-lg border p-4'>
        <h2 className='text-lg font-semibold'>{t('My billing')}</h2>
        <p>
          {t('Current payer')}: {self.name ?? t('Personal wallet')}
          {self.billing_unit_id && !self.enabled ? ` (${t('Paused')})` : ''}
        </p>
        <p>
          {t('Personal balance')}: {formatQuota(self.personal_quota)}
        </p>
        <p className='text-muted-foreground'>
          {t(
            'Each request is recorded under your own account. Team charges are paid from the shared balance.'
          )}
        </p>
        <div className='flex flex-wrap gap-3'>
          <Button variant='outline' render={<Link to='/wallet' />}>
            {t('Top up personal wallet')}
          </Button>
          <Button variant='outline' render={<Link to='/usage-logs' />}>
            {t('My usage')}
          </Button>
          {!!self.billing_unit_id && (
            <Button
              variant='outline'
              onClick={() => {
                setExpectedTeamId(self.billing_unit_id)
                setConfirm('leave')
              }}
            >
              {t('Leave billing team')}
            </Button>
          )}
        </div>
        <p>
          {t('Personal fallback')}:{' '}
          {self.personal_billing_fallback && self.fallback_enabled
            ? t('Enabled')
            : t('Disabled')}
        </p>
        <Button
          variant='outline'
          disabled={
            mutation.isPending ||
            (!self.fallback_enabled && !self.personal_billing_fallback)
          }
          onClick={() => {
            setTargetFallback(!self.personal_billing_fallback)
            setConfirm('fallback')
          }}
        >
          {self.personal_billing_fallback
            ? t('Disable personal fallback')
            : t('Enable personal fallback')}
        </Button>
      </section>
      <section className='space-y-3'>
        <h2 className='text-lg font-semibold'>{t('My invitations')}</h2>
        <Invitations
          currentTeamId={self.billing_unit_id ?? 0}
          enabled={self.self_service_enabled}
        />
      </section>
      <section className='space-y-3'>
        <h2 className='text-lg font-semibold'>{t('Teams I manage')}</h2>
        {self.self_service_enabled && <NameForm />}
        <PagedTable<Team>
          path='billing-units'
          rowKey={(row) => row.id}
          columns={[
            { id: 'name', header: t('Team'), cell: (row) => row.name },
            {
              id: 'manage',
              header: t('Actions'),
              cell: (row) => (
                <Button
                  variant='outline'
                  size='sm'
                  onClick={() => setTeamId(row.id)}
                >
                  {t('Manage')}
                </Button>
              ),
            },
          ]}
        />
      </section>
      {teamId !== null && <TeamDetail key={teamId} id={teamId} self={self} />}
      <ConfirmDialog
        open={confirm !== null}
        onOpenChange={(open) => {
          if (!open) setConfirm(null)
        }}
        title={
          confirm === 'leave' ? t('Leave billing team') : t('Personal fallback')
        }
        desc={
          confirm === 'leave'
            ? t(
                'Future requests will use your personal balance. Earlier team charges remain unchanged.'
              )
            : t(
                'When enabled, insufficient team funds before a request may charge your personal wallet. Paused teams and other errors never trigger fallback.'
              )
        }
        isLoading={mutation.isPending}
        handleConfirm={() =>
          mutation.mutate(
            confirm === 'leave'
              ? {
                  method: 'DELETE',
                  path: 'billing-units/self/membership',
                  data: { expected_unit_id: expectedTeamId },
                }
              : {
                  method: 'PUT',
                  path: 'billing-units/self/preference',
                  data: {
                    personal_billing_fallback: targetFallback,
                  },
                },
            { onSuccess: () => setConfirm(null) }
          )
        }
      />
    </Main>
  )
}
