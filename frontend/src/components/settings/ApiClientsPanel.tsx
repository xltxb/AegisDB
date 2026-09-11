import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import clsx from 'clsx'
import { Bot, Check, Copy, KeyRound, Plus, Trash2, TriangleAlert } from 'lucide-react'
import { CardRow } from '@/components/common/Card'
import { Badge } from '@/components/common/Badge'
import { Button } from '@/components/common/Button'
import { Switch } from '@/components/common/Switch'
import { Modal } from '@/components/common/Modal'
import { Loading, ErrorState, Empty } from '@/components/common/States'
import {
  useApiClients, useCreateApiClient, useCreateServiceAccount, useDeleteApiClient,
  useServiceAccounts, useSetApiClientEnabled,
} from '@/hooks/useSettings'
import { useIsAdmin, useRoles } from '@/hooks/usePermissions'
import { usePipelines } from '@/hooks/usePipeline'
import { copyText } from '@/lib/clipboard'
import { confirmAction } from '@/lib/confirm'
import { useUIStore } from '@/stores/ui'
import { invalidIpEntries } from '@/lib/ipAllowlist'

/** 外部系统能申请到的能力。服务端认的就这三个。 */
const SCOPES = ['release:create', 'release:read', 'review:check'] as const

/**
 * 开放接口凭据。
 *
 * 整块界面围着一个事实转:**明文密钥只在创建响应里出现一次**,服务端只留 bcrypt
 * 散列,没有"再取一次"的接口。所以创建之后必须有一屏专门交接它,而不是把它塞进
 * 一条 4 秒后消失的 toast 里。这也是 React 版当初跳过这个功能的原因 —— 现在补上。
 */
export default function ApiClientsPanel() {
  const { t } = useTranslation()
  const isAdmin = useIsAdmin()
  const clients = useApiClients()
  const accounts = useServiceAccounts()
  const toggle = useSetApiClientEnabled()
  const create = useCreateApiClient()
  const del = useDeleteApiClient()

  /** 刚发出去的那一份明文。只在内存里,人点确认才丢掉 —— 之后谁也取不回来。 */
  const [issued, setIssued] = useState<{ name: string; token: string } | null>(null)
  const [formOpen, setFormOpen] = useState(false)
  const [saOpen, setSaOpen] = useState(false)

  return (
    <>
      <CardRow
        title={t('acTitle')}
        hint={t('acSub')}
      >
        {isAdmin && (
          <Button variant="primary" onClick={() => setSaOpen(true)}>
            <Bot size={14} />{t('saNew')}
          </Button>
        )}
        {isAdmin && (
          <Button variant="primary" onClick={() => setFormOpen(true)}>
            <Plus size={14} />{t('acNew')}
          </Button>
        )}
      </CardRow>

      {/* ---- 服务账号:凭据背后的机器主体 ---- */}
      <CardRow title={t('saTitle')} hint={t('saSub')}>
        {accounts.isLoading && <Loading />}
        {accounts.error && <span className="set-hint">{t('saUnavailable')}</span>}
        {accounts.data && !accounts.data.length && <span className="set-hint">{t('saEmpty')}</span>}
      </CardRow>
      {(accounts.data ?? []).map((sa) => (
        <CardRow
          key={sa.id}
          title={<><Bot size={13} /> {sa.name}</>}
          hint={<>
            <span className="mono">{sa.email}</span>
            {sa.roles.length ? ` · ${sa.roles.join(' / ')}` : ''}
            {sa.tags.length ? ` · ${sa.tags.join(',')}` : ''}
          </>}
        >
          <Badge tone={sa.status === 'active' ? 'success' : 'neutral'}>
            {t('saClients', { n: sa.clients })}
          </Badge>
        </CardRow>
      ))}

      {/* ---- 凭据 ---- */}
      {clients.isLoading && <Loading />}
      {clients.error && <ErrorState error={clients.error} retry={() => clients.refetch()} />}
      {clients.data && !clients.data.length && <Empty hint={t('apiCliEmpty')} />}
      {(clients.data ?? []).map((c) => (
        <CardRow
          key={c.id}
          title={c.name}
          hint={<>
            <span className="mono">{c.key}</span>
            {' · '}{t('apiCliActs', { name: c.userName })}
            {' · '}{c.scopes || t('acNoScope')}
            {' · '}{c.allowIps || t('acAnyIP')}
            {' · '}{c.lastUsedAt
              ? t('apiCliLastUsed', { at: c.lastUsedAt.slice(0, 16).replace('T', ' ') })
              : t('apiCliNeverUsed')}
          </>}
        >
          <Badge tone={c.enabled ? 'success' : 'neutral'}>
            {t(c.enabled ? 'enabledTag' : 'disabledTag')}
          </Badge>
          <Switch
            checked={c.enabled}
            disabled={!isAdmin || toggle.isPending}
            onChange={(v) => toggle.mutate({ id: c.id, enabled: v })}
          />
          {isAdmin && (
            <button
              className="row-del"
              title={t('acDelete')}
              onClick={() => { if (confirmAction(t('acDelConfirm', { name: c.name }))) del.mutate(c.id) }}
            >
              <Trash2 size={14} />
            </button>
          )}
        </CardRow>
      ))}

      <ServiceAccountModal open={saOpen} onClose={() => setSaOpen(false)} />
      <NewClientModal
        open={formOpen}
        onClose={() => setFormOpen(false)}
        onIssued={(v) => { setFormOpen(false); setIssued(v) }}
        create={create}
      />
      {issued && <IssuedSecret issued={issued} onDone={() => setIssued(null)} />}
    </>
  )
}

/**
 * 一次性明文的交接屏。
 *
 * 刻意**不**复用 Modal:那个弹窗点遮罩、按 Esc 都会关,而这一屏关掉就等于把密钥
 * 扔了。只有那一个「我已经抄走了」的按钮能让它消失。
 */
function IssuedSecret({
  issued, onDone,
}: { issued: { name: string; token: string }; onDone: () => void }) {
  const { t } = useTranslation()
  const notify = useUIStore((s) => s.notify)
  const [copied, setCopied] = useState(false)

  async function copy() {
    const ok = await copyText(issued.token)
    setCopied(ok)
    if (!ok) notify(t('acCopyFailed'), 'error')
  }

  return (
    <div className="once-overlay">
      <div className="once">
        <div className="once-head">
          <TriangleAlert size={18} />
          <span>{t('acIssued', { name: issued.name })}</span>
        </div>
        <p className="once-warn">{t('acIssuedHint')}</p>
        <code className="once-token">{issued.token}</code>
        <div className="once-foot">
          <Button onClick={copy}>
            {copied ? <Check size={14} /> : <Copy size={14} />}
            {copied ? t('acCopied') : t('acCopy')}
          </Button>
          <Button variant="primary" onClick={onDone}>{t('acIssuedDone')}</Button>
        </div>
      </div>
    </div>
  )
}

/** 新建凭据。主体只给服务账号 —— 绑在人身上的集成会随着人离职一起断。 */
function NewClientModal({
  open, onClose, onIssued, create,
}: {
  open: boolean
  onClose: () => void
  onIssued: (v: { name: string; token: string }) => void
  create: ReturnType<typeof useCreateApiClient>
}) {
  const { t } = useTranslation()
  const notify = useUIStore((s) => s.notify)
  const accounts = useServiceAccounts()
  const pipelines = usePipelines()
  const [name, setName] = useState('')
  const [userId, setUserId] = useState(0)
  const [allowIps, setAllowIps] = useState('')
  const [pipelineId, setPipelineId] = useState(0)
  const [scopes, setScopes] = useState<string[]>([...SCOPES])

  const badIps = invalidIpEntries(allowIps)

  function submit() {
    if (!name.trim() || !userId) { notify(t('acNeedFields'), 'error'); return }
    if (badIps.length) { notify(t('ipInvalidEntries', { list: badIps.join(', ') }), 'error'); return }
    create.mutate(
      { name: name.trim(), userId, allowIps: allowIps.trim(), scopes, pipelineId, enabled: true },
      {
        onSuccess: (r) => {
          setName(''); setUserId(0); setAllowIps(''); setPipelineId(0); setScopes([...SCOPES])
          onIssued({ name: r.client.name, token: r.token })
        },
      },
    )
  }

  return (
    <Modal
      open={open}
      title={t('acNew')}
      sub={t('acNewSub')}
      onClose={onClose}
      footer={<>
        <Button onClick={onClose}>{t('cancel')}</Button>
        <Button variant="primary" disabled={create.isPending} onClick={submit}>{t('acCreate')}</Button>
      </>}
    >
      <div className="fld">
        <label>{t('acName')}</label>
        <input value={name} placeholder={t('acNamePh')} onChange={(e) => setName(e.target.value)} />
      </div>
      <div className="fld">
        <label>{t('acAccount')}</label>
        <select value={userId} onChange={(e) => setUserId(Number(e.target.value))}>
          <option value={0}>{t('acAccountPick')}</option>
          {(accounts.data ?? []).map((a) => (
            <option key={a.id} value={a.id}>{a.name} · {a.email}</option>
          ))}
        </select>
        <div className="set-hint">{t('acAccountHint')}</div>
      </div>
      <div className="fld">
        <label>{t('acScopes')}</label>
        <div className="set-chips">
          {SCOPES.map((s) => (
            <button
              key={s}
              type="button"
              className={clsx('set-chip', scopes.includes(s) && 'on')}
              onClick={() => setScopes((p) => p.includes(s) ? p.filter((x) => x !== s) : [...p, s])}
            >
              {s}
            </button>
          ))}
        </div>
      </div>
      <div className="fld">
        <label>{t('acPipeline')}</label>
        <select value={pipelineId} onChange={(e) => setPipelineId(Number(e.target.value))}>
          <option value={0}>{t('acPipeDefault')}</option>
          {(pipelines.data ?? []).filter((p) => p.enabled).map((p) => (
            <option key={p.id} value={p.id}>{p.name}</option>
          ))}
        </select>
        <div className="set-hint">{t('acPipelineHint')}</div>
      </div>
      <div className="fld">
        <label>{t('acAllowIPs')}</label>
        <input
          className={clsx(badIps.length && 'is-bad')}
          value={allowIps}
          placeholder="10.0.0.0/8, 203.0.113.7"
          onChange={(e) => setAllowIps(e.target.value)}
        />
        <div className={clsx('set-hint', badIps.length && 'bad')}>
          {badIps.length ? t('ipInvalidEntries', { list: badIps.join(', ') }) : t('acAllowIPsHint')}
        </div>
      </div>
    </Modal>
  )
}

/** 建服务账号。角色清单来自 `GET /roles`(挂着 perms 闸),拿不到就如实说。 */
function ServiceAccountModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t } = useTranslation()
  const notify = useUIStore((s) => s.notify)
  const roles = useRoles()
  const createSA = useCreateServiceAccount()
  const [name, setName] = useState('')
  const [tags, setTags] = useState('')
  const [roleIds, setRoleIds] = useState<number[]>([])

  function submit() {
    if (!name.trim() || !roleIds.length) { notify(t('saNeedFields'), 'error'); return }
    createSA.mutate(
      {
        name: name.trim(),
        roleIds,
        tags: tags.split(',').map((x) => x.trim()).filter(Boolean),
      },
      { onSuccess: () => { setName(''); setTags(''); setRoleIds([]); onClose() } },
    )
  }

  return (
    <Modal
      open={open}
      title={t('saNew')}
      sub={t('saFormHint')}
      onClose={onClose}
      footer={<>
        <Button onClick={onClose}>{t('cancel')}</Button>
        <Button variant="primary" disabled={createSA.isPending} onClick={submit}>{t('saCreate')}</Button>
      </>}
    >
      <div className="fld">
        <label>{t('saName')}</label>
        <input value={name} placeholder={t('saNamePh')} onChange={(e) => setName(e.target.value)} />
      </div>
      <div className="fld">
        <label>{t('saRoles')}</label>
        {roles.error && <div className="set-hint">{t('saRolesUnavailable')}</div>}
        <div className="set-chips wrap">
          {(roles.data ?? []).map((r) => (
            <button
              key={r.id}
              type="button"
              className={clsx('set-chip', roleIds.includes(r.id) && 'on')}
              onClick={() => setRoleIds((p) => p.includes(r.id) ? p.filter((x) => x !== r.id) : [...p, r.id])}
            >
              {roleIds.includes(r.id) && <Check size={11} />}{r.name}
            </button>
          ))}
        </div>
      </div>
      <div className="fld">
        <label>{t('saTags')}</label>
        <input value={tags} placeholder={t('saTagsPh')} onChange={(e) => setTags(e.target.value)} />
      </div>
      <div className="notice">
        <KeyRound size={15} />{t('saThenIssue')}
      </div>
    </Modal>
  )
}
