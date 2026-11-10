<script setup lang="ts">
/**
 * 统一凭据页面，共享加密密钥并按用途管理 DNS 验证和云部署授权。
 *
 * @author Andersen
 */
import AppButton from '@/components/AppButton.vue'
import { computed, onMounted, reactive, ref } from 'vue'
import { NAlert, NCard, NCheckbox, NForm, NFormItem, NIcon, NInput, NPopconfirm, NSpin, NTag, NSpace } from 'naive-ui'
import { CloudLock, Key } from '@vicons/tabler'
import { api } from '@/api'
import type { Credential, CredentialInput } from '@/types/credentials'
import { credentialsApi } from '@/api/credentials'
import { useLatestRequest } from '@/composables/useLatestRequest'
import CloudCertificatePicker from '@/components/cloud/CloudCertificatePicker.vue'
import AppSelect from '@/components/AppSelect.vue'
import AppModal from '@/components/AppModal.vue'
import DetailFields from '@/components/DetailFields.vue'
import EmptyState from '@/components/EmptyState.vue'
import PageHeader from '@/components/PageHeader.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import { credentialZones, parseCredentialZones } from '@/utils/credentialZones'
import { formatDate } from '@/utils/format'
import { useToast } from '@/composables/useToast'
import { useDiscardChanges } from '@/composables/useDiscardChanges'

/** credentials 是全部用途的脱敏凭据列表。 */
const credentials = ref<Credential[]>([])
/** loading 表示凭据列表是否正在加载。 */
const loader = useLatestRequest()
/** loading 反映最新请求的加载状态。 */
const loading = loader.loading
/** saving 表示凭据表单是否正在提交。 */
const saving = ref(false)
/** testingId 是当前正在测试的凭据主键。 */
const testingId = ref<number | null>(null)
/** modalOpen 表示凭据弹窗是否打开。 */
const modalOpen = ref(false)
/** editing 是当前正在修改的凭据。 */
const editing = ref<Credential | null>(null)
/** toast 提供统一页面通知。 */
const toast = useToast()
/** confirmDiscard 使用统一对话框确认丢弃凭据草稿。 */
const confirmDiscard = useDiscardChanges()
/** browsing 是正在查询云证书的凭据。 */
const browsing = ref<Credential | null>(null)
/** selectedCloudCertificate 保存浏览选择，不执行部署。 */
const selectedCloudCertificate = ref('')
/** initial 保存表单打开时的草稿快照。 */
const initial = ref('')

/** form 是共享凭据创建和编辑草稿。 */
const form = reactive<CredentialInput>({
  name: '',
  provider: 'ALIYUN',
  zone: '',
  accessKeyId: '',
  accessKeySecret: '',
  enabled: true,
  dnsEnabled: true,
  cloudEnabled: false,
})

/** providerOptions 是当前支持的服务商选项。 */
const providerOptions = [
  { label: '阿里云', value: 'ALIYUN', description: '支持 DNS 验证、证书上传和云产品部署', details: ['认证方式：AccessKeyId / AccessKeySecret'] },
  { label: 'Cloudflare', value: 'CLOUDFLARE', description: '支持 DNS 验证', details: ['认证方式：API Token'] },
]
/** draftZones 将输入草稿解析为可预览的根域名标签。 */
const draftZones = computed(() => parseCredentialZones(form.zone))
/** enabledCount 是已启用凭据数量。 */
const enabledCount = computed(() => credentials.value.filter((item) => item.enabled).length)

/** load 只应用最新的共享凭据列表。 */
async function load() {
  await loader.run(
    /** 查询统一凭据列表。 */
    (signal) => credentialsApi.list(signal),
    /** 应用脱敏结果。 */
    (values) => { credentials.value = values },
    /** 展示加载失败原因。 */
    (error) => toast.error('凭据加载失败', error instanceof Error ? error.message : undefined),
  )
}

/** close 在丢弃草稿之前确认，并清理输入中的秘密。 */
function close() {
  if (saving.value) return
  if (JSON.stringify(form) !== initial.value) {
    confirmDiscard('凭据修改尚未保存，放弃后将清除本次输入的内容。', discard)
    return
  }
  discard()
}

/** discard 关闭凭据编辑并清理本次输入的秘密。 */
function discard() {
  modalOpen.value = false; form.accessKeyId = ''; form.accessKeySecret = ''
}

/** changeProvider 切换到 Cloudflare 时清除不支持的云部署用途。 */
function changeProvider() { if (form.provider === 'CLOUDFLARE') { form.cloudEnabled = false; form.dnsEnabled = true } }

/** browse 查询当前凭据的证书中心权限和已有证书。 */
function browse(value: Credential) { browsing.value = value; selectedCloudCertificate.value = '' }

/** openCreate 重置表单并打开新增弹窗。 */
function openCreate() {
  editing.value = null
  Object.assign(form, { name: '', provider: 'ALIYUN', zone: '', accessKeyId: '', accessKeySecret: '', enabled: true, dnsEnabled: true, cloudEnabled: false })
  initial.value = JSON.stringify(form)
  modalOpen.value = true
}

/** openEdit 将非秘密字段映射到编辑表单。 */
function openEdit(credential: Credential) {
  editing.value = credential
  Object.assign(form, {
    name: credential.name,
    provider: credential.provider,
    zone: credentialZones(credential).join('\n'),
    accessKeyId: '',
    accessKeySecret: '',
    enabled: credential.enabled,
    dnsEnabled: credential.dnsEnabled,
    cloudEnabled: credential.cloudEnabled,
  })
  initial.value = JSON.stringify(form)
  modalOpen.value = true
}

/** save 校验必填项并创建或更新加密凭据。 */
async function save() {
  if (saving.value) return
  if (!form.name.trim() || (form.dnsEnabled && !draftZones.value.length) || (!form.dnsEnabled && !form.cloudEnabled)) {
    toast.error('请填写名称、选择用途；DNS 验证用途还需根域名')
    return
  }
  if (draftZones.value.length > 100) { toast.error('一条凭据最多配置 100 个根域名'); return }
  if (!editing.value && (!form.accessKeySecret || (form.provider === 'ALIYUN' && !form.accessKeyId.trim()))) {
    toast.error(form.provider === 'CLOUDFLARE' ? '请输入 Cloudflare API Token' : '请输入 AccessKeyId 和 AccessKeySecret')
    return
  }

  saving.value = true
  try {
    await credentialsApi.save(editing.value?.id || null, { ...form, zones: draftZones.value, zone: draftZones.value[0] || '' })
    toast.success('共享凭据已加密保存')
    modalOpen.value = false; form.accessKeyId = ''; form.accessKeySecret = ''
    await load()
  } catch (error) {
    toast.error('凭据保存失败', error instanceof Error ? error.message : undefined)
  } finally {
    saving.value = false
  }
}

/** testCredential 验证凭据的服务商读取权限。 */
async function testCredential(credential: Credential) {
  if (testingId.value !== null) return
  testingId.value = credential.id
  try {
    /** result 是控制平面返回的凭据测试结果。 */
    const result = await api.testDnsCredential(credential.id)
    if (result.success) toast.success('所有根域名 DNS 权限验证通过', result.message)
    else toast.error('部分根域名 DNS 权限未通过', result.message)
    await load()
    detailId.value = credential.id
  } catch (error) {
    toast.error('凭据测试失败', error instanceof Error ? error.message : undefined)
  } finally {
    testingId.value = null
  }
}

/** remove 删除未被证书、订单或云资源引用的凭据。 */
async function remove(credential: Credential) {
  try {
    await credentialsApi.remove(credential.id)
    toast.success('凭据已删除')
    await load()
  } catch (error) {
    toast.error('凭据删除失败', error instanceof Error ? error.message : undefined)
  }
}

onMounted(load)
/** detailId 标识正在只读查看的凭据，刷新后自动使用当前数据。 */
const detailId = ref<number | null>(null)
/** detailItem 是当前列表中的最新详情对象。 */
const detailItem = computed(() => credentials.value.find((item) => item.id === detailId.value) || null)
/** detailFields 显式选择可展示的业务元数据，不访问秘密材料。 */
const detailFields = computed(() => {
  /** item 是当前详情对象。 */
  const item = detailItem.value
  if (!item) return []
  return [
    { group: '凭据信息', label: '凭据名称', value: item.name },
    { group: '凭据信息', label: '凭据编号', value: item.id },
    { group: '凭据信息', label: '服务商', value: item.provider === 'ALIYUN' ? '阿里云' : 'Cloudflare' },
    { group: '凭据信息', label: '状态', status: item.enabled ? 'SUCCESS' : 'DISABLED', value: item.enabled ? '已启用' : '已停用' },
    { group: '授权用途', label: 'DNS 验证用途', value: item.dnsEnabled ? '开启' : '关闭' },
    { group: '授权用途', label: '云部署用途', value: item.cloudEnabled ? '开启' : '关闭' },
    { group: '授权用途', label: 'DNS 根域名', value: item.dnsEnabled ? credentialZones(item) : '未启用 DNS 验证', wide: true },
    { group: '授权用途', label: '认证标识（脱敏）', mono: true, value: item.maskedAccessKeyId },
    { group: '授权用途', label: '云账号身份固定', value: item.accessKeyIdLocked ? '是，不可更换 AccessKeyId' : '否' },
    { group: 'DNS 权限测试', label: '最近 DNS 测试', status: item.lastTestStatus || undefined, value: item.lastTestStatus === 'SUCCESS' ? '成功' : item.lastTestStatus === 'FAILED' ? '失败' : '尚未测试' },
    { group: 'DNS 权限测试', label: '最近测试时间', value: formatDate(item.lastTestAt) },
    { group: 'DNS 权限测试', label: '完整测试结果', value: item.lastTestMessage, wide: true },
    { group: '维护时间', label: '创建时间', value: formatDate(item.createdAt) },
    { group: '维护时间', label: '更新时间', value: formatDate(item.updatedAt) },
  ]
})
</script>

<template>
  <div>
    <PageHeader eyebrow="访问授权" title="凭据管理" description="统一管理阿里云和 Cloudflare 密钥，按用途提供 DNS 验证和云部署授权。">
      <template #actions><AppButton action="create" tone="primary" @click="openCreate">添加凭据</AppButton></template>
    </PageHeader>

    <section class="security-banner">
      <span class="shield"><NIcon :component="CloudLock" /></span>
      <div><strong>凭据加密存储</strong><p>AccessKeySecret 和 API Token 加密保存，管理端不会返回凭据明文。</p></div>
      <div class="security-metric"><span>已启用</span><strong>{{ enabledCount }}<small>/{{ credentials.length }}</small></strong></div>
    </section>

    <NCard class="content-card" :bordered="true" content-style="padding: 0">
      <template #header><div class="card-title"><h2>服务商凭据</h2><p>同一份阿里云密钥可同时用于 DNS 验证、证书上传和云产品部署</p></div></template>
      <template #header-extra><AppButton action="refresh" size="small" :loading="loading" @click="load">刷新</AppButton></template>

      <div v-if="loading" class="loading-block"><NSpin size="small" />正在加载加密凭据…</div>
      <div v-else-if="credentials.length" class="credential-grid">
        <NCard
          v-for="credential in credentials"
          :key="credential.id"
          class="credential-card"
          :class="{ disabled: !credential.enabled }"
          size="small"
          :bordered="true"
          content-style="padding: 18px 16px 20px"
          footer-style="padding: 12px 16px"
          hoverable
        >
          <template #header>
            <div class="credential-head">
              <span class="aliyun-mark">{{ credential.provider === 'ALIYUN' ? 'A' : 'C' }}</span>
              <div><strong>{{ credential.name }}</strong><small>{{ credential.provider === 'ALIYUN' ? 'Alibaba Cloud' : 'Cloudflare' }}</small></div>
            </div>
          </template>
          <template #header-extra><StatusBadge :status="credential.enabled ? 'ONLINE' : 'OFFLINE'" :label="credential.enabled ? '已启用' : '已停用'" /></template>

          <div class="credential-body">
            <div><span>用途</span><NSpace :size="4"><NTag v-if="credential.dnsEnabled" size="small">DNS 验证</NTag><NTag v-if="credential.cloudEnabled" size="small" type="success">云部署</NTag></NSpace></div>
            <div v-if="credential.dnsEnabled" class="credential-zones"><span>根域名（{{ credentialZones(credential).length }}）</span><ul class="zone-tags"><li v-for="zone in credentialZones(credential)" :key="zone">{{ zone }}</li></ul></div>
            <div><span>{{ credential.provider === 'ALIYUN' ? 'AccessKeyId' : '认证方式' }}</span><strong class="mono">{{ credential.maskedAccessKeyId }}</strong></div>
            <div><span>最近测试</span><strong>{{ formatDate(credential.lastTestAt) }}</strong></div>
            <div><span>测试结果</span><StatusBadge v-if="credential.lastTestStatus" :status="credential.lastTestStatus" /><strong v-else>尚未测试</strong></div>
          </div>
          <NAlert v-if="credential.lastTestMessage" class="test-message" :type="credential.lastTestStatus === 'FAILED' ? 'error' : 'info'" :show-icon="false">
            {{ credential.lastTestMessage }}
          </NAlert>

          <template #footer>
            <div class="action-group credential-actions">
              <AppButton action="detail" size="small" :aria-label="`查看凭据 ${credential.name} 详情`" @click="detailId = credential.id">详情</AppButton>
              <AppButton action="security" v-if="credential.dnsEnabled" size="small" :loading="testingId === credential.id" :disabled="testingId !== null" @click="testCredential(credential)">验证 DNS 权限</AppButton>
              <AppButton action="cloud" v-if="credential.cloudEnabled" size="small" :disabled="!credential.enabled" @click="browse(credential)">查看云证书</AppButton>
              <AppButton action="edit" size="small" @click="openEdit(credential)">编辑</AppButton>
              <NPopconfirm positive-text="确认删除" negative-text="取消" @positive-click="remove(credential)">
                <template #trigger><AppButton action="delete" tone="danger" size="small">删除</AppButton></template>
                确认删除“{{ credential.name }}”吗？已有证书或云资源引用时请停用凭据。
              </NPopconfirm>
            </div>
          </template>
        </NCard>
      </div>
      <EmptyState v-else title="还没有凭据" description="添加具有目标区域 DNS 写入权限的阿里云 AccessKey 或 Cloudflare API Token。" symbol="◎">
        <AppButton action="create" tone="primary" @click="openCreate">添加第一条凭据</AppButton>
      </EmptyState>
    </NCard>

    <AppModal :open="!!detailItem" title="凭据详情" description="查看完整信息；修改配置请使用列表中的编辑操作。" width="large" @close="detailId = null">
      <DetailFields v-if="detailItem" :fields="detailFields" />
      <section v-if="detailItem?.lastTestResults?.length" class="zone-results" aria-label="逐域名测试结果">
        <h3 class="detail-heading">逐域名测试结果</h3>
        <ul>
          <li v-for="result in detailItem.lastTestResults" :key="result.zone">
            <div class="zone-result-heading"><strong>{{ result.zone }}</strong><StatusBadge :status="result.success ? 'SUCCESS' : 'FAILED'" :label="result.success ? '通过' : '未通过'" /></div>
            <p>{{ result.message }}</p><small>{{ formatDate(result.testedAt) }}</small>
          </li>
        </ul>
      </section>
      <template #footer><AppButton action="close" @click="detailId = null">关闭</AppButton></template>
    </AppModal>

    <AppModal :open="modalOpen" :title="editing ? '编辑凭据' : '添加凭据'" description="选择用途后仍需在服务商侧授予相应权限。" @close="close">
      <NAlert class="credential-tip" type="warning" :show-icon="true" title="Secret 保存后无法再次查看">
        如需轮换凭据，请输入新密钥；编辑时留空保留现有值。已有凭据的服务商不可修改。共用凭据的秘密更新会同时影响 DNS 验证和云部署。
      </NAlert>
      <NForm id="credential-form" class="credential-form" :model="form" label-placement="top" @submit.prevent="save">
        <NFormItem label="凭据名称" :show-feedback="false"><NInput v-model:value="form.name" placeholder="生产域名 · 华东账号" maxlength="64" /></NFormItem>
        <NFormItem label="用途"><NSpace><NCheckbox v-model:checked="form.dnsEnabled">DNS 验证</NCheckbox><NCheckbox v-model:checked="form.cloudEnabled" :disabled="form.provider !== 'ALIYUN'">云部署（阿里云）</NCheckbox></NSpace></NFormItem>
        <div class="form-grid">
          <NFormItem label="服务商" :show-feedback="false"><AppSelect v-model:value="form.provider" :options="providerOptions" :disabled="Boolean(editing)" @update:value="changeProvider" /></NFormItem>

        </div>
        <NFormItem v-if="form.dnsEnabled" label="根域名（支持多个）" :show-feedback="false">
          <div class="zone-editor">
            <NInput v-model:value="form.zone" type="textarea" :autosize="{ minRows: 3, maxRows: 6 }" placeholder="example.org&#10;example.com&#10;example.net" :input-props="{ 'aria-label': '根域名列表' }" />
            <p class="field-hint">每行一个，也可用逗号分隔，最多 100 个。填写 DNS 根域名，不含 https://、路径或 *.。</p>
            <ul v-if="draftZones.length" class="zone-tags" aria-label="根域名预览"><li v-for="zone in draftZones" :key="zone">{{ zone }}</li></ul>
            <p class="field-hint">同一份密钥须具备所有已填根域名的 DNS 权限；空列表不代表允许全部域名。移除仍被证书系列使用的域名会被阻止，请先调整系列的 DNS 凭据。</p>
          </div>
        </NFormItem>
        <NAlert v-if="form.provider === 'CLOUDFLARE'" class="credential-tip" type="info">Token 需要所有已填根域名所在区域的 Zone Read 和 DNS Write 权限，系统会自动查询 Zone ID。</NAlert>
        <NFormItem v-if="form.provider === 'ALIYUN'" label="AccessKeyId" :show-feedback="false">
          <NInput v-model:value="form.accessKeyId" :disabled="Boolean(editing?.accessKeyIdLocked)" :placeholder="editing ? `留空保留 ${editing.maskedAccessKeyId}` : 'LTAI5t…'" autocomplete="off"><template #prefix><NIcon :component="Key" /></template></NInput>
        </NFormItem>
        <NFormItem :label="form.provider === 'CLOUDFLARE' ? 'API Token' : 'AccessKeySecret'" :show-feedback="false">
          <NInput v-model:value="form.accessKeySecret" type="password" :placeholder="editing ? '留空表示不修改' : '输入 Secret，仅本次提交可见'" autocomplete="new-password" show-password-on="click"><template #prefix><NIcon :component="Key" /></template></NInput>
        </NFormItem>
        <NCheckbox v-model:checked="form.enabled">启用凭据，允许所选用途使用它</NCheckbox>
      </NForm>
      <template #footer><AppButton action="close" :disabled="saving" @click="close">取消</AppButton><AppButton action="save" tone="primary" attr-type="submit" form="credential-form" :loading="saving">保存凭据</AppButton></template>
    </AppModal>
    <AppModal :open="Boolean(browsing)" :title="`${browsing?.name || ''} · 云端证书`" width="large" @close="browsing = null">
      <NAlert type="info" :show-icon="false" style="margin-bottom: 16px">此查询验证证书中心的读取权限；OSS/CDN 部署权限在执行对应任务时检查。</NAlert>
      <CloudCertificatePicker v-if="browsing" v-model="selectedCloudCertificate" :credential-id="browsing.id" />
    </AppModal>
  </div>
</template>

<style scoped>
.security-banner { min-height: 92px; display: grid; grid-template-columns: 46px 1fr auto; align-items: center; gap: 16px; margin-bottom: 18px; padding: 16px 22px; color: #3d493f; background: var(--sage); border: 1px solid #dce3dc; border-radius: 12px; box-shadow: var(--shadow); }
.shield { width: 43px; height: 43px; display: grid; place-items: center; color: #526b5d; border: 1px solid #d3ddd4; border-radius: 9px; background: #fafbf8; font-size: 21px; }.security-banner strong { display: block; font-size: 13px; }.security-banner p { margin: 6px 0 0; color: #758078; font-size: 11px; line-height: 1.6; }
.security-metric { min-width: 105px; padding-left: 24px; border-left: 1px solid #d5ddd6; }.security-metric span { color: #7d877f; font-size: 10px; }.security-metric strong { display: block; margin-top: 4px; color: #465f51; font-size: 20px; font-weight: 700; }.security-metric small { color: #8b928c; font-size: 10px; }
.content-card { box-shadow: var(--shadow); }.card-title h2 { margin: 0; font-size: 15px; }.card-title p { margin: 4px 0 0; color: var(--muted); font-size: 11px; }
.credential-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; align-items: stretch; padding: 20px; }.credential-card { height: 100%; background: #fffefa; }.credential-card :deep(.n-card__content) { flex: 1; }.credential-card.disabled { opacity: .64; }
.credential-head { display: flex; align-items: center; gap: 11px; }.aliyun-mark { width: 38px; height: 38px; display: grid; place-items: center; color: #a15f35; background: #f7ebe2; border-radius: 9px; font-weight: 800; }.credential-head strong, .credential-head small { display: block; }.credential-head strong { color: #3b3d36; font-size: 13px; }.credential-head small { margin-top: 3px; color: #918e84; font-size: 9px; }
.credential-body { display: grid; grid-template-columns: 1fr 1fr; gap: 20px 16px; }.credential-body span { display: block; margin-bottom: 6px; color: #8b897f; font-size: 10px; }.credential-body strong { color: #51534b; font-size: 11px; }.test-message { margin-top: 18px; }.credential-actions { align-content: start; }
.zone-editor { width: 100%; min-width: 0; }.field-hint { margin: 8px 0; color: var(--muted); font-size: 12px; line-height: 1.6; }
.zone-tags { list-style: none; display: flex; flex-wrap: wrap; gap: 6px; padding: 0; margin: 0; }.zone-tags li { max-width: 100%; padding: 3px 8px; border-radius: 6px; background: var(--sage); color: #4d6859; overflow-wrap: anywhere; font-size: 12px; }
.credential-zones { grid-column: 1 / -1; min-width: 0; }.zone-results { margin-top: 24px; }.zone-results ul { list-style: none; margin: 0; padding: 0; border: 1px solid var(--line); border-radius: 8px; overflow: hidden; }.zone-results li { padding: 14px 16px; }.zone-results li + li { border-top: 1px solid var(--line); }.zone-result-heading { display: flex; justify-content: space-between; align-items: start; gap: 12px; }.zone-result-heading strong { min-width: 0; overflow-wrap: anywhere; }.zone-results p { margin: 8px 0; line-height: 1.6; }.zone-results small { color: var(--muted); }
.credential-tip { margin-bottom: 20px; }.credential-form { display: grid; }.form-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 18px; }
@media (max-width: 840px) { .credential-grid { grid-template-columns: 1fr; }.security-metric { display: none; }.security-banner { grid-template-columns: 46px 1fr; } }
@media (max-width: 560px) { .security-banner p { font-size: 10px; }.form-grid { grid-template-columns: 1fr; gap: 0; } }
</style>
