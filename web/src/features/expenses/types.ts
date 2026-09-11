export interface Page<T> {
  items: T[]
  total: number
}
export interface Team {
  id: number
  name: string
  owner_user_id: number
}
export interface BillingSelf {
  billing_unit_id: number | null
  name?: string
  enabled?: boolean
  personal_quota: number
  personal_billing_fallback: boolean
  self_service_enabled: boolean
  fallback_enabled: boolean
  topup_enabled: boolean
}
export interface Invitation {
  id: number
  billing_unit_id: number
  team_name: string
  username: string
  display_name: string
  status: string
  expires_at: number
}
export interface Member {
  user_id: number
  username: string
  display_name: string
}
export interface Usage extends Member {
  quota: number
  requests: number
  prompt_tokens: number
  completion_tokens: number
}
export interface TeamDetail {
  unit: Team
  enabled: boolean
  balance_quota: number
  used_quota: number
}
export interface Topup {
  id: number
  amount: number
  money: number
  status: string
  trade_no: string
}
