<script setup lang="ts">
/**
 * 证书申请页面，负责创建、筛选、轮询和重试 ACME 订单。
 *
 * @author Andersen
 */
import DetailFields from '@/components/DetailFields.vue'
import type { DetailField } from '@/types/details'
import AppButton from '@/components/AppButton.vue'
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { NAlert, NCard, NCheckbox, NForm, NFormItem, NIcon, NInput, NPagination, NRadioButton, NRadioGroup, NSpin, NTable } from 'naive-ui'
import { Search } from '@vicons/tabler'
import { api } from '@/api'
import type { CertificateOrder, DnsCredential, OrderEvent } from '@/types'
import CredentialPicker from '@/components/CredentialPicker.vue'
import AppSelect from '@/components/AppSelect.vue'
import AppModal from '@/components/AppModal.vue'
import EmptyState from '@/components/EmptyState.vue'
import PageHeader from '@/components/PageHeader.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { formatDate, stageLabel } from '@/utils/format'
import { useToast } from '@/composables/useToast'
import { useLatestRequest } from '@/composables/useLatestRequest'

/** orders 是证书订单列表。 */
const orders = ref<CertificateOrder[]>([])
/** total 是服务端订单记录总数。 */
const total = ref(0)
/** page 是当前订单页码。 */
const page = ref(1)
/** pageSize 是订单单页记录数。 */
const pageSize = 50
/** credentials 是可用于申请的 DNS 凭据列表。 */
const credentials = ref<DnsCredential[]>([])
/** loading 表示订单列表是否正在加载。 */
const query = useLatestRequest()
/** loading 是订单列表的局部加载状态。 */
const loading = query.loading
/** detailQuery 独立维护详情请求的取消和顺序。 */
const detailQuery = useLatestRequest()
/** submitting 表示新订单是否正在提交。 */
const submitting = ref(false)
/** modalOpen 表示新建订单弹窗是否打开。 */
const modalOpen = ref(false)
/** detailOrder 是当前打开详情的订单。 */
const detailOrder = ref<CertificateOrder | null>(null)
/** detailEvents 是当前详情的持久执行时间线。 */
const detailEvents = ref<OrderEvent[]>([])
/** retrying 表示重试提交尚未结束。 */
const retrying = ref(false)
/** filter 是订单状态筛选条件。 */
const filter = ref<'ALL' | CertificateOrder['status']>('ALL')
/** search 是订单域名或主键搜索关键字。 */
const search = ref('')
/** toast 提供统一页面通知。 */
const toast = useToast()
/** pollingTimer 是运行中订单定时刷新句柄。 */
let pollingTimer: number | undefined
/** searchTimer 合并连续输入，减少无效搜索请求。 */
let searchTimer: number | undefined

/** form 是新证书订单表单。 */
const form = reactive({
  name: '',
  domains: '',
  email: '',
  caEnvironment: 'LETS_ENCRYPT_STAGING' as CertificateOrder['caEnvironment'],
  credentialId: 0,
  agreeTerms: false,
})

/** caOptions 是 Let's Encrypt 测试和生产环境选项。 */
const caOptions = [
  { label: 'Let’s Encrypt 测试环境', value: 'LETS_ENCRYPT_STAGING', badge: '调试使用', description: '用于验证申请流程，签发的证书不受浏览器信任。' },
  { label: 'Let’s Encrypt 生产环境', value: 'LETS_ENCRYPT_PRODUCTION', badge: '正式证书', description: '用于正式业务域名，签发可供浏览器验证的证书。' },
]

/** runningCount 是仍需轮询的排队中和执行中订单数量。 */
const runningCount = computed(() => orders.value.filter((order) => ['QUEUED', 'RUNNING'].includes(order.status)).length)

/** load 取消旧查询并应用最后一次翻页或搜索，静默刷新不会产生重复错误弹窗。 */
async function load(silent = false) {
  window.clearTimeout(searchTimer)
  await query.run(
    /** 查询回调把完整筛选条件和取消信号交给接口层。 */
    (signal) => Promise.all([api.listOrders(page.value, pageSize, { search: search.value, status: filter.value }, signal), api.listDnsCredentials(signal)]),
    /** 只有最新查询可以替换列表和总数。 */
    ([orderData, credentialData]) => {
      orders.value = orderData.items
      total.value = orderData.total
      // 凭据选择只展示已启用的账户。
      credentials.value = credentialData.filter((item) => item.enabled)
    },
    /** 最新人工查询失败时提供明确反馈。 */
    (error) => { if (!silent) toast.error('申请记录加载失败', error instanceof Error ? error.message : undefined) },
    silent,
  )
}

/** loadDetail 让已打开的详情和时间线跟随后台任务刷新。 */
async function loadDetail() {
  /** id 是当前详情身份的快照。 */
  const id = detailOrder.value?.id
  if (!id) return
  await detailQuery.run(
    /** 详情查询使用独立取消信号。 */
    (signal) => api.orderDetail(id, signal),
    /** 身份匹配后一起更新状态和事件，避免显示另一笔订单的历史。 */
    (result) => { if (detailOrder.value?.id === id) { detailOrder.value = result.order; detailEvents.value = result.events } },
    /** 详情失败保留已有状态并通知管理员。 */
    (error) => toast.error('申请详情加载失败', error instanceof Error ? error.message : undefined),
    true,
  )
}

/** openDetail 清理上一笔任务的事件后读取最新执行详情。 */
function openDetail(order: CertificateOrder) {
  detailOrder.value = order
  detailEvents.value = []
  void loadDetail()
}

/** closeDetail 取消详情请求，防止关闭后的响应重新打开弹窗。 */
function closeDetail() {
  detailQuery.cancel()
  detailOrder.value = null
  detailEvents.value = []
}

/** changePage 切换服务端订单页并刷新当前结果。 */
function changePage(value: number) {
  page.value = value
  void load()
}

/** openCreate 校验可用凭据并重置新订单表单。 */
function openCreate() {
  if (!credentials.value.length) {
    toast.info('请先添加 DNS 凭据', '证书申请需要一个已启用的 DNS 服务商凭据。')
    return
  }
  Object.assign(form, {
    name: '',
    domains: '',
    email: '',
    caEnvironment: 'LETS_ENCRYPT_STAGING',
    credentialId: credentials.value[0]?.id || 0,
    agreeTerms: false,
  })
  modalOpen.value = true
}

/** createOrder 规范化域名并提交新的 ACME 订单。 */
async function createOrder() {
  if (submitting.value) return
  /** domains 是去重后的域名输入列表。 */
  const domains = [...new Set(form.domains.split(/[\s,，]+/).map(
    /** 域名规范化回调去除每项首尾空白。 */
    (item) => item.trim(),
  ).filter(Boolean))]
  if (!domains.length) {
    toast.error('请输入至少一个域名')
    return
  }
  if (!form.email.trim() || !form.credentialId) {
    toast.error('请完整填写联系邮箱与 DNS 凭据')
    return
  }
  if (!form.agreeTerms) {
    toast.error('提交前需要接受证书颁发机构服务条款')
    return
  }

  submitting.value = true
  try {
    await api.createOrder({
      name: form.name.trim() || domains[0] || '',
      domains,
      email: form.email.trim(),
      caEnvironment: form.caEnvironment,
      credentialId: form.credentialId,
      agreeTerms: form.agreeTerms,
    })
    modalOpen.value = false
    toast.success('证书申请已提交', '任务将在后台签发队列中执行。')
    page.value = 1
    filter.value = 'ALL'
    search.value = ''
    await load()
  } catch (error) {
    toast.error('申请提交失败', error instanceof Error ? error.message : undefined)
  } finally {
    submitting.value = false
  }
}

/** retry 为指定失败订单创建重试任务。 */
async function retry(order: CertificateOrder) {
  if (retrying.value) return
  retrying.value = true
  try {
    await api.retryOrder(order.id)
    toast.success('重试任务已创建')
    closeDetail()
    page.value = 1
    filter.value = 'ALL'
    search.value = ''
    await load()
  } catch (error) {
    toast.error('无法重试申请', error instanceof Error ? error.message : undefined)
  } finally {
    retrying.value = false
  }
}

/** 挂载回调首次加载数据并启动运行中订单轮询。 */
onMounted(() => {
  void load()
  /** 可见页面持续接管后台新任务，轮询不会打断人工查询。 */
  pollingTimer = window.setInterval(() => {
    if (document.visibilityState !== 'visible') return
    if (!query.pending.value) void load(true)
    if (detailOrder.value && ['QUEUED', 'RUNNING'].includes(detailOrder.value.status) && !detailQuery.pending.value) void loadDetail()
  }, 5000)
})

/** 卸载回调清理订单轮询定时器。 */
onUnmounted(() => { window.clearInterval(pollingTimer); window.clearTimeout(searchTimer) })

/** 状态筛选变化时重新查询第一页。 */
watch(filter, () => { page.value = 1; void load() })
/** 搜索输入变化立即使旧响应失效，再合并短时间内的连续输入。 */
watch(search, () => {
  query.cancel()
  page.value = 1
  window.clearTimeout(searchTimer)
  /** 合并输入后查询完整记录范围。 */
  searchTimer = window.setTimeout(() => { void load() }, 250)
})

/** orderDetailFields 展示当前申请的身份、进度和结果，时间线保持独立。 */
const orderDetailFields = computed<DetailField[]>(() => {
  /** item 是随轮询更新的申请记录。 */
  const item = detailOrder.value
  if (!item) return []
  return [
    { group: '申请信息', label: '申请编号', value: `#${item.id}` },
    { group: '申请信息', label: '证书系列', value: `${item.seriesCode} · ${item.seriesName}` },
    { group: '申请信息', label: '联系邮箱', value: item.email, wide: true },
    { group: '申请信息', label: '申请域名', value: item.domains, wide: true },
    { group: '执行进度', label: '状态', value: item.status, status: item.status },
    { group: '执行进度', label: '当前阶段', value: stageLabel(item.stage) },
    { group: '执行进度', label: '开始时间', value: formatDate(item.startedAt) },
    { group: '执行进度', label: '完成时间', value: formatDate(item.completedAt) },
    ...(item.errorMessage ? [{ group: '执行结果', label: '失败原因', value: item.errorMessage, wide: true, error: true }] : []),
  ]
})
</script>

<template>
  <div>
    <PageHeader eyebrow="证书签发" title="证书申请" description="通过 DNS-01 自动完成域名验证，跟踪每一次 CA 签发的完整执行阶段。">
      <template #actions>
        <AppButton action="create" tone="primary" @click="openCreate">新建申请</AppButton>
      </template>
    </PageHeader>

    <section class="summary-row">
      <div><span>记录总数</span><strong>{{ total }}</strong></div>
      <div><span>本页执行中</span><strong class="text-warning">{{ runningCount }}</strong></div>
      <div><span>本页成功</span><strong class="text-success">{{ orders.filter((item) => item.status === 'SUCCESS').length }}</strong></div>
      <div><span>本页失败</span><strong class="text-danger">{{ orders.filter((item) => item.status === 'FAILED').length }}</strong></div>
    </section>

    <NCard class="content-card" :bordered="true" content-style="padding: 0">
      <template #header><div class="card-title"><h2>申请记录</h2><p>执行中的任务每 5 秒自动刷新</p></div></template>
      <template #header-extra>
        <AppButton action="refresh" size="small" :loading="loading" @click="load()">刷新</AppButton>
      </template>

      <div class="toolbar">
        <NInput v-model:value="search" class="search-input" placeholder="搜索域名或申请编号" clearable>
          <template #prefix><NIcon :component="Search" /></template>
        </NInput>
        <NRadioGroup v-model:value="filter" size="small">
          <NRadioButton value="ALL">全部</NRadioButton>
          <NRadioButton value="QUEUED">排队中</NRadioButton>
          <NRadioButton value="RUNNING">执行中</NRadioButton>
          <NRadioButton value="SUCCESS">成功</NRadioButton>
          <NRadioButton value="FAILED">失败</NRadioButton>
        </NRadioGroup>
      </div>

      <div v-if="loading" class="loading-block"><NSpin size="small" />正在加载申请记录…</div>
      <div v-else-if="orders.length" class="table-wrap">
        <NTable class="data-table" :bordered="false" :single-line="true" size="small">
          <thead><tr><th>申请 / 域名</th><th>CA 环境</th><th>执行状态</th><th>当前阶段</th><th>创建时间</th><th class="action-cell">操作</th></tr></thead>
          <tbody>
            <tr v-for="order in orders" :key="order.id">
              <td><span class="table-primary">{{ order.seriesName || order.domains[0] }}</span><span class="table-secondary">{{ order.seriesCode }} · #{{ order.id }} · {{ order.domains.length }} 个域名</span></td>
              <td><span class="table-primary">{{ order.caEnvironment === 'LETS_ENCRYPT_PRODUCTION' ? 'Let’s Encrypt' : 'Let’s Encrypt Staging' }}</span><span class="table-secondary">DNS-01</span></td>
              <td><StatusBadge :status="order.status" /></td>
              <td><span class="stage-cell"><i :class="{ spinning: ['QUEUED', 'RUNNING'].includes(order.status) }" />{{ stageLabel(order.stage) }}</span></td>
              <td><span class="mono table-date">{{ formatDate(order.createdAt) }}</span></td>
              <td class="action-cell"><div class="action-group"><AppButton action="detail" size="small" @click="openDetail(order)">详情</AppButton></div></td>
            </tr>
          </tbody>
        </NTable>
      </div>
      <EmptyState v-else title="没有匹配的申请" description="新建申请后，ACME 的每一个执行阶段都会显示在这里。" symbol="↗">
        <AppButton action="create" tone="primary" @click="openCreate">新建申请</AppButton>
      </EmptyState>
      <div v-if="total > pageSize" class="pagination"><NPagination :page="page" :page-size="pageSize" :item-count="total" @update:page="changePage" /></div>
    </NCard>

    <AppModal :open="modalOpen" title="新建证书申请" description="建议先使用测试环境验证 DNS 权限与完整签发流程。" width="large" @close="modalOpen = false">
      <NAlert class="staging-tip" type="info" :show-icon="true" title="推荐先在 Staging 环境完成首轮验证">
        测试环境不会消耗生产签发配额，流程验证通过后再切换生产环境。
      </NAlert>
      <NForm id="order-form" class="form-grid" :model="form" label-placement="top" @submit.prevent="createOrder">
        <NFormItem class="full" label="证书系列名称" :show-feedback="false">
          <NInput v-model:value="form.name" placeholder="例如：公网入口证书（留空时使用首个域名）" maxlength="100" />
        </NFormItem>
        <NFormItem class="full" label="申请域名" :show-feedback="false">
          <NInput v-model:value="form.domains" type="textarea" :autosize="{ minRows: 4, maxRows: 7 }" placeholder="example.com&#10;*.example.com&#10;api.example.com" />
          <template #feedback>每行一个域名，也可以使用逗号分隔。</template>
        </NFormItem>
        <NFormItem label="联系邮箱" :show-feedback="false"><NInput v-model:value="form.email" placeholder="ops@example.com" /></NFormItem>
        <NFormItem label="CA 环境" :show-feedback="false"><AppSelect v-model:value="form.caEnvironment" :options="caOptions" /></NFormItem>
        <NFormItem class="full" label="DNS 凭据" :show-feedback="false"><CredentialPicker v-model="form.credentialId" :credentials="credentials" purpose="DNS" /></NFormItem>
        <NCheckbox v-model:checked="form.agreeTerms" class="full terms-checkbox">
          我已接受所选证书颁发机构的服务条款，并授权系统创建 DNS TXT 验证记录。
        </NCheckbox>
      </NForm>
      <template #footer>
        <AppButton action="close" @click="modalOpen = false">取消</AppButton>
        <AppButton action="certificate" tone="primary" attr-type="submit" form="order-form" :loading="submitting">
          提交申请
        </AppButton>
      </template>
    </AppModal>

    <AppModal :open="Boolean(detailOrder)" title="申请执行详情" description="阶段与失败原因持续保存，重试会创建新的独立记录。" width="large" @close="closeDetail">
      <DetailFields v-if="detailOrder" :fields="orderDetailFields" />
      <h3 v-if="detailEvents.length" class="detail-heading order-history-heading">执行时间线</h3>
      <ol v-if="detailEvents.length" class="order-timeline" aria-label="申请执行时间线">
        <li v-for="event in detailEvents" :key="event.id">
          <strong>{{ stageLabel(event.stage) }}</strong><time>{{ formatDate(event.createdAt) }}</time>
          <p>{{ event.message }}</p>
        </li>
      </ol>
      <template #footer>
        <AppButton action="close" @click="closeDetail">关闭</AppButton>
        <AppButton action="refresh" tone="primary" v-if="detailOrder?.status === 'FAILED'" :loading="retrying" :disabled="retrying" @click="retry(detailOrder)">重新申请</AppButton>
      </template>
    </AppModal>
  </div>
</template>

<style scoped>
.order-history-heading { margin-top: 22px; }
.order-timeline { margin: 0; padding-left: 22px; border-top: 1px solid var(--line-soft); }
.order-timeline li { padding: 14px 0 8px; font-size: 12px; }
.order-timeline time { margin-left: 12px; color: var(--muted); font-size: 11px; }
.order-timeline p { overflow-wrap: anywhere; white-space: pre-wrap; margin: 6px 0; color: var(--muted); line-height: 1.6; }
.summary-row { display: grid; grid-template-columns: repeat(4, 1fr); gap: 14px; margin-bottom: 18px; }
.summary-row div { display: flex; align-items: center; justify-content: space-between; min-height: 78px; padding: 0 20px; border: 1px solid var(--line); border-radius: 12px; background: var(--panel); box-shadow: var(--shadow); }
.summary-row span { color: #77776e; font-size: 12px; }.summary-row strong { color: #3d3f38; font-size: 21px; font-weight: 700; }
.text-success { color: #587361 !important; }.text-warning { color: #a56c41 !important; }.text-danger { color: #ad5753 !important; }
.content-card { box-shadow: var(--shadow); }.card-title h2 { margin: 0; font-size: 15px; }.card-title p { margin: 4px 0 0; color: var(--muted); font-size: 11px; }
.pagination { display: flex; justify-content: center; padding: 16px; border-top: 1px solid var(--line-soft); }
.toolbar { display: flex; align-items: center; justify-content: space-between; gap: 14px; padding: 16px 20px; background: #fbfaf7; border-bottom: 1px solid var(--line-soft); }.search-input { width: min(320px, 100%); }
.data-table { min-width: 880px; }.data-table :deep(th) { height: 46px; font-size: 11px; letter-spacing: 0; }.data-table :deep(td) { height: 68px; font-size: 12px; }
.table-date { font-size: 11px; white-space: nowrap; }.stage-cell { display: flex; align-items: center; gap: 8px; color: #686a62; font-size: 11px; }.stage-cell i { width: 7px; height: 7px; border: 1px solid #8c8d84; border-radius: 50%; }.stage-cell i.spinning { border-color: #b88258; border-top-color: transparent; animation: spin .8s linear infinite; }
.staging-tip { margin-bottom: 20px; }.form-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 0 18px; }.form-grid .full { grid-column: 1 / -1; }.terms-checkbox { margin: 4px 0 8px; align-items: flex-start; }
@media (max-width: 720px) {
  .summary-row { grid-template-columns: 1fr 1fr; gap: 8px; }
  .toolbar { align-items: stretch; flex-direction: column; }.search-input { width: 100%; }.toolbar :deep(.n-radio-group) { display: flex; overflow-x: auto; }.toolbar :deep(.n-radio-button) { flex: 1; }
  .form-grid { grid-template-columns: 1fr; }.form-grid .full { grid-column: auto; }
}
</style>
