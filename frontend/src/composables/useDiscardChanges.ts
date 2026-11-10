/**
 * 未保存修改的统一确认交互，复用全局对话框主题并避免重复弹出。
 *
 * @author Andersen
 */
import { onBeforeUnmount } from 'vue'
import { useDialog, type DialogReactive } from 'naive-ui'

/** useDiscardChanges 仅在明确确认后丢弃草稿；取消或 Esc 保留当前编辑内容。 */
export function useDiscardChanges() {
  /** dialog 使用根组件安装的对话框上下文。 */
  const dialog = useDialog()
  /** activeDialog 限制当前表单同时只展示一个确认框。 */
  let activeDialog: DialogReactive | null = null

  /** confirmDiscard 用纯文本说明关闭影响，并在用户确认后执行调用方的草稿清理。 */
  function confirmDiscard(content: string, onDiscard: () => void) {
    if (activeDialog) return
    activeDialog = dialog.warning({
      title: '放弃未保存的修改？',
      content,
      positiveText: '放弃修改',
      negativeText: '继续编辑',
      closable: false,
      maskClosable: false,
      closeOnEsc: true,
      onPositiveClick: onDiscard,
      /** 确认框退出后允许再次发起关闭确认。 */
      onAfterLeave: () => { activeDialog = null },
    })
  }

  /** 页面离开时销毁当前确认框，避免在其他页面保留过期操作。 */
  onBeforeUnmount(() => { activeDialog?.destroy(); activeDialog = null })

  return confirmDiscard
}
