<script setup lang="ts">
/**
 * 管理端根组件，统一安装本地化、主题、对话框、通知和加载条上下文。
 *
 * @author Andersen
 */
import { dateZhCN, NConfigProvider, NDialogProvider, NLoadingBarProvider, NNotificationProvider, zhCN } from 'naive-ui'
import { acmeTheme } from '@/theme'
</script>

<template>
  <NConfigProvider :locale="zhCN" :date-locale="dateZhCN" :theme-overrides="acmeTheme">
    <NLoadingBarProvider>
      <NDialogProvider>
        <NNotificationProvider placement="top-right" :max="4">
          <RouterView />
        </NNotificationProvider>
      </NDialogProvider>
    </NLoadingBarProvider>
  </NConfigProvider>
</template>
