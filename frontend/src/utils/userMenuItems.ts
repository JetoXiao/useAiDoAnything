export const DEFAULT_USER_MENU_ITEMS = [
  'dashboard',
  'api_keys',
  'help_center',
  'image_generation',
  'usage',
  'channel_status',
  'subscriptions',
  'purchase',
  'orders',
  'redeem',
  'affiliate',
  'support_contact',
  'profile',
] as const

export type DefaultUserMenuItem = typeof DEFAULT_USER_MENU_ITEMS[number]
export const OPTIONAL_USER_MENU_ITEMS = [
  'affiliate_usage',
  'second_level_agency',
] as const

export type UserMenuItem = DefaultUserMenuItem | typeof OPTIONAL_USER_MENU_ITEMS[number]
const USER_MENU_ITEMS = [...DEFAULT_USER_MENU_ITEMS, ...OPTIONAL_USER_MENU_ITEMS] as const

export const USER_MENU_PATHS: Record<UserMenuItem, string> = {
  dashboard: '/dashboard',
  api_keys: '/keys',
  help_center: '/help-center',
  image_generation: '/image-generation',
  usage: '/usage',
  channel_status: '/monitor',
  subscriptions: '/subscriptions',
  purchase: '/purchase',
  orders: '/orders',
  redeem: '/redeem',
  affiliate: '/affiliate',
  affiliate_usage: '/affiliate/usage',
  second_level_agency: '/affiliate/second-level',
  support_contact: '/support-contact',
  profile: '/profile',
}

const USER_MENU_ITEM_SET = new Set<UserMenuItem>(USER_MENU_ITEMS)

export function normalizeUserMenuItems(value: unknown): UserMenuItem[] {
  const parsed = parseUserMenuItemsValue(value)
  if (!Array.isArray(parsed)) {
    return [...DEFAULT_USER_MENU_ITEMS]
  }

  const enabled = new Set<UserMenuItem>()
  for (const item of parsed) {
    const normalized = normalizeUserMenuItemKey(item)
    if (normalized && USER_MENU_ITEM_SET.has(normalized)) {
      enabled.add(normalized)
    }
  }
  return DEFAULT_USER_MENU_ITEMS.filter((item) => enabled.has(item))
}

export function normalizeUserPermissionMenuItems(value: unknown): UserMenuItem[] {
  const parsed = parseUserMenuItemsValue(value)
  if (!Array.isArray(parsed)) {
    return []
  }

  const enabled = new Set<UserMenuItem>()
  for (const item of parsed) {
    const normalized = normalizeUserMenuItemKey(item)
    if (normalized && USER_MENU_ITEM_SET.has(normalized)) {
      enabled.add(normalized)
    }
  }
  return USER_MENU_ITEMS.filter((item) => enabled.has(item))
}

export function isUserMenuItemEnabled(value: unknown, item: UserMenuItem): boolean {
  return normalizeUserMenuItems(value).includes(item)
}

export function isOptionalUserMenuItem(item: string | undefined): item is typeof OPTIONAL_USER_MENU_ITEMS[number] {
  return !!item && (OPTIONAL_USER_MENU_ITEMS as readonly string[]).includes(item)
}

export function isUserMenuPermissionKey(item: string | undefined): item is UserMenuItem {
  return !!item && USER_MENU_ITEM_SET.has(item as UserMenuItem)
}

export function resolveUserMenuFallbackPath(value: unknown): string {
  const enabled = normalizeUserMenuItems(value)
  const preferred = enabled.find((item) => item === 'dashboard')
    ?? enabled.find((item) => item === 'profile')
    ?? enabled[0]
  return preferred ? USER_MENU_PATHS[preferred] : '/home'
}

function parseUserMenuItemsValue(value: unknown): unknown {
  if (typeof value === 'string') {
    try {
      return JSON.parse(value)
    } catch {
      return undefined
    }
  }
  return value
}

function normalizeUserMenuItemKey(value: unknown): UserMenuItem | undefined {
  if (typeof value !== 'string') return undefined
  switch (value.trim()) {
    case 'dashboard':
      return 'dashboard'
    case 'api_keys':
    case 'keys':
      return 'api_keys'
    case 'help_center':
    case 'help-center':
    case 'help':
      return 'help_center'
    case 'image_generation':
    case 'image-generation':
      return 'image_generation'
    case 'usage':
      return 'usage'
    case 'channel_status':
    case 'monitor':
      return 'channel_status'
    case 'subscriptions':
      return 'subscriptions'
    case 'purchase':
      return 'purchase'
    case 'orders':
      return 'orders'
    case 'redeem':
      return 'redeem'
    case 'affiliate':
      return 'affiliate'
    case 'affiliate_usage':
    case 'affiliate-usage':
      return 'affiliate_usage'
    case 'support_contact':
    case 'support':
    case 'after_sales':
    case 'after-sales':
      return 'support_contact'
    case 'profile':
      return 'profile'
    default:
      return undefined
  }
}
