import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { api } from '@/lib/api'
import {
  requireServerSuccess,
  createServerError,
} from '@/lib/server-error-message'

import type { BillingSelf } from './types'

export async function expenseRequest<T>(
  method: string,
  path: string,
  data?: unknown
): Promise<T> {
  const response = await api.request<{ success: boolean; data: T }>({
    method,
    url: `/api/${path}`,
    data,
  })
  return requireServerSuccess(response.data).data
}
export function useExpenseQuery<T>(path: string) {
  return useQuery({
    queryKey: ['expenses', path],
    queryFn: () => expenseRequest<T>('GET', path),
  })
}
export function useBillingSelf() {
  return useExpenseQuery<BillingSelf>('billing-units/self')
}
export function useExpenseMutation() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (v: { method: string; path: string; data?: unknown }) =>
      expenseRequest<unknown>(v.method, v.path, v.data),
    onSettled: () => client.invalidateQueries({ queryKey: ['expenses'] }),
  })
}
export async function teamPayment<T>(
  id: number,
  quote: boolean,
  amount: number
): Promise<{ data: T; url?: string }> {
  const response = await api.post(
    `/api/billing-units/${id}/topups${quote ? '/quote' : ''}`,
    { amount, payment_method: 'alipay' }
  )
  if (response.data.message !== 'success') {
    throw createServerError(response.data)
  }
  return response.data
}
