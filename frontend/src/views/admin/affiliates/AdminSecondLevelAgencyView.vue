<template>
  <AppLayout>
    <div class="mx-auto max-w-6xl space-y-6 p-6">
      <div>
        <h1 class="text-2xl font-semibold">二级代理权限</h1>
        <p class="mt-1 text-sm text-gray-500">所有一级合伙人都会显示在这里，管理员可以单独授权其使用二级代理功能。</p>
      </div>
      <div class="overflow-hidden rounded-xl border bg-white">
        <table class="min-w-full text-left text-sm">
          <thead class="bg-gray-50">
            <tr>
              <th class="px-4 py-3">一级代理</th>
              <th class="px-4 py-3">合伙人等级</th>
              <th class="px-4 py-3">状态</th>
              <th class="px-4 py-3">默认分佣</th>
              <th class="px-4 py-3">最高分佣</th>
              <th class="px-4 py-3">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in rows" :key="row.root_partner_user_id" class="border-t">
              <td class="px-4 py-3">
                <div class="font-medium text-gray-900">{{ row.email || row.username || `用户 #${row.root_partner_user_id}` }}</div>
                <div class="text-xs text-gray-500">#{{ row.root_partner_user_id }}<span v-if="row.username && row.email"> · {{ row.username }}</span></div>
              </td>
              <td class="px-4 py-3">{{ partnerLabel(row) }}</td>
              <td class="px-4 py-3">{{ statusLabel(row) }}</td>
              <td class="px-4 py-3">{{ row.default_subagent_rate }}%</td>
              <td class="px-4 py-3">{{ row.max_subagent_rate }}%</td>
              <td class="px-4 py-3">
                <button class="text-primary-600" @click="edit(row)">
                  {{ row.enabled ? '调整/关闭' : '授权' }}
                </button>
              </td>
            </tr>
            <tr v-if="!rows.length"><td colspan="6" class="px-4 py-10 text-center text-gray-500">暂无一级合伙人</td></tr>
          </tbody>
        </table>
      </div>
    </div>
    <BaseDialog
      :show="!!editing"
      :title="editing ? `${editing.email || editing.username || `用户 #${editing.root_partner_user_id}`} 的二级代理权限` : '二级代理权限'"
      width="normal"
      close-on-click-outside
      @close="closeEditor"
    >
      <template v-if="editing">
        <div class="space-y-4">
          <div class="rounded-lg bg-gray-50 px-4 py-3 text-sm text-gray-600">
            <div class="font-medium text-gray-900">{{ partnerLabel(editing) }}</div>
            <div class="mt-1">用户 ID：#{{ editing.root_partner_user_id }}</div>
          </div>
          <div class="grid gap-4 sm:grid-cols-2">
            <label class="space-y-1">
              <span class="text-sm text-gray-600">默认分佣比例</span>
              <div class="relative">
                <input v-model.number="form.default_subagent_rate" type="number" min="0" max="100" step="0.01" class="w-full rounded-lg border px-3 py-2 pr-8" placeholder="默认分佣比例">
                <span class="pointer-events-none absolute inset-y-0 right-3 flex items-center text-sm text-gray-400">%</span>
              </div>
            </label>
            <label class="space-y-1">
              <span class="text-sm text-gray-600">最高分佣比例</span>
              <div class="relative">
                <input v-model.number="form.max_subagent_rate" type="number" min="0" max="100" step="0.01" class="w-full rounded-lg border px-3 py-2 pr-8" placeholder="最高分佣比例">
                <span class="pointer-events-none absolute inset-y-0 right-3 flex items-center text-sm text-gray-400">%</span>
              </div>
            </label>
          </div>
          <label class="flex items-center gap-2 text-sm text-gray-700">
            <input v-model="form.enabled" type="checkbox" class="rounded border-gray-300 text-primary-600">
            开启二级代理
          </label>
          <p class="text-xs leading-5 text-gray-500">默认分佣用于新建二级代理时的初始比例，最高分佣用于限制二级代理可设置的最高比例。</p>
        </div>
      </template>
      <template #footer>
        <div class="flex justify-end gap-2">
          <button class="rounded-lg border px-4 py-2 text-sm" @click="closeEditor">取消</button>
          <button class="rounded-lg bg-primary-600 px-4 py-2 text-sm text-white" @click="save">保存</button>
        </div>
      </template>
    </BaseDialog>
  </AppLayout>
</template>
<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { listSecondLevelAgencyCapabilities, updateSecondLevelAgencyCapability, type SecondLevelAgencyCapability } from '@/api/admin/affiliates'
const rows = ref<SecondLevelAgencyCapability[]>([]); const editing = ref<SecondLevelAgencyCapability | null>(null); const form = reactive({ enabled: false, default_subagent_rate: 30, max_subagent_rate: 50 })
function partnerLevelLabel(level?: string) { const labels: Record<string, string> = { spark: '星火合伙人', voyage: '远航合伙人', summit: '巅峰合伙人', cocreate: '共创合伙人' }; return labels[level || ''] || level || '-' }
function partnerLabel(row: SecondLevelAgencyCapability) {
  if (row.partner_level && row.partner_level !== 'none') return partnerLevelLabel(row.partner_level)
  if (row.aff_rebate_rate_percent != null) return `特殊返佣 ${row.aff_rebate_rate_percent}%`
  return '特殊返佣'
}
function statusLabel(row: SecondLevelAgencyCapability) { if (!row.configured) return '未授权'; return row.enabled ? '已授权' : '已关闭' }
function edit(row: SecondLevelAgencyCapability) {
  editing.value = row
  // Opening the dialog must preserve the current authorization state.
  // Revocation is an explicit action: uncheck the switch and save.
  form.enabled = row.enabled
  form.default_subagent_rate = row.default_subagent_rate
  form.max_subagent_rate = row.max_subagent_rate
}
function closeEditor() { editing.value = null }
async function load() { rows.value = await listSecondLevelAgencyCapabilities() }
async function save() { if (!editing.value) return; await updateSecondLevelAgencyCapability(editing.value.root_partner_user_id, form); editing.value = null; await load() }
onMounted(load)
</script>
