// 浏览器文件下载的两个小零件。

/**
 * 从 Content-Disposition 里取文件名。优先 RFC 5987 的 filename*=UTF-8''…(中文包名、
 * schema 名只能这样传),其次普通 filename=,都没有就用 fallback。
 */
export function filenameFromDisposition(header: string | null | undefined, fallback: string): string {
  if (!header) return fallback
  const star = /filename\*\s*=\s*(?:UTF-8|utf-8)''([^;]+)/.exec(header)
  if (star) {
    try {
      const v = decodeURIComponent(star[1].trim())
      if (v) return v
    } catch { /* 编码坏了就往下走 */ }
  }
  const plain = /filename\s*=\s*"?([^";]+)"?/.exec(header)
  if (plain && plain[1].trim()) return plain[1].trim()
  return fallback
}

/** 把一个 Blob 当作文件交给浏览器保存。 */
export function saveBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  // 立刻 revoke 会让部分浏览器拿不到内容 —— 留一拍。
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
