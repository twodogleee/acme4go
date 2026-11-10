/**
 * 前端 ESLint 扁平配置，统一检查 Vue、TypeScript 与构建脚本。
 *
 * @author Andersen
 */
import eslint from '@eslint/js'
import globals from 'globals'
import tseslint from 'typescript-eslint'
import vue from 'eslint-plugin-vue'

/** eslintConfig 合并基础、TypeScript 和 Vue 推荐规则。 */
const eslintConfig = [
  { ignores: ['dist/**', 'node_modules/**', 'coverage/**'] },
  eslint.configs.recommended,
  ...tseslint.configs.recommended,
  ...vue.configs['flat/essential'],
  {
    files: ['**/*.{ts,vue}'],
    languageOptions: {
      globals: globals.browser,
      parserOptions: { parser: tseslint.parser, ecmaVersion: 'latest', sourceType: 'module' },
    },
    rules: {
      '@typescript-eslint/no-explicit-any': 'off',
      'vue/multi-word-component-names': 'off',
    },
  },
  {
    files: ['*.config.{js,ts}', '**/*.mjs'],
    languageOptions: { globals: globals.node },
  },
]

export default eslintConfig
