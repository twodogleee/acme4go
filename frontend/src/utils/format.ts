/**
 * 页面共享的日期、剩余天数、短标识和订单阶段格式化方法。
 *
 * @author Andersen
 */

/** formatDate 将 ISO 时间格式化为中文日期或日期时间。 */
export function formatDate(value?: string | null, withTime = true) {
  if (!value) return '—'
  /** date 是解析后的日期对象。 */
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    ...(withTime ? { hour: '2-digit', minute: '2-digit', hour12: false } : {}),
  }).format(date)
}

/** daysUntil 计算目标时间距当前时间的向上取整天数。 */
export function daysUntil(value?: string | null) {
  if (!value) return null
  return Math.ceil((new Date(value).getTime() - Date.now()) / 86_400_000)
}

/** shortId 按指定长度截断较长标识。 */
export function shortId(value?: string | null, size = 10) {
  if (!value) return '—'
  return value.length <= size ? value : `${value.slice(0, size)}…`
}

/** stageLabel 将订单执行阶段转换为中文说明。 */
export function stageLabel(stage: string) {
  /** labels 是后端订单阶段与中文名称的映射。 */
  const labels: Record<string, string> = {
    QUEUED: '等待调度',
    PREPARING_ACCOUNT: '准备 ACME 账户',
    CREATING_ORDER: '创建 CA 订单',
    AUTHORIZING_DNS: 'DNS 所有权验证',
    ISSUING: '等待 CA 签发',
    STORING: '加密保存文件',
    SYNCING: '更新部署状态',
    COMPLETED: '申请完成',
    FAILED: '申请失败',
  }
  return labels[stage] || stage
}
