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
import { removeTrailingSlash } from './utils'

export const DEFAULT_LIANLIAN_BUSI_TYPE = '100001'
export const DEFAULT_LIANLIAN_RISK_CATEGORY = '1002'

export type LianlianSettingsValues = {
  LianlianEnabled: boolean
  LianlianEnv: string
  LianlianGateway: string
  LianlianMchId: string
  LianlianPrivateKey: string
  LianlianPrivateKeyConfigured: boolean
  LianlianClearPrivateKey: boolean
  LianlianPlatformPublicCert: string
  LianlianBusiType: string
  LianlianRiskCategory: string
}

export type SaveLianlianSettingsRequest = {
  enabled: boolean
  env: string
  gateway: string
  merchant_id: string
  private_key: string
  platform_public_key: string
  busi_type: string
  risk_category: string
  clear_private_key: boolean
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

export function buildLianlianSettingsRequest(
  values: LianlianSettingsValues,
  initialValues: LianlianSettingsValues
): SaveLianlianSettingsRequest | null {
  const normalized = {
    enabled: values.LianlianEnabled,
    env: values.LianlianEnv === 'prod' ? 'prod' : 'test',
    gateway: removeTrailingSlash(values.LianlianGateway),
    merchantID: values.LianlianMchId.trim(),
    privateKey: values.LianlianClearPrivateKey
      ? ''
      : values.LianlianPrivateKey.trim(),
    clearPrivateKey: values.LianlianClearPrivateKey,
    platformPublicKey: values.LianlianPlatformPublicCert.trim(),
    busiType:
      values.LianlianBusiType.trim() || DEFAULT_LIANLIAN_BUSI_TYPE,
    riskCategory:
      values.LianlianRiskCategory.trim() || DEFAULT_LIANLIAN_RISK_CATEGORY,
  }
  const initial = {
    enabled: initialValues.LianlianEnabled,
    env: initialValues.LianlianEnv === 'prod' ? 'prod' : 'test',
    gateway: removeTrailingSlash(initialValues.LianlianGateway),
    merchantID: initialValues.LianlianMchId.trim(),
    platformPublicKey: initialValues.LianlianPlatformPublicCert.trim(),
    busiType:
      initialValues.LianlianBusiType.trim() || DEFAULT_LIANLIAN_BUSI_TYPE,
    riskCategory:
      initialValues.LianlianRiskCategory.trim() ||
      DEFAULT_LIANLIAN_RISK_CATEGORY,
  }

  const changed =
    normalized.enabled !== initial.enabled ||
    normalized.env !== initial.env ||
    normalized.gateway !== initial.gateway ||
    normalized.merchantID !== initial.merchantID ||
    normalized.platformPublicKey !== initial.platformPublicKey ||
    normalized.busiType !== initial.busiType ||
    normalized.riskCategory !== initial.riskCategory ||
    normalized.privateKey !== '' ||
    normalized.clearPrivateKey
  if (!changed) {
    return null
  }

  return {
    enabled: normalized.enabled,
    env: normalized.env,
    gateway: normalized.gateway,
    merchant_id: normalized.merchantID,
    private_key: normalized.privateKey,
    platform_public_key: normalized.platformPublicKey,
    busi_type: normalized.busiType,
    risk_category: normalized.riskCategory,
    clear_private_key: normalized.clearPrivateKey,
  }
}
