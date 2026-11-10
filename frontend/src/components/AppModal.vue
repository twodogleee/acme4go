<script setup lang="ts">
/**
 * 应用级弹窗容器，统一标题、正文滚动区与底部操作区的布局和响应式行为。
 *
 * @author Andersen
 */
import { computed, useId } from 'vue'
import { NButton, NCard, NIcon, NModal } from 'naive-ui'
import { X } from '@vicons/tabler'

/** props 是弹窗显示、标题和尺寸配置。 */
const props = defineProps<{
  open: boolean // open 表示弹窗是否可见。
  title: string // title 是弹窗标题。
  description?: string // description 是标题下方的辅助说明。
  width?: 'small' | 'medium' | 'large' // width 是预设内容宽度。
}>()

/** emit 声明弹窗关闭事件。 */
const emit = defineEmits<{
  close: [] // close 在用户请求关闭弹窗时触发。
}>()
/** titleId 是标题与对话框无障碍关联标识。 */
const titleId = useId()

/** widths 是弹窗尺寸预设与像素宽度映射。 */
const widths = {
  small: '440px',
  medium: '620px',
  large: '860px',
}

/** modalWidth 是当前尺寸预设对应的弹窗宽度。 */
const modalWidth = computed(() => widths[props.width || 'medium'])

/** handleShowChange 将遮罩或 Esc 触发的隐藏状态转换为关闭事件。 */
function handleShowChange(value: boolean) {
  if (!value) emit('close')
}
</script>

<template>
  <NModal
    :show="open"
    :mask-closable="true"
    :close-on-esc="true"
    transform-origin="center"
    @update:show="handleShowChange"
  >
    <NCard
      class="app-modal-card"
      :style="{ width: modalWidth, maxWidth: 'calc(100vw - 28px)' }"
      :bordered="false"
      content-style="padding: 0"
      role="dialog"
      aria-modal="true"
      :aria-labelledby="titleId"
    >
      <div class="app-modal-shell">
        <header class="app-modal-header">
          <div class="app-modal-heading">
            <h2 :id="titleId">{{ title }}</h2>
            <p v-if="description">{{ description }}</p>
          </div>
          <NButton class="app-modal-close" quaternary circle aria-label="关闭弹窗" @click="emit('close')">
            <template #icon><NIcon :component="X" /></template>
          </NButton>
        </header>

        <div class="app-modal-body"><slot /></div>

        <footer v-if="$slots.footer" class="app-modal-footer"><slot name="footer" /></footer>
      </div>
    </NCard>
  </NModal>
</template>

<style scoped>
.app-modal-card {
  --app-modal-max-height: calc(100vh - 48px);
  max-height: var(--app-modal-max-height);
  overflow: hidden;
  box-shadow: 0 24px 70px rgba(58, 50, 38, .2);
}
.app-modal-shell { display: flex; flex-direction: column; max-height: var(--app-modal-max-height); min-height: 0; }
.app-modal-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 20px; padding: 20px 20px 18px 24px; border-bottom: 1px solid var(--line-soft); }
.app-modal-heading { min-width: 0; }
.app-modal-heading h2 { margin: 0; color: var(--ink); font-size: 15px; font-weight: 650; line-height: 1.5; }
.app-modal-heading p { margin: 8px 0 0; color: var(--muted); font-size: 12px; line-height: 1.65; }
.app-modal-close { margin-top: -4px; flex: 0 0 auto; color: #77766d; }
.app-modal-body { min-height: 0; overflow-y: auto; overscroll-behavior: contain; padding: 24px; }
/* 表单项按自然高度顶对齐，避免网格拉伸后将较矮的输入控件垂直居中。 */
.app-modal-body :deep(.n-form-item) { min-width: 0; align-self: start; margin-bottom: 14px; }
/* 只收起表单直属末项的间距，嵌套双列中的右侧字段仍保留与左侧相同的间距。 */
.app-modal-body :deep(.n-form > .n-form-item:last-child) { margin-bottom: 0; }
.app-modal-footer { min-height: 72px; display: flex; align-items: center; justify-content: flex-end; flex: 0 0 auto; gap: 10px; padding: 15px 20px 15px 24px; background: #fbfaf7; border-top: 1px solid var(--line-soft); }
.app-modal-footer :deep(.n-button:not(.n-button--circle)) { min-width: 72px; }

@media (max-width: 640px) {
  .app-modal-card { --app-modal-max-height: calc(100vh - 20px); }
  .app-modal-header { padding: 18px 16px 16px 20px; }
  .app-modal-body { padding: 20px; }
  .app-modal-footer { min-height: 68px; padding: 13px 16px 13px 20px; }
}

@media (max-width: 420px) {
  .app-modal-footer :deep(.n-button:not(.n-button--circle)) { flex: 1; }
}
</style>
