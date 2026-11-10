<!--
  Acme4go 品牌标识：使用证书文件轮廓表达产品定位，并统一登录页与管理端导航的视觉。
  @author Andersen
-->
<script setup lang="ts">
import { computed } from 'vue'
import { NIcon } from 'naive-ui'
import { FileCertificate } from '@vicons/tabler'

/** props 是品牌标识尺寸配置。 */
const props = withDefaults(defineProps<{
  size?: 'default' | 'large' // size 是品牌标识预设尺寸。
}>(), {
  size: 'default',
})

/** iconSize 是当前品牌标识对应的图标像素尺寸。 */
const iconSize = computed(() => (props.size === 'large' ? 23 : 21))
</script>

<template>
  <span class="brand-mark" :class="`brand-mark--${size}`" aria-hidden="true">
    <NIcon :size="iconSize" :component="FileCertificate" />
  </span>
</template>

<style scoped>
.brand-mark {
  position: relative;
  width: 38px;
  height: 38px;
  display: grid;
  place-items: center;
  flex: 0 0 auto;
  overflow: hidden;
  color: #fff;
  border: 1px solid rgba(41, 68, 53, .22);
  border-radius: 10px;
  background: linear-gradient(145deg, #597566 0%, #415f50 100%);
  box-shadow: inset 0 1px 0 rgba(255, 255, 255, .18), 0 4px 10px rgba(57, 80, 67, .16);
}

.brand-mark::after {
  content: '';
  position: absolute;
  right: 7px;
  bottom: 6px;
  width: 4px;
  height: 4px;
  border: 1.5px solid #496556;
  border-radius: 50%;
  background: #e0c47f;
  box-shadow: 0 0 0 1px rgba(255, 255, 255, .72);
}

.brand-mark--large {
  width: 40px;
  height: 40px;
}

.brand-mark :deep(.n-icon) {
  transform: translate(-1px, -1px);
}
</style>
