<script setup lang="ts">
/** 凭据选择器：按 DNS 或云部署用途筛选脱敏凭据，并结构化展示服务商、范围及状态。 @author Andersen */
import { computed } from 'vue'
import AppSelect from '@/components/AppSelect.vue'
import type { DnsCredential } from '@/types'
import type { Credential } from '@/types/credentials'
import { credentialZones } from '@/utils/credentialZones'
import type { SelectChoice } from '@/types/select'

/** PickerCredential 兼容 DNS 专用接口和提供全部用途的共享凭据接口。 */
type PickerCredential = DnsCredential & Partial<Pick<Credential, 'dnsEnabled' | 'cloudEnabled'>>
/** props 提供已脱敏的凭据，不读取任何秘密字段。 */
const props = defineProps<{
  modelValue: number // 零表示尚未选择。
  credentials: PickerCredential[] // 调用方已加载的凭据列表。
  purpose: 'DNS' | 'CLOUD' // 当前操作需要的凭据用途。
}>()
/** emit 返回选中凭据编号，由调用方处理切换时的关联字段。 */
const emit = defineEmits<{
  'update:modelValue': [value: number] // 原始数字凭据编号。
}>()
/** supportsPurpose 保持 DNS 专用接口兼容，同时严格限制云部署服务商和用途。 */
function supportsPurpose(item: PickerCredential) {
  return props.purpose === 'CLOUD' ? item.provider === 'ALIYUN' && item.cloudEnabled === true : item.dnsEnabled !== false
}
/** options 保留原绑定的不可用凭据，但不允许重新选择停用或用途不符的项。 */
const options = computed<SelectChoice[]>(() => {
  /** values 只使用名称、范围、脱敏身份和用途元数据。 */
  const values = props.credentials.filter((item) => (item.enabled && supportsPurpose(item)) || item.id === props.modelValue).map((item) => ({
    value: item.id,
    label: item.name,
    badge: !item.enabled ? '已停用' : !supportsPurpose(item) ? '用途不符' : '已启用',
    description: `${item.provider === 'ALIYUN' ? '阿里云' : 'Cloudflare'} · 凭据 #${item.id}`,
    details: [
      `用途：${[item.dnsEnabled !== false ? 'DNS 验证' : '', item.cloudEnabled ? '云部署' : ''].filter(Boolean).join('、') || '未启用'}`,
      `DNS 根域名：${credentialZones(item).join('、') || '未配置'}`,
      `身份：${item.maskedAccessKeyId || '未提供'}`,
    ],
    disabled: !item.enabled || !supportsPurpose(item),
  }))
  if (props.modelValue && !values.some(
    /** 检查原绑定是否仍存在于当前可见凭据中。 */
    (item) => item.value === props.modelValue)) {
    values.unshift({ value: props.modelValue, label: `凭据 #${props.modelValue}`, badge: '不可用', description: '原绑定凭据不在可用列表中，请重新选择', details: [], disabled: true })
  }
  return values
})
</script>

<template>
  <AppSelect :value="modelValue || null" :options="options" class="credential-picker" filterable show-details placeholder="搜索凭据名称、服务商或域名" :input-props="{ 'aria-label': purpose === 'DNS' ? '选择 DNS 凭据' : '选择云部署凭据' }" @update:value="emit('update:modelValue', $event)" />
</template>

<style scoped>
.credential-picker { width: 100%; }
</style>
