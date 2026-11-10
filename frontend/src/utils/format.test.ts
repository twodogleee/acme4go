/**
 * 日期、剩余天数、短标识和订单阶段格式化方法的前端单元测试。
 *
 * @author Andersen
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { daysUntil, formatDate, shortId, stageLabel } from './format'

// 测试套件回调集中验证共享格式化工具。
describe('format utilities', () => {
  // 前置回调固定系统时间，避免日期计算受运行时间影响。
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-09-04T00:00:00Z'))
  })

  // 后置回调恢复真实计时器，避免污染其他测试。
  afterEach(() => {
    vi.useRealTimers()
  })

  // 空值与非法日期测试回调验证安全占位符。
  it('handles empty and invalid dates safely', () => {
    expect(formatDate(null)).toBe('—')
    expect(formatDate('invalid')).toBe('—')
  })

  // 剩余天数测试回调验证向上取整规则。
  it('calculates rounded-up remaining days', () => {
    expect(daysUntil('2026-09-04T00:00:01Z')).toBe(1)
    expect(daysUntil('2026-09-06T00:00:00Z')).toBe(2)
  })

  // 标识与阶段测试回调验证截断和中文映射。
  it('formats identifiers and known stages', () => {
    expect(shortId('1234567890', 5)).toBe('12345…')
    expect(shortId('123', 5)).toBe('123')
    expect(stageLabel('AUTHORIZING_DNS')).toBe('DNS 所有权验证')
    expect(stageLabel('CUSTOM')).toBe('CUSTOM')
  })
})
