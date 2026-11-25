import { test, expect } from '@playwright/test'

import { csvCell } from '../../src/lib/csv'

// L-FE2(2026-10 安全审计):连接配置导出的 CSV 不防公式注入 —— 实例名、标签是人填的,
// 以 = + - @ 开头的单元格会被表格软件当公式执行。
test('csvCell 给公式头加单引号,需要时加引号', () => {
  expect(csvCell('=HYPERLINK("http://x","y")')).toBe(`"'=HYPERLINK(""http://x"",""y"")"`)
  expect(csvCell('+1')).toBe("'+1")
  expect(csvCell('@SUM(A1)')).toBe("'@SUM(A1)")
  expect(csvCell('  =1+1')).toBe("'  =1+1")
  expect(csvCell('order-cluster')).toBe('order-cluster')
  expect(csvCell('a,b')).toBe('"a,b"')
  expect(csvCell(3306)).toBe('3306')
  expect(csvCell(null)).toBe('')
})
