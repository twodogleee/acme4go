/** 凭据根域名的表单解析和旧接口兼容展示，不读取密钥材料。 @author Andersen */
import type { DnsCredential } from '@/types'

/** credentialZones 返回完整范围；旧接口没有 zones 时回退到单个 zone。 */
export function credentialZones(value: Pick<DnsCredential, 'zone' | 'zones'>): string[] {
  return value.zones ?? (value.zone ? [value.zone] : [])
}

/** parseCredentialZones 支持逐行或逗号粘贴，规范化大小写并去重；格式校验由后端执行。 */
export function parseCredentialZones(value: string): string[] {
  return [...new Set(value.split(/[\s,，;；]+/).filter(Boolean).map(
    /** 去除 DNS 完整名称的末尾点，保持和服务端相同的规范形式。 */
    (zone) => zone.toLowerCase().replace(/\.$/, ''),
  ))]
}
