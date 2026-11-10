<script setup lang="ts">
/**
 * 文件部署页面，管理本机与远程 Agent 目标、路径和部署历史。
 *
 * @author Andersen
 */
import AppButton from '@/components/AppButton.vue'
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { NAlert, NCard, NForm, NFormItem, NIcon, NInput, NPopconfirm, NSpin, NSwitch } from 'naive-ui'
import { Copy, FileCertificate, Folder, Lock } from '@vicons/tabler'
import { api } from '@/api'
import type { AgentNode, CertificateSeries, DeploymentRecord, DeploymentTarget, DeploymentTargetInput, SystemInfo } from '@/types'
import type { SelectChoice } from '@/types/select'
import AppSelect from '@/components/AppSelect.vue'
import AppModal from '@/components/AppModal.vue'
import LocalCertificatePicker from '@/components/LocalCertificatePicker.vue'
import EmptyState from '@/components/EmptyState.vue'
import PageHeader from '@/components/PageHeader.vue'
import DetailFields from '@/components/DetailFields.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { formatDate, shortId } from '@/utils/format'
import { useToast } from '@/composables/useToast'
import { useLatestRequest } from '@/composables/useLatestRequest'

/** targets 是全部文件部署目标。 */
const targets = ref<DeploymentTarget[]>([])
/** records 是最近部署结果记录。 */
const records = ref<DeploymentRecord[]>([])
/** agents 是可选远程执行节点。 */
const agents = ref<AgentNode[]>([])
/** series 是可供人工绑定的证书系列列表。 */
const series = ref<CertificateSeries[]>([])
/** systemInfo 是当前 Server 的执行器配置摘要。 */
const systemInfo = ref<SystemInfo | null>(null)
/** loading 表示页面数据是否正在加载。 */
const loader = useLatestRequest()
/** loading 是当前非静默刷新状态。 */
const loading = loader.loading
/** syncTimer 仅在有远程手动任务时轮询。 */
let syncTimer: number | undefined
/** saving 表示目标表单是否正在提交。 */
const saving = ref(false)
/** operatingTargetId 是当前执行同步或删除操作的目标主键。 */
const operatingTargetId = ref<number | null>(null)
/** modalOpen 表示目标编辑弹窗是否打开。 */
const modalOpen = ref(false)
/** editingTarget 是当前正在修改的部署目标。 */
const editingTarget = ref<DeploymentTarget | null>(null)
/** toast 提供统一页面通知。 */
const toast = useToast()
/** form 是部署目标编辑表单。 */
const form = reactive({
  executorKey: null as string | null,
  name: '',
  certificateSeriesId: null as string | null,
  targetAlias: '',
  enabled: true,
})

/** successfulCount 是最近部署成功的目标数量。 */
const successfulCount = computed(() => targets.value.filter((target) => target.status === 'SUCCESS').length)
/** failedCount 是最近部署失败的目标数量。 */
const failedCount = computed(() => targets.value.filter(
  /** 异常目标过滤回调同时统计部署失败和待重新绑定目标。 */
  (target) => target.status === 'FAILED' || target.status === 'NEEDS_BINDING',
).length)
/** executorOptions 是当前可选择的本机和 Agent 执行器。 */
const executorOptions = computed(() => {
  /** options 是由已启用 Agent 构造的选择项。 */
  const options: SelectChoice[] = agents.value
    .filter((agent) => agent.enabled)
    .map((agent) => ({ label: agent.name, value: `agent:${agent.id}`, badge: agent.status === 'ONLINE' ? '在线' : agent.status === 'REVOKED' ? '已撤销' : '离线', description: agent.status === 'OFFLINE' ? '远程 Agent · 离线时任务等待节点恢复' : '远程 Agent 执行器', details: [`平台：${agent.platform || '未提供'} · 版本：${agent.version || '未提供'}`, `最近在线：${formatDate(agent.lastSeenAt)}`, `节点 ID：${agent.id}`] }))
  if (systemInfo.value?.localDeploymentEnabled) {
    options.unshift({ label: 'Server 本机执行器', value: 'local', badge: '本机', description: '由控制平面所在服务器直接部署证书文件', details: [`部署目录：${systemInfo.value.deploymentDir || '由服务器配置'}`] })
  }
  return options
})
/** defaultExecutorKey 是新目标默认选中的可用执行器。 */
const defaultExecutorKey = computed(() => {
  if (systemInfo.value?.localDeploymentEnabled) return 'local'
  /** agent 是第一个可用远程执行节点。 */
  const agent = agents.value.find((item) => item.enabled)
  return agent ? `agent:${agent.id}` : null
})

/** load 只应用最新刷新结果，静默轮询不打断已有查询。 */
async function load(silent = false) {
  if (silent && loader.pending.value) return
  await loader.run(
    /** 并行查询页面关联数据。 */
    () => Promise.all([api.listDeploymentTargets(), api.listDeploymentRecords(), api.listAgents(), api.systemInfo(), api.listCertificateSeries()]),
    /** 应用本轮完整状态。 */
    ([targetData, recordData, agentData, infoData, seriesData]) => {
      targets.value = targetData; records.value = recordData; agents.value = agentData
      systemInfo.value = infoData; series.value = seriesData
    },
    /** 静默轮询失败保留状态，手动刷新给出错误提示。 */
    (error) => { if (!silent) toast.error('文件部署状态加载失败', error instanceof Error ? error.message : undefined) },
    silent,
  )
}

/** openCreate 重置表单并选择默认执行器。 */
function openCreate() {
  editingTarget.value = null
  Object.assign(form, {
    executorKey: defaultExecutorKey.value,
    name: '',
    certificateSeriesId: series.value.find(
      /** 默认系列查找回调选择首个启用系列。 */
      (item) => item.enabled,
    )?.id || null,
    targetAlias: '',
    enabled: true,
  })
  modalOpen.value = true
}

/** openEdit 将现有目标映射到编辑表单。 */
function openEdit(target: DeploymentTarget) {
  editingTarget.value = target
  Object.assign(form, {
    executorKey: target.executorType === 'LOCAL' ? 'local' : `agent:${target.agentId}`,
    name: target.name,
    certificateSeriesId: target.certificateSeriesId,
    targetAlias: target.targetAlias,
    enabled: target.enabled,
  })
  modalOpen.value = true
}

/** save 校验目录别名并创建或更新部署目标。 */
async function save() {
  if (!form.executorKey || !form.name.trim() || !form.certificateSeriesId || !form.targetAlias.trim()) {
    toast.error('请完整填写部署目标信息')
    return
  }
  if (!/^[a-z0-9][a-z0-9_-]{0,63}$/.test(form.targetAlias)) {
    toast.error('目录别名只能包含小写字母、数字、下划线和中划线')
    return
  }
  saving.value = true
  try {
    /** isLocal 表示当前表单选择 Server 本机执行器。 */
    const isLocal = form.executorKey === 'local'
    /** payload 是提交给控制平面的标准部署目标参数。 */
    const payload: DeploymentTargetInput = {
      executorType: isLocal ? 'LOCAL' : 'AGENT',
      agentId: isLocal ? null : form.executorKey.replace(/^agent:/, ''),
      name: form.name.trim(),
      certificateSeriesId: form.certificateSeriesId,
      targetAlias: form.targetAlias.trim(),
      enabled: form.enabled,
    }
    if (editingTarget.value) {
      await api.updateDeploymentTarget(editingTarget.value.id, payload)
      toast.success('部署目标已更新')
    } else {
      await api.createDeploymentTarget(payload)
      toast.success('部署目标已创建', '若已有匹配证书，节点会立即生成标准 PEM 文件。')
    }
    modalOpen.value = false
    await load()
  } catch (error) {
    toast.error('部署目标保存失败', error instanceof Error ? error.message : undefined)
  } finally {
    saving.value = false
  }
}

/** deploy 本机等待执行完成，远程展示持久请求受理状态。 */
async function deploy(target: DeploymentTarget) {
  if (operatingTargetId.value !== null || target.manualSync) return
  operatingTargetId.value = target.id
  try {
    await api.deployTarget(target.id)
    if (target.executorType === 'AGENT') toast.success('同步请求已登记', 'Agent 下次拉取后执行；离线时请求会保留。')
    else toast.success('证书文件已切换', `${target.name} 的 current 目录已经指向最新版本。`)
    await load()
  } catch (error) {
    toast.error('证书文件部署失败', error instanceof Error ? error.message : undefined)
  } finally {
    operatingTargetId.value = null
  }
}

/** remove 删除目标配置但保留已物化文件。 */
async function remove(target: DeploymentTarget) {
  operatingTargetId.value = target.id
  try {
    await api.deleteDeploymentTarget(target.id)
    toast.success('部署目标配置已删除', '磁盘文件继续保留，运行中的业务服务不会被中断。')
    await load()
  } catch (error) {
    toast.error('部署目标删除失败', error instanceof Error ? error.message : undefined)
  } finally {
    operatingTargetId.value = null
  }
}

/** copyPath 将业务应用稳定读取路径写入剪贴板。 */
async function copyPath(path: string) {
  try {
    await navigator.clipboard.writeText(path)
    toast.success('读取路径已复制')
  } catch {
    toast.error('复制失败', '请手动选择路径文本。')
  }
}

onMounted(() => {
  void load()
  syncTimer = window.setInterval(
    /** 只轮询可见页面中的活动单次请求。 */
    () => { if (!document.hidden && targets.value.some(
      /** 活动任务完成后自动停止查询。 */
      (target) => target.manualSync,
    )) void load(true) }, 5000,
  )
})
onBeforeUnmount(() => { if (syncTimer) window.clearInterval(syncTimer) })
/** detailId 标识正在只读查看的文件部署目标，刷新后自动使用当前数据。 */
const detailId = ref<number | null>(null)
/** detailItem 是当前列表中的最新详情对象。 */
const detailItem = computed(() => targets.value.find((item) => item.id === detailId.value) || null)
/** detailFields 显式选择可展示的业务元数据，不访问秘密材料。 */
const detailFields = computed(() => {
  /** item 是当前详情对象。 */
  const item = detailItem.value
  if (!item) return []
  return [
    { group: '目标配置', label: '目标名称', value: item.name },
    { group: '目标配置', label: '目标编号', value: item.id },
    { group: '目标配置', label: '执行器', value: `${item.executorType === 'LOCAL' ? 'Server 本机' : '远程 Agent'} · ${item.executorName}` },
    { group: '目标配置', label: 'Agent ID', mono: true, value: item.agentId },
    { group: '绑定证书', label: '证书系列', value: item.seriesName || '待绑定' },
    { group: '绑定证书', label: '系列编号', mono: true, value: item.seriesCode },
    { group: '绑定证书', label: '系列 ID', mono: true, value: item.certificateSeriesId, wide: true },
    { group: '绑定证书', label: '完整域名组合', value: item.seriesDomains, wide: true },
    { group: '目标配置', label: '目录别名', mono: true, value: item.targetAlias },
    { group: '目标配置', label: '自动部署', value: item.enabled ? '开启' : '关闭' },
    { group: '目标配置', label: '稳定读取路径', mono: true, value: item.runtimePath, wide: true },
    { group: '最近部署', label: '最近部署状态', status: item.status, value: { PENDING: '待部署', RUNNING: '执行中', SUCCESS: '成功', FAILED: '失败', NEEDS_BINDING: '需要绑定' }[item.status] },
    { group: '最近部署', label: '手动同步请求', value: item.manualSync ? '等待或执行中' : '无待处理请求' },
    { group: '最近部署', label: '最近部署证书', value: item.lastCertificateId ? `#${item.lastCertificateId}` : '尚未部署' },
    { group: '最近部署', label: '最近成功部署', value: formatDate(item.lastDeployedAt) },
    { group: '最近部署', label: '完整证书指纹', mono: true, value: item.lastFingerprint, wide: true },
    { group: '最近部署', label: '最近失败原因', error: true, value: item.lastError, wide: true },
    { group: '维护时间', label: '创建时间', value: formatDate(item.createdAt) },
    { group: '维护时间', label: '更新时间', value: formatDate(item.updatedAt) },
  ]
})
/** expandedId 是当前原位展开的记录编号，不复制记录以便跟随刷新。 */
const expandedId = ref<number | null>(null)
/** recordFields 完整展示指定记录的安全元数据和结果。 */
function recordFields(item: DeploymentRecord) {
  return [
    { group: '执行信息', label: '记录编号', value: item.id },
    { group: '执行信息', label: '目标编号', value: item.targetId || '目标已删除' },
    { group: '执行信息', label: '执行器类型', value: item.executorType === 'LOCAL' ? 'Server 本机' : '远程 Agent' },
    { group: '执行信息', label: '完整 Agent ID', mono: true, value: item.agentId },
    { group: '执行信息', label: '证书编号', value: item.certificateId },
    { group: '执行信息', label: '执行状态', status: item.status, value: { PENDING: '待部署', RUNNING: '执行中', SUCCESS: '成功', FAILED: '失败', NEEDS_BINDING: '需要绑定' }[item.status] },
    { group: '执行结果', label: '完整证书指纹', mono: true, value: item.fingerprint, wide: true },
    { group: '执行信息', label: '执行时间', value: formatDate(item.createdAt) },
    { group: '执行结果', label: '完整执行结果', value: item.message, wide: true },
  ]
}
</script>

<template>
  <div>
    <PageHeader
      eyebrow="文件交付"
      title="文件部署"
      description="把控制平面中的证书安全物化为 Nginx、EMQX 等业务应用可直接读取的标准 PEM 文件。"
    >
      <template #actions>
        <AppButton action="refresh" :loading="loading" @click="load()">刷新</AppButton>
        <AppButton action="create" tone="primary" @click="openCreate">新建目标</AppButton>
      </template>
    </PageHeader>

    <NAlert class="security-boundary" type="info" :show-icon="true" title="只负责文件，不执行任何命令">
      内部证书库中的私钥始终加密落盘；只有部署目录中的 <code>private-key.pem</code> 是供业务进程读取的明文运行副本，权限为 0640。请将部署目录以只读方式挂载给应用，并通过共享用户组授予读取权限。Acme4go 不连接 SSH、Docker Socket，也不执行 reload 命令。
    </NAlert>

    <section class="metric-strip">
      <div><span>部署目标</span><strong>{{ targets.length }}</strong><small>本机与远程消费者</small></div>
      <div><span>就绪目标</span><strong class="metric-good">{{ successfulCount }}</strong><small>current 已可读取</small></div>
      <div><span>异常目标</span><strong :class="{ 'metric-bad': failedCount }">{{ failedCount }}</strong><small>旧版本不会被覆盖</small></div>
      <div class="path-contract"><span>稳定路径契约</span><code>&lt;部署根目录&gt;/&lt;别名&gt;/current/{fullchain.pem,private-key.pem}</code></div>
    </section>

    <section class="deployment-layout">
      <NCard class="content-card" :bordered="true" content-style="padding: 0">
        <template #header><div class="card-title"><h2>部署目标</h2><p>新证书签发后由本机或指定 Agent 自动更新</p></div></template>
        <div v-if="loading" class="loading-block"><NSpin size="small" />正在检查部署期望状态…</div>
        <div v-else-if="targets.length" class="target-list">
          <article v-for="target in targets" :key="target.id" class="target-card">
            <div class="target-icon"><NIcon :size="21" :component="Folder" /><span class="target-status-dot" :class="`state-${target.status.toLowerCase()}`" /></div>
            <div class="target-main">
              <div class="target-title"><strong>{{ target.name }}</strong><StatusBadge :status="target.status" /></div>
              <span>{{ target.seriesCode || '待绑定' }} · {{ target.seriesName || target.commonName }} · {{ target.executorName }} · <code>{{ target.targetAlias }}</code></span>
              <button class="runtime-path" type="button" title="复制稳定读取路径" @click="copyPath(target.runtimePath)">
                <code>{{ target.runtimePath }}</code><NIcon :size="14" :component="Copy" />
              </button>
              <small v-if="target.manualSync">{{ target.status === 'RUNNING' ? 'Agent 已开始同步，等待执行回执。' : '等待 Agent 拉取；离线时请求保留。' }}</small>
              <small v-if="target.lastError" class="target-error">{{ target.lastError }}</small>
            </div>
            <div class="target-version">
              <span>当前证书</span>
              <strong>#{{ target.lastCertificateId || '—' }}</strong>
              <small class="mono">{{ shortId(target.lastFingerprint, 12) }}</small>
            </div>
            <div class="target-time"><span>最后切换</span><strong>{{ formatDate(target.lastDeployedAt) }}</strong><small>{{ target.enabled ? '自动部署已启用' : '自动部署已暂停' }}</small></div>
            <div class="action-group target-actions">
              <AppButton action="detail" size="small" :aria-label="`查看文件部署目标 ${target.name} 详情`" @click="detailId = target.id">详情</AppButton>
              <AppButton action="deploy" tone="accent" v-if="target.certificateSeriesId && (target.executorType === 'AGENT' || systemInfo?.localDeploymentEnabled)" size="small" :loading="operatingTargetId === target.id" :disabled="target.manualSync || operatingTargetId !== null" @click="deploy(target)">
                {{ target.manualSync ? target.status === 'RUNNING' ? '同步执行中' : '等待 Agent' : '立即同步' }}
              </AppButton>
              <AppButton action="edit" size="small" @click="openEdit(target)">编辑</AppButton>
              <NPopconfirm positive-text="删除配置" negative-text="取消" @positive-click="remove(target)">
                <template #trigger><AppButton action="delete" tone="danger" size="small">删除</AppButton></template>
                只删除管理配置，不删除磁盘上的 current 和历史版本文件。
              </NPopconfirm>
            </div>
          </article>
        </div>
        <EmptyState v-else title="还没有文件部署目标" description="选择一个证书系列和目录别名，让本机 Nginx、EMQX 或其他服务读取稳定的 current 路径。" symbol="↳">
          <AppButton action="create" tone="primary" @click="openCreate">新建部署目标</AppButton>
        </EmptyState>
      </NCard>

      <NCard class="content-card records-card" :bordered="true" content-style="padding: 0">
        <template #header><div class="card-title"><h2>部署记录</h2><p>最近 {{ Math.min(records.length, 16) }} 次文件切换</p></div></template>
        <div v-if="records.length" class="record-list">
          <div v-for="record in records.slice(0, 16)" :key="record.id" class="record-row">
            <span class="record-icon" :class="`record-${record.status.toLowerCase()}`"><NIcon :size="17" :component="FileCertificate" /></span>
            <div><strong>目标 #{{ record.targetId || '已删除' }} · 证书 #{{ record.certificateId || '—' }}</strong><small>{{ record.message }}</small><time>{{ formatDate(record.createdAt) }}</time></div>
            <div class="record-actions"><StatusBadge :status="record.status" /><AppButton action="expand" size="small" :aria-label="`${expandedId === record.id ? '收起' : '展开'}文件部署记录 #${record.id} 详情`" :aria-expanded="expandedId === record.id" :aria-controls="`deployment-record-${record.id}`" @click="expandedId = expandedId === record.id ? null : record.id">{{ expandedId === record.id ? '收起' : '展开详情' }}</AppButton></div>
            <DetailFields v-if="expandedId === record.id" :id="`deployment-record-${record.id}`" class="record-details" :fields="recordFields(record)" />
          </div>
        </div>
        <EmptyState v-else title="暂无部署记录" description="创建目标并取得匹配证书后，这里会记录每次原子切换结果。" symbol="∅" />
      </NCard>
    </section>

    <AppModal :open="!!detailItem" title="文件部署目标详情" description="查看完整信息；修改配置请使用列表中的编辑操作。" width="large" @close="detailId = null">
      <DetailFields v-if="detailItem" :fields="detailFields" />
      <template #footer><AppButton action="close" @click="detailId = null">关闭</AppButton></template>
    </AppModal>

    <AppModal
      :open="modalOpen"
      :title="editingTarget ? '编辑部署目标' : '新建部署目标'"
      description="一个目标绑定一个稳定证书系列，续期版本会自动沿用同一 current 路径。"
      @close="modalOpen = false"
    >
      <NAlert class="modal-tip" type="warning" :show-icon="true">
        目录别名创建后不可修改；切换证书系列会更新目标期望版本。关闭自动部署后仍可单次同步。本机直接执行；远程由 Agent 下次拉取后执行，离线时请求保留。修改配置会取消此前未完成的手动请求。
      </NAlert>
      <NForm id="deployment-form" :model="form" label-placement="top" @submit.prevent="save">
        <NFormItem label="执行节点" :show-feedback="false">
          <AppSelect v-model:value="form.executorKey" :options="executorOptions" filterable show-details placeholder="请选择本机执行器或已注册 Agent" :input-props="{ 'aria-label': '选择执行节点' }" />
        </NFormItem>
        <NFormItem label="目标名称" :show-feedback="false">
          <NInput v-model:value="form.name" placeholder="例如：公网 Nginx" maxlength="64"><template #prefix><NIcon :component="Folder" /></template></NInput>
        </NFormItem>
        <NFormItem label="证书系列" :show-feedback="false">
          <LocalCertificatePicker v-model="form.certificateSeriesId" :series="series" />
        </NFormItem>
        <NFormItem label="目录别名" :show-feedback="false">
          <NInput v-model:value="form.targetAlias" :disabled="Boolean(editingTarget)" placeholder="nginx-public" maxlength="64"><template #prefix><NIcon :component="Lock" /></template></NInput>
        </NFormItem>
        <div class="switch-row"><div><strong>自动部署</strong><small>匹配的新证书签发或同步到本节点后立即原子切换</small></div><NSwitch v-model:value="form.enabled" /></div>
      </NForm>
      <template #footer><AppButton action="close" @click="modalOpen = false">取消</AppButton><AppButton action="save" tone="primary" attr-type="submit" form="deployment-form" :loading="saving">保存目标</AppButton></template>
    </AppModal>
  </div>
</template>

<style scoped>
.record-actions { display: flex; flex-direction: column; align-items: flex-end; gap: 8px; }
.record-details { grid-column: 1 / -1; padding: 16px; margin-bottom: 12px; border: 1px solid var(--line-soft); border-radius: 8px; background: var(--canvas); }
.security-boundary { margin-bottom: 18px; }.security-boundary code { font: 10px var(--mono); }
.metric-strip { display: grid; grid-template-columns: 150px 150px 150px minmax(300px, 1fr); margin-bottom: 18px; background: var(--panel); border: 1px solid var(--line); border-radius: 12px; box-shadow: var(--shadow); overflow: hidden; }.metric-strip > div { min-height: 94px; padding: 17px 20px; border-right: 1px solid var(--line-soft); }.metric-strip > div:last-child { border-right: 0; }.metric-strip span, .metric-strip small { display: block; color: #777a71; font-size: 11px; line-height: 1.45; }.metric-strip span { font-weight: 600; }.metric-strip strong { display: block; margin: 5px 0 3px; color: #353831; font-size: 26px; font-weight: 700; line-height: 1.25; }.metric-strip .metric-good { color: #587361; }.metric-strip .metric-bad { color: #ad5753; }.metric-strip .path-contract { display: flex; flex-direction: column; justify-content: center; background: var(--sand); }.path-contract code { margin-top: 9px; color: #496457; font: 11px/1.6 var(--mono); overflow-wrap: anywhere; }
.deployment-layout { display: grid; grid-template-columns: minmax(0, 1.35fr) minmax(330px, .65fr); gap: 18px; align-items: start; }.content-card { box-shadow: var(--shadow); }.card-title h2 { margin: 0; color: #34362f; font-size: 16px; font-weight: 700; line-height: 1.45; letter-spacing: -.1px; }.card-title p { margin: 4px 0 0; color: #777970; font-size: 12px; line-height: 1.55; }
.target-list { padding: 8px 20px; }.target-card { display: grid; grid-template-columns: 48px minmax(0, 1fr) 92px 130px; align-items: center; gap: 12px 15px; padding-top: 18px; min-height: 124px; border-bottom: 1px solid var(--line-soft); }.target-card:last-child { border-bottom: 0; }.target-icon { position: relative; width: 44px; height: 44px; display: grid; place-items: center; color: #597064; background: var(--sage); border-radius: 9px; }.target-icon :deep(.n-icon) { position: static; display: inline-flex; width: 21px; height: 21px; flex: 0 0 auto; }.target-status-dot { position: absolute; right: -1px; bottom: -1px; width: 10px; height: 10px; border: 2px solid #fffefa; border-radius: 50%; background: #b48359; }.target-icon .state-success { background: #6a8575; }.target-icon .state-failed { background: #b65e59; }
.target-title { display: flex; align-items: center; gap: 8px; }.target-title > strong { color: #3d4038; font-size: 14px; line-height: 1.45; }.target-main > span { display: block; margin-top: 6px; color: #74766d; font-size: 11px; line-height: 1.5; }.target-main > span code { color: #526b5e; font: 10px var(--mono); }.runtime-path { max-width: 100%; display: flex; align-items: center; gap: 6px; margin: 8px 0 0; padding: 0; color: #6f746c; background: none; border: 0; cursor: pointer; }.runtime-path code { min-width: 0; overflow: hidden; font: 10px var(--mono); text-overflow: ellipsis; white-space: nowrap; }.runtime-path:hover { color: #4d6859; }.target-error { display: block; margin-top: 7px; color: #a85350; font-size: 10px; line-height: 1.5; }.target-version span, .target-time span { display: block; margin-bottom: 6px; color: #85877e; font-size: 10px; }.target-version strong { display: block; color: #454840; font-size: 15px; font-weight: 700; }.target-version small, .target-time small { display: block; margin-top: 5px; color: #8e9087; font-size: 10px; line-height: 1.45; }.target-time strong { display: block; color: #60635b; font-size: 10px; line-height: 1.45; }.target-actions { grid-column: 2 / -1; padding-bottom: 16px; }
.record-list { padding: 6px 18px; }.record-row { display: grid; grid-template-columns: 34px minmax(0, 1fr) auto; align-items: center; gap: 10px; min-height: 76px; border-bottom: 1px solid var(--line-soft); }.record-row:last-child { border-bottom: 0; }.record-icon { width: 32px; height: 32px; display: grid; place-items: center; color: #597064; background: var(--sage); border-radius: 8px; }.record-failed { color: #aa5552; background: #f8eceb; }.record-row strong, .record-row small, .record-row time { display: block; }.record-row strong { color: #454840; font-size: 11px; line-height: 1.45; }.record-row small { overflow-wrap: anywhere; margin-top: 4px; color: #74766e; font-size: 10px; line-height: 1.5; }.record-row time { margin-top: 4px; color: #8f9189; font-size: 10px; line-height: 1.45; }
.modal-tip { margin-bottom: 20px; }.switch-row { display: flex; align-items: center; justify-content: space-between; gap: 20px; padding: 14px 15px; background: #f6f4ef; border: 1px solid var(--line); border-radius: 10px; }.switch-row strong, .switch-row small { display: block; }.switch-row strong { color: #454840; font-size: 12px; }.switch-row small { margin-top: 5px; color: #7e8077; font-size: 10px; line-height: 1.5; }
@media (max-width: 1240px) { .deployment-layout { grid-template-columns: 1fr; }.target-card { grid-template-columns: 48px minmax(0, 1fr) 90px 125px; } }
@media (max-width: 850px) { .metric-strip { grid-template-columns: repeat(3, 1fr); }.path-contract { grid-column: 1 / -1; border-top: 1px solid var(--line-soft); }.target-card { grid-template-columns: 44px minmax(0, 1fr); padding: 16px 0; }.target-version, .target-time { display: none; }.target-actions { grid-column: 2 / -1; } }
@media (max-width: 560px) { .metric-strip { grid-template-columns: repeat(3, 1fr); }.metric-strip > div { min-height: 82px; padding: 14px 12px; }.metric-strip strong { font-size: 20px; }.target-list { padding-inline: 14px; }.target-card { grid-template-columns: 40px minmax(0, 1fr); }.target-actions { grid-column: 1 / -1; }.runtime-path { max-width: calc(100vw - 125px); } }
</style>
