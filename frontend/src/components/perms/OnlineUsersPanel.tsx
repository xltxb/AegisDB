// 管理后台「在线用户」:此刻谁登录着网关、从哪来、最近一次操作、在哪些实例上开着终端。
//
// 数据来自服务端的在线记录(见 backend/internal/presence):3 分钟内有请求、或者还开着终端的会话
// 算在线;退出登录 / 停用 / 重置口令后立即消失。只记在内存里 —— 网关重启后清空,几十秒内开着的
// 页面再次请求就又回来了。
//
// 「最近操作」与「在线」分开:页面开着就会定时轮询,所以一个挂了一整天没人动的标签页一直"在线";
// 最近操作不算轮询,说的是人还在不在用。超过 10 分钟没操作的标成"空闲"。
import { useState, type HTMLAttributes } from 'react'
import clsx from 'clsx'
import { Search, TerminalSquare, RotateCw } from 'lucide-react'
import api from '@/api'
import type { OnlineSession } from '@/types'
import { relTime, absTime } from '@/lib/relTime'
import { useMountEffect } from '@/hooks/effects'
import { useI18n } from '@/hooks/useI18n'
import { useNow } from '@/hooks/useNow'
import './OnlineUsersPanel.scoped.css'

const IDLE_MS = 10 * 60_000

export default function OnlineUsersPanel({ className, ...rest }: HTMLAttributes<HTMLDivElement>) {
  const { t } = useI18n()
  const now = useNow(15_000)

  const [sessions, setSessions] = useState<OnlineSession[]>([])
  const [users, setUsers] = useState(0)
  const [terminals, setTerminals] = useState(0)
  const [loaded, setLoaded] = useState(false)
  const [failed, setFailed] = useState('')
  const [q, setQ] = useState('')

  const idle = (s: OnlineSession) => !s.lastAction || now - new Date(s.lastAction).getTime() > IDLE_MS

  const k = q.trim().toLowerCase()
  const shown = !k ? sessions : sessions.filter((s) =>
    [s.name, s.email, s.dept, s.ip, ...(s.terminals ?? []).map((x) => x.instance)].some((v) => (v || '').toLowerCase().includes(k)))

  async function load() {
    try {
      const r = await api.onlineUsers()
      setSessions(r.sessions ?? [])
      setUsers(r.users)
      setTerminals(r.terminals)
      setFailed('')
    } catch (e: any) {
      setFailed(e?.message || t('loadFailed'))
    } finally {
      setLoaded(true)
    }
  }

  // 15 秒刷新一次:在线列表本来就是"此刻",停着不动的列表比没有更误导
  useMountEffect(() => {
    void load()
    const timer = setInterval(load, 15_000)
    return () => clearInterval(timer)
  })

  return (
    <div {...rest} className={clsx('olp', className)} data-testid="online-panel">
      <div className="olbar">
        <div className="olsum" data-testid="online-summary">
          <span className="dot" />{t('olSummary', { users, sessions: sessions.length, terminals })}
        </div>
        <div className="olsearch"><Search size={13} /><input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t('olSearchPh')} /></div>
        <button className="olref" title={t('olRefresh')} onClick={load}><RotateCw size={13} /></button>
      </div>
      <div className="olnote">{t('olNote')}</div>
      {failed && <div className="olerr">{failed}</div>}
      <div className="olt">
        <div className="olh">
          <span>{t('olColUser')}</span><span>{t('olColFrom')}</span><span>{t('olColLogin')}</span>
          <span>{t('olColAction')}</span><span>{t('olColTerm')}</span>
        </div>
        {loaded && !shown.length && <div className="olempty">{q ? t('olEmptyFiltered') : t('olEmpty')}</div>}
        {shown.map((s, i) => (
          <div key={`${s.userId}-${s.loginAt}-${s.ip}-${i}`} className="olr" data-testid="online-row">
            <div className="cell user">
              <div className="ava">{s.initials || (s.name || '').slice(0, 1)}</div>
              <div className="nm"><div className="b nm">{s.name}</div><div className="s nm">{s.email}{s.dept && s.dept !== '—' && ` · ${s.dept}`}</div></div>
            </div>
            <div className="cell"><div className="b nm mono">{s.ip || '—'}</div><div className="s nm">{s.browser || '—'}</div></div>
            <div className="cell s" title={absTime(s.loginAt)}>{s.loginAt ? relTime(t, s.loginAt, now) : '—'}</div>
            <div className="cell" title={absTime(s.lastAction)}>
              <span className={clsx('pill', idle(s) ? 'idle' : 'active')} data-testid="online-state">{idle(s) ? t('olIdle') : t('olActive')}</span>
              <span className="s">{relTime(t, s.lastAction, now)}</span>
            </div>
            <div className="cell terms">
              {!s.terminals?.length && <span className="s">—</span>}
              {s.terminals?.map((tm, j) => (
                <span
                  key={`${tm.openedAt}${tm.instance}-${j}`} className={clsx('term', tm.running && 'run')}
                  title={tm.running ? t('olTermRunning') : absTime(tm.lastExecAt || tm.openedAt)} data-testid="online-term"
                >
                  <TerminalSquare size={12} />{tm.instance || t('olTermIdle')}{tm.running && <span className="rdot" />}
                </span>
              ))}
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}
