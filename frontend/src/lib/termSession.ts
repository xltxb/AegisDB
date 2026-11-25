import { sessionHeaders } from '@/lib/session'

// Web 命令行每个标签页独占一条数据库连接:前端给每个标签一个随机标识,随执行带给网关;
// 标签关掉时通知网关断开。浏览器直接关掉 / 刷新时来不及走正常请求,用 keepalive 的 fetch
// 尽量送达;送不到也没关系,网关会在空闲超时(跟随空闲锁定)后自己断开。

const live = new Set<string>()

export function newTermSessionId(): string {
  let id: string
  try {
    id = crypto.randomUUID()
  } catch {
    // 非安全上下文(http 访问)里没有 randomUUID
    id = Array.from({ length: 4 }, () => Math.random().toString(16).slice(2, 10)).join('-')
  }
  live.add(id)
  return id
}

function closeRequest(id: string) {
  const base = import.meta.env.VITE_API_BASE || '/api/v1'
  try {
    void fetch(`${base}/terminal/sessions/close`, {
      method: 'POST',
      keepalive: true,
      headers: { 'Content-Type': 'application/json', ...sessionHeaders() },
      body: JSON.stringify({ sessionId: id }),
    }).catch(() => {})
  } catch { /* 关不掉就交给网关的空闲回收 */ }
}

export function releaseTermSession(id: string) {
  if (!live.delete(id)) return
  closeRequest(id)
}

// 关掉 / 刷新整个页面:所有还开着的标签一起释放
if (typeof window !== 'undefined') {
  window.addEventListener('pagehide', () => {
    for (const id of live) closeRequest(id)
    live.clear()
  })
}
