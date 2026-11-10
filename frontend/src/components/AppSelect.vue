<script setup lang="ts" generic="T extends string | number">
/** 应用单选框：统一弹出层、多行选项、搜索和简短回显，业务层负责提供可选项。 @author Andersen */
import { computed, h } from 'vue'
import type { CSSProperties } from 'vue'
import { NSelect } from 'naive-ui'
import type { SelectFilter, SelectOption, SelectRenderLabel, SelectRenderTag } from 'naive-ui'
import type { SelectChoice } from '@/types/select'

// 将 class、disabled、loading、filterable 等属性交给实际输入控件。
defineOptions({ inheritAttrs: false })
/** props 使用单值契约，当前业务入口不提供多选或清空操作。 */
const props = defineProps<{
  value: T | null // 保留调用方原始值类型，空值表示尚未选择。
  options: SelectChoice[] // 已按业务权限和用途过滤的选项。
  showDetails?: boolean // 是否在收起后仍展示所选项的补充信息。
}>()
/** emit 透传业务标识，不进行数字或字符串转换。 */
const emit = defineEmits<{
  'update:value': [value: T] // 选择后的原始业务标识。
}>()
/** selected 是信息区对应的当前选项。 */
const selected = computed(() => props.options.find((item) => item.value === props.value))
/** selectOptions 将展示契约映射为组件库的基础选项，保留禁用状态与原始标识。 */
const selectOptions = computed<SelectOption[]>(() => props.options.map((item) => ({ value: item.value, label: item.label, disabled: item.disabled })))
/** renderLabel 将业务元数据排成可换行的名称、说明和详情。 */
const renderLabel: SelectRenderLabel = (option) => {
  /** item 保证仅使用本组件公开的结构化字段。 */
  const item = props.options.find((entry) => entry.value === option.value)
  if (!item) return String(option.label || '')
  return h('div', { class: 'app-select-option' }, [
    h('div', { class: 'app-select-option-heading' }, [
      h('span', { class: 'app-select-option-name' }, item.label),
      item.badge ? h('span', { class: 'app-select-option-badge' }, item.badge) : null,
    ]),
    item.description ? h('div', { class: 'app-select-option-description' }, item.description) : null,
    ...(item.details || []).map((detail) => h('div', { class: 'app-select-option-detail' }, detail)),
  ])
}
/** renderTag 保持输入框单行，长名称通过标题和选项完整呈现。 */
const renderTag: SelectRenderTag = ({ option }) => h('span', { title: String(option.label || '') }, String(option.label || ''))
/** filter 同时匹配名称、标识、说明和详情，避免结构化后丢失原搜索能力。 */
const filter: SelectFilter = (pattern, option) => {
  /** item 是正在匹配的结构化选项。 */
  const item = props.options.find((entry) => entry.value === option.value)
  return !!item && [item.label, item.value, item.description, item.badge, ...(item.details || [])].join(' ').toLowerCase().includes(pattern.trim().toLowerCase())
}
/** updateValue 保持单选值的类型，调用方负责选项与字段契约的一致性。 */
function updateValue(value: string | number) { emit('update:value', value as T) }
</script>

<template>
  <div class="app-select-field" :class="$attrs.class" :style="$attrs.style as CSSProperties">
    <NSelect
      v-bind="{ ...$attrs, class: undefined, style: undefined }"
      :value="value"
      :options="selectOptions"
      :to="true"
      :virtual-scroll="false"
      :render-label="renderLabel"
      :render-tag="renderTag"
      :filter="filter"
      :menu-props="{ class: 'app-select-menu' }"
      @update:value="updateValue"
    />
    <div v-if="showDetails && selected" class="app-select-summary">
      <strong>{{ selected.label }}<span v-if="selected.badge"> · {{ selected.badge }}</span></strong>
      <p v-if="selected.description">{{ selected.description }}</p>
      <p v-for="(detail, index) in selected.details" :key="index">{{ detail }}</p>
    </div>
  </div>
</template>

<style scoped>
.app-select-field { width: 100%; min-width: 0; }
.app-select-summary { margin-top: 10px; padding: 12px 14px; border: 1px solid var(--line); border-radius: 8px; background: var(--canvas); font-size: 12px; white-space: normal; overflow-wrap: anywhere; }
.app-select-summary strong { font-weight: 600; }
.app-select-summary p { margin: 5px 0 0; color: var(--muted); }
</style>

<style>
/* 菜单独立挂载到页面，只覆盖专属类，避开弹窗裁切并允许可变行高。 */
.app-select-menu .n-base-select-option__content { min-width: 0; white-space: normal; }
.app-select-menu .app-select-option { display: grid; gap: 4px; padding: 5px 0; min-width: 0; white-space: normal; overflow-wrap: anywhere; line-height: 1.6; }
.app-select-option-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 10px; }
.app-select-option-name { min-width: 0; font-size: 13px; font-weight: 600; }
.app-select-option-badge { flex: 0 0 auto; padding: 0 6px; border: 1px solid var(--line); border-radius: 5px; font-size: 10px; }
.app-select-option-description, .app-select-option-detail { color: var(--muted); font-size: 12px; }
</style>
