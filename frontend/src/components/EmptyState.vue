<script setup lang="ts">
/**
 * 通用空状态组件，统一图标、标题、说明和操作插槽。
 *
 * @author Andersen
 */
import { NEmpty, NIcon } from 'naive-ui'
import { Inbox } from '@vicons/tabler'

/** 空状态展示属性。 */
defineProps<{
  title: string // title 是空状态标题。
  description: string // description 是补充说明。
  symbol?: string // symbol 是保留的自定义符号配置。
}>()
</script>

<template>
  <NEmpty class="empty-state" :description="title" size="large">
    <template #icon><span class="empty-symbol"><NIcon :component="Inbox" /></span></template>
    <template #extra><p>{{ description }}</p><slot /></template>
  </NEmpty>
</template>
