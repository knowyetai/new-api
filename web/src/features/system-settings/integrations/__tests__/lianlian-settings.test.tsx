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
  buildLianlianSettingsUpdates,
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
  LianlianPlatformPublicCert: '',
  LianlianBusiType: '100001',
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

  it('validates gateway URLs without rejecting the official default', () => {
    expect(isValidLianlianGateway('')).toBe(true)
    expect(isValidLianlianGateway('https://gateway.example/mch/')).toBe(true)
    expect(isValidLianlianGateway('gateway.example/mch')).toBe(false)
    expect(isValidLianlianGateway('https://gateway.example/mch?sign=1')).toBe(
      false
    )
  })

  it('keeps the stored private key when the write-only field is blank', () => {
    expect(buildLianlianSettingsUpdates(initialValues, initialValues)).toEqual(
      []
    )
  })

  it('normalizes changes and allows clearing the public key', () => {
    expect(
      buildLianlianSettingsUpdates(
        {
          ...initialValues,
          LianlianEnabled: true,
          LianlianEnv: 'prod',
          LianlianGateway: 'https://gateway.example/mch/',
          LianlianBusiType: '',
          LianlianPrivateKey: ' private-key ',
          LianlianPlatformPublicCert: ' public-key ',
        },
        {
          ...initialValues,
          LianlianPlatformPublicCert: 'old-public-key',
        }
      )
    ).toEqual([
      { key: 'LianlianEnabled', value: true },
      { key: 'LianlianEnv', value: 'prod' },
      {
        key: 'LianlianGateway',
        value: 'https://gateway.example/mch',
      },
      { key: 'LianlianPlatformPublicCert', value: 'public-key' },
      { key: 'LianlianPrivateKey', value: 'private-key' },
    ])

    expect(
      buildLianlianSettingsUpdates(
        { ...initialValues, LianlianPlatformPublicCert: '' },
        {
          ...initialValues,
          LianlianPlatformPublicCert: 'old-public-key',
        }
      )
    ).toEqual([{ key: 'LianlianPlatformPublicCert', value: '' }])
  })
})
