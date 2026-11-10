<script setup lang="ts">
/**
 * 通用状态徽标组件，统一业务状态中文名称与语义颜色。
 *
 * @author Andersen
 */
import { computed } from 'vue'
import { NTag } from 'naive-ui'

/** props 是状态编码和可选覆盖文案。 */
const props = defineProps<{
  status: string // status 是后端返回的业务状态编码。
  label?: string // label 是可选的自定义显示文本。
}>()

/** statusLabels 是业务状态编码与中文名称映射。 */
const statusLabels: Record<string, string> = {
  VALID: '有效',
  EXPIRING: '即将到期',
  EXPIRED: '已过期',
  QUEUED: '排队中',
  RUNNING: '执行中',
  SUCCESS: '成功',
  FAILED: '失败',
  ONLINE: '在线',
  OFFLINE: '离线',
  REVOKED: '已撤销',
  UNKNOWN: '未知',
  PRIMARY: '主节点',
  FOLLOWER: '跟随节点',
  PUSH: '推送',
  PULL: '接收',
  PENDING: '待部署',
  NEEDS_BINDING: '需要绑定',
  DISABLED: '已停用',
}

/** type 是状态对应的 Naive UI 语义颜色类型。 */
const type = computed(() => {
  if (props.status === 'DISABLED') return 'default'
  if (['VALID', 'SUCCESS', 'ONLINE', 'PRIMARY'].includes(props.status)) return 'success'
  if (['EXPIRING', 'QUEUED', 'RUNNING', 'PENDING', 'NEEDS_BINDING'].includes(props.status)) return 'warning'
  if (['EXPIRED', 'FAILED', 'OFFLINE', 'REVOKED'].includes(props.status)) return 'error'
  return 'info'
})
</script>

<template>
  <NTag :type="type" size="small" round :bordered="false">{{ label || statusLabels[status] || status }}</NTag>
</template>
