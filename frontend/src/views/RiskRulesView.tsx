import { useState } from 'react'
import { Link } from 'react-router'
import clsx from 'clsx'
import { ListX, X, Plus } from 'lucide-react'
import VButton from '@/components/common/VButton'
import api from '@/api'
import { confirmAction } from '@/lib/confirm'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import { useEnvTierStore } from '@/stores/envtier'
import { useI18n } from '@/hooks/useI18n'
import { useStateRef } from '@/hooks/useStateRef'
import { useMountEffect } from '@/hooks/effects'
import type { RiskCommandView } from '@/types'
import './RiskRulesView.scoped.css'

const defaultTriggers = () => [
  { name: 'DROP', on: true }, { name: 'TRUNCATE', on: true }, { name: 'ALTER', on: false },
  { name: 'DELETE', on: false }, { name: 'GRANT', on: false },
]
const cleanName = (s: string) => s.trim().toUpperCase().replace(/[^A-Z_ ]/g, '')
const nextLevel: Record<string, string> = { high: 'mid', mid: 'off', off: 'high' }

interface PolicyCard {
  key: string; label: string; tag: string; border: string
  tagBg: string; tagC: string; expr: string; hint?: string
}

export default function RiskRulesView() {
  const { t } = useI18n()
  const envtier = useEnvTierStore()
  // Rule configuration is platform-admin only (backend enforces it too).
  const isAdmin = useAuthStore((s) => s.me?.roleCode === 'admin')
  const [cmds, setCmds] = useState<RiskCommandView[]>([])
  // The dictionary is keyed by control tier, and tiers are rows an administrator
  // creates — so the visible tab set comes from the store, not from a union type.
  // `env` here (and in the API) is a TIER code; the column name is historical.
  const [env, setEnv, envRef] = useStateRef('prod')
  const [draft, setDraft] = useState('')
  const [ruleForm, setRuleForm] = useState(false)

  // new-rule form state (functional: creates dictionary entries)
  const [rfTriggers, setRfTriggers] = useState(defaultTriggers)
  const [rfTrigDraft, setRfTrigDraft] = useState('')
  const [rfEnv, setRfEnv] = useState('prod')
  const [rfAction, setRfAction] = useState<'block' | 'approve'>('block')

  /** Tier codes in display order — the tabs, the segmented control and the cards. */
  const tierCodes = envtier.tierCodes

  function openRule() {
    setRfTriggers(defaultTriggers())
    setRfTrigDraft('')
    setRfEnv(tierCodes[0] ?? 'prod')
    setRfAction('block')
    setRuleForm(true)
  }
  function addTrigger() {
    const n = cleanName(rfTrigDraft)
    setRfTrigDraft('')
    if (!n || rfTriggers.some((x) => x.name === n)) return
    setRfTriggers([...rfTriggers, { name: n, on: true }])
  }
  async function createRule() {
    // block→high (拦截), approve→mid (需审批). 字典没有"放行但告警"这一档(issue #125)。
    // Never map to 'off', which would DISABLE the rule and silently allow the command (R11).
    const level = rfAction === 'block' ? 'high' : 'mid'
    const selected = rfTriggers.filter((x) => x.on).map((x) => x.name)
    // M14: 创建规则失败以 toast 呈现
    try {
      for (const name of selected) {
        // Only touch the chosen environment — sending off-defaults for the others
        // would wipe their existing levels (e.g. clear a PROD=high interception).
        setCmds(await api.upsertRiskCommand(name, { [rfEnv]: level }))
      }
      setEnv(rfEnv)
      setRuleForm(false)
    } catch (e) {
      useUIStore.getState().notifyError(e, t('actionFailed'))
    }
  }

  // Real rule-hit count (PROD interceptions), from the gateway — no demo number.
  const [hits, setHits] = useState(0)

  async function load() {
    // Tiers first: they are the tab set, and `env` must point at one that exists.
    try {
      await useEnvTierStore.getState().load()
      const codes = useEnvTierStore.getState().tierCodes
      if (!codes.includes(envRef.current)) setEnv(codes[0] ?? envRef.current)
    } catch { /* tabs fall back to the current selection */ }
    // M14: 加载失败以 toast 呈现，避免静默失败
    try {
      const list = (await api.riskCommands()) ?? []
      setCmds(list)
      useUIStore.getState().setPageSub({ key: 'subRules', params: { n: list.length } })
    } catch (e) {
      useUIStore.getState().notifyError(e, t('loadFailed'))
    }
    try {
      setHits((await api.gatewayStats()).intercepts || 0)
    } catch { /* keep 0 */ }
  }
  useMountEffect(() => { void load() })

  // PROD policy coverage: share of dictionary commands actively gated (not "off").
  const gated = cmds.filter((c) => c.tiers?.prod !== 'off').length
  const coverage = cmds.length ? Math.round((gated / cmds.length) * 1000) / 10 : 0

  // 无 WHERE 的 DELETE / UPDATE 现在按分层开关,存在分层上(迁移 0030),和它上面
  // 两层规则一样。这里只呈现哪些分层开着 —— 改在【环境分层】页,那里是所有按分层
  // 生效的开关的所在地,不再在两个页面各放一个能改同一件事的控件。
  const strictTiers = envtier.tiers.filter((x) => x.strictNoWhere).map((x) => x.code)

  function lvlMeta(l?: string) {
    if (l === 'high') return { bg: 'var(--danger-subtle)', c: 'var(--danger-text)', tag: t('block') }
    if (l === 'mid') return { bg: 'var(--warning-subtle)', c: 'var(--warning-text)', tag: t('approve') }
    return { bg: 'var(--surface-sunken)', c: 'var(--text-muted)', tag: t('allow') }
  }

  async function cycle(c: RiskCommandView) {
    if (!isAdmin) return
    const cur = c.tiers?.[env] || 'off'
    // M14: 切换等级失败以 toast 呈现
    try {
      setCmds(await api.patchRiskCommand(c.command, env, nextLevel[cur]))
    } catch (e) {
      useUIStore.getState().notifyError(e, t('actionFailed'))
    }
  }
  // Adding a command opens a per-tier picker instead of posting immediately.
  //
  // The operator names the level on EVERY tier, because the alternative is the
  // server guessing — and a guess here is not visible anywhere afterwards. Tiers
  // left at 放行 still get a row: absent and `off` mean the same thing to the
  // engine, but only one of them shows up on this page as a decision somebody made.
  const [addOpen, setAddOpen] = useState(false)
  const [addLevels, setAddLevels] = useState<Record<string, string>>({})

  function openAdd() {
    if (!isAdmin) return
    const name = cleanName(draft)
    if (!name) return
    if (cmds.some((c) => c.command === name)) { setDraft(''); return }
    // Prefilled with the same shape the server would have chosen, so the common
    // case is one confirmation rather than a form — but every value is on screen
    // and changeable before anything is written.
    const pre: Record<string, string> = {}
    for (const x of envtier.tiers) pre[x.code] = x.scanBaseline || x.requireMfa ? 'high' : 'off'
    setAddLevels(pre)
    setAddOpen(true)
  }

  async function confirmAdd() {
    if (!isAdmin) return
    const name = cleanName(draft)
    if (!name) return
    try {
      setCmds(await api.upsertRiskCommand(name, { ...addLevels }))
      setDraft('')
      setAddOpen(false)
    } catch (e) {
      useUIStore.getState().notifyError(e, t('actionFailed'))
    }
  }
  async function removeCmd(c: RiskCommandView) {
    if (!isAdmin) return
    // M15: 删除风控规则前二次确认
    if (!(await confirmAction(t('rrDelConfirm', { name: c.command })))) return
    // M14: 删除失败以 toast 呈现
    try {
      setCmds(await api.deleteRiskCommand(c.command))
    } catch (e) {
      useUIStore.getState().notifyError(e, t('actionFailed'))
    }
  }

  // Policy summary cards derived from the live dictionary + strict mode — no
  // fabricated rules. Each reflects real backend state.
  const policies: PolicyCard[] = []

  const highProd = cmds.filter((c) => c.tiers?.prod === 'high').map((c) => c.command)
  if (highProd.length) {
    policies.push({
      key: 'pHigh', label: t('pHigh'), tag: t('actBlockAppr'), border: 'var(--danger)',
      tagBg: 'var(--danger-subtle)', tagC: 'var(--danger-text)',
      expr: `env=PROD AND cmd IN (${highProd.join(', ')}) → ${t('block')} → ${t('approve')}`,
    })
  }

  // 第三层:按分层生效,卡片照实说它在哪些分层上开着。
  const on = strictTiers.length > 0
  policies.push({
    key: 'pStrict', label: t('pStrict'), tag: on ? t('actBlockAppr') : t('actOff'), border: on ? 'var(--danger)' : 'var(--border-default)',
    tagBg: on ? 'var(--danger-subtle)' : 'var(--surface-sunken)', tagC: on ? 'var(--danger-text)' : 'var(--text-faint)',
    expr: on
      ? `env IN (${strictTiers.join(', ').toUpperCase()}) AND cmd IN (DELETE, UPDATE) AND NOT contains(WHERE) → ${t('block')}`
      : `cmd IN (DELETE, UPDATE) AND NOT contains(WHERE) → ${t('allow')}`,
    hint: t('pStrictWhere'),
  })

  const midAny = cmds
    .filter((c) => tierCodes.some((e) => c.tiers?.[e] === 'mid'))
    .map((c) => c.command)
  if (midAny.length) {
    policies.push({
      key: 'pAppr', label: t('pAppr'), tag: t('actAppr'), border: 'var(--warning)',
      tagBg: 'var(--warning-subtle)', tagC: 'var(--warning-text)',
      expr: `cmd IN (${midAny.join(', ')}) → ${t('approve')}`,
    })
  }

  return (
    <div className="scy page">
      <div className="v-head">
        <div className="v-head-l">
          <div className="v-eyebrow">RISK POLICIES</div>
          <div className="v-sub">{t('riskSub')}</div>
        </div>
        {!isAdmin
          ? <span className="roflag">{t('readOnlyPerms')}</span>
          : <VButton variant="primary" onClick={openRule}>{t('newRule')}</VButton>}
      </div>

      {/* dictionary */}
      <div className="dict">
        <div className="dhead">
          <div className="dic"><ListX size={17} color="var(--danger)" /></div>
          <div className="grow"><div className="dt">{t('dictTitle')}</div><div className="ds">{t('dictSub')}</div></div>
          {/* One tab per tier; scrolls sideways once they outgrow the header. */}
          <div className="envtabs scx">
            {tierCodes.map((e) => (
              <div key={e} className={clsx('et', env === e && 'active')} title={envtier.tierLabel(e, t)} onClick={() => setEnv(e)}>{e.toUpperCase()}</div>
            ))}
          </div>
        </div>
        <div className="chips">
          {cmds.map((c) => {
            const lv = c.tiers?.[env]
            const m = lvlMeta(lv)
            return (
              <div key={c.command} className="chip" style={{ background: m.bg, color: m.c, opacity: lv === 'off' ? 0.5 : 1 }}>
                <span className="cname">{c.command}</span>
                <span className="ctag" title={isAdmin ? t('tipCycleLevel') : ''} style={{ cursor: isAdmin ? 'pointer' : 'default' }} onClick={() => cycle(c)}>{m.tag}</span>
                {isAdmin && <span className="cx" title={t('tipRemove')} onClick={() => removeCmd(c)}><X size={12} /></span>}
              </div>
            )
          })}
          {isAdmin && (
            <div className="addchip">
              <input value={draft} onChange={(e) => setDraft(e.target.value)} placeholder={t('addCmdPh')} onKeyUp={(e) => { if (e.key === 'Enter') openAdd() }} />
              <span className="addbtn" onClick={openAdd}><Plus size={14} /></span>
            </div>
          )}
          {/* Every tier is listed, including the ones being left alone: a command
              that does not apply somewhere is a decision, and it should be visible
              as one rather than inferred from a missing row. */}
          {addOpen && (
            <div className="addpanel">
              <div className="apt">{t('addCmdScope', { cmd: draft.trim().toUpperCase() })}</div>
              {tierCodes.map((e) => (
                <div className="aprow" key={e}>
                  <span className="apc">{e.toUpperCase()}</span>
                  <span className="apn">{envtier.tierLabel(e, t)}</span>
                  <div className="apseg">
                    {['high', 'mid', 'off'].map((lv) => (
                      <span
                        key={lv}
                        className={clsx(addLevels[e] === lv && 'on', 'lv-' + lv)}
                        onClick={() => setAddLevels((m) => ({ ...m, [e]: lv }))}
                      >{t('lv_' + lv)}</span>
                    ))}
                  </div>
                </div>
              ))}
              <div className="apfoot">
                <VButton variant="secondary" height="30px" onClick={() => setAddOpen(false)}>{t('btnCancel')}</VButton>
                <VButton variant="primary" height="30px" onClick={confirmAdd}>{t('addCmdConfirm')}</VButton>
              </div>
            </div>
          )}
        </div>
      </div>

      {/* stats */}
      <div className="stats">
        <div className="stat"><div className="n danger">{highProd.length}</div><div className="l">{t('statBlock')}</div></div>
        <div className="stat"><div className="n warn">{cmds.filter((c) => c.tiers?.prod === 'mid').length}</div><div className="l">{t('statAppr')}</div></div>
        <div className="stat"><div className="n grad">{hits}</div><div className="l">{t('statHits')}</div></div>
        <div className="stat"><div className="n">{coverage}%</div><div className="l">{t('statCover')}</div></div>
      </div>

      {/* policies (derived from the live dictionary + strict mode) */}
      <div className="rules">
        {policies.map((r) => (
          <div key={r.key} className="rule" style={{ borderLeftColor: r.border }}>
            <div className="rgrow">
              <div className="rtop">
                <span className="rn">{r.label}</span>
                <span className="rtag" style={{ background: r.tagBg, color: r.tagC }}>{r.tag}</span>
              </div>
              <div className="rexpr">{r.expr}</div>
              {r.hint && <div className="rhint">{r.hint}</div>}
            </div>
            {r.hint && <Link className="rlink" to="/admin/env-tiers">{t('pStrictGo')}</Link>}
          </div>
        ))}
      </div>

      {/* new rule modal (visual) */}
      {ruleForm && (
        <div className="overlay">
          <div className="mask" onClick={() => setRuleForm(false)} />
          <div className="rfmodal">
            <div className="rfhead">
              <div className="rfic"><Plus size={19} color="var(--accent-text)" /></div>
              <div><div className="rft">{t('rfTitle')}</div><div className="rfs">{t('rfSub')}</div></div>
              <X size={18} className="rfx" onClick={() => setRuleForm(false)} />
            </div>
            <div className="rfbody">
              <div>
                <div className="fl">{t('rfTriggers')}</div>
                <div className="trigs">
                  {rfTriggers.map((tg) => (
                    <span
                      key={tg.name} className={clsx('trig', !tg.on && 'off')}
                      onClick={() => setRfTriggers((xs) => xs.map((x) => (x.name === tg.name ? { ...x, on: !x.on } : x)))}
                    >{tg.name}</span>
                  ))}
                  <span className="trig add">
                    <input value={rfTrigDraft} onChange={(e) => setRfTrigDraft(e.target.value)} placeholder={t('rfCustomPh')} onKeyUp={(e) => { if (e.key === 'Enter') addTrigger() }} />
                    <Plus size={12} onClick={addTrigger} />
                  </span>
                </div>
              </div>
              <div className="rf2">
                <div>
                  <div className="fl">{t('rfEnv')}</div>
                  <div className="seg scx">
                    {tierCodes.map((e) => (
                      <div key={e} className={clsx('si', rfEnv === e && 'active')} title={envtier.tierLabel(e, t)} onClick={() => setRfEnv(e)}>{e.toUpperCase()}</div>
                    ))}
                  </div>
                </div>
                <div>
                  <div className="fl">{t('rfAction')}</div>
                  <div className="seg">
                    <div className={clsx('si', rfAction === 'block' && 'active danger')} onClick={() => setRfAction('block')}>{t('rfActBlock')}</div>
                    <div className={clsx('si', rfAction === 'approve' && 'active')} onClick={() => setRfAction('approve')}>{t('rfActApprove')}</div>
                  </div>
                </div>
              </div>
            </div>
            <div className="rffoot">
              <div className="rfhint">{t('rfFootHint')}</div>
              <div className="rfacts"><VButton variant="secondary" onClick={() => setRuleForm(false)}>{t('cancel')}</VButton><VButton variant="primary" onClick={createRule}>{t('rfCreate')}</VButton></div>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
