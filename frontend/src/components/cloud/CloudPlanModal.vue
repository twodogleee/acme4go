<script setup lang="ts">
/** 独立云配置表单，证书来源与产品配置解耦。 @author Andersen */
import AppButton from '@/components/AppButton.vue'
import { computed, reactive, ref, watch } from 'vue'
import { NAlert, NCheckbox, NForm, NFormItem, NInput, NSpace } from 'naive-ui'
import CredentialPicker from '@/components/CredentialPicker.vue'
import AppSelect from '@/components/AppSelect.vue'
import AppModal from '@/components/AppModal.vue'
import LocalCertificatePicker from '@/components/LocalCertificatePicker.vue'
import CloudCertificatePicker from './CloudCertificatePicker.vue'
import { cloudApi } from '@/api/cloud'
import { useToast } from '@/composables/useToast'
import { useDiscardChanges } from '@/composables/useDiscardChanges'
import type { Credential } from '@/types/credentials'
import type { CertificateSeries } from '@/types'
import type { CloudPlan, CloudPlanInput, CloudProduct } from '@/types/cloud'

/** props 提供领域数据和编辑上下文。 */
const props = defineProps<{
  open: boolean // 弹窗开关。
  editing: CloudPlan | null // 已有配置。
  kind: 'UPLOAD' | 'DEPLOY' // 新增类型。
  credentials: Credential[] // 可选凭据。
  series: CertificateSeries[] // 可选系列。
  products: CloudProduct[] // 注册的产品能力。
}>()
/** emit 通知保存和关闭。 */
const emit = defineEmits<{
  close: [] // 关闭表单。
  saved: [] // 保存后刷新列表。
}>()
/** form 保存本地可编辑草稿。 */
const form = reactive<CloudPlanInput>({ name: '', credentialId: 0, kind: 'UPLOAD', product: '', seriesId: null, remoteCertificateId: '', config: {}, autoSync: false, enabled: true })
/** source 区分跟随本地系列和固定云证书。 */
const source = ref<'SERIES' | 'REMOTE'>('SERIES')
/** saving 防止重复提交。 */
const saving = ref(false)
/** initial 是未保存变更比较快照。 */
const initial = ref('')
/** toast 提供操作反馈。 */
const toast = useToast()
/** confirmDiscard 使用统一对话框确认丢弃云配置草稿。 */
const confirmDiscard = useDiscardChanges()
/** productOptions 来源于后端产品注册表。 */
const productOptions = computed(() => props.products.map((p) => ({ label: p.name, value: p.id, description: p.id === 'OSS' ? '更新 Bucket 已绑定自定义域名的 HTTPS 证书' : p.id === 'CDN' ? '更新 CDN 加速域名的 HTTPS 证书' : undefined, details: [`配置项：${p.fields.map(
    /** 仅展示产品要求填写的资源名称。 */
    (field) => field.label).join('、')}`] })))
/** fields 是当前适配器所需的资源参数。 */
const fields = computed(() => props.products.find(
    /** 查找当前产品的字段定义。 */
    (p) => p.id === form.product)?.fields || [])
/** sourceOptions 描述两个互斥来源。 */
const sourceOptions = [
  { label: '本地证书系列', value: 'SERIES', badge: '支持自动同步', description: '上传本地当前版本后部署，可跟随后续续期版本。' },
  { label: '阿里云已有证书', value: 'REMOTE', badge: '手动执行', description: '使用指定云证书部署，不重复上传，也不跟随本地续期。' },
]

watch(() => props.open, (open) => {
  if (!open) return
  Object.assign(form, props.editing ? { ...props.editing, config: { ...props.editing.config } } : { name: '', credentialId: 0, kind: props.kind, product: props.kind === 'DEPLOY' ? props.products[0]?.id || '' : '', seriesId: null, remoteCertificateId: '', config: {}, autoSync: false, enabled: true })
  source.value = form.seriesId || form.kind === 'UPLOAD' ? 'SERIES' : 'REMOTE'
  initial.value = JSON.stringify(form)
})

/** changeSource 清除不再适用的来源，固定云证书不能自动跟随。 */
function changeSource(value: 'SERIES' | 'REMOTE') {
  source.value = value; form.seriesId = null; form.remoteCertificateId = ''; form.autoSync = false
}
/** changeCredential 避免跨账号复用上次选择的证书 ID。 */
function changeCredential(value: number) { form.credentialId = value; form.remoteCertificateId = '' }
/** changeProduct 清理其他产品特有配置。 */
function changeProduct(value: string) { form.product = value; form.config = {} }
/** close 在有草稿变更时确认放弃。 */
function close() {
  if (saving.value) return
  if (initial.value !== JSON.stringify(form)) {
    confirmDiscard('云配置修改尚未保存，放弃后将丢失本次修改。', discard)
    return
  }
  discard()
}
/** discard 通知父工作区关闭表单，下次打开时重新初始化草稿。 */
function discard() { emit('close') }
/** save 显式构造输入，避免把持久主键或修订号发送到严格接口。 */
async function save() {
  if (saving.value) return
  if (!form.name.trim() || !form.credentialId || (source.value === 'SERIES' ? !form.seriesId : !form.remoteCertificateId)) { toast.error('请填写名称、凭据并选择证书来源'); return }
  if (form.kind === 'DEPLOY' && fields.value.some(
    /** 检查必填资源字段。 */
    (field) => !form.config[field.key]?.trim())) { toast.error('请完整填写云产品资源信息'); return }
  saving.value = true
  try {
    await cloudApi.savePlan(props.editing?.id || null, { name: form.name, credentialId: form.credentialId, kind: form.kind, product: form.kind === 'DEPLOY' ? form.product : '', seriesId: source.value === 'SERIES' ? form.seriesId : null, remoteCertificateId: source.value === 'REMOTE' ? form.remoteCertificateId : '', config: form.kind === 'DEPLOY' ? { ...form.config } : {}, autoSync: form.autoSync, enabled: form.enabled })
    toast.success('云配置已保存'); emit('saved'); emit('close')
  } catch (error) { toast.error('保存失败', error instanceof Error ? error.message : undefined) }
  finally { saving.value = false }
}
</script>

<template>
  <AppModal :open="open" :title="`${editing ? '编辑' : '新建'}${kind === 'UPLOAD' ? '上传规则' : '部署目标'}`" width="large" @close="close">
    <NForm label-placement="top">
      <NFormItem label="名称"><NInput v-model:value="form.name" maxlength="100" placeholder="例如：生产站点 CDN" /></NFormItem>
      <NFormItem label="云部署凭据"><CredentialPicker :model-value="form.credentialId" :credentials="credentials" purpose="CLOUD" @update:model-value="changeCredential" /></NFormItem>
      <NFormItem v-if="kind === 'DEPLOY'" label="证书来源"><AppSelect :value="source" :options="sourceOptions" @update:value="changeSource" /></NFormItem>
      <NFormItem v-if="source === 'SERIES'" label="证书系列"><LocalCertificatePicker v-model="form.seriesId" :series="series" /></NFormItem>
      <NFormItem v-else label="已有云证书"><CloudCertificatePicker v-if="open" v-model="form.remoteCertificateId" :credential-id="form.credentialId" /></NFormItem>
      <template v-if="kind === 'DEPLOY'">
        <NFormItem label="云产品"><AppSelect :value="form.product" :options="productOptions" @update:value="changeProduct" /></NFormItem>
        <NFormItem v-for="field in fields" :key="field.key" :label="field.label"><NInput v-model:value="form.config[field.key]" :placeholder="field.placeholder" /></NFormItem>
        <NAlert type="info" :show-icon="false">请使用已配置的云产品域名。OSS 前面使用 CDN 加速时，加速域名应选择 CDN 目标。</NAlert>
      </template>
      <NSpace style="margin-top: 20px"><NCheckbox v-model:checked="form.enabled">启用</NCheckbox><NCheckbox v-if="source === 'SERIES'" v-model:checked="form.autoSync">自动同步当前证书及后续续期版本</NCheckbox></NSpace>
      <p class="form-help">{{ kind === 'UPLOAD' ? '只将证书和私钥上传到阿里云证书中心。' : source === 'SERIES' ? '执行时上传本地当前版本，再更新指定云产品；已上传的版本会复用。' : '直接使用所选云证书部署，不读取本地证书，也不重新上传。' }}关闭自动同步时，保存后可手动执行。</p>
    </NForm>
    <template #footer><AppButton action="close" :disabled="saving" @click="close">取消</AppButton><AppButton action="save" tone="primary" :loading="saving" @click="save">保存配置</AppButton></template>
  </AppModal>
</template>

<style scoped>
.form-help { color: var(--muted); font-size: 12px; line-height: 1.7; }
</style>
