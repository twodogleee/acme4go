/**
 * 页面共享的通知反馈组合式函数。
 *
 * @author Andersen
 */
import { useNotification } from 'naive-ui'

/** useToast 返回统一展示时长和悬停行为的通知方法。 */
export function useToast() {
  /** notification 是 Naive UI 通知控制器。 */
  const notification = useNotification()

  return {
    /** success 展示成功通知。 */
    success: (title: string, detail?: string) => notification.success({ title, content: detail, duration: 4200, keepAliveOnHover: true }),
    /** error 展示错误通知。 */
    error: (title: string, detail?: string) => notification.error({ title, content: detail, duration: 5200, keepAliveOnHover: true }),
    /** info 展示一般信息通知。 */
    info: (title: string, detail?: string) => notification.info({ title, content: detail, duration: 4200, keepAliveOnHover: true }),
  }
}
