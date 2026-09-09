<template>
  <AppLayout>
    <div class="mx-auto max-w-6xl space-y-6 p-6">
      <div><h1 class="text-2xl font-semibold">二级代理权限</h1><p class="mt-1 text-sm text-gray-500">仅管理员授权的一级合伙人可以使用二级代理功能。</p></div>
      <div class="overflow-hidden rounded-xl border bg-white"><table class="min-w-full text-left text-sm"><thead class="bg-gray-50"><tr><th class="px-4 py-3">一级代理用户 ID</th><th class="px-4 py-3">状态</th><th class="px-4 py-3">默认分佣</th><th class="px-4 py-3">最高分佣</th><th class="px-4 py-3">操作</th></tr></thead><tbody><tr v-for="row in rows" :key="row.root_partner_user_id" class="border-t"><td class="px-4 py-3">{{ row.root_partner_user_id }}</td><td class="px-4 py-3">{{ row.enabled ? '已授权' : '已关闭' }}</td><td class="px-4 py-3">{{ row.default_subagent_rate }}%</td><td class="px-4 py-3">{{ row.max_subagent_rate }}%</td><td class="px-4 py-3"><button class="text-primary-600" @click="edit(row)">{{ row.enabled ? '调整/关闭' : '授权' }}</button></td></tr><tr v-if="!rows.length"><td colspan="5" class="px-4 py-10 text-center text-gray-500">暂无授权记录</td></tr></tbody></table></div>
      <div v-if="editing" class="rounded-xl border bg-white p-5"><div class="grid gap-3 sm:grid-cols-3"><input v-model.number="form.default_subagent_rate" type="number" class="rounded-lg border px-3 py-2" placeholder="默认分佣比例"><input v-model.number="form.max_subagent_rate" type="number" class="rounded-lg border px-3 py-2" placeholder="最大分佣比例"><label class="flex items-center gap-2"><input v-model="form.enabled" type="checkbox"> 开启二级代理</label></div><div class="mt-4 flex gap-2"><button class="rounded-lg bg-primary-600 px-4 py-2 text-sm text-white" @click="save">保存</button><button class="rounded-lg border px-4 py-2 text-sm" @click="editing = null">取消</button></div></div>
    </div>
  </AppLayout>
</template>
<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { listSecondLevelAgencyCapabilities, updateSecondLevelAgencyCapability, type SecondLevelAgencyCapability } from '@/api/admin/affiliates'
const rows = ref<SecondLevelAgencyCapability[]>([]); const editing = ref<SecondLevelAgencyCapability | null>(null); const form = reactive({ enabled: false, default_subagent_rate: 30, max_subagent_rate: 50 })
function edit(row: SecondLevelAgencyCapability) { editing.value = row; form.enabled = !row.enabled; form.default_subagent_rate = row.default_subagent_rate; form.max_subagent_rate = row.max_subagent_rate }
async function load() { rows.value = await listSecondLevelAgencyCapabilities() }
async function save() { if (!editing.value) return; await updateSecondLevelAgencyCapability(editing.value.root_partner_user_id, form); editing.value = null; await load() }
onMounted(load)
</script>
