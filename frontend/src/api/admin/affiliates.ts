/**
 * Admin Affiliate API endpoints
 * Manage per-user affiliate (邀请返利) configurations:
 * exclusive invite codes (overrides aff_code) and exclusive rebate rates.
 */

import { apiClient } from '../client'
import type {
  AffiliatePartnerApplication,
  AffiliatePartnerApplicationStatus,
  AffiliatePartnerLevel,
  AffiliatePartnerTier,
  PaginatedResponse,
} from '@/types'

export interface AffiliateAdminEntry {
  user_id: number
  email: string
  username: string
  aff_code: string
  aff_code_custom: boolean
  aff_rebate_rate_percent?: number | null
  partner_level: AffiliatePartnerLevel
  partner_tier?: AffiliatePartnerTier | null
  aff_count: number
}

export interface SecondLevelAgencyCapability {
  root_partner_user_id: number
  enabled: boolean
  default_subagent_rate: number
  max_subagent_rate: number
  granted_by?: number | null
  granted_at?: string | null
  revoked_at?: string | null
}

export interface ListAffiliateUsersParams {
  page?: number
  page_size?: number
  search?: string
}

export interface ListAffiliateRecordsParams {
  page?: number
  page_size?: number
  search?: string
  start_at?: string
  end_at?: string
  user_id?: number
  sort_by?: string
  sort_order?: 'asc' | 'desc'
  timezone?: string
}

export interface ListAffiliateUsageParams extends ListAffiliateRecordsParams {
  inviter_id?: number
  invitee_id?: number
  view?: 'users' | 'groups'
}

export interface AffiliateInviteRecord {
  inviter_id: number
  inviter_email: string
  inviter_username: string
  invitee_id: number
  invitee_email: string
  invitee_username: string
  aff_code: string
  total_rebate: number
  created_at: string
}

export interface AffiliateUsageDailyRecord {
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
  profit_details?: AffiliateUsageProfitDetail[]
  members?: AffiliateUsageDailyRecord[]
}

export interface AffiliateUsageProfitDetail {
  group_id: number
  group_name: string
  model: string
  source?: 'usage' | 'subscription' | string
  requests: number
  total_tokens: number
  actual_cost: number
  profit_rate_percent: number
  net_profit: number
  rebate_amount: number
}

export interface AffiliateUsageSummary {
  total_requests: number
  total_tokens: number
  total_actual_cost: number
  total_account_cost: number
  total_net_profit: number
  total_recharge_amount?: number
  total_rebate_amount: number
  total_settled_amount?: number
  total_pending_amount?: number
}

export interface AffiliateUsageResponse extends PaginatedResponse<AffiliateUsageDailyRecord> {
  summary: AffiliateUsageSummary
}

export interface AffiliateRebateRecord {
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

export interface AffiliateTransferRecord {
  ledger_id: number
  user_id: number
  user_email: string
  username: string
  amount: number
  balance_after?: number | null
  available_quota_after?: number | null
  frozen_quota_after?: number | null
  history_quota_after?: number | null
  snapshot_available: boolean
  created_at: string
}

export interface AffiliateSettlementRecord {
  id: number
  user_id: number
  user_email: string
  username: string
  amount: number
  settled_on: string
  note: string
  created_by?: number | null
  created_by_email?: string
  created_by_username?: string
  created_at: string
  updated_at: string
}

export interface CreateAffiliateSettlementRequest {
  user_id: number
  amount: number
  settled_on: string
  note?: string
}

export interface AffiliateUserOverview {
  user_id: number
  email: string
  username: string
  aff_code: string
  partner_level: AffiliatePartnerLevel
  partner_tier?: AffiliatePartnerTier | null
  rebate_rate_percent: number
  invited_count: number
  rebated_invitee_count: number
  available_quota: number
  history_quota: number
}

export interface UpdateAffiliateUserRequest {
  aff_code?: string
  aff_rebate_rate_percent?: number | null
  partner_level?: AffiliatePartnerLevel
  /** Set true to explicitly clear the per-user rate (sets it to NULL). */
  clear_rebate_rate?: boolean
}

export interface ListPartnerApplicationsParams {
  page?: number
  page_size?: number
  search?: string
  status?: AffiliatePartnerApplicationStatus | 'all' | ''
}

export interface ReviewPartnerApplicationRequest {
  status: Exclude<AffiliatePartnerApplicationStatus, 'pending'>
  granted_level?: Exclude<AffiliatePartnerLevel, 'none'>
  review_note?: string
}

export interface BatchSetRateRequest {
  user_ids: number[]
  aff_rebate_rate_percent?: number | null
  /** Set true to clear rates instead of setting. */
  clear?: boolean
}

export interface AssignInviterRequest {
  inviter_id: number
  invitee_id: number
}

export interface AssignInviterResponse {
  inviter_id: number
  invitee_id: number
  changed: boolean
}

export interface SimpleUser {
  id: number
  email: string
  username: string
}

export async function listUsers(
  params: ListAffiliateUsersParams = {},
): Promise<PaginatedResponse<AffiliateAdminEntry>> {
  const { data } = await apiClient.get<PaginatedResponse<AffiliateAdminEntry>>(
    '/admin/affiliates/users',
    {
      params: {
        page: params.page ?? 1,
        page_size: params.page_size ?? 20,
        search: params.search ?? '',
      },
    },
  )
  return data
}

export async function lookupUsers(q: string): Promise<SimpleUser[]> {
  const { data } = await apiClient.get<SimpleUser[]>(
    '/admin/affiliates/users/lookup',
    { params: { q } },
  )
  return data
}

export async function listSecondLevelAgencyCapabilities(): Promise<SecondLevelAgencyCapability[]> {
  const { data } = await apiClient.get<SecondLevelAgencyCapability[]>('/admin/affiliates/agencies')
  return data
}

export async function updateSecondLevelAgencyCapability(userId: number, payload: { enabled: boolean; default_subagent_rate: number; max_subagent_rate: number }): Promise<SecondLevelAgencyCapability> {
  const { data } = await apiClient.put<SecondLevelAgencyCapability>(`/admin/affiliates/agencies/${userId}/capability`, payload)
  return data
}

export async function updateUserSettings(
  userId: number,
  payload: UpdateAffiliateUserRequest,
): Promise<{ user_id: number }> {
  const { data } = await apiClient.put<{ user_id: number }>(
    `/admin/affiliates/users/${userId}`,
    payload,
  )
  return data
}

export async function clearUserSettings(
  userId: number,
): Promise<{ user_id: number }> {
  const { data } = await apiClient.delete<{ user_id: number }>(
    `/admin/affiliates/users/${userId}`,
  )
  return data
}

export async function batchSetRate(
  payload: BatchSetRateRequest,
): Promise<{ affected: number }> {
  const { data } = await apiClient.post<{ affected: number }>(
    '/admin/affiliates/users/batch-rate',
    payload,
  )
  return data
}

export async function listPartnerTiers(): Promise<AffiliatePartnerTier[]> {
  const { data } = await apiClient.get<AffiliatePartnerTier[]>(
    '/admin/affiliates/partner-tiers',
  )
  return data
}

export async function listPartnerApplications(
  params: ListPartnerApplicationsParams = {},
): Promise<PaginatedResponse<AffiliatePartnerApplication>> {
  const { data } = await apiClient.get<PaginatedResponse<AffiliatePartnerApplication>>(
    '/admin/affiliates/partner-applications',
    {
      params: {
        page: params.page ?? 1,
        page_size: params.page_size ?? 20,
        search: params.search ?? '',
        status: params.status || undefined,
      },
    },
  )
  return data
}

export async function reviewPartnerApplication(
  applicationId: number,
  payload: ReviewPartnerApplicationRequest,
): Promise<AffiliatePartnerApplication> {
  const { data } = await apiClient.put<AffiliatePartnerApplication>(
    `/admin/affiliates/partner-applications/${applicationId}/review`,
    payload,
  )
  return data
}

function recordParams(params: ListAffiliateRecordsParams = {}) {
  return {
    page: params.page ?? 1,
    page_size: params.page_size ?? 20,
    search: params.search ?? '',
    start_at: params.start_at || undefined,
    end_at: params.end_at || undefined,
    user_id: params.user_id || undefined,
    sort_by: params.sort_by || undefined,
    sort_order: params.sort_order || undefined,
    timezone: params.timezone || undefined,
  }
}

export async function listInviteRecords(
  params: ListAffiliateRecordsParams = {},
): Promise<PaginatedResponse<AffiliateInviteRecord>> {
  const { data } = await apiClient.get<PaginatedResponse<AffiliateInviteRecord>>(
    '/admin/affiliates/invites',
    { params: recordParams(params) },
  )
  return data
}

export async function assignInviter(
  payload: AssignInviterRequest,
): Promise<AssignInviterResponse> {
  const { data } = await apiClient.post<AssignInviterResponse>(
    '/admin/affiliates/invites/assign',
    payload,
  )
  return data
}

export async function listUsageDailyRecords(
  params: ListAffiliateUsageParams = {},
): Promise<AffiliateUsageResponse> {
  const { data } = await apiClient.get<AffiliateUsageResponse>(
    '/admin/affiliates/usage',
    {
      params: {
        ...recordParams(params),
        inviter_id: params.inviter_id || undefined,
        invitee_id: params.invitee_id || undefined,
        view: params.view || undefined,
      },
    },
  )
  return data
}

export async function listRebateRecords(
  params: ListAffiliateRecordsParams = {},
): Promise<PaginatedResponse<AffiliateRebateRecord>> {
  const { data } = await apiClient.get<PaginatedResponse<AffiliateRebateRecord>>(
    '/admin/affiliates/rebates',
    { params: recordParams(params) },
  )
  return data
}

export async function listTransferRecords(
  params: ListAffiliateRecordsParams = {},
): Promise<PaginatedResponse<AffiliateTransferRecord>> {
  const { data } = await apiClient.get<PaginatedResponse<AffiliateTransferRecord>>(
    '/admin/affiliates/transfers',
    { params: recordParams(params) },
  )
  return data
}

export async function listSettlementRecords(
  params: ListAffiliateRecordsParams = {},
): Promise<PaginatedResponse<AffiliateSettlementRecord>> {
  const { data } = await apiClient.get<PaginatedResponse<AffiliateSettlementRecord>>(
    '/admin/affiliates/settlements',
    { params: recordParams(params) },
  )
  return data
}

export async function createSettlement(
  payload: CreateAffiliateSettlementRequest,
): Promise<AffiliateSettlementRecord> {
  const { data } = await apiClient.post<AffiliateSettlementRecord>(
    '/admin/affiliates/settlements',
    payload,
  )
  return data
}

export async function getUserOverview(
  userId: number,
): Promise<AffiliateUserOverview> {
  const { data } = await apiClient.get<AffiliateUserOverview>(
    `/admin/affiliates/users/${userId}/overview`,
  )
  return data
}

export const affiliatesAPI = {
  listUsers,
  lookupUsers,
  updateUserSettings,
  clearUserSettings,
  batchSetRate,
  listPartnerTiers,
  listPartnerApplications,
  reviewPartnerApplication,
  assignInviter,
  listInviteRecords,
  listUsageDailyRecords,
  listRebateRecords,
  listTransferRecords,
  listSettlementRecords,
  createSettlement,
  getUserOverview,
}

export default affiliatesAPI
