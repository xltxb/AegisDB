// 手填库名清单(执行窗口列不出库时):逗号、全角逗号、空白或换行分隔,去重保序。
//
// 2026-10-26:原先内联在 ExecWindowsView 里,正则把 \s 写成了 s,`orders` 被拆成 `order`,
// 编辑老窗口时回填进输入框的库名也被当场拆坏。
export function parseDbList(text: string): string[] {
  return [...new Set(text.split(/[\s,，]+/).map((x) => x.trim()).filter(Boolean))]
}
