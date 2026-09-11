// WsTerminal — resilient WebSocket client for the terminal channel.
//
// Adds over a bare WebSocket: exponential-backoff auto-reconnect and an
// app-level ping/pong heartbeat (browsers can't send native ping frames), so a
// half-open socket is detected and recycled instead of silently swallowing
// commands. The backend answers {type:"ping"} with {type:"pong"}.

export type WsStatus = 'connecting' | 'open' | 'closed'

export interface WsTerminalOpts {
  url: () => string // recomputed on every (re)connect so the token stays fresh
  // Optional WebSocket subprotocols, recomputed per (re)connect. Used to carry
  // the auth token in the Sec-WebSocket-Protocol header instead of the query
  // string, keeping it out of access logs / Referer.
  protocols?: () => string[]
  onMessage: (m: any) => void
  onStatus: (s: WsStatus, attempt: number) => void
  // Called when the socket repeatedly fails to even open (likely a rejected /
  // expired token). The client stops reconnecting; the app should force a
  // re-login (M16).
  onAuthError?: () => void
  heartbeatMs?: number
  pongTimeoutMs?: number
}

export class WsTerminal {
  private ws: WebSocket | null = null
  private attempt = 0
  private closedByUser = false
  private hbTimer: ReturnType<typeof setInterval> | null = null
  private pongTimer: ReturnType<typeof setTimeout> | null = null
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null
  private failedOpens = 0 // consecutive sockets that closed before ever opening
  private static readonly MAX_FAILED_OPENS = 3
  private readonly heartbeatMs: number
  private readonly pongTimeoutMs: number

  constructor(private opts: WsTerminalOpts) {
    this.heartbeatMs = opts.heartbeatMs ?? 20000
    this.pongTimeoutMs = opts.pongTimeoutMs ?? 5000
  }

  get isOpen() {
    return this.ws?.readyState === WebSocket.OPEN
  }

  connect() {
    this.closedByUser = false
    this.failedOpens = 0 // fresh budget so a resumed session isn't instantly re-flagged
    this.open()
  }

  private open() {
    this.opts.onStatus('connecting', this.attempt)
    const protocols = this.opts.protocols?.()
    // An empty token (e.g. after logout) makes the subprotocol invalid and would
    // throw in the WebSocket constructor, spinning a fake reconnect loop. Treat a
    // missing credential as an auth failure and stop (R27).
    if (protocols && protocols.some((p) => p === '')) {
      this.closedByUser = true
      this.opts.onStatus('closed', this.attempt)
      this.opts.onAuthError?.()
      return
    }
    let ws: WebSocket
    try {
      ws =
        protocols && protocols.length
          ? new WebSocket(this.opts.url(), protocols)
          : new WebSocket(this.opts.url())
    } catch {
      this.scheduleReconnect()
      return
    }
    this.ws = ws
    let opened = false
    ws.onopen = () => {
      opened = true
      this.attempt = 0
      this.failedOpens = 0
      this.opts.onStatus('open', 0)
      this.startHeartbeat()
    }
    ws.onmessage = (ev) => {
      let m: any
      try {
        m = JSON.parse(ev.data)
      } catch {
        return
      }
      if (m.type === 'pong') {
        this.clearPongTimer()
        return
      }
      this.opts.onMessage(m)
    }
    ws.onclose = () => {
      this.stopHeartbeat()
      this.opts.onStatus('closed', this.attempt)
      if (this.closedByUser) return
      // A socket that closes without ever opening usually means the handshake was
      // rejected (bad/expired token). After a few such failures, stop hammering
      // the server and hand off to the auth-error handler (M16).
      if (!opened) {
        this.failedOpens++
        if (this.failedOpens >= WsTerminal.MAX_FAILED_OPENS && this.opts.onAuthError) {
          this.closedByUser = true
          this.opts.onAuthError()
          return
        }
      }
      this.scheduleReconnect()
    }
    ws.onerror = () => ws.close()
  }

  private scheduleReconnect() {
    if (this.closedByUser) return
    this.attempt++
    const delay = Math.min(15000, 500 * 2 ** Math.min(this.attempt, 5)) // 1s..15s
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer)
    this.reconnectTimer = setTimeout(() => this.open(), delay)
  }

  private startHeartbeat() {
    this.stopHeartbeat()
    this.hbTimer = setInterval(() => {
      if (!this.isOpen) return
      this.send({ type: 'ping' })
      this.clearPongTimer()
      this.pongTimer = setTimeout(() => {
        // no pong in time → assume half-open, force a reconnect cycle
        try {
          this.ws?.close()
        } catch {
          /* ignore */
        }
      }, this.pongTimeoutMs)
    }, this.heartbeatMs)
  }

  private stopHeartbeat() {
    if (this.hbTimer) clearInterval(this.hbTimer)
    this.hbTimer = null
    this.clearPongTimer()
  }
  private clearPongTimer() {
    if (this.pongTimer) clearTimeout(this.pongTimer)
    this.pongTimer = null
  }

  send(obj: any): boolean {
    if (!this.isOpen) return false
    try {
      this.ws!.send(JSON.stringify(obj))
      return true
    } catch {
      return false
    }
  }

  /**
   * 立即重连(「刷新会话」按的就是它)。
   *
   * 必须先把挂着的退避定时器清掉再自己 open。只调 `close()` 是不够的:socket
   * 早就 CLOSED 了,close 是空操作,不会触发 onclose,于是这一次"立即"要等退避
   * 定时器到期 —— 最长 15 秒里按钮看着像没反应。`close()` 那边清了这个定时器,
   * 说明作者知道要清,只是这里漏了。
   */
  reconnect() {
    this.attempt = 0
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer)
      this.reconnectTimer = null
    }
    const live = this.ws && this.ws.readyState === WebSocket.OPEN
    try {
      this.ws?.close()
    } catch {
      /* ignore */
    }
    // socket 已经关着时不会有 onclose 把我们带回 open(),自己来。
    if (!live) this.open()
  }

  close() {
    this.closedByUser = true
    this.stopHeartbeat()
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer)
    try {
      this.ws?.close()
    } catch {
      /* ignore */
    }
    this.ws = null
  }
}
