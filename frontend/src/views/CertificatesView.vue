<script setup lang="ts">
/**
 * 证书管理页面，提供状态筛选、详情查看和受保护材料下载。
 *
 * @author Andersen
 */
import { Certificate as CertificateIcon } from '@vicons/tabler'
import DetailFields from '@/components/DetailFields.vue'
import type { DetailField } from '@/types/details'
import AppButton from '@/components/AppButton.vue'
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { NAlert, NCard, NIcon, NInput, NPagination, NPopconfirm, NSpin, NTable } from 'naive-ui'
import { Search } from '@vicons/tabler'
import { api } from '@/api'
import type { Certificate, CertificateCounts } from '@/types'
import EmptyState from '@/components/EmptyState.vue'
import PageHeader from '@/components/PageHeader.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import AppSelect from '@/components/AppSelect.vue'
import AppModal from '@/components/AppModal.vue'
import { daysUntil, formatDate, shortId } from '@/utils/format'
import { useToast } from '@/composables/useToast'
import { useLatestRequest } from '@/composables/useLatestRequest'

/** certificates 是证书库列表。 */
const certificates = ref<Certificate[]>([])
/** total 是服务端证书版本总数。 */
const total = ref(0)
/** page 是当前证书页码。 */
const page = ref(1)
/** pageSize 是证书单页记录数。 */
const pageSize = 50
/** loading 表示证书列表是否正在加载。 */
const query = useLatestRequest()
/** loading 是最新请求控制的局部加载状态。 */
const loading = query.loading
/** search 是证书域名搜索关键字。 */
const search = ref('')
/** status 是后端处理的生命周期筛选。 */
const status = ref('ALL')
/** summary 是完整筛选结果的统计快照。 */
const summary = ref<CertificateCounts>({ total: 0, valid: 0, expiring: 0, expired: 0 })
/** searchTimer 合并连续输入的请求。 */
let searchTimer: number | undefined
/** statusOptions 是证书生命周期选项。 */
const statusOptions = [{ label: '全部状态', value: 'ALL' }, { label: '有效', value: 'VALID' }, { label: '即将到期', value: 'EXPIRING' }, { label: '已过期', value: 'EXPIRED' }]
/** detail 是当前打开详情的证书。 */
const detail = ref<Certificate | null>(null)
/** toast 提供统一页面通知。 */
const toast = useToast()

/** validCount 是有效证书数量。 */
const validCount = computed(() => summary.value.valid)
/** expiringCount 是即将过期证书数量。 */
const expiringCount = computed(() => summary.value.expiring)
/** expiredCount 是已过期证书数量。 */
const expiredCount = computed(() => summary.value.expired)

/** load 刷新证书列表并维护加载状态。 */
async function load() {
  window.clearTimeout(searchTimer)
  await query.run(
    /** 将全量筛选和取消信号交给后端。 */
    (signal) => api.listCertificates(page.value, pageSize, { search: search.value, status: status.value }, signal),
    /** 最新响应同时更新列表、总数和完整统计。 */
    (result) => {
      certificates.value = result.items
      total.value = result.total
      summary.value = result.summary ?? { total: result.total, valid: 0, expiring: 0, expired: 0 }
    },
    /** 查询失败保留已有记录并提供反馈。 */
    (error) => toast.error('证书列表加载失败', error instanceof Error ? error.message : undefined),
  )
}

/** changePage 切换服务端证书页并刷新结果。 */
function changePage(value: number) {
  page.value = value
  void load()
}

/** download 下载指定证书材料并展示结果通知。 */
async function download(id: number, type: 'CERTIFICATE' | 'FULL_CHAIN' | 'PRIVATE_KEY') {
  try {
    await api.downloadCertificate(id, type)
    toast.success('文件下载已开始')
  } catch (error) {
    toast.error('文件下载失败', error instanceof Error ? error.message : undefined)
  }
}

onMounted(load)
/** 状态切换后按相同条件重新统计并查询第一页。 */
watch(status, () => { page.value = 1; void load() })
/** 输入变化即取消旧查询，避免旧域名结果覆盖新搜索。 */
watch(search, () => {
  query.cancel()
  page.value = 1
  window.clearTimeout(searchTimer)
  /** 输入稳定后查询第一页。 */
  searchTimer = window.setTimeout(() => { void load() }, 250)
})
/** 页面销毁后清理尚未发出的搜索。 */
onUnmounted(() => window.clearTimeout(searchTimer))

/** certificateDetailFields 统一证书身份、有效期、域名和指纹的阅读顺序。 */
const certificateDetailFields = computed<DetailField[]>(() => {
  /** item 是当前查看的证书。 */
  const item = detail.value
  if (!item) return []
  return [
    { group: '证书信息', label: '主域名', value: item.commonName, wide: true },
    { group: '证书信息', label: '证书系列', value: item.seriesCode, mono: true },
    { group: '证书信息', label: '当前版本', value: `v${item.version}` },
    { group: '证书信息', label: '证书状态', value: item.status, status: item.status },
    { group: '证书信息', label: '颁发者', value: item.issuer },
    { group: '有效期与域名', label: '生效时间', value: formatDate(item.notBefore) },
    { group: '有效期与域名', label: '到期时间', value: formatDate(item.notAfter) },
    { group: '有效期与域名', label: 'DNS 名称', value: item.sans, wide: true },
    { group: '证书标识', label: '序列号', value: item.serialNumber, mono: true, wide: true },
    { group: '证书标识', label: 'SHA-256 指纹', value: item.fingerprint, mono: true, wide: true },
  ]
})
</script>

<template>
  <div>
    <PageHeader eyebrow="证书资料库" title="证书管理" description="集中查看有效期、颁发者和 SHA-256 指纹，并按需下载或重新分发证书文件。">
      <template #actions><RouterLink to="/orders"><AppButton action="create" tone="primary">申请新证书</AppButton></RouterLink></template>
    </PageHeader>

    <section class="certificate-stats">
      <div><i class="stat-icon valid">✓</i><span>有效证书<strong>{{ validCount }}</strong></span></div>
      <div><i class="stat-icon warning">!</i><span>即将到期<strong>{{ expiringCount }}</strong></span></div>
      <div><i class="stat-icon danger">×</i><span>已过期<strong>{{ expiredCount }}</strong></span></div>
      <div class="stat-note"><span>私钥存储</span><strong>AES-256-GCM</strong><small>仅在鉴权下载时即时解密</small></div>
    </section>

    <NCard class="content-card" :bordered="true" content-style="padding: 0">
      <template #header><div class="card-title"><h2>证书清单</h2><p>共 {{ total }} 张证书</p></div></template>
      <template #header-extra><AppButton action="refresh" size="small" :loading="loading" @click="load">刷新</AppButton></template>

      <div class="toolbar">
        <NInput v-model:value="search" class="search-input" placeholder="搜索主域名或 SAN" clearable>
          <template #prefix><NIcon :component="Search" /></template>
        </NInput>
        <AppSelect v-model:value="status" :options="statusOptions" class="status-filter" />
      </div>

      <div v-if="loading" class="loading-block"><NSpin size="small" />正在加载证书…</div>
      <div v-else-if="certificates.length" class="table-wrap">
        <NTable class="data-table certificate-table" :bordered="false" :single-line="true" size="small">
          <thead><tr><th>证书域名</th><th>状态</th><th>颁发者</th><th>有效期</th><th>版本与指纹</th><th class="action-cell">操作</th></tr></thead>
          <tbody>
            <tr v-for="certificate in certificates" :key="certificate.id">
              <td>
                <span class="certificate-name"><i><NIcon :component="CertificateIcon" /></i><span><b>{{ certificate.commonName }}</b><small>{{ certificate.seriesCode }} · {{ certificate.sans.length }} 个 SAN · v{{ certificate.version }}</small></span></span>
              </td>
              <td><StatusBadge :status="certificate.status" /></td>
              <td><span class="table-primary truncate">{{ certificate.issuer }}</span><span class="table-secondary">SN {{ shortId(certificate.serialNumber, 12) }}</span></td>
              <td><span class="table-primary mono">{{ formatDate(certificate.notAfter, false) }}</span><span class="table-secondary">剩余 {{ Math.max(daysUntil(certificate.notAfter) || 0, 0) }} 天</span></td>
              <td><span class="table-primary mono">v{{ certificate.version }}</span><span class="table-secondary">SHA {{ shortId(certificate.fingerprint, 12) }}</span></td>
              <td class="action-cell"><div class="action-group"><AppButton action="detail" size="small" @click="detail = certificate">详情</AppButton></div></td>
            </tr>
          </tbody>
        </NTable>
      </div>
      <EmptyState v-else title="还没有已签发证书" description="完成一条 ACME 申请后，证书会自动出现在这里并进入部署期望状态。" symbol="▱" />
      <div v-if="total > pageSize" class="pagination"><NPagination :page="page" :page-size="pageSize" :item-count="total" @update:page="changePage" /></div>
    </NCard>

    <AppModal :open="Boolean(detail)" title="证书详情" description="查看证书元数据并按需下载 PEM 文件；部署目标会自动跟随最新有效版本。" width="large" @close="detail = null">
      <template v-if="detail">
        <DetailFields :fields="certificateDetailFields" />

        <h3 class="detail-heading certificate-download-heading">证书下载</h3>
        <NAlert class="private-warning" type="warning" :show-icon="true" title="私钥属于高敏感文件">
          下载后的明文私钥不再受 Acme4go 主密钥保护，请使用受控目录保存并限制文件权限。
        </NAlert>

        <div class="download-grid">
          <AppButton action="download" class="download-button" @click="download(detail.id, 'CERTIFICATE')"><span><b>叶证书</b><small>certificate.pem</small></span></AppButton>
          <AppButton action="download" class="download-button" @click="download(detail.id, 'FULL_CHAIN')"><span><b>完整证书链</b><small>fullchain.pem</small></span></AppButton>
          <NPopconfirm positive-text="确认下载" negative-text="取消" @positive-click="download(detail.id, 'PRIVATE_KEY')">
            <template #trigger><AppButton action="credential" class="download-button"><span><b>证书私钥</b><small>即时解密 PEM</small></span></AppButton></template>
            确认在当前设备下载明文证书私钥吗？
          </NPopconfirm>
        </div>
      </template>
      <template #footer>
        <AppButton action="close" @click="detail = null">关闭</AppButton>
      </template>
    </AppModal>
  </div>
</template>

<style scoped>
.status-filter { width: 150px; }
.certificate-stats { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)) minmax(180px, 1.2fr); gap: 14px; margin-bottom: 18px; }
.certificate-stats > div { min-height: 82px; display: flex; align-items: center; gap: 13px; padding: 0 20px; border: 1px solid var(--line); border-radius: 12px; background: var(--panel); box-shadow: var(--shadow); }
.stat-icon { width: 34px; height: 34px; display: grid; place-items: center; border-radius: 50%; font-style: normal; font-size: 12px; font-weight: 700; }.stat-icon.valid { color: #506d5c; background: var(--sage); }.stat-icon.warning { color: #9c673e; background: #f6eee4; }.stat-icon.danger { color: #a65350; background: #f8ecea; }
.certificate-stats span { color: #77776e; font-size: 11px; }.certificate-stats span strong { display: block; margin-top: 5px; color: #3c3e37; font-size: 20px; font-weight: 700; }.stat-note { display: grid !important; grid-template-columns: 1fr auto; align-content: center; gap: 4px 12px !important; background: var(--sand) !important; }.stat-note strong { color: #52695c; font-size: 11px; }.stat-note small { grid-column: 1/-1; color: #89877e; font-size: 10px; }
.content-card { box-shadow: var(--shadow); }.card-title h2 { margin: 0; font-size: 15px; }.card-title p { margin: 4px 0 0; color: var(--muted); font-size: 11px; }.pagination { display: flex; justify-content: center; padding: 16px; border-top: 1px solid var(--line-soft); }
.toolbar { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 16px 20px; background: #fbfaf7; border-bottom: 1px solid var(--line-soft); }.search-input { width: min(330px, 100%); }.inventory-hint { color: #8b897f; font-size: 11px; }
.certificate-table { min-width: 960px; }.certificate-table :deep(th) { height: 46px; font-size: 11px; }.certificate-table :deep(td) { height: 70px; font-size: 12px; }
.certificate-name { display: flex; align-items: center; gap: 11px; }.certificate-name > i { width: 34px; height: 34px; display: grid; place-items: center; color: #5c7265; background: var(--sage); border-radius: 8px; font-size: 18px; font-style: normal; }.certificate-name b, .certificate-name small { display: block; }.certificate-name b { color: #3d3f38; font-size: 12px; }.certificate-name small { margin-top: 4px; color: #8b897f; font-size: 10px; }
.certificate-download-heading { margin-top: 22px; }.private-warning { margin-top: 12px; }.download-grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 10px; margin-top: 16px; }.download-button { width: 100%; min-height: 66px; justify-content: flex-start; }.download-button :deep(.n-button__content) { gap: 10px; text-align: left; }.download-button b, .download-button small { display: block; }.download-button b { font-size: 12px; }.download-button small { margin-top: 3px; color: #8b897f; font-size: 10px; }
@media (max-width: 760px) {
  .certificate-stats { grid-template-columns: repeat(3, 1fr); gap: 8px; }.certificate-stats > div { justify-content: center; padding: 0 8px; }.stat-note { display: none !important; }
  .inventory-hint { display: none; }.search-input { width: 100%; }.download-grid { grid-template-columns: 1fr; }}
</style>
