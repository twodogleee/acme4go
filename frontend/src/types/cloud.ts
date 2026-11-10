/** 云证书同步、产品能力和持久任务的前端契约。 @author Andersen */
/** 产品字段驱动动态表单，新增产品不需要改动页面分支。 */
export interface CloudField {
  key: string // 配置键。
  label: string // 字段名称。
  placeholder: string // 示例值。
}

/** 后端注册的产品能力。 */
export interface CloudProduct {
  id: string // 适配器标识。
  name: string // 产品名称。
  fields: CloudField[] // 所需资源配置。
}

/** 上传或部署配置的编辑契约。 */
export interface CloudPlanInput {
  name: string // 规则名称。
  credentialId: number // 执行所用的共享凭据标识。
  kind: 'UPLOAD' | 'DEPLOY' // 上传规则或部署目标。
  product: string // 产品适配器标识。
  seriesId: string | null // 自动跟随的本地系列。
  remoteCertificateId: string // 固定的云端证书，保留字符串精度。
  config: Record<string, string> // 无秘密的产品资源定位参数。
  autoSync: boolean // 续期后是否自动执行。
  enabled: boolean // 规则启用状态。
}

/** 已保存的云配置。 */
export interface CloudPlan extends CloudPlanInput {
  id: number // 本地配置标识。
  revision: number // 后端配置修订号。
}

/** 云端证书元数据，不含私钥。 */
export interface CloudCertificate {
  id: string // 云证书标识。
  name: string // 云证书名称。
  domains: string[] // 域名覆盖范围。
  fingerprint: string // SHA-256 指纹。
  notAfter: string // 到期日期。
}

/** 云证书分页结果。 */
export interface CloudCertificatePage {
  items: CloudCertificate[] // 当前页证书。
  total: number // 匹配总数。
}

/** 持久云任务结果。 */
export interface CloudJob {
  id: number // 任务编号。
  planId: number // 关联配置。
  planName: string // 配置名称快照展示。
  revision: number // 执行配置修订。
  certificateId: number | null // 本地证书版本。
  remoteCertificateId: string // 实际云证书 ID。
  status: 'QUEUED' | 'RUNNING' | 'SUCCESS' | 'FAILED' // 任务状态。
  stage: string // 上传、部署或验证阶段。
  message: string // 安全结果说明。
  createdAt: string // 创建时间。
  updatedAt: string // 最近更新时间。
}
