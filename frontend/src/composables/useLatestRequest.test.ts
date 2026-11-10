/**
 * 验证翻页、慢响应、失败和卸载期间只允许最新请求更新页面。
 *
 * @author Andersen
 */
import { effectScope } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import { useLatestRequest } from './useLatestRequest'

/** deferred 创建与当前项目 TypeScript 标准库兼容的可控制异步结果。 */
function deferred<T>() {
  /** resolve 完成合成响应。 */
  let resolve!: (value: T) => void
  /** reject 拒绝合成响应。 */
  let reject!: (reason: unknown) => void
  /** promise 保存尚未完成的合成请求。 */
  const promise = new Promise<T>((accept, fail) => { resolve = accept; reject = fail })
  return { promise, resolve, reject }
}

// 最新请求测试覆盖不遵循取消信号的慢接口和组件生命周期。
describe('useLatestRequest', () => {
  // 页码已经变化时旧页即使更晚返回也不能替换新页。
  it('applies the newest page when responses finish out of order', async () => {
    /** scope 模拟页面所属响应式生命周期。 */
    const scope = effectScope()
    /** query 是页面请求控制器。 */
    const query = scope.run(useLatestRequest)!
    /** first 是忽略取消信号的第一页请求。 */
    const first = deferred<string>()
    /** second 是后发的第二页请求。 */
    const second = deferred<string>()
    /** accept 记录实际应用到页面的数据。 */
    const accept = vi.fn()
    /** failure 记录应向用户展示的错误。 */
    const failure = vi.fn()
    /** oldRequest 模拟旧接口继续等待响应。 */
    const oldRequest = query.run(() => first.promise, accept, failure)
    /** newRequest 模拟用户立即切换到第二页。 */
    const newRequest = query.run(() => second.promise, accept, failure)
    second.resolve('page-2')
    await newRequest
    first.resolve('page-1')
    await oldRequest
    expect(accept.mock.calls).toEqual([['page-2']])
    expect(failure).not.toHaveBeenCalled()
    expect(query.pending.value).toBe(false)
    scope.stop()
  })

  // 被取消的旧错误不产生提示，也不能结束新请求的加载态。
  it('ignores stale failures while the current request remains pending', async () => {
    /** scope 是独立页面生命周期。 */
    const scope = effectScope()
    /** query 管理两个相互竞争的请求。 */
    const query = scope.run(useLatestRequest)!
    /** first 是即将失败的旧请求。 */
    const first = deferred<string>()
    /** second 是仍在加载的新请求。 */
    const second = deferred<string>()
    /** failure 记录页面提示。 */
    const failure = vi.fn()
    /** oldRequest 保留旧请求完成句柄。 */
    const oldRequest = query.run(() => first.promise, vi.fn(), failure)
    /** currentRequest 保留新请求完成句柄。 */
    const currentRequest = query.run(() => second.promise, vi.fn(), failure)
    first.reject(new Error('old failure'))
    await oldRequest
    expect(failure).not.toHaveBeenCalled()
    expect(query.pending.value).toBe(true)
    expect(query.loading.value).toBe(true)
    second.resolve('done')
    await currentRequest
    scope.stop()
  })

  // 页面卸载立即取消网络信号，晚到结果不能再修改状态。
  it('cancels and suppresses results after unmount', async () => {
    /** scope 模拟被销毁的页面。 */
    const scope = effectScope()
    /** query 是待销毁的请求控制器。 */
    const query = scope.run(useLatestRequest)!
    /** response 延迟到页面销毁之后完成。 */
    const response = deferred<string>()
    /** accept 是不应再运行的数据更新。 */
    const accept = vi.fn()
    /** observedSignal 用于确认网络取消已经传递。 */
    let observedSignal: AbortSignal | undefined
    /** task 记录网络信号并模拟慢接口。 */
    const task = (signal: AbortSignal) => { observedSignal = signal; return response.promise }
    /** pending 是未完成的页面请求。 */
    const pending = query.run(task, accept, vi.fn())
    scope.stop()
    expect(observedSignal?.aborted).toBe(true)
    response.resolve('late')
    await pending
    expect(accept).not.toHaveBeenCalled()
  })
})
