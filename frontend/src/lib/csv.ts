// csv — 把几行数据变成一个能在 Excel 里打开的文件。
//
// 两条规则,两条都是踩出来的:
//
// 1. **带 BOM**。这些表里装的是中文实例名,而 Excel 打开没有字节序标记的 CSV 时
//    按 ANSI 读,于是中文变成乱码;更糟的是它还会按 ANSI 存回去,下一次导入读到的
//    就是一串 GBK 字节被当成 UTF-8。带上标记,整趟往返都是 UTF-8。
// 2. **该引号的时候引号**。逗号、引号、换行都可能出现在实例名、标签和说明里 ——
//    一个没转义的逗号会把一行悄悄错开一列,而错开的那份表看起来完全正常。

import { UTF8_BOM } from '@/lib/transcript'

/** 一个字段。含逗号 / 引号 / 换行时加引号,内部的引号翻倍。 */
export function csvEscape(v: unknown): string {
  const s = String(v ?? '')
  return /[",\n\r]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s
}

/** 表头 + 数据行 → 一整段 CSV 文本(不含 BOM,BOM 在落盘时加)。 */
export function toCsv(head: readonly string[], rows: readonly unknown[][]): string {
  return [head.join(','), ...rows.map((r) => r.map(csvEscape).join(','))].join('\n')
}

/**
 * 把文本存成一个文件。
 *
 * `revokeObjectURL` 延后一拍:同步撤销时 Safari 偶尔会在下载真正开始之前就丢掉
 * 那个 URL,表现为"点了没反应"。
 */
export function downloadCsv(filename: string, text: string): void {
  const url = URL.createObjectURL(new Blob([UTF8_BOM + text], { type: 'text/csv;charset=utf-8' }))
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
