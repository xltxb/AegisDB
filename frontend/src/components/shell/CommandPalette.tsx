import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import clsx from 'clsx'
import { Database, Search, type LucideIcon } from 'lucide-react'
import { connectionsQueryOptions } from '@/api/modules/connections'
import type { Connection } from '@/types'

/** 命令面板收的一条候选。`sub` 是数据类内容(实例名、地址),不进 i18n。 */
export interface PaletteEntry {
  to: string
  label: string
  sub?: string
  icon: LucideIcon
  /** 参与匹配的文本,已经拼好并小写。 */
  hay: string
}

/** 外壳把当前门户下**已经过菜单闸**的导航项交过来,面板不自己判权限。 */
export interface PalettePage {
  to: string
  label: string
  icon: LucideIcon
}

/**
 * ⌘K 命令面板。
 *
 * 候选只有两类:**页面**和**实例**。两类都是本来就在这个人手里的东西 —— 页面按
 * 菜单闸收敛过,实例来自 `GET /connections`(服务端已按标签收过范围)。
 *
 * 刻意**不**做全局全文检索:后端没有那样一个接口,凑一个出来只能在前端已经拉到的
 * 几页数据里翻 —— 那会变成一个"搜得到的东西取决于你刚才打开过哪一页"的搜索框,
 * 比没有搜索更坏。
 */
export default function CommandPalette({
  open, onClose, pages, instanceTo,
}: {
  open: boolean
  onClose: () => void
  pages: PalettePage[]
  /** 选中一台实例后去哪一页;空字符串 = 这个人没有能落脚的实例页,不收实例。 */
  instanceTo: string
}) {
  const { t } = useTranslation()
  const nav = useNavigate()
  const [q, setQ] = useState('')
  const [cursor, setCursor] = useState(0)

  const { data: conns } = useQuery({
    ...connectionsQueryOptions(),
    enabled: open && !!instanceTo,
  })

  // 每次打开都从空开始:上一次搜的词留在框里,下一次打开看见的是一份过期的结果。
  useEffect(() => {
    setQ('')
    setCursor(0)
  }, [open])

  if (!open) return null

  const needle = q.trim().toLowerCase()

  const pageEntries: PaletteEntry[] = pages.map((p) => ({
    to: p.to, label: p.label, icon: p.icon,
    hay: `${p.label} ${p.to}`.toLowerCase(),
  }))

  const connEntries: PaletteEntry[] = instanceTo
    ? ((conns ?? []) as Connection[]).map((c) => ({
        to: instanceTo,
        label: c.name,
        sub: `${c.env} · ${c.engine} · ${c.host}`,
        icon: Database,
        hay: `${c.name} ${c.env} ${c.engine} ${c.host} ${c.tags}`.toLowerCase(),
      }))
    : []

  const match = (e: PaletteEntry) => !needle || e.hay.includes(needle)
  const hitPages = pageEntries.filter(match)
  const hitConns = connEntries.filter(match).slice(0, 8)
  const flat = [...hitPages, ...hitConns]
  const active = Math.min(cursor, Math.max(0, flat.length - 1))

  function go(e: PaletteEntry | undefined) {
    if (!e) return
    onClose()
    nav(e.to)
  }

  function onKey(ev: React.KeyboardEvent) {
    if (ev.key === 'Escape') { onClose(); return }
    if (ev.key === 'ArrowDown') { ev.preventDefault(); setCursor((c) => Math.min(c + 1, flat.length - 1)); return }
    if (ev.key === 'ArrowUp') { ev.preventDefault(); setCursor((c) => Math.max(c - 1, 0)); return }
    if (ev.key === 'Enter') { ev.preventDefault(); go(flat[active]) }
  }

  const row = (e: PaletteEntry, i: number) => (
    <button
      key={`${e.to}-${e.label}-${i}`}
      className={clsx('cmdk-row', i === active && 'on')}
      onMouseEnter={() => setCursor(i)}
      onClick={() => go(e)}
    >
      <e.icon size={15} />
      <span className="cmdk-label">{e.label}</span>
      {e.sub && <span className="cmdk-sub">{e.sub}</span>}
    </button>
  )

  return (
    <div className="cmdk-overlay" onMouseDown={(ev) => { if (ev.target === ev.currentTarget) onClose() }}>
      <div className="cmdk" role="dialog" aria-modal="true">
        <div className="cmdk-input">
          <Search size={16} />
          <input
            autoFocus
            value={q}
            placeholder={t('cmdkPlaceholder')}
            onChange={(ev) => { setQ(ev.target.value); setCursor(0) }}
            onKeyDown={onKey}
          />
          <kbd>esc</kbd>
        </div>
        <div className="cmdk-list">
          {!flat.length && <div className="cmdk-empty">{t('cmdkEmpty')}</div>}
          {!!hitPages.length && <div className="cmdk-sec">{t('cmdkPages')}</div>}
          {hitPages.map((e, i) => row(e, i))}
          {!!hitConns.length && <div className="cmdk-sec">{t('cmdkInstances')}</div>}
          {hitConns.map((e, i) => row(e, hitPages.length + i))}
        </div>
        <div className="cmdk-foot">{t('cmdkHint')}</div>
      </div>
    </div>
  )
}
