<script setup lang="ts">
/**
 * 统一业务操作按钮的图标、层级与尺寸；业务事件和表单属性透传给原生按钮。
 *
 * @author Andersen
 */
import { computed } from 'vue'
import { NButton, NIcon } from 'naive-ui'
import { ArrowLeft, ArrowRight, Check, ChevronDown, Cloud, Copy, Download, Edit, Eye, FileCertificate, History, Key, Lock, Plus, Refresh, Search, Send, Trash, Upload, X } from '@vicons/tabler'

/** icons 统一相同操作在不同页面的图标，不根据按钮文案猜测行为。 */
const icons = {
  detail: Eye, // 查看对象详情。
  expand: ChevronDown, // 原位展开，展开后旋转箭头。
  create: Plus, // 新增业务对象。
  edit: Edit, // 修改配置。
  save: Check, // 保存或确认表单。
  refresh: Refresh, // 重新获取数据或重试。
  delete: Trash, // 删除或撤销。
  close: X, // 取消或关闭。
  search: Search, // 执行查询。
  credential: Key, // 进入凭据管理。
  certificate: FileCertificate, // 证书申请及查看。
  deploy: Upload, // 提交部署或同步。
  cloud: Cloud, // 查看云端资源。
  send: Send, // 发送通知。
  copy: Copy, // 复制结果。
  download: Download, // 下载证书材料。
  security: Lock, // 身份和密码操作。
  back: ArrowLeft, // 返回上级或最新记录。
  next: ArrowRight, // 前往其他页面。
  history: History, // 查看较早记录。
}

/** props 显式声明操作语义；列表使用 small，页面与表单使用 medium。 */
const props = withDefaults(defineProps<{
  action: keyof typeof icons // action 决定固定图标。
  tone?: 'neutral' | 'primary' | 'accent' | 'danger' // tone 区分页面主操作、强调操作及破坏性操作。
  size?: 'small' | 'medium' | 'large' // size 统一点击区域高度。
  loading?: boolean // loading 展示进度，并禁用原生按钮以阻止重复表单提交。
  disabled?: boolean // disabled 保留调用方的权限或业务状态约束。
}>(), { tone: 'neutral', size: 'medium', loading: false, disabled: false })

/** buttonType 将业务层级映射至组件库颜色，描边保持统一。 */
const buttonType = computed(() => props.tone === 'danger' ? 'error' : props.tone === 'neutral' ? 'default' : 'primary')
</script>

<template>
  <NButton class="app-button" :class="`app-button--${tone}`" :type="buttonType" :ghost="tone !== 'primary'" :size="size" :loading="loading" :disabled="disabled || loading" :aria-busy="loading">
    <template #icon><NIcon class="app-button-icon" :component="icons[action]" aria-hidden="true" /></template>
    <slot />
  </NButton>
</template>

<style scoped>
.app-button { flex-shrink: 0; vertical-align: middle; }
.app-button:focus-visible { outline: 2px solid var(--teal); outline-offset: 3px; }
.app-button-icon { transition: transform .16s ease; }
.app-button[aria-expanded="true"] .app-button-icon { transform: rotate(180deg); }
@media (prefers-reduced-motion: reduce) { .app-button-icon { transition: none; } }
</style>
