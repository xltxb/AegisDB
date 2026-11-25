// 管理后台的独立空闲超时(工单 09)。
//
// 停在管理后台无操作满 adminIdleMinutes(会话策略里读,默认 5)就退回用户后台,不登出 —— 终端标签与
// 事务都不受影响。人有操作时报心跳(节流),后端据此判断"这个管理会话还活着";后端那边超时了
// (页面被挂起、前端计时失效)会回 40304,同样退回。
//
// 心跳节流不能让后端看到的"最后一次操作"落后太多(2026-10 审查):从前每分钟最多一次、且节流掉的
// 操作不补报 —— 最后一分钟里的操作后端看不见,人明明 5 分钟内还在点,接口却回 40304;空闲时长设成
// 1 分钟时更是隔一会儿就被踢。现在间隔取 min(30 秒, 空闲时长的 1/4),被节流的那次在间隔到了补一拍
// (后端另留 30 秒余量)。滚动不冒泡,要在捕获阶段听,面板里的滚动才算;鼠标移动也算操作。
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import api from '@/api'
import { CODE_OK, CODE_ADMIN_IP_BLOCKED } from '@/api/codes'
import { hasUnsavedChanges, clearUnsaved } from '@/lib/unsavedChanges'
import { useUIStore } from '@/stores/ui'
import { useEvent } from '@/hooks/useEvent'
import { useI18n } from '@/hooks/useI18n'
import { nav } from '@/hooks/useNav'

const ADMIN_EVENTS = ['mousedown', 'mousemove', 'wheel', 'keydown', 'scroll', 'touchstart'] as const

type Timer = ReturnType<typeof setTimeout> | null

export function useAdminIdle(inAdmin: boolean) {
  const { t } = useI18n()
  const [locked, setLocked] = useState(false)
  const [unlocking, setUnlocking] = useState(false)
  const [lockMsg, setLockMsg] = useState('')
  const st = useRef({
    locked: false, idleMs: 5 * 60 * 1000, timer: null as Timer, trailing: null as Timer,
    lastBeat: 0, lastKick: 0, notices: {} as Record<string, number>,
  }).current
  const inAdminRef = useRef(false)

  // 同一件事几秒内只提示一次:一个页面同时发出好几个管理接口请求(管理总览一次取五六样),每个都回
  // 40303 / 40304,从前就弹五六个一模一样的提示(2026-10 审查)。
  function noticeOnce(key: string, msg: string, kind: 'error' | 'info') {
    const now = Date.now()
    if (now - (st.notices[key] || 0) < 5000) return
    st.notices[key] = now
    useUIStore.getState().notify(msg, kind, 6000)
  }
  function setLock(on: boolean) { st.locked = on; setLocked(on) }
  function clear(k: 'timer' | 'trailing') {
    if (st[k]) clearTimeout(st[k])
    st[k] = null
  }
  function kickOut(msg: string) {
    noticeOnce('idle', msg, 'info')
    void nav.push('/dashboard')
  }

  // 空闲到点时当前页有没保存的改动(2026-10-26 回归排查 10):不跳走 —— 跳走页面就销毁了,改到一半的
  // 配置 / 权限跟着丢。改为遮罩锁住当前页(不可操作),重新进入管理后台(POST /admin/enter,和从用户
  // 后台走进来是同一道门:角色 + 管理后台 IP 白名单)之后原地解锁,内容还在。没有改动的照旧弹回总览。
  function lock() {
    if (st.locked) return
    setLock(true)
    setLockMsg('')
    clear('timer')
    clear('trailing')
    // 焦点留在表单里的话,键盘还能接着往被锁住的输入框里打字
    ;(document.activeElement as HTMLElement | null)?.blur?.()
  }
  const idleOut = useEvent(() => {
    if (!inAdminRef.current) return
    if (hasUnsavedChanges()) lock()
    else kickOut(t('adminIdleBack'))
  })
  const arm = useEvent(() => {
    clear('timer')
    if (inAdminRef.current) st.timer = setTimeout(idleOut, st.idleMs)
  })
  async function unlock() {
    if (unlocking) return
    setUnlocking(true)
    setLockMsg('')
    try {
      const res = await api.adminEnter()
      if (res.code === CODE_OK) {
        setLock(false)
        st.lastBeat = Date.now()
        arm()
      } else {
        setLockMsg(res.code === CODE_ADMIN_IP_BLOCKED ? t('adminIpBlockedStay') : (res.msg || t('actionFailed')))
      }
    } catch {
      setLockMsg(t('actionFailed'))
    } finally {
      setUnlocking(false)
    }
  }
  // 锁屏里选"放弃修改":清掉登记,照旧回总览
  function leave() {
    clearUnsaved()
    setLock(false)
    kickOut(t('adminIdleBack'))
  }

  const beat = useEvent(() => {
    st.trailing = null
    if (!inAdminRef.current) return
    st.lastBeat = Date.now()
    api.adminHeartbeat().catch(() => {})
  })
  const kick = useEvent(() => {
    if (!inAdminRef.current || st.locked) return // 锁着时的鼠标移动不算操作,也不报心跳
    const now = Date.now()
    if (now - st.lastKick < 1000) return // mousemove 一秒能来几十次,一秒算一次足够
    st.lastKick = now
    arm()
    const wait = st.lastBeat + Math.min(30_000, st.idleMs / 4) - now
    if (wait <= 0) beat()
    else if (!st.trailing) st.trailing = setTimeout(beat, wait) // 节流掉的这次到点补报
  })
  // 管理接口说"你的 IP 不在管理后台白名单内"(工单 08,api/http.ts 发出这个事件):提示,并退回用户后台。
  const onIpBlocked = useEvent((e: Event) => {
    noticeOnce('ip', (e as CustomEvent<string>).detail || t('adminIpBlocked'), 'error')
    if (inAdminRef.current) void nav.push('/dashboard')
  })
  const onAdminIdle = useEvent((e: Event) => {
    if (!inAdminRef.current) return
    // 后端先判了超时(页面被挂起、前端计时失效):有没保存的改动同样锁屏,不跳走
    if (st.locked || hasUnsavedChanges()) { lock(); return }
    kickOut((e as CustomEvent<string>).detail || t('adminIdleBack'))
  })

  // 刚进管理后台时路由已经调过 enter,心跳从此刻算起。
  // "在不在管理后台"与"心跳起点"必须同一刻生效,而且在绘制之前:分开的话,夹在中间的一次点击会拿着
  // 0 当起点,刚进来就多报一次心跳。
  useLayoutEffect(() => {
    inAdminRef.current = inAdmin
    if (inAdmin) st.lastBeat = Date.now()
    else { st.locked = false; setLocked(false) }
    arm()
  }, [inAdmin, arm, st])

  useEffect(() => {
    window.addEventListener('aegisdb:admin-ip-blocked', onIpBlocked)
    window.addEventListener('aegisdb:admin-idle', onAdminIdle)
    ADMIN_EVENTS.forEach((e) => window.addEventListener(e, kick, { passive: true, capture: true }))
    return () => {
      window.removeEventListener('aegisdb:admin-ip-blocked', onIpBlocked)
      window.removeEventListener('aegisdb:admin-idle', onAdminIdle)
      ADMIN_EVENTS.forEach((e) => window.removeEventListener(e, kick, { capture: true }))
      if (st.timer) clearTimeout(st.timer)
      if (st.trailing) clearTimeout(st.trailing)
      st.timer = st.trailing = null
    }
  }, [onIpBlocked, onAdminIdle, kick, st])

  /** 会话策略读到之后更新空闲时长。 */
  function setIdleMs(ms: number) { st.idleMs = ms; arm() }

  return { locked: locked && inAdmin, unlocking, lockMsg, unlock, leave, setIdleMs }
}
