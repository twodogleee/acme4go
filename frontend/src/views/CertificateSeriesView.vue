<script setup lang="ts">
/**
 * 证书系列页面，显式维护自动续期、联系邮箱、DNS 凭据与启停状态。
 *
 * @author Andersen
 */
import AppButton from '@/components/AppButton.vue'
import { computed, onMounted, reactive, ref } from 'vue'
import { NAlert, NCard, NCheckbox, NForm, NFormItem, NIcon, NInput, NSpin, NTable } from 'naive-ui'
import { Versions } from '@vicons/tabler'
import { api } from '@/api'
import type { CertificateSeries, CertificateSeriesInput, DnsCredential } from '@/types'
import CredentialPicker from '@/components/CredentialPicker.vue'
import AppModal from '@/components/AppModal.vue'
import DetailFields from '@/components/DetailFields.vue'
import EmptyState from '@/components/EmptyState.vue'
import PageHeader from '@/components/PageHeader.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { formatDate } from '@/utils/format'
import { useToast } from '@/composables/useToast'

/** seriesValues 是全部稳定证书系列。 */
const seriesValues = ref<CertificateSeries[]>([])
/** credentials 是可供系列选择的 DNS 凭据。 */
const credentials = ref<DnsCredential[]>([])
/** loading 表示列表是否正在加载。 */
const loading = ref(true)
/** saving 表示系列配置是否正在提交。 */
const saving = ref(false)
/** editing 是当前编辑的证书系列。 */
const editing = ref<CertificateSeries | null>(null)
/** toast 提供统一页面通知。 */
const toast = useToast()
/** form 是系列长期配置编辑表单。 */
const form = reactive<CertificateSeriesInput>({ name: '', email: '', credentialId: 0, autoRenew: true, enabled: true })
/** load 并行刷新系列和脱敏凭据列表。 */
async function load() {
  loading.value = true
  try {
    ;[seriesValues.value, credentials.value] = await Promise.all([api.listCertificateSeries(), api.listDnsCredentials()])
  } catch (error) {
    toast.error('证书系列加载失败', error instanceof Error ? error.message : undefined)
  } finally {
    loading.value = false
  }
}

/** openEdit 把当前系列长期配置映射到编辑表单。 */
function openEdit(series: CertificateSeries) {
  editing.value = series
  Object.assign(form, {
    name: series.name,
    email: series.email,
    credentialId: series.credentialId,
    autoRenew: series.autoRenew,
    enabled: series.enabled,
  })
}

/** save 校验并显式更新系列配置。 */
async function save() {
  if (!editing.value || !form.name.trim() || !form.email.trim() || !form.credentialId) {
    toast.error('请完整填写系列名称、联系邮箱和 DNS 凭据')
    return
  }
  saving.value = true
  try {
    await api.updateCertificateSeries(editing.value.id, { ...form })
    toast.success('证书系列配置已更新')
    editing.value = null
    await load()
  } catch (error) {
    toast.error('系列配置保存失败', error instanceof Error ? error.message : undefined)
  } finally {
    saving.value = false
  }
}

onMounted(load)
/** detailId 标识正在只读查看的证书系列，刷新后自动使用当前数据。 */
const detailId = ref<string | null>(null)
/** detailItem 是当前列表中的最新详情对象。 */
const detailItem = computed(() => seriesValues.value.find((item) => item.id === detailId.value) || null)
/** detailFields 显式选择可展示的业务元数据，不访问秘密材料。 */
const detailFields = computed(() => {
  /** item 是当前详情对象。 */
  const item = detailItem.value
  if (!item) return []
  return [
    { group: '系列信息', label: '系列名称', value: item.name },
    { group: '系列信息', label: '系列编号', mono: true, value: item.code },
    { group: '系列信息', label: '系列 ID', mono: true, value: item.id, wide: true },
    { group: '系列信息', label: '完整域名组合', value: item.domains, wide: true },
    { group: '系列信息', label: 'CA 环境', value: item.caEnvironment === 'LETS_ENCRYPT_PRODUCTION' ? 'Let’s Encrypt 生产环境' : 'Let’s Encrypt 测试环境' },
    { group: '续期配置', label: '联系邮箱', value: item.email },
    { group: '续期配置', label: 'DNS 凭据', value: `凭据 #${item.credentialId}` },
    { group: '系列信息', label: '系列状态', status: item.enabled ? 'SUCCESS' : 'DISABLED', value: item.enabled ? '已启用' : '已停用' },
    { group: '续期配置', label: '自动续期', value: item.autoRenew ? '开启' : '关闭' },
    { group: '当前证书', label: '当前证书', value: item.currentCertificateId ? `#${item.currentCertificateId} · v${item.currentVersion}` : '尚未签发' },
    { group: '当前证书', label: '当前证书状态', status: item.currentStatus || undefined, value: item.currentStatus ? ({ VALID: '有效', EXPIRING: '即将到期', EXPIRED: '已过期' } as Record<string, string>)[item.currentStatus] || item.currentStatus : '尚未签发' },
    { group: '当前证书', label: '当前证书到期时间', value: formatDate(item.currentNotAfter) },
    { group: '续期配置', label: '配置修订', value: item.revision },
    { group: '维护时间', label: '创建时间', value: formatDate(item.createdAt) },
    { group: '维护时间', label: '更新时间', value: formatDate(item.updatedAt) },
  ]
})
</script>

<template>
  <div>
    <PageHeader eyebrow="生命周期配置" title="证书系列" description="域名组合与 CA 环境保持不变；续期邮箱、DNS 凭据、自动续期和启停状态在此显式维护。">
      <template #actions><AppButton action="refresh" :loading="loading" @click="load">刷新</AppButton></template>
    </PageHeader>

    <NAlert class="series-tip" type="info" title="签发记录与长期配置已解耦">
      重试失败订单会使用这里保存的最新配置；新建相同域名组合的订单不会再覆盖系列设置。
    </NAlert>

    <NCard class="content-card" :bordered="true" content-style="padding: 0">
      <template #header><div class="card-title"><h2>系列清单</h2><p>共 {{ seriesValues.length }} 个稳定域名组合</p></div></template>
      <div v-if="loading" class="loading-block"><NSpin size="small" />正在加载证书系列…</div>
      <div v-else-if="seriesValues.length" class="table-wrap">
        <NTable class="data-table" :bordered="false" :single-line="true" size="small">
          <thead><tr><th>系列</th><th>域名组合</th><th>当前版本</th><th>续期配置</th><th>状态</th><th class="action-cell">操作</th></tr></thead>
          <tbody>
            <tr v-for="series in seriesValues" :key="series.id">
              <td><span class="series-name"><i><NIcon :component="Versions" /></i><span><b>{{ series.name }}</b><small>{{ series.code }}</small></span></span></td>
              <td><span class="table-primary">{{ series.domains[0] }}</span><span class="table-secondary">{{ series.domains.length }} 个域名 · {{ series.caEnvironment === 'LETS_ENCRYPT_PRODUCTION' ? '生产' : '测试' }}</span></td>
              <td><span class="table-primary">{{ series.currentVersion ? `v${series.currentVersion}` : '尚未签发' }}</span><span class="table-secondary">{{ formatDate(series.currentNotAfter, false) }}</span></td>
              <td><span class="table-primary">{{ series.email }}</span><span class="table-secondary">凭据 #{{ series.credentialId }} · {{ series.autoRenew ? '自动续期' : '手动续期' }}</span></td>
              <td><StatusBadge :status="series.enabled ? (series.currentStatus || 'PENDING') : 'OFFLINE'" :label="series.enabled ? (series.autoRenew ? '已启用' : '暂停自动续期') : '已停用'" /></td>
              <td class="action-cell"><div class="action-group"><AppButton action="detail" size="small" :aria-label="`查看证书系列 ${series.name} 详情`" @click="detailId = series.id">详情</AppButton><AppButton action="edit" size="small" @click="openEdit(series)">编辑</AppButton></div></td>
            </tr>
          </tbody>
        </NTable>
      </div>
      <EmptyState v-else title="还没有证书系列" description="提交第一笔证书申请后，会自动建立稳定的证书系列。" symbol="▱" />
    </NCard>

    <AppModal :open="!!detailItem" title="证书系列详情" description="查看完整信息；修改配置请使用列表中的编辑操作。" width="large" @close="detailId = null">
      <DetailFields v-if="detailItem" :fields="detailFields" />
      <template #footer><AppButton action="close" @click="detailId = null">关闭</AppButton></template>
    </AppModal>

    <AppModal :open="Boolean(editing)" title="编辑证书系列" description="域名组合和 CA 环境不可变，如需不同组合请新建证书申请。" @close="editing = null">
      <NForm id="series-form" class="series-form" :model="form" label-placement="top" @submit.prevent="save">
        <NFormItem label="系列名称" :show-feedback="false"><NInput v-model:value="form.name" maxlength="100" /></NFormItem>
        <NFormItem label="ACME 联系邮箱" :show-feedback="false"><NInput v-model:value="form.email" placeholder="ops@example.com" /></NFormItem>
        <NFormItem label="DNS 凭据" :show-feedback="false"><CredentialPicker v-model="form.credentialId" :credentials="credentials" purpose="DNS" /></NFormItem>
        <div class="check-list">
          <NCheckbox v-model:checked="form.autoRenew">进入续期窗口后自动创建新订单</NCheckbox>
          <NCheckbox v-model:checked="form.enabled">启用系列，允许签发和部署最新证书</NCheckbox>
        </div>
      </NForm>
      <template #footer><AppButton action="close" @click="editing = null">取消</AppButton><AppButton action="save" tone="primary" attr-type="submit" form="series-form" :loading="saving">保存配置</AppButton></template>
    </AppModal>
  </div>
</template>

<style scoped>
.series-tip { margin-bottom: 18px; }.content-card { box-shadow: var(--shadow); }.card-title h2 { margin: 0; font-size: 15px; }.card-title p { margin: 4px 0 0; color: var(--muted); font-size: 11px; }.series-name { display: flex; align-items: center; gap: 10px; }.series-name i { width: 32px; height: 32px; display: grid; place-items: center; color: #607668; background: var(--sage); border-radius: 8px; }.series-name b, .series-name small { display: block; }.series-name b { color: #41433c; font-size: 12px; }.series-name small { margin-top: 3px; color: #8e8c83; font: 9px ui-monospace, monospace; }.series-form { display: grid; }.check-list { display: grid; gap: 12px; padding: 4px 0; }
</style>
