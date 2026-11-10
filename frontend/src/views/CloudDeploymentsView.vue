<script setup lang="ts">
/** 云部署工作区：以表格管理上传规则、产品目标及执行记录，提供筛选和完整结果详情。 @author Andersen */
import DetailFields from '@/components/DetailFields.vue'
import type { DetailField } from '@/types/details'
import AppButton from '@/components/AppButton.vue'
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { NAlert, NCard, NInput, NPopconfirm, NSpin, NTable, NTabPane, NTabs, NTag } from 'naive-ui'
import PageHeader from '@/components/PageHeader.vue'
import AppSelect from '@/components/AppSelect.vue'
import AppModal from '@/components/AppModal.vue'
import CloudPlanModal from '@/components/cloud/CloudPlanModal.vue'
import CloudJobDetails from '@/components/cloud/CloudJobDetails.vue'
import { api } from '@/api'
import { cloudApi } from '@/api/cloud'
import { credentialsApi } from '@/api/credentials'
import type { Credential } from '@/types/credentials'
import type { CertificateSeries } from '@/types'
import type { CloudJob, CloudPlan, CloudProduct } from '@/types/cloud'
import { useLatestRequest } from '@/composables/useLatestRequest'
import { useToast } from '@/composables/useToast'
import { formatDate } from '@/utils/format'

/** credentials 是脱敏凭据列表。 */
const credentials = ref<Credential[]>([])
/** series 是可跟随续期的本地系列。 */
const series = ref<CertificateSeries[]>([])
/** products 是后端注册的产品集合。 */
const products = ref<CloudProduct[]>([])
/** plans 是独立上传与部署配置。 */
const plans = ref<CloudPlan[]>([])
/** jobs 是最新一页任务记录。 */
const jobs = ref<CloudJob[]>([])
/** before 是历史页游标，零表示最新页。 */
const before = ref(0)
/** tab 是当前工作区。 */
const tab = ref('UPLOAD')
/** search 在当前标签页按配置名称、资源、来源和凭据筛选。 */
const search = ref('')
/** productFilter 只筛选产品部署，不影响上传规则和任务历史。 */
const productFilter = ref('ALL')
/** selectedPlanId 标识只读详情，刷新后从配置列表读取最新数据。 */
const selectedPlanId = ref<number | null>(null)
/** jobStatus 只筛选当前已加载的一页记录，不改变后端历史游标。 */
const jobStatus = ref<CloudJob['status'] | 'ALL'>('ALL')
/** expandedJobId 控制主列表中原位展开的任务。 */
const expandedJobId = ref<number | null>(null)
/** expandedRelatedJobId 控制目标详情内展开的关联任务，不再打开第二个弹窗。 */
const expandedRelatedJobId = ref<number | null>(null)
/** modalOpen 控制配置弹窗。 */
const modalOpen = ref(false)
/** editing 保存当前编辑项。 */
const editing = ref<CloudPlan | null>(null)
/** kind 是表单的规则类型。 */
const kind = ref<'UPLOAD' | 'DEPLOY'>('UPLOAD')
/** busyId 限制重复执行或删除。 */
const busyId = ref<number | null>(null)
/** loader 保护主工作区加载竞态。 */
const loader = useLatestRequest()
/** jobLoader 保护任务轮询和历史翻页竞态。 */
const jobLoader = useLatestRequest()
/** toast 提供统一反馈。 */
const toast = useToast()
/** timer 在页面可见时刷新最新记录，捕获后台自动任务。 */
let timer: number | undefined
/** uploads 是只上传的配置。 */
const uploads = computed(() => plans.value.filter((p) => p.kind === 'UPLOAD'))
/** targets 是独立产品部署目标。 */
const targets = computed(() => plans.value.filter((p) => p.kind === 'DEPLOY'))
/** productOptions 从已注册产品与已有配置生成筛选项，兼容暂未注册的历史产品。 */
const productOptions = computed(() => [
  { label: '全部云产品', value: 'ALL' },
  ...Array.from(new Set([...products.value.map(
    /** 注册产品提供可筛选的类型。 */
    (product) => product.id), ...targets.value.map(
    /** 保留已有目标使用的历史类型。 */
    (plan) => plan.product)]))
    .filter(Boolean).map(
      /** 筛选项以中文名称展示，以稳定标识匹配。 */
      (id) => ({ label: productName(id), value: id })),
])
/** hasPlanFilters 只计算当前标签页的条件，用于匹配计数和清除筛选。 */
const hasPlanFilters = computed(() => !!search.value.trim() || (tab.value === 'DEPLOY' && productFilter.value !== 'ALL'))
/** detailPlan 显示当前保存的配置，避免详情使用编辑前的旧数据。 */
const detailPlan = computed(() => plans.value.find((plan) => plan.id === selectedPlanId.value) || null)
/** detailSeries 提供本地证书系列的域名和当前版本。 */
const detailSeries = computed(() => series.value.find((item) => item.id === detailPlan.value?.seriesId))
/** detailFields 按产品能力展示资源字段，也保留未知字段以免遗漏历史配置。 */
const detailFields = computed(() => {
  if (!detailPlan.value) return []
  /** fields 是该产品声明的资源字段。 */
  const fields = products.value.find((product) => product.id === detailPlan.value?.product)?.fields || []
  return Object.entries(detailPlan.value.config).map(
    /** 为资源配置补充中文名称，未知字段保留原键。 */
    ([key, value]) => ({ key, label: fields.find(
      /** 从后端能力定义中查找字段名。 */
      (field) => field.key === key)?.label || ({ domain: '业务域名', bucket: 'Bucket 名称', region: 'Bucket 地域' } as Record<string, string>)[key] || key, value }))
})
/** detailPlanJobs 仅展示当前已加载记录页中的关联任务，不将其冒充完整历史。 */
const detailPlanJobs = computed(() => jobs.value.filter((job) => job.planId === selectedPlanId.value))
/** sections 使两类规则共享表格与操作入口。 */
const sections = computed(() => [{ key: 'UPLOAD' as const, label: '证书上传', items: uploads.value }, { key: 'DEPLOY' as const, label: '产品部署', items: targets.value }])
/** canCreate 表示存在可用于云部署的启用凭据。 */
const canCreate = computed(() => credentials.value.some((item) => item.enabled && item.cloudEnabled && item.provider === 'ALIYUN'))
/** visiblePlans 是当前标签页匹配关键词的配置。 */
const visiblePlans = computed(() => {
  /** keyword 用于不区分大小写地匹配用户输入。 */
  const keyword = search.value.trim().toLowerCase()
  return plans.value.filter(
    /** 搜索只涉及配置的展示元数据，不请求或读取凭据秘密。 */
    (plan) => plan.kind === tab.value && (plan.kind !== 'DEPLOY' || productFilter.value === 'ALL' || plan.product === productFilter.value) && (!keyword || [plan.name, plan.product, ...Object.values(plan.config), sourceName(plan), credentialName(plan.credentialId)].join(' ').toLowerCase().includes(keyword)),
  )
})
/** visibleJobs 保留接口顺序，只过滤当前页执行状态。 */
const visibleJobs = computed(() => jobs.value.filter((job) => jobStatus.value === 'ALL' || job.status === jobStatus.value))
/** active 表示最新页存在进行中的任务。 */
const active = computed(() => jobs.value.some((j) => j.status === 'QUEUED' || j.status === 'RUNNING'))
/** statuses 是面向用户的任务状态。 */
const statuses = { QUEUED: '排队中', RUNNING: '执行中', SUCCESS: '成功', FAILED: '失败' }
/** jobStatusOptions 明确状态筛选仅作用于当前记录页。 */
const jobStatusOptions = [
  { label: '全部状态', value: 'ALL' },
  { label: '排队中', value: 'QUEUED' },
  { label: '执行中', value: 'RUNNING' },
  { label: '成功', value: 'SUCCESS' },
  { label: '失败', value: 'FAILED' },
]
/** stages 描述阶段位置。 */
const stages: Record<string, string> = { QUEUED: '等待执行', UPLOAD: '上传证书', CHECK_CERTIFICATE: '校验云证书', DEPLOY: '更新云产品', VERIFY: '确认生效', COMPLETE: '已完成' }

/** fail 将错误转换为统一通知。 */
function fail(error: unknown) { toast.error('云部署操作失败', error instanceof Error ? error.message : undefined) }
/** load 并行加载无依赖的工作区数据。 */
async function load() {
  await loader.run(

    /** 同时查询各领域的列表。 */

    async (signal) => Promise.all([credentialsApi.list(signal), cloudApi.plans(signal), cloudApi.products(), api.listCertificateSeries()]),

    /** 一次性应用同一轮查询结果。 */

    ([a, p, capabilities, local]) => { credentials.value = a; plans.value = p; products.value = capabilities; series.value = local },
    fail,
  )
  await loadJobs()
}
/** loadJobs 查询当前游标页；轮询不会打断已有请求。 */
function loadJobs(silent = false) {
  if (silent && jobLoader.pending.value) return
  return jobLoader.run(
    /** 查询当前任务游标。 */
    (signal) => cloudApi.jobs(before.value, signal),
    /** 更新任务列表。 */
    (value) => { jobs.value = value }, fail, silent)
}
/** openPlan 打开新增或编辑配置。 */
function openPlan(value: 'UPLOAD' | 'DEPLOY', plan: CloudPlan | null = null) { kind.value = value; editing.value = plan; modalOpen.value = true }
/** productName 使用能力注册表中的中文名称，未知产品保留原标识。 */
function productName(id: string) { return products.value.find(
  /** 匹配产品能力描述。 */
  (product) => product.id === id)?.name || id }
/** clearPlanFilters 清除配置列表的搜索和产品筛选。 */
function clearPlanFilters() { search.value = ''; productFilter.value = 'ALL' }
/** editDetailPlan 从只读详情进入既有编辑流程，避免同时打开两层弹窗。 */
function editDetailPlan() {
  if (!detailPlan.value) return
  openPlan(detailPlan.value.kind, detailPlan.value)
  selectedPlanId.value = null
}
/** showPlanDetails 打开显式详情入口，并重置上一次展开的关联任务。 */
function showPlanDetails(plan: CloudPlan) { expandedRelatedJobId.value = null; selectedPlanId.value = plan.id }
/** credentialName 显示凭据名称。 */
function credentialName(id: number) { return credentials.value.find(
    /** 匹配任务凭据。 */
    (a) => a.id === id)?.name || `凭据 #${id}` }
/** sourceName 展示本地系列或固定云证书。 */
function sourceName(plan: CloudPlan) { return plan.seriesId ? series.value.find(
    /** 匹配来源系列。 */
    (s) => s.id === plan.seriesId)?.name || plan.seriesId : `阿里云证书 #${plan.remoteCertificateId}` }
/** isRunning 根据最近任务禁用重复操作，后端仍执行最终互斥。 */
function isRunning(id: number) { return before.value === 0 && jobs.value.some(
    /** 检测该配置的活动任务。 */
    (j) => j.planId === id && ['QUEUED', 'RUNNING'].includes(j.status)) }
/** run 只提交异步任务，完成状态通过记录展示。 */
async function run(plan: CloudPlan) {
  if (busyId.value !== null) return
  busyId.value = plan.id
  try { await cloudApi.runPlan(plan.id); before.value = 0; await loadJobs(); toast.success('任务已入队，请查看执行记录') }
  catch (error) { fail(error) }
  finally { busyId.value = null }
}
/** remove 删除规则配置，保留云端资源。 */
async function remove(plan: CloudPlan) {
  if (busyId.value !== null) return
  busyId.value = plan.id
  try { await cloudApi.deletePlan(plan.id); await load(); toast.success('配置已删除，云端资源保留') }
  catch (error) { fail(error) }
  finally { busyId.value = null }
}
/** history 切换历史游标页。 */
function history(older: boolean) { expandedJobId.value = null; before.value = older ? jobs.value[jobs.value.length - 1]?.id || 0 : 0; void loadJobs() }

/** jobMessage 为尚无结果的任务提供与当前状态一致的说明。 */
function jobMessage(job: CloudJob) {
  return job.message || (job.status === 'QUEUED' ? '等待执行' : job.status === 'RUNNING' ? '正在执行，请稍候' : '暂无结果说明')
}

/** 挂载时加载工作区，并持续接管可见页面的新任务。 */
onMounted(() => {
  void load()
  timer = window.setInterval(
    /** 仅在可见的最新页轮询。 */
    () => { if (before.value === 0 && !document.hidden) void loadJobs(true) }, 5000)
})
/** 卸载时停止轮询，请求取消由 useLatestRequest 清理。 */
onBeforeUnmount(() => { if (timer) window.clearInterval(timer) })

/** planDetailFields 按配置、资源和证书组织详情，不读取云端私钥。 */
const planDetailFields = computed<DetailField[]>(() => {
  /** item 是当前已保存的配置，编辑刷新后同步更新。 */
  const item = detailPlan.value
  if (!item) return []
  /** certificateSeries 是当前配置关联的本地系列。 */
  const certificateSeries = detailSeries.value
  return [
    { group: '配置概览', label: item.kind === 'UPLOAD' ? '规则名称' : '目标名称', value: item.name },
    { group: '配置概览', label: '配置编号 / 修订', value: `#${item.id} · 修订 ${item.revision}` },
    { group: '配置概览', label: '状态', value: item.enabled ? '启用' : '暂停', status: item.enabled ? 'SUCCESS' : 'DISABLED' },
    { group: '配置概览', label: '同步方式', value: item.autoSync ? '自动同步当前证书及后续续期版本' : '手动执行' },
    { group: '部署资源', label: '云产品', value: item.kind === 'UPLOAD' ? '阿里云证书中心（仅上传）' : productName(item.product) },
    { group: '部署资源', label: '云部署凭据', value: `${credentialName(item.credentialId)} · 凭据 #${item.credentialId}` },
    ...detailFields.value.map(
      /** 产品声明的资源字段完整显示，不截断域名或定位参数。 */
      (field) => ({ group: '部署资源', label: field.label, value: field.value || '未配置', wide: true })),
    { group: '证书来源', label: '来源类型', value: item.seriesId ? '本地证书系列' : '阿里云已有证书' },
    { group: '证书来源', label: '关联证书', value: sourceName(item) },
    ...(item.seriesId ? [{ group: '证书来源', label: '证书系列编号', value: certificateSeries?.code || item.seriesId, mono: true, wide: true }] : [{ group: '证书来源', label: '固定云证书 ID', value: item.remoteCertificateId, mono: true }]),
    ...(certificateSeries ? [
      { group: '证书来源', label: '证书覆盖域名', value: certificateSeries.domains, wide: true },
      { group: '证书来源', label: '当前本地证书', value: certificateSeries.currentCertificateId ? `#${certificateSeries.currentCertificateId} · 版本 ${certificateSeries.currentVersion}` : '尚未签发' },
      { group: '证书来源', label: '当前证书到期时间', value: certificateSeries.currentNotAfter ? formatDate(certificateSeries.currentNotAfter) : '暂无证书' },
    ] : []),
  ]
})
</script>

<template>
  <div class="cloud-workspace">
    <PageHeader eyebrow="云端交付" title="云部署" description="上传证书到阿里云，或将证书部署到 OSS、CDN。支持续期后自动同步。">
      <template #actions><AppButton action="credential" @click="$router.push('/credentials')">凭据管理</AppButton><AppButton action="refresh" :loading="loader.loading.value" @click="load">刷新</AppButton></template>
    </PageHeader>
    <NAlert class="workspace-hint" type="info">本地证书可自动上传并部署，已有云证书可直接绑定；仅需同步到证书中心时，使用上传规则。</NAlert>
    <NCard class="plans-card" size="small" content-style="padding: 0">
      <NSpin :show="loader.loading.value">
        <NTabs v-model:value="tab" class="plan-tabs" type="line" :pane-style="{ padding: '0' }">
          <NTabPane v-for="section in sections" :key="section.key" :name="section.key" :tab="`${section.label}（${section.items.length}）`">
            <div class="table-toolbar">
              <div class="toolbar-copy">
                <h2>{{ section.key === 'UPLOAD' ? '上传规则' : '部署目标' }}</h2>
                <p>{{ section.key === 'UPLOAD' ? '将本地证书同步到阿里云证书中心' : '管理业务域名的证书绑定与续期同步' }}</p>
              </div>
              <div class="toolbar-actions">
                <AppSelect v-if="section.key === 'DEPLOY'" v-model:value="productFilter" class="product-filter" :options="productOptions" :input-props="{ 'aria-label': '筛选云产品类型' }" />
                <NInput v-model:value="search" class="plan-search" placeholder="搜索名称、域名、凭据" :input-props="{ 'aria-label': '搜索云配置' }" clearable />
                <AppButton action="create" tone="primary" :disabled="!canCreate" @click="openPlan(section.key)">{{ section.key === 'UPLOAD' ? '新建上传规则' : '新建部署目标' }}</AppButton>
              </div>
            </div>
            <div v-if="visiblePlans.length" class="table-wrap" role="region" :aria-label="`${section.label}配置表，可横向滚动`" tabindex="0">
              <NTable class="cloud-table plans-table" :class="{ 'deploy-table': section.key === 'DEPLOY' }" :bordered="false" :single-line="true" size="small">
                <thead><tr>
                  <th scope="col">{{ section.key === 'UPLOAD' ? '规则名称' : '目标名称' }}</th>
                  <th v-if="section.key === 'DEPLOY'" scope="col" class="target-column">云产品 / 域名</th>
                  <th scope="col" class="source-column">证书来源</th>
                  <th scope="col" class="credential-column">云部署凭据</th>
                  <th scope="col" class="sync-column">同步方式</th>
                  <th scope="col" class="status-column">状态</th>
                  <th scope="col" class="actions-column">操作</th>
                </tr></thead>
                <tbody>
                  <tr v-for="plan in visiblePlans" :key="plan.id">
                    <td><span class="cell-primary">{{ plan.name }}</span><span class="cell-secondary">配置 #{{ plan.id }}</span></td>
                    <td v-if="section.key === 'DEPLOY'">
                      <div class="target-heading"><span class="product-label">{{ plan.product }}</span><span class="cell-primary">{{ plan.config.domain || '—' }}</span></div>
                      <span v-if="plan.config.bucket" class="cell-secondary">{{ plan.config.bucket }}</span>
                      <span v-if="plan.config.region" class="cell-secondary">{{ plan.config.region }}</span>
                    </td>
                    <td><span class="source-label" :class="{ 'local-source': plan.seriesId }">{{ plan.seriesId ? '本地证书系列' : '阿里云已有证书' }}</span><span class="cell-secondary">{{ sourceName(plan) }}</span></td>
                    <td><span class="cell-primary">{{ credentialName(plan.credentialId) }}</span></td>
                    <td><span class="sync-label" :class="{ 'auto-sync': plan.autoSync }">{{ plan.autoSync ? '自动同步' : '手动执行' }}</span><span class="cell-secondary">{{ plan.autoSync ? '跟随当前及续期版本' : '按需触发' }}</span></td>
                    <td><NTag :type="plan.enabled ? 'success' : 'default'" size="small" :bordered="false">{{ plan.enabled ? '启用' : '暂停' }}</NTag></td>
                    <td class="actions-column">
                      <div class="action-group row-actions">
                        <AppButton action="detail" size="small" :aria-label="`查看${plan.kind === 'DEPLOY' ? '部署目标' : '上传规则'} ${plan.name} 详情`" @click="showPlanDetails(plan)">详情</AppButton>
                        <AppButton action="deploy" tone="accent" size="small" :loading="busyId === plan.id" :disabled="!plan.enabled || isRunning(plan.id) || busyId !== null" @click="run(plan)">{{ isRunning(plan.id) ? '执行中' : plan.kind === 'UPLOAD' ? '立即上传' : '立即部署' }}</AppButton>
                        <AppButton action="edit" size="small" :disabled="isRunning(plan.id) || busyId !== null" @click="openPlan(plan.kind, plan)">编辑</AppButton>
                        <NPopconfirm @positive-click="remove(plan)"><template #trigger><AppButton action="delete" tone="danger" size="small" :disabled="isRunning(plan.id) || busyId !== null">删除</AppButton></template>删除本地配置，保留云端证书及现有绑定。</NPopconfirm>
                      </div>
                    </td>
                  </tr>
                </tbody>
              </NTable>
            </div>
            <div v-else-if="!loader.loading.value" class="table-empty">
              <strong>{{ section.items.length ? '没有匹配的配置' : section.key === 'UPLOAD' ? '暂无上传规则' : '暂无部署目标' }}</strong>
              <p>{{ section.items.length ? '试试其他关键词，或调整筛选条件。' : canCreate ? '创建配置后，即可手动执行或开启自动同步。' : '请先在凭据管理中添加阿里云凭据，并启用云部署用途。' }}</p>
              <AppButton action="refresh" v-if="hasPlanFilters" size="small" @click="clearPlanFilters">清除筛选</AppButton>
              <AppButton action="credential" v-else-if="!canCreate" size="small" @click="$router.push('/credentials')">配置云部署凭据</AppButton>
            </div>
            <div v-if="section.items.length" class="table-footer"><span>{{ hasPlanFilters ? `匹配 ${visiblePlans.length} / ${section.items.length} 项配置` : `共 ${section.items.length} 项配置` }}</span><span>删除配置会保留云端证书及现有绑定</span></div>
          </NTabPane>
        </NTabs>
      </NSpin>
    </NCard>
    <NCard class="jobs-card" size="small" content-style="padding: 0">
      <div class="table-toolbar">
        <div class="toolbar-copy"><h2>执行记录</h2><p><span v-if="!before" class="live-indicator" :class="{ 'has-active': active }" />{{ before ? '历史记录 · 每页最多 50 条' : '最新记录 · 每 5 秒自动刷新' }}</p></div>
        <div class="toolbar-actions"><span class="filter-label">当前页状态</span><AppSelect v-model:value="jobStatus" class="status-filter" :options="jobStatusOptions" :input-props="{ 'aria-label': '筛选当前页执行状态' }" /></div>
      </div>
      <NSpin :show="jobLoader.loading.value">
        <div v-if="visibleJobs.length" class="table-wrap" role="region" aria-label="云部署执行记录表，可横向滚动" tabindex="0">
          <NTable class="cloud-table jobs-table" :bordered="false" :single-line="true" size="small">
            <thead><tr><th scope="col" class="time-column">更新时间</th><th scope="col" class="job-name-column">规则 / 目标</th><th scope="col" class="status-column">执行状态</th><th scope="col" class="stage-column">执行阶段</th><th scope="col" class="certificate-column">云证书 ID</th><th scope="col">结果说明</th><th scope="col" class="detail-column">操作</th></tr></thead>
            <tbody>
              <template v-for="job in visibleJobs" :key="job.id">
              <tr>
                <td><time class="cell-date" :datetime="job.updatedAt">{{ formatDate(job.updatedAt) }}</time></td>
                <td><span class="cell-primary">{{ job.planName }}</span><span class="cell-secondary">任务 #{{ job.id }}</span></td>
                <td><NTag :type="job.status === 'SUCCESS' ? 'success' : job.status === 'FAILED' ? 'error' : 'info'" size="small" :bordered="false">{{ statuses[job.status] }}</NTag></td>
                <td>{{ stages[job.stage] || job.stage }}</td>
                <td><span class="mono">{{ job.remoteCertificateId || '—' }}</span></td>
                <td><div class="result-preview" :class="{ 'result-error': job.status === 'FAILED' }" :title="jobMessage(job)">{{ jobMessage(job) }}</div></td>
                <td class="detail-column"><AppButton action="expand" size="small" :aria-label="`${expandedJobId === job.id ? '收起' : '展开'}任务 #${job.id} 详情`" :aria-expanded="expandedJobId === job.id" :aria-controls="`job-details-${job.id}`" @click="expandedJobId = expandedJobId === job.id ? null : job.id">{{ expandedJobId === job.id ? '收起' : '展开详情' }}</AppButton></td>
              </tr>
              <tr v-if="expandedJobId === job.id" class="job-expanded-row"><td colspan="7"><CloudJobDetails :id="`job-details-${job.id}`" :job="job" :status-label="statuses[job.status]" :stage-label="stages[job.stage] || job.stage" :message="jobMessage(job)" /></td></tr>
              </template>
            </tbody>
          </NTable>
        </div>
        <div v-else-if="!jobLoader.loading.value" class="table-empty">
          <strong>{{ jobs.length ? '当前页没有该状态的记录' : '暂无执行记录' }}</strong>
          <p>{{ jobs.length ? '切换状态筛选，或翻阅更早记录。' : '执行上传规则或部署目标后，可在这里查看进度与结果。' }}</p>
          <AppButton action="refresh" v-if="jobStatus !== 'ALL'" size="small" @click="jobStatus = 'ALL'">显示全部状态</AppButton>
        </div>
      </NSpin>
      <div class="table-footer"><span>本页 {{ jobs.length }} 条<span v-if="jobStatus !== 'ALL'"> · 筛选后 {{ visibleJobs.length }} 条</span></span><div class="action-group row-actions"><AppButton action="back" v-if="before" size="small" :disabled="jobLoader.pending.value" @click="history(false)">返回最新</AppButton><AppButton action="history" v-if="jobs.length === 50" size="small" :disabled="jobLoader.pending.value" @click="history(true)">更早记录</AppButton></div></div>
    </NCard>
    <AppModal :open="!!detailPlan" :title="detailPlan?.kind === 'UPLOAD' ? '上传规则详情' : '部署目标详情'" description="查看已保存的配置、证书来源及关联执行记录。" width="large" @close="selectedPlanId = null">
      <template v-if="detailPlan">
        <DetailFields :fields="planDetailFields" />
        <section class="plan-job-history" aria-label="此配置的当前页执行记录">
          <h3 class="detail-heading">关联执行记录 <span>当前记录页 · {{ detailPlanJobs.length }} 条</span></h3>
          <p v-if="!detailPlanJobs.length" class="cell-secondary">当前记录页中没有此配置的任务，可关闭详情后翻阅更早记录。</p>
          <ul v-else class="related-job-list">
            <li v-for="job in detailPlanJobs" :key="job.id">
              <div class="related-job-heading"><span>任务 #{{ job.id }} · {{ stages[job.stage] || job.stage }}</span><NTag :type="job.status === 'SUCCESS' ? 'success' : job.status === 'FAILED' ? 'error' : 'info'" size="small" :bordered="false">{{ statuses[job.status] }}</NTag></div>
              <time class="cell-secondary" :datetime="job.updatedAt">{{ formatDate(job.updatedAt) }} · 云证书 {{ job.remoteCertificateId || '尚未关联' }}</time>
              <p v-if="expandedRelatedJobId !== job.id" class="related-job-message result-preview" :class="{ 'result-error': job.status === 'FAILED' }">{{ jobMessage(job) }}</p>
              <AppButton action="expand" size="small" :aria-label="`${expandedRelatedJobId === job.id ? '收起' : '展开'}关联任务 #${job.id} 详情`" :aria-expanded="expandedRelatedJobId === job.id" :aria-controls="`related-job-details-${job.id}`" @click="expandedRelatedJobId = expandedRelatedJobId === job.id ? null : job.id">{{ expandedRelatedJobId === job.id ? '收起详情' : '展开执行详情' }}</AppButton>
              <CloudJobDetails v-if="expandedRelatedJobId === job.id" :id="`related-job-details-${job.id}`" class="related-job-details" :job="job" :status-label="statuses[job.status]" :stage-label="stages[job.stage] || job.stage" :message="jobMessage(job)" />
            </li>
          </ul>
        </section>
      </template>
      <template #footer><AppButton action="close" @click="selectedPlanId = null">关闭</AppButton><AppButton action="edit" tone="primary" v-if="detailPlan" :disabled="isRunning(detailPlan.id) || busyId !== null" @click="editDetailPlan">编辑配置</AppButton></template>
    </AppModal>
    <CloudPlanModal :open="modalOpen" :editing="editing" :kind="kind" :credentials="credentials" :series="series" :products="products" @close="modalOpen = false" @saved="load" />
  </div>
</template>

<style scoped>
.cloud-workspace { min-width: 0; }
.workspace-hint { margin-bottom: 22px; }
.jobs-card { margin-top: 24px; }
/* 标签导航与表格共用一张卡片，仅给导航保留左右内边距。 */
.plan-tabs :deep(.n-tabs-nav) { padding: 0 20px; }
.table-toolbar { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 16px; padding: 20px; }
.toolbar-copy h2 { margin: 0; color: var(--ink); font-size: 15px; font-weight: 650; }
.toolbar-copy p { display: flex; align-items: center; gap: 7px; margin: 5px 0 0; color: var(--muted); font-size: 12px; }
.toolbar-actions { display: flex; align-items: center; gap: 8px; }
.toolbar-actions { flex-wrap: wrap; }
.plans-table .row-actions { flex-wrap: nowrap; }
.plan-search { width: 250px; }
.status-filter { width: 140px; }
.product-filter { width: 160px; }
.plan-job-history { margin-top: 24px; padding-top: 20px; border-top: 1px solid var(--line-soft); }
.plan-job-history h3 { margin: 0 0 14px; font-size: 13px; font-weight: 600; }
.plan-job-history h3 span { margin-left: 8px; color: var(--muted); font-size: 11px; font-weight: 400; }
.related-job-list { margin: 0; padding: 0; list-style: none; }
.related-job-list li { padding: 14px 0; border-bottom: 1px solid var(--line-soft); }
.related-job-list li:last-child { border-bottom: 0; }
.related-job-heading { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; font-size: 12px; }
.related-job-details { margin-top: 12px; }
.related-job-message { margin: 10px 0 6px; font-size: 12px; line-height: 1.7; }
.related-job-message.result-error { color: var(--red); }
.filter-label { color: var(--muted); font-size: 12px; }
.live-indicator { width: 6px; height: 6px; flex: 0 0 auto; border-radius: 50%; background: var(--teal); }
.live-indicator.has-active { background: var(--orange); }
.table-wrap:focus-visible { outline: 2px solid var(--teal); outline-offset: -2px; }
/* 由外层 table-wrap 承担滚动，解除组件裁切，确保操作列相对滚动区域固定。 */
.cloud-table { width: 100%; table-layout: fixed; overflow: visible; font-size: 12px; }
.plans-table { min-width: 1000px; }
.deploy-table { min-width: 1300px; }
.jobs-table { min-width: 1060px; }
/* Naive UI 的单元格位于组件内部，在本页统一密度、换行和悬浮行背景。 */
.cloud-table :deep(th) { padding: 11px 16px; color: var(--muted); background: var(--nav); font-size: 11px; font-weight: 600; white-space: nowrap; }
.cloud-table :deep(td) { padding: 15px 16px; vertical-align: middle; overflow-wrap: anywhere; }
.cloud-table :deep(tbody tr:hover td) { background: var(--canvas); }
.target-column { width: 220px; }
.source-column { width: 190px; }
.credential-column { width: 110px; }
.sync-column { width: 148px; }
.status-column { width: 80px; }
.cloud-table :deep(.actions-column) { width: 372px; }
/* 桌面横向滚动时固定操作列；手机解除固定，避免操作遮住主要字段。 */
.cloud-table :deep(.actions-column), .cloud-table :deep(.detail-column) { position: sticky; right: 0; z-index: 1; background: var(--panel); border-left: 1px solid var(--line-soft); }
.cloud-table :deep(th.actions-column), .cloud-table :deep(th.detail-column) { background: var(--nav); }
.cell-primary { display: block; color: var(--ink); font-weight: 600; line-height: 1.6; }
.cell-secondary { display: block; margin-top: 5px; color: var(--muted); font-size: 11px; line-height: 1.6; }
.target-heading { display: flex; align-items: flex-start; gap: 7px; }
.product-label { flex: 0 0 auto; margin-top: 1px; padding: 1px 5px; border: 1px solid var(--line); border-radius: 4px; color: var(--muted); font-size: 10px; }
.source-label { display: inline-block; color: var(--blue); font-size: 12px; }
.source-label.local-source, .auto-sync { color: var(--teal); }
.sync-label { white-space: nowrap; font-weight: 500; }
.time-column { width: 164px; }
.job-name-column { width: 174px; }
.stage-column { width: 112px; }
.certificate-column { width: 124px; }
.cloud-table :deep(.detail-column) { width: 144px; }
.cell-date { color: var(--muted); font-size: 11px; font-variant-numeric: tabular-nums; white-space: nowrap; }
.result-preview { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--muted); }
.result-preview.result-error { color: var(--red); }
.table-empty { padding: 42px 20px; text-align: center; }
.table-empty strong { color: var(--ink); font-size: 14px; font-weight: 500; }
.table-empty p { margin: 8px 0 16px; color: var(--muted); font-size: 12px; }
.table-footer { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 10px; min-height: 48px; padding: 12px 20px; color: var(--muted); font-size: 11px; border-top: 1px solid var(--line-soft); }
/* 窄屏让工具栏换行，表格在自身区域滚动，避免整页横向溢出。 */
@media (max-width: 700px) {
  .table-toolbar { align-items: flex-start; padding: 16px; }
  .toolbar-actions { width: 100%; }
  .plan-search { width: 100%; }
  .table-footer { padding: 12px 16px; }
  .cloud-table :deep(.actions-column), .cloud-table :deep(.detail-column) { position: static; }
}
</style>
