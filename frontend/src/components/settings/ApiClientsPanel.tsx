// 开放接口凭据 —— 外部系统(DevOps 平台 / CI)提交 SQL 升级单用的 Key/Secret。
//
// The panel is built around one fact: the secret exists exactly once, in the
// create response. So the flow is create → copy → gone, and everything else on
// this screen is about the credential's identity (which service account it acts
// as, what it may do, where it may call from) rather than about the secret.
import { useState, type HTMLAttributes } from 'react'
import clsx from 'clsx'
import { KeyRound, Plus, Copy, Check, Trash2, X, TriangleAlert, Bot, Pencil, ShieldCheck } from 'lucide-react'
import VButton from '@/components/common/VButton'
import VSwitch from '@/components/common/VSwitch'
import VSelect from '@/components/common/VSelect'
import api from '@/api'
import { envError } from '@/api/http'
import { confirmAction } from '@/lib/confirm'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import { useI18n } from '@/hooks/useI18n'
import { useMountEffect } from '@/hooks/effects'
import { useStateRef } from '@/hooks/useStateRef'
import type { APIClient, Pipeline, RoleBrief, ServiceAccount, UserView } from '@/types'
import './ApiClientsPanel.scoped.css'

// grant:* 给外部审批系统下发查询授权用。它们不在默认勾选里(见 nf):一把 CI 凭据
// 不该顺带拿到给人开生产数据查询权的能力。
// inspection:write 只给 AI 巡检系统那一把凭据:它只能上传巡检报告。同样不在默认勾选里。
// release:approve 给外部审批系统:替审批链上的人通过 / 驳回发布单,替确认人放行人工确认。审批是职责分离的那一半,不在默认勾选里。
const SCOPES = ['release:create', 'release:read', 'review:check', 'grant:read', 'grant:write', 'inspection:write', 'release:approve']
const DEFAULT_SCOPES = ['release:create', 'release:read', 'review:check']
const isGrantScope = (s: string) => s.startsWith('grant:')

const emptyForm = () => ({ name: '', userId: 0, allowIps: '', scopes: [...DEFAULT_SCOPES], pipelineId: 0 })
const emptySA = () => ({ name: '', roleIds: [] as number[], tags: '' })

function flip<T>(list: T[], v: T): T[] { return list.includes(v) ? list.filter((x) => x !== v) : [...list, v] }
const scopesOf = (c: APIClient) => (c.scopes || '').split(',').map((s) => s.trim()).filter(Boolean)
function fmt(s: string | null) { return s ? new Date(s).toLocaleString('en-GB') : '—' }

export default function ApiClientsPanel({ className, ...rest }: HTMLAttributes<HTMLDivElement>) {
  const { t } = useI18n()
  const { notify, notifyError } = useUIStore.getState()
  const isAdmin = useAuthStore((s) => s.me?.roleCode === 'admin' || (s.me?.roleCodes || []).includes('admin'))

  const [clients, setClients] = useState<APIClient[]>([])
  const [users, setUsers, usersRef] = useStateRef<UserView[]>([])
  // 服务账号 —— 凭据背后的机器主体。凭据应绑定它,而不是某个人的账号:人会离职、
  // 会换角色,绑在人身上的集成会跟着一起断。
  const [svcAccounts, setSvcAccounts] = useState<ServiceAccount[]>([])
  const [roles, setRoles] = useState<RoleBrief[]>([])
  const [saOpen, setSaOpen] = useState(false)
  const [sf, setSf] = useState(emptySA)
  const [formOpen, setFormOpen] = useState(false)
  const [nf, setNf] = useState(emptyForm)
  const patchNf = (p: Partial<typeof nf>) => setNf((v) => ({ ...v, ...p }))
  // 发布流程绑定在凭据上(网关侧策略),外部请求不可指定 —— 0 = 走目标分层的默认流程。
  const [pipelines, setPipelines] = useState<Pipeline[]>([])
  // The one plaintext copy of a freshly-issued credential. Held in memory only,
  // shown until dismissed, never fetched again — there is nothing to fetch.
  const [issued, setIssued] = useState<{ name: string; token: string } | null>(null)
  const [copied, setCopied] = useState(false)

  // 凭据只能绑服务账号(后端新建/改绑都会拒绝人类账号;存量绑真人的凭据不追溯)。
  const principalLabel = (u: UserView) =>
    u.kind === 'service' ? `🤖 ${u.name} · ${t('saMark')}` : `${u.name} · ${u.email}`
  const principals = users.filter((u) => u.kind === 'service' && u.status === 'active')
  // 服务账号不能带平台管理员角色(后端拒绝),选择器里直接不给。
  const saRoles = roles.filter((r) => r.code !== 'admin')
  const userLabels = principals.map(principalLabel)
  const pipeLabel = pipelines.find((x) => x.id === nf.pipelineId)?.name ?? t('acPipeDefault')
  const setPipeLabel = (l: string) => patchNf({ pipelineId: pipelines.find((x) => x.name === l)?.id ?? 0 })
  const pipeOptions = [t('acPipeDefault'), ...pipelines.map((p) => p.name)]
  const pipeNameOf = (id: number) => pipelines.find((p) => p.id === id)?.name || ''
  const boundUser = users.find((x) => x.id === nf.userId)
  const userLabel = boundUser ? principalLabel(boundUser) : ''
  const setUserLabel = (l: string) => patchNf({ userId: users.find((x) => principalLabel(x) === l)?.id ?? 0 })

  async function load() {
    try { setClients((await api.apiClients()) ?? []) } catch (e) { notifyError(e, t('loadFailed')) }
    try { setUsers((await api.users()) ?? []) } catch { /* the picker degrades to empty */ }
    try { setSvcAccounts((await api.serviceAccounts()) ?? []) } catch { /* section degrades to empty */ }
    try { setRoles((await api.roles()) ?? []) } catch { /* role picker degrades */ }
    try { setPipelines((await api.pipelines()).filter((p) => p.enabled)) } catch { /* picker degrades */ }
  }

  async function createSA() {
    if (!sf.name.trim() || !sf.roleIds.length) { notify(t('saNeedFields'), 'error'); return }
    try {
      const env = await api.createServiceAccount({
        name: sf.name.trim(), roleIds: sf.roleIds,
        tags: sf.tags.split(',').map((x) => x.trim()).filter(Boolean),
      })
      if (env.code !== 0) { notifyError(envError(env), t('actionFailed')); return }
      setSaOpen(false)
      setSf(emptySA())
      await load()
      // 建完主体顺手打开发凭据的表单 —— 这两步几乎总是连着做的。
      const created = usersRef.current.find((u) => u.kind === 'service' && env.data && u.id === env.data.id)
      openForm()
      if (created) patchNf({ userId: created.id })
    } catch (e) { notifyError(e, t('actionFailed')) }
  }
  useMountEffect(() => { void load() })

  function openForm() {
    setNf(emptyForm())
    setIssued(null)
    setFormOpen(true)
  }

  async function create() {
    if (!nf.name.trim() || !nf.userId) { notify(t('acNeedFields'), 'error'); return }
    try {
      const env = await api.createApiClient({
        name: nf.name.trim(), userId: nf.userId,
        allowIps: nf.allowIps.trim(), scopes: nf.scopes, pipelineId: nf.pipelineId, enabled: true,
      })
      if (env.code !== 0) { notifyError(envError(env), t('actionFailed')); return }
      setIssued({ name: env.data.client.name, token: env.data.token })
      setFormOpen(false)
      setCopied(false)
      await load()
    } catch (e) { notifyError(e, t('actionFailed')) }
  }

  async function copyToken() {
    if (!issued) return
    try {
      await navigator.clipboard.writeText(issued.token)
      setCopied(true)
    } catch { notify(t('acCopyFailed'), 'error') }
  }

  async function toggle(c: APIClient) {
    if (!isAdmin) return
    try {
      const env = await api.updateApiClient(c.id, { enabled: !c.enabled })
      if (env.code !== 0) { notifyError(envError(env), t('actionFailed')); return }
      setClients((list) => list.map((x) => (x.id === c.id ? { ...x, enabled: !c.enabled } : x)))
    } catch (e) { notifyError(e, t('actionFailed')) }
  }

  async function remove(c: APIClient) {
    if (!isAdmin) return
    if (!(await confirmAction(t('acDelConfirm', { name: c.name })))) return
    try {
      const env = await api.deleteApiClient(c.id)
      if (env.code !== 0) { notifyError(envError(env), t('actionFailed')); return }
      setClients((list) => list.filter((x) => x.id !== c.id))
    } catch (e) { notifyError(e, t('actionFailed')) }
  }

  // 已有凭据改 IP 白名单。只发 {allowIps}(后端局部更新,其余字段不动),校验/去重在后端,
  // 非法项原样把后端的报错给出来。清空 = 放开所有来源,保存前必须再确认一次。
  const [ipEdit, setIpEdit] = useState<{ client: APIClient; text: string } | null>(null)
  const [ipSaving, setIpSaving, ipSavingRef] = useStateRef(false)

  function openIpEdit(c: APIClient) {
    if (!isAdmin) return
    setIpEdit({ client: c, text: (c.allowIps || '').split(',').filter(Boolean).join('\n') })
  }

  async function saveIpEdit() {
    const ed = ipEdit
    if (!ed || ipSavingRef.current) return
    const next = ed.text.trim()
    if (!next && !ed.client.allowIps) { setIpEdit(null); return } // 本来就不限来源,没有可改的
    if (!next && !(await confirmAction(t('acIpClearConfirm', { name: ed.client.name }), { danger: true, okText: t('acIpClearOk') }))) return
    setIpSaving(true)
    try {
      const env = await api.updateApiClient(ed.client.id, { allowIps: next })
      if (env.code !== 0) { notifyError(envError(env), t('saveFailed')); return }
      setIpEdit(null)
      notify(t('acIpSaved'), 'success')
      await load()
    } catch (e) { notifyError(e, t('saveFailed')) } finally { setIpSaving(false) }
  }

  // 已有凭据改权限范围。只发 {scopes},校验在后端(认不出的范围整单拒绝)。
  // 加的是默认集合之外的范围(查询授权、巡检、外部审批)时保存前再确认一次 —— 那几项
  // 本该只发给特定系统,顺手勾上就等于给一个系统开了它本不该有的门。
  const [scopeEdit, setScopeEdit] = useState<{ client: APIClient; scopes: string[] } | null>(null)
  const [scopeSaving, setScopeSaving, scopeSavingRef] = useStateRef(false)
  // 同一把凭据既能提单又能审批:网关挡得住"发起人批自己",挡不住系统以链上某个人的名义批自己提的单。
  const scopeSodRisk = !!scopeEdit && scopeEdit.scopes.includes('release:create') && scopeEdit.scopes.includes('release:approve')

  function openScopeEdit(c: APIClient) {
    if (!isAdmin) return
    setScopeEdit({ client: c, scopes: scopesOf(c) })
  }

  async function saveScopeEdit() {
    const ed = scopeEdit
    if (!ed || scopeSavingRef.current || !ed.scopes.length) return
    const before = scopesOf(ed.client)
    const next = SCOPES.filter((s) => ed.scopes.includes(s)) // 按固定顺序发,列表里读起来稳定
    if (next.length === before.length && next.every((s) => before.includes(s))) { setScopeEdit(null); return }
    const added = next.filter((s) => !before.includes(s) && !DEFAULT_SCOPES.includes(s))
    if (added.length && !(await confirmAction(t('acScopeAddConfirm', { name: ed.client.name, scopes: added.join(', ') }), { okText: t('acScopeAddOk') }))) return
    setScopeSaving(true)
    try {
      const env = await api.updateApiClient(ed.client.id, { scopes: next })
      if (env.code !== 0) { notifyError(envError(env), t('saveFailed')); return }
      setScopeEdit(null)
      notify(t('acScopeSaved'), 'success')
      await load()
    } catch (e) { notifyError(e, t('saveFailed')) } finally { setScopeSaving(false) }
  }

  // A copy-paste starting point. The base URL is this console's own origin, which
  // is the one an integrator is looking at right now.
  const sample = `curl -X POST ${window.location.origin}/api/v1/open/releases \\
  -H "Authorization: Bearer <key>.<secret>" \\
  -H "Content-Type: application/json" \\
  -d '{"title":"订单表加字段","externalRef":"CHG-1001","instance":"order-cluster","database":"orders","pipeline":"标准发布流程","sql":"ALTER TABLE ...;","reason":"需求 #123"}'`

  const CopyIcon = copied ? Check : Copy

  return (
    <div {...rest} className={clsx('ac', className)}>
      <div className="achead">
        <div className="acic"><KeyRound size={18} color="var(--accent-text)" /></div>
        <div className="grow">
          <div className="act">{t('acTitle')}</div>
          <div className="acs">{t('acSub')}</div>
        </div>
        {isAdmin && <VButton variant="primary" height="34px" onClick={openForm}><Plus size={14} />{t('acNew')}</VButton>}
      </div>

      {/* one-time secret */}
      {issued && (
        <div className="issued">
          <div className="ihead">
            <TriangleAlert size={15} />
            <span className="it">{t('acIssued', { name: issued.name })}</span>
            <X size={15} className="ix" onClick={() => setIssued(null)} />
          </div>
          <div className="ibody">
            <code className="tok">{issued.token}</code>
            <VButton height="32px" onClick={copyToken}>
              <CopyIcon size={13} />{copied ? t('acCopied') : t('acCopy')}
            </VButton>
          </div>
          <div className="ihint">{t('acIssuedHint')}</div>
        </div>
      )}

      {/* 服务账号:凭据背后的机器主体 */}
      <div className="sablk">
        <div className="sahead">
          <Bot size={14} /><span className="sat">{t('saTitle')}</span>
          <span className="sasub">{t('saSub')}</span>
          {isAdmin && <VButton height="28px" onClick={() => setSaOpen((o) => !o)}><Plus size={12} />{t('saNew')}</VButton>}
        </div>
        {saOpen && (
          <div className="saform">
            <input value={sf.name} onChange={(e) => setSf((v) => ({ ...v, name: e.target.value }))} className="in" placeholder={t('saNamePh')} />
            <div className="scopes">
              {saRoles.map((ro) => (
                <span key={ro.id} className={clsx('sc', sf.roleIds.includes(ro.id) && 'on')} onClick={() => setSf((v) => ({ ...v, roleIds: flip(v.roleIds, ro.id) }))}>
                  {sf.roleIds.includes(ro.id) && <Check size={11} />}{ro.name}
                </span>
              ))}
            </div>
            <input value={sf.tags} onChange={(e) => setSf((v) => ({ ...v, tags: e.target.value }))} className="in" placeholder={t('saTagsPh')} />
            <div className="sarow">
              <span className="hint">{t('saFormHint')}</span>
              <VButton variant="primary" height="30px" onClick={createSA}>{t('saCreate')}</VButton>
            </div>
          </div>
        )}
        {svcAccounts.map((sa) => (
          <div key={sa.id} className={clsx('sarowi', sa.status !== 'active' && 'off')}>
            <Bot size={13} className="saic" />
            <span className="san">{sa.name}</span>
            <code className="ikey">{sa.email}</code>
            <span className="sam">{(sa.roles ?? []).join(' / ')}{!!sa.tags?.length && ` · ${sa.tags.join(',')}`}</span>
            <span className="sam right">{t('saClients', { n: sa.clients })}</span>
          </div>
        ))}
        {!svcAccounts.length && <div className="saempty">{t('saEmpty')}</div>}
      </div>

      <div className="list">
        {clients.map((c) => (
          <div key={c.id} className={clsx('item', !c.enabled && 'off')}>
            <div className="grow">
              <div className="itop">
                <span className="iname">{c.name}</span>
                <code className="ikey">{c.key}</code>
              </div>
              <div className="imeta">
                <span>{t('acAccount')}: {c.userName}</span>
                <span className="sep">·</span>
                <span>{c.scopes}</span>
                <span className="sep">·</span>
                <span>{c.pipelineId ? pipeNameOf(c.pipelineId) : t('acPipeDefault')}</span>
                <span className="sep">·</span>
                <span data-testid="ac-ips" className={clsx(!c.allowIps && 'anyip')}>{c.allowIps ? c.allowIps : t('acAnyIP')}</span>
                <span className="sep">·</span>
                <span>{t('acLastUsed')}: {fmt(c.lastUsedAt)}</span>
              </div>
            </div>
            {isAdmin && <span className="del edit" data-testid="ac-scope-edit" title={t('acScopeEdit')} onClick={() => openScopeEdit(c)}><ShieldCheck size={13} /></span>}
            {isAdmin && <span className="del edit" data-testid="ac-ip-edit" title={t('acIpEdit')} onClick={() => openIpEdit(c)}><Pencil size={13} /></span>}
            <VSwitch value={c.enabled} disabled={!isAdmin} onChange={() => toggle(c)} />
            {isAdmin && <span className="del" onClick={() => remove(c)}><Trash2 size={13} /></span>}
          </div>
        ))}
        {!clients.length && <div className="empty">{t('acEmpty')}</div>}
      </div>

      <div className="sample">
        <div className="fl">{t('acSample')}</div>
        <pre className="code">{sample}</pre>
        <div className="ihint">{t('acSampleHint')}</div>
      </div>

      {/* 编辑 IP 白名单 */}
      {ipEdit && (
        <div className="overlay" data-testid="ac-ip-modal">
          <div className="mask" onClick={() => setIpEdit(null)} />
          <div className="modal">
            <div className="mhead">
              <div className="mic"><KeyRound size={17} color="var(--accent-text)" /></div>
              <div><div className="mt">{t('acIpEdit')}</div><div className="ms">{t('acIpEditSub', { name: ipEdit.client.name })}</div></div>
              <X size={17} className="mx" onClick={() => setIpEdit(null)} />
            </div>
            <div className="mbody">
              <div>
                <div className="fl">{t('acAllowIPs')}</div>
                <textarea
                  value={ipEdit.text} onChange={(e) => { const text = e.target.value; setIpEdit((v) => (v ? { ...v, text } : v)) }}
                  className="in ta" data-testid="ac-ip-input" rows={5} placeholder={'10.0.0.0/8\n203.0.113.7'}
                />
                <div className="hint">{t('acIpEditHint')}</div>
                {!ipEdit.text.trim() && !!ipEdit.client.allowIps && <div className="hint warn" data-testid="ac-ip-clear-warn">{t('acIpClearWarn')}</div>}
              </div>
            </div>
            <div className="mfoot">
              <VButton onClick={() => setIpEdit(null)}>{t('btnCancel')}</VButton>
              <VButton variant="primary" data-testid="ac-ip-save" disabled={ipSaving} onClick={saveIpEdit}>{t('btnSave')}</VButton>
            </div>
          </div>
        </div>
      )}

      {/* 编辑权限范围 */}
      {scopeEdit && (
        <div className="overlay" data-testid="ac-scope-modal">
          <div className="mask" onClick={() => setScopeEdit(null)} />
          <div className="modal">
            <div className="mhead">
              <div className="mic"><ShieldCheck size={17} color="var(--accent-text)" /></div>
              <div><div className="mt">{t('acScopeEdit')}</div><div className="ms">{t('acIpEditSub', { name: scopeEdit.client.name })}</div></div>
              <X size={17} className="mx" onClick={() => setScopeEdit(null)} />
            </div>
            <div className="mbody">
              <div>
                <div className="fl">{t('acScopes')}</div>
                <div className="scopes">
                  {SCOPES.map((s) => (
                    <span
                      key={s} className={clsx('sc', scopeEdit.scopes.includes(s) && 'on', isGrantScope(s) && 'grant')}
                      onClick={() => setScopeEdit((v) => (v ? { ...v, scopes: flip(v.scopes, s) } : v))}
                    >
                      {scopeEdit.scopes.includes(s) && <Check size={11} />}{s}
                    </span>
                  ))}
                </div>
                {!scopeEdit.scopes.length && <div className="hint warn" data-testid="ac-scope-empty">{t('acScopeEmpty')}</div>}
                {scopeEdit.scopes.some(isGrantScope) && <div className="hint warn">{t('acGrantScopeWarn')}</div>}
                {scopeEdit.scopes.includes('inspection:write') && <div className="hint">{t('acInspectionScopeHint')}</div>}
                {scopeEdit.scopes.includes('release:approve') && <div className="hint warn">{t('acApproveScopeHint')}</div>}
                {scopeSodRisk && <div className="hint warn" data-testid="ac-scope-sod-warn">{t('acScopeSodWarn')}</div>}
              </div>
            </div>
            <div className="mfoot">
              <VButton onClick={() => setScopeEdit(null)}>{t('btnCancel')}</VButton>
              <VButton variant="primary" data-testid="ac-scope-save" disabled={scopeSaving || !scopeEdit.scopes.length} onClick={saveScopeEdit}>{t('btnSave')}</VButton>
            </div>
          </div>
        </div>
      )}

      {/* create form */}
      {formOpen && (
        <div className="overlay">
          <div className="mask" onClick={() => setFormOpen(false)} />
          <div className="modal">
            <div className="mhead">
              <div className="mic"><KeyRound size={17} color="var(--accent-text)" /></div>
              <div><div className="mt">{t('acNew')}</div><div className="ms">{t('acNewSub')}</div></div>
              <X size={17} className="mx" onClick={() => setFormOpen(false)} />
            </div>
            <div className="mbody">
              <div><div className="fl">{t('acName')}</div><input value={nf.name} onChange={(e) => patchNf({ name: e.target.value })} className="in" placeholder={t('acNamePh')} /></div>
              <div>
                <div className="fl">{t('acAccount')}</div>
                <VSelect value={userLabel} onChange={setUserLabel} options={userLabels} searchable />
                <div className="hint">{t('acAccountHint')}</div>
              </div>
              <div>
                <div className="fl">{t('acScopes')}</div>
                <div className="scopes">
                  {SCOPES.map((s) => (
                    <span
                      key={s} className={clsx('sc', nf.scopes.includes(s) && 'on', isGrantScope(s) && 'grant')}
                      onClick={() => setNf((v) => ({ ...v, scopes: flip(v.scopes, s) }))}
                    >
                      {nf.scopes.includes(s) && <Check size={11} />}{s}
                    </span>
                  ))}
                </div>
                {nf.scopes.some(isGrantScope) && <div className="hint warn" data-testid="grant-scope-warn">{t('acGrantScopeWarn')}</div>}
                {nf.scopes.includes('inspection:write') && <div className="hint" data-testid="inspection-scope-hint">{t('acInspectionScopeHint')}</div>}
                {nf.scopes.includes('release:approve') && <div className="hint warn" data-testid="approve-scope-hint">{t('acApproveScopeHint')}</div>}
              </div>
              <div>
                <div className="fl">{t('acPipeline')}</div>
                <VSelect value={pipeLabel} onChange={setPipeLabel} options={pipeOptions} />
                <div className="hint">{t('acPipelineHint')}</div>
              </div>
              <div>
                <div className="fl">{t('acAllowIPs')}</div>
                <input value={nf.allowIps} onChange={(e) => patchNf({ allowIps: e.target.value })} className="in" placeholder="10.0.0.0/8, 203.0.113.7" />
                <div className="hint">{t('acAllowIPsHint')}</div>
              </div>
            </div>
            <div className="mfoot">
              <VButton onClick={() => setFormOpen(false)}>{t('btnCancel')}</VButton>
              <VButton variant="primary" onClick={create}>{t('acCreate')}</VButton>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
