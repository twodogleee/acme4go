<script setup lang="ts">
/**
 * 运行概览页面，聚合证书、订单和 Agent 的关键指标与近期记录。
 *
 * @author Andersen
 */
import { Certificate as CertificateIcon } from '@vicons/tabler'
import AppButton from '@/components/AppButton.vue'
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { NCard, NIcon, NSpin } from 'naive-ui'
import { AlertTriangle, CircleCheck, FileCertificate, Server } from '@vicons/tabler'
import { api } from '@/api'
import type { Certificate, CertificateOrder, Dashboard } from '@/types'
import PageHeader from '@/components/PageHeader.vue'
import StatusBadge from '@/components/StatusBadge.vue'
import EmptyState from '@/components/EmptyState.vue'
import { daysUntil, formatDate, stageLabel } from '@/utils/format'
import { useToast } from '@/composables/useToast'
import { useLatestRequest } from '@/composables/useLatestRequest'

/** dashboard 是服务端运行概览指标。 */
const dashboard = ref<Dashboard | null>(null)
/** certificates 是证书列表快照。 */
const certificates = ref<Certificate[]>([])
/** orders 是证书订单列表快照。 */
const orders = ref<CertificateOrder[]>([])
/** loading 表示概览数据是否正在加载。 */
const query = useLatestRequest()
/** loading 是概览区域的最新请求状态。 */
const loading = query.loading
/** serviceReady 是真实就绪端点返回的状态。 */
const serviceReady = ref(false)
/** refreshTimer 定期刷新可见页面的运维数据。 */
let refreshTimer: number | undefined
/** toast 提供统一页面通知。 */
const toast = useToast()

/** recentOrders 是最近五笔证书订单。 */
const recentOrders = computed(() => orders.value.slice(0, 5))
/** upcomingCertificates 是按到期时间由近到远排列的前五份证书。 */
const upcomingCertificates = computed(() => certificates.value)

/** load 并行刷新概览、证书和订单数据。 */
async function load(silent = false) {
  await query.run(
    /** 概览直接使用后端的在用版本聚合，不再下载第一页历史版本计算预警。 */
    (signal) => Promise.all([api.dashboard(signal), api.listOrders(1, 5, {}, signal), readReadiness()]),
    /** 最新快照同时更新指标、观察列表和服务就绪状态。 */
    ([dashboardData, orderData, ready]) => {
      dashboard.value = dashboardData
      certificates.value = dashboardData.upcomingCertificates
      orders.value = orderData.items
      serviceReady.value = ready
    },
    /** 接口失败时取消健康结论，并为人工刷新提供错误提示。 */
    (error) => { serviceReady.value = false; if (!silent) toast.error('概览加载失败', error instanceof Error ? error.message : undefined) },
    silent,
  )
}

/** readReadiness 将就绪检查失败转换成页面可展示的降级状态。 */
async function readReadiness(): Promise<boolean> {
  try { await api.readiness(); return true } catch { return false }
}

/** 页面挂载后开始刷新，并只在可见且无请求时静默轮询。 */
onMounted(() => {
  void load()
  /** 定期接管后台续期与部署造成的状态变化。 */
  refreshTimer = window.setInterval(() => { if (document.visibilityState === 'visible' && !query.pending.value) void load(true) }, 30_000)
})
/** 卸载后停止概览轮询。 */
onUnmounted(() => window.clearInterval(refreshTimer))
</script>

<template>
  <div>
    <PageHeader eyebrow="工作台" title="运行概览" description="证书生命周期、签发任务与部署 Agent 的实时运行状态。">
      <template #actions>
        <AppButton action="refresh" :loading="loading" @click="load()">刷新数据</AppButton>
        <RouterLink to="/orders"><AppButton action="create" tone="primary">申请证书</AppButton></RouterLink>
      </template>
    </PageHeader>

    <div v-if="loading" class="panel loading-block"><NSpin size="small" />正在汇总运行状态…</div>
    <template v-else-if="dashboard">
      <section class="node-strip">
        <div class="node-identity">
          <span class="node-glyph"><NIcon :size="20" :component="Server" /></span>
          <div class="node-copy"><span>当前运行角色</span><strong>{{ dashboard.localDeploymentEnabled ? 'Server + 本机执行器' : 'Server · 仅控制平面' }}</strong></div>
        </div>
        <StatusBadge :status="serviceReady ? 'ONLINE' : 'FAILED'" :label="serviceReady ? '服务就绪' : '服务异常'" />
        <div class="node-metric"><span>Agent 在线</span><strong>{{ dashboard.agentOnline }}<small>/{{ dashboard.agentTotal }}</small></strong></div>
        <div class="node-metric"><span>最近到期</span><strong>{{ formatDate(dashboard.nearestExpiry, false) }}</strong></div>
        <div class="live-indicator">{{ serviceReady ? '状态每 30 秒刷新' : '请检查服务状态' }}</div>
      </section>

      <section class="metric-grid">
        <article class="metric-card metric-total">
          <div class="metric-top"><span>在用证书</span><b><NIcon :size="18" :component="CertificateIcon" /></b></div>
          <strong>{{ dashboard.certificateTotal }}</strong>
          <small>当前与部署版本 · 证书库共 {{ dashboard.historyVersionTotal }} 个版本</small>
          <i :style="{ width: dashboard.certificateTotal ? '76%' : '8%' }" />
        </article>
        <article class="metric-card metric-valid">
          <div class="metric-top"><span>有效证书</span><b><NIcon :size="18" :component="CircleCheck" /></b></div>
          <strong>{{ dashboard.certificateValid }}</strong>
          <small>处于正常有效期</small>
          <i :style="{ width: dashboard.certificateTotal ? `${dashboard.certificateValid / dashboard.certificateTotal * 100}%` : '0' }" />
        </article>
        <article class="metric-card metric-warning">
          <div class="metric-top"><span>需要关注</span><b><NIcon :size="18" :component="AlertTriangle" /></b></div>
          <strong>{{ dashboard.certificateExpiring + dashboard.certificateExpired }}</strong>
          <small>{{ dashboard.certificateExpiring }} 即将到期 · {{ dashboard.certificateExpired }} 已过期</small>
          <i :style="{ width: dashboard.certificateTotal ? `${(dashboard.certificateExpiring + dashboard.certificateExpired) / dashboard.certificateTotal * 100}%` : '0' }" />
        </article>
        <article class="metric-card metric-orders">
          <div class="metric-top"><span>申请任务</span><b><NIcon :size="18" :component="FileCertificate" /></b></div>
          <strong>{{ dashboard.orderRunning }}</strong>
          <small>执行中 · {{ dashboard.orderFailed }} 个失败记录</small>
          <i :style="{ width: dashboard.orderRunning ? '55%' : '0' }" />
        </article>
      </section>

      <section class="dashboard-grid">
        <NCard class="dashboard-card" :bordered="true" content-style="padding: 0">
          <template #header><div class="card-title"><h2>证书到期观察</h2><p>当前版本及部署目标最近成功使用的版本</p></div></template>
          <template #header-extra><RouterLink to="/certificates"><AppButton action="next" tone="accent" size="small">查看全部</AppButton></RouterLink></template>
          <div v-if="upcomingCertificates.length" class="certificate-watch">
            <div v-for="certificate in upcomingCertificates" :key="certificate.id" class="watch-row">
              <span class="watch-icon">▱</span>
              <div class="watch-name"><strong>{{ certificate.commonName }}</strong><small>{{ certificate.sans.length }} 个域名 · v{{ certificate.version }}</small></div>
              <StatusBadge :status="certificate.status" />
              <div class="watch-expiry">
                <strong>{{ formatDate(certificate.notAfter, false) }}</strong>
                <small>{{ daysUntil(certificate.notAfter) }} 天后</small>
              </div>
            </div>
          </div>
          <EmptyState v-else title="还没有证书" description="新增 DNS 凭据后即可发起第一张证书申请。" symbol="▱" />
        </NCard>

        <NCard class="dashboard-card" :bordered="true" content-style="padding: 0">
          <template #header><div class="card-title"><h2>最近申请</h2><p>ACME 任务执行轨迹</p></div></template>
          <template #header-extra><RouterLink to="/orders"><AppButton action="history" tone="accent" size="small">申请记录</AppButton></RouterLink></template>
          <div v-if="recentOrders.length" class="order-feed">
            <div v-for="order in recentOrders" :key="order.id" class="feed-row">
              <span class="feed-line" :class="`feed-${order.status.toLowerCase()}`" />
              <div><strong>{{ order.domains[0] }}</strong><small>#{{ order.id }} · {{ stageLabel(order.stage) }}</small></div>
              <StatusBadge :status="order.status" />
              <time>{{ formatDate(order.createdAt) }}</time>
            </div>
          </div>
          <EmptyState v-else title="暂无申请记录" description="新的 ACME 申请会在这里显示执行进度。" symbol="↗" />
        </NCard>
      </section>
    </template>
  </div>
</template>

<style scoped>
.node-strip { min-height: 76px; display: flex; align-items: center; gap: 22px; margin-bottom: 18px; padding: 14px 18px; border: 1px solid var(--line); border-radius: 12px; background: var(--panel); box-shadow: var(--shadow); }
.node-identity { display: flex; align-items: center; gap: 12px; min-width: 220px; }.node-glyph { width: 40px; height: 40px; display: grid; place-items: center; flex: 0 0 auto; color: #50695b; border: 1px solid #d9e2da; background: linear-gradient(145deg, #f1f5ef, #e7ede7); border-radius: 9px; box-shadow: inset 0 1px 0 rgba(255, 255, 255, .75); }.node-copy > span, .node-metric > span { display: block; color: #7e8178; font-size: 11px; line-height: 1.4; }.node-copy > strong { display: block; margin-top: 4px; color: #393b34; font-size: 14px; line-height: 1.45; }.node-metric { min-width: 105px; padding-left: 22px; border-left: 1px solid var(--line-soft); }.node-metric strong { display: block; margin-top: 5px; color: #45473f; font-size: 15px; font-weight: 700; }.node-metric small { color: #949188; font-size: 10px; }.live-indicator { margin-left: auto; padding: 6px 10px; color: #556d5f; background: var(--sage); border-radius: 999px; font-size: 11px; font-weight: 650; }.live-indicator i { display: inline-block; width: 6px; height: 6px; margin-right: 7px; border-radius: 50%; background: #6b8576; }
.metric-grid { display: grid; grid-template-columns: repeat(4, 1fr); gap: 14px; margin-bottom: 18px; }.metric-card { position: relative; min-height: 150px; overflow: hidden; padding: 20px; border: 1px solid var(--line); border-radius: 12px; background: var(--panel); box-shadow: var(--shadow); }.metric-top { display: flex; justify-content: space-between; align-items: center; }.metric-top span { color: #696a62; font-size: 11px; font-weight: 700; }.metric-top b { width: 34px; height: 34px; display: grid; place-items: center; color: #67736b; background: #f1f0eb; border-radius: 9px; font-size: 17px; }.metric-card > strong { display: block; margin-top: 14px; color: #30322c; font-size: 30px; font-weight: 700; line-height: 1; }.metric-card > small { display: block; margin-top: 10px; color: #8b897f; font-size: 10px; }.metric-card > i { display: none; }.metric-valid .metric-top b { color: #4e695a; background: var(--sage); }.metric-warning .metric-top b { color: #9b663e; background: #f6eee5; }.metric-orders .metric-top b { color: #617087; background: #eef0f3; }
.dashboard-grid { display: grid; grid-template-columns: minmax(0, 1.25fr) minmax(360px, .75fr); gap: 18px; }.certificate-watch { padding: 3px 20px 9px; }.watch-row { display: grid; grid-template-columns: 36px minmax(130px, 1fr) auto 105px; align-items: center; gap: 12px; min-height: 74px; border-bottom: 1px solid var(--line-soft); }.watch-row:last-child { border-bottom: 0; }.watch-icon { width: 32px; height: 32px; display: grid; place-items: center; color: #607668; background: var(--sage); border-radius: 8px; }.watch-name strong, .watch-name small, .watch-expiry strong, .watch-expiry small { display: block; }.watch-name strong { overflow: hidden; color: #41433c; font-size: 12px; text-overflow: ellipsis; }.watch-name small, .watch-expiry small { margin-top: 4px; color: #8e8c83; font-size: 9px; }.watch-expiry { text-align: right; }.watch-expiry strong { color: #5b5d55; font-size: 10px; font-weight: 650; }
.order-feed { padding: 3px 20px 9px; }.feed-row { display: grid; grid-template-columns: 3px minmax(120px,1fr) auto 82px; align-items: center; gap: 12px; min-height: 74px; border-bottom: 1px solid var(--line-soft); }.feed-row:last-child { border-bottom: 0; }.feed-line { width: 3px; height: 30px; border-radius: 3px; background: #a9a69d; }.feed-success { background: #718a7b; }.feed-failed { background: #b96762; }.feed-running, .feed-queued { background: #bb8356; }.feed-row strong, .feed-row small { display: block; }.feed-row strong { overflow: hidden; color: #41433c; font-size: 11px; text-overflow: ellipsis; }.feed-row small { margin-top: 4px; color: #908d84; font-size: 9px; }.feed-row time { color: #8b897f; font-size: 9px; text-align: right; white-space: normal; }
.dashboard-card { box-shadow: var(--shadow); }
.card-title h2 { margin: 0; color: #34362f; font-size: 15px; }
.card-title p { margin: 4px 0 0; color: var(--muted); font-size: 11px; }
@media (max-width: 1200px) { .metric-grid { grid-template-columns: repeat(2, 1fr); }.dashboard-grid { grid-template-columns: 1fr; }.node-metric { display: none; } }
@media (max-width: 620px) { .node-strip { gap: 10px; }.node-identity { min-width: 0; flex: 1; }.live-indicator { display: none; }.metric-grid { grid-template-columns: 1fr 1fr; gap: 9px; }.metric-card { min-height: 133px; padding: 14px; }.metric-card > strong { font-size: 23px; }.watch-row { grid-template-columns: 32px 1fr auto; }.watch-expiry { display: none; }.feed-row { grid-template-columns: 3px 1fr auto; }.feed-row time { display: none; } }
</style>
