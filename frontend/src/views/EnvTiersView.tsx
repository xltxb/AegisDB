/**
 * Control tiers and environments.
 *
 * A tier owns the rules; an environment groups instances and binds to one tier.
 * Adding a second production cluster is a new ENVIRONMENT on the existing prod
 * tier — nothing is copied and it is governed from its first second.
 *
 * Almost every control on this page is a guard rather than a convenience. Both
 * rule lookups treat a missing row as permission granted, so a tier without its
 * rules, or an instance whose environment no longer resolves, is an unregulated
 * production database that looks exactly like a regulated one. The server
 * enforces each rule; the UI states the reason up front so an operator does not
 * discover it from a rejected request.
 */
import { useState } from 'react'
import clsx from 'clsx'
import { Layers, Boxes, Plus, X, ShieldCheck, Pencil, Trash2, Database } from 'lucide-react'
import VButton from '@/components/common/VButton'
import VSwitch from '@/components/common/VSwitch'
import VSelect from '@/components/common/VSelect'
import api from '@/api'
import { confirmAction } from '@/lib/confirm'
import { sameForm } from '@/lib/unsavedChanges'
import { BUILTIN_CONN_LAYER, builtinLabel } from '@/lib/builtinNames'
import { useMountEffect } from '@/hooks/effects'
import { useI18n } from '@/hooks/useI18n'
import { useUnsavedFlag } from '@/hooks/useUnsavedFlag'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import { useEnvTierStore } from '@/stores/envtier'
import type { EnvTier } from '@/types'
import './EnvTiersView.scoped.css'

// 已绑定环境:列表里最多摊开三个,其余折成 +N。一个分层底下挂七八个环境是常事,
// 全铺出来这一列会把整行撑成两行高,而它回答的问题只是"大概挂了哪些"。
const MAX_ENV_CHIPS = 3

const BLANK_TIER = {
  code: '', displayName: '', templateCode: '', sortOrder: 0,
  requireMfa: false, dangerBanner: false, countsInPending: false, strictNoWhere: true, maskSensitive: true, bizExportRestrict: true,
  autoExecute: false, connLayer: '', defaultRole: 'dba_l2',
}

export default function EnvTiersView() {
  const { t } = useI18n()
  const layerLabel = (s: string) => builtinLabel(BUILTIN_CONN_LAYER, s, t)
  const notify = useUIStore((s) => s.notify)
  const notifyError = useUIStore((s) => s.notifyError)
  const me = useAuthStore((s) => s.me)
  const envtier = useEnvTierStore()
  // Every mutation is admin-only server-side; non-admins get the page read-only.
  const isAdmin = me?.roleCodes?.includes('admin') ?? (me?.roleCode === 'admin')

  // 三个子领域分成页签,而不是一路铺下去。它们是三张各自完整的表,叠在一页里要滚
  // 三屏才看得完,而每次进来其实只处理其中一件事。
  const [tab, setTab] = useState<'tiers' | 'envs'>('tiers')

  const [usage, setUsage] = useState<Record<string, number>>({})
  const [busy, setBusy] = useState(false)

  async function reload() {
    await useEnvTierStore.getState().load(true)
    try { setUsage((await api.environmentUsage()) ?? {}) } catch { /* counts are advisory */ }
    const s = useEnvTierStore.getState()
    useUIStore.getState().setPageSub({ key: 'etSub', params: { tiers: s.tiers.length, envs: s.environments.length } })
  }

  useMountEffect(() => {
    reload().catch((e) => notifyError(e, t('actionFailed')))
  })

  const envCount = (code: string) => usage[code] ?? 0

  /** Environments bound to a tier — what blocks its deletion. */
  const boundEnvs = (code: string) => envtier.environments.filter((e) => e.tierCode === code)
  const envChips = (code: string) => boundEnvs(code).slice(0, MAX_ENV_CHIPS)
  const envChipsMore = (code: string) => Math.max(0, boundEnvs(code).length - MAX_ENV_CHIPS)

  // 环境行的分层绑定:默认只显示一个标签,点"编辑"才换成下拉。整列都摆着下拉框时,
  // 一张只是想看一眼归属的表会长得像一张待填的表单。
  const [editEnv, setEditEnv] = useState('')

  // ---------------------------------------------------------------- tier form

  const [tierForm, setTierForm] = useState(false)
  const [tf, setTf] = useState(BLANK_TIER)
  const patchTf = (p: Partial<typeof BLANK_TIER>) => setTf((f) => ({ ...f, ...p }))
  const [tfSnap, setTfSnap] = useState<unknown>(null)
  function openTierForm() {
    if (!isAdmin) return
    const f = { ...BLANK_TIER, templateCode: envtier.tierCodes[0] ?? '', sortOrder: envtier.tiers.length }
    setTf(f)
    setTfSnap(f)
    setTierForm(true)
  }
  async function createTier() {
    if (!tf.code.trim() || !tf.displayName.trim() || !tf.templateCode) return
    setBusy(true)
    try {
      // The template is required by the API, not just prefilled here: a tier
      // created empty would have no capability and no dictionary rows, and every
      // lookup against it falls through to allowed.
      await api.createEnvTier({ ...tf, scanBaseline: false })
      setTierForm(false)
      await reload()
      notify(t('etTierCreated', { code: tf.code, from: tf.templateCode }), 'success')
    } catch (e) {
      notifyError(e, t('actionFailed'))
    } finally { setBusy(false) }
  }

  async function saveTier(tier: EnvTier, patch: Partial<EnvTier>) {
    if (!isAdmin) return
    setBusy(true)
    try {
      await api.updateEnvTier(tier.code, { ...tier, ...patch })
      await reload()
    } catch (e) {
      notifyError(e, t('actionFailed'))
      await reload() // the switch showed the attempted state; put it back
    } finally { setBusy(false) }
  }

  /**
   * Moving the scan baseline. It is single-valued: setting it here clears it
   * elsewhere in the same transaction, and it can never be switched off outright —
   * with no baseline, script scanning matches nothing and reports every uploaded
   * script as clean, without raising an error anywhere.
   */
  async function makeBaseline(tier: EnvTier) {
    if (!isAdmin || tier.scanBaseline) return
    if (!(await confirmAction(t('etBaselineConfirm', { code: tier.code })))) return
    await saveTier(tier, { scanBaseline: true })
  }

  async function removeTier(tier: EnvTier) {
    if (!isAdmin || !tierDeletable(tier)) return
    if (!(await confirmAction(t('etTierDeleteConfirm', { code: tier.code })))) return
    setBusy(true)
    try {
      await api.deleteEnvTier(tier.code)
      await reload()
      notify(t('etTierDeleted', { code: tier.code }), 'success')
    } catch (e) {
      notifyError(e, t('actionFailed'))
    } finally { setBusy(false) }
  }

  const tierDeletable = (tier: EnvTier) =>
    !tier.scanBaseline && boundEnvs(tier.code).length === 0 && envtier.tiers.length > 1
  /** Why the delete is unavailable — shown instead of an unexplained dead button. */
  function tierBlockReason(tier: EnvTier): string {
    if (tier.scanBaseline) return t('etBlockedBaseline')
    const n = boundEnvs(tier.code).length
    if (n > 0) return t('etBlockedBound', { n })
    if (envtier.tiers.length <= 1) return t('etBlockedLast')
    return ''
  }

  // ------------------------------------------------------------ environment form

  const [envForm, setEnvForm] = useState(false)
  const [ef, setEf] = useState({ code: '', displayName: '', tierCode: '', sortOrder: 0 })
  const patchEf = (p: Partial<typeof ef>) => setEf((f) => ({ ...f, ...p }))
  const [efSnap, setEfSnap] = useState<unknown>(null)
  function openEnvForm() {
    if (!isAdmin) return
    const f = {
      code: '', displayName: '', tierCode: envtier.tierCodes[0] ?? '',
      sortOrder: envtier.environments.length,
    }
    setEf(f)
    setEfSnap(f)
    setEnvForm(true)
  }
  // 新建分层 / 环境的表单开着且填过东西 = 有没保存的改动(管理后台空闲锁屏,2026-10-26 回归排查 10)。
  // 分层的开关、环境的归属是即改即存的,不算。
  useUnsavedFlag((tierForm && !sameForm(tf, tfSnap)) || (envForm && !sameForm(ef, efSnap)))
  async function createEnvironment() {
    if (!ef.code.trim() || !ef.displayName.trim() || !ef.tierCode) return
    setBusy(true)
    try {
      await api.createEnvironment(ef)
      setEnvForm(false)
      await reload()
      notify(t('etEnvCreated', { code: ef.code, tier: ef.tierCode }), 'success')
    } catch (e) {
      notifyError(e, t('actionFailed'))
    } finally { setBusy(false) }
  }

  /**
   * Rebinding an environment to another tier changes how every instance in it is
   * governed from that moment on. It does NOT rewrite history: approvals and audit
   * rows keep the tier they were judged under.
   */
  async function rebind(code: string, tierLabel: string) {
    const tierCode = envtier.tierCodes.find((c) => tierOptionLabel(c) === tierLabel)
    const env = envtier.envByCode[code]
    if (!isAdmin || !env || !tierCode || tierCode === env.tierCode) return
    if (!(await confirmAction(t('etRebindConfirm', { code, from: env.tierCode, to: tierCode, n: envCount(code) })))) {
      await reload() // revert the select
      return
    }
    setBusy(true)
    try {
      await api.updateEnvironment(code, { ...env, tierCode })
      await reload()
    } catch (e) {
      notifyError(e, t('actionFailed'))
      await reload()
    } finally { setBusy(false) }
  }

  /**
   * Deleting an environment moves its instances rather than orphaning them: an
   * instance pointing at an environment that no longer exists resolves to no tier,
   * and therefore to no rules at all.
   */
  const [delEnv, setDelEnv] = useState<{ open: boolean; code: string; moveTo: string }>({ open: false, code: '', moveTo: '' })
  function closeDelEnv() { setDelEnv((d) => ({ ...d, open: false })) }
  function openDeleteEnv(code: string) {
    if (!isAdmin || envtier.environments.length <= 1) return
    const other = envtier.environments.find((e) => e.code !== code)
    setDelEnv({ open: true, code, moveTo: other ? envOptionLabel(other.code) : '' })
  }
  async function confirmDeleteEnv() {
    const moveTo = envtier.environments.find((e) => envOptionLabel(e.code) === delEnv.moveTo)?.code
    if (!moveTo || moveTo === delEnv.code) return
    setBusy(true)
    try {
      await api.deleteEnvironment(delEnv.code, moveTo)
      notify(t('etEnvDeleted', { code: delEnv.code, n: envCount(delEnv.code), to: moveTo }), 'success')
      setDelEnv({ open: false, code: '', moveTo: '' })
      await reload()
    } catch (e) {
      notifyError(e, t('actionFailed'))
    } finally { setBusy(false) }
  }

  // Selects bind on label strings, so labels must be unique — codes are.
  const tierOptionLabel = (code: string) => `${code} · ${envtier.tierLabel(code, t)}`
  const envOptionLabel = (code: string) => `${code} · ${envtier.envLabel(code)}`
  const tierOptions = envtier.tierCodes.map(tierOptionLabel)
  const moveTargets = envtier.environments.filter((e) => e.code !== delEnv.code).map((e) => envOptionLabel(e.code))

  const sw = (tier: EnvTier, patch: (v: boolean) => Partial<EnvTier>) => (v: boolean) => { if (isAdmin) void saveTier(tier, patch(v)) }
  const ctr = clsx('ctr', !isAdmin && 'ro')

  return (
    <div className="scy page">
      <div className="v-head">
        <div className="v-head-l">
          <div className="v-eyebrow">TIERS &amp; ENVIRONMENTS</div>
          <div className="v-h1">{t('t_envtier')}</div>
          <div className="v-sub">{t('etIntro')}</div>
        </div>
        {!isAdmin && <span className="roflag">{t('readOnlyPerms')}</span>}
      </div>

      {/* 三个子领域分页签。它们是三张各自完整的表 —— 一路铺下去要滚三屏才看得完,
          而每次进来其实只处理其中一件事。 */}
      <div className="segtabs">
        <button className={clsx('segtab', tab === 'tiers' && 'on')} onClick={() => setTab('tiers')}>
          <Layers size={14} />{t('etTierTitle')}<span className="segn">{envtier.tiers.length}</span>
        </button>
        <button className={clsx('segtab', tab === 'envs' && 'on')} onClick={() => setTab('envs')}>
          <Boxes size={14} />{t('etEnvTitle')}<span className="segn">{envtier.environments.length}</span>
        </button>
      </div>

      {/* ------------------------------------------------------------ tiers */}
      <div style={{ display: tab === 'tiers' ? undefined : 'none' }} className="card">
        <div className="chead">
          <div className="cic"><Layers size={17} color="var(--danger)" /></div>
          <div className="grow">
            <div className="ct">{t('etTierTitle')}</div>
            <div className="cs">{t('etTierSub')}</div>
          </div>
          {isAdmin && (
            <VButton variant="secondary" height="34px" onClick={openTierForm}>
              <Plus size={14} />{t('etNewTier')}
            </VButton>
          )}
        </div>

        <div className="scx">
          <div className="tgrid">
            {/* 六个管控开关合并到一个大表头下面。它们回答的是同一个问题("这一层
                按什么规矩办"),分散成平级列头时,读的人得逐列去猜彼此的关系。 */}
            <div className="thgroup">
              <span />
              <span className="grouphd">{t('etGroupPolicy')}</span>
              <span /><span /><span />
            </div>
            <div className="th" data-testid="tier-th">
              <span>{t('etColTier')}</span><span className="ctr">{t('etColMfa')}</span>
              <span className="ctr">{t('etColBanner')}</span><span className="ctr">{t('etColPending')}</span>
              <span className="ctr" title={t('etColStrictHint')}>{t('etColStrict')}</span>
              <span className="ctr" title={t('etColMaskHint')}>{t('etColMask')}</span>
              <span className="ctr" title={t('etColBizExportHint')}>{t('etColBizExport')}</span>
              <span className="ctr" title={t('etColAutoExecHint')}>{t('etColAutoExec')}</span>
              <span className="ctr">{t('etColBaseline')}</span><span>{t('etColBound')}</span><span />
            </div>
            {envtier.tiers.map((tier) => (
              <div key={tier.code} className="tr">
                <div className="tiercell">
                  <span className={clsx('tierbadge', envtier.dotFor(tier.code))}>{tier.code}</span>
                  <span className="tiertxt">
                    <span className="cl">{envtier.tierLabel(tier.code, t)}</span>
                    <span className="cl2">{layerLabel(tier.connLayer)} · {tier.defaultRole}</span>
                  </span>
                </div>
                <div className={ctr}><VSwitch value={tier.requireMfa} onChange={sw(tier, (v) => ({ requireMfa: v }))} /></div>
                <div className={ctr}><VSwitch value={tier.dangerBanner} onChange={sw(tier, (v) => ({ dangerBanner: v }))} /></div>
                <div className={ctr}><VSwitch value={tier.countsInPending} onChange={sw(tier, (v) => ({ countsInPending: v }))} /></div>
                {/* 判定的第三层。前两层(能力矩阵、高危命令字典)本来就按分层存,
                    这一层过去是个全局开关,于是它是唯一一道瞄不准的闸。 */}
                <div className={ctr} title={t('etColStrictHint')}><VSwitch value={tier.strictNoWhere} onChange={sw(tier, (v) => ({ strictNoWhere: v }))} /></div>
                {/* 2026-10-29:脱敏与数据查询导出限制按分层开关,默认只在 prod / staging / gli 上生效 */}
                <div className={ctr} title={t('etColMaskHint')}><VSwitch value={tier.maskSensitive} data-testid={'tier-mask-' + tier.code} onChange={sw(tier, (v) => ({ maskSensitive: v }))} /></div>
                <div className={ctr} title={t('etColBizExportHint')}><VSwitch value={tier.bizExportRestrict} data-testid={'tier-bizexport-' + tier.code} onChange={sw(tier, (v) => ({ bizExportRestrict: v }))} /></div>
                {/* 2026-10-30:发布流程能不能用「自动执行」阶段,默认只在 dev / uat 上开 */}
                <div className={ctr} title={t('etColAutoExecHint')}><VSwitch value={tier.autoExecute} data-testid={'tier-autoexec-' + tier.code} onChange={sw(tier, (v) => ({ autoExecute: v }))} /></div>
                {/* Radio semantics, not a switch: exactly one tier holds it, and
                    turning it off is never an option — only moving it elsewhere. */}
                <div className="ctr">
                  {tier.scanBaseline ? <span className="basebadge" title={t('etBaselineHeld')}><ShieldCheck size={13} />{t('etBaselineOn')}</span>
                    : isAdmin ? <button className="baselink" disabled={busy} onClick={() => makeBaseline(tier)}>{t('etBaselineSet')}</button>
                      : <span className="dash">—</span>}
                </div>
                <div className="chips">
                  {boundEnvs(tier.code).length > 0 ? (
                    <>
                      {envChips(tier.code).map((e) => <span key={e.code} className="chip">{e.code}</span>)}
                      {envChipsMore(tier.code) > 0 && <span className="chip more" title={boundEnvs(tier.code).map((e) => e.code).join(', ')}>+{envChipsMore(tier.code)}</span>}
                    </>
                  ) : <span className="dash">{t('etNoEnv')}</span>}
                </div>
                <div className="acts">
                  {isAdmin && (
                    <button className="iconbtn danger" disabled={!tierDeletable(tier) || busy} title={tierBlockReason(tier) || t('etDelete')} onClick={() => removeTier(tier)}>
                      <Trash2 size={13} />
                    </button>
                  )}
                </div>
              </div>
            ))}
          </div>
        </div>

        {tierForm && (
          <div className="form">
            <div className="fhead">{t('etNewTier')}<span className="x" onClick={() => setTierForm(false)}><X size={16} /></span></div>
            <div className="frow">
              <div><div className="fl">{t('etFCode')}</div><input value={tf.code} onChange={(e) => patchTf({ code: e.target.value })} placeholder="prod-hk" /></div>
              <div><div className="fl">{t('etFName')}</div><input value={tf.displayName} onChange={(e) => patchTf({ displayName: e.target.value })} placeholder={t('etFNamePh')} /></div>
            </div>
            <div className="frow">
              <div><div className="fl">{t('etFTemplate')}</div><VSelect value={tf.templateCode} onChange={(v) => patchTf({ templateCode: v })} options={envtier.tierCodes} floating /></div>
              <div><div className="fl">{t('etFLayer')}</div><input value={tf.connLayer} onChange={(e) => patchTf({ connLayer: e.target.value })} placeholder={t('etLayerPh')} /></div>
            </div>
            {/* Stating what the clone does is the whole point of the mandatory
                template: the operator is copying a rule set, not naming a label. */}
            <div className="warn">{t('etCloneNote', { from: tf.templateCode || '—' })}</div>
            <div className="frow3">
              <label className="chk"><VSwitch value={tf.requireMfa} onChange={(v) => patchTf({ requireMfa: v })} />{t('etColMfa')}</label>
              <label className="chk"><VSwitch value={tf.dangerBanner} onChange={(v) => patchTf({ dangerBanner: v })} />{t('etColBanner')}</label>
              <label className="chk"><VSwitch value={tf.countsInPending} onChange={(v) => patchTf({ countsInPending: v })} />{t('etColPending')}</label>
              <label className="chk" title={t('etColStrictHint')}><VSwitch value={tf.strictNoWhere} onChange={(v) => patchTf({ strictNoWhere: v })} />{t('etColStrict')}</label>
              <label className="chk" title={t('etColMaskHint')}><VSwitch value={tf.maskSensitive} onChange={(v) => patchTf({ maskSensitive: v })} />{t('etColMask')}</label>
              <label className="chk" title={t('etColBizExportHint')}><VSwitch value={tf.bizExportRestrict} onChange={(v) => patchTf({ bizExportRestrict: v })} />{t('etColBizExport')}</label>
              <label className="chk" title={t('etColAutoExecHint')}><VSwitch value={tf.autoExecute} onChange={(v) => patchTf({ autoExecute: v })} />{t('etColAutoExec')}</label>
            </div>
            <div className="ffoot">
              <VButton variant="secondary" height="34px" onClick={() => setTierForm(false)}>{t('btnCancel')}</VButton>
              <VButton variant="primary" height="34px" disabled={busy || !tf.code || !tf.displayName || !tf.templateCode} onClick={createTier}>{t('btnSave')}</VButton>
            </div>
          </div>
        )}
      </div>

      {/* ----------------------------------------------------- environments */}
      <div style={{ display: tab === 'envs' ? undefined : 'none' }} className="card">
        <div className="chead">
          <div className="cic"><Boxes size={17} color="var(--accent-text)" /></div>
          <div className="grow">
            <div className="ct">{t('etEnvTitle')}</div>
            <div className="cs">{t('etEnvSub')}</div>
          </div>
          {isAdmin && (
            <VButton variant="secondary" height="34px" onClick={openEnvForm}>
              <Plus size={14} />{t('etNewEnv')}
            </VButton>
          )}
        </div>

        <div className="scx">
          <div className="egrid">
            <div className="th"><span>{t('etColEnv')}</span><span>{t('etColTierBind')}</span><span className="ctr">{t('etColInsts')}</span><span /></div>
            {envtier.environments.map((e) => (
              <div key={e.code} className="tr">
                <div className="tiercell">
                  <span className={clsx('d', envtier.dotForEnv(e.code))} />
                  <span className="tiertxt">
                    <span className="cn">{e.code}</span>
                    <span className="cl">{envtier.envLabel(e.code)}</span>
                  </span>
                </div>
                {/* 归属默认是一个标签,点铅笔才换成下拉。整列都摆着下拉框时,一张
                    只是想看一眼归属的表会长得像一张待填的表单。 */}
                <div className="bindcell">
                  {isAdmin && editEnv === e.code ? (
                    <>
                      <VSelect
                        value={tierOptionLabel(e.tierCode)} options={tierOptions} height="32px" floating
                        onChange={(v) => { void rebind(e.code, v); setEditEnv('') }}
                      />
                      <button className="iconbtn" title={t('btnCancel')} onClick={() => setEditEnv('')}><X size={13} /></button>
                    </>
                  ) : (
                    <>
                      <span className={clsx('tierbadge sm', envtier.dotFor(e.tierCode))}>{e.tierCode}</span>
                      <span className="bindname">{envtier.tierLabel(e.tierCode, t)}</span>
                      {isAdmin && <button className="iconbtn ghost" title={t('etRebind')} onClick={() => setEditEnv(e.code)}><Pencil size={12} /></button>}
                    </>
                  )}
                </div>
                <div className="ctr">
                  <span className={clsx('countpill', !envCount(e.code) && 'zero')}>
                    {/* 带上计数走复数分支(参数名 n 就是复数下标):英文里 "1 instances" 是错的,
                        而这一列大多数行的值恰恰是 1。中文只有一种形式,同一个 key 照常工作。 */}
                    <Database size={11} />{t('etInstN', { n: envCount(e.code) })}
                  </span>
                </div>
                <div className="acts">
                  {isAdmin && (
                    <button
                      className="iconbtn danger" disabled={envtier.environments.length <= 1 || busy}
                      title={envtier.environments.length <= 1 ? t('etBlockedLastEnv') : t('etDelete')} onClick={() => openDeleteEnv(e.code)}
                    >
                      <Trash2 size={13} />
                    </button>
                  )}
                </div>
              </div>
            ))}
          </div>
        </div>

        {envForm && (
          <div className="form">
            <div className="fhead">{t('etNewEnv')}<span className="x" onClick={() => setEnvForm(false)}><X size={16} /></span></div>
            <div className="frow">
              <div><div className="fl">{t('etFCode')}</div><input value={ef.code} onChange={(e) => patchEf({ code: e.target.value })} placeholder="prod-hk" /></div>
              <div><div className="fl">{t('etFName')}</div><input value={ef.displayName} onChange={(e) => patchEf({ displayName: e.target.value })} placeholder={t('etFEnvNamePh')} /></div>
            </div>
            <div className="frow">
              <div><div className="fl">{t('etColTierBind')}</div><VSelect value={ef.tierCode} onChange={(v) => patchEf({ tierCode: v })} options={envtier.tierCodes} floating /></div>
              <div />
            </div>
            <div className="note">{t('etEnvCreateNote', { tier: ef.tierCode || '—' })}</div>
            <div className="ffoot">
              <VButton variant="secondary" height="34px" onClick={() => setEnvForm(false)}>{t('btnCancel')}</VButton>
              <VButton variant="primary" height="34px" disabled={busy || !ef.code || !ef.displayName || !ef.tierCode} onClick={createEnvironment}>{t('btnSave')}</VButton>
            </div>
          </div>
        )}
      </div>

      {/* Deleting an environment always relocates its instances. */}
      {delEnv.open && (
        <div className="ovl">
          <div className="mask" onClick={closeDelEnv} />
          <div className="modal">
            <div className="mhead">{t('etDeleteEnvTitle', { code: delEnv.code })}</div>
            <div className="mbody">
              <div className="mwarn">{t('etDeleteEnvWarn', { n: envCount(delEnv.code) })}</div>
              <div className="fl">{t('etMoveTo')}</div>
              <VSelect value={delEnv.moveTo} onChange={(v) => setDelEnv((d) => ({ ...d, moveTo: v }))} options={moveTargets} />
            </div>
            <div className="mfoot">
              <VButton variant="secondary" height="34px" onClick={closeDelEnv}>{t('btnCancel')}</VButton>
              <VButton variant="danger" height="34px" disabled={busy || !delEnv.moveTo} onClick={confirmDeleteEnv}>{t('etDelete')}</VButton>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
