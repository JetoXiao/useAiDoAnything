import { apiClient } from './client'

export interface SecondLevelAgencyCapability { root_partner_user_id: number; enabled: boolean; default_subagent_rate: number; max_subagent_rate: number }
export interface SecondLevelAgent { id: number; root_partner_user_id: number; subagent_user_id: number; email: string; username: string; aff_code: string; status: string; commission_rate: number; invited_count: number; created_at: string }
export interface SecondLevelAgencyCandidate { user_id: number; email: string; username: string }

export const secondLevelAgencyAPI = {
  getStatus: async () => (await apiClient.get<SecondLevelAgencyCapability>('/agency/second-level/status')).data,
  list: async () => (await apiClient.get<SecondLevelAgent[]>('/agency/second-level/agents')).data,
  candidates: async (q: string) => (await apiClient.get<SecondLevelAgencyCandidate[]>('/agency/second-level/candidates', { params: { q } })).data,
  create: async (payload: { user_id: number; aff_code?: string; commission_rate?: number }) => (await apiClient.post<SecondLevelAgent>('/agency/second-level/agents', payload)).data,
  setStatus: async (id: number, status: string) => (await apiClient.put(`/agency/second-level/agents/${id}/status`, { status })).data,
  setRate: async (id: number, commission_rate: number) => (await apiClient.put(`/agency/second-level/agents/${id}/rate`, { commission_rate })).data,
  usage: async (id: number, params?: Record<string, unknown>) => (await apiClient.get(`/agency/second-level/agents/${id}/usage`, { params })).data,
  rebates: async (id: number, params?: Record<string, unknown>) => (await apiClient.get(`/agency/second-level/agents/${id}/rebates`, { params })).data,
}
