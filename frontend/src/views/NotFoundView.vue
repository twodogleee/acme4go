<script setup lang="ts">
/**
 * 管理端未知路由的 404 结果页面。
 *
 * @author Andersen
 */
import AppButton from '@/components/AppButton.vue'
import { NResult } from 'naive-ui'
</script>

<template>
  <main class="not-found">
    <NResult status="404" title="页面不存在" description="请求的管理端页面可能已移动，或地址输入有误。">
      <template #footer><RouterLink to="/dashboard"><AppButton action="back" tone="primary">返回运行概览</AppButton></RouterLink></template>
    </NResult>
  </main>
</template>

<style scoped>
.not-found { min-height: 100vh; display: grid; place-items: center; padding: 32px; background: var(--canvas); }
</style>
