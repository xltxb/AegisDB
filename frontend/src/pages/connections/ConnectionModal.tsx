import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Database } from 'lucide-react'
import { ENGINES, engineDisplay } from '@/lib/engines'
import type { ConnectionDraft } from '@/hooks/useConnections'
import { Button } from '@/components/common/Button'
import { Modal } from '@/components/common/Modal'
import type { Connection, Environment } from '@/types'

export const POLICIES = ['strict', 'approve-1', 'audit-only']

export const blankDraft = (env: string): ConnectionDraft => ({
  name: '', engine: ENGINES[0].id, host: '', env, policy: 'strict',
  username: '', password: '', database: '', tags: '',
})

export function draftOf(c: Connection): ConnectionDraft {
  return {
    id: c.id,
    name: c.name,
    engine: c.engine,
    host: `${c.host}:${c.port}`,
    env: c.env,
    policy: c.policy,
    username: c.username || '',
    // 编辑一律从空口令开始 —— 服务端把空当成"保持原样",而把打码后的假值填进框里
    // 只会诱使人原样提交,把一串星号写成新口令。
    password: '',
    database: c.database || '',
    tags: c.tags || '',
  }
}

/**
 * 环境下拉的选项。
 *
 * 两台环境重名时把码缀在标签上:下拉绑的是码,但人按的是标签,两个一模一样的
 * 标签里有一个是永远选不中的。
 *
 * `current` 是这台实例现在挂的环境。它已经被删掉时也要出现在列表里 —— 否则下拉
 * 会退回第一项,而"打开看一眼再保存"就会把一台实例悄悄搬进生产。
 */
function envOptions(envs: Environment[], current: string) {
  const dup = new Map<string, number>()
  for (const e of envs) dup.set(e.displayName, (dup.get(e.displayName) ?? 0) + 1)
  const opts = envs.map((e) => ({
    code: e.code,
    label: (dup.get(e.displayName) ?? 0) > 1 ? `${e.displayName} (${e.code})` : e.displayName,
  }))
  if (current && !opts.some((o) => o.code === current)) opts.push({ code: current, label: current })
  return opts
}

/** 引擎下拉。存的是目录里的 id;历史上存了展示名的实例照原样留一项,不被悄悄改写。 */
function engineOptions(current: string) {
  const opts = ENGINES.map((e) => ({ id: e.id, label: e.label }))
  if (current && !opts.some((o) => o.id === current)) opts.push({ id: current, label: engineDisplay(current) })
  return opts
}

export function ConnectionModal({
  draft, envs, busy, onClose, onSubmit,
}: {
  draft: ConnectionDraft
  envs: Environment[]
  busy: boolean
  onClose: () => void
  onSubmit: (d: ConnectionDraft) => void
}) {
  const { t } = useTranslation()
  const [f, setF] = useState<ConnectionDraft>(draft)
  const set = (patch: Partial<ConnectionDraft>) => setF((p) => ({ ...p, ...patch }))
  const editing = !!f.id

  return (
    <Modal
      open
      title={t(editing ? 'connEditTitle' : 'connNew')}
      sub={t('connModalSub')}
      width={640}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>{t('cancel')}</Button>
          <Button
            variant="primary"
            disabled={busy || !f.name.trim() || !f.host.trim() || !f.env}
            onClick={() => onSubmit(f)}
          >
            {busy ? t('connSaving') : t('save')}
          </Button>
        </>
      }
    >
      <div className="fld-2">
        <div className="fld">
          <label>{t('connFName')}</label>
          <input value={f.name} onChange={(e) => set({ name: e.target.value })} />
        </div>
        <div className="fld">
          <label>{t('connFEngine')}</label>
          <select value={f.engine} onChange={(e) => set({ engine: e.target.value })}>
            {engineOptions(draft.engine).map((o) => (
              <option key={o.id} value={o.id}>{o.label}</option>
            ))}
          </select>
        </div>
      </div>

      <div className="fld-2">
        <div className="fld">
          <label>{t('connFEnv')}</label>
          <select value={f.env} onChange={(e) => set({ env: e.target.value })}>
            {envOptions(envs, draft.env).map((o) => (
              <option key={o.code} value={o.code}>{o.label}</option>
            ))}
          </select>
        </div>
        <div className="fld">
          {/* 端口不单列一个框:服务端收的就是 host:port 这一串,拆开只会多一处能填错。 */}
          <label>{t('connFHost')}</label>
          <input
            value={f.host}
            placeholder="10.20.30.40:3306"
            onChange={(e) => set({ host: e.target.value })}
          />
        </div>
      </div>

      <div className="fld-2">
        <div className="fld">
          <label>{t('connFPolicy')}</label>
          <select value={f.policy} onChange={(e) => set({ policy: e.target.value })}>
            {POLICIES.map((p) => <option key={p} value={p}>{p}</option>)}
          </select>
        </div>
        <div className="fld">
          {/* Oracle 认的是服务名(或 sid/ 前缀的 SID),不是普通 schema 名。 */}
          <label>{t('connFDatabase')}</label>
          <input
            value={f.database}
            placeholder={/oracle/i.test(f.engine) ? t('connDbHintOracle') : 'orders_db'}
            onChange={(e) => set({ database: e.target.value })}
          />
        </div>
      </div>

      <div className="fld-2">
        <div className="fld">
          <label>{t('connFUser')}</label>
          <input
            value={f.username}
            autoComplete="off"
            onChange={(e) => set({ username: e.target.value })}
          />
        </div>
        <div className="fld">
          <label>{t('connFPassword')}</label>
          <input
            type="password"
            value={f.password}
            autoComplete="new-password"
            placeholder={editing ? t('connPwdKeep') : ''}
            onChange={(e) => set({ password: e.target.value })}
          />
        </div>
      </div>

      <div className="fld">
        {/* 标签决定谁碰得到这台实例(判定层会读),所以它和"归哪个项目"不是一回事。 */}
        <label>{t('connFTags')}</label>
        <input
          value={f.tags}
          placeholder="core,payment"
          onChange={(e) => set({ tags: e.target.value })}
        />
      </div>

      <div className="notice">
        <Database size={15} />
        {t('connSaveProbes')}
      </div>
    </Modal>
  )
}
