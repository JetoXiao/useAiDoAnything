import { apiClient } from './client'

export interface SecondLevelAgencyCapability { root_partner_user_id: number; enabled: boolean; default_subagent_rate: number; max_subagent_rate: number }
export interface SecondLevelAgent {
  id: number
  root_partner_user_id: number
  subagent_user_id: number
  email: string
  username: string
  aff_code: string
  status: string
  commission_rate: number
  invited_count: number
  cashback_due: number
  cashback_settled: number
  cashback_pending: number
  created_at: string
}
export interface SecondLevelAgencyCandidate { user_id: number; email: string; username: string }

export interface SecondLevelUsageProfitDetail {
  group_id: number
  group_name: string
  model: string
  source?: string
  requests: number
  total_tokens: number
  actual_cost: number
  profit_rate_percent: number
  net_profit: number
  rebate_amount: number
}

export interface SecondLevelUsageRecord {
  date?: string
  inviter_id: number
  inviter_email: string
  inviter_username: string
  invitee_id: number
  invitee_email: string
  invitee_username: string
  invitee_count: number
  requests: number
  total_tokens: number
  actual_cost: number
  account_cost: number
  net_profit: number
  recharge_amount: number
  rebate_rate_percent: number
  rebate_amount: number
  settled_amount: number
  pending_amount: number
  unassigned: boolean
  profit_details?: SecondLevelUsageProfitDetail[]
  members?: SecondLevelUsageRecord[]
}

export interface SecondLevelUsageSummary {
  total_requests: number
  total_tokens: number
  total_actual_cost: number
  total_account_cost: number
  total_net_profit: number
  total_recharge_amount: number
  total_rebate_amount: number
  total_settled_amount: number
  total_pending_amount: number
  total_invited_users: number
}

export interface SecondLevelCashbackSummary {
  total_due: number
  total_settled: number
  total_pending: number
}

export interface SecondLevelUsageResponse {
  items: SecondLevelUsageRecord[]
  summary: SecondLevelUsageSummary
  cashback: SecondLevelCashbackSummary
  total: number
  page: number
  page_size: number
  pages?: number
}

export interface SecondLevelRebateRecord {
  order_id: number
  out_trade_no: string
  inviter_id: number
  inviter_email: string
  inviter_username: string
  invitee_id: number
  invitee_email: string
  invitee_username: string
  order_amount: number
  pay_amount: number
  rebate_amount: number
  payment_type: string
  order_status: string
  created_at: string
}

export interface SecondLevelSettlementRecord {
  id: number
  user_id: number
  user_email: string
  username: string
  amount: number
  settled_on: string
  note: string
  created_by?: number | null
  created_at: string
  updated_at: string
}

export const secondLevelAgencyAPI = {
  getStatus: async () => (await apiClient.get<SecondLevelAgencyCapability>('/agency/second-level/status')).data,
  list: async () => (await apiClient.get<SecondLevelAgent[]>('/agency/second-level/agents')).data,
  candidates: async (q: string) => (await apiClient.get<SecondLevelAgencyCandidate[]>('/agency/second-level/candidates', { params: { q } })).data,
  create: async (payload: { user_id: number; aff_code?: string; commission_rate?: number }) => (await apiClient.post<SecondLevelAgent>('/agency/second-level/agents', payload)).data,
  setStatus: async (id: number, status: string) => (await apiClient.put(`/agency/second-level/agents/${id}/status`, { status })).data,
  setRate: async (id: number, commission_rate: number) => (await apiClient.put(`/agency/second-level/agents/${id}/rate`, { commission_rate })).data,
  usage: async (id: number, params?: Record<string, unknown>) => (await apiClient.get<SecondLevelUsageResponse>(`/agency/second-level/agents/${id}/usage`, { params })).data,
  rebates: async (id: number, params?: Record<string, unknown>) => (await apiClient.get<{ items: SecondLevelRebateRecord[]; total: number; page: number; page_size: number }>(`/agency/second-level/agents/${id}/rebates`, { params })).data,
  createSettlement: async (id: number, payload: { amount: number; settled_on: string; note?: string }) => (await apiClient.post<SecondLevelSettlementRecord>(`/agency/second-level/agents/${id}/settlements`, payload)).data,
}
