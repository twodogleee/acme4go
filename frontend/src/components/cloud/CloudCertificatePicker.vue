<script setup lang="ts">
/** 阿里云已有证书选择器，支持分页、搜索和账号切换取消请求。 @author Andersen */
import DetailFields from '@/components/DetailFields.vue'
import AppButton from '@/components/AppButton.vue'
import { computed, h, ref, watch } from 'vue'
import { NInput, NPagination, NSelect, NSpace } from 'naive-ui'
import type { SelectRenderLabel, SelectRenderTag } from 'naive-ui'
import { cloudApi } from '@/api/cloud'
import type { CloudCertificate } from '@/types/cloud'
import { useLatestRequest } from '@/composables/useLatestRequest'
import { useToast } from '@/composables/useToast'

/** props 限定查询账号与当前选择。 */
const props = defineProps<{
  credentialId: number // 共享凭据标识。
  modelValue: string // 已选云证书 ID。
}>()
/** emit 将选择通知父表单。 */
const emit = defineEmits<{
  'update:modelValue': [value: string] // 新选中的云端证书标识。
}>()
/** items 是当前页元数据。 */
const items = ref<CloudCertificate[]>([])
/** page 是云端页码。 */
const page = ref(1)
/** total 是云端匹配数量。 */
const total = ref(0)
/** search 是待提交搜索关键字。 */
const search = ref('')
/** loader 管理请求取消和竞态。 */
const loader = useLatestRequest()
/** toast 提供错误提示。 */
const toast = useToast()
/** selectedCertificate 在选择框下方展示当前页已选证书的完整元数据。 */
const selectedCertificate = computed(() => items.value.find((item) => item.id === props.modelValue))
/** options 保留未出现在当前页中的已有选择。 */
const options = computed(() => {

    /** values 是带域名和到期时间的选项。 */

  const values = items.value.map((item) => ({ value: item.id, label: `${item.name} · ${item.domains.join(', ')} · ${item.notAfter} · #${item.id}` }))
  if (props.modelValue && !values.some(
    /** 判断选择是否已在当前页。 */
    (item) => item.value === props.modelValue)) values.unshift({ value: props.modelValue, label: `已选证书 #${props.modelValue}` })
  return values
})

/** renderCertificateLabel 将长证书信息分行展示，域名和标识均允许换行。 */
const renderCertificateLabel: SelectRenderLabel = (option) => {
  /** certificate 是当前选项对应的云证书元数据。 */
  const certificate = items.value.find((item) => item.id === option.value)
  if (!certificate) return String(option.label || '')
  return h('div', { class: 'cloud-certificate-option' }, [
    h('strong', { class: 'cloud-certificate-option-name' }, certificate.name),
    h('span', null, `域名：${certificate.domains.join('、') || '未提供'}`),
    h('span', { class: 'cloud-certificate-option-meta' }, `到期：${certificate.notAfter || '未提供'} · ID：${certificate.id}`),
  ])
}
/** renderCertificateTag 保持选择框紧凑，完整内容由下方信息区呈现。 */
const renderCertificateTag: SelectRenderTag = ({ option }) => {
  /** certificate 是当前选择，翻页后仍可回显已选 ID。 */
  const certificate = items.value.find((item) => item.id === option.value)
  /** label 包含足以区分同名证书的云端编号。 */
  const label = certificate ? `${certificate.name} · #${certificate.id}` : String(option.label || '')
  return h('span', { title: label }, label)
}

/** load 只应用当前账号与查询条件的最新响应。 */
function load() {
  if (!props.credentialId) return
  void loader.run(

    /** 查询当前账号的证书页。 */

    (signal) => cloudApi.certificates(props.credentialId, page.value, search.value, signal),

    /** 应用最新证书页。 */

    (value) => { items.value = value.items; total.value = value.total },

    /** 显示查询失败原因。 */

    (error) => toast.error('云证书查询失败', error instanceof Error ? error.message : undefined),
  )
}
/** searchCertificates 从第一页搜索。 */
function searchCertificates() { page.value = 1; load() }
/** changePage 切换云端页码。 */
function changePage(value: number) { page.value = value; load() }

watch(() => props.credentialId, () => {
  loader.cancel()
  items.value = []; total.value = 0; page.value = 1
  load()
}, { immediate: true })
</script>

<template>
  <NSpace vertical style="width: 100%">
    <NSpace :wrap="false"><NInput v-model:value="search" placeholder="搜索域名或资源 ID" @keyup.enter="searchCertificates" /><AppButton action="search" :disabled="!credentialId" :loading="loader.loading.value" @click="searchCertificates">查询</AppButton></NSpace>
    <!-- 独立挂载到页面弹出层，避免被 AppModal 的圆角裁切和正文滚动区截断；变高选项不使用虚拟滚动。 -->
    <NSelect :value="modelValue || null" :options="options" :to="true" :virtual-scroll="false" :render-label="renderCertificateLabel" :render-tag="renderCertificateTag" :menu-props="{ class: 'cloud-certificate-menu' }" :loading="loader.loading.value" :disabled="!credentialId" placeholder="选择阿里云已有证书" :input-props="{ 'aria-label': '选择阿里云已有证书' }" @update:value="emit('update:modelValue', $event)" />
    <DetailFields v-if="selectedCertificate" title="已选云证书" :fields="[
      { label: '证书名称', value: selectedCertificate.name, wide: true },
      { label: '绑定域名', value: selectedCertificate.domains, wide: true },
      { label: '到期时间', value: selectedCertificate.notAfter },
      { label: '证书 ID', value: selectedCertificate.id, mono: true },
    ]" />
    <NPagination v-if="total > 50" :page="page" :page-size="50" :item-count="total" simple @update:page="changePage" />
    <small>证书中心：中国站（杭州）。支持已上传证书和已有签发证书。</small>
  </NSpace>
</template>

<style>
/* 菜单传送到弹窗外，使用专属类限定覆盖范围；多行选项仍由组件处理选择和键盘导航。 */
.cloud-certificate-menu .n-base-select-option__content { min-width: 0; white-space: normal; }
.cloud-certificate-menu .cloud-certificate-option { display: grid; gap: 5px; min-width: 0; padding: 8px 0; white-space: normal; overflow-wrap: anywhere; font-size: 12px; line-height: 1.6; }
.cloud-certificate-option-name { font-size: 13px; font-weight: 600; }
.cloud-certificate-option-meta { color: var(--muted); }
</style>
