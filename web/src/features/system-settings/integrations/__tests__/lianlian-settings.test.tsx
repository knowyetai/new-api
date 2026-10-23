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
        onValueChange={(key, value) =>
          setValues((current) => ({ ...current, [key]: value }))
        }
      />
    </Form>
  )
}

describe('Lianlian settings', () => {
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

  it('keeps the stored private key when the write-only field is blank', () => {
    expect(buildLianlianSettingsRequest(initialValues, initialValues)).toBeNull()
  })

  it('normalizes changes and allows clearing the public key', () => {
    expect(
      buildLianlianSettingsRequest(
        {
          ...initialValues,
          LianlianEnabled: true,
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
