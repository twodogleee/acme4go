<script setup lang="ts">
/**
 * 审计日志页面，按游标分页追溯管理员敏感操作与证书材料下载。
 *
 * @author Andersen
 */
import AppButton from '@/components/AppButton.vue'
import { onMounted, ref } from 'vue'
import { NCard, NIcon, NSpin, NTable } from 'naive-ui'
import { History } from '@vicons/tabler'
import { api } from '@/api'
import type { AuditLog } from '@/types'
import EmptyState from '@/components/EmptyState.vue'
import AppSelect from '@/components/AppSelect.vue'
import PageHeader from '@/components/PageHeader.vue'
import DetailFields from '@/components/DetailFields.vue'
import { formatDate } from '@/utils/format'
import { useToast } from '@/composables/useToast'

/** logs 是已经加载的审计记录。 */
const logs = ref<AuditLog[]>([])
/** nextCursor 是下一页游标。 */
const nextCursor = ref<number | null>(null)
/** loading 表示正在读取第一页或刷新。 */
const loading = ref(true)
/** loadingMore 表示正在追加下一页。 */
const loadingMore = ref(false)
/** action 是当前动作筛选条件。 */
const action = ref('')
/** resourceType 是当前资源类型筛选条件。 */
const resourceType = ref('')
/** toast 提供统一页面通知。 */
const toast = useToast()
/** actionOptions 是常用审计动作筛选项。 */
const actionOptions = [
  { label: '全部动作', value: '' }, { label: '创建', value: 'CREATE' }, { label: '更新', value: 'UPDATE' },
  { label: '删除', value: 'DELETE' }, { label: '撤销', value: 'REVOKE' }, { label: '下载', value: 'DOWNLOAD' },
  { label: '登录', value: 'LOGIN' }, { label: '修改密码', value: 'CHANGE_PASSWORD' },
]
/** resourceOptions 是常用审计资源筛选项。 */
const resourceOptions = [
  { label: '全部资源', value: '' }, { label: '证书订单', value: 'CERTIFICATE_ORDER' },
  { label: '证书系列', value: 'CERTIFICATE_SERIES' }, { label: '证书材料', value: 'CERTIFICATE_MATERIAL' },
  { label: 'DNS 凭据', value: 'DNS_CREDENTIAL' }, { label: '部署目标', value: 'DEPLOYMENT_TARGET' },
  { label: 'Agent', value: 'AGENT' }, { label: 'Agent 注册', value: 'AGENT_ENROLLMENT' }, { label: '用户', value: 'USER' },
]

/** load 刷新筛选条件下的第一页审计记录。 */
async function load() {
  expandedId.value = null
  loading.value = true
  try {
    /** page 是服务端返回的首个游标页。 */
    const page = await api.listAuditLogs(undefined, action.value, resourceType.value)
    logs.value = page.items
    nextCursor.value = page.nextCursor
  } catch (error) {
    toast.error('审计日志加载失败', error instanceof Error ? error.message : undefined)
  } finally {
    loading.value = false
  }
}

/** loadMore 使用下一页游标追加更早记录。 */
async function loadMore() {
  if (!nextCursor.value) return
  loadingMore.value = true
  try {
    /** page 是服务端返回的下一游标页。 */
    const page = await api.listAuditLogs(nextCursor.value, action.value, resourceType.value)
    logs.value.push(...page.items)
    nextCursor.value = page.nextCursor
  } catch (error) {
    toast.error('更早审计记录加载失败', error instanceof Error ? error.message : undefined)
  } finally {
    loadingMore.value = false
  }
}

onMounted(load)
/** expandedId 是当前原位展开的记录编号，不复制记录以便跟随刷新。 */
const expandedId = ref<number | null>(null)
/** recordFields 完整展示指定记录的安全元数据和结果。 */
function recordFields(item: AuditLog) {
  return [
    { group: '操作信息', label: '记录编号', value: item.id },
    { group: '操作信息', label: '操作时间', value: formatDate(item.createdAt) },
    { group: '操作信息', label: '操作人', value: item.username || '系统任务' },
    { group: '操作信息', label: '用户编号', value: item.userId },
    { group: '操作信息', label: '动作', value: item.action },
    { group: '关联资源', label: '资源类型', value: item.resourceType },
    { group: '关联资源', label: '完整资源标识', mono: true, value: item.resourceId, wide: true },
    { group: '操作内容', label: '完整操作详情', value: item.detail, wide: true },
  ]
}
</script>

<template>
  <div>
    <PageHeader eyebrow="安全追溯" title="审计日志" description="集中查看管理操作和证书材料下载记录；日志不保存密码、令牌、私钥或 DNS Secret。">
      <template #actions><AppButton action="refresh" :loading="loading" @click="load">刷新</AppButton></template>
    </PageHeader>

    <NCard class="content-card" :bordered="true" content-style="padding: 0">
      <template #header><div class="card-title"><h2><NIcon :component="History" />操作记录</h2><p>按时间从近到远排列</p></div></template>
      <div class="toolbar">
        <AppSelect v-model:value="action" class="filter" :options="actionOptions" @update:value="load" />
        <AppSelect v-model:value="resourceType" class="filter wide" :options="resourceOptions" @update:value="load" />
        <span>已加载 {{ logs.length }} 条</span>
      </div>
      <div v-if="loading" class="loading-block"><NSpin size="small" />正在读取审计日志…</div>
      <div v-else-if="logs.length" class="table-wrap">
        <NTable class="data-table" :bordered="false" :single-line="true" size="small">
          <thead><tr><th>时间</th><th>管理员</th><th>动作</th><th>资源</th><th>摘要</th><th class="action-cell">操作</th></tr></thead>
          <tbody>
            <template v-for="log in logs" :key="log.id"><tr>
              <td><span class="table-primary mono">{{ formatDate(log.createdAt) }}</span><span class="table-secondary">#{{ log.id }}</span></td>
              <td><span class="table-primary">{{ log.username || '系统任务' }}</span><span class="table-secondary">{{ log.userId ? `用户 #${log.userId}` : '无用户身份' }}</span></td>
              <td><span class="action-code">{{ log.action }}</span></td>
              <td><span class="table-primary mono">{{ log.resourceType }}</span><span class="table-secondary">{{ log.resourceId || '—' }}</span></td>
              <td><span class="detail">{{ log.detail || '—' }}</span></td>
              <td class="action-cell"><div class="action-group"><AppButton action="expand" size="small" :aria-label="`${expandedId === log.id ? '收起' : '展开'}审计记录 #${log.id} 详情`" :aria-expanded="expandedId === log.id" :aria-controls="`audit-details-${log.id}`" @click="expandedId = expandedId === log.id ? null : log.id">{{ expandedId === log.id ? '收起' : '展开详情' }}</AppButton></div></td>
            </tr><tr v-if="expandedId === log.id"><td colspan="6"><DetailFields :id="`audit-details-${log.id}`" :fields="recordFields(log)" /></td></tr></template>
          </tbody>
        </NTable>
        <div class="load-more"><AppButton action="history" v-if="nextCursor" :loading="loadingMore" @click="loadMore">加载更早记录</AppButton><span v-else>已经到底了</span></div>
      </div>
      <EmptyState v-else title="没有匹配的审计记录" description="后续登录、配置修改、撤销和证书材料下载操作会显示在这里。" symbol="≡" />
    </NCard>
  </div>
</template>

<style scoped>
.content-card { box-shadow: var(--shadow); }.card-title h2 { display: flex; align-items: center; gap: 8px; margin: 0; font-size: 15px; }.card-title p { margin: 4px 0 0; color: var(--muted); font-size: 11px; }.toolbar { display: flex; align-items: center; gap: 10px; padding: 14px 18px; border-bottom: 1px solid var(--line-soft); }.toolbar .filter { width: 150px; }.toolbar .wide { width: 190px; }.toolbar > span { margin-left: auto; color: var(--muted); font-size: 10px; }.action-code { display: inline-flex; padding: 4px 7px; color: #4f6558; background: var(--sage); border-radius: 5px; font: 10px ui-monospace, monospace; }.detail { display: block; max-width: 360px; overflow: hidden; color: #55574f; font-size: 11px; text-overflow: ellipsis; white-space: nowrap; }.load-more { display: flex; justify-content: center; padding: 16px; border-top: 1px solid var(--line-soft); }.load-more span { color: var(--muted); font-size: 10px; }
@media (max-width: 680px) { .toolbar { align-items: stretch; flex-direction: column; }.toolbar .filter, .toolbar .wide { width: 100%; }.toolbar > span { margin-left: 0; }.detail { max-width: 220px; } }
</style>
