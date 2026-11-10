<script setup lang="ts">
/**
 * 系统设置页面，展示脱敏运行信息并提供管理员密码修改。
 *
 * @author Andersen
 */
import DetailFields from '@/components/DetailFields.vue'
import type { DetailField } from '@/types/details'
import AppButton from '@/components/AppButton.vue'
import { computed, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { NAlert, NCard, NForm, NFormItem, NIcon, NInput, NSpin, NTag } from 'naive-ui'
import { Clock, Database, Key, Lock, Server, ShieldCheck } from '@vicons/tabler'
import { api } from '@/api'
import type { SystemInfo } from '@/types'
import PageHeader from '@/components/PageHeader.vue'
import { useToast } from '@/composables/useToast'
import { useAuthStore } from '@/stores/auth'

/** info 是脱敏后的系统运行信息。 */
const info = ref<SystemInfo | null>(null)
/** loading 表示系统信息是否正在加载。 */
const loading = ref(true)
/** changing 表示密码修改是否正在提交。 */
const changing = ref(false)
/** toast 提供统一页面通知。 */
const toast = useToast()
/** auth 是管理员身份状态仓库。 */
const auth = useAuthStore()
/** router 用于密码修改后返回登录页。 */
const router = useRouter()
/** password 是当前密码、新密码和确认密码表单。 */
const password = reactive({ current: '', next: '', confirm: '' })

/** load 查询系统运行信息。 */
async function load() {
  try {
    info.value = await api.systemInfo()
  } catch (error) {
    toast.error('系统信息加载失败', error instanceof Error ? error.message : undefined)
  } finally {
    loading.value = false
  }
}

/** changePassword 校验两次输入并修改管理员密码。 */
async function changePassword() {
  if (!password.current || password.next.length < 12) {
    toast.error('请输入当前密码，并设置至少 12 位的新密码')
    return
  }
  if (password.next !== password.confirm) {
    toast.error('两次输入的新密码不一致')
    return
  }
  changing.value = true
  try {
    await api.changePassword(password.current, password.next)
    toast.success('管理员密码已更新', '所有既有会话均已失效，请重新登录。')
    auth.clear()
    await router.replace('/login')
  } catch (error) {
    toast.error('密码修改失败', error instanceof Error ? error.message : undefined)
  } finally {
    changing.value = false
  }
}

onMounted(load)

/** runtimeFields 用统一只读字段展示运行身份、访问地址与维护策略。 */
const runtimeFields = computed<DetailField[]>(() => {
  /** item 是后端已脱敏的系统摘要。 */
  const item = info.value
  if (!item) return []
  return [
    { group: '节点身份', label: '运行角色', value: 'server · 控制平面' },
    { group: '节点身份', label: '本机执行器', value: item.localDeploymentEnabled ? '已启用 · 可部署本机证书' : '已关闭 · 仅远程 Agent' },
    { group: '节点身份', label: '运行时', value: item.runtime, mono: true },
    { group: '节点身份', label: '应用版本', value: `Acme4go ${item.version}` },
    { group: '访问与路径', label: '管理端地址', value: item.adminUrl, wide: true },
    { group: '访问与路径', label: 'Agent mTLS 地址', value: item.agentUrl, wide: true },
    { group: '访问与路径', label: '证书分发根目录', value: item.deploymentDir, mono: true, wide: true },
    { group: '维护策略', label: '续期窗口', value: `到期前 ${item.renewBeforeDays} 天` },
    { group: '维护策略', label: 'Agent 对账间隔', value: `${item.pollIntervalSeconds} 秒` },
    { group: '维护策略', label: '历史保留期', value: `${item.historyRetentionDays} 天` },
    { group: '维护策略', label: '失败告警', value: item.alertWebhookEnabled ? 'Webhook 已启用' : '未配置' },
  ]
})
</script>

<template>
  <div>
    <PageHeader eyebrow="系统管理" title="系统设置" description="查看当前节点运行参数，确认安全边界，并维护管理端登录密码。" />

    <div class="settings-layout">
      <NCard class="settings-card runtime-card" :bordered="true" content-style="padding: 0">
        <template #header><div class="card-title"><h2>节点运行信息</h2><p>当前实例的只读配置摘要</p></div></template>
        <template #header-extra><NTag type="success" round :bordered="false">Go 静态二进制</NTag></template>
        <div v-if="loading" class="loading-block"><NSpin size="small" />正在读取运行参数…</div>
        <DetailFields v-else-if="info" class="runtime-details" :fields="runtimeFields" />
      </NCard>

      <NCard class="settings-card password-card" :bordered="true">
        <template #header><div class="card-title"><h2>修改登录密码</h2><p>保存后所有设备需要重新登录</p></div></template>
        <template #header-extra><span class="card-icon"><NIcon :size="18" :component="Lock" /></span></template>
        <NAlert class="password-alert" type="info" :show-icon="true">建议使用密码管理器生成并保存至少 16 位的随机密码。</NAlert>
        <NForm id="password-form" :model="password" label-placement="top" @submit.prevent="changePassword">
          <NFormItem label="当前密码" :show-feedback="false"><NInput v-model:value="password.current" type="password" autocomplete="current-password" show-password-on="click"><template #prefix><NIcon :size="16" :component="Lock" /></template></NInput></NFormItem>
          <NFormItem label="新密码" :show-feedback="false"><NInput v-model:value="password.next" type="password" autocomplete="new-password" show-password-on="click" maxlength="128"><template #prefix><NIcon :size="16" :component="Key" /></template></NInput></NFormItem>
          <NFormItem label="确认新密码" :show-feedback="false"><NInput v-model:value="password.confirm" type="password" autocomplete="new-password" show-password-on="click" maxlength="128"><template #prefix><NIcon :size="16" :component="Key" /></template></NInput></NFormItem>
          <AppButton action="security" tone="primary" attr-type="submit" :loading="changing" block>更新密码并退出登录</AppButton>
        </NForm>
      </NCard>

      <NCard class="settings-card security-panel" :bordered="true" content-style="padding: 0">
        <template #header><div class="card-title"><h2>安全边界</h2><p>当前部署启用的核心保护机制</p></div></template>
        <div class="security-items">
          <div><span class="security-feature-icon"><NIcon :size="18" :component="Database" /></span><span><strong>私钥与 AK Secret 加密落盘</strong><small>AES-256-GCM · 每次写入独立随机向量</small></span></div>
          <div><span class="security-feature-icon"><NIcon :size="18" :component="ShieldCheck" /></span><span><strong>管理员密码不可逆摘要</strong><small>Argon2id · 独立随机盐 · 内存硬化</small></span></div>
          <div><span class="security-feature-icon"><NIcon :size="18" :component="Key" /></span><span><strong>Cookie 会话与 CSRF 防护</strong><small>HttpOnly / SameSite · 令牌仅存摘要 · 来源校验</small></span></div>
          <div><span class="security-feature-icon"><NIcon :size="18" :component="Clock" /></span><span><strong>Agent 双向 TLS 身份</strong><small>内部独立 CA · 一次性注册 · 自动续签 · 可即时撤销</small></span></div>
        </div>
      </NCard>

      <NCard class="settings-card env-panel" :bordered="true" content-style="padding: 0">
        <template #header><div class="card-title"><h2>生产部署参数</h2><p>由 Docker 或密钥管理系统注入</p></div></template>
        <template #header-extra><span class="card-icon"><NIcon :size="18" :component="Server" /></span></template>
        <div class="env-list">
          <code>ACME_MASTER_KEY</code><span>凭据与证书私钥本地加密主密钥</span>
          <code>ACME_ADMIN_PASSWORD_FILE</code><span>首次启动管理员密码的 Docker Secret 路径</span>
          <code>ACME_PUBLIC_URL</code><span>管理员访问当前控制台的公开地址</span>
          <code>ACME_AGENT_PUBLIC_URL</code><span>Agent 访问的 HTTPS / mTLS 地址</span>
          <code>ACME_LOCAL_DEPLOYMENT_ENABLED</code><span>Server 是否启用本机证书执行器</span>
          <code>ACME_DEPLOYMENT_DIR</code><span>业务应用只读挂载的标准 PEM 根目录</span>
          <code>ACME_RENEW_BEFORE</code><span>证书自动续签窗口，例如 720h</span>
        </div>
      </NCard>
    </div>
  </div>
</template>

<style scoped>
.settings-layout { display: grid; grid-template-columns: minmax(0, 1.15fr) minmax(360px, .85fr); gap: 18px; align-items: start; }.settings-card { box-shadow: var(--shadow); }.card-title h2 { margin: 0; font-size: 15px; }.card-title p { margin: 4px 0 0; color: var(--muted); font-size: 11px; }
.card-icon { width: 34px; height: 34px; display: grid; place-items: center; color: #526c5e; border: 1px solid #dbe4dc; background: linear-gradient(145deg, #f1f5ef, #e5ece5); border-radius: 9px; box-shadow: inset 0 1px 0 rgba(255, 255, 255, .72); }
.runtime-details { padding: 20px; }
.password-alert { margin-bottom: 20px; }.password-card :deep(.n-form-item) { margin-bottom: 18px; }
.security-items { display: grid; grid-template-columns: 1fr 1fr; }.security-items > div { min-height: 92px; display: flex; align-items: center; gap: 13px; padding: 17px 20px; border-right: 1px solid var(--line-soft); border-bottom: 1px solid var(--line-soft); }.security-items > div:nth-child(even) { border-right: 0; }.security-items > div:nth-last-child(-n+2) { border-bottom: 0; }.security-feature-icon { width: 34px; height: 34px; display: grid; place-items: center; flex: 0 0 auto; color: #526c5e; border: 1px solid #dbe4dc; background: linear-gradient(145deg, #f1f5ef, #e5ece5); border-radius: 9px; box-shadow: inset 0 1px 0 rgba(255, 255, 255, .72); }.security-items strong, .security-items small { display: block; }.security-items strong { color: #474941; font-size: 11px; }.security-items small { margin-top: 5px; color: #8e8c83; font-size: 9px; line-height: 1.5; }
.env-list { display: grid; grid-template-columns: auto 1fr; padding: 8px 20px; }.env-list code, .env-list span { min-height: 52px; display: flex; align-items: center; border-bottom: 1px solid var(--line-soft); }.env-list code { padding-right: 20px; color: #526b5d; font: 10px var(--mono); }.env-list span { color: #7e7e75; font-size: 10px; }.env-list code:nth-last-of-type(1), .env-list span:last-child { border-bottom: 0; }
@media (max-width: 980px) { .settings-layout { grid-template-columns: 1fr; } }
@media (max-width: 580px) { .security-items { grid-template-columns: 1fr; }.security-items > div { border-right: 0; border-bottom: 1px solid var(--line-soft) !important; }.security-items > div:last-child { border-bottom: 0 !important; }.env-list { grid-template-columns: 1fr; padding-block: 12px; }.env-list code { min-height: 30px; border-bottom: 0; }.env-list span { min-height: 38px; } }
</style>
