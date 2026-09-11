import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Button } from '@/components/ui/button'

import { useExpenseMutation } from '../api'
import type { Invitation } from '../types'
import { PagedTable } from './paged-table'

export function Invitations(props: {
  teamId?: number
  currentTeamId: number
  enabled: boolean
}) {
  const { t } = useTranslation()
  const [selected, setSelected] = useState<
    (Invitation & { expectedUnitId: number }) | null
  >(null)
  const mutation = useExpenseMutation()
  const respond = (
    id: number,
    action: string,
    expectedUnitId = props.currentTeamId
  ) =>
    mutation.mutate(
      {
        method: 'POST',
        path: `billing-unit-invitations/${id}/${action}`,
        data: { expected_unit_id: expectedUnitId },
      },
      { onSuccess: () => setSelected(null) }
    )
  return (
    <>
      <PagedTable<Invitation>
        path={
          props.teamId
            ? `billing-units/${props.teamId}/invitations`
            : 'billing-unit-invitations/self'
        }
        rowKey={(row) => row.id}
        columns={[
          {
            id: 'name',
            header: props.teamId ? t('Username') : t('Team'),
            cell: (row) => (props.teamId ? row.username : row.team_name),
          },
          { id: 'status', header: t('Status'), cell: (row) => t(row.status) },
          {
            id: 'actions',
            header: t('Actions'),
            cell: (row) =>
              row.status === 'pending' && (
                <div className='flex gap-2'>
                  {!props.teamId && (
                    <Button
                      size='sm'
                      disabled={!props.enabled || mutation.isPending}
                      onClick={() =>
                        setSelected({
                          ...row,
                          expectedUnitId: props.currentTeamId,
                        })
                      }
                    >
                      {t('Accept')}
                    </Button>
                  )}
                  <Button
                    size='sm'
                    variant='outline'
                    disabled={!props.enabled || mutation.isPending}
                    onClick={() =>
                      respond(row.id, props.teamId ? 'revoke' : 'reject')
                    }
                  >
                    {props.teamId ? t('Revoke') : t('Reject')}
                  </Button>
                </div>
              ),
          },
        ]}
      />
      <ConfirmDialog
        open={selected !== null}
        onOpenChange={(open) => {
          if (!open) setSelected(null)
        }}
        title={t('Join team')}
        desc={t(
          'Join {{team}}? Future requests will use this team. This replaces your current billing team; earlier charges remain unchanged.',
          { team: selected?.team_name }
        )}
        isLoading={mutation.isPending}
        handleConfirm={() => {
          if (selected) respond(selected.id, 'accept', selected.expectedUnitId)
        }}
      />
    </>
  )
}
