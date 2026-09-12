import { test, expect } from '@playwright/test'
import fs from 'node:fs'
import ts from 'typescript'

// 前端的 ExecResp 要认得后端契约里写着的每一个字段。
//
// 漏字段不会报错,只会**悄悄丢东西**:结果集(columns / data)和截断标记(truncated)
// 都在这个响应里,类型里没有,读它的地方就得另起一个类型来补 —— 而那正是这个仓库
// 里发生过的事:lib/execOutcome.ts 自己声明了一份 ExecPayload。同一个契约两份类型,
// 从此各改各的。
//
// 这条只管一个方向:**openapi 里有的,TS 里必须有**。反过来不管 —— dto.go 里有几个
// 字段(auditId / rule / ruleRef / outputRef)openapi 自己还没补上,那是后端契约文档
// 的欠账(见 #32),不该由这条用例来拦前端。
function openapiExecRespFields(): string[] {
  const yaml = fs.readFileSync('../backend/docs/openapi.yaml', 'utf8')
  const start = yaml.indexOf('\n    ExecResp:')
  expect(start, 'openapi.yaml 里找不到 ExecResp').toBeGreaterThan(-1)
  const rest = yaml.slice(start + 1)
  // 到下一个同级 schema 为止
  const end = rest.search(/\n    \w+:/)
  const block = end > 0 ? rest.slice(0, end) : rest
  const props = block.slice(block.indexOf('properties:'))
  return [...props.matchAll(/^\s{8}(\w+):/gm)].map((m) => m[1])
}

function tsInterfaceFields(file: string, name: string): string[] {
  const sf = ts.createSourceFile(file, fs.readFileSync(file, 'utf8'), ts.ScriptTarget.Latest, true)
  let out: string[] = []
  const visit = (n: ts.Node) => {
    if (ts.isInterfaceDeclaration(n) && n.name.text === name) {
      out = n.members.map((m) => (m.name && ts.isIdentifier(m.name) ? m.name.text : '')).filter(Boolean)
    }
    ts.forEachChild(n, visit)
  }
  visit(sf)
  return out
}

test('ExecResp 认得后端契约里的每一个字段', () => {
  const wire = openapiExecRespFields()
  expect(wire.length, '没从 openapi.yaml 解析出字段').toBeGreaterThan(5)
  const mine = tsInterfaceFields('src/types/index.ts', 'ExecResp')
  const missing = wire.filter((f) => !mine.includes(f))
  expect(missing, `types/index.ts 的 ExecResp 漏了这些字段:${missing.join(', ')}`).toEqual([])
})
