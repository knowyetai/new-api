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
import type { UpdateOptionRequest } from '../types'
import { removeTrailingSlash } from './utils'

export const DEFAULT_LIANLIAN_BUSI_TYPE = '100001'

export type LianlianSettingsValues = {
  LianlianEnabled: boolean
  LianlianEnv: string
  LianlianGateway: string
  LianlianMchId: string
  LianlianPrivateKey: string
  LianlianPlatformPublicCert: string
  LianlianBusiType: string
}

export function isValidLianlianGateway(value: string): boolean {
  const trimmed = value.trim()
  if (!trimmed) return true

  try {
    const url = new URL(trimmed)
    const isHttp = url.protocol === 'http:' || url.protocol === 'https:'
    return (
      isHttp &&
      !url.username &&
      !url.password &&
      !url.search &&
      !url.hash &&
      url.pathname.replace(/\/+$/, '').endsWith('/mch')
    )
  } catch {
    return false
  }
}

export function buildLianlianSettingsUpdates(
  values: LianlianSettingsValues,
  initialValues: LianlianSettingsValues
): UpdateOptionRequest[] {
  const normalized = {
    LianlianEnabled: values.LianlianEnabled,
    LianlianEnv: values.LianlianEnv === 'prod' ? 'prod' : 'test',
    LianlianGateway: removeTrailingSlash(values.LianlianGateway),
    LianlianMchId: values.LianlianMchId.trim(),
    LianlianPrivateKey: values.LianlianPrivateKey.trim(),
    LianlianPlatformPublicCert: values.LianlianPlatformPublicCert.trim(),
    LianlianBusiType:
      values.LianlianBusiType.trim() || DEFAULT_LIANLIAN_BUSI_TYPE,
  }
  const initial = {
    LianlianEnabled: initialValues.LianlianEnabled,
    LianlianEnv: initialValues.LianlianEnv === 'prod' ? 'prod' : 'test',
    LianlianGateway: removeTrailingSlash(initialValues.LianlianGateway),
    LianlianMchId: initialValues.LianlianMchId.trim(),
    LianlianPlatformPublicCert: initialValues.LianlianPlatformPublicCert.trim(),
    LianlianBusiType:
      initialValues.LianlianBusiType.trim() || DEFAULT_LIANLIAN_BUSI_TYPE,
  }

  const updates: UpdateOptionRequest[] = []
  for (const key of [
    'LianlianEnabled',
    'LianlianEnv',
    'LianlianGateway',
    'LianlianMchId',
    'LianlianPlatformPublicCert',
    'LianlianBusiType',
  ] as const) {
    if (normalized[key] !== initial[key]) {
      updates.push({ key, value: normalized[key] })
    }
  }

  // Secrets are write-only because GET /api/option/ strips key-suffixed
  // options. An empty field therefore means "keep the stored private key".
  if (normalized.LianlianPrivateKey) {
    updates.push({
      key: 'LianlianPrivateKey',
      value: normalized.LianlianPrivateKey,
    })
  }
  return updates
}
