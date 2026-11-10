/**
 * 管理员身份状态仓库，协调服务端会话、CSRF 令牌与本地展示缓存。
 *
 * @author Andersen
 */
import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { api } from '@/api'
import { csrfStorage } from '@/api/http'
import type { AuthUser } from '@/types'

/** USER_KEY 是本地管理员展示缓存的存储键。 */
const USER_KEY = 'acme4go.user'

/** useAuthStore 提供登录、会话恢复、退出和本地状态清理能力。 */
export const useAuthStore = defineStore('auth', () => {
  /** user 是当前登录管理员信息。 */
  const user = ref<AuthUser | null>(readStoredUser())
  /** authenticated 表示当前是否有可展示的登录身份。 */
  const authenticated = computed(() => Boolean(user.value))

  /** login 创建服务端会话并保存 CSRF 令牌与用户缓存。 */
  async function login(username: string, password: string) {
    /** result 是服务端返回的登录会话数据。 */
    const result = await api.login(username, password)
    csrfStorage.set(result.csrfToken)
    localStorage.setItem(USER_KEY, JSON.stringify(result.user))
    user.value = result.user
  }

  /** restore 通过服务端会话恢复当前用户，失败时清理本地提示。 */
  async function restore() {
    try {
      user.value = await api.me()
      localStorage.setItem(USER_KEY, JSON.stringify(user.value))
      return true
    } catch {
      clear()
      return false
    }
  }

  /** logout 请求注销服务端会话，并保证本地状态始终被清理。 */
  async function logout() {
    try {
      await api.logout()
    } finally {
      clear()
    }
  }

  /** clear 清理 CSRF 令牌、用户缓存和响应式身份。 */
  function clear() {
    csrfStorage.remove()
    localStorage.removeItem(USER_KEY)
    user.value = null
  }

  return { user, authenticated, login, restore, logout, clear }
})

/** readStoredUser 安全解析本地管理员展示缓存。 */
function readStoredUser(): AuthUser | null {
  try {
    /** value 是本地保存的管理员 JSON 文本。 */
    const value = localStorage.getItem(USER_KEY)
    return value ? (JSON.parse(value) as AuthUser) : null
  } catch {
    return null
  }
}
