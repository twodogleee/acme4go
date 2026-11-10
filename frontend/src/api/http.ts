/**
 * 统一处理管理端 HTTP 鉴权、CSRF、超时和错误归一化。
 *
 * @author Andersen
 */
import type { ApiResponse } from '@/types'

/** CSRF_KEY 是浏览器本地保存 CSRF 令牌的键。 */
const CSRF_KEY = 'acme4go.csrf'
/** API_TIMEOUT_MS 是普通接口最长等待时间。 */
const API_TIMEOUT_MS = 30_000
/** DOWNLOAD_TIMEOUT_MS 是证书文件下载最长等待时间。 */
const DOWNLOAD_TIMEOUT_MS = 120_000

/** ApiError 是可供页面直接展示的稳定接口错误。 */
export class ApiError extends Error {
  /** constructor 创建带 HTTP 状态码的稳定接口错误。 */
  constructor(
    message: string,
    /** status 是 HTTP 状态码；网络和超时错误使用零。 */
    public readonly status: number,
  ) {
    super(message)
  }
}

/** request 发送 JSON API 请求，并保证服务端失联时页面能在限定时间内退出加载状态。 */
export async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  /** headers 是在调用方请求头基础上构造的可变请求头集合。 */
  const headers = new Headers(options.headers)
  if (options.body && !(options.body instanceof FormData)) {
    headers.set('Content-Type', 'application/json')
  }
  /** method 是规范化为大写的 HTTP 方法。 */
  const method = (options.method || 'GET').toUpperCase()
  if (!['GET', 'HEAD', 'OPTIONS'].includes(method)) {
    /** csrfToken 是状态变更请求携带的本地 CSRF 令牌。 */
    const csrfToken = localStorage.getItem(CSRF_KEY)
    if (csrfToken) headers.set('X-CSRF-Token', csrfToken)
  }
  return executeFetch(
    path,
    { ...options, headers, credentials: 'same-origin' },
    API_TIMEOUT_MS,
    /** JSON 消费回调校验统一响应信封并处理会话失效。 */
    async (response) => {
      /** payload 是解析后的统一接口响应信封。 */
      const payload = (await response.json().catch(
        /** JSON 解析失败回调将非 JSON 响应归一化为空。 */
        () => null,
      )) as ApiResponse<T> | null
      if (!response.ok || !payload?.success) {
        if (response.status === 401) {
          localStorage.removeItem(CSRF_KEY)
          localStorage.removeItem('acme4go.user')
          if (!location.pathname.startsWith('/login')) location.assign('/login')
        }
        throw new ApiError(payload?.message || `请求失败（${response.status}）`, response.status)
      }
      return payload.data
    },
  )
}

/** download 下载证书文件，并限制包括响应正文读取在内的最长等待时间。 */
export async function download(path: string): Promise<void> {
  /** headers 是证书下载请求使用的请求头集合。 */
  const headers = new Headers()
  /** csrfToken 是私钥下载所需的本地 CSRF 令牌。 */
  const csrfToken = localStorage.getItem(CSRF_KEY)
  if (csrfToken) headers.set('X-CSRF-Token', csrfToken)
  await executeFetch(
    path,
    { credentials: 'same-origin', headers },
    DOWNLOAD_TIMEOUT_MS,
    /** 文件消费回调校验响应并触发浏览器保存。 */
    async (response) => {
      if (!response.ok) {
        /** payload 是下载失败时的统一接口响应信封。 */
        const payload = (await response.json().catch(
          /** JSON 解析失败回调将非 JSON 下载错误归一化为空。 */
          () => null,
        )) as ApiResponse<never> | null
        throw new ApiError(payload?.message || '文件下载失败', response.status)
      }
      /** disposition 是服务端返回的下载文件名响应头。 */
      const disposition = response.headers.get('Content-Disposition') || ''
      /** utf8Name 是 RFC 5987 编码的 UTF-8 文件名。 */
      const utf8Name = disposition.match(/filename\*=UTF-8''([^;]+)/i)?.[1]
      /** plainName 是传统 Content-Disposition 文件名。 */
      const plainName = disposition.match(/filename="?([^";]+)"?/i)?.[1]
      /** filename 是解码并回退后的实际下载文件名。 */
      const filename = decodeURIComponent(utf8Name || plainName || 'certificate.pem')
      /** url 是下载 Blob 的临时对象地址。 */
      const url = URL.createObjectURL(await response.blob())
      /** anchor 是触发浏览器下载的临时链接元素。 */
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = filename
      anchor.click()
      URL.revokeObjectURL(url)
    },
  )
}

/** executeFetch 组合调用方取消信号与超时信号，并把网络异常转换为统一错误。 */
async function executeFetch<T>(
  path: string,
  options: RequestInit,
  timeoutMs: number,
  consume: (response: Response) => Promise<T>,
): Promise<T> {
  /** controller 组合调用方取消与内部超时信号。 */
  const controller = new AbortController()
  let timedOut = false
  /** abortFromCaller 将调用方取消原因转发到内部控制器。 */
  const abortFromCaller = () => controller.abort(options.signal?.reason)
  if (options.signal?.aborted) {
    abortFromCaller()
  } else {
    options.signal?.addEventListener('abort', abortFromCaller, { once: true })
  }
  /** timeout 是到期后取消请求的定时器句柄。 */
  const timeout = window.setTimeout(() => {
    timedOut = true
    controller.abort()
  }, timeoutMs)
  try {
    /** response 是在组合取消信号保护下返回的 HTTP 响应。 */
    const response = await fetch(path, { ...options, signal: controller.signal })
    return await consume(response)
  } catch (error) {
    if (error instanceof ApiError) throw error
    if (timedOut) throw new ApiError('请求超时，请检查服务端运行状态后重试', 0)
    if (controller.signal.aborted) throw new ApiError('请求已取消', 0)
    throw new ApiError('无法连接服务端，请检查网络或服务端日志', 0)
  } finally {
    window.clearTimeout(timeout)
    options.signal?.removeEventListener('abort', abortFromCaller)
  }
}

/** csrfStorage 只在浏览器本地保存 CSRF 原始令牌，会话 Cookie 仍由浏览器安全管理。 */
export const csrfStorage = {
  /** get 读取当前 CSRF 原始令牌。 */
  get: () => localStorage.getItem(CSRF_KEY),
  /** set 保存登录响应返回的 CSRF 原始令牌。 */
  set: (token: string) => localStorage.setItem(CSRF_KEY, token),
  /** remove 清理当前 CSRF 原始令牌。 */
  remove: () => localStorage.removeItem(CSRF_KEY),
}
