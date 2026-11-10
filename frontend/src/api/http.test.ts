/**
 * 管理端 HTTP 客户端的 CSRF、统一响应和会话失效行为测试。
 *
 * @author Andersen
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, csrfStorage, request } from './http'

/** TestPayload 是统一响应数据解包测试使用的最小业务对象。 */
interface TestPayload {
  id: number // id 是模拟业务对象主键。
}

/** MemoryStorage 为 Node 测试环境提供符合浏览器接口的内存存储。 */
class MemoryStorage implements Storage {
  /** values 保存当前测试用例的键值。 */
  private readonly values = new Map<string, string>()

  /** length 返回当前键数量。 */
  get length() {
    return this.values.size
  }

  /** clear 删除全部键。 */
  clear() {
    this.values.clear()
  }

  /** getItem 读取键值。 */
  getItem(key: string) {
    return this.values.get(key) ?? null
  }

  /** key 按插入顺序读取指定键。 */
  key(index: number) {
    return [...this.values.keys()][index] ?? null
  }

  /** removeItem 删除指定键。 */
  removeItem(key: string) {
    this.values.delete(key)
  }

  /** setItem 保存字符串键值。 */
  setItem(key: string, value: string) {
    this.values.set(key, value)
  }
}

// HTTP 客户端测试套件验证管理会话最关键的浏览器边界。
describe('HTTP client', () => {
  /** fetchMock 捕获请求并返回每个测试指定的响应。 */
  const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>()
  /** locationAssign 记录会话失效后的登录页跳转。 */
  const locationAssign = vi.fn()

  // 前置回调安装浏览器 API 替身并清空请求记录。
  beforeEach(() => {
    fetchMock.mockReset()
    locationAssign.mockReset()
    vi.stubGlobal('fetch', fetchMock)
    vi.stubGlobal('localStorage', new MemoryStorage())
    vi.stubGlobal('location', { pathname: '/dashboard', assign: locationAssign })
    vi.stubGlobal('window', { setTimeout, clearTimeout })
  })

  // 后置回调移除全局替身，避免测试间共享状态。
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  // 变更请求测试回调验证 CSRF 和统一响应数据解包。
  it('adds CSRF to mutations and unwraps response data', async () => {
    csrfStorage.set('csrf-value')
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ success: true, data: { id: 7 } }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))

    await expect(request<TestPayload>('/api/test', { method: 'POST', body: '{}' })).resolves.toEqual({ id: 7 })
    /** options 是 fetch 收到的最终请求参数。 */
    const options = fetchMock.mock.calls[0]?.[1]
    expect(options?.credentials).toBe('same-origin')
    expect(new Headers(options?.headers).get('Content-Type')).toBe('application/json')
    expect(new Headers(options?.headers).get('X-CSRF-Token')).toBe('csrf-value')
  })

  // 只读请求测试回调验证不会错误附加 CSRF 请求头。
  it('does not add CSRF to read-only requests', async () => {
    csrfStorage.set('csrf-value')
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ success: true, data: [] }), { status: 200 }))

    await request('/api/test')
    /** options 是只读请求的最终请求参数。 */
    const options = fetchMock.mock.calls[0]?.[1]
    expect(new Headers(options?.headers).has('X-CSRF-Token')).toBe(false)
  })

  // 未授权测试回调验证本地会话清理、稳定错误和登录页跳转。
  it('clears local session and redirects on unauthorized responses', async () => {
    csrfStorage.set('csrf-value')
    localStorage.setItem('acme4go.user', '{"id":1}')
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ success: false, message: '会话已失效' }), {
      status: 401,
      headers: { 'Content-Type': 'application/json' },
    }))

    await expect(request('/api/test')).rejects.toEqual(new ApiError('会话已失效', 401))
    expect(csrfStorage.get()).toBeNull()
    expect(localStorage.getItem('acme4go.user')).toBeNull()
    expect(locationAssign).toHaveBeenCalledWith('/login')
  })
})
