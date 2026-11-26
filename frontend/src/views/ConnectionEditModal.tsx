// Edit an existing instance in a modal. A blank password keeps the stored one.
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
import type { Connection } from '@/types'
import { blankDraft, dbHint, engineOpts, probe, useEnvOpts, type ConnDraft } from './connectionForm'
import './ConnectionsView.scoped.css'

export interface EditModalHandle { open(c: Connection): void }

interface ReplicaDraft { host: string; username: string; password: string }

export const replicaAddr = (c: Connection) => (c.replicaPort ? `${c.replicaHost}:${c.replicaPort}` : c.replicaHost || '')
const sameAddr = (a: string, b: string) => a.trim().toLowerCase() === b.trim().toLowerCase()

export default function ConnectionEditModal({ ref, reload }: { ref: Ref<EditModalHandle>; reload: () => Promise<void> }) {
  const { t } = useI18n()
  const notify = useUIStore((s) => s.notify)
  const notifyError = useUIStore((s) => s.notifyError)
  const { envOpts, envOptsFor } = useEnvOpts()

  const [modal, setModal] = useState<{ open: boolean; id: number }>({ open: false, id: 0 })
  const close = () => setModal((m) => ({ ...m, open: false }))
  const [draft, setDraft] = useState<ConnDraft>(() => blankDraft())
  const patch = (p: Partial<ConnDraft>) => setDraft((d) => ({ ...d, ...p }))
  // 从库(只读副本):业务方的数据查询都由它提供。地址留空 = 不用从库;账号留空沿用主库账号密码。
  const [replica, setReplica] = useState<ReplicaDraft>({ host: '', username: '', password: '' })
  const patchReplica = (p: Partial<ReplicaDraft>) => setReplica((d) => ({ ...d, ...p }))
  const [busy, setBusy] = useState(false)
  const [snap, setSnap] = useState<unknown>(null)
  // 改了地址时口令要当场重新输入(服务端规则见 service.UpdateConnection / applyReplicaConfig,2026-10
  // 安全审计 M4:不然存着的口令会被发往新地址)。这里提前算出来,标在对应的密码框下面 —— 从前要点了
  // 保存才看到一句报错。只提示、不拦:前端不知道服务端存没存口令(口令从不回传),按"原来填了账号"
  // 近似判断,拦了就可能挡住一次本该成功的保存;真要拒绝的由服务端说。
  const [orig, setOrig] = useState({ host: '', replicaHost: '', username: '', replicaUsername: '' })

  useImperativeHandle(ref, () => ({
    open(c) {
      const d: ConnDraft = {
        name: c.name,
        host: `${c.host}:${c.port}`,
        engine: c.engine,
        // Never fall back to the first option — see envOptsFor.
        envLabel: envOptsFor(c.env).find((o) => o.env === c.env)!.label,
        username: c.username || '',
        password: '',
        database: c.database || '',
      }
      const r: ReplicaDraft = { host: replicaAddr(c), username: c.replicaUsername || '', password: '' }
      setDraft(d)
      setReplica(r)
      setOrig({ host: d.host, replicaHost: r.host, username: d.username, replicaUsername: r.username })
      setSnap([d, r])
      setModal({ open: true, id: c.id })
    },
  }))

  const mainNeedsPw = modal.open && !draft.password && !!orig.username && !sameAddr(draft.host, orig.host)
  const replicaMoved = !!replica.host.trim() && !sameAddr(replica.host, orig.replicaHost)
  const replicaNeedsMainPw = modal.open && replicaMoved && !replica.username.trim()
    && !draft.password && !!orig.username && !mainNeedsPw
  const replicaNeedsOwnPw = modal.open && replicaMoved && !!replica.username.trim()
    && !replica.password && !!orig.replicaUsername
  // 弹窗开着且和打开时不一样 = 有没保存的改动(管理后台空闲锁屏,2026-10-26 回归排查 10)
  useUnsavedFlag(modal.open && !sameForm([draft, replica], snap))

  async function save() {
    if (!draft.name.trim() || !draft.host.trim()) return
    // A label with no match is the instance's own dangling environment, which
    // envOptsFor added verbatim — so the label IS the code. Keeping it means an
    // untouched save leaves the instance where it was; the server refuses it if
    // that environment is really gone, which is the correct, visible outcome.
    const env = envOpts.find((o) => o.label === draft.envLabel)?.env ?? draft.envLabel
    setBusy(true)
    try {
      // Update, then test-attach to the gateway (best-effort).
      const updated = await api.updateConnection(modal.id, {
        name: draft.name.trim(), engine: draft.engine,
        host: draft.host.trim(), env: env as any,
        username: draft.username.trim(), password: draft.password, database: draft.database.trim(),
        replicaHost: replica.host.trim(), replicaUsername: replica.username.trim(),
        replicaPassword: replica.password,
      })
      // 同新建:改完连不上要当场说,不能只说"已保存"。
      const err = await probe(updated.id, t('connUnreachable'))
      if (err) notify(t('connSavedUnreachable', { msg: err }), 'error', 8000)
      setModal({ open: false, id: 0 })
      await reload()
    } catch (e) {
      notifyError(e, t('actionFailed'))
    } finally {
      setBusy(false)
    }
  }

  if (!modal.open) return null
  return createPortal(
    <div className="ce-mask" onClick={(e) => { if (e.target === e.currentTarget) close() }}>
      <div className="ce-card">
        <div className="ce-head"><div className="ce-title">{t('formEditConn')}</div><div className="ce-sub">{t('formEditConnSub')}</div></div>
        <div className="ce-body">
          <div className="fgrid">
            <div><div className="fl">{t('fName')}</div><input value={draft.name} onChange={(e) => patch({ name: e.target.value })} placeholder="order-cluster-2" /></div>
            <div><div className="fl">{t('fEngine')}</div><VSelect value={draft.engine} onChange={(v) => patch({ engine: v })} options={engineOpts} /></div>
            <div><div className="fl">{t('fEnv')}</div><VSelect value={draft.envLabel} onChange={(v) => patch({ envLabel: v })} options={envOpts.map((o) => o.label)} /></div>
            <div><div className="fl">{t('fAddr')}</div><input value={draft.host} onChange={(e) => patch({ host: e.target.value })} placeholder="10.20.3.12:3306" /></div>
            <div><div className="fl">{t('fDatabase')}</div><input value={draft.database} onChange={(e) => patch({ database: e.target.value })} placeholder={dbHint(draft.engine, t)} /></div>
            <div><div className="fl">{t('fUser')}</div><input value={draft.username} onChange={(e) => patch({ username: e.target.value })} placeholder="app_ro" /></div>
            <div><div className="fl">{t('fPassword')}</div><input value={draft.password} onChange={(e) => patch({ password: e.target.value })} type="password" placeholder={t('fPasswordKeep')} className={clsx((mainNeedsPw || replicaNeedsMainPw) && 'invalid')} data-testid="conn-edit-password" /></div>
          </div>
          {mainNeedsPw && <div className="reqmsg" data-testid="conn-need-main-pw">{t('connNeedPwAddr')}</div>}
          {replicaNeedsMainPw && <div className="reqmsg" data-testid="conn-need-main-pw-replica">{t('connNeedPwReplicaMain')}</div>}
          <div className="credhint">{t('connCredHint')}</div>
          <div className="replica" data-testid="conn-replica">
            <div className="rp-title">{t('connReplica')}</div>
            <div className="rp-sub">{t('connReplicaSub')}</div>
            <div className="fgrid">
              <div><div className="fl">{t('connReplicaAddr')}</div><input value={replica.host} onChange={(e) => patchReplica({ host: e.target.value })} placeholder={t('connReplicaAddrPh')} data-testid="conn-replica-host" /></div>
              <div><div className="fl">{t('fUser')}</div><input value={replica.username} onChange={(e) => patchReplica({ username: e.target.value })} placeholder={t('connReplicaUserPh')} /></div>
              <div><div className="fl">{t('fPassword')}</div><input value={replica.password} onChange={(e) => patchReplica({ password: e.target.value })} type="password" disabled={!replica.username.trim()} placeholder={replica.username.trim() ? t('fPasswordKeep') : t('connReplicaUserPh')} className={clsx(replicaNeedsOwnPw && 'invalid')} /></div>
            </div>
            {replicaNeedsOwnPw && <div className="reqmsg" data-testid="conn-need-replica-pw">{t('connNeedPwReplicaOwn')}</div>}
          </div>
        </div>
        <div className="ce-foot">
          <VButton variant="secondary" onClick={close}>{t('mCancel')}</VButton>
          <VButton variant="primary" disabled={busy} onClick={save}>{busy ? t('connTestSaving') : t('connTestSave')}</VButton>
        </div>
      </div>
    </div>,
    document.body,
  )
}
