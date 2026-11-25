// 读 zip 里的文本文件(sqlplus-compat 08:@ 脚本连同它 @@ 引用的文件打成一个 zip 上传)。
//
// 不引依赖:zip 的结构很简单(末尾的中央目录列出每个文件的位置),解压用浏览器自带的
// DecompressionStream('deflate-raw')。只支持"存储"(0)和"deflate"(8)两种压缩方式 —— 系统
// 自带的压缩、7-Zip、WinRAR 打的 zip 都是这两种;加密的 zip、ZIP64(单个文件超过 4GB)不支持。
//
// 文件名:标志位 11 置位时是 UTF-8;没置位时多半是 Windows 中文系统打的,按 GBK 解(按
// CP437 解出来是一串乱码,@@ 就找不到文件了)。

export interface ZipEntry { name: string; text: string }

const SIG_EOCD = 0x06054b50
const SIG_CEN = 0x02014b50
const SIG_LOC = 0x04034b50

/** 一个文件最大多少(解压后)。脚本不会这么大,超过的多半不是脚本,拒绝而不是把浏览器撑爆。 */
const MAX_ENTRY = 50 * 1024 * 1024

export async function readZipTexts(buf: ArrayBuffer): Promise<ZipEntry[]> {
  const b = new Uint8Array(buf)
  const v = new DataView(buf)
  // 中央目录结束记录在末尾(后面最多跟 64KB 注释)
  let eocd = -1
  for (let i = b.length - 22; i >= Math.max(0, b.length - 22 - 65535); i--) {
    if (v.getUint32(i, true) === SIG_EOCD) { eocd = i; break }
  }
  if (eocd < 0) throw new Error('not a zip file')
  const count = v.getUint16(eocd + 10, true)
  let p = v.getUint32(eocd + 16, true)
  const out: ZipEntry[] = []
  for (let n = 0; n < count; n++) {
    if (v.getUint32(p, true) !== SIG_CEN) throw new Error('corrupt zip directory')
    const flags = v.getUint16(p + 8, true)
    const method = v.getUint16(p + 10, true)
    const csize = v.getUint32(p + 20, true)
    const usize = v.getUint32(p + 24, true)
    const nlen = v.getUint16(p + 28, true)
    const xlen = v.getUint16(p + 30, true)
    const clen = v.getUint16(p + 32, true)
    const loc = v.getUint32(p + 42, true)
    const nameBytes = b.subarray(p + 46, p + 46 + nlen)
    const name = decodeName(nameBytes, (flags & 0x800) !== 0)
    p += 46 + nlen + xlen + clen
    if (name.endsWith('/')) continue // 目录
    if (flags & 0x1) throw new Error(`encrypted zip entry: ${name}`)
    if (usize > MAX_ENTRY) throw new Error(`zip entry too large: ${name}`)
    if (v.getUint32(loc, true) !== SIG_LOC) throw new Error('corrupt zip entry')
    const start = loc + 30 + v.getUint16(loc + 26, true) + v.getUint16(loc + 28, true)
    const data = b.subarray(start, start + csize)
    let raw: Uint8Array
    if (method === 0) raw = data
    else if (method === 8) raw = await inflateRaw(data)
    else throw new Error(`unsupported zip compression (${method}): ${name}`)
    out.push({ name, text: decodeText(raw) })
  }
  return out
}

function decodeName(bytes: Uint8Array, utf8: boolean): string {
  if (utf8) return new TextDecoder('utf-8').decode(bytes)
  // 全是 ASCII 时两种解法一样;否则按 GBK
  if (bytes.every((x) => x < 0x80)) return new TextDecoder('utf-8').decode(bytes)
  try { return new TextDecoder('gbk').decode(bytes) } catch { return new TextDecoder('utf-8').decode(bytes) }
}

/** 脚本内容:有 UTF-8 BOM 去掉;不是合法 UTF-8 时按 GBK(Windows 上用记事本另存的老脚本)。 */
export function decodeText(raw: Uint8Array): string {
  let bytes = raw
  if (bytes[0] === 0xef && bytes[1] === 0xbb && bytes[2] === 0xbf) bytes = bytes.subarray(3)
  try { return new TextDecoder('utf-8', { fatal: true }).decode(bytes) } catch { /* 不是 UTF-8 */ }
  try { return new TextDecoder('gbk').decode(bytes) } catch { return new TextDecoder('utf-8').decode(bytes) }
}

async function inflateRaw(data: Uint8Array): Promise<Uint8Array> {
  const ds = new DecompressionStream('deflate-raw')
  const stream = new Blob([data as BlobPart]).stream().pipeThrough(ds)
  return new Uint8Array(await new Response(stream).arrayBuffer())
}
