/**
 * 管理端路由表与基于本地会话提示的访问导航守卫。
 *
 * @author Andersen
 */
import { createRouter, createWebHistory } from 'vue-router'
import AdminLayout from '@/layouts/AdminLayout.vue'

/** LoginView 延迟加载登录页。 */
const LoginView = () => import('@/views/LoginView.vue')
/** DashboardView 延迟加载运行概览页。 */
const DashboardView = () => import('@/views/DashboardView.vue')
/** OrdersView 延迟加载证书申请页。 */
const OrdersView = () => import('@/views/OrdersView.vue')
/** CertificatesView 延迟加载证书版本页。 */
const CertificatesView = () => import('@/views/CertificatesView.vue')
/** CertificateSeriesView 延迟加载证书系列页。 */
const CertificateSeriesView = () => import('@/views/CertificateSeriesView.vue')
/** CredentialsView 延迟加载统一凭据管理页。 */
const CredentialsView = () => import('@/views/CredentialsView.vue')
/** ClusterView 延迟加载 Agent 页。 */
const ClusterView = () => import('@/views/ClusterView.vue')
/** DeploymentsView 延迟加载文件部署页。 */
const DeploymentsView = () => import('@/views/DeploymentsView.vue')
/** CloudDeploymentsView 延迟加载独立云部署工作区。 */
const CloudDeploymentsView = () => import('@/views/CloudDeploymentsView.vue')
/** AuditLogsView 延迟加载审计日志页。 */
const AuditLogsView = () => import('@/views/AuditLogsView.vue')
/** AlertsView 延迟加载持久通知投递页面。 */
const AlertsView = () => import('@/views/AlertsView.vue')
/** SettingsView 延迟加载设置页。 */
const SettingsView = () => import('@/views/SettingsView.vue')
/** NotFoundView 延迟加载未找到页。 */
const NotFoundView = () => import('@/views/NotFoundView.vue')
/** router 定义登录页、管理布局和各领域页面路由。 */
const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', name: 'login', component: LoginView, meta: { public: true } },
    {
      path: '/',
      component: AdminLayout,
      children: [
        { path: '', redirect: '/dashboard' },
        { path: 'dashboard', name: 'dashboard', component: DashboardView },
        { path: 'orders', name: 'orders', component: OrdersView },
        { path: 'certificates', name: 'certificates', component: CertificatesView },
        { path: 'certificate-series', name: 'certificate-series', component: CertificateSeriesView },
        { path: 'credentials', name: 'credentials', component: CredentialsView },
        { path: 'dns-credentials', redirect: '/credentials' },
        { path: 'agents', name: 'agents', component: ClusterView },
        { path: 'deployments', name: 'deployments', component: DeploymentsView },
        { path: 'cloud-deployments', name: 'cloud-deployments', component: CloudDeploymentsView },
        { path: 'audit-logs', name: 'audit-logs', component: AuditLogsView },
        { path: 'alerts', name: 'alerts', component: AlertsView },
        { path: 'settings', name: 'settings', component: SettingsView },
      ],
    },
    { path: '/:pathMatch(.*)*', component: NotFoundView },
  ],
})

/** 路由前置回调阻止无会话提示的用户进入受保护页面。 */
router.beforeEach((to) => {
  /** hasSessionHint 表示浏览器中存在上次成功登录的用户提示。 */
  const hasSessionHint = Boolean(localStorage.getItem('acme4go.user'))
  if (!to.meta.public && !hasSessionHint) return { name: 'login', query: { redirect: to.fullPath } }
  if (to.name === 'login' && hasSessionHint) return { name: 'dashboard' }
})

export default router
