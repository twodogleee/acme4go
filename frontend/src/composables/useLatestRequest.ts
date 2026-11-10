/**
 * 管理可取消的最新请求，防止翻页、搜索和卸载后的旧响应覆盖页面。
 *
 * @author Andersen
 */
import { onScopeDispose, ref } from 'vue'

/** useLatestRequest 为一个独立加载区域创建最新请求及生命周期边界。 */
export function useLatestRequest() {
  /** loading 表示当前请求需要显示局部加载态。 */
  const loading = ref(false)
  /** pending 包含静默刷新，用于避免轮询打断用户查询。 */
  const pending = ref(false)
  /** sequence 区分先后请求，忽略不遵循取消信号的旧响应。 */
  let sequence = 0
  /** controller 取消上一次网络请求。 */
  let controller: AbortController | undefined

  /** cancel 同时取消网络操作和已返回但尚未应用的响应。 */
  function cancel() {
    sequence += 1
    controller?.abort()
    pending.value = false
    loading.value = false
  }

  /** run 只将最后一次请求结果交给调用方；被替代和卸载的请求不会触发错误提示。 */
  async function run<T>(
    task: (signal: AbortSignal) => Promise<T>,
    accept: (value: T) => void,
    failure: (error: unknown) => void,
    silent = false,
  ) {
    cancel()
    /** requestSequence 是本次请求的顺序快照。 */
    const requestSequence = sequence
    controller = new AbortController()
    /** signal 是本次请求独立的取消信号。 */
    const signal = controller.signal
    pending.value = true
    loading.value = !silent
    try {
      /** value 是等待调用方应用的业务数据。 */
      const value = await task(signal)
      if (sequence === requestSequence && !signal.aborted) accept(value)
    } catch (error) {
      if (sequence === requestSequence && !signal.aborted) failure(error)
    } finally {
      if (sequence === requestSequence) {
        loading.value = false
        pending.value = false
      }
    }
  }

  onScopeDispose(cancel)
  return { loading, pending, run, cancel }
}
