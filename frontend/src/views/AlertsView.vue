<script setup lang="ts">
/**
 * 通知投递页面，查看持久告警、测试连接并重新发送失败事件。
 *
 * @author Andersen
 */
import AppButton from '@/components/AppButton.vue'
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { NAlert, NCard, NPagination, NSpin, NTable, NTag } from 'naive-ui'
import { api } from '@/api'
import type { AlertDelivery } from '@/types'
import AppSelect from '@/components/AppSelect.vue'
import PageHeader from '@/components/PageHeader.vue'
import DetailFields from '@/components/DetailFields.vue'
import EmptyState from '@/components/EmptyState.vue'
import { useLatestRequest } from '@/composables/useLatestRequest'
import { useToast } from '@/composables/useToast'
import { formatDate } from '@/utils/format'

/** deliveries 是当前页的通知投递记录。 */
const deliveries = ref<AlertDelivery[]>([])
/** page 是当前页码。 */
const page = ref(1)
/** total 是筛选范围的通知总数。 */
const total = ref(0)
/** status 是投递状态筛选。 */
const status = ref('ALL')
/** enabled 表示服务端已经配置通知地址。 */
const enabled = ref(false)
/** testing 阻止重复创建测试事件。 */
const testing = ref(false)
/** retrying 保存当前重发的通知标识。 */
const retrying = ref<number | null>(null)
/** query 维护最新列表请求。 */
const query = useLatestRequest()
/** loading 是列表局部加载状态。 */
const loading = query.loading
/** toast 提供统一操作结果反馈。 */
const toast = useToast()
/** timer 是可见页面的轮询句柄。 */
let timer: number | undefined
/** statusOptions 将投递阶段映射为管理员可理解的文字。 */
const statusOptions = [{ label: '全部状态', value: 'ALL' }, { label: '待发送', value: 'PENDING' }, { label: '发送中', value: 'SENDING' }, { label: '等待重试', value: 'RETRY' }, { label: '已送达', value: 'SENT' }, { label: '发送失败', value: 'FAILED' }]

/** statusLabel 返回投递状态对应的中文文案。 */
function statusLabel(value: string) {
  // 通过固定枚举查询中文状态，未知值原样显示以兼容后端升级。
  return statusOptions.find((option) => option.value === value)?.label ?? value
}

/** load 按完整状态条件刷新列表，并同步通知配置状态。 */
async function load(silent = false) {
  await query.run(
    /** 读取持久记录和脱敏配置摘要。 */
    (signal) => Promise.all([api.listAlerts(page.value, status.value, signal), api.systemInfo()]),
    /** 最新结果同步分页和配置状态。 */
    ([result, info]) => { deliveries.value = result.items; total.value = result.total; enabled.value = info.alertWebhookEnabled },
    /** 人工加载失败时保留已有记录并显示原因。 */
    (error) => { if (!silent) toast.error('通知记录加载失败', error instanceof Error ? error.message : undefined) },
    silent,
  )
}

/** testNotification 创建测试事件后刷新记录，让用户查看真实送达结果。 */
async function testNotification() {
  if (testing.value) return
  testing.value = true
  try {
    await api.testAlert()
    toast.success('测试通知已入队', '请查看投递状态确认是否送达。')
    page.value = 1
    status.value = 'ALL'
    await load()
  } catch (error) {
    toast.error('创建测试通知失败', error instanceof Error ? error.message : undefined)
  } finally { testing.value = false }
}

/** retry 将失败事件重新排队，发送中的记录不能重复提交。 */
async function retry(id: number) {
  if (retrying.value !== null) return
  retrying.value = id
  try {
    await api.retryAlert(id)
    toast.success('通知已重新入队')
    await load(true)
  } catch (error) {
    toast.error('重新发送失败', error instanceof Error ? error.message : undefined)
  } finally { retrying.value = null }
}

/** changePage 切换页码并取消尚未完成的旧页请求。 */
function changePage(value: number) { expandedId.value = null; page.value = value; void load() }

/** 筛选改变时回到第一页。 */
watch(status, () => { expandedId.value = null; page.value = 1; void load() })
/** 挂载时立即加载，并在可见页面持续跟踪投递结果。 */
onMounted(() => {
  void load()
  /** 静默轮询不会打断用户刚发起的查询。 */
  timer = window.setInterval(() => { if (document.visibilityState === 'visible' && !query.pending.value) void load(true) }, 5000)
})
/** 离开页面时停止轮询。 */
onUnmounted(() => window.clearInterval(timer))
/** expandedId 是当前原位展开的记录编号，不复制记录以便跟随刷新。 */
const expandedId = ref<number | null>(null)
/** recordFields 完整展示指定记录的安全元数据和结果。 */
function recordFields(item: AlertDelivery) {
  return [
    { group: '通知信息', label: '通知编号', value: item.id },
    { group: '通知信息', label: '事件类型', value: item.event },
    { group: '通知信息', label: '通知级别', status: item.severity === 'ERROR' ? 'FAILED' : item.severity === 'WARNING' ? 'PENDING' : 'UNKNOWN', value: item.severity === 'ERROR' ? '错误' : item.severity === 'WARNING' ? '警告' : '信息' },
    { group: '投递进度', label: '投递状态', status: item.status === 'SENT' ? 'SUCCESS' : item.status === 'FAILED' ? 'FAILED' : 'PENDING', value: statusLabel(item.status) },
    { group: '关联资源', label: '资源类型', value: item.resourceType },
    { group: '关联资源', label: '完整资源标识', mono: true, value: item.resourceId },
    { group: '通知内容', label: '完整通知内容', value: item.message, wide: true },
    { group: '投递进度', label: '已尝试次数', value: item.attempts },
    { group: '投递进度', label: '下次投递时间', value: ['PENDING', 'RETRY'].includes(item.status) ? formatDate(item.nextAttemptAt) : '无待定投递' },
    { group: '通知信息', label: '事件时间', value: formatDate(item.timestamp) },
    { group: '投递进度', label: '送达时间', value: formatDate(item.deliveredAt) },
    { group: '投递进度', label: '最近投递错误', error: true, value: item.lastError, wide: true },
  ]
}
</script>

<template>
  <div>
    <PageHeader eyebrow="运行通知" title="告警投递" description="跟踪签发、部署、证书到期和 Agent 离线通知，失败后自动重试。">
      <template #actions><AppButton action="send" tone="primary" :disabled="!enabled || testing" :loading="testing" @click="testNotification">测试通知</AppButton></template>
    </PageHeader>
    <NAlert v-if="!loading && !enabled" type="info" class="notification-note">尚未启用通知。请配置 Webhook 接收地址和签名密钥后重启 Server，历史记录仍可查看。</NAlert>
    <NAlert v-else type="info" class="notification-note">发送失败后逐步延长重试间隔，最多尝试 8 次；仍失败的记录可人工重新发送。到期和离线提醒每日去重，证书风险升级时会立即创建新提醒。</NAlert>
    <NCard :bordered="true" content-style="padding: 0">
      <template #header>通知记录 · {{ total }}</template>
      <template #header-extra><AppButton action="refresh" :loading="loading" @click="load()" size="small">刷新</AppButton></template>
      <div class="notification-toolbar"><AppSelect v-model:value="status" :options="statusOptions" /></div>
      <div v-if="loading" class="loading-block"><NSpin size="small" />正在加载通知…</div>
      <div v-else-if="deliveries.length" class="table-wrap">
        <NTable :bordered="false" size="small">
          <thead><tr><th>通知内容</th><th>投递状态</th><th>发送进度</th><th>时间</th><th class="action-cell">操作</th></tr></thead>
          <tbody>
            <template v-for="delivery in deliveries" :key="delivery.id"><tr>
              <td class="notification-message"><strong>{{ delivery.message }}</strong><small>#{{ delivery.id }} · {{ delivery.event }} · {{ delivery.resourceType }} / {{ delivery.resourceId }}</small></td>
              <td><NTag :type="delivery.status === 'SENT' ? 'success' : delivery.status === 'FAILED' ? 'error' : 'warning'" size="small">{{ statusLabel(delivery.status) }}</NTag></td>
              <td><span>已尝试 {{ delivery.attempts }} 次</span><small v-if="delivery.status === 'RETRY'">下次 {{ formatDate(delivery.nextAttemptAt) }}</small><small v-if="delivery.lastError" class="text-danger">{{ delivery.lastError }}</small></td>
              <td><span>{{ formatDate(delivery.timestamp) }}</span><small v-if="delivery.deliveredAt">送达 {{ formatDate(delivery.deliveredAt) }}</small></td>
              <td class="action-cell"><div class="action-group delivery-actions"><AppButton action="expand" size="small" :aria-label="`${expandedId === delivery.id ? '收起' : '展开'}通知 #${delivery.id} 详情`" :aria-expanded="expandedId === delivery.id" :aria-controls="`alert-details-${delivery.id}`" @click="expandedId = expandedId === delivery.id ? null : delivery.id">{{ expandedId === delivery.id ? '收起' : '展开详情' }}</AppButton><AppButton action="send" v-if="['FAILED', 'RETRY'].includes(delivery.status)" size="small" :disabled="!enabled || retrying !== null" :loading="retrying === delivery.id" @click="retry(delivery.id)">重新发送</AppButton></div></td>
            </tr><tr v-if="expandedId === delivery.id"><td colspan="5"><DetailFields :id="`alert-details-${delivery.id}`" :fields="recordFields(delivery)" /></td></tr></template>
          </tbody>
        </NTable>
      </div>
      <EmptyState v-else title="暂无匹配通知" description="启用 Webhook 后，系统会记录通知和每次投递结果。" />
      <div v-if="total > 50" class="notification-pagination"><NPagination :page="page" :page-size="50" :item-count="total" @update:page="changePage" /></div>
    </NCard>
  </div>
</template>

<style scoped>
.delivery-actions { flex-wrap: nowrap; }
.notification-note { margin-bottom: 20px; }
.notification-toolbar { padding: 16px 20px; max-width: 230px; }
.notification-pagination { display: flex; justify-content: flex-end; padding: 16px 20px; }
.notification-message { min-width: 220px; max-width: 460px; overflow-wrap: anywhere; }
td { font-size: 12px; }
td small { display: block; margin-top: 7px; color: var(--muted); font-size: 11px; }
</style>
