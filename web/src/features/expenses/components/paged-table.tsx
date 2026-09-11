import { getCoreRowModel, useReactTable } from '@tanstack/react-table'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  StaticDataTable,
  DataTablePagination,
  type StaticDataTableColumn,
} from '@/components/data-table'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'

import { useExpenseQuery } from '../api'
import type { Page } from '../types'

export function PagedTable<T>(props: {
  path: string
  columns: StaticDataTableColumn<T>[]
  rowKey: (row: T) => number
}) {
  const { t } = useTranslation()
  const [pagination, setPagination] = useState({ pageIndex: 0, pageSize: 10 })
  const query = useExpenseQuery<Page<T>>(
    `${props.path}?p=${pagination.pageIndex + 1}&page_size=${pagination.pageSize}`
  )
  const table = useReactTable({
    data: query.data?.items ?? [],
    columns: [],
    state: { pagination },
    onPaginationChange: setPagination,
    manualPagination: true,
    rowCount: query.data?.total ?? 0,
    getCoreRowModel: getCoreRowModel(),
  })
  if (query.isPending) return <LoadingState />
  if (query.isError) return <ErrorState onRetry={() => void query.refetch()} />
  return (
    <div className='space-y-3'>
      <StaticDataTable
        columns={props.columns}
        data={query.data.items}
        getRowKey={props.rowKey}
        emptyContent={t('No records yet')}
      />
      <DataTablePagination table={table} compact />
    </div>
  )
}
