// download — 把一个 Blob 交给浏览器保存。
//
// 十行代码,单独成文件,是因为其中两行是**被踩出来的**,而它们只在某些浏览器上才
// 露面 —— 抄错了不会报错,只会在某个人的某个浏览器上「点了没反应」,而那是最难被
// 报上来的一种故障。

/**
 * 触发一次文件下载。
 *
 * 两个不能省的动作:
 *
 *   · `document.body.appendChild(a)` —— 不在文档里的 <a>,click() 在 Firefox 上
 *     不触发下载。用完就 remove,页面上不留痕迹。
 *   · `revokeObjectURL` **延后一拍** —— 同步撤销时 Safari 偶尔会在下载真正开始
 *     之前就丢掉那个 URL。
 */
export function downloadBlob(filename: string, blob: Blob): void {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
