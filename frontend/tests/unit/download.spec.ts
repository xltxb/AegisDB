import { test, expect } from '@playwright/test'

import { filenameFromDisposition } from '../../src/lib/download'

// 程序包下载的文件名由服务端决定(单个 <包名>.pck,多个 <schema>_packages_<时间>.zip),
// 前端只负责把它原样取出来。中文名只能走 filename*=UTF-8''。
test('优先取 RFC 5987 的 UTF-8 文件名', () => {
  expect(filenameFromDisposition("attachment; filename*=UTF-8''PKG_BILLING.pck", 'x')).toBe('PKG_BILLING.pck')
  expect(filenameFromDisposition("attachment; filename*=UTF-8''%E8%B4%A6%E5%8A%A1_packages.zip", 'x')).toBe('账务_packages.zip')
})

test('没有 filename* 时取普通 filename', () => {
  expect(filenameFromDisposition('attachment; filename=audit_export.csv', 'x')).toBe('audit_export.csv')
  expect(filenameFromDisposition('attachment; filename="a b.pck"', 'x')).toBe('a b.pck')
})

test('取不到或编码坏了用兜底名', () => {
  expect(filenameFromDisposition(null, 'fallback.pck')).toBe('fallback.pck')
  expect(filenameFromDisposition('attachment', 'fallback.pck')).toBe('fallback.pck')
  expect(filenameFromDisposition("attachment; filename*=UTF-8''%E0%A4%A", 'fallback.pck')).toBe('fallback.pck')
})
