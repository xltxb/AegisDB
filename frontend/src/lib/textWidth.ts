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

// 光标该怎么在字符串里挪一格 —— 按**字符**(码点),不是按 UTF-16 单元。
//
// 这两个函数和 dispWidth 住在一起不是凑巧:基本平面之外的字符(emoji、生僻字)在 JS
// 字符串里占两个单元,而 dispWidth 一直是按码点迭代的。光标要是按单元挪,就会停在
// 一对代理的正中间 —— 此后插入的字符插进了半个字符里,退格删掉的是半个字符,缓冲里
// 留下一个既显示不出来、又会随语句提交到目标库的孤立代理;而 dispWidth 看到那半个
// 代理只算一格(完整的 emoji 算两格),光标位置从此一路漂移。
//
// 两边必须按同一个单位读同一个字符串,所以它们放在同一个文件里。
//
// 这一层认的是码点,不是字形簇:👨‍👩‍👧 这类用零宽连接符拼起来的序列、以及带变体选择符
// 的 emoji,仍然要按几下方向键才能走完。再进一步需要 Intl.Segmenter,而那要 dispWidth
// 一起改口径 —— 留到需要时一并做,不在这里只改一半。

const isHighSurrogate = (c: number) => c >= 0xd800 && c <= 0xdbff
const isLowSurrogate = (c: number) => c >= 0xdc00 && c <= 0xdfff

/** 从下标 i 往左挪一个字符后的下标。i<=0 时留在 0。 */
export function prevCharIndex(s: string, i: number): number {
  if (i <= 0) return 0
  if (i >= 2 && isLowSurrogate(s.charCodeAt(i - 1)) && isHighSurrogate(s.charCodeAt(i - 2))) {
    return i - 2
  }
  return i - 1
}

/** 从下标 i 往右挪一个字符后的下标。i>=长度时留在末尾。 */
export function nextCharIndex(s: string, i: number): number {
  if (i >= s.length) return s.length
  if (isHighSurrogate(s.charCodeAt(i)) && isLowSurrogate(s.charCodeAt(i + 1))) {
    return i + 2
  }
  return i + 1
}
