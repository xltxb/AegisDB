// New-instance modal (same shape as the edit one).
//
// Creating an instance uses the same modal treatment as editing one: the form
// used to sit permanently at the bottom of the page, below the table, so the
// "new connection" button had nowhere to go and was wired to nothing.
import { useImperativeHandle, useState, type Ref } from 'react'
import { createPortal } from 'react-dom'
import clsx from 'clsx'
import VButton from '@/components/common/VButton'
import VSelect from '@/components/common/VSelect'
import api from '@/api'
import { useI18n } from '@/hooks/useI18n'
import { useUnsavedFlag } from '@/hooks/useUnsavedFlag'
import { sameForm } from '@/lib/unsavedChanges'
import { useUIStore } from '@/stores/ui'
import { blankDraft, dbHint, engineOpts, probe, useEnvOpts, type ConnDraft } from './connectionForm'
import './ConnectionsView.scoped.css'

export interface NewModalHandle { open(): void }

export default function ConnectionNewModal({ ref, reload }: { ref: Ref<NewModalHandle>; reload: () => Promise<void> }) {
  const { t } = useI18n()
  const notify = useUIStore((s) => s.notify)
  const notifyError = useUIStore((s) => s.notifyError)
  const { envOpts } = useEnvOpts()

  const [draft, setDraft] = useState<ConnDraft>(() => blankDraft())
  const patch = (p: Partial<ConnDraft>) => setDraft((d) => ({ ...d, ...p }))
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  // 点过「测试保存」才标红缺的必填项:一打开表单就满屏红框,是在责备人还没来得及填的东西。
  // 从前留空点下去什么都不发生(黑盒 2026-10 工单 05),人只会以为按钮坏了。
  const [tried, setTried] = useState(false)
  const missing = { name: !draft.name.trim(), host: !draft.host.trim() }
  const [snap, setSnap] = useState<unknown>(null)

  useImperativeHandle(ref, () => ({
    open() {
      const d = blankDraft(envOpts[0]?.label)
      setDraft(d)
      setSnap(d)
      setTried(false)
      setOpen(true)
    },
  }))
  // 弹窗开着且和打开时不一样 = 有没保存的改动(管理后台空闲锁屏,2026-10-26 回归排查 10)
  useUnsavedFlag(open && !sameForm(draft, snap))

  async function add() {
    setTried(true)
    if (missing.name || missing.host) return
    const env = envOpts.find((o) => o.label === draft.envLabel)?.env || 'prod'
    const body = {
      name: draft.name.trim(), engine: draft.engine,
      host: draft.host.trim(), env: env as any,
      username: draft.username.trim(), password: draft.password, database: draft.database.trim(),
    }
    // M14: 失败以 toast 呈现
    setBusy(true)
    try {
      // "测试连接并保存" (FR-CONN-02): create then test-attach to the gateway.
      const created = await api.createConnection(body)
      // 测试失败**不再静默吞掉**。连接确实建成了(所以不抛错、不回滚),但一台连不上的
      // 实例如果建完只说一句"已保存",人会以为它是好的 —— 而那正是这次报上来的现象。
      const err = await probe(created.id, t('connUnreachable'))
      setDraft(blankDraft(envOpts[0]?.label))
      setOpen(false)
      await reload()
      if (err) notify(t('connSavedUnreachable', { msg: err }), 'error', 8000)
      else notify(t('connSaved'), 'success')
    } catch (e) {
      notifyError(e, t('actionFailed'))
    } finally {
      setBusy(false)
    }
  }

  if (!open) return null
  return createPortal(
    <div className="ce-mask" onClick={(e) => { if (e.target === e.currentTarget) setOpen(false) }}>
      <div className="ce-card">
        <div className="ce-head"><div className="ce-title">{t('formNewConn')}</div><div className="ce-sub">{t('formNewConnSub')}</div></div>
        <div className="ce-body">
          <div className="fgrid">
            <div><div className="fl">{t('fName')}<span className="req">*</span></div><input value={draft.name} onChange={(e) => patch({ name: e.target.value })} placeholder="order-cluster-2" className={clsx(tried && missing.name && 'invalid')} data-testid="new-conn-name" /></div>
            <div><div className="fl">{t('fEngine')}</div><VSelect value={draft.engine} onChange={(v) => patch({ engine: v })} options={engineOpts} /></div>
            <div><div className="fl">{t('fEnv')}</div><VSelect value={draft.envLabel} onChange={(v) => patch({ envLabel: v })} options={envOpts.map((o) => o.label)} /></div>
            <div><div className="fl">{t('fAddr')}<span className="req">*</span></div><input value={draft.host} onChange={(e) => patch({ host: e.target.value })} placeholder="10.20.3.12:3306" className={clsx(tried && missing.host && 'invalid')} data-testid="new-conn-host" /></div>
            <div><div className="fl">{t('fDatabase')}</div><input value={draft.database} onChange={(e) => patch({ database: e.target.value })} placeholder={dbHint(draft.engine, t)} /></div>
            <div><div className="fl">{t('fUser')}</div><input value={draft.username} onChange={(e) => patch({ username: e.target.value })} placeholder="app_ro" /></div>
            <div><div className="fl">{t('fPassword')}</div><input value={draft.password} onChange={(e) => patch({ password: e.target.value })} type="password" placeholder="••••••" /></div>
          </div>
          {tried && (missing.name || missing.host) && <div className="reqmsg" data-testid="new-conn-required">{t('connRequiredMissing')}</div>}
          <div className="credhint">{t('connCredHint')}</div>
        </div>
        <div className="ce-foot">
          <VButton variant="secondary" onClick={() => setOpen(false)}>{t('mCancel')}</VButton>
          <VButton variant="primary" disabled={busy} onClick={add}>{busy ? t('connTestSaving') : t('connTestSave')}</VButton>
        </div>
      </div>
    </div>,
    document.body,
  )
}
