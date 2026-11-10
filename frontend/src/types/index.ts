/**
 * 管理端与控制平面共享的接口模型和状态枚举。
 *
 * @author Andersen
 */

/** ApiResponse 描述后端统一 JSON 响应信封。 */
export interface ApiResponse<T> {
  success: boolean // success 表示请求是否成功完成。
  data: T // data 是响应的业务数据。
  message: string // message 是面向用户的结果说明。
  timestamp: string // timestamp 是服务端生成响应的 UTC 时间。
}

/** Page 描述管理端大列表的页码分页响应。 */
export interface Page<T> {
  items: T[] // items 是当前页记录。
  total: number // total 是全部记录数量。
  page: number // page 是从一开始的当前页码。
  pageSize: number // pageSize 是单页记录数。
  summary?: CertificateCounts // summary 是证书筛选范围内的完整聚合数量。
}

/** CertificateCounts 是不受分页限制的证书生命周期统计。 */
export interface CertificateCounts {
  total: number // total 是筛选结果总数。
  valid: number // valid 是有效且尚未进入续期窗口的版本数。
  expiring: number // expiring 是即将过期的版本数。
  expired: number // expired 是已经过期的版本数。
}

/** ListFilter 是后端执行的列表筛选条件。 */
export interface ListFilter {
  search?: string // search 是完整记录范围内的关键词。
  status?: string // status 是状态编码或 ALL。
  seriesId?: string // seriesId 是可选证书系列。
}

/** OrderEvent 描述持久化的脱敏执行阶段。 */
export interface OrderEvent {
  id: number // id 是事件顺序标识。
  orderId: number // orderId 是所属订单。
  stage: OrderStage // stage 是业务阶段。
  message: string // message 是安全说明。
  createdAt: string // createdAt 是 UTC 事件时间。
}

/** OrderDetail 同时提供最新订单快照和完整阶段历史。 */
export interface OrderDetail {
  order: CertificateOrder // order 是最新执行状态。
  events: OrderEvent[] // events 按事件发生顺序排列。
}

/** AlertDelivery 是持久通知的业务内容和投递状态。 */
export interface AlertDelivery {
  id: number // id 是接收端幂等去重标识。
  event: string // event 是通知类型。
  severity: 'INFO' | 'WARNING' | 'ERROR' // severity 是通知级别。
  resourceType: string // resourceType 是资源类型。
  resourceId: string // resourceId 是资源标识。
  message: string // message 是脱敏内容。
  timestamp: string // timestamp 是原始事件时间。
  status: 'PENDING' | 'SENDING' | 'RETRY' | 'SENT' | 'FAILED' // status 是投递进度。
  attempts: number // attempts 是本轮尝试次数。
  nextAttemptAt: string // nextAttemptAt 是下次发送时间。
  lastError: string | null // lastError 是最近投递失败摘要。
  deliveredAt: string | null // deliveredAt 是成功送达时间。
}

/** Readiness 描述控制平面就绪检查结果。 */
export interface Readiness {
  status: string // status 是 READY 就绪状态编码。
}

/** AuthUser 描述当前登录管理员的公开信息。 */
export interface AuthUser {
  id: number // id 是管理员数据库主键。
  username: string // username 是登录用户名。
  displayName: string // displayName 是界面显示名称。
}

/** LoginResponse 描述登录成功后的 CSRF 和用户信息。 */
export interface LoginResponse {
  csrfToken: string // csrfToken 是状态变更请求需要回传的令牌。
  expiresAt: string // expiresAt 是会话过期时间。
  user: AuthUser // user 是当前登录管理员。
}

/** CertificateStatus 是证书生命周期状态。 */
export type CertificateStatus = 'VALID' | 'EXPIRING' | 'EXPIRED'
/** OrderStatus 是证书订单总体状态。 */
export type OrderStatus = 'QUEUED' | 'RUNNING' | 'SUCCESS' | 'FAILED'
/** OrderStage 是证书订单的细分执行阶段。 */
export type OrderStage =
  | 'QUEUED'
  | 'PREPARING_ACCOUNT'
  | 'CREATING_ORDER'
  | 'AUTHORIZING_DNS'
  | 'ISSUING'
  | 'STORING'
  | 'SYNCING'
  | 'COMPLETED'
  | 'FAILED'

/** Dashboard 描述运行概览的聚合指标。 */
export interface Dashboard {
  historyVersionTotal: number // historyVersionTotal 是包含历史版本的证书库总量。
  upcomingCertificates: Certificate[] // upcomingCertificates 是在用版本中最早到期的五份证书。
  certificateTotal: number // certificateTotal 是证书总数。
  certificateValid: number // certificateValid 是有效证书数。
  certificateExpiring: number // certificateExpiring 是即将过期证书数。
  certificateExpired: number // certificateExpired 是已过期证书数。
  orderRunning: number // orderRunning 是执行中订单数。
  orderFailed: number // orderFailed 是失败订单数。
  agentOnline: number // agentOnline 是在线 Agent 数。
  agentTotal: number // agentTotal 是 Agent 总数。
  nearestExpiry: string | null // nearestExpiry 是最近证书过期时间。
  role: 'server' // role 是当前控制平面角色。
  localDeploymentEnabled: boolean // localDeploymentEnabled 表示本机执行器是否启用。
}

/** DnsCredential 描述脱敏后的 DNS 服务商凭据。 */
export interface DnsCredential {
  id: number // id 是凭据主键。
  name: string // name 是凭据名称。
  provider: 'ALIYUN' | 'CLOUDFLARE' // provider 是 DNS 服务商类型。
  zone: string // zone 是首个根域名，兼容旧接口。
  zones?: string[] // zones 是完整根域名范围；未返回时回退到 zone。
  lastTestResults?: DnsZoneTestResult[] // lastTestResults 是最近一次逐域名测试结果。
  maskedAccessKeyId: string // maskedAccessKeyId 是脱敏访问密钥标识。
  enabled: boolean // enabled 表示签发任务是否可以使用该凭据。
  lastTestStatus: 'SUCCESS' | 'FAILED' | null // lastTestStatus 是最近测试状态。
  lastTestMessage: string | null // lastTestMessage 是最近测试说明。
  lastTestAt: string | null // lastTestAt 是最近测试时间。
  createdAt: string // createdAt 是凭据创建时间。
  updatedAt: string // updatedAt 是凭据更新时间。
}

/** DnsCredentialInput 是创建或修改 DNS 凭据的表单数据。 */
export interface DnsCredentialInput {
  name: string // name 是凭据名称。
  provider: 'ALIYUN' | 'CLOUDFLARE' // provider 是 DNS 服务商类型。
  zone: string // zone 是兼容旧接口的首个根域名。
  zones?: string[] // zones 提供时作为完整根域名范围，空数组不代表全部域名。
  accessKeyId: string // accessKeyId 是访问密钥标识。
  accessKeySecret: string // accessKeySecret 是访问密钥秘密。
  enabled: boolean // enabled 表示是否启用凭据。
}

/** DnsZoneTestResult 描述单根域名的实际查询、写入和清理结果。 */
export interface DnsZoneTestResult {
  zone: string // zone 是已配置的根域名。
  success: boolean // success 表示该根域名所有测试步骤成功。
  message: string // message 是脱敏的结果说明。
  testedAt: string // testedAt 是本项测试开始时间。
}

/** DnsTestResult 描述凭据连通性测试结果。 */
export interface DnsTestResult {
  success: boolean // success 表示所有根域名是否通过。
  results: DnsZoneTestResult[] // results 包含逐根域名的结果，部分失败也完整返回。
  message: string // message 是测试结果说明。
  testedAt: string // testedAt 是测试执行时间。
}

/** CertificateOrder 描述一笔 ACME 证书签发任务。 */
export interface CertificateOrder {
  id: number // id 是订单主键。
  seriesId: string // seriesId 是订单所属证书系列的稳定标识。
  seriesCode: string // seriesCode 是系列的可读编码。
  seriesName: string // seriesName 是系列显示名称。
  domains: string[] // domains 是申请覆盖的域名。
  email: string // email 是 ACME 联系邮箱。
  caEnvironment: 'LETS_ENCRYPT_PRODUCTION' | 'LETS_ENCRYPT_STAGING' // caEnvironment 是 CA 环境。
  credentialId: number // credentialId 是 DNS 凭据主键。
  status: OrderStatus // status 是订单总体状态。
  stage: OrderStage // stage 是当前执行阶段。
  errorMessage: string | null // errorMessage 是失败原因。
  certificateId: number | null // certificateId 是成功签发的证书主键。
  startedAt: string | null // startedAt 是任务开始时间。
  completedAt: string | null // completedAt 是任务完成时间。
  createdAt: string // createdAt 是订单创建时间。
  updatedAt: string // updatedAt 是订单更新时间。
}

/** CreateOrderInput 是新建证书订单的表单数据。 */
export interface CreateOrderInput {
  name: string // name 是证书系列显示名称。
  domains: string[] // domains 是证书域名列表。
  email: string // email 是 ACME 联系邮箱。
  caEnvironment: CertificateOrder['caEnvironment'] // caEnvironment 是目标 CA 环境。
  credentialId: number // credentialId 是 DNS 凭据主键。
  agreeTerms: boolean // agreeTerms 表示是否同意 CA 条款。
}

/** Certificate 描述证书库中的一份证书元数据。 */
export interface Certificate {
  id: number // id 是证书主键。
  seriesId: string // seriesId 是证书所属系列的稳定标识。
  seriesCode: string // seriesCode 是证书所属系列编码。
  commonName: string // commonName 是证书主名称。
  sans: string[] // sans 是证书覆盖的 DNS 名称。
  status: CertificateStatus // status 是生命周期状态。
  issuer: string // issuer 是签发者名称。
  serialNumber: string // serialNumber 是证书序列号。
  notBefore: string // notBefore 是证书生效时间。
  notAfter: string // notAfter 是证书过期时间。
  fingerprint: string // fingerprint 是证书 SHA-256 指纹。
  version: number // version 是同一证书系列内的递增版本。
  createdAt: string // createdAt 是记录创建时间。
  updatedAt: string // updatedAt 是记录更新时间。
}

/** CertificateSeries 描述部署与续期共同引用的稳定证书系列。 */
export interface CertificateSeries {
  id: string // id 是系列不可变标识。
  code: string // code 是系列可读唯一编码。
  name: string // name 是系列显示名称。
  domains: string[] // domains 是系列覆盖的完整域名组合。
  email: string // email 是后续签发与自动续期使用的联系邮箱。
  caEnvironment: CertificateOrder['caEnvironment'] // caEnvironment 是系列 CA 环境。
  credentialId: number // credentialId 是续期使用的 DNS 凭据主键。
  currentCertificateId: number | null // currentCertificateId 是当前版本证书主键。
  currentVersion: number // currentVersion 是当前系列版本号。
  currentStatus: CertificateStatus | null // currentStatus 是当前证书生命周期状态。
  currentNotAfter: string | null // currentNotAfter 是当前证书到期时间。
  autoRenew: boolean // autoRenew 表示系列是否自动续期。
  enabled: boolean // enabled 表示系列是否可用于签发和部署。
  revision: number // revision 是系列配置修订号。
  createdAt: string // createdAt 是系列创建时间。
  updatedAt: string // updatedAt 是系列更新时间。
}

/** CertificateSeriesInput 描述可显式修改的系列长期配置。 */
export interface CertificateSeriesInput {
  name: string // name 是系列显示名称。
  email: string // email 是 ACME 联系邮箱。
  credentialId: number // credentialId 是续期使用的 DNS 凭据主键。
  autoRenew: boolean // autoRenew 表示是否自动创建续期订单。
  enabled: boolean // enabled 表示系列是否允许签发和部署。
}

/** AuditLog 描述一条不包含敏感明文的管理操作记录。 */
export interface AuditLog {
  id: number // id 是审计记录主键。
  userId: number | null // userId 是管理员主键，系统任务可为空。
  username: string // username 是管理员登录名。
  action: string // action 是操作动作。
  resourceType: string // resourceType 是资源类型。
  resourceId: string // resourceId 是资源稳定标识。
  detail: string // detail 是操作摘要。
  createdAt: string // createdAt 是操作时间。
}

/** AuditLogPage 描述基于游标的审计记录分页结果。 */
export interface AuditLogPage {
  items: AuditLog[] // items 是当前页记录。
  nextCursor: number | null // nextCursor 是下一页游标。
}

/** AgentNode 描述已注册部署节点的状态。 */
export interface AgentNode {
  id: string // id 是 Agent 稳定身份标识。
  name: string // name 是 Agent 名称。
  status: 'ONLINE' | 'OFFLINE' | 'REVOKED' // status 是节点在线状态。
  enabled: boolean // enabled 表示节点是否允许工作。
  certificateExpiresAt: string // certificateExpiresAt 是客户端证书过期时间。
  lastSeenAt: string | null // lastSeenAt 是最近在线时间。
  version: string // version 是 Agent 应用版本。
  platform: string // platform 是操作系统与架构。
  targetCount: number // targetCount 是绑定部署目标数。
  createdAt: string // createdAt 是注册时间。
  updatedAt: string // updatedAt 是更新时间。
}

/** Enrollment 描述一次性 Agent 注册信息。 */
export interface Enrollment {
  joinUrl: string // joinUrl 是交给 Agent 使用的加入链接。
  expiresAt: string // expiresAt 是加入链接过期时间。
}

/** DeploymentStatus 是部署目标最近执行状态。 */
export type DeploymentStatus = 'PENDING' | 'RUNNING' | 'SUCCESS' | 'FAILED' | 'NEEDS_BINDING'
/** ExecutorType 是部署目标的执行器类型。 */
export type ExecutorType = 'LOCAL' | 'AGENT'

/** DeploymentTarget 描述本机或远程证书文件部署目标。 */
export interface DeploymentTarget {
  manualSync: boolean // manualSync 表示持久等待或正在执行的远程单次同步。
  id: number // id 是目标主键。
  executorType: ExecutorType // executorType 是本机或 Agent 执行类型。
  agentId: string | null // agentId 是远程执行节点标识。
  executorName: string // executorName 是执行器显示名称。
  name: string // name 是目标名称。
  certificateSeriesId: string | null // certificateSeriesId 是目标绑定的证书系列标识。
  seriesCode: string // seriesCode 是绑定系列的可读编码。
  seriesName: string // seriesName 是绑定系列的显示名称。
  seriesDomains: string[] // seriesDomains 是绑定系列的完整域名组合。
  commonName: string // commonName 是匹配证书的主域名。
  targetAlias: string // targetAlias 是部署目录别名。
  runtimePath: string // runtimePath 是业务应用稳定读取路径。
  enabled: boolean // enabled 表示是否自动部署。
  status: DeploymentStatus // status 是最近部署状态。
  lastCertificateId: number | null // lastCertificateId 是最近部署证书主键。
  lastFingerprint: string | null // lastFingerprint 是最近部署证书指纹。
  lastDeployedAt: string | null // lastDeployedAt 是最近成功部署时间。
  lastError: string | null // lastError 是最近失败说明。
  createdAt: string // createdAt 是目标创建时间。
  updatedAt: string // updatedAt 是目标更新时间。
}

/** DeploymentTargetInput 是创建或修改部署目标的表单数据。 */
export interface DeploymentTargetInput {
  executorType: ExecutorType // executorType 是执行器类型。
  agentId: string | null // agentId 是远程 Agent 标识。
  name: string // name 是目标名称。
  certificateSeriesId: string // certificateSeriesId 是人工选择的证书系列标识。
  targetAlias: string // targetAlias 是安全目录别名。
  enabled: boolean // enabled 表示是否启用自动部署。
}

/** DeploymentRecord 描述一次部署结果审计记录。 */
export interface DeploymentRecord {
  id: number // id 是记录主键。
  targetId: number | null // targetId 是部署目标主键。
  executorType: ExecutorType // executorType 是实际执行器类型。
  agentId: string | null // agentId 是实际执行 Agent 标识。
  certificateId: number | null // certificateId 是部署证书主键。
  fingerprint: string | null // fingerprint 是部署证书指纹。
  status: DeploymentStatus // status 是部署结果状态。
  message: string // message 是部署结果说明。
  createdAt: string // createdAt 是记录创建时间。
}

/** SystemInfo 描述设置页展示的脱敏运行配置。 */
export interface SystemInfo {
  version: string // version 是应用版本。
  runtime: string // runtime 是后端运行时名称。
  role: 'server' // role 是当前运行角色。
  localDeploymentEnabled: boolean // localDeploymentEnabled 表示本机执行器是否启用。
  adminUrl: string // adminUrl 是管理端公开地址。
  agentUrl: string // agentUrl 是 Agent mTLS 接口地址。
  deploymentDir: string // deploymentDir 是本机部署根目录。
  renewBeforeDays: number // renewBeforeDays 是提前续期天数。
  pollIntervalSeconds: number // pollIntervalSeconds 是 Agent 对账间隔秒数。
  historyRetentionDays: number // historyRetentionDays 是历史记录和部署版本保留天数。
  alertWebhookEnabled: boolean // alertWebhookEnabled 表示失败告警是否已配置。
}
