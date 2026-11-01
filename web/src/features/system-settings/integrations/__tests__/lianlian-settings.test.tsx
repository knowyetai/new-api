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
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { describe, expect, it } from 'vitest'

import { Form } from '@/components/ui/form'

import {
  buildLianlianSettingsRequest,
  isValidLianlianGateway,
  type LianlianSettingsValues,
} from '../lianlian-settings'
import { LianlianSettingsSection } from '../lianlian-settings-section'

const initialValues: LianlianSettingsValues = {
  LianlianEnabled: false,
  LianlianTeamTopUpEnabled: false,
  LianlianEnv: 'test',
  LianlianGateway: '',
  LianlianMchId: 'merchant',
  LianlianPrivateKey: '',
  LianlianPrivateKeyConfigured: true,
  LianlianClearPrivateKey: false,
  LianlianPlatformPublicCert: '',
  LianlianBusiType: '100001',
  LianlianRiskCategory: '1002',
}

function SettingsFixture() {
  const [values, setValues] = useState<LianlianSettingsValues>(initialValues)
  const form = useForm<LianlianSettingsValues>({
    defaultValues: initialValues,
  })

  return (
    <Form {...form}>
      <LianlianSettingsSection
        values={values}
        notifyURL='https://pay.example.test/api/user/lianlian/notify'
        onValueChange={(key, value) =>
          setValues((current) => ({ ...current, [key]: value }))
        }
      />
    </Form>
  )
}

describe('Lianlian settings', () => {
  it('lets administrators configure member access independently of the provider switch', async () => {
    render(<SettingsFixture />)
    const user = userEvent.setup()
    const toggle = screen.getByRole('switch', {
      name: 'Allow ordinary team members to top up with LianLian',
    })
    expect(toggle).toHaveAccessibleDescription(
      'When disabled, only team owners and platform administrators can create team top-ups. Personal top-ups and existing orders are unaffected.'
    )
    expect(toggle).not.toBeChecked()
    toggle.focus()
    await user.keyboard(' ')
    expect(toggle).toBeChecked()
    expect(
      screen.getByRole('switch', { name: 'Enable LianLian' })
    ).not.toBeChecked()
    expect(
      buildLianlianSettingsRequest(
        { ...initialValues, LianlianTeamTopUpEnabled: true },
        initialValues
      )
    ).toMatchObject({ enabled: false, team_topup_enabled: true })
    await user.click(toggle)
    expect(toggle).not.toBeChecked()
  })

  it('updates the merchant ID and private key from accessible fields', async () => {
    render(<SettingsFixture />)
    const user = userEvent.setup()

    const merchantID = screen.getByLabelText('Merchant ID')
    await user.clear(merchantID)
    await user.type(merchantID, 'new-merchant')
    expect(merchantID).toHaveValue('new-merchant')

    const privateKey = screen.getByLabelText('Merchant RSA private key')
    await user.type(privateKey, 'private-key')
    expect(privateKey).toHaveValue('private-key')
  })

  it('shows the stored-key state and lets the operator clear it', async () => {
    render(<SettingsFixture />)
    const user = userEvent.setup()

    expect(
      screen.getByText('A merchant private key is already stored.')
    ).toBeInTheDocument()
    const clearKey = screen.getByRole('checkbox', {
      name: 'Clear the stored merchant private key',
    })
    await user.click(clearKey)
    expect(clearKey).toBeChecked()
  })

  it('validates gateway URLs without rejecting the official default', () => {
    expect(isValidLianlianGateway('')).toBe(true)
    expect(isValidLianlianGateway('https://gateway.example/mch/')).toBe(true)
    expect(isValidLianlianGateway('gateway.example/mch')).toBe(false)
    expect(isValidLianlianGateway('https://gateway.example/mch?sign=1')).toBe(
      false
    )
  })

  it('shows the effective notify URL supplied by the backend', () => {
    render(<SettingsFixture />)

    expect(
      screen.getByText('https://pay.example.test/api/user/lianlian/notify')
    ).toBeInTheDocument()
  })

  it('keeps the stored private key when the write-only field is blank', () => {
    expect(
      buildLianlianSettingsRequest(initialValues, initialValues)
    ).toBeNull()
  })

  it('normalizes changes and allows clearing the public key', () => {
    expect(
      buildLianlianSettingsRequest(
        {
          ...initialValues,
          LianlianEnabled: true,
          LianlianTeamTopUpEnabled: true,
          LianlianEnv: 'prod',
          LianlianGateway: 'https://gateway.example/mch/',
          LianlianBusiType: '',
          LianlianRiskCategory: '2001',
          LianlianPrivateKey: ' private-key ',
          LianlianPlatformPublicCert: ' public-key ',
        },
        {
          ...initialValues,
          LianlianPlatformPublicCert: 'old-public-key',
        }
      )
    ).toEqual({
      enabled: true,
      team_topup_enabled: true,
      env: 'prod',
      gateway: 'https://gateway.example/mch',
      merchant_id: 'merchant',
      private_key: 'private-key',
      platform_public_key: 'public-key',
      busi_type: '100001',
      risk_category: '2001',
      clear_private_key: false,
    })

    expect(
      buildLianlianSettingsRequest(
        { ...initialValues, LianlianPlatformPublicCert: '' },
        {
          ...initialValues,
          LianlianPlatformPublicCert: 'old-public-key',
        }
      )
    ).toEqual({
      enabled: false,
      team_topup_enabled: false,
      env: 'test',
      gateway: '',
      merchant_id: 'merchant',
      private_key: '',
      platform_public_key: '',
      busi_type: '100001',
      risk_category: '1002',
      clear_private_key: false,
    })
  })

  it('can explicitly clear a stored private key', () => {
    expect(
      buildLianlianSettingsRequest(
        {
          ...initialValues,
          LianlianClearPrivateKey: true,
        },
        initialValues
      )
    ).toMatchObject({
      private_key: '',
      clear_private_key: true,
    })
  })
})
