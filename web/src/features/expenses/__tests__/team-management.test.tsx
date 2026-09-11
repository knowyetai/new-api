import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { Invitations } from '../components/invitations'
import { NameForm } from '../components/name-form'
import { TeamTopup } from '../components/team-topup'

const clients: QueryClient[] = []
afterEach(() => {
  cleanup()
  clients.forEach((c) => c.clear())
  clients.length = 0
  vi.restoreAllMocks()
})
function mount(child: React.ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  clients.push(client)
  return render(
    <QueryClientProvider client={client}>{child}</QueryClientProvider>
  )
}
it('rejects an empty team name and creates a zero-balance team through the self-service API', async () => {
  const request = vi
    .spyOn(api, 'request')
    .mockResolvedValue({ data: { success: true, data: { id: 8 } } })
  mount(<NameForm />)
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: 'Create team' }))
  expect(request).not.toHaveBeenCalled()
  await user.type(screen.getByLabelText('Team name'), 'Research')
  await user.click(screen.getByRole('button', { name: 'Create team' }))
  await screen.findByRole('status')
  expect(request).toHaveBeenCalledWith(
    expect.objectContaining({
      url: '/api/billing-units/self-service',
      data: { name: 'Research', request_key: expect.any(String) },
    })
  )
  expect(screen.getByRole('status')).toHaveTextContent('zero balance')
})
it('keeps form input and idempotency key after a failed submission', async () => {
  const request = vi
    .spyOn(api, 'request')
    .mockResolvedValue({ data: { success: false, message: 'unavailable' } })
  mount(<NameForm />)
  const user = userEvent.setup()
  await user.type(screen.getByLabelText('Team name'), 'Research')
  await user.click(screen.getByRole('button', { name: 'Create team' }))
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Create team' })).toBeEnabled()
  )
  expect(screen.queryByRole('status')).not.toBeInTheDocument()
  expect(screen.getByLabelText('Team name')).toHaveValue('Research')
  await user.click(screen.getByRole('button', { name: 'Create team' }))
  await waitFor(() => expect(request).toHaveBeenCalledTimes(2))
  expect(
    (request.mock.calls[0][0].data as { request_key: string }).request_key
  ).toEqual(
    (request.mock.calls[1][0].data as { request_key: string }).request_key
  )
})
it('invites by exact username without changing membership', async () => {
  const request = vi
    .spyOn(api, 'request')
    .mockResolvedValue({ data: { success: true, data: { id: 5 } } })
  mount(<NameForm teamId={8} />)
  const user = userEvent.setup()
  await user.type(screen.getByLabelText('Invite by username'), 'alice')
  await user.click(screen.getByRole('button', { name: 'Send invitation' }))
  expect(await screen.findByRole('status')).toHaveTextContent(
    'after acceptance'
  )
  expect(request).toHaveBeenCalledWith(
    expect.objectContaining({
      url: '/api/billing-units/8/invitations',
      data: { username: 'alice' },
    })
  )
})
it('requires confirmation before replacing the current paying team', async () => {
  const request = vi.spyOn(api, 'request').mockResolvedValue({
    data: {
      success: true,
      data: {
        items: [{ id: 5, team_name: 'Research', status: 'pending' }],
        total: 1,
      },
    },
  })
  mount(<Invitations currentTeamId={3} enabled />)
  const user = userEvent.setup()
  await user.click(await screen.findByRole('button', { name: 'Accept' }))
  expect(screen.getByRole('alertdialog')).toHaveTextContent(
    'replaces your current billing team'
  )
  expect(request.mock.calls.filter((c) => c[0].method === 'POST')).toHaveLength(
    0
  )
  await user.click(screen.getByRole('button', { name: 'Continue' }))
  await waitFor(() =>
    expect(request).toHaveBeenCalledWith(
      expect.objectContaining({
        method: 'POST',
        url: '/api/billing-unit-invitations/5/accept',
        data: { expected_unit_id: 3 },
      })
    )
  )
})
it('disables invitation actions when self-service is unavailable', async () => {
  vi.spyOn(api, 'request').mockResolvedValue({
    data: {
      success: true,
      data: {
        items: [{ id: 5, team_name: 'Research', status: 'pending' }],
        total: 1,
      },
    },
  })
  mount(<Invitations currentTeamId={0} enabled={false} />)
  expect(await screen.findByRole('button', { name: 'Accept' })).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Reject' })).toBeDisabled()
})

it('confirms the receiving team and quoted amount before submitting an Alipay payment', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        enable_online_topup: true,
        pay_methods: [{ type: 'alipay' }],
        min_topup: 1,
      },
    },
  })
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValueOnce({ data: { message: 'success', data: '10.00' } })
    .mockResolvedValueOnce({
      data: {
        message: 'success',
        data: { out_trade_no: 'test-order' },
        url: 'https://payment.example.test/submit',
      },
    })
  let submittedAction = ''
  const submit = vi
    .spyOn(HTMLFormElement.prototype, 'submit')
    .mockImplementation(function (this: HTMLFormElement) {
      submittedAction = this.action
    })
  mount(<TeamTopup team={{ id: 8, name: 'Research', owner_user_id: 2 }} />)
  const user = userEvent.setup()
  const amount = await screen.findByLabelText('Top-up amount')
  await user.clear(amount)
  await user.type(amount, '10')
  await user.click(
    screen.getByRole('button', { name: 'Top up team with Alipay' })
  )
  const dialog = await screen.findByRole('alertdialog')
  expect(dialog).toHaveTextContent('Research')
  expect(dialog).toHaveTextContent('¥10.00')
  expect(submit).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: 'Continue' }))
  await waitFor(() => expect(submit).toHaveBeenCalledOnce())
  expect(post).toHaveBeenLastCalledWith('/api/billing-units/8/topups', {
    amount: 10,
    payment_method: 'alipay',
  })
  expect(submittedAction).toBe('https://payment.example.test/submit')
})
it('does not open payment confirmation or submit a payment when the quote fails', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        enable_online_topup: true,
        pay_methods: [{ type: 'alipay' }],
        min_topup: 1,
      },
    },
  })
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { message: 'unavailable' } })
  const submit = vi
    .spyOn(HTMLFormElement.prototype, 'submit')
    .mockImplementation(() => {})
  mount(<TeamTopup team={{ id: 8, name: 'Research', owner_user_id: 2 }} />)
  const user = userEvent.setup()
  const button = await screen.findByRole('button', {
    name: 'Top up team with Alipay',
  })
  await user.click(button)
  await waitFor(() => expect(post).toHaveBeenCalledOnce())
  await waitFor(() => expect(button).toBeEnabled())
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  expect(submit).not.toHaveBeenCalled()
})
