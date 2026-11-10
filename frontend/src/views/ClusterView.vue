<script setup lang="ts">
/**
 * Agent 管理页面，展示节点状态并提供一次性注册与身份撤销操作。
 *
 * @author Andersen
 */
import AppButton from '@/components/AppButton.vue'
import { computed, onMounted, reactive, ref } from 'vue'
import { NAlert, NCard, NForm, NFormItem, NIcon, NInput, NPopconfirm, NSpin } from 'naive-ui'
import { Server, ShieldLock } from '@vicons/tabler'
import { api } from '@/api'
import type { AgentNode, Enrollment } from '@/types'
import AppModal from '@/components/AppModal.vue'
import DetailFields from '@/components/DetailFields.vue'
import EmptyState from '@/components/EmptyState.vue'
import PageHeader from '@/components/PageHeader.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { formatDate, shortId } from '@/utils/format'
import { useToast } from '@/composables/useToast'

/** agents 是已注册 Agent 列表。 */
const agents = ref<AgentNode[]>([])
/** loading 表示节点列表是否正在加载。 */
const loading = ref(true)
/** creating 表示注册信息是否正在生成。 */
const creating = ref(false)
/** operatingId 是当前执行撤销操作的 Agent 标识。 */
const operatingId = ref<string | null>(null)
/** modalOpen 表示注册弹窗是否打开。 */
const modalOpen = ref(false)
/** enrollment 是最近生成的一次性注册信息。 */
const enrollment = ref<Enrollment | null>(null)
/** form 是 Agent 注册名称表单。 */
const form = reactive({ name: '' })
/** toast 提供统一页面通知。 */
const toast = useToast()

/** onlineCount 是当前在线 Agent 数量。 */
const onlineCount = computed(() => agents.value.filter((agent) => agent.status === 'ONLINE').length)
/** targetCount 是所有 Agent 绑定的部署目标总数。 */
const targetCount = computed(() => agents.value.reduce((total, agent) => total + agent.targetCount, 0))

/** load 刷新 Agent 状态列表。 */
async function load() {
  loading.value = true
  try {
    agents.value = await api.listAgents()
  } catch (error) {
    toast.error('Agent 状态加载失败', error instanceof Error ? error.message : undefined)
  } finally {
    loading.value = false
  }
}

/** openEnrollment 重置表单并打开注册弹窗。 */
function openEnrollment() {
  form.name = ''
  enrollment.value = null
  modalOpen.value = true
}

/** createEnrollment 校验名称并生成一次性 Join URL。 */
async function createEnrollment() {
  if (!form.name.trim()) {
    toast.error('请输入 Agent 名称')
    return
  }
  creating.value = true
  try {
    enrollment.value = await api.createEnrollment(form.name.trim())
    toast.success('一次性 Join URL 已生成', '请在 10 分钟内粘贴到 Agent 配置中。')
  } catch (error) {
    toast.error('生成注册信息失败', error instanceof Error ? error.message : undefined)
  } finally {
    creating.value = false
  }
}

/** copyJoinUrl 将当前 Join URL 写入系统剪贴板。 */
async function copyJoinUrl() {
  if (!enrollment.value) return
  await navigator.clipboard.writeText(enrollment.value.joinUrl)
  toast.success('Join URL 已复制')
}

/** revoke 撤销指定 Agent 身份并刷新列表。 */
async function revoke(agent: AgentNode) {
  operatingId.value = agent.id
  try {
    await api.revokeAgent(agent.id)
    toast.success('Agent 已撤销', `${agent.name} 将无法继续拉取证书。`)
    await load()
  } catch (error) {
    toast.error('撤销 Agent 失败', error instanceof Error ? error.message : undefined)
  } finally {
    operatingId.value = null
  }
}

onMounted(load)
/** detailId 标识正在只读查看的部署节点，刷新后自动使用当前数据。 */
const detailId = ref<string | null>(null)
/** detailItem 是当前列表中的最新详情对象。 */
const detailItem = computed(() => agents.value.find((item) => item.id === detailId.value) || null)
/** detailFields 显式选择可展示的业务元数据，不访问秘密材料。 */
const detailFields = computed(() => {
  /** item 是当前详情对象。 */
  const item = detailItem.value
  if (!item) return []
  return [
    { group: '节点身份', label: '节点名称', value: item.name },
    { group: '节点身份', label: '节点状态', status: item.status, value: item.status === 'ONLINE' ? '在线' : item.status === 'REVOKED' ? '已撤销' : '离线' },
    { group: '节点身份', label: '完整 Agent ID', mono: true, value: item.id, wide: true },
    { group: '节点身份', label: '允许工作', value: item.enabled ? '是' : '否' },
    { group: '运行信息', label: '平台', value: item.platform },
    { group: '运行信息', label: '应用版本', value: item.version },
    { group: '运行信息', label: '部署目标数', value: item.targetCount },
    { group: '运行信息', label: '最近在线', value: formatDate(item.lastSeenAt) },
    { group: '身份与时间', label: '身份到期时间', value: formatDate(item.certificateExpiresAt) },
    { group: '身份与时间', label: '注册时间', value: formatDate(item.createdAt) },
    { group: '身份与时间', label: '更新时间', value: formatDate(item.updatedAt) },
  ]
})
</script>

<template>
  <div>
    <PageHeader eyebrow="节点管理" title="部署 Agent" description="Agent 仅主动连接控制平面，无需开放入站端口；证书通过 mTLS 通道按期望状态拉取。">
      <template #actions>
        <AppButton action="refresh" :loading="loading" @click="load">刷新</AppButton>
        <AppButton action="create" tone="primary" @click="openEnrollment">注册 Agent</AppButton>
      </template>
    </PageHeader>

    <section class="agent-hero">
      <div class="hero-icon"><NIcon :component="ShieldLock" /></div>
      <div class="hero-copy"><span>通信模型</span><h2>Pull + mTLS</h2><p>一次性注册 · 30 秒对账 · 身份证书自动续签</p></div>
      <div class="hero-metric"><span>在线 Agent</span><strong>{{ onlineCount }}<small>/{{ agents.length }}</small></strong></div>
      <div class="hero-metric"><span>远程目标</span><strong>{{ targetCount }}</strong></div>
    </section>

    <NCard class="content-card" :bordered="true" content-style="padding: 0">
      <template #header><div class="card-title"><h2>已注册节点</h2><p>离线判定窗口为 2 分钟，撤销后立即拒绝 mTLS 身份</p></div></template>
      <div v-if="loading" class="loading-block"><NSpin size="small" />正在检查 Agent 心跳…</div>
      <div v-else-if="agents.length" class="agent-list">
        <article v-for="agent in agents" :key="agent.id" class="agent-card">
          <div class="agent-icon" :class="`agent-${agent.status.toLowerCase()}`"><NIcon :size="20" :component="Server" /><span class="agent-status-dot" /></div>
          <div class="agent-main"><strong>{{ agent.name }}</strong><small class="mono">{{ shortId(agent.id, 18) }}</small><small>{{ agent.platform || '等待首次心跳' }} · {{ agent.version || '未知版本' }}</small></div>
          <StatusBadge :status="agent.status" />
          <div class="agent-metric"><span>部署目标</span><strong>{{ agent.targetCount }}</strong></div>
          <div class="agent-seen"><span>最后在线</span><strong>{{ formatDate(agent.lastSeenAt) }}</strong><small>身份到期：{{ formatDate(agent.certificateExpiresAt, false) }}</small></div>
          <div class="action-group agent-actions"><AppButton action="detail" size="small" :aria-label="`查看 Agent ${agent.name} 详情`" @click="detailId = agent.id">详情</AppButton>
          <NPopconfirm v-if="agent.enabled" positive-text="确认撤销" negative-text="取消" @positive-click="revoke(agent)">
            <template #trigger><AppButton action="delete" tone="danger" size="small" :loading="operatingId === agent.id">撤销</AppButton></template>
            撤销后该节点会立即失去证书下载权限，已有本地文件不会被删除。
          </NPopconfirm></div>
        </article>
      </div>
      <EmptyState v-else title="还没有 Agent" description="生成一次性 Join URL，在目标服务器启动同一个 acme4go 镜像即可。" symbol="◎" />
    </NCard>

    <AppModal :open="!!detailItem" title="Agent 详情" description="查看节点身份、运行状态与注册时间。" width="large" @close="detailId = null">
      <DetailFields v-if="detailItem" :fields="detailFields" />
      <template #footer><AppButton action="close" @click="detailId = null">关闭</AppButton></template>
    </AppModal>

    <AppModal :open="modalOpen" title="注册部署 Agent" description="注册信息只展示在本次窗口中，并在 10 分钟后或使用一次后失效。" @close="modalOpen = false">
      <template v-if="!enrollment">
        <NAlert class="modal-tip" type="info" :show-icon="true">目标服务器不需要开放任何端口，只需能够访问控制平面的 Agent HTTPS 地址。</NAlert>
        <NForm id="enrollment-form" :model="form" label-placement="top" @submit.prevent="createEnrollment">
          <NFormItem label="Agent 名称" :show-feedback="false"><NInput v-model:value="form.name" placeholder="例如：上海生产入口" maxlength="100"><template #prefix><NIcon :component="Server" /></template></NInput></NFormItem>
        </NForm>
      </template>
      <template v-else>
        <NAlert class="modal-tip" type="warning" :show-icon="true" title="请立即保存">有效期至 {{ formatDate(enrollment.expiresAt) }}，关闭后无法再次查看。</NAlert>
        <label class="join-label">ACME_AGENT_JOIN_URL</label>
        <NInput :value="enrollment.joinUrl" type="textarea" readonly :autosize="{ minRows: 7, maxRows: 11 }" class="mono" />
        <AppButton action="copy" class="copy-button" block @click="copyJoinUrl">复制 Join URL</AppButton>
      </template>
      <template #footer>
        <AppButton action="close" @click="modalOpen = false">{{ enrollment ? '完成' : '取消' }}</AppButton>
        <AppButton action="create" tone="primary" v-if="!enrollment" attr-type="submit" form="enrollment-form" :loading="creating">生成注册信息</AppButton>
      </template>
    </AppModal>
  </div>
</template>

<style scoped>
.agent-actions { flex-wrap: nowrap; }
@media (max-width: 620px) { .agent-actions { grid-column: 2 / -1; } }
.agent-hero { min-height: 108px; display: flex; align-items: center; gap: 18px; margin-bottom: 18px; padding: 18px 22px; background: var(--panel); border: 1px solid var(--line); border-radius: 12px; box-shadow: var(--shadow); }.hero-icon { width: 52px; height: 52px; display: grid; place-items: center; flex: 0 0 auto; color: #536d5f; background: var(--sage); border-radius: 10px; font-size: 23px; }.hero-copy { flex: 1; }.hero-copy span, .hero-metric span { color: #8b897f; font-size: 10px; }.hero-copy h2 { margin: 5px 0; color: #363831; font-size: 17px; }.hero-copy p { margin: 0; color: #918e84; font-size: 10px; }.hero-metric { min-width: 105px; padding-left: 22px; border-left: 1px solid var(--line-soft); }.hero-metric strong { display: block; margin-top: 5px; color: #465f51; font-size: 21px; }.hero-metric small { color: #918e84; font-size: 10px; }.content-card { box-shadow: var(--shadow); }.card-title h2 { margin: 0; font-size: 15px; }.card-title p { margin: 4px 0 0; color: var(--muted); font-size: 11px; }
.agent-list { padding: 8px 20px; }.agent-card { display: grid; grid-template-columns: 46px minmax(210px, 1fr) auto 80px 210px auto; align-items: center; gap: 15px; min-height: 94px; border-bottom: 1px solid var(--line-soft); }.agent-card:last-child { border-bottom: 0; }.agent-icon { position: relative; width: 42px; height: 42px; display: grid; place-items: center; color: #5a7163; background: var(--sage); border-radius: 9px; }.agent-icon :deep(.n-icon) { position: static; display: inline-flex; width: 20px; height: 20px; flex: 0 0 auto; }.agent-status-dot { position: absolute; right: -1px; bottom: -1px; width: 10px; height: 10px; border: 2px solid #fffefa; border-radius: 50%; background: #aaa79d; }.agent-online .agent-status-dot { background: #6b8576; }.agent-revoked .agent-status-dot { background: #b65e59; }.agent-main strong, .agent-main small, .agent-seen span, .agent-seen strong, .agent-seen small { display: block; }.agent-main strong { color: #41433c; font-size: 12px; }.agent-main small { margin-top: 4px; color: #908e84; font-size: 9px; }.agent-metric span { color: #8f8c83; font-size: 9px; }.agent-metric strong { display: block; margin-top: 5px; font-size: 17px; }.agent-seen span { margin-bottom: 5px; color: #8f8c83; font-size: 9px; }.agent-seen strong { color: #6b6c64; font-size: 9px; }.agent-seen small { margin-top: 5px; color: #96938a; font-size: 8px; }.modal-tip { margin-bottom: 20px; }.join-label { display: block; margin-bottom: 8px; color: #63645d; font-size: 11px; font-weight: 650; }.copy-button { margin-top: 12px; }
@media (max-width: 960px) { .agent-card { grid-template-columns: 44px minmax(170px, 1fr) auto 70px auto; }.agent-seen { display: none; } }
@media (max-width: 620px) { .hero-metric { display: none; }.agent-card { grid-template-columns: 44px 1fr auto; padding: 14px 0; }.agent-metric { display: none; }.agent-card > :deep(.n-button), .agent-card > :deep(.n-popconfirm) { grid-column: 2 / -1; } }
</style>
