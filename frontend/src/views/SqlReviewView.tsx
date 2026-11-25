// 数据库规范审查规则库 + 手工自查。
//
// The page keeps the library and the checker together on purpose: a rule whose
// effect you cannot try is a rule nobody trusts enough to enable. Editing is
// admin-only (the server enforces it too); running a check is open to anyone who
// can use the terminal, because self-checking your own change before submitting
// it is what makes the standards useful rather than punitive.
import { useMemo, useState } from 'react'
import clsx from 'clsx'
import { reviewRuleName } from '@/lib/builtinNames'
import { reviewGroups } from '@/lib/reviewGroups'
import {
  paramSpecsFor, effectiveDefault, formatParamValue, paramsToDraft, draftToParams, effectiveParams, specForDialect,
} from '@/lib/reviewParams'
import {
  SpellCheck, Play, Plus, X, Trash2, CircleAlert, TriangleAlert, Info,
  Search, Settings2, ShieldAlert, ShieldCheck, WandSparkles, CircleCheck, FlaskConical,
} from 'lucide-react'
import VButton from '@/components/common/VButton'
import VSwitch from '@/components/common/VSwitch'
import VSelect from '@/components/common/VSelect'
import api from '@/api'
import { envError, localizeText } from '@/api/http'
import { confirmAction } from '@/lib/confirm'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import { useI18n } from '@/hooks/useI18n'
import { nav, useRoute } from '@/hooks/useNav'
import { useMountEffect } from '@/hooks/effects'
import type { Connection, ReviewCatalog, ReviewParamSpec, ReviewResult, ReviewRule } from '@/types'
import './SqlReviewView.scoped.css'

const LEVELS = ['error', 'warn', 'info'] as const
const SPECS = ['critical', 'mandatory', 'recommended']
const DIALECT_LABEL: Record<string, string> = { mysql: 'MySQL', tidb: 'TiDB', oracle: 'Oracle', dws: 'DWS', generic: 'Generic' }

function specMeta(s: string) {
  switch (s) {
    case 'critical': return { bg: 'var(--danger-subtle)', c: 'var(--danger-text)' }
    case 'mandatory': return { bg: 'var(--warn-subtle)', c: 'var(--warn-text)' }
    case 'recommended': return { bg: 'var(--accent-subtle)', c: 'var(--accent-text)' }
    default: return { bg: 'var(--surface-sunken)', c: 'var(--text-faint)' }
  }
}
function levelMeta(l: string) {
  if (l === 'error') return { bg: 'var(--danger-subtle)', c: 'var(--danger-text)', icon: CircleAlert }
  if (l === 'warn') return { bg: 'var(--warning-subtle)', c: 'var(--warning-text)', icon: TriangleAlert }
  return { bg: 'var(--surface-sunken)', c: 'var(--text-muted)', icon: Info }
}
// A rule's dialect scope as a LIST. Stored as "mysql,tidb" (or "all"), which
// rendered raw was an uppercased run-on — "MYSQL,TIDB" reads as one word.
function dialectsOf(r: ReviewRule) {
  if (!r.dialect || r.dialect === 'all') return []
  return r.dialect.split(',').map((d) => d.trim()).filter(Boolean)
}

export default function SqlReviewView() {
  const { t, te } = useI18n()
  // 内置规则名按读者语言显示;改过名的原样显示(lib/builtinNames reviewRuleName)。
  const zhText = (k: string) => (te(k, 'zh') ? t(k, {}, { locale: 'zh' }) : undefined)
  const ruleName = (code: string, stored: string) => reviewRuleName(code, stored, t, zhText)
  const ui = useUIStore.getState() // 只用动作,不读状态
  const me = useAuthStore((s) => s.me)
  // 规则库的维护在管理后台(管理后台拆分,工单 05):同一页,在用户后台只读 + 自查,在管理后台
  // (/admin/sql-review)才出现编辑控件。管理员在用户后台也是只读,给一个去管理后台的入口。
  const route = useRoute()
  const adminUser = me?.roleCode === 'admin' || (me?.roleCodes || []).includes('admin')
  const isAdmin = adminUser && route.meta.portal === 'admin'

  const [rules, setRules] = useState<ReviewRule[]>([])
  const [catalog, setCatalog] = useState<ReviewCatalog>({ dialects: ['all'], categories: [], levels: ['error', 'warn', 'info'] })
  const [dialect, setDialect] = useState('all')
  const [category, setCategory] = useState('')
  // 按规范分级筛选 —— 上线前最常问的一句是“这次改动碰了哪几条【高危】”,
  // 而不是“碰了哪几条 dml 规则”。'' = 全部,'none' = 规范未覆盖的平台内置规则。
  const [spec, setSpec] = useState('')
  const [conns, setConns] = useState<Connection[]>([])

  // ---- library ----
  // 规则名 / code 搜索。这里在客户端筛是**对的**:规则库是一次性全量拉下来的
  // (api.reviewRules 不分页),搜索看到的就是全部 —— 和审批页那个必须走服务端的
  // 搜索框不是一回事,那边列表分页,只筛当前页会对第三页的工单谎称"没有"。
  const [q, setQ] = useState('')
  const inDialect = (r: ReviewRule) => dialect === 'all' || r.dialect === 'all' || (r.dialect ?? '').split(',').includes(dialect)
  // 出处与分级按所选的库显示:MySQL 页上不拿 DWS 的【強制】去说一条 MySQL 规范没写的规则。
  const srcOf = (r: ReviewRule) => specForDialect(r, dialect)
  const kw = q.trim().toLowerCase()
  const shown = rules.filter((r) => {
    const dialectOk = inDialect(r)
    const catOk = !category || r.category === category
    const sp = srcOf(r).spec
    const specOk = !spec || (spec === 'none' ? !sp : sp === spec)
    const kwOk = !kw || r.name.toLowerCase().includes(kw) || ruleName(r.code, r.name).toLowerCase().includes(kw) || r.code.toLowerCase().includes(kw)
    return dialectOk && catOk && specOk && kwOk
  })
  const filtered = !!(q.trim() || category || spec || dialect !== 'all')
  function clearFilters() { setQ(''); setCategory(''); setSpec(''); setDialect('all') }
  // 拦截/警告只数**启用中**的:一条关掉的规则拦不下任何东西,把它算进"拦截规则"
  // 这张牌,牌面上的数字就不是此刻真正生效的那个数。
  const stats = {
    total: rules.length,
    on: rules.filter((r) => r.enabled).length,
    err: rules.filter((r) => r.enabled && r.level === 'error').length,
    warn: rules.filter((r) => r.enabled && r.level === 'warn').length,
    custom: rules.filter((r) => r.kind === 'regex').length,
  }

  // 分级筛选页签。计数放在标签上 —— 一条规范分级下面有几条规则是可以直接看到的事实,
  // 不该要人先点进去才知道。
  const specTabs = (() => {
    const order = catalog.specs || SPECS
    // 计数跟着所选的库走,和下面列表里的分级徽章说的是同一件事。
    const pool = rules.filter(inDialect).map((r) => srcOf(r).spec)
    const tabs = order.map((s) => ({ key: s, n: pool.filter((x) => x === s).length }))
    tabs.push({ key: 'none', n: pool.filter((x) => !x).length })
    return tabs.filter((x) => x.n > 0)
  })()

  async function load() {
    try {
      const list = (await api.reviewRules()) ?? []
      setRules(list)
      ui.setPageSub({ key: 'srSub2', params: { n: list.length } })
    } catch (e) { ui.notifyError(e, t('loadFailed')) }
    try { setCatalog((await api.reviewCatalog()) ?? catalog) } catch { /* keep defaults */ }
    try { setConns((await api.connections()) ?? []) } catch { /* the dialect picker still works */ }
  }
  useMountEffect(() => { void load() })

  // A rule's level and enabled state are the two things an operator actually
  // decides; both write through immediately so there is no "save" step to forget.
  async function setLevel(r: ReviewRule, level: ReviewRule['level']) {
    if (!isAdmin || r.level === level) return
    await patch(r, { level })
  }
  async function toggle(r: ReviewRule) {
    if (!isAdmin) return
    await patch(r, { enabled: !r.enabled })
  }
  async function patch(r: ReviewRule, fields: Partial<ReviewRule>) {
    const body = {
      name: r.name, dialect: r.dialect, category: r.category, level: r.level,
      enabled: r.enabled, params: r.params, message: r.message, sortOrder: r.sortOrder, ...fields,
    }
    try {
      const env = await api.saveReviewRule(r.id, body)
      if (env.code !== 0) { ui.notifyError(envError(env), t('actionFailed')); return }
      setRules((list) => list.map((x) => (x.id === r.id ? { ...x, ...env.data } : x)))
    } catch (e) { ui.notifyError(e, t('actionFailed')) }
  }

  // params are the rule's knobs (a naming pattern, a length cap). Editing them
  // inline keeps the value visible next to the rule it governs.
  const [editing, setEditing] = useState(0)
  const [draftParams, setDraftParams] = useState<Record<string, string>>({})
  const [draftMsg, setDraftMsg] = useState('')
  // 出处可改:规范会改版、章节会挪,运维自己写的规则也该能标出自哪一条。
  const [draftSpec, setDraftSpec] = useState<ReviewRule['spec']>('')
  const [draftRef, setDraftRef] = useState('')
  function openParams(r: ReviewRule) {
    if (!isAdmin) return
    setEditing(editing === r.id ? 0 : r.id)
    setDraftParams(paramsToDraft(r.params || '', specsOf(r)))
    setDraftMsg(r.message || '')
    setDraftSpec(r.spec || '')
    setDraftRef(r.specRef || '')
  }
  async function saveParams(r: ReviewRule) {
    await patch(r, {
      params: draftToParams(draftParams, specsOf(r), r.params || ''), message: draftMsg.trim(),
      spec: draftSpec, specRef: draftRef.trim(),
    })
    setEditing(0)
  }
  const setDraft = (key: string, v: string) => setDraftParams((d) => ({ ...d, [key]: v }))

  // ---- 参数表单 ----
  // 说明书来自 /sql-review/catalog 的 params(后端 review/params_spec.go)。不在其中的内置规则
  // 没有可调参数 —— 编辑区直接这么说,而不是摆一个空文本框让人以为漏配了。
  const specsOf = (r: ReviewRule) => paramSpecsFor(catalog, r)
  const prmKey = (r: ReviewRule, key: string) => (r.kind === 'regex' ? '_regex' : r.code.replace(/\./g, '_')) + '__' + key
  const prmLabel = (r: ReviewRule, key: string) => (te('srPrm.' + prmKey(r, key)) ? t('srPrm.' + prmKey(r, key)) : key)
  const prmHelp = (r: ReviewRule, key: string) => (te('srPrmH.' + prmKey(r, key)) ? t('srPrmH.' + prmKey(r, key)) : '')
  const prmOpt = (o: string) => (te('srPrmOpt_' + o) ? t('srPrmOpt_' + o) : o)
  // 默认值写成人读的样子:列表用逗号连,开关用是/否,枚举用选项名。
  function showValue(ps: ReviewParamSpec, v: unknown): string {
    if (ps.type === 'list') return Array.isArray(v) ? v.join(', ') : String(v ?? '')
    if (ps.type === 'bool') return v === true ? t('srParamYes') : t('srParamNo')
    if (ps.type === 'enum') return prmOpt(String(v ?? ''))
    return String(v ?? '')
  }
  // 编辑框的占位符:留空时真正生效的值(已选库就按该库)。
  const placeholderOf = (ps: ReviewParamSpec) => formatParamValue(ps, effectiveDefault(ps, dialect))
  const listRows = (ps: ReviewParamSpec) => Math.min(6, Math.max(2, placeholderOf(ps).split('\n').length))
  // 某些库上默认值不同 —— 在说明里逐个点出来,免得人以为 Oracle 也按 64 判。
  function dialectNotes(ps: ReviewParamSpec): string[] {
    return Object.entries(ps.dialectDefaults || {}).map(
      ([d, v]) => t('srParamDefaultFor', { d: DIALECT_LABEL[d] || d }) + ': ' + showValue(ps, v),
    )
  }
  // 列表行里的一行摘要:每个参数的生效值,没填的就是默认值。
  function paramSummary(r: ReviewRule): string {
    return effectiveParams(r.params || '', specsOf(r), dialect)
      .map((e) => {
        let v = showValue(e.spec, e.value)
        if (v.length > 48) v = v.slice(0, 46) + '…'
        return prmLabel(r, e.spec.key) + ' ' + v
      })
      .join(' · ')
  }
  function resetParams(r: ReviewRule) {
    for (const ps of specsOf(r)) setDraft(ps.key, '')
  }

  async function removeRule(r: ReviewRule) {
    if (!isAdmin) return
    if (!(await confirmAction(t('srDelConfirm', { name: ruleName(r.code, r.name) })))) return
    try {
      const env = await api.deleteReviewRule(r.id)
      if (env.code !== 0) { ui.notifyError(envError(env), t('actionFailed')); return }
      setRules((list) => list.filter((x) => x.id !== r.id))
    } catch (e) { ui.notifyError(e, t('actionFailed')) }
  }

  // ---- custom rule form ----
  const [formOpen, setFormOpen] = useState(false)
  const [nf, setNf] = useState({ code: '', name: '', dialect: 'all', category: 'dml', level: 'warn', pattern: '', message: '' })
  const patchNf = (fields: Partial<typeof nf>) => setNf((f) => ({ ...f, ...fields }))
  function openForm() {
    setNf({ code: '', name: '', dialect: 'all', category: catalog.categories?.[0] || 'dml', level: 'warn', pattern: '', message: '' })
    setFormOpen(true)
  }
  async function createRule() {
    const f = nf
    if (!f.code.trim() || !f.name.trim() || !f.pattern.trim()) {
      ui.notify(t('srFormIncomplete'), 'error'); return
    }
    try {
      const env = await api.saveReviewRule(0, {
        code: f.code.trim(), name: f.name.trim(), dialect: f.dialect, category: f.category,
        level: f.level as ReviewRule['level'], enabled: true, message: f.message.trim(),
        params: JSON.stringify({ pattern: f.pattern.trim(), mode: 'forbid' }),
      })
      if (env.code !== 0) { ui.notifyError(envError(env), t('actionFailed')); return }
      setRules((list) => [...list, env.data])
      setFormOpen(false)
    } catch (e) { ui.notifyError(e, t('actionFailed')) }
  }

  // ---- manual check ----
  const [checkSQL, setCheckSQL] = useState('')
  const [checkConn, setCheckConn] = useState(0)
  const [checkDialect, setCheckDialect] = useState('mysql')
  const [checking, setChecking] = useState(false)
  const [result, setResult] = useState<ReviewResult | null>(null)
  const groups = useMemo(() => reviewGroups(result), [result])
  const connLabels = [t('srNoInstance'), ...conns.map((c) => `${c.env}-${c.name}`)]
  const picked = conns.find((x) => x.id === checkConn)
  const connLabel = picked ? `${picked.env}-${picked.name}` : t('srNoInstance')
  function setConnLabel(l: string) {
    const c = conns.find((x) => `${x.env}-${x.name}` === l)
    setCheckConn(c ? c.id : 0)
  }

  async function runCheck() {
    if (!checkSQL.trim()) return
    setChecking(true)
    try {
      const r = await api.reviewCheck({
        connectionId: checkConn || undefined,
        dialect: checkConn ? undefined : checkDialect,
        sql: checkSQL,
      })
      setResult(r.result)
    } catch (e) { ui.notifyError(e, t('actionFailed')) } finally { setChecking(false) }
  }

  const ResIcon = result?.passed ? CircleCheck : CircleAlert

  // inline params editor
  function paramField(r: ReviewRule, ps: ReviewParamSpec) {
    const v = draftParams[ps.key] ?? ''
    return (
      <div key={ps.key} className="pfield" data-key={ps.key}>
        <div className="plabel">{prmLabel(r, ps.key)} <span className="pkey">{ps.key}</span></div>
        {ps.type === 'list' ? (
          <textarea
            value={v} onChange={(e) => setDraft(ps.key, e.target.value)} className="pbox" spellCheck={false}
            placeholder={placeholderOf(ps)} rows={listRows(ps)}
          />
        ) : ps.type === 'bool' || ps.type === 'enum' ? (
          <select value={v} onChange={(e) => setDraft(ps.key, e.target.value)} className="in">
            <option value="">{`${t('srParamDefault')}: ${showValue(ps, effectiveDefault(ps, dialect))}`}</option>
            {ps.type === 'bool' ? (
              <>
                <option value="true">{t('srParamYes')}</option>
                <option value="false">{t('srParamNo')}</option>
              </>
            ) : (ps.options || []).map((o) => <option key={o} value={o}>{prmOpt(o)}</option>)}
          </select>
        ) : (
          <input
            value={v} onChange={(e) => setDraft(ps.key, e.target.value)} className="in" spellCheck={false}
            inputMode={ps.type === 'int' ? 'numeric' : undefined} placeholder={placeholderOf(ps)}
          />
        )}
        <div className="phelp">
          {prmHelp(r, ps.key) && <span>{prmHelp(r, ps.key)}</span>}
          {ps.type === 'list' && <span>{t('srParamList')}</span>}
          {ps.type === 'regex' && <span>{t('srParamRegex')}</span>}
          {ps.type !== 'list' && ps.type !== 'bool' && ps.type !== 'enum' && <span>{t('srParamDefault')}: {showValue(ps, effectiveDefault(ps, dialect)) || '—'}</span>}
          {dialectNotes(ps).map((n) => <span key={n}>{n}</span>)}
        </div>
      </div>
    )
  }

  return (
    <div className="scy page">
      <div className="v-head">
        <div className="v-head-l">
          <div className="v-eyebrow">SQL REVIEW RULES</div>
          <div className="v-sub">{t('srSub')}</div>
        </div>
        {isAdmin ? <VButton variant="primary" onClick={openForm}><Plus size={15} />{t('srNewRule')}</VButton>
          : adminUser ? <VButton variant="secondary" data-testid="sr-go-admin" onClick={() => nav.push('/admin/sql-review')}>{t('srManageInAdmin')}</VButton>
            : <span className="roflag">{t('readOnlyPerms')}</span>}
      </div>

      {/* 概览指标。数字上面是它在问什么,下面是答案 —— 四个孤立的数字并排时,
          读的人得先猜每个数字量的是什么。 */}
      <div className="stats">
        <div className="stat">
          <div className="sl">{t('srStatOn')}<span className="sicon accent"><ShieldCheck size={15} /></span></div>
          <div className="sn">{stats.on}</div>
          <div className="sf">{t('srStatOfTotal', { n: stats.total })}</div>
        </div>
        <div className="stat">
          <div className="sl">{t('srStatErr')}<span className="sicon danger"><ShieldAlert size={15} /></span></div>
          <div className="sn danger">{stats.err}</div>
          <div className="sf">{t('srStatErrHint')}</div>
        </div>
        <div className="stat">
          <div className="sl">{t('srStatWarn')}<span className="sicon warn"><TriangleAlert size={15} /></span></div>
          <div className="sn warn">{stats.warn}</div>
          <div className="sf">{t('srStatWarnHint')}</div>
        </div>
        <div className="stat">
          <div className="sl">{t('srStatCustom')}<span className="sicon muted"><WandSparkles size={15} /></span></div>
          <div className="sn muted">{stats.custom}</div>
          <div className="sf">{t('srStatCustomHint')}</div>
        </div>
      </div>

      <div className="wrap">
        {/* ---------------------------------------------- 左:规则库 */}
        <div className="card lib">
          <div className="chead">
            <div className="cic"><SpellCheck size={17} color="var(--accent-text)" /></div>
            <div className="grow"><div className="ct">{t('srLibTitle')}</div><div className="cs">{t('srLibSub')}</div></div>
          </div>

          {/* 第一行:引擎分段器 + 搜索。引擎是"这条规则管哪种库",它和下面那排
              标签不是一组开关,所以分开一行、用完全不同的控件形态。 */}
          <div className="frow">
            <div className="tabs scx">
              {(catalog.dialects ?? []).map((d) => (
                <div key={d} className={clsx('tab', dialect === d && 'active')} onClick={() => setDialect(d)}>
                  {d === 'all' ? t('srAllDialects') : d.toUpperCase()}
                </div>
              ))}
            </div>
            <div className="searchbox">
              <Search size={14} color="var(--text-faint)" />
              <input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t('srSearchPh')} spellCheck={false} />
              {q && <button className="sclear" title={t('apSearchClear')} onClick={() => setQ('')}><X size={13} /></button>}
            </div>
          </div>

          {/* 第二行:标签过滤条。规范分级与规则分类合成一行(原先是两行,叠在一起
              很乱),但中间留一道竖线 —— 它们回答的仍是两个问题:"规范怎么定性"和
              "查的是哪一类写法"。不加分隔就等于说它们是同一组开关。 */}
          <div className="tagbar">
            <span className={clsx('tg', !spec && !category && 'on')} onClick={() => { setSpec(''); setCategory('') }}>{t('srAllRules')}</span>
            <span className="tsep" />
            {specTabs.map((sp) => (
              <span
                key={sp.key} className={clsx('tg', spec === sp.key && 'on')}
                style={spec === sp.key ? { background: specMeta(sp.key).bg, color: specMeta(sp.key).c, borderColor: 'transparent' } : {}}
                onClick={() => setSpec(spec === sp.key ? '' : sp.key)}
              >{t('srSpec_' + sp.key)}<b>{sp.n}</b></span>
            ))}
            <span className="tsep" />
            {(catalog.categories ?? []).map((c) => (
              <span key={c} className={clsx('tg', category === c && 'on')} onClick={() => setCategory(category === c ? '' : c)}>{t('srCat_' + c)}</span>
            ))}
          </div>

          <div className="rules">
            {shown.map((r) => {
              const src = srcOf(r)
              const specs = editing === r.id ? specsOf(r) : []
              return (
                <div key={r.id} className={clsx('rule', !r.enabled && 'off')}>
                  <div className="rmain">
                    <div className="rtop">
                      <span className="rname">{ruleName(r.code, r.name)}</span>
                      <span className="rcode">{r.code}</span>
                      {r.kind === 'regex' && <span className="rkind">{t('srCustom')}</span>}
                      {/* Only SCOPED rules carry a badge. A rule that applies to every
                          dialect saying so on every row was 24 identical chips down the
                          list; the absence is the statement, and the ten dialect-specific
                          rules now stand out instead of drowning. */}
                      {dialectsOf(r).length > 0 && (
                        <span className="rdias">
                          {dialectsOf(r).map((d) => <span key={d} className="rdia">{d.toUpperCase()}</span>)}
                        </span>
                      )}
                    </div>
                    <div className="rmsg">{r.message || paramSummary(r) || '—'}</div>
                    {/* 出处:被这条规则拦下来的人要能回去读原文。没有出处的写"平台内置",
                        免得有人拿平台的默认值当规范原文去引用。 */}
                    <div className="rsrc">
                      <span className="sbadge" style={{ background: specMeta(src.spec).bg, color: specMeta(src.spec).c }}>
                        {t('srSpec_' + (src.spec || 'none'))}
                      </span>
                      {src.specRef && <span className="sref">{src.specRef}</span>}
                    </div>
                  </div>

                  {/* 级别仍然是**可点的**,不是一个只读徽章:改级别是这一页的核心操作
                      之一,没有别的入口。当前级别用语义色实心标出,其余两档留作淡底 ——
                      既看得出现在是哪一档,也还点得动。 */}
                  <div className="lvseg">
                    {LEVELS.map((lv) => (
                      <span
                        key={lv} className={clsx('lv', r.level === lv && 'on', 'lv-' + lv, !isAdmin && 'ro')}
                        onClick={() => setLevel(r, lv)}
                      >{t('srLv_' + lv)}</span>
                    ))}
                  </div>
                  <VSwitch value={r.enabled} disabled={!isAdmin} onChange={() => toggle(r)} />
                  {isAdmin && <span className="rbtn" title={t('srEditParams')} onClick={() => openParams(r)}><Settings2 size={14} /></span>}
                  {isAdmin && r.kind === 'regex' && (
                    <span className="rbtn danger" title={t('delete')} onClick={() => removeRule(r)}>
                      <Trash2 size={13} />
                    </span>
                  )}

                  {/* inline params editor */}
                  {editing === r.id && (
                    <div className="pedit">
                      <div className="fl">{t('srParamsTitle')}</div>
                      {!specs.length ? <div className="pnone">{t('srParamsNone')}</div> : (
                        <>
                          <div className="phint">
                            {t('srParamsHint')}{' '}
                            <button className="preset" onClick={() => resetParams(r)}>{t('srParamReset')}</button>
                          </div>
                          {specs.map((ps) => paramField(r, ps))}
                        </>
                      )}
                      <div className="fl">{t('srMessage')}</div>
                      <input value={draftMsg} onChange={(e) => setDraftMsg(e.target.value)} className="in" placeholder={t('srMessagePh')} />
                      <div className="fl">{t('srSpecLevel')}</div>
                      <select value={draftSpec} onChange={(e) => setDraftSpec(e.target.value as ReviewRule['spec'])} className="in">
                        <option value="">{t('srSpec_none')}</option>
                        {(catalog.specs || SPECS).map((sp) => <option key={sp} value={sp}>{t('srSpec_' + sp)}</option>)}
                      </select>
                      <div className="fl">{t('srSpecRef')}</div>
                      <input value={draftRef} onChange={(e) => setDraftRef(e.target.value)} className="in" placeholder={t('srSpecRefPh')} />
                      <div className="pacts">
                        <VButton height="30px" onClick={() => setEditing(0)}>{t('btnCancel')}</VButton>
                        <VButton variant="primary" height="30px" onClick={() => saveParams(r)}>{t('save')}</VButton>
                      </div>
                    </div>
                  )}
                </div>
              )
            })}
            {!shown.length && (
              <div className="empty">
                <div className="et">{filtered ? t('srNoRulesFiltered') : t('srNoRules')}</div>
                {filtered && <button className="eclear" onClick={clearFilters}>{t('apClearFilters')}</button>}
              </div>
            )}
          </div>
        </div>

        {/* ---------------------------------------------- 右:上线前自查 */}
        <div className="card check">
          <div className="chead">
            <div className="cic ok"><FlaskConical size={16} color="var(--success-text)" /></div>
            <div className="grow"><div className="ct">{t('srCheckTitle')}</div><div className="cs">{t('srCheckSub')}</div></div>
            <span className="toolflag">{t('srSandbox')}</span>
          </div>
          <div className="field">
            <div className="fl">{t('srTarget')}</div>
            <VSelect value={connLabel} onChange={setConnLabel} options={connLabels} searchable height="38px" />
          </div>
          {!checkConn && (
            <div className="field">
              <div className="fl">{t('srDialect')}</div>
              <VSelect value={checkDialect} onChange={setCheckDialect} options={(catalog.dialects ?? []).filter((d) => d !== 'all')} height="38px" />
            </div>
          )}
          <div className="field">
            <div className="fl">SQL</div>
            <textarea value={checkSQL} onChange={(e) => setCheckSQL(e.target.value)} className="sqlbox" spellCheck={false} placeholder={t('srSqlPh')} />
          </div>
          <VButton className="runbtn" variant="primary" height="38px" disabled={checking} onClick={runCheck}>
            <Play size={14} />{checking ? t('srChecking') : t('srRunCheck')}
          </VButton>

          {/* 结果面板。没跑过的时候留一块占位,而不是让按钮下面空着 —— 那块空白
              会让人以为点了没反应。 */}
          {result ? (
            <div className="res">
              <div className={clsx('rsum', result.passed ? 'ok' : 'bad')}>
                <ResIcon size={15} />
                <span className="rst">{result.passed ? t('srPassed') : t('srBlocked')}</span>
                <span className="rcnt">{t('srCounts', { s: result.statements, e: result.errors, w: result.warnings, i: result.infos })}</span>
              </div>
              {/* 按规则分组:一条规则一行 + 命中总数 + 前 5 条示例(大脚本不再逐条画几十万行) */}
              {groups.map((g) => {
                const lm = levelMeta(g.level)
                return (
                  <div key={g.code + g.level} className={clsx('find', g.level)}>
                    <lm.icon size={14} className="fi" />
                    <div className="fbody">
                      <div className="fname">{ruleName(g.code, g.name)} <span className="fcount">{t('srRuleHits', { n: g.count })}</span></div>
                      <div className="fmsg">{localizeText(g.message, g.messageRef)}</div>
                      {g.examples.map((f, i) => (
                        <div key={i} className="fex">
                          <span className="floc">{t('srStmtAt', { n: f.stmt, line: f.line })}</span>
                          {f.message !== g.message && <div className="fmsg">{localizeText(f.message, f.messageRef)}</div>}
                          <div className="fsql">{f.sql}</div>
                        </div>
                      ))}
                      {!!g.more && <div className="fmore">{t('srMoreHits', { n: g.more })}</div>}
                    </div>
                  </div>
                )
              })}
              {!groups.length && <div className="nofind">{t('srNoFindings')}</div>}
            </div>
          ) : <div className="resempty">{t('srResultIdle')}</div>}
        </div>
      </div>

      {/* custom rule modal */}
      {formOpen && (
        <div className="overlay">
          <div className="mask" onClick={() => setFormOpen(false)} />
          <div className="modal">
            <div className="mhead">
              <div className="mic"><Plus size={18} color="var(--accent-text)" /></div>
              <div><div className="mt">{t('srNewRule')}</div><div className="ms">{t('srNewRuleSub')}</div></div>
              <X size={18} className="mx" onClick={() => setFormOpen(false)} />
            </div>
            <div className="mbody">
              <div className="grid2">
                <div><div className="fl">{t('srCode')}</div><input value={nf.code} onChange={(e) => patchNf({ code: e.target.value })} className="in" placeholder="no-select-star" /></div>
                <div><div className="fl">{t('srName')}</div><input value={nf.name} onChange={(e) => patchNf({ name: e.target.value })} className="in" /></div>
                <div><div className="fl">{t('srDialect')}</div><VSelect value={nf.dialect} onChange={(v) => patchNf({ dialect: v })} options={catalog.dialects ?? []} /></div>
                <div><div className="fl">{t('srCategory')}</div><VSelect value={nf.category} onChange={(v) => patchNf({ category: v })} options={catalog.categories ?? []} /></div>
              </div>
              <div>
                <div className="fl">{t('srLevel')}</div>
                <div className="seg">
                  {LEVELS.map((lv) => (
                    <div key={lv} className={clsx('si', nf.level === lv && 'active')} onClick={() => patchNf({ level: lv })}>
                      {t('srLv_' + lv)}
                    </div>
                  ))}
                </div>
              </div>
              <div><div className="fl">{t('srPattern')}</div><input value={nf.pattern} onChange={(e) => patchNf({ pattern: e.target.value })} className="in" placeholder="SQL_NO_CACHE" /></div>
              <div><div className="fl">{t('srMessage')}</div><input value={nf.message} onChange={(e) => patchNf({ message: e.target.value })} className="in" placeholder={t('srMessagePh')} /></div>
              <div className="hint">{t('srPatternHint')}</div>
            </div>
            <div className="mfoot">
              <VButton onClick={() => setFormOpen(false)}>{t('btnCancel')}</VButton>
              <VButton variant="primary" onClick={createRule}>{t('srCreate')}</VButton>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
