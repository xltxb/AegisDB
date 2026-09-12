import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { BusFront, Trash2, Plus } from 'lucide-react'
import { useExecWindows, useCreateExecWindow, useDeleteExecWindow } from '@/hooks/useExecWindows'
import { connectionsQueryOptions } from '@/api/modules/connections'
import { Table, type Column } from '@/components/common/Table'
import { Badge, toneOfStatus } from '@/components/common/Badge'
import { Button } from '@/components/common/Button'
import { Modal } from '@/components/common/Modal'
import { Loading, ErrorState, Empty } from '@/components/common/States'
import type { ExecWindow, Connection } from '@/types'

/** ISO 星期(1=周一…7=周日)的文案键 —— 值跟着界面语言走。 */
const WEEKDAY_KEY = ['wd1', 'wd2', 'wd3', 'wd4', 'wd5', 'wd6', 'wd7']

/** 把 00:00 起的分钟数显示成 HH:MM。 */
function hhmm(min: number): string {
  const h = Math.floor(min / 60) % 24
  const m = min % 60
  return `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}`
}

export default function ExecWindowsPage() {
  const { t } = useTranslation()
  const { data, isLoading, error, refetch } = useExecWindows()
  const { data: conns } = useQuery(connectionsQueryOptions())
  const create = useCreateExecWindow()
  const del = useDeleteExecWindow()
  const [open, setOpen] = useState(false)

  const connName = useMemo(() => {
    const m = new Map<number, string>()
    for (const c of (conns ?? []) as Connection[]) m.set(c.id, `${c.env}-${c.name}`)
    return m
  }, [conns])

  /**
   * 状态一列只显示服务端算好的东西。
   *
   * 跨午夜与时区换算在这里再写一遍必然与后端分叉,而分叉的样子是「界面说开着、
   * 网关说没开」。`active` 与 `expired` 都由后端给。
   */
  function statusOf(w: ExecWindow) {
    if (w.status !== 'approved') return { tone: toneOfStatus(w.status), label: t(`win_${w.status}`) }
    if (!w.enabled) return { tone: 'neutral' as const, label: t('winDisabled') }
    if (w.expired) return { tone: 'neutral' as const, label: t('winExpired') }
    if (w.active) return { tone: 'success' as const, label: t('winActive') }
    return { tone: 'accent' as const, label: t('winWaiting') }
  }

  const columns: Column<ExecWindow>[] = [
    {
      key: 'win', head: t('winColWin'), width: '1.5fr',
      cell: (w) => (
        <div>
          <div className="cell-strong">{w.name}</div>
          <div className="cell-sub">
            {w.kind === 'once'
              ? `${t('winOnce')} · ${(w.startsAt || '').slice(0, 16).replace('T', ' ')}`
              : `${t('winRecurring')} · ${hhmm(w.startMin)}–${hhmm(w.endMin)}${
                  w.endMin <= w.startMin ? ` (${t('winCrossMidnight')})` : ''
                }`}
          </div>
        </div>
      ),
    },
    {
      key: 'db', head: t('winColDb'), width: '1.1fr', mono: true,
      cell: (w) => `${connName.get(w.connectionId) ?? `#${w.connectionId}`} · ${w.database}`,
    },
    {
      key: 'tz', head: t('winColTz'), width: '1.2fr', mono: true,
      cell: (w) => (w.kind === 'recurring' ? w.timezone : '—'),
    },
    {
      key: 'days', head: t('winColDays'), width: '0.8fr',
      cell: (w) =>
        w.kind === 'once'
          ? '—'
          : w.weekdays
            ? w.weekdays.split(',').map((d) => t(WEEKDAY_KEY[Number(d) - 1])).join(' ')
            : t('winEveryDay'),
    },
    {
      key: 'status', head: t('winColStatus'), width: '1.1fr',
      cell: (w) => {
        const s = statusOf(w)
        return <Badge tone={s.tone}>{s.label}</Badge>
      },
    },
    {
      key: 'op', head: '', width: '1.5fr',
      cell: (w) => (
        <div className="row-ops">
          <span className="cell-sub">{w.apNo}</span>
          <Button
            variant="ghost"
            title={t('winClose')}
            disabled={del.isPending}
            onClick={() => del.mutate(w.id)}
          >
            <Trash2 size={14} />
          </Button>
        </div>
      ),
    },
  ]

  const rows = data ?? []

  return (
    <div className="page page-narrow">
      <header className="page-head">
        <div>
          <h1>{t('busTitle')}</h1>
          <p>{t('busSub')}</p>
        </div>
        <div className="grow">
          <Button variant="primary" onClick={() => setOpen(true)}>
            <Plus size={15} />{t('busApply')}
          </Button>
        </div>
      </header>

      <div className="notice">
        <BusFront size={15} />
        {t('busBoundary')}
      </div>

      {isLoading && <Loading />}
      {error && <ErrorState error={error} retry={() => refetch()} />}
      {!isLoading && !error && (
        <Table
          columns={columns}
          rows={rows}
          rowKey={(w) => w.id}
          empty={<Empty hint={t('winEmpty')} />}
        />
      )}

      <ApplyModal
        open={open}
        conns={(conns ?? []) as Connection[]}
        busy={create.isPending}
        onClose={() => setOpen(false)}
        onSubmit={(body) => create.mutate(body, { onSuccess: () => setOpen(false) })}
      />
    </div>
  )
}

function ApplyModal({
  open, conns, busy, onClose, onSubmit,
}: {
  open: boolean
  conns: Connection[]
  busy: boolean
  onClose: () => void
  onSubmit: (b: Partial<ExecWindow>) => void
}) {
  const { t } = useTranslation()
  const [f, setF] = useState<Partial<ExecWindow>>({
    kind: 'recurring', timezone: 'Asia/Shanghai', startMin: 120, endMin: 240, weekdays: '',
  })
  const set = (patch: Partial<ExecWindow>) => setF((p) => ({ ...p, ...patch }))

  return (
    <Modal
      open={open}
      title={t('busApply')}
      sub={t('busApplySub')}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>{t('cancel')}</Button>
          <Button
            variant="primary"
            disabled={busy || !f.name || !f.connectionId || !f.database}
            onClick={() => onSubmit(f)}
          >
            {t('busSubmit')}
          </Button>
        </>
      }
    >
      <div className="fld">
        <label>{t('winName')}</label>
        <input value={f.name ?? ''} onChange={(e) => set({ name: e.target.value })} />
      </div>
      <div className="fld-2">
        <div className="fld">
          <label>{t('winInstance')}</label>
          <select
            value={f.connectionId ?? 0}
            onChange={(e) => set({ connectionId: Number(e.target.value) })}
          >
            <option value={0}>—</option>
            {conns.map((c) => <option key={c.id} value={c.id}>{c.env}-{c.name}</option>)}
          </select>
        </div>
        <div className="fld">
          {/* 库名必填:一台实例底下往往混着不同业务的库,放开面越小越好。 */}
          <label>{t('winDatabase')}</label>
          <input value={f.database ?? ''} onChange={(e) => set({ database: e.target.value })} />
        </div>
      </div>
      <div className="fld-2">
        <div className="fld">
          <label>{t('winKind')}</label>
          <select value={f.kind} onChange={(e) => set({ kind: e.target.value as ExecWindow['kind'] })}>
            <option value="recurring">{t('winRecurring')}</option>
            <option value="once">{t('winOnce')}</option>
          </select>
        </div>
        <div className="fld">
          <label>{t('winColTz')}</label>
          <input value={f.timezone ?? ''} onChange={(e) => set({ timezone: e.target.value })} />
        </div>
      </div>
      {f.kind === 'recurring' ? (
        <div className="fld-2">
          <div className="fld">
            <label>{t('winStart')}</label>
            <input
              type="time"
              value={hhmm(f.startMin ?? 0)}
              onChange={(e) => {
                const [h, m] = e.target.value.split(':').map(Number)
                set({ startMin: h * 60 + m })
              }}
            />
          </div>
          <div className="fld">
            <label>{t('winEnd')}</label>
            <input
              type="time"
              value={hhmm(f.endMin ?? 0)}
              onChange={(e) => {
                const [h, m] = e.target.value.split(':').map(Number)
                set({ endMin: h * 60 + m })
              }}
            />
          </div>
        </div>
      ) : (
        <div className="fld-2">
          <div className="fld">
            <label>{t('winStartsAt')}</label>
            <input type="datetime-local" value={(f.startsAt ?? '').slice(0, 16)}
                   onChange={(e) => set({ startsAt: e.target.value })} />
          </div>
          <div className="fld">
            <label>{t('winEndsAt')}</label>
            <input type="datetime-local" value={(f.endsAt ?? '').slice(0, 16)}
                   onChange={(e) => set({ endsAt: e.target.value })} />
          </div>
        </div>
      )}
      <div className="fld">
        <label>{t('winReason')}</label>
        <textarea value={f.reason ?? ''} onChange={(e) => set({ reason: e.target.value })} />
      </div>
    </Modal>
  )
}
