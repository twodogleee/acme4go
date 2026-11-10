/**
 * 控制平面管理 API 的类型安全客户端入口。
 *
 * @author Andersen
 */
import { download, request } from './http'
import type {
  AuthUser,
  Certificate,
  CertificateOrder,
  CertificateSeries,
  CertificateSeriesInput,
  AgentNode,
  CreateOrderInput,
  Dashboard,
  DnsCredential,
  DnsCredentialInput,
  DnsTestResult,
  DeploymentRecord,
  DeploymentTarget,
  DeploymentTargetInput,
  LoginResponse,
  Enrollment,
  SystemInfo,
  AuditLogPage,
  Page,
  Readiness,
  ListFilter,
  OrderDetail,
  AlertDelivery,
} from '@/types'

/** listQuery 对分页和筛选条件统一编码，保留完整后端查询语义。 */
function listQuery(page: number, pageSize: number, filter: ListFilter = {}): string {
  /** query 是发送给后端的安全编码参数。 */
  const query = new URLSearchParams({ page: String(page), pageSize: String(pageSize) })
  if (filter.search) query.set('search', filter.search)
  if (filter.status) query.set('status', filter.status)
  if (filter.seriesId) query.set('seriesId', filter.seriesId)
  return query.toString()
}

/** api 汇总管理端各领域接口，避免页面直接拼接请求路径。 */
export const api = {
  /** login 创建管理员会话。 */
  login: (username: string, password: string) =>
    request<LoginResponse>('/api/auth/login', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    }),
  /** me 查询当前登录管理员。 */
  me: () => request<AuthUser>('/api/auth/me'),
  /** logout 注销当前管理员会话。 */
  logout: () => request<void>('/api/auth/logout', { method: 'POST' }),
  /** changePassword 修改管理员密码并撤销现有会话。 */
  changePassword: (currentPassword: string, newPassword: string) =>
    request<void>('/api/auth/password', {
      method: 'POST',
      body: JSON.stringify({ currentPassword, newPassword }),
    }),
  /** dashboard 查询运行概览聚合指标。 */
  dashboard: (signal?: AbortSignal) => request<Dashboard>('/api/dashboard', { signal }),
  /** readiness 查询控制平面及内部 Agent TLS 服务的真实就绪状态。 */
  readiness: () => request<Readiness>('/readyz'),
  /** listOrders 查询证书订单列表。 */
  listOrders: (page = 1, pageSize = 50, filter: ListFilter = {}, signal?: AbortSignal) => request<Page<CertificateOrder>>(`/api/orders?${listQuery(page, pageSize, filter)}`, { signal }),
  /** orderDetail 查询订单最新状态和持久执行时间线。 */
  orderDetail: (id: number, signal?: AbortSignal) => request<OrderDetail>(`/api/orders/${id}`, { signal }),
  /** createOrder 提交新的证书申请。 */
  createOrder: (input: CreateOrderInput) =>
    request<CertificateOrder>('/api/orders', { method: 'POST', body: JSON.stringify(input) }),
  /** retryOrder 重新提交指定失败订单。 */
  retryOrder: (id: number) => request<CertificateOrder>(`/api/orders/${id}/retry`, { method: 'POST' }),
  /** listCertificates 查询证书库元数据。 */
  listCertificates: (page = 1, pageSize = 50, filter: ListFilter = {}, signal?: AbortSignal) => request<Page<Certificate>>(`/api/certificates?${listQuery(page, pageSize, filter)}`, { signal }),
  /** listCertificateSeries 查询可用于部署和续期的稳定证书系列。 */
  listCertificateSeries: () => request<CertificateSeries[]>('/api/certificate-series'),
  /** updateCertificateSeries 显式更新系列长期续期配置。 */
  updateCertificateSeries: (id: string, input: CertificateSeriesInput) =>
    request<CertificateSeries>(`/api/certificate-series/${id}`, { method: 'PUT', body: JSON.stringify(input) }),
  /** downloadCertificate 下载指定类型的证书材料。 */
  downloadCertificate: (id: number, type: 'CERTIFICATE' | 'FULL_CHAIN' | 'PRIVATE_KEY') =>
    download(`/api/certificates/${id}/download?type=${type}`),
  /** listDnsCredentials 查询脱敏 DNS 凭据。 */
  listDnsCredentials: (signal?: AbortSignal) => request<DnsCredential[]>('/api/dns-credentials', { signal }),
  /** createDnsCredential 创建加密 DNS 凭据。 */
  createDnsCredential: (input: DnsCredentialInput) =>
    request<DnsCredential>('/api/dns-credentials', { method: 'POST', body: JSON.stringify(input) }),
  /** updateDnsCredential 更新指定 DNS 凭据。 */
  updateDnsCredential: (id: number, input: DnsCredentialInput) =>
    request<DnsCredential>(`/api/dns-credentials/${id}`, { method: 'PUT', body: JSON.stringify(input) }),
  /** deleteDnsCredential 删除未被引用的 DNS 凭据。 */
  deleteDnsCredential: (id: number) => request<void>(`/api/dns-credentials/${id}`, { method: 'DELETE' }),
  /** testDnsCredential 验证指定 DNS 凭据权限。 */
  testDnsCredential: (id: number) =>
    request<DnsTestResult>(`/api/dns-credentials/${id}/test`, { method: 'POST' }),
  /** listAgents 查询已注册部署节点。 */
  listAgents: () => request<AgentNode[]>('/api/agents'),
  /** createEnrollment 创建一次性 Agent 加入信息。 */
  createEnrollment: (name: string) =>
    request<Enrollment>('/api/agents/enrollments', {
      method: 'POST',
      body: JSON.stringify({ name }),
    }),
  /** revokeAgent 撤销指定 Agent 身份。 */
  revokeAgent: (id: string) => request<void>(`/api/agents/${id}`, { method: 'DELETE' }),
  /** listDeploymentTargets 查询全部文件部署目标。 */
  listDeploymentTargets: () => request<DeploymentTarget[]>('/api/deployments/targets'),
  /** createDeploymentTarget 创建文件部署目标。 */
  createDeploymentTarget: (input: DeploymentTargetInput) =>
    request<DeploymentTarget>('/api/deployments/targets', {
      method: 'POST',
      body: JSON.stringify(input),
    }),
  /** updateDeploymentTarget 修改文件部署目标。 */
  updateDeploymentTarget: (id: number, input: DeploymentTargetInput) =>
    request<DeploymentTarget>(`/api/deployments/targets/${id}`, {
      method: 'PUT',
      body: JSON.stringify(input),
    }),
  /** deleteDeploymentTarget 删除文件部署目标配置。 */
  deleteDeploymentTarget: (id: number) =>
    request<void>(`/api/deployments/targets/${id}`, { method: 'DELETE' }),
  /** deployTarget 同步本机目标或登记远程单次同步请求。 */
  deployTarget: (id: number) =>
    request<DeploymentTarget>(`/api/deployments/targets/${id}/sync`, { method: 'POST' }),
  /** listDeploymentRecords 查询最近部署历史。 */
  listDeploymentRecords: () => request<DeploymentRecord[]>('/api/deployments/records'),
  /** systemInfo 查询脱敏运行配置摘要。 */
  systemInfo: () => request<SystemInfo>('/api/system/info'),
  /** listAlerts 按投递状态读取通知记录。 */
  listAlerts: (page = 1, status = 'ALL', signal?: AbortSignal) => request<Page<AlertDelivery>>(`/api/alerts?${listQuery(page, 50, { status })}`, { signal }),
  /** testAlert 创建测试通知，实际结果由投递记录反映。 */
  testAlert: () => request<void>('/api/alerts/test', { method: 'POST' }),
  /** retryAlert 将失败或等待重试的通知重新入队。 */
  retryAlert: (id: number) => request<void>(`/api/alerts/${id}/retry`, { method: 'POST' }),
  /** listAuditLogs 按游标和可选筛选条件查询管理审计记录。 */
  listAuditLogs: (cursor?: number, action = '', resourceType = '') => {
    /** query 是审计接口的安全编码查询参数。 */
    const query = new URLSearchParams({ limit: '50' })
    if (cursor) query.set('cursor', String(cursor))
    if (action) query.set('action', action)
    if (resourceType) query.set('resourceType', resourceType)
    return request<AuditLogPage>(`/api/audit-logs?${query}`)
  },
}
