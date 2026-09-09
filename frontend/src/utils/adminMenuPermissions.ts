import type { UserMenuItem } from './userMenuItems'

export const ADMIN_MENU_ITEMS = [
  'admin_dashboard',
  'admin_visitor_analytics',
  'admin_download_resources',
  'admin_ops',
  'admin_ttft_analysis',
  'admin_response_cache',
  'admin_requests',
  'admin_users',
  'admin_groups',
  'admin_channel_pricing',
  'admin_channel_monitor',
  'admin_subscriptions',
  'admin_accounts',
  'admin_announcements',
  'admin_proxies',
  'admin_risk_control',
  'admin_redeem',
  'admin_promo_codes',
  'admin_affiliate_usage',
  'admin_affiliate_applications',
  'admin_affiliate_agencies',
  'admin_affiliate_invites',
  'admin_affiliate_rebates',
  'admin_affiliate_transfers',
  'admin_order_dashboard',
  'admin_orders',
  'admin_order_plans',
  'admin_usage',
  'admin_settings',
] as const

export type AdminMenuItem = typeof ADMIN_MENU_ITEMS[number]
export type AdminPermissionKey = AdminMenuItem | UserMenuItem | `custom:${string}`

export const ADMIN_MENU_PATHS: Record<AdminMenuItem, string> = {
  admin_dashboard: '/admin/dashboard',
  admin_visitor_analytics: '/admin/visitor-analytics',
  admin_download_resources: '/admin/download-resources',
  admin_ops: '/admin/ops',
  admin_ttft_analysis: '/admin/ops/ttft',
  admin_response_cache: '/admin/ops/response-cache',
  admin_requests: '/admin/requests',
  admin_users: '/admin/users',
  admin_groups: '/admin/groups',
  admin_channel_pricing: '/admin/channels/pricing',
  admin_channel_monitor: '/admin/channels/monitor',
  admin_subscriptions: '/admin/subscriptions',
  admin_accounts: '/admin/accounts',
  admin_announcements: '/admin/announcements',
  admin_proxies: '/admin/proxies',
  admin_risk_control: '/admin/risk-control',
  admin_redeem: '/admin/redeem',
  admin_promo_codes: '/admin/promo-codes',
  admin_affiliate_usage: '/admin/affiliates/usage',
  admin_affiliate_applications: '/admin/affiliates/applications',
  admin_affiliate_agencies: '/admin/affiliates/agencies',
  admin_affiliate_invites: '/admin/affiliates/invites',
  admin_affiliate_rebates: '/admin/affiliates/rebates',
  admin_affiliate_transfers: '/admin/affiliates/transfers',
  admin_order_dashboard: '/admin/orders/dashboard',
  admin_orders: '/admin/orders',
  admin_order_plans: '/admin/orders/plans',
  admin_usage: '/admin/usage',
  admin_settings: '/admin/settings',
}

export function normalizeAdminMenuPermissions(value: unknown): string[] {
  const parsed = parsePermissionValue(value)
  if (!Array.isArray(parsed)) {
    return []
  }
  const out: string[] = []
  const seen = new Set<string>()
  for (const item of parsed) {
    if (typeof item !== 'string') continue
    const key = item.trim()
    if (!key || seen.has(key)) continue
    seen.add(key)
    out.push(key)
  }
  return out
}

export function hasAdminMenuPermission(value: unknown, key?: string): boolean {
  if (!key) return false
  return normalizeAdminMenuPermissions(value).includes(key)
}

export function resolveReadonlyAdminRouteRedirect(options: {
  isReadonlyAdmin: boolean
  requiresAdmin: boolean
  adminMenuKey?: string
  permissions: unknown
}): string | undefined {
  if (!options.isReadonlyAdmin || !options.requiresAdmin || !options.adminMenuKey) {
    return undefined
  }
  if (hasAdminMenuPermission(options.permissions, options.adminMenuKey)) {
    return undefined
  }
  return resolveReadonlyAdminFallbackPath(options.permissions)
}

export function resolveReadonlyAdminFallbackPath(value: unknown): string {
  const permissions = normalizeAdminMenuPermissions(value)
  for (const item of ADMIN_MENU_ITEMS) {
    if (permissions.includes(item)) return ADMIN_MENU_PATHS[item]
  }
  return '/dashboard'
}

function parsePermissionValue(value: unknown): unknown {
  if (typeof value === 'string') {
    try {
      return JSON.parse(value)
    } catch {
      return undefined
    }
  }
  return value
}
