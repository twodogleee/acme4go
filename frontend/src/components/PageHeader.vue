<script setup lang="ts">
/**
 * 页面标题组件，统一栏目眉题、主标题、说明和操作区布局。
 *
 * @author Andersen
 */
/** 页面标题展示属性。 */
defineProps<{
  eyebrow?: string // eyebrow 是主标题上方的栏目名称。
  title: string // title 是页面主标题。
  description: string // description 是页面用途说明。
}>()
</script>

<template>
  <header class="page-header">
    <div>
      <span v-if="eyebrow" class="eyebrow">{{ eyebrow }}</span>
      <h1>{{ title }}</h1>
      <p>{{ description }}</p>
    </div>
    <div v-if="$slots.actions" class="page-actions"><slot name="actions" /></div>
  </header>
</template>
