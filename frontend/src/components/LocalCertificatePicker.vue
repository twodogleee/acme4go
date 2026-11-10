<script setup lang="ts">
/** 本地证书系列选择器：统一展示名称、域名、环境及当前版本，供云部署和文件部署复用。 @author Andersen */
import DetailFields from '@/components/DetailFields.vue'
import { computed, h } from 'vue'
import { NSelect } from 'naive-ui'
import type { SelectOption, SelectRenderLabel, SelectRenderTag } from 'naive-ui'
import type { CertificateSeries } from '@/types'
import { formatDate } from '@/utils/format'

/** props 接收已有系列元数据，选择器不额外发起证书或私钥查询。 */
const props = defineProps<{
  modelValue: string | null // 当前绑定的稳定系列 ID。
  series: CertificateSeries[] // 调用方加载的证书系列。
}>()
/** emit 只更新系列绑定，签发和部署仍由调用方执行。 */
const emit = defineEmits<{
  'update:modelValue': [value: string | null] // 新选择的系列 ID。
}>()
/** selectedSeries 用于收起菜单后核对完整证书信息。 */
const selectedSeries = computed(() => props.series.find((item) => item.id === props.modelValue))
/** options 仅允许选择启用系列，同时保留停用或缺失的已有绑定供辨认。 */
const options = computed<SelectOption[]>(() => {
  /** values 的文本标签包含搜索字段，展示时由结构化渲染替代。 */
  const values: SelectOption[] = props.series.filter((item) => item.enabled || item.id === props.modelValue).map((item) => ({
    value: item.id,
    label: `${item.name} ${item.code} ${item.id} ${item.domains.join(' ')} ${environmentName(item)}`,
    disabled: !item.enabled,
  }))
  if (props.modelValue && !selectedSeries.value) {
    values.unshift({ value: props.modelValue, label: `当前系列 ${props.modelValue}（不可用）`, disabled: true })
  }
  return values
})

/** environmentName 区分可对外使用的正式证书与测试环境证书。 */
function environmentName(item: CertificateSeries) {
  return item.caEnvironment === 'LETS_ENCRYPT_STAGING' ? '测试环境' : '正式环境'
}
/** versionDescription 对未签发系列显示真实状态，不拼接虚构的版本或到期时间。 */
function versionDescription(item: CertificateSeries) {
  if (!item.currentCertificateId) return '尚未签发'
  /** statusLabels 使用接口提供的当前证书生命周期状态。 */
  const statusLabels = { VALID: '有效', EXPIRING: '即将到期', EXPIRED: '已过期' }
  return `版本 v${item.currentVersion} · ${item.currentStatus ? statusLabels[item.currentStatus] : '状态未知'} · 到期：${formatDate(item.currentNotAfter, false)}`
}
/** renderSeriesLabel 将各系列按相同层级排列，长名称、域名和编码均可换行。 */
const renderSeriesLabel: SelectRenderLabel = (option) => {
  /** item 是选项对应的完整系列元数据。 */
  const item = props.series.find((entry) => entry.id === option.value)
  if (!item) return h('div', { class: 'local-certificate-option' }, String(option.label || ''))
  return h('div', { class: 'local-certificate-option' }, [
    h('strong', { class: 'local-certificate-option-name' }, `${item.name}${item.enabled ? '' : '（已停用）'}`),
    h('span', null, `域名：${item.domains.join('、') || '未提供'}`),
    h('span', { class: 'local-certificate-option-meta' }, `${environmentName(item)} · ${versionDescription(item)}`),
    h('span', { class: 'local-certificate-option-meta' }, `系列编码：${item.code || item.id}`),
  ])
}
/** renderSeriesTag 让收起的选择框保持单行，完整信息保留在下方。 */
const renderSeriesTag: SelectRenderTag = ({ option }) => {
  /** item 是当前选择的系列，缺失时仍展示原绑定编号。 */
  const item = props.series.find((entry) => entry.id === option.value)
  /** label 以名称和唯一编码区分同域名的不同系列。 */
  const label = item ? `${item.name} · ${item.code || item.id}${item.enabled ? '' : '（已停用）'}` : String(option.label || '')
  return h('span', { title: label }, label)
}
</script>

<template>
  <div class="local-certificate-picker">
    <!-- 菜单独立挂载，避开弹窗裁切；可变高度选项关闭虚拟滚动以完整显示长文本。 -->
    <NSelect
      :value="modelValue"
      :options="options"
      :to="true"
      :virtual-scroll="false"
      :render-label="renderSeriesLabel"
      :render-tag="renderSeriesTag"
      :menu-props="{ class: 'local-certificate-menu' }"
      filterable
      placeholder="搜索证书名称、系列编码或域名"
      :input-props="{ 'aria-label': '选择本地证书系列' }"
      @update:value="emit('update:modelValue', $event)"
    />
    <DetailFields v-if="selectedSeries" class="local-certificate-summary" title="已选本地证书" :fields="[
      { label: '系列名称', value: selectedSeries.name },
      { label: '状态', value: selectedSeries.enabled ? '已启用' : '已停用', status: selectedSeries.enabled ? 'SUCCESS' : 'DISABLED' },
      { label: '绑定域名', value: selectedSeries.domains, wide: true },
      { label: '证书环境', value: environmentName(selectedSeries) },
      { label: '当前证书', value: versionDescription(selectedSeries) },
      { label: '系列编码', value: selectedSeries.code || selectedSeries.id, mono: true, wide: true },
    ]" />
    <p v-else-if="modelValue" class="local-certificate-unavailable">当前绑定系列 {{ modelValue }} 不可用，请重新选择。</p>
  </div>
</template>

<style scoped>
.local-certificate-summary { margin-top: 14px; }
.local-certificate-picker { width: 100%; min-width: 0; }
.local-certificate-unavailable { margin: 10px 0 0; color: var(--muted); font-size: 12px; overflow-wrap: anywhere; }
</style>

<style>
/* 菜单位于弹窗外，只覆盖本地证书菜单的文字布局，保留组件的键盘导航和禁用行为。 */
.local-certificate-menu .n-base-select-option__content { min-width: 0; white-space: normal; }
.local-certificate-menu .local-certificate-option { display: grid; gap: 5px; min-width: 0; padding: 8px 0; white-space: normal; overflow-wrap: anywhere; font-size: 12px; line-height: 1.6; }
.local-certificate-option-name { font-size: 13px; font-weight: 600; }
.local-certificate-option-meta { color: var(--muted); }
</style>
