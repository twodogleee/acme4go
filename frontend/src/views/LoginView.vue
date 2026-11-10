<script setup lang="ts">
/**
 * 管理员登录页面，提交凭据并按原始目标路由进入控制台。
 *
 * @author Andersen
 */
import AppButton from '@/components/AppButton.vue'
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NAlert, NForm, NFormItem, NIcon, NInput } from 'naive-ui'
import { Affiliate, Certificate, Database, Lock, ShieldLock, User } from '@vicons/tabler'
import { useAuthStore } from '@/stores/auth'
import { ApiError } from '@/api/http'
import BrandMark from '@/components/BrandMark.vue'

/** username 是管理员用户名输入。 */
const username = ref('')
/** password 是管理员密码输入。 */
const password = ref('')
/** loading 表示登录请求是否正在执行。 */
const loading = ref(false)
/** error 是可向用户展示的登录错误。 */
const error = ref('')
/** auth 是管理员身份状态仓库。 */
const auth = useAuthStore()
/** route 提供登录前目标页面参数。 */
const route = useRoute()
/** router 用于登录成功后替换当前页面。 */
const router = useRouter()

/** submit 提交登录凭据并跳转到安全的目标页面。 */
async function submit() {
  error.value = ''
  loading.value = true
  try {
    await auth.login(username.value.trim(), password.value)
    /** redirect 是登录成功后的目标路由。 */
    const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : '/dashboard'
    await router.replace(redirect)
  } catch (exception) {
    error.value = exception instanceof ApiError ? exception.message : '无法连接管理服务'
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <main class="login-page">
    <section class="login-aside">
      <div class="login-brand">
        <BrandMark size="large" />
        <div><strong>Acme4go</strong><small>证书管理</small></div>
      </div>

      <div class="aside-copy">
        <span class="eyebrow">证书生命周期管理</span>
        <h1>让证书管理，<br /><em>清楚而从容。</em></h1>
        <p>从域名验证、加密保管到多节点交付，在一个简洁的工作台中完成证书全生命周期管理。</p>
      </div>

      <div class="aside-grid">
        <div><NIcon :component="Certificate" /><span><strong>自动签发</strong><small>到期前自动续期</small></span></div>
        <div><NIcon :component="Database" /><span><strong>安全保管</strong><small>凭据与私钥本地加密</small></span></div>
        <div><NIcon :component="Affiliate" /><span><strong>节点交付</strong><small>多节点保持一致</small></span></div>
      </div>
    </section>

    <section class="login-form-area">
      <NForm class="login-card" label-placement="top" size="large" @submit.prevent="submit">
        <div class="login-card-top">
          <BrandMark class="mobile-login-mark" size="large" />
          <span class="login-kicker">管理端登录</span>
          <h2>欢迎回来</h2>
          <p>登录证书控制台，查看签发任务与 Agent 部署状态。</p>
        </div>

        <NAlert v-if="error" class="login-error" title="登录失败" type="error" :show-icon="true" closable @close="error = ''">
          {{ error }}
        </NAlert>

        <NFormItem label="管理员账号" :show-feedback="false">
          <NInput v-model:value="username" autocomplete="username" placeholder="请输入用户名" clearable>
            <template #prefix><NIcon :size="16" :component="User" /></template>
          </NInput>
        </NFormItem>

        <NFormItem label="登录密码" :show-feedback="false">
          <NInput
            v-model:value="password"
            type="password"
            autocomplete="current-password"
            placeholder="请输入密码"
            show-password-on="click"
          >
            <template #prefix><NIcon :size="16" :component="Lock" /></template>
          </NInput>
        </NFormItem>

        <AppButton action="next" tone="primary" class="login-submit" size="large" block attr-type="submit" :loading="loading" :disabled="!username || !password">
          {{ loading ? '正在验证身份' : '进入控制台' }}

        </AppButton>

        <div class="login-security"><NIcon :component="ShieldLock" />会话令牌只保存摘要，浏览器不会接触部署密钥与证书私钥。</div>
      </NForm>
      <footer>Acme4go · 证书管理控制台</footer>
    </section>
  </main>
</template>

<style scoped>
.login-page { min-height: 100vh; display: grid; grid-template-columns: minmax(470px, 1.05fr) minmax(430px, .95fr); background: #f7f5f1; }
.login-aside { position: relative; min-height: 100vh; overflow: hidden; padding: 40px 58px; color: #34372f; background: #ebe7dd; border-right: 1px solid #ddd7cb; }
.login-aside::after { content: ''; position: absolute; right: -70px; bottom: -120px; width: 330px; height: 330px; border: 72px solid rgba(111, 130, 116, .08); border-radius: 50%; }
.login-brand { position: relative; z-index: 2; display: flex; align-items: center; gap: 14px; }
.login-brand strong, .login-brand small { display: block; }
.login-brand strong { color: #33362f; font-size: 20px; }
.login-brand small { margin-top: 2px; color: #89877e; font-size: 10px; }
.aside-copy { position: relative; z-index: 2; max-width: 640px; margin-top: clamp(110px, 18vh, 190px); }
.aside-copy h1 { margin: 0; color: #30332c; font-size: clamp(40px, 4.1vw, 60px); line-height: 1.2; letter-spacing: -2.2px; }
.aside-copy h1 em { color: #50695a; font-style: normal; }
.aside-copy p { max-width: 520px; margin: 26px 0 0; color: #72736b; font-size: 14px; line-height: 1.9; }
.aside-grid { position: absolute; z-index: 2; right: 58px; bottom: 44px; left: 58px; display: grid; grid-template-columns: repeat(3, 1fr); border-top: 1px solid #d7d1c5; }
.aside-grid > div { display: flex; align-items: center; gap: 11px; padding: 20px 16px 0 0; }
.aside-grid :deep(.n-icon) { flex: 0 0 auto; color: #607668; font-size: 21px; }
.aside-grid strong, .aside-grid small { display: block; }
.aside-grid strong { color: #454840; font-size: 12px; }
.aside-grid small { margin-top: 4px; color: #85837a; font-size: 9px; }
.login-form-area { position: relative; display: grid; place-items: center; align-content: center; padding: 50px; background: #f7f5f1; }
.login-card { width: min(410px, 100%); padding: 36px; border: 1px solid var(--line); border-radius: 14px; background: var(--panel); box-shadow: 0 16px 46px rgba(58, 50, 38, .075); }
.login-card-top { margin-bottom: 27px; }
.login-kicker { display: block; margin-bottom: 12px; color: #607064; font-size: 11px; font-weight: 650; }
.login-card h2 { margin: 0; color: #30322c; font-size: 30px; letter-spacing: -.8px; }
.login-card-top p { margin: 9px 0 0; color: #85837a; font-size: 13px; line-height: 1.65; }
.login-error { margin-bottom: 20px; }
.login-card :deep(.n-form-item) { margin-bottom: 19px; }
.login-card :deep(.n-form-item-label) { color: #4b4d45; font-size: 12px; font-weight: 650; }
.login-card :deep(.n-input) { box-shadow: none; }
.login-submit { margin-top: 4px; }
.login-security { display: flex; align-items: flex-start; justify-content: center; gap: 7px; margin-top: 20px; color: #918e84; font-size: 10px; line-height: 1.6; text-align: center; }
.login-security :deep(.n-icon) { flex: 0 0 auto; margin-top: 2px; color: #63786b; font-size: 15px; }
.login-form-area footer { position: absolute; bottom: 22px; color: #a29e94; font-size: 9px; }
.mobile-login-mark { display: none; }
@media (max-width: 900px) {
  .login-page { grid-template-columns: 1fr; }
  .login-aside { display: none; }
  .login-form-area { min-height: 100vh; padding: 34px 22px; }
  .login-card { padding: 28px 24px; }
  .mobile-login-mark { display: grid; margin-bottom: 27px; }
}
</style>
