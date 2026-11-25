// 终端列宽 —— "这段文字在终端里占几格"。
//
// 单独成文件,是因为有两处必须对同一个问题给出同一个答案:
//   · 结果表格的对齐(lib/sqlResult 画框、补空格)
//   · 输入行的重绘与光标定位(lib/lineEditor 算折行)
// 两边各写一份的话,它们会慢慢分叉,而分叉的表现是"表格对得上、光标对不上"这种
// 找起来很费劲的错位。

// isWideChar 判断一个码点是否占两格(中日韩、假名、全角形式、emoji)。
export function isWideChar(cp: number): boolean {
  return (
    (cp >= 0x1100 && cp <= 0x115f) ||
    (cp >= 0x2e80 && cp <= 0xa4cf) ||
    (cp >= 0xac00 && cp <= 0xd7a3) ||
    (cp >= 0xf900 && cp <= 0xfaff) ||
    (cp >= 0xfe30 && cp <= 0xfe4f) ||
    (cp >= 0xff00 && cp <= 0xff60) ||
    (cp >= 0xffe0 && cp <= 0xffe6) ||
    (cp >= 0x1f300 && cp <= 0x1faff) ||
    (cp >= 0x20000 && cp <= 0x3fffd)
  )
}

// dispWidth 是一段字符串的屏幕列数(中日韩按 2 算)。
//
// 注意它与 String.length 的区别正是这个模块存在的理由:一条 40 个字符的中文
// 语句可能占 58 列。用 length 当列数,折行就会算少,重绘时"少上移一行",于是旧
// 内容的第一行连同提示符一起留在屏幕上。
export function dispWidth(s: string): number {
  let w = 0
  for (const ch of s) w += isWideChar(ch.codePointAt(0) || 0) ? 2 : 1
  return w
}
