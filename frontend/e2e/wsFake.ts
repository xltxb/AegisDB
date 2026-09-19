import { expect, type Page, type WebSocketRoute } from '@playwright/test'

export interface Frame { type: string; [k: string]: unknown }

export interface WsFake {
  /** 客户端发过来的帧,按顺序。**不含 ping** —— 心跳由替身自己答掉。 */
  readonly sent: Frame[]
  sentOf(type: string): Frame[]
  /** 等到客户端发出第 n 条该类型的帧,并把它返回。 */
  waitFor(type: string, n?: number): Promise<Frame>
  /** 按剧本回一帧。说话对象永远是最新那条活 socket。 */
  send(frame: Frame): void
  /** 单方面断开。客户端会在 1 秒后自动重连 —— 要稳住断开状态请用 setOffline。 */
  drop(): void
  /**
   * 拒不接客。
   *
   * `drop()` 之后 `WsTerminal.scheduleReconnect` 第一次退避是 500×2¹ = **1 秒**,
   * 于是「断开后状态灯转红」这个断言会和自动重连赛跑。offline 让断开成为一个
   * 稳定状态:新 socket 一建起就关掉,客户端一直停在 closed。
   */
  setOffline(v: boolean): void
  stats(): { opened: number; peakLive: number; live: number }
}

/**
 * 终端那条 WebSocket 的脚本化替身。
 *
 * 它顶替的是网关,所以要守住网关这一侧的两条约定,否则规格会毫无征兆地飘:
 *
 *  · **自动回 pong。** 客户端每 20s 发一次 ping,5s 内收不到 pong 就判定半开并
 *    强制重连(见 lib/wsTerminal.ts 的 startHeartbeat)。
 *  · **永远对最新那条活 socket 说话。** StrictMode 下 effect 跑两遍,第一条被
 *    清理函数关掉,留活的是第二条。
 *
 * 用 routeWebSocket 而不是起一个真网关:e2e 的前提是「纯前端、几秒钟」,起一个
 * Go 后端加一个库会把这个前提换掉;而这里要验的是前端的生命周期,不是服务端的
 * 判定 —— 那些已经有后端测试在管。
 */
export async function installWsFake(
  page: Page,
  onFrame?: (frame: Frame, fake: WsFake) => void,
): Promise<WsFake> {
  const sent: Frame[] = []
  const live: WebSocketRoute[] = []
  let opened = 0
  let peakLive = 0
  let offline = false

  const newest = () => live[live.length - 1]

  const fake: WsFake = {
    sent,
    sentOf: (type) => sent.filter((f) => f.type === type),
    async waitFor(type, n = 1) {
      await expect
        .poll(() => fake.sentOf(type).length, { message: `等客户端发出第 ${n} 条 "${type}"` })
        .toBeGreaterThanOrEqual(n)
      return fake.sentOf(type)[n - 1]
    },
    send(frame) {
      const ws = newest()
      if (!ws) throw new Error('没有活着的 socket 可发')
      ws.send(JSON.stringify(frame))
    },
    drop() {
      const ws = newest()
      if (!ws) throw new Error('没有活着的 socket 可断')
      ws.close()
    },
    setOffline(v) {
      offline = v
      if (v) for (const ws of [...live]) ws.close()
    },
    stats: () => ({ opened, peakLive, live: live.length }),
  }

  await page.routeWebSocket('**/terminal/ws', (ws) => {
    opened++
    if (offline) { ws.close(); return }
    live.push(ws)
    peakLive = Math.max(peakLive, live.length)
    ws.onClose(() => {
      const i = live.indexOf(ws)
      if (i >= 0) live.splice(i, 1)
    })
    ws.onMessage((raw) => {
      let frame: Frame
      try { frame = JSON.parse(String(raw)) as Frame } catch { return }
      if (frame.type === 'ping') { ws.send(JSON.stringify({ type: 'pong' })); return }
      sent.push(frame)
      onFrame?.(frame, fake)
    })
  })

  return fake
}

/** 一份看得出是「哪一行」的结果。 */
export const ROWS_OUTPUT: Frame = {
  type: 'output', columns: ['id', 'name'], data: [['1', 'alice']], rows: 1, ms: 12,
}
