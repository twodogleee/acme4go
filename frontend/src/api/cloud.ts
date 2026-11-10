/** 云部署独立 API 客户端，复用管理端鉴权和 CSRF。 @author Andersen */
import { request } from './http'
import type { CloudCertificatePage, CloudJob, CloudPlan, CloudPlanInput, CloudProduct } from '@/types/cloud'

/** cloudApi 定义上传规则、部署目标和任务接口。 */
export const cloudApi = {
  /** products 查询可扩展产品字段。 */
  products: () => request<CloudProduct[]>('/api/cloud/products'),
  /** certificates 查询云端已有有效证书，不包含私钥。 */
  certificates: (id: number, page: number, search: string, signal?: AbortSignal) => request<CloudCertificatePage>(`/api/credentials/${id}/cloud-certificates?${new URLSearchParams({ page: String(page), search })}`, { signal }),
  /** plans 查询独立上传和部署配置。 */
  plans: (signal?: AbortSignal) => request<CloudPlan[]>('/api/cloud/plans', { signal }),
  /** savePlan 保存配置；启用自动同步的规则由工作者处理。 */
  savePlan: (id: number | null, input: CloudPlanInput) => request<CloudPlan>(`/api/cloud/plans${id ? `/${id}` : ''}`, { method: id ? 'PUT' : 'POST', body: JSON.stringify(input) }),
  /** deletePlan 删除配置并保留云端证书和绑定。 */
  deletePlan: (id: number) => request<void>(`/api/cloud/plans/${id}`, { method: 'DELETE' }),
  /** runPlan 创建持久异步任务。 */
  runPlan: (id: number) => request<CloudJob>(`/api/cloud/plans/${id}/run`, { method: 'POST' }),
  /** jobs 以游标读取任务历史。 */
  jobs: (before = 0, signal?: AbortSignal) => request<CloudJob[]>(`/api/cloud/jobs?before=${before}`, { signal }),
}
