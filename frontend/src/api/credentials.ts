/** 统一凭据 API，DNS 签发和云部署共用秘密。 @author Andersen */
import { request } from './http'
import type { Credential, CredentialInput } from '@/types/credentials'

/** credentialsApi 提供脱敏列表与共享凭据维护。 */
export const credentialsApi = {
  /** list 查询全部用途，可取消过期请求。 */
  list: (signal?: AbortSignal) => request<Credential[]>('/api/credentials', { signal }),
  /** save 以明确主键执行新增或更新。 */
  save: (id: number | null, input: CredentialInput) => request<Credential>(id ? `/api/credentials/${id}` : '/api/credentials', { method: id ? 'PUT' : 'POST', body: JSON.stringify(input) }),
  /** remove 删除未被引用的共享凭据。 */
  remove: (id: number) => request<void>(`/api/credentials/${id}`, { method: 'DELETE' }),
}
