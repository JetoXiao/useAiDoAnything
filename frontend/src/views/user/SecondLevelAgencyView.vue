<template>
  <AppLayout>
    <div class="mx-auto max-w-7xl space-y-6">
      <div>
        <h1 class="text-2xl font-semibold text-gray-900 dark:text-white">二级代理管理</h1>
        <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">
          仅管理员授权的一级合伙人可使用。平台只与一级代理结算，二级代理由一级代理自行结算。
        </p>
      </div>

      <div v-if="loading" class="rounded-xl border border-gray-200 bg-white p-6 text-gray-500 dark:border-dark-700 dark:bg-dark-800 dark:text-dark-400">
        加载中...
      </div>
      <div
        v-else-if="!capability?.enabled"
        class="rounded-xl border border-amber-200 bg-amber-50 p-6 text-amber-800 dark:border-amber-900/60 dark:bg-amber-950/30 dark:text-amber-200"
      >
        当前账号尚未获得二级代理权限，请联系管理员开通。
      </div>

      <template v-else>
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div class="text-sm text-gray-500 dark:text-dark-400">
            已管理 {{ agents.length }} 个二级代理
          </div>
          <button class="btn btn-primary" type="button" @click="showCreate = true">
            新增二级代理
          </button>
        </div>

        <div class="grid gap-3 sm:grid-cols-3">
          <div class="summary-tile">
            <p>全部二级代理应返</p>
            <strong class="text-primary-600 dark:text-primary-400">{{ formatUsdAmount(cashbackTotals.due) }}</strong>
          </div>
          <div class="summary-tile">
            <p>全部二级代理已返</p>
            <strong class="text-emerald-600 dark:text-emerald-400">{{ formatUsdAmount(cashbackTotals.settled) }}</strong>
          </div>
          <div class="summary-tile">
            <p>全部二级代理待返</p>
            <strong class="text-amber-600 dark:text-amber-400">{{ formatUsdAmount(cashbackTotals.pending) }}</strong>
          </div>
        </div>

        <div class="overflow-hidden rounded-xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800">
          <div class="overflow-x-auto">
            <table class="min-w-full text-left text-sm">
              <thead class="bg-gray-50 dark:bg-dark-900">
                <tr>
                  <th class="px-4 py-3">代理</th>
                  <th class="px-4 py-3">邀请码</th>
                  <th class="px-4 py-3">分佣比例</th>
                  <th class="px-4 py-3">邀请人数</th>
                  <th class="px-4 py-3">应返 / 已返 / 待返</th>
                  <th class="px-4 py-3">状态</th>
                  <th class="px-4 py-3">操作</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
                <tr v-for="agent in agents" :key="agent.id" class="hover:bg-gray-50 dark:hover:bg-dark-900/50">
                  <td class="px-4 py-3">
                    <div class="font-medium text-gray-900 dark:text-white">
                      {{ agent.username || agent.email || `用户 #${agent.subagent_user_id}` }}
                    </div>
                    <div class="text-xs text-gray-500 dark:text-dark-400">
                      #{{ agent.subagent_user_id }}
                      <span v-if="agent.username && agent.email"> · {{ agent.email }}</span>
                    </div>
                  </td>
                  <td class="px-4 py-3 font-mono text-gray-700 dark:text-dark-200">{{ agent.aff_code || '-' }}</td>
                  <td class="px-4 py-3">
                    <div class="relative w-28">
                      <input
                        class="input w-full pr-7"
                        type="number"
                        min="0"
                        max="100"
                        step="0.01"
                        :value="agent.commission_rate"
                        @change="onRateChange(agent, $event)"
                      />
                      <span class="pointer-events-none absolute inset-y-0 right-2 flex items-center text-xs text-gray-400">%</span>
                    </div>
                  </td>
                  <td class="px-4 py-3 font-mono">{{ formatInteger(agent.invited_count) }}</td>
                  <td class="px-4 py-3">
                    <div class="space-y-0.5 text-xs">
                      <div class="text-primary-600 dark:text-primary-400">应返 {{ formatUsdAmount(agent.cashback_due) }}</div>
                      <div class="text-emerald-600 dark:text-emerald-400">已返 {{ formatUsdAmount(agent.cashback_settled) }}</div>
                      <div class="text-amber-600 dark:text-amber-400">待返 {{ formatUsdAmount(agent.cashback_pending) }}</div>
                    </div>
                  </td>
                  <td class="px-4 py-3">
                    <span :class="agent.status === 'active' ? 'text-emerald-600 dark:text-emerald-400' : 'text-gray-500 dark:text-dark-400'">
                      {{ agent.status === 'active' ? '启用' : '停用' }}
                    </span>
                  </td>
                  <td class="px-4 py-3">
                    <div class="flex flex-wrap gap-3">
                      <button class="text-primary-600 hover:text-primary-700 dark:text-primary-400" type="button" @click="inspect(agent)">
                        查看统计
                      </button>
                      <button class="text-primary-600 hover:text-primary-700 dark:text-primary-400" type="button" @click="toggle(agent)">
                        {{ agent.status === 'active' ? '停用' : '启用' }}
                      </button>
                    </div>
                  </td>
                </tr>
                <tr v-if="!agents.length">
                  <td colspan="7" class="px-4 py-10 text-center text-gray-500 dark:text-dark-400">暂无二级代理</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>

        <div v-if="showCreate" class="rounded-xl border border-gray-200 bg-white p-5 dark:border-dark-700 dark:bg-dark-800">
          <div class="mb-4 flex items-center justify-between">
            <h2 class="font-semibold text-gray-900 dark:text-white">新增二级代理</h2>
            <button class="text-sm text-gray-500 hover:text-gray-700 dark:text-dark-400 dark:hover:text-dark-200" type="button" @click="closeCreate">
              关闭
            </button>
          </div>
          <div class="grid gap-3 md:grid-cols-3">
            <div class="relative">
              <input
                v-model="candidateQuery"
                placeholder="搜索邮箱、用户名或用户 ID"
                class="input w-full"
                @input="searchCandidates"
              />
              <div
                v-if="candidates.length"
                class="absolute left-0 right-0 top-full z-10 mt-1 max-h-48 overflow-auto rounded-lg border border-gray-200 bg-white shadow-lg dark:border-dark-600 dark:bg-dark-800"
              >
                <button
                  v-for="candidate in candidates"
                  :key="candidate.user_id"
                  type="button"
                  class="block w-full px-3 py-2 text-left text-sm hover:bg-gray-50 dark:hover:bg-dark-700"
                  @click="selectCandidate(candidate)"
                >
                  {{ candidate.username || candidate.email }}
                  <span class="text-gray-400">#{{ candidate.user_id }}</span>
                </button>
              </div>
            </div>
            <input v-model="form.aff_code" placeholder="邀请码（可选）" class="input w-full" />
            <div class="relative">
              <input v-model.number="form.commission_rate" type="number" min="0" max="100" step="0.01" placeholder="分佣比例" class="input w-full pr-7" />
              <span class="pointer-events-none absolute inset-y-0 right-3 flex items-center text-sm text-gray-400">%</span>
            </div>
          </div>
          <div v-if="form.user_id" class="mt-2 text-sm text-gray-500 dark:text-dark-400">
            已选择用户 ID：{{ form.user_id }}
          </div>
          <div class="mt-4 flex gap-2">
            <button class="btn btn-primary" type="button" :disabled="!form.user_id" @click="create">保存</button>
            <button class="btn btn-secondary" type="button" @click="closeCreate">取消</button>
          </div>
        </div>
      </template>
    </div>

    <BaseDialog
      :show="statsDialog"
      :title="selectedAgent ? `${agentName(selectedAgent)} 的统计` : '二级代理统计'"
      width="full"
      close-on-click-outside
      @close="closeStats"
    >
      <div v-if="selectedAgent" class="space-y-5">
        <div class="rounded-lg border border-blue-100 bg-blue-50 px-4 py-3 text-sm text-blue-800 dark:border-blue-900/60 dark:bg-blue-950/30 dark:text-blue-200">
          统计范围：二级代理直接邀请的用户。日期为空时统计全部历史数据；结束日期包含当天。
        </div>

        <div class="flex flex-wrap items-center gap-3">
          <div class="relative w-full md:w-80">
            <Icon name="search" size="md" class="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400" />
            <input
              v-model="statsFilters.search"
              class="input w-full pl-10"
              placeholder="搜索邀请用户邮箱、用户名或 ID"
              @input="debounceStatsLoad"
            />
          </div>
          <label class="flex w-full items-center gap-2 sm:w-auto">
            <span class="whitespace-nowrap text-sm text-gray-500 dark:text-dark-400">开始日期</span>
            <input v-model="statsFilters.start_at" type="date" class="input w-full sm:w-44" @change="reloadStatsFromFirstPage" />
          </label>
          <label class="flex w-full items-center gap-2 sm:w-auto">
            <span class="whitespace-nowrap text-sm text-gray-500 dark:text-dark-400">结束日期</span>
            <input v-model="statsFilters.end_at" type="date" class="input w-full sm:w-44" @change="reloadStatsFromFirstPage" />
          </label>
          <button class="btn btn-secondary px-3" type="button" :disabled="statsLoading" title="刷新" @click="loadStats">
            <Icon name="refresh" size="md" :class="statsLoading ? 'animate-spin' : ''" />
          </button>
        </div>

        <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4 xl:grid-cols-8">
          <div class="summary-tile">
            <p>邀请用户</p>
            <strong>{{ formatInteger(stats?.summary?.total_invited_users ?? statsPagination.total) }}</strong>
          </div>
          <div class="summary-tile">
            <p>累计充值（CNY）</p>
            <strong>{{ formatCnyAmount(stats?.summary?.total_recharge_amount) }}</strong>
          </div>
          <div class="summary-tile">
            <p>消费金额（USD）</p>
            <strong>{{ formatUsdAmount(stats?.summary?.total_actual_cost) }}</strong>
          </div>
          <div class="summary-tile">
            <p>请求次数</p>
            <strong>{{ formatInteger(stats?.summary?.total_requests) }}</strong>
          </div>
          <div class="summary-tile">
            <p>Token</p>
            <strong>{{ formatTokens(stats?.summary?.total_tokens) }}</strong>
          </div>
          <div class="summary-tile">
            <p>应返二级代理</p>
            <strong class="text-primary-600 dark:text-primary-400">{{ formatUsdAmount(stats?.cashback?.total_due) }}</strong>
          </div>
          <div class="summary-tile">
            <p>已返二级代理</p>
            <strong class="text-emerald-600 dark:text-emerald-400">{{ formatUsdAmount(stats?.cashback?.total_settled) }}</strong>
          </div>
          <div class="summary-tile">
            <p>待返二级代理</p>
            <strong class="text-amber-600 dark:text-amber-400">{{ formatUsdAmount(stats?.cashback?.total_pending) }}</strong>
          </div>
        </div>

        <div class="rounded-lg border border-gray-200 bg-gray-50 p-4 dark:border-dark-700 dark:bg-dark-900/60">
          <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
            <div>
              <h3 class="font-medium text-gray-900 dark:text-white">登记返现</h3>
              <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">记录已经支付给该二级代理的金额，保存后会从待返中扣除。</p>
            </div>
            <span class="text-xs text-gray-500 dark:text-dark-400">当前待返：{{ formatUsdAmount(stats?.cashback?.total_pending) }}</span>
          </div>
          <div class="grid gap-3 md:grid-cols-[minmax(0,1fr)_12rem_minmax(0,2fr)_auto]">
            <input v-model.number="settlementForm.amount" class="input" type="number" min="0.00000001" step="0.00000001" placeholder="返现金额" />
            <input v-model="settlementForm.settled_on" class="input" type="date" />
            <input v-model="settlementForm.note" class="input" maxlength="1000" placeholder="备注（可选）" />
            <button class="btn btn-primary" type="button" :disabled="settlementSaving || !settlementForm.amount || !settlementForm.settled_on" @click="createSettlement">
              {{ settlementSaving ? '保存中...' : '保存返现' }}
            </button>
          </div>
        </div>

        <div class="overflow-hidden rounded-lg border border-gray-200 dark:border-dark-700">
          <div class="overflow-x-auto">
            <table class="w-full min-w-[76rem] divide-y divide-gray-200 dark:divide-dark-700">
              <thead class="bg-gray-50 dark:bg-dark-900">
                <tr>
                  <th class="px-4 py-3 text-left text-xs font-medium uppercase text-gray-500 dark:text-dark-400">邀请用户</th>
                  <th class="px-4 py-3 text-right text-xs font-medium uppercase text-gray-500 dark:text-dark-400">请求次数</th>
                  <th class="px-4 py-3 text-right text-xs font-medium uppercase text-gray-500 dark:text-dark-400">Token</th>
                  <th class="px-4 py-3 text-right text-xs font-medium uppercase text-gray-500 dark:text-dark-400">消费（USD）</th>
                  <th class="px-4 py-3 text-right text-xs font-medium uppercase text-gray-500 dark:text-dark-400">充值（CNY）</th>
                  <th class="px-4 py-3 text-left text-xs font-medium uppercase text-gray-500 dark:text-dark-400">调用分组 / 模型</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
                <tr v-if="statsLoading">
                  <td colspan="6" class="px-4 py-10 text-center text-sm text-gray-500 dark:text-dark-400">加载中...</td>
                </tr>
                <tr v-else-if="!stats?.items?.length">
                  <td colspan="6" class="px-4 py-10 text-center text-sm text-gray-500 dark:text-dark-400">暂无邀请用户数据</td>
                </tr>
                <tr v-for="row in stats?.items || []" v-else :key="row.invitee_id" class="align-top hover:bg-gray-50 dark:hover:bg-dark-900/50">
                  <td class="px-4 py-3">
                    <div class="font-mono text-sm text-gray-900 dark:text-white">#{{ row.invitee_id }}</div>
                    <div class="max-w-64 truncate text-sm font-medium text-gray-900 dark:text-white">{{ row.invitee_email || '-' }}</div>
                    <div class="max-w-64 truncate text-xs text-gray-500 dark:text-dark-400">{{ row.invitee_username || '-' }}</div>
                  </td>
                  <td class="px-4 py-3 text-right font-mono text-sm text-gray-700 dark:text-dark-200">{{ formatInteger(row.requests) }}</td>
                  <td class="px-4 py-3 text-right font-mono text-sm text-gray-900 dark:text-white">{{ formatTokens(row.total_tokens) }}</td>
                  <td class="px-4 py-3 text-right font-mono text-sm text-emerald-600 dark:text-emerald-400">{{ formatUsdAmount(row.actual_cost) }}</td>
                  <td class="px-4 py-3 text-right font-mono text-sm text-sky-600 dark:text-sky-400">{{ formatCnyAmount(row.recharge_amount) }}</td>
                  <td class="px-4 py-3">
                    <div v-if="row.profit_details?.length" class="space-y-1.5">
                      <div v-for="detail in row.profit_details" :key="detailKey(detail)" class="max-w-[30rem] text-xs text-gray-700 dark:text-dark-200">
                        <span class="font-medium">{{ detail.group_name || `#${detail.group_id}` }}</span>
                        <span class="text-gray-400"> / </span>
                        <span>{{ detail.source === 'subscription' ? '订阅套餐' : detail.model || '-' }}</span>
                        <span class="ml-2 font-mono text-gray-500 dark:text-dark-400">{{ formatInteger(detail.requests) }} 次</span>
                      </div>
                    </div>
                    <span v-else class="text-xs text-gray-400">暂无调用</span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>

        <Pagination
          v-if="statsPagination.total > 0"
          :page="statsPagination.page"
          :total="statsPagination.total"
          :page-size="statsPagination.page_size"
          @update:page="handleStatsPageChange"
          @update:pageSize="handleStatsPageSizeChange"
        />
      </div>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import Pagination from '@/components/common/Pagination.vue'
import { useAppStore } from '@/stores/app'
import {
  secondLevelAgencyAPI,
  type SecondLevelAgent,
  type SecondLevelAgencyCandidate,
  type SecondLevelAgencyCapability,
  type SecondLevelUsageProfitDetail,
  type SecondLevelUsageResponse,
} from '@/api/secondLevelAgency'

const appStore = useAppStore()
const loading = ref(true)
const capability = ref<SecondLevelAgencyCapability | null>(null)
const agents = ref<SecondLevelAgent[]>([])
const cashbackTotals = computed(() => agents.value.reduce(
  (totals, agent) => ({
    due: totals.due + Number(agent.cashback_due || 0),
    settled: totals.settled + Number(agent.cashback_settled || 0),
    pending: totals.pending + Number(agent.cashback_pending || 0),
  }),
  { due: 0, settled: 0, pending: 0 },
))
const showCreate = ref(false)
const candidateQuery = ref('')
const candidates = ref<SecondLevelAgencyCandidate[]>([])
const form = reactive({ user_id: 0, aff_code: '', commission_rate: 0 })

const statsDialog = ref(false)
const statsLoading = ref(false)
const selectedAgent = ref<SecondLevelAgent | null>(null)
const stats = ref<SecondLevelUsageResponse | null>(null)
const statsPagination = reactive({ page: 1, page_size: 20, total: 0 })
const statsFilters = reactive({ search: '', start_at: '', end_at: '' })
const settlementSaving = ref(false)
const settlementForm = reactive({ amount: 0, settled_on: todayDateInput(), note: '' })
let candidateTimer: ReturnType<typeof setTimeout> | null = null
let statsTimer: ReturnType<typeof setTimeout> | null = null

async function load() {
  loading.value = true
  try {
    capability.value = await secondLevelAgencyAPI.getStatus()
    if (capability.value.enabled) {
      agents.value = await secondLevelAgencyAPI.list()
    } else {
      agents.value = []
    }
  } catch (error: any) {
    appStore.showError(error?.response?.data?.detail || '加载二级代理失败')
  } finally {
    loading.value = false
  }
}

async function create() {
  if (!form.user_id) return
  try {
    await secondLevelAgencyAPI.create(form)
    closeCreate()
    await load()
  } catch (error: any) {
    appStore.showError(error?.response?.data?.detail || '新增二级代理失败')
  }
}

function closeCreate() {
  showCreate.value = false
  form.user_id = 0
  form.aff_code = ''
  form.commission_rate = 0
  candidateQuery.value = ''
  candidates.value = []
}

function searchCandidates() {
  if (candidateTimer) clearTimeout(candidateTimer)
  candidateTimer = setTimeout(async () => {
    if (candidateQuery.value.trim().length < 2) {
      candidates.value = []
      return
    }
    try {
      candidates.value = await secondLevelAgencyAPI.candidates(candidateQuery.value.trim())
    } catch (error: any) {
      appStore.showError(error?.response?.data?.detail || '搜索用户失败')
    }
  }, 250)
}

function selectCandidate(candidate: SecondLevelAgencyCandidate) {
  form.user_id = candidate.user_id
  candidateQuery.value = candidate.username || candidate.email
  candidates.value = []
}

async function toggle(agent: SecondLevelAgent) {
  try {
    await secondLevelAgencyAPI.setStatus(agent.id, agent.status === 'active' ? 'disabled' : 'active')
    await load()
  } catch (error: any) {
    appStore.showError(error?.message || error?.response?.data?.detail || '更新代理状态失败')
  }
}

async function setRate(agent: SecondLevelAgent, rate: number) {
  try {
    await secondLevelAgencyAPI.setRate(agent.id, rate)
    await load()
  } catch (error: any) {
    appStore.showError(error?.response?.data?.detail || '更新分佣比例失败')
  }
}

function onRateChange(agent: SecondLevelAgent, event: Event) {
  const target = event.target as HTMLInputElement
  const rate = Number(target.value)
  if (!Number.isFinite(rate)) return
  void setRate(agent, rate)
}

async function inspect(agent: SecondLevelAgent) {
  selectedAgent.value = agent
  statsDialog.value = true
  stats.value = null
  statsPagination.page = 1
  statsFilters.search = ''
  statsFilters.start_at = ''
  statsFilters.end_at = ''
  settlementForm.amount = 0
  settlementForm.settled_on = todayDateInput()
  settlementForm.note = ''
  await loadStats()
}

function closeStats() {
  statsDialog.value = false
  selectedAgent.value = null
  stats.value = null
}

async function loadStats() {
  if (!selectedAgent.value) return
  statsLoading.value = true
  try {
    const result = await secondLevelAgencyAPI.usage(selectedAgent.value.id, {
      page: statsPagination.page,
      page_size: statsPagination.page_size,
      view: 'users',
      search: statsFilters.search.trim() || undefined,
      start_at: statsFilters.start_at || undefined,
      end_at: statsFilters.end_at || undefined,
      timezone: userTimezone(),
      sort_by: 'actual_cost',
      sort_order: 'desc',
    })
    stats.value = result
    statsPagination.total = Number(result.total || 0)
    statsPagination.page = Number(result.page || statsPagination.page)
    statsPagination.page_size = Number(result.page_size || statsPagination.page_size)
  } catch (error: any) {
    appStore.showError(error?.response?.data?.detail || '加载二级代理统计失败')
  } finally {
    statsLoading.value = false
  }
}

function debounceStatsLoad() {
  if (statsTimer) clearTimeout(statsTimer)
  statsTimer = setTimeout(reloadStatsFromFirstPage, 300)
}

function reloadStatsFromFirstPage() {
  statsPagination.page = 1
  void loadStats()
}

function handleStatsPageChange(page: number) {
  statsPagination.page = page
  void loadStats()
}

function handleStatsPageSizeChange(pageSize: number) {
  statsPagination.page_size = pageSize
  statsPagination.page = 1
  void loadStats()
}

async function createSettlement() {
  if (!selectedAgent.value || settlementForm.amount <= 0 || !settlementForm.settled_on) return
  settlementSaving.value = true
  try {
    await secondLevelAgencyAPI.createSettlement(selectedAgent.value.id, {
      amount: settlementForm.amount,
      settled_on: settlementForm.settled_on,
      note: settlementForm.note.trim() || undefined,
    })
    settlementForm.amount = 0
    settlementForm.note = ''
    await Promise.all([loadStats(), load()])
  } catch (error: any) {
    appStore.showError(error?.response?.data?.detail || '保存返现记录失败')
  } finally {
    settlementSaving.value = false
  }
}

function agentName(agent: SecondLevelAgent): string {
  return agent.username || agent.email || `用户 #${agent.subagent_user_id}`
}

function detailKey(detail: SecondLevelUsageProfitDetail): string {
  return `${detail.source || 'usage'}:${detail.group_id}:${detail.model}:${detail.requests}:${detail.actual_cost}`
}

function userTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  } catch {
    return 'UTC'
  }
}

function todayDateInput(): string {
  const date = new Date()
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

function formatInteger(value: number | null | undefined): string {
  return Math.round(Number(value || 0)).toLocaleString()
}

function formatTokens(value: number | null | undefined): string {
  const n = Number(value || 0)
  if (n >= 1_000_000) return `${trimNumber(n / 1_000_000)}M`
  if (n >= 1_000) return `${trimNumber(n / 1_000)}K`
  return formatInteger(n)
}

function trimNumber(value: number): string {
  return value.toFixed(value >= 10 ? 1 : 2).replace(/\.0+$/, '').replace(/(\.\d*[1-9])0+$/, '$1')
}

function formatUsdAmount(value: number | null | undefined): string {
  const n = Number(value || 0)
  return `$${Math.abs(n).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 4 })}`
}

function formatCnyAmount(value: number | null | undefined): string {
  const n = Number(value || 0)
  return `${n < 0 ? '-' : ''}\u00a5${Math.abs(n).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 4 })}`
}

onMounted(() => {
  void load()
})
</script>

<style scoped>
.summary-tile {
  min-height: 5.75rem;
  border-radius: 0.5rem;
  border: 1px solid rgb(229 231 235);
  background: white;
  padding: 0.875rem 1rem;
  box-shadow: 0 1px 2px rgb(15 23 42 / 0.04);
}

.summary-tile p {
  font-size: 0.75rem;
  color: rgb(107 114 128);
}

.summary-tile strong {
  margin-top: 0.4rem;
  display: block;
  font-size: 1.25rem;
  font-weight: 700;
  color: rgb(17 24 39);
}

:global(.dark) .summary-tile {
  border-color: rgb(51 65 85);
  background: rgb(30 41 59);
}

:global(.dark) .summary-tile p {
  color: rgb(148 163 184);
}

:global(.dark) .summary-tile strong {
  color: white;
}
</style>
