import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { formatQuota } from '@/lib/format'

import { useExpenseMutation, useExpenseQuery } from '../api'
import type {
  BillingSelf,
  Member,
  TeamDetail as Detail,
  Topup,
  Usage,
} from '../types'
import { Invitations } from './invitations'
import { NameForm } from './name-form'
import { PagedTable } from './paged-table'
import { TeamTopup } from './team-topup'

export function TeamDetail(props: { id: number; self: BillingSelf }) {
  const { t } = useTranslation()
  const query = useExpenseQuery<Detail>(`billing-units/${props.id}`)
  const mutation = useExpenseMutation()
  const [remove, setRemove] = useState<Member | null>(null)
  const [expectedTeamId, setExpectedTeamId] = useState(0)
  const [targetEnabled, setTargetEnabled] = useState(false)
  const [action, setAction] = useState<'select' | 'status' | null>(null)
  if (query.isPending) return <LoadingState />
  if (query.isError) return <ErrorState onRetry={() => void query.refetch()} />
  const detail = query.data
  const apply = () => {
    if (action === 'select') {
      mutation.mutate(
        {
          method: 'POST',
          path: `billing-units/${props.id}/select`,
          data: { expected_unit_id: expectedTeamId },
        },
        { onSuccess: () => setAction(null) }
      )
    }
    if (action === 'status') {
      mutation.mutate(
        {
          method: 'PATCH',
          path: `billing-units/${props.id}`,
          data: { enabled: targetEnabled },
        },
        { onSuccess: () => setAction(null) }
      )
    }
  }
  return (
    <section className='space-y-6 rounded-lg border p-4'>
      <div className='flex flex-wrap items-center justify-between gap-3'>
        <h2 className='text-xl font-semibold break-words'>
          {detail.unit.name}
        </h2>
        <span>{detail.enabled ? t('Enabled') : t('Paused')}</span>
      </div>
      <p>
        {t('Team balance')}: {formatQuota(detail.balance_quota)} · {t('Used')}:{' '}
        {formatQuota(detail.used_quota)}
      </p>
      <div className='flex flex-wrap gap-2'>
        <Button
          variant='outline'
          disabled={
            !props.self.self_service_enabled ||
            !detail.enabled ||
            props.self.billing_unit_id === props.id
          }
          onClick={() => {
            setExpectedTeamId(props.self.billing_unit_id ?? 0)
            setAction('select')
          }}
        >
          {t('Use this team')}
        </Button>
        <Button
          variant='outline'
          disabled={mutation.isPending}
          onClick={() => {
            setTargetEnabled(!detail.enabled)
            setAction('status')
          }}
        >
          {detail.enabled ? t('Pause team') : t('Resume team')}
        </Button>
      </div>
      {props.self.self_service_enabled && <NameForm teamId={props.id} />}
      <h3 className='font-semibold'>{t('Members')}</h3>
      <PagedTable<Member>
        path={`billing-units/${props.id}/members`}
        rowKey={(row) => row.user_id}
        columns={[
          {
            id: 'username',
            header: t('Username'),
            cell: (row) => row.username,
          },
          {
            id: 'name',
            header: t('Display name'),
            cell: (row) => row.display_name,
          },
          {
            id: 'actions',
            header: t('Actions'),
            cell: (row) => (
              <Button
                variant='outline'
                size='sm'
                disabled={mutation.isPending}
                onClick={() => setRemove(row)}
              >
                {t('Remove')}
              </Button>
            ),
          },
        ]}
      />
      <h3 className='font-semibold'>{t('Invitations')}</h3>
      <Invitations
        teamId={props.id}
        currentTeamId={props.self.billing_unit_id ?? 0}
        enabled={props.self.self_service_enabled}
      />
      <h3 className='font-semibold'>{t('Usage this month (UTC)')}</h3>
      <PagedTable<Usage>
        path={`billing-units/${props.id}/usage-summary`}
        rowKey={(row) => row.user_id}
        columns={[
          {
            id: 'user',
            header: t('Username'),
            cell: (row) => row.username || row.user_id,
          },
          {
            id: 'quota',
            header: t('Cost'),
            cell: (row) => formatQuota(row.quota),
          },
          {
            id: 'requests',
            header: t('Requests'),
            cell: (row) => row.requests,
          },
          {
            id: 'tokens',
            header: t('Tokens'),
            cell: (row) => row.prompt_tokens + row.completion_tokens,
          },
        ]}
      />
      {props.self.topup_enabled && <TeamTopup team={detail.unit} />}
      <h3 className='font-semibold'>{t('Team top-up history')}</h3>
      <PagedTable<Topup>
        path={`billing-units/${props.id}/topups`}
        rowKey={(row) => row.id}
        columns={[
          { id: 'trade', header: t('Order'), cell: (row) => row.trade_no },
          {
            id: 'money',
            header: t('Paid (CNY)'),
            cell: (row) => row.money.toFixed(2),
          },
          { id: 'status', header: t('Status'), cell: (row) => t(row.status) },
        ]}
      />
      <ConfirmDialog
        open={remove !== null}
        onOpenChange={(open) => {
          if (!open) setRemove(null)
        }}
        title={t('Remove member')}
        desc={t(
          'Remove {{user}}? Future requests will use their personal balance. Earlier team charges remain unchanged.',
          { user: remove?.username }
        )}
        destructive
        isLoading={mutation.isPending}
        handleConfirm={() => {
          if (remove) {
            mutation.mutate(
              {
                method: 'DELETE',
                path: `billing-units/${props.id}/members/${remove.user_id}`,
              },
              { onSuccess: () => setRemove(null) }
            )
          }
        }}
      />
      <ConfirmDialog
        open={action !== null}
        onOpenChange={(open) => {
          if (!open) setAction(null)
        }}
        title={
          action === 'select'
            ? t('Switch billing team')
            : t('Change team availability')
        }
        desc={
          action === 'select'
            ? t(
                'Future requests will use {{team}}. Earlier charges remain unchanged.',
                { team: detail.unit.name }
              )
            : t(
                'Pausing blocks future team requests, including personal fallback. Requests already running keep their original payer.'
              )
        }
        isLoading={mutation.isPending}
        handleConfirm={apply}
      />
    </section>
  )
}
