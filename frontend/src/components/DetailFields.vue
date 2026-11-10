<script setup lang="ts">
/**
 * 分组只读详情：统一标签底色、字段边界、状态与长文本，供弹窗和行内详情复用。
 * 所有内容按文本渲染，只接收业务页明确选出的安全字段。
 * @author Andersen
 */
import { computed } from 'vue'
import StatusBadge from '@/components/StatusBadge.vue'
import type { DetailField } from '@/types/details'

/** props 定义可展示字段及未指定分组时的标题。 */
const props = withDefaults(defineProps<{
  fields: DetailField[] // 页面选出的已脱敏字段。
  title?: string // 无显式分组字段的默认标题。
}>(), { title: '基本信息' })

/** groups 按页面声明顺序组织字段，刷新时从当前数据重新派生。 */
const groups = computed(() => {
  /** result 保留分组插入顺序，不改变同组字段的阅读顺序。 */
  const result = new Map<string, DetailField[]>()
  for (const field of props.fields) {
    /** name 使用业务分组或默认标题。 */
    const name = field.group || props.title
    if (!result.has(name)) result.set(name, [])
    result.get(name)!.push({ ...field })
  }
  // 双列中落单的字段占满一行，避免奇数项或长字段前出现无含义的空白格。
  for (const fields of result.values()) {
    /** unpaired 是当前尚未配对的展示副本，不修改调用方字段。 */
    let unpaired: DetailField | undefined
    for (const field of fields) {
      if (field.wide) {
        if (unpaired) unpaired.wide = true
        unpaired = undefined
      } else {
        unpaired = unpaired ? undefined : field
      }
    }
    if (unpaired) unpaired.wide = true
  }
  return Array.from(result,
    /** 转换为模板可按标题追踪的分组，保持字段顺序。 */
    ([title, fields]) => ({ title, fields }))
})

/** isEmpty 区分无内容与有效的数字零。 */
function isEmpty(value: DetailField['value']) {
  return value === '' || value === null || value === undefined || (Array.isArray(value) && value.length === 0)
}
</script>

<template>
  <div class="detail-fields">
    <section v-for="group in groups" :key="group.title" class="detail-section" :aria-label="group.title">
      <h3 class="detail-heading">{{ group.title }}</h3>
      <dl class="detail-field-grid">
        <div v-for="field in group.fields" :key="field.label" class="detail-field" :class="{ 'detail-field-wide': field.wide, 'detail-field-error': field.error }">
          <dt>{{ field.label }}</dt>
          <dd :class="{ 'detail-value-mono': field.mono, 'detail-value-empty': isEmpty(field.value) }">
            <span v-if="isEmpty(field.value)">—</span>
            <StatusBadge v-else-if="field.status" :status="field.status" :label="String(field.value) === field.status ? undefined : String(field.value)" />
            <ul v-else-if="Array.isArray(field.value)" class="detail-value-list"><li v-for="(value, index) in field.value" :key="index">{{ value }}</li></ul>
            <template v-else>{{ field.value }}</template>
          </dd>
        </div>
      </dl>
    </section>
  </div>
</template>

<style scoped>
/* 按组件实际宽度排布，窄侧栏中的详情不会误用桌面双列布局。 */
.detail-fields { container-type: inline-size; min-width: 0; white-space: normal; }
.detail-section + .detail-section { margin-top: 22px; }
.detail-field-grid { display: grid; grid-template-columns: minmax(0, 1fr); gap: 1px; margin: 0; padding: 1px; border-radius: 8px; overflow: hidden; background: var(--line); }
.detail-field { display: grid; grid-template-columns: 132px minmax(0, 1fr); min-width: 0; background: var(--panel); }
.detail-field-wide { grid-column: 1 / -1; }
.detail-field dt { display: flex; align-items: flex-start; margin: 0; padding: 12px; background: var(--canvas); color: #65675e; font-size: 12px; font-weight: 500; line-height: 1.75; overflow-wrap: anywhere; }
.detail-field dd { min-width: 0; margin: 0; padding: 12px 14px; color: var(--ink); font-size: 13px; font-weight: 500; line-height: 1.65; white-space: pre-wrap; overflow-wrap: anywhere; }
.detail-field .detail-value-mono { font-family: var(--mono); font-size: 12px; }
.detail-field .detail-value-empty { color: var(--muted); }
.detail-field-error dd { color: var(--red); }
.detail-value-list { display: flex; flex-wrap: wrap; gap: 6px; margin: 0; padding: 0; list-style: none; }
.detail-value-list li { min-width: 0; max-width: 100%; padding: 2px 7px; border: 1px solid var(--line); border-radius: 5px; background: var(--nav); font-size: 12px; overflow-wrap: anywhere; }
@container (min-width: 640px) { .detail-field-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
@container (max-width: 380px) {
  .detail-field { grid-template-columns: 92px minmax(0, 1fr); }
  .detail-field dt { padding: 10px; }
  .detail-field dd { padding: 10px 12px; }
}
</style>
