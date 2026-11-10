<script setup lang="ts">
/**
 * 管理端主布局，负责导航、用户状态、响应式侧栏和页面内容承载。
 *
 * @author Andersen
 */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NAvatar, NButton, NIcon, NTooltip } from 'naive-ui'
import {
  Activity,
  Affiliate,
  Certificate,
  CloudUpload,
  Dashboard,
  FileCertificate,
  Folder,
  History,
  Key,
  Logout,
  Menu2,
  Settings,
  ShieldLock,
  Versions,
} from '@vicons/tabler'
import { api } from '@/api'
import { useAuthStore } from '@/stores/auth'
import BrandMark from '@/components/BrandMark.vue'

/** auth 是管理员身份状态仓库。 */
const auth = useAuthStore()
/** route 是当前路由信息。 */
const route = useRoute()
/** router 用于退出后替换到登录页。 */
const router = useRouter()
/** sidebarOpen 表示移动端侧栏是否展开。 */
const sidebarOpen = ref(false)
/** serviceReady 是最近一次就绪检查结果，空值表示尚未完成检查。 */
const serviceReady = ref<boolean | null>(null)
/** healthTimer 是周期检查服务就绪状态的定时器。 */
let healthTimer: number | undefined

/** navigation 是管理端功能导航定义。 */
const navigation = [
  { to: '/dashboard', label: '运行概览', description: '状态与指标', icon: Dashboard },
  { to: '/orders', label: '证书申请', description: 'ACME 工作流', icon: FileCertificate },
  { to: '/certificates', label: '证书管理', description: '证书与私钥', icon: Certificate },
  { to: '/certificate-series', label: '证书系列', description: '续期长期配置', icon: Versions },
  { to: '/credentials', label: '凭据管理', description: 'DNS 验证 · 云部署', icon: Key },
  { to: '/agents', label: '部署 Agent', description: '注册与心跳', icon: Affiliate },
  { to: '/deployments', label: '文件部署', description: 'Nginx 与 EMQX', icon: Folder },
  { to: '/cloud-deployments', label: '云部署', description: '证书上传 · OSS · CDN', icon: CloudUpload },
  { to: '/audit-logs', label: '审计日志', description: '敏感操作追溯', icon: History },
  { to: '/alerts', label: '告警投递', description: '通知与失败重试', icon: History },
  { to: '/settings', label: '系统设置', description: '运行与安全', icon: Settings },
]

/** activeItem 是与当前路径匹配的导航项目。 */
const activeItem = computed(() => navigation.find((item) => route.path.startsWith(item.to)))

/** logout 注销服务端会话并返回登录页。 */
async function logout() {
  await auth.logout()
  await router.replace('/login')
}

/** checkReadiness 从真实就绪端点刷新服务状态，避免展示静态健康结论。 */
async function checkReadiness() {
  try {
    await api.readiness()
    serviceReady.value = true
  } catch {
    serviceReady.value = false
  }
}

onMounted(() => {
  void checkReadiness()
  healthTimer = window.setInterval(checkReadiness, 30_000)
})
onBeforeUnmount(() => {
  if (healthTimer) window.clearInterval(healthTimer)
})
</script>

<template>
  <div class="admin-shell">
    <Transition name="overlay">
      <button v-if="sidebarOpen" class="mobile-overlay" type="button" aria-label="关闭导航" @click="sidebarOpen = false" />
    </Transition>

    <aside class="sidebar" :class="{ 'sidebar-open': sidebarOpen }">
      <RouterLink class="brand" to="/dashboard" @click="sidebarOpen = false">
        <BrandMark />
        <span class="brand-copy"><strong>Acme4go</strong><small>证书管理</small></span>
      </RouterLink>

      <div class="sidebar-section-label">功能导航</div>
      <nav class="main-nav" aria-label="主导航">
        <RouterLink
          v-for="item in navigation"
          :key="item.to"
          :to="item.to"
          @click="sidebarOpen = false"
        >
          <NIcon class="nav-icon" :size="20" :component="item.icon" />
          <span class="nav-copy"><strong>{{ item.label }}</strong><small>{{ item.description }}</small></span>
          <span class="nav-active-dot" />
        </RouterLink>
      </nav>

      <div class="sidebar-foot">
        <div class="security-note">
          <span class="security-icon"><NIcon :size="19" :component="ShieldLock" /></span>
          <div><strong>安全存储已启用</strong><small>凭据与私钥均已加密</small></div>
        </div>
        <div class="profile-row">
          <NAvatar round :size="38">{{ auth.user?.displayName?.slice(0, 1) || 'A' }}</NAvatar>
          <div><strong>{{ auth.user?.displayName || '管理员' }}</strong><small>@{{ auth.user?.username || 'admin' }}</small></div>
          <NTooltip placement="top">
            <template #trigger>
              <NButton quaternary circle aria-label="退出登录" @click="logout">
                <template #icon><NIcon :size="18" :component="Logout" /></template>
              </NButton>
            </template>
            退出登录
          </NTooltip>
        </div>
      </div>
    </aside>

    <div class="main-column">
      <header class="desktop-topbar">
        <div class="topbar-title">
          <span>证书管理 /</span>
          <strong>{{ activeItem?.label || '证书控制台' }}</strong>
        </div>
        <button class="topbar-state" :class="{ failed: serviceReady === false }" type="button" @click="checkReadiness">
          <NIcon :size="14" :component="Activity" />
          <span>{{ serviceReady === null ? '正在检查服务' : serviceReady ? '服务已就绪' : '服务未就绪' }}</span>
        </button>
      </header>

      <header class="mobile-header">
        <NButton quaternary circle aria-label="打开导航" @click="sidebarOpen = true">
          <template #icon><NIcon :size="22" :component="Menu2" /></template>
        </NButton>
        <strong>{{ activeItem?.label || 'Acme4go' }}</strong>
        <span class="mobile-brand-dot" />
      </header>

      <main class="main-content">
        <RouterView v-slot="{ Component, route: currentRoute }">
          <Transition name="page" mode="out-in">
            <component :is="Component" :key="currentRoute.path" />
          </Transition>
        </RouterView>
      </main>
    </div>
  </div>
</template>

<style scoped>
.admin-shell { min-height: 100vh; background: var(--canvas); }
.sidebar { position: fixed; inset: 0 auto 0 0; z-index: 40; width: 244px; padding: 26px 16px 18px; display: flex; flex-direction: column; color: var(--ink); background: var(--nav); border-right: 1px solid var(--line); }
.brand { display: flex; align-items: center; gap: 12px; margin: 0 8px 34px; }
.brand-copy strong, .brand-copy small { display: block; }
.brand-copy strong { color: #30322c; font-size: 19px; letter-spacing: -.4px; }
.brand-copy small { margin-top: 2px; color: #8b897f; font-size: 10px; }
.sidebar-section-label { padding: 0 12px 10px; color: #969287; font-size: 10px; font-weight: 650; }
.main-nav { display: grid; gap: 4px; }
.main-nav a { position: relative; min-height: 52px; display: grid; grid-template-columns: 24px 1fr; align-items: center; gap: 11px; padding: 0 12px; color: #77786f; border-radius: 9px; transition: color .18s ease, background .18s ease; }
.main-nav a:hover { color: #3f5348; background: #f1efe9; }
.main-nav a.router-link-active { color: #344d40; background: #e9eee9; }
.main-nav a.router-link-active::before { content: ''; position: absolute; left: 0; width: 3px; height: 22px; border-radius: 0 3px 3px 0; background: #5d7768; }
.nav-icon { color: #8e9188; transition: color .18s ease; }
.router-link-active .nav-icon { color: #4d6859; }
.nav-copy strong, .nav-copy small { display: block; }
.nav-copy strong { font-size: 13px; font-weight: 650; }
.nav-copy small { margin-top: 2px; color: #aaa69c; font-size: 10px; font-weight: 400; }
.router-link-active .nav-copy small { color: #7d887f; }
.nav-active-dot { display: none; }
.sidebar-foot { margin-top: auto; }
.security-note { display: grid; grid-template-columns: 34px 1fr; align-items: center; gap: 10px; margin: 0 4px 14px; padding: 12px; border: 1px solid #e1ded5; border-radius: 10px; background: #f5f2eb; }
.security-icon { width: 34px; height: 34px; display: grid; place-items: center; color: #526b5d; background: #e5ebe5; border-radius: 8px; }
.security-note strong, .security-note small, .profile-row strong, .profile-row small { display: block; }
.security-note strong, .profile-row strong { color: #4a4c44; font-size: 11px; }
.security-note small, .profile-row small { margin-top: 3px; color: #949087; font-size: 9px; }
.profile-row { display: grid; grid-template-columns: 38px 1fr 36px; align-items: center; gap: 10px; padding: 14px 4px 0; border-top: 1px solid var(--line); }
.profile-row :deep(.n-avatar) { color: #fff; background: #718579; font-weight: 700; }
.profile-row :deep(.n-button) { color: #87847a; }
.profile-row :deep(.n-button:hover) { color: #3d5549; }
.main-column { min-width: 0; min-height: 100vh; margin-left: 244px; }
.desktop-topbar { height: 64px; display: flex; align-items: center; justify-content: space-between; padding: 0 42px; background: rgba(255, 254, 250, .88); border-bottom: 1px solid var(--line); backdrop-filter: blur(14px); }
.topbar-title { display: flex; align-items: center; gap: 7px; font-size: 12px; }
.topbar-title span { color: #a09c92; }
.topbar-title strong { color: #5a5c54; }
.topbar-state { display: flex; align-items: center; gap: 7px; padding: 6px 10px; color: #5a6c61; background: #eef1ec; border: 1px solid #dfe5df; border-radius: 999px; font: inherit; font-size: 11px; cursor: pointer; }
.topbar-state.failed { color: #9c514c; border-color: #ead0cd; background: #fbefed; }
.main-content { width: min(1440px, 100%); margin: 0 auto; padding: 38px 42px 72px; }
.mobile-header, .mobile-overlay { display: none; }
.page-enter-active, .page-leave-active { transition: opacity .16s ease, transform .16s ease; }
.page-enter-from { opacity: 0; transform: translateY(5px); }
.page-leave-to { opacity: 0; transform: translateY(-3px); }
.overlay-enter-active, .overlay-leave-active { transition: opacity .2s ease; }
.overlay-enter-from, .overlay-leave-to { opacity: 0; }
@media (max-width: 960px) {
  .sidebar { transform: translateX(-100%); transition: transform .22s ease; }
  .sidebar-open { transform: translateX(0); }
  .mobile-overlay { position: fixed; inset: 0; z-index: 35; display: block; width: 100%; border: 0; background: rgba(45, 42, 35, .32); backdrop-filter: blur(2px); }
  .main-column { margin-left: 0; }
  .desktop-topbar { display: none; }
  .mobile-header { height: 60px; display: grid; grid-template-columns: 42px 1fr 42px; align-items: center; padding: 0 14px; color: #393b34; background: #fffefa; border-bottom: 1px solid var(--line); }
  .mobile-header :deep(.n-button) { color: #61665e; }
  .mobile-header strong { font-size: 13px; text-align: center; }
  .mobile-brand-dot { justify-self: center; width: 10px; height: 12px; border: 2px solid #5c7667; border-radius: 2px; }
  .main-content { padding: 28px 22px 60px; }
}
@media (max-width: 640px) { .main-content { padding: 24px 14px 52px; } }
</style>
