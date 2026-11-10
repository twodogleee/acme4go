/**
 * 验证前端 ESLint 配置的文件匹配、忽略规则和语言规则。
 *
 * @author Andersen
 */
import path from 'node:path'
import { ESLint } from 'eslint'
import { describe, expect, it } from 'vitest'

/** eslint 使用仓库中的实际扁平配置检查文件。 */
const eslint = new ESLint()

// 回归测试覆盖 ESLint 和其配置匹配器使用 minimatch 的实际行为。
describe('ESLint 配置与文件匹配', () => {
  // 文件枚举测试验证嵌套目录和扩展名通配符可找到真实源码。
  it('通过 glob 查找嵌套的 TypeScript 和 Vue 文件', async () => {
    const results = await eslint.lintFiles(['src/utils/**/*.ts', 'src/components/**/*.vue'])
    const files = results.map((result) => result.filePath)

    expect(files.some((file) => file.endsWith(path.join('src', 'utils', 'format.ts')))).toBe(true)
    expect(files.some((file) => file.endsWith(path.join('src', 'components', 'AppButton.vue')))).toBe(true)
  })

  // 忽略规则测试防止生成目录被误当成源码检查。
  it('忽略构建、覆盖率和依赖目录，同时检查源码', async () => {
    for (const file of ['dist/main.js', 'coverage/index.js', 'node_modules/example/index.js']) {
      expect(await eslint.isPathIgnored(file)).toBe(true)
    }
    expect(await eslint.isPathIgnored('src/utils/format.ts')).toBe(false)
  })

  // TypeScript 规则测试确认扩展名匹配后仍使用对应解析器和规则集。
  it('对嵌套 TypeScript 文件执行 TypeScript 规则', async () => {
    const [result] = await eslint.lintText('const value: string = "example"', {
      filePath: 'src/example/compatibility.ts',
    })
    expect(result.messages.map((message) => message.ruleId)).toContain('@typescript-eslint/no-unused-vars')
  })

  // Vue 规则测试确认单文件组件按模板语义检查，而不是被当成普通 JavaScript。
  it('对 Vue 单文件组件执行模板规则', async () => {
    const [result] = await eslint.lintText('<template><div v-if="ready" v-else /></template>', {
      filePath: 'src/example/Compatibility.vue',
    })
    expect(result.messages.map((message) => message.ruleId)).toContain('vue/valid-v-if')
  })

  // Node 环境测试确认配置脚本通配符只为目标文件启用 Node 全局变量。
  it('仅在匹配的配置脚本中允许 Node 全局变量', async () => {
    const [config] = await eslint.lintText('process.cwd(); unknownGlobal()', { filePath: 'example.config.js' })
    const [ordinary] = await eslint.lintText('process.cwd(); unknownGlobal()', { filePath: 'example.js' })
    expect(config.messages.filter((message) => message.ruleId === 'no-undef')).toHaveLength(1)
    expect(ordinary.messages.filter((message) => message.ruleId === 'no-undef')).toHaveLength(2)
  })
})
