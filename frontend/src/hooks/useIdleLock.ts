import { useEffect, useRef } from 'react'
import { useQuery } from '@tanstack/react-query'
import { settingsQueryOptions } from '@/api/modules/settings'
import { SESSION_TTL_KEYS, keyOf, parseSetting } from '@/lib/settingOptions'

/** 算"人还在"的事件。滚动和触摸也算 —— 只读一份长报表的人并没有离开。 */
const IDLE_EVENTS = ['mousemove', 'mousedown', 'keydown', 'scroll', 'touchstart'] as const
/** 事件节流:最多每 5s 重置一次计时器。鼠标一动就重挂 setTimeout 太费。 */
const KICK_MS = 5000

export interface IdlePolicy {
  enabled: boolean
  /** 有效空闲时长(毫秒)。 */
  idleMs: number
}

/**
 * 空闲锁定的策略,来自运行时设置。
 *
 * **只有够得到 `GET /settings` 的人才去拉它**(它挂着 `menu("settings")` 闸)。
 * Vue 版对所有人发这一次请求,非管理员每次进控制台都稳定收一个 403 —— 那条 403
 * 在审计与日志里长得跟真正的越权一模一样。拉不到就用文档里的默认值:开着、15 分钟,
 * 这与服务端的默认值一致,所以两边不会分叉。
 */
export function useIdlePolicy(canReadSettings: boolean): IdlePolicy {
  const { data } = useQuery({ ...settingsQueryOptions(), enabled: canReadSettings, retry: false })
  const g = data?.settings ?? {}
  const enabled = parseSetting(g['security.idleLock'], true)
  const mins = Number(parseSetting(g['security.idleMinutes'], 15)) || 15
  // 空闲时长封顶在会话 TTL 上:锁定比令牌本身活得更久没有意义 —— 令牌先过期,人
  // 看到的是一次莫名其妙的 401,而不是"会话已因空闲锁定"。
  const ttl = keyOf(parseSetting<string>(g['security.sessionTTL'], '8h'), SESSION_TTL_KEYS, '8h')
  const ttlHours = parseInt(ttl, 10) || 8
  return { enabled, idleMs: Math.min(mins, ttlHours * 60) * 60_000 }
}

/**
 * 无操作到点就锁。
 *
 * `onLock` 走 ref 而不是进依赖数组:外壳每 20s 因为通知轮询重渲染一次,把回调
 * 放进依赖里会让这个 effect 跟着重挂,而重挂就意味着**倒计时被清零** —— 于是这道
 * 锁在一个 20s 轮询的页面上永远不会触发。
 */
export function useIdleLock(enabled: boolean, idleMs: number, onLock: () => void) {
  const lock = useRef(onLock)
  useEffect(() => { lock.current = onLock })

  useEffect(() => {
    if (!enabled || idleMs <= 0) return
    let timer: ReturnType<typeof setTimeout> | undefined
    let lastKick = 0
    const kick = () => {
      const now = Date.now()
      if (now - lastKick < KICK_MS) return
      lastKick = now
      if (timer) clearTimeout(timer)
      timer = setTimeout(() => lock.current(), idleMs)
    }
    for (const e of IDLE_EVENTS) window.addEventListener(e, kick, { passive: true })
    // 先起一轮:一个打开后就没再动过的标签页也该锁。
    timer = setTimeout(() => lock.current(), idleMs)
    return () => {
      if (timer) clearTimeout(timer)
      for (const e of IDLE_EVENTS) window.removeEventListener(e, kick)
    }
  }, [enabled, idleMs])
}
