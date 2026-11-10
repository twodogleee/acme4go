/** 多根域名输入和旧接口兼容测试。 @author Andersen */
import { describe, expect, it } from 'vitest'
import { credentialZones, parseCredentialZones } from './credentialZones'

// 测试多根域名输入与旧接口的兼容语义。
describe('credentialZones', () => {
  // 显式数组优先，避免空范围被旧字段意外扩展。
  it('兼容单域名、保留完整数组，并尊重显式空范围', () => {
    expect(credentialZones({ zone: 'example.com' })).toEqual(['example.com'])
    expect(credentialZones({ zone: 'example.com', zones: ['example.com', 'example.net'] })).toEqual(['example.com', 'example.net'])
    expect(credentialZones({ zone: 'example.com', zones: [] })).toEqual([])
  })
  // 粘贴输入保留域名顺序，并消除大小写与末尾点产生的重复项。
  it('支持多行和中英文分隔，规范化大小写和去重', () => {
    expect(parseCredentialZones(' Example.COM.\nexample.net，example.org; example.com ')).toEqual(['example.com', 'example.net', 'example.org'])
    expect(parseCredentialZones(' ,\n； ')).toEqual([])
  })
})
