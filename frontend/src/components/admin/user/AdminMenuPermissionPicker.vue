<template>
  <section class="space-y-3 rounded-lg border border-gray-200 bg-gray-50/70 p-4 dark:border-dark-600 dark:bg-dark-800/50">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <label class="input-label mb-1">{{ t('admin.users.form.adminMenuPermissions') }}</label>
        <p class="text-xs text-gray-500 dark:text-dark-400">
          {{ t(role === 'sub_admin' ? 'admin.users.form.readonlyAdminMenuPermissionsHint' : 'admin.users.form.userMenuPermissionsHint') }}
        </p>
        <p v-if="role === 'user'" class="mt-1 text-xs text-primary-600 dark:text-primary-400">
          {{ t('admin.users.form.secondLevelAgencyPermissionManagedHint') }}
        </p>
      </div>
      <div class="flex gap-2">
        <button type="button" class="btn btn-secondary px-3 py-1 text-xs" @click="selectAll">
          {{ t('common.selectAll') }}
        </button>
        <button type="button" class="btn btn-secondary px-3 py-1 text-xs" @click="clearAll">
          {{ t('common.clear') }}
        </button>
      </div>
    </div>

    <div class="space-y-4">
      <div v-if="role === 'sub_admin'">
        <h4 class="mb-2 text-sm font-semibold text-gray-800 dark:text-dark-100">{{ t('admin.users.form.adminMenus') }}</h4>
        <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
          <label
            v-for="option in adminOptions"
            :key="option.key"
            class="flex cursor-pointer items-center gap-2 rounded-md border border-gray-200 bg-white px-3 py-2 text-sm text-gray-700 transition-colors hover:border-primary-300 hover:bg-primary-50/50 dark:border-dark-600 dark:bg-dark-700 dark:text-dark-200 dark:hover:border-primary-500/50 dark:hover:bg-primary-900/20"
          >
            <input
              type="checkbox"
              class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500"
              :checked="selectedSet.has(option.key)"
              @change="toggle(option.key)"
            />
            <span>{{ option.label }}</span>
          </label>
        </div>
      </div>

      <div v-else>
        <h4 class="mb-2 text-sm font-semibold text-gray-800 dark:text-dark-100">{{ t('admin.users.form.userMenus') }}</h4>
        <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
          <label
            v-for="option in userOptions"
            :key="option.key"
            class="flex cursor-pointer items-center gap-2 rounded-md border border-gray-200 bg-white px-3 py-2 text-sm text-gray-700 transition-colors hover:border-primary-300 hover:bg-primary-50/50 dark:border-dark-600 dark:bg-dark-700 dark:text-dark-200 dark:hover:border-primary-500/50 dark:hover:bg-primary-900/20"
          >
            <input
              type="checkbox"
              class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500"
              :checked="selectedSet.has(option.key)"
              @change="toggle(option.key)"
            />
            <span>{{ option.label }}</span>
          </label>
        </div>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ADMIN_MENU_ITEMS, normalizeAdminMenuPermissions, type AdminPermissionKey } from '@/utils/adminMenuPermissions'
import { DEFAULT_USER_MENU_ITEMS, OPTIONAL_USER_MENU_ITEMS, type UserMenuItem } from '@/utils/userMenuItems'

const props = defineProps<{
  modelValue: string[]
  role: 'sub_admin' | 'user'
}>()
const emit = defineEmits<{ 'update:modelValue': [value: string[]] }>()
const { t } = useI18n()

const adminLabelKeys: Record<string, string> = {
  admin_dashboard: 'nav.dashboard',
  admin_visitor_analytics: 'nav.visitorAnalytics',
  admin_download_resources: 'nav.downloadResources',
  admin_ops: 'nav.ops',
  admin_ttft_analysis: 'nav.ttftAnalysis',
  admin_response_cache: 'nav.responseCache',
  admin_requests: 'nav.requests',
  admin_users: 'nav.users',
  admin_groups: 'nav.groups',
  admin_channel_pricing: 'nav.channelPricing',
  admin_channel_monitor: 'nav.channelMonitor',
  admin_subscriptions: 'nav.subscriptions',
  admin_accounts: 'nav.accounts',
  admin_announcements: 'nav.announcements',
  admin_proxies: 'nav.proxies',
  admin_risk_control: 'nav.riskControl',
  admin_redeem: 'nav.redeemCodes',
  admin_promo_codes: 'nav.promoCodes',
  admin_affiliate_usage: 'nav.affiliateUsage',
  admin_affiliate_applications: 'nav.affiliateApplications',
  admin_affiliate_invites: 'nav.affiliateInviteRecords',
  admin_affiliate_rebates: 'nav.affiliateRebateRecords',
  admin_affiliate_transfers: 'nav.affiliateTransferRecords',
  admin_order_dashboard: 'nav.paymentDashboard',
  admin_orders: 'nav.orderManagement',
  admin_order_plans: 'nav.paymentPlans',
  admin_usage: 'nav.usage',
  admin_settings: 'nav.settings',
}

const userLabelKeys: Record<UserMenuItem, string> = {
  dashboard: 'nav.dashboard',
  api_keys: 'nav.apiKeys',
  help_center: 'nav.helpCenter',
  image_generation: 'nav.imageGeneration',
  usage: 'nav.usage',
  channel_status: 'nav.channelStatus',
  subscriptions: 'nav.mySubscriptions',
  purchase: 'nav.buySubscription',
  orders: 'nav.myOrders',
  redeem: 'nav.redeem',
  affiliate: 'nav.affiliate',
  affiliate_usage: 'nav.affiliateUsage',
  second_level_agency: 'nav.secondLevelAgency',
  support_contact: 'nav.supportContact',
  profile: 'nav.profile',
}

const adminOptions = computed(() => ADMIN_MENU_ITEMS.map((key) => ({ key, label: t(adminLabelKeys[key]) })))
// 二级代理页面权限由“二级代理权限”页面自动托管，不能在用户管理中手动修改。
const userPermissionItems = computed(() => [...DEFAULT_USER_MENU_ITEMS, ...OPTIONAL_USER_MENU_ITEMS]
  .filter((key) => key !== 'second_level_agency') as UserMenuItem[])
const userOptions = computed(() => userPermissionItems.value.map((key) => ({ key, label: t(userLabelKeys[key]) })))
const allKeys = computed<AdminPermissionKey[]>(() => props.role === 'sub_admin'
  ? [...ADMIN_MENU_ITEMS]
  : [...userPermissionItems.value])
const selectedSet = computed(() => {
  const allowed = new Set<string>(allKeys.value)
  return new Set(normalizeAdminMenuPermissions(props.modelValue).filter((key) => allowed.has(key)))
})

function toggle(key: AdminPermissionKey) {
  const next = new Set(selectedSet.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  emit('update:modelValue', Array.from(next))
}

function selectAll() {
  emit('update:modelValue', [...allKeys.value])
}

function clearAll() {
  emit('update:modelValue', [])
}
</script>
