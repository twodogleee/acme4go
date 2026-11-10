<script setup lang="ts">
/** 云执行记录的分组详情，列表展开行与目标关联记录共用相同字段展示。 @author Andersen */
import { computed } from 'vue'
import DetailFields from '@/components/DetailFields.vue'
import type { DetailField } from '@/types/details'
import type { CloudJob } from '@/types/cloud'
import { formatDate } from '@/utils/format'

/** props 使用当前任务及文案，轮询时直接更新，不保存独立快照。 */
const props = defineProps<{
  job: CloudJob // 当前任务记录。
  statusLabel: string // 执行状态的中文说明。
  stageLabel: string // 执行阶段的中文说明。
  message: string // 完整结果或等待提示，按纯文本渲染。
}>()

/** fields 按任务、证书和结果组织安全元数据。 */
const fields = computed<DetailField[]>(() => [
  { group: '执行信息', label: '规则 / 目标', value: props.job.planName },
  { group: '执行信息', label: '任务编号', value: `#${props.job.id}` },
  { group: '执行信息', label: '执行状态', value: props.statusLabel, status: props.job.status },
  { group: '执行信息', label: '执行阶段', value: props.stageLabel },
  { group: '执行信息', label: '创建时间', value: formatDate(props.job.createdAt) },
  { group: '执行信息', label: '更新时间', value: formatDate(props.job.updatedAt) },
  { group: '证书关联', label: '云证书 ID', value: props.job.remoteCertificateId || '尚未关联', mono: true },
  { group: '证书关联', label: '本地证书版本', value: props.job.certificateId === null ? '未关联本地证书' : `#${props.job.certificateId}` },
  { group: '执行结果', label: props.job.status === 'FAILED' ? '失败原因' : '结果说明', value: props.message, wide: true, error: props.job.status === 'FAILED' },
])
</script>

<template>
  <DetailFields class="cloud-job-details" :fields="fields" />
</template>

<style scoped>
.cloud-job-details { padding: 18px; border: 1px solid var(--line-soft); border-radius: 8px; background: var(--panel); }
</style>
