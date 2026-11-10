/** 共享凭据的用途和身份保护契约。 @author Andersen */
import type { DnsCredential, DnsCredentialInput } from '@/types'

/** Credential 在一份秘密上声明 DNS 与云部署用途。 */
export interface Credential extends DnsCredential {
  dnsEnabled: boolean // dnsEnabled 允许用于 DNS 验证。
  cloudEnabled: boolean // cloudEnabled 允许用于云证书及产品部署。
  accessKeyIdLocked: boolean // accessKeyIdLocked 表示云身份已固定，不允许更换 AccessKeyId。
}

/** CredentialInput 显式指定用途；纯云部署可不填写根域名。 */
export interface CredentialInput extends DnsCredentialInput {
  dnsEnabled: boolean // dnsEnabled 开启时根域名必填。
  cloudEnabled: boolean // cloudEnabled 当前仅阿里云可用。
}
