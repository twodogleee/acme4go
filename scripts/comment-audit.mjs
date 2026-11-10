/**
 * 检查 TypeScript 与 Vue script setup 中的类型、属性、常量、函数和回调注释。
 *
 * @author Andersen
 */
import fs from 'node:fs'
import path from 'node:path'
import { createRequire } from 'node:module'

/** root 是需要递归检查的前端源码目录。 */
const root = path.resolve(process.argv[2] ?? '.')
/** require 从前端工程自身依赖中加载 TypeScript 编译器。 */
const require = createRequire(path.join(root, 'package.json'))
/** ts 是用于解析 TypeScript AST 的编译器 API。 */
const ts = require('typescript')
let missing = 0

/** sourceFiles 递归返回需要审计的 TypeScript 和 Vue 文件。 */
function sourceFiles(directory) {
  return fs.readdirSync(directory, { withFileTypes: true }).flatMap(
    /** 目录项展开回调跳过依赖和构建产物目录。 */
    (entry) => {
      const value = path.join(directory, entry.name)
      if (entry.isDirectory()) return ['node_modules', 'dist'].includes(entry.name) ? [] : sourceFiles(value)
      return /\.(ts|vue)$/.test(entry.name) ? [value] : []
    },
  )
}

/** scriptText 读取 TypeScript 文件或提取 Vue 的 script setup 内容。 */
function scriptText(filename) {
  const text = fs.readFileSync(filename, 'utf8')
  if (!filename.endsWith('.vue')) return text
  const match = text.match(/<script\s+setup(?:\s+lang="ts")?>([\s\S]*?)<\/script>/)
  return match?.[1] ?? ''
}

/** hasLeadingComment 判断节点是否具有直接前置注释。 */
function hasLeadingComment(node, source) {
  const ranges = ts.getLeadingCommentRanges(source.text, node.getFullStart()) ?? []
  return ranges.some(
    /** 注释范围判断回调只接受声明起点之前的注释。 */
    (range) => range.end <= node.getStart(source),
  )
}

/** documentedContext 判断匿名函数所在的变量、属性或调用是否已被注释。 */
function documentedContext(node, source) {
  let current = node
  for (let depth = 0; current && depth < 7; depth++, current = current.parent) {
    if (hasLeadingComment(current, source)) return true
    if (ts.isSourceFile(current) || ts.isBlock(current)) break
  }
  return false
}

/** report 输出可定位的缺失项并累加失败数。 */
function report(source, filename, node, kind, name) {
  missing++
  const line = source.getLineAndCharacterOfPosition(node.getStart(source)).line + 1
  console.log(`${filename}:${line}: ${kind} ${name} 缺少注释`)
}

/** visitNode 递归检查一个 TypeScript AST 节点。 */
function visitNode(source, filename, node) {
  if (ts.isInterfaceDeclaration(node) || ts.isTypeAliasDeclaration(node) || ts.isClassDeclaration(node)) {
    if (!hasLeadingComment(node, source)) report(source, filename, node, '类型', node.name?.text ?? '<anonymous>')
  } else if (ts.isFunctionDeclaration(node)) {
    if (!hasLeadingComment(node, source)) report(source, filename, node, '函数', node.name?.text ?? '<anonymous>')
  } else if (ts.isMethodDeclaration(node) || ts.isMethodSignature(node) || ts.isPropertyDeclaration(node) || ts.isPropertySignature(node)) {
    const trailing = ts.getTrailingCommentRanges(source.text, node.end) ?? []
    if (!hasLeadingComment(node, source) && trailing.length === 0) {
      report(source, filename, node, '属性', node.name?.getText(source) ?? '<anonymous>')
    }
  } else if (ts.isVariableStatement(node) && (node.declarationList.flags & ts.NodeFlags.Const)) {
    if (!hasLeadingComment(node, source)) {
      const names = node.declarationList.declarations.map(
        /** 常量名称回调提取单条声明中的全部绑定名称。 */
        (item) => item.name.getText(source),
      ).join(',')
      report(source, filename, node, '常量', names)
    }
  } else if ((ts.isArrowFunction(node) || ts.isFunctionExpression(node)) && !documentedContext(node, source)) {
    report(source, filename, node, '回调', '<anonymous>')
  }
  ts.forEachChild(
    node,
    /** 子节点访问回调沿用当前文件上下文继续递归。 */
    (child) => visitNode(source, filename, child),
  )
}

for (const filename of sourceFiles(root)) {
  const completeText = fs.readFileSync(filename, 'utf8')
  if (!completeText.includes('@author Andersen')) {
    missing++
    console.log(`${filename}:1: 文件缺少 @author Andersen 标识`)
  }
  const text = scriptText(filename)
  if (!text.trim()) continue
  const source = ts.createSourceFile(filename, text, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
  visitNode(source, filename, source)
}

if (missing > 0) {
  console.error(`TypeScript/Vue 注释审计失败：共 ${missing} 处缺失`)
  process.exitCode = 1
} else {
  console.log('TypeScript/Vue 注释审计通过')
}
