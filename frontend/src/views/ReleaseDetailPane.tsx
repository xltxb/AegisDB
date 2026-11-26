// 变更发布页右栏:摊开的那张单的详情,拆成独立卡片。
//
// The pipeline strip is the point of this page: a release is a sequence of
// stages, and what an operator needs to know at a glance is WHICH stage it is
// sitting on and why. So every stage is drawn even before it runs (pending), a
// stage blocked on a person is drawn differently from one doing work, and
// clicking a stage shows the log or the review findings it produced rather than
// a generic "failed".
import { Fragment, useMemo, useState } from 'react'
import clsx from 'clsx'
import {
  Rocket, Check, Loader, Hourglass, CircleX, Minus, Play,
  ShieldCheck, ClipboardCheck, DatabaseBackup, Terminal, Zap, Search, Bell, CircleAlert, Copy,
  TriangleAlert, CircleDashed, FileCode, ScrollText, type LucideIcon,
} from 'lucide-react'
import VButton from '@/components/common/VButton'
import { localizeText } from '@/api/http'
import { renderPreText } from '@/lib/ruleText'
import { groupPrecheckIssues, formatStmtRuns } from '@/lib/precheckGroup'
import { oscItemText } from '@/lib/oscText'
import { reviewRuleName } from '@/lib/builtinNames'
import { renderRelLog, renderRelErr } from '@/lib/relLogText'
import { copyText } from '@/lib/clipboard'
import { highlightSqlHtml } from '@/lib/sqlHighlight'
import { reviewGroups } from '@/lib/reviewGroups'
import { releaseExecutor, type ReleaseExecutor } from '@/lib/releaseExecutor'
import { useUIStore } from '@/stores/ui'
import { useEnvTierStore } from '@/stores/envtier'
import { useI18n } from '@/hooks/useI18n'
import { useUpdateEffect } from '@/hooks/effects'
import type {
  Release, ReleaseDetail, ReleaseStage, ReleaseStatus, ReviewResult, Precheck, PrecheckIssue, EnvType, StepItem,
} from '@/types'
import './ReleasesView.scoped.css'

// 环境胶囊的颜色跟分层走,和库树、审批列表、分层页同一个来源(envtier.dotFor)。
// 分层表没加载到时一律中性色 —— 不替查不到的分层断言它是生产还是测试。
export function useEnvTone() {
  const dotFor = useEnvTierStore((s) => s.dotFor)
  useEnvTierStore((s) => s.tiers) // dotFor 读的是它:分层表到了要重画
  return (tierCode: string): EnvType => (tierCode ? dotFor(tierCode) as EnvType : 'muted')
}

// 执行变更人:执行阶段点「确认执行」的人;自动执行阶段没经人手写「自动执行」。
export function executorText(e: ReleaseExecutor, t: (k: string) => string): string {
  const parts = [...e.names]
  if (e.auto) parts.push(t('rlExecAuto'))
  return parts.join('、')
}

// ---- stage presentation ----
const stageIcon: Record<string, LucideIcon> = {
  review: ShieldCheck, approve: ClipboardCheck, backup: DatabaseBackup,
  execute: Terminal, auto_execute: Zap, verify: Search, manual: Hourglass, notify: Bell,
}
// 自动执行在分层不再允许时退回成执行闸(见后端 stageExecute),按钮与执行变更相同
const isExecStage = (ty: string) => ty === 'execute' || ty === 'auto_execute'
function statusMeta(s: string) {
  switch (s) {
    case 'success': return { icon: Check, c: 'var(--success-text)', bg: 'var(--success-subtle)', spin: false }
    case 'running': return { icon: Loader, c: 'var(--accent-text)', bg: 'var(--accent-subtle)', spin: true }
    case 'waiting': return { icon: Hourglass, c: 'var(--warning-text)', bg: 'var(--warning-subtle)', spin: false }
    case 'failed': return { icon: CircleX, c: 'var(--danger-text)', bg: 'var(--danger-subtle)', spin: false }
    case 'skipped': return { icon: Minus, c: 'var(--text-faint)', bg: 'var(--surface-sunken)', spin: false }
    case 'aborted': return { icon: CircleX, c: 'var(--text-muted)', bg: 'var(--surface-sunken)', spin: false }
    default: return { icon: CircleDashed, c: 'var(--text-muted)', bg: 'var(--surface-sunken)', spin: false }
  }
}
// The connector between two stages reports what has actually flowed: solid
// behind finished work, animated into the stage running right now, flat ahead
// of it. Computed from the PAIR, which is why it cannot live in the template.
function connClass(stages: ReleaseStage[], i: number): StepItem['conn'] {
  const prevDone = ['success', 'skipped'].includes(stages[i - 1]?.status)
  if (!prevDone) return ''
  return stages[i].status === 'running' ? 'live' : 'done'
}
function duration(st: ReleaseStage) {
  if (!st.startedAt) return ''
  const end = st.finishedAt ? new Date(st.finishedAt).getTime() : Date.now()
  const ms = end - new Date(st.startedAt).getTime()
  if (ms < 1000) return ms + 'ms'
  return (ms / 1000).toFixed(1) + 's'
}
function fmt(s: string | null) { return s ? new Date(s).toLocaleString('en-GB') : '—' }

// A review stage carries its findings as JSON so a blocked release can still be
// explained after the rule library has moved on.
function parseFindings(raw: string | undefined): ReviewResult | null {
  if (!raw) return null
  try { return JSON.parse(raw) as ReviewResult } catch { return null }
}
function issueCls(level: string) {
  return level === 'error' ? 'err' : level === 'warn' ? 'warn' : 'info'
}
function riskCls(r: string) { return r === 'high' ? 'bad' : r === 'mid' ? 'warn' : 'ok' }

const PRE_FOLD = 6


// 详情页脚本最多渲染这么多:行数与字数各一道,先到先截(单行超长的脚本也截得住)。
const SCRIPT_VIEW_MAX_LINES = 5000
const SCRIPT_VIEW_MAX_CHARS = 300_000

function countLines(text: string): number {
  if (!text) return 0
  let n = 1
  for (let i = text.indexOf('\n'); i >= 0; i = text.indexOf('\n', i + 1)) n++
  return text.endsWith('\n') ? n - 1 : n
}

/** 脚本开头用来显示的一段。截在行尾;一行就超过字数上限时只能截在行中。 */
function scriptHead(text: string): { text: string; truncated: boolean } {
  let end = -1
  for (let k = 0, i = text.indexOf('\n'); i >= 0; i = text.indexOf('\n', i + 1)) {
    if (i > SCRIPT_VIEW_MAX_CHARS) break
    if (++k === SCRIPT_VIEW_MAX_LINES) { end = i; break }
    end = i
  }
  if (text.length <= SCRIPT_VIEW_MAX_CHARS && (end < 0 || countLines(text) <= SCRIPT_VIEW_MAX_LINES)) {
    return { text, truncated: false }
  }
  return { text: end > 0 ? text.slice(0, end) : text.slice(0, SCRIPT_VIEW_MAX_CHARS), truncated: true }
}

interface Props {
  open: ReleaseDetail
  openStage: number
  codeTab: 'sql' | 'log'
  onCodeTab: (tab: 'sql' | 'log') => void
  onToggleStage: (id: number) => void
  onAbort: (r: Release) => void
  onContinue: (r: Release, st: ReleaseStage) => void
}

export default function ReleaseDetailPane({ open, openStage, codeTab, onCodeTab, onToggleStage, onAbort, onContinue }: Props) {
  const { t, te, locale } = useI18n()
  const ui = useUIStore.getState() // 只用动作,不读状态
  const envTone = useEnvTone()

  // 预检结论按读者语言显示;旧单没有 ref,回落到存下的中文原串(lib/ruleText)。
  const preI18n = { t, te }
  // 内置规则名按读者语言显示;改过名的原样显示(lib/builtinNames reviewRuleName)。
  const zhText = (k: string) => (te(k, 'zh') ? t(k, {}, { locale: 'zh' }) : undefined)
  const ruleName = (code: string, stored: string) => reviewRuleName(code, stored, t, zhText)
  // 阶段名是按类型派生的规范中文(后端 stageTypeLabel,进记录所以语言稳定),界面按 plType_* 显示。
  // 与该类型的规范中文对不上的(派生之前的老快照里自定义的名字)是用户数据,原样显示。
  const stageName = (type: string, stored: string) => {
    const key = 'plType_' + type
    return !stored || zhText(key) === stored ? t(key) : stored
  }
  const stageLabel = (st: ReleaseStage) => stageName(st.type, st.name)
  // 阶段日志与错误条按读者语言渲染;老单没有 refs,显示存下的中文原串(lib/relLogText)。
  const logI18n = { t, te, locale, stage: stageName, rule: ruleName }
  const riskText = (r: string) => (r === 'high' ? t('highTag') : r === 'mid' ? t('med') : t('low'))
  // 在线变更前置检查的阻塞 / 警告带着引擎的原话(表名、行数等细节):中文界面照原话,其它语言按 code 翻
  const preMsg = (i: PrecheckIssue) => {
    const code = i.ref?.args?.code
    if (code && (i.ref?.code === 'preOscBlocked' || i.ref?.code === 'preOscWarn')) {
      const text = oscItemText({ code, reason: i.ref.args?.reason || '' }, locale, t, te)
      return t(i.ref.code === 'preOscBlocked' ? 'preText.preOscBlocked' : 'preText.preOscWarn', { reason: text })
    }
    return renderPreText(i.ref, i.message, preI18n)
  }
  const preNote = (p: Precheck) => {
    const parts = (p.noteRefs || []).map((r) => renderPreText(r, '', preI18n))
    // 任何一条认不出就整行回落到中文原串,不拼半句。
    return parts.length && parts.every(Boolean) ? parts.join(' ') : (p.note || '')
  }

  const stages = open.stages ?? []
  const stageObj = stages.find((s) => s.id === openStage) || null
  // 只解析一次:每次渲染都整段 JSON.parse 的话,老的大单有几十 MB。
  const openReview = useMemo(() => parseFindings(stageObj?.findings), [stageObj?.findings])
  const openGroups = useMemo(() => reviewGroups(openReview), [openReview])
  const executor = releaseExecutor(open.stages)
  const executorLabel = executorText(executor, t)

  // 步骤条的节点。每个节点自带与前一节点之间连线的状态,模板里不再成对地算。
  // 运行中阶段的时长按"数据到的那一刻"算,跟着轮询走,不跟着每次重渲染跳。
  const steps = useMemo<StepItem[]>(() => (open.stages ?? []).map((st, i, all) => ({
    stage: st,
    label: stageLabel(st),
    status: st.status as ReleaseStatus,
    duration: duration(st),
    conn: i > 0 ? connClass(all, i) : '',
    // stageLabel 只依赖语言,t 随语言换引用
    // eslint-disable-next-line react-hooks/exhaustive-deps
  })), [open, t])

  // ---- 脚本 / 日志查看器 ----
  const sqlText = open.sql || ''
  // 大脚本只渲染开头一段(#161):整段高亮是几百万个节点,打开就卡十几秒,执行中状态灯的动画还会
  // 每一帧重绘整块代码。行数统计与「复制」仍用完整脚本。
  const sqlView = useMemo(() => scriptHead(sqlText), [sqlText])
  const sqlHtml = useMemo(() => highlightSqlHtml(sqlView.text), [sqlView])
  const logText = useMemo(
    () => (stageObj?.log ? renderRelLog(stageObj.logRefs, stageObj.log, logI18n) : ''),
    // logI18n 只依赖语言,t 随语言换引用
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [stageObj?.log, stageObj?.logRefs, t],
  )
  const codeText = codeTab === 'sql' ? sqlText : logText
  // 行号单独一列(不进 <pre> 的文本):复制、选中、断言拿到的都是原文,不夹行号。
  const codeLines = useMemo(() => countLines(codeText), [codeText])
  const shownLines = codeTab === 'sql' && sqlView.truncated ? countLines(sqlView.text) : codeLines
  const gutterText = useMemo(() => Array.from({ length: shownLines }, (_, i) => i + 1).join('\n'), [shownLines])

  // 上游单号是对端系统(发布平台)那边的编号,跨系统对账、找人时要整串报给对方 —— 给个复制。
  async function copy(text: string) {
    if (await copyText(text)) ui.notify(t('copied'), 'success')
    else ui.notify(t('actionFailed'), 'error')
  }

  // 收单检查。存在单子上的是一串 JSON —— 解析不出来就当没有,不去猜它的形状。
  //
  // null 有两种来历,面板上要分开说:0036 之前的老单子没有这一栏,以及这次检查一条都
  // 没做成。两者都**不是**"检查通过" —— 一块什么都不显示的区域会被读成没问题。
  const precheck = useMemo<Precheck | null>(() => {
    const raw = open.precheck
    if (!raw) return null
    try { return JSON.parse(raw) as Precheck } catch { return null }
  }, [open.precheck])
  const issues = precheck?.issues ?? []

  // 这份结果覆盖了多少 —— 读它的人最需要的一句话。
  // 只说"0 个错误"会让人以为每条语句都验过了,而 DDL 根本没法在不执行的前提下验。
  const precheckCoverage = precheck ? t('rlPreCoverage', { checked: precheck.syntaxChecked, skipped: precheck.syntaxSkipped }) : ''

  // 对象结论的来源。cache 时把那份缓存的时间一并说出来:一份三天前的缓存说"表不存在",
  // 很可能只是那张表是昨天建的。
  const precheckSource = !precheck ? ''
    : precheck.objectSource === 'cache' ? t('rlPreSrcCache', { at: precheck.cacheSyncedAt ? fmt(precheck.cacheSyncedAt) : '—' })
      : precheck.objectSource === 'live' ? t('rlPreSrcLive')
        : t('rlPreSrcNone')

  // 风险提示块的色调:有错误 = 危险,有警告或根本没有检查结果 = 警示(没结果不等于通过),
  // 都没有 = 通过。
  const precheckLevel = !precheck ? 'lv-warn'
    : precheck.errors ? 'lv-err'
      : precheck.warnings || issues.some((i) => i.level === 'warn') ? 'lv-warn'
        : 'lv-ok'

  // 同一结论的条目并成一行(第 1–5、12 条 · 共 6 条),错误排在前面;超过 PRE_FOLD 行先收起。
  // 几百条 DDL 的脚本原来是几百行一模一样的"未做语法检查"(2026-11-23)。
  const [preAll, setPreAll] = useState(false)
  const precheckGroups = useMemo(
    () => groupPrecheckIssues(precheck?.issues || [], preMsg),
    // preMsg 只依赖语言,t 随语言换引用
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [precheck, t],
  )
  const shownPreGroups = preAll ? precheckGroups : precheckGroups.slice(0, PRE_FOLD)
  const stmtRuns = (ns: number[]) => formatStmtRuns(ns, locale.startsWith('zh') ? '、' : ', ')
  useUpdateEffect(() => { setPreAll(false) }, [open.id])

  return (
    <div className="detail">
      {/* ① 头部 + 关键信息 */}
      <section className="card hcard">
        <div className="dhead">
          <div className="dic"><Rocket size={18} /></div>
          <div className="grow">
            <div className="dt">
              <span className="dno">{open.relNo}</span>
              <span className="dtitle">{open.title}</span>
              {open.changeType && <span className={clsx('ctb big', open.changeType)}>{open.changeType.toUpperCase()}</span>}
            </div>
            <div className="ds">{open.pipelineName} · {fmt(open.createdAt)}</div>
          </div>
          <div className="dbadges">
            {open.env && <span className={clsx('envcap', envTone(open.tierCode))} title={t('rlKEnv')}>{open.env.toUpperCase()}</span>}
            <span className={clsx('ostat', open.status)}>{t('rlSt_' + open.status)}</span>
            <span className={clsx('pill', riskCls(open.risk))}>{t('rlRisk')} {riskText(open.risk)}</span>
            {(open.status === 'pending' || open.status === 'waiting') && (
              <VButton height="32px" onClick={() => onAbort(open)}>
                {t('rlAbort')}
              </VButton>
            )}
          </div>
        </div>

        {/* 关键信息:审批、执行、事后对账的人先要知道的几件事 —— 改哪个环境的哪个库、
            对应上游哪张单、谁从哪儿提的。 */}
        <div className="kinfo" data-testid="rl-keyinfo">
          <div className="kv" data-k="env">
            <span className="k">{t('rlKEnv')}</span>
            <span className="v"><b className={clsx('envchip', envTone(open.tierCode))}>{open.env || '—'}</b>{open.tierCode && <span className={clsx('tag tier', envTone(open.tierCode))} title={t('rlKTier')}>{open.tierCode.toUpperCase()}</span>}</span>
          </div>
          <div className="kv" data-k="inst">
            <span className="k">{t('rlKInst')}</span>
            <span className="v mono strong">{open.instance || '—'}{open.engine && <span className="dim"> · {open.engine}</span>}</span>
          </div>
          <div className="kv" data-k="db">
            <span className="k">{t('rlKDb')}</span>
            <span className="v mono strong">{open.database || '—'}</span>
          </div>
          <div className="kv" data-k="project">
            <span className="k">{t('rlKProject')}</span>
            <span className="v strong">{open.projectName || '—'}</span>
          </div>
          <div className="kv" data-k="creator">
            <span className="k">{t('rlKCreator')}</span>
            <span className="v strong">{open.creator || '—'}</span>
          </div>
          <div className="kv" data-k="executor">
            <span className="k">{t('rlKExecutor')}</span>
            {executorLabel ? <span className="v strong">{executorLabel}</span>
              : executor.waiting ? <span className="v dim">{t('rlExecWaiting')}</span>
                : <span className="v dim">—</span>}
          </div>
          <div className="kv" data-k="source">
            <span className="k">{t('rlKSource')}</span>
            <span className="v strong">
              {open.source === 'api' ? <><span className="api">API</span>{' '}{open.clientName || '—'}</> : t('rlKConsole')}
            </span>
          </div>
          <div className="kv" data-k="pipeline">
            <span className="k">{t('rlPipeline')}</span>
            <span className="v strong">{open.pipelineName || '—'}</span>
          </div>
          <div className="kv wide" data-k="ref">
            <span className="k">{t('rlKRef')}</span>
            {open.externalRef ? (
              <span className="v mono strong nowrap">
                <span className="refv">{open.externalRef}</span>
                <button type="button" className="kcopy" title={t('copy')} aria-label={t('copy')} onClick={() => copy(open.externalRef!)}><Copy size={12} /></button>
              </span>
            ) : <span className="v dim">—</span>}
          </div>
          <div className="kv wide" data-k="time">
            <span className="k">{t('rlKTime')}</span>
            <span className="v mono"><span className="nw">{fmt(open.createdAt)}</span>{open.finishedAt && <><span className="dim">→</span><span className="nw">{fmt(open.finishedAt)}</span></>}</span>
          </div>
          {open.reason && <div className="reason">{t('reason')}: {open.reason}</div>}
        </div>
      </section>

      {/* ② 流水线步骤条 */}
      <section className="card">
        <div className="ctitle"><Rocket size={14} />{t('rlCardPipeline')}</div>
        <div className="pipe scx">
          {steps.map((s, i) => {
            const sm = statusMeta(s.status)
            const TypeIcon = stageIcon[s.stage.type] || Terminal
            return (
              <Fragment key={s.stage.id}>
                {i > 0 && <div className={clsx('conn', s.conn)} />}
                <div
                  className={clsx('node', s.status, openStage === s.stage.id && 'sel')}
                  role="button" tabIndex={0} onClick={() => onToggleStage(s.stage.id)}
                  onKeyDown={(e) => { if (e.key === 'Enter') onToggleStage(s.stage.id) }}
                >
                  <div className={clsx('dot', s.status === 'running' && 'halo')} style={{ background: sm.bg, color: sm.c }}>
                    <sm.icon size={17} className={clsx(sm.spin && 'spin')} />
                  </div>
                  <div className="nname">
                    <TypeIcon size={12} />{' '}
                    {s.label}
                  </div>
                  <div className="nst" style={{ color: sm.c }}>{t('rlSt_' + s.status)}</div>
                  <div className="ndur">{s.duration || '—'}</div>
                </div>
              </Fragment>
            )
          })}
        </div>
        {open.error && <div className="errbar"><CircleAlert size={14} />{renderRelErr(open.errorRef, open.error, logI18n)}</div>}
      </section>

      {/* ③ 收单检查 / 风险提示:收单那一刻算的,不是现在算的。
          它**不拦发布** —— 有 error 的单子照样会往下走,卡不卡由流程的阶段决定。 */}
      <section className={clsx('card pbox', precheckLevel)}>
        <div className="pbhead">
          <span className="pbic">{precheckLevel === 'lv-ok' ? <ShieldCheck size={15} /> : <TriangleAlert size={15} />}</span>
          <span className="pbt">{t('rlPrecheck')}</span>
          {precheck && (
            <>
              <span className={clsx('pcnt err', !precheck.errors && 'zero')}>{t('rlPreErr', { n: precheck.errors })}</span>
              <span className={clsx('pcnt warn', !precheck.warnings && 'zero')}>{t('rlPreWarn', { n: precheck.warnings })}</span>
              <span className="pdim">{precheckCoverage}</span>
            </>
          )}
        </div>
        {/* 没有结果时说清是哪一种"没有",不留白 —— 空白会被读成通过 */}
        {!precheck ? <div className="pnone">{t('rlPreAbsent')}</div> : (
          <>
            <div className="psrc">{precheckSource}</div>
            {precheck.note && <div className="psrc">{preNote(precheck)}</div>}
            {!issues.length && <div className="pnone">{t('rlPreNoIssue')}</div>}
            {shownPreGroups.map((g, k) => (
              <div key={k} className={clsx('pitem', issueCls(g.level))}>
                <span className="pstmt">{g.stmts.some((n) => n > 0) ? t('rlPreStmt', { n: stmtRuns(g.stmts) }) : '·'}</span>
                {g.object && <span className="pobj">{g.object}</span>}
                <span className="pmsg">{g.message}</span>
                {g.stmts.length > 1 && <span className="pgcnt">{t('rlPreGroupN', { n: g.stmts.length })}</span>}
                {g.source && <span className="psrctag">{g.source === 'cache' ? t('rlPreTagCache') : t('rlPreTagLive')}</span>}
              </div>
            ))}
            {precheckGroups.length > PRE_FOLD && (
              <button type="button" className="pmore" onClick={() => setPreAll(!preAll)}>
                {preAll ? t('rlPreLess') : t('rlPreMore', { n: precheckGroups.length - PRE_FOLD })}
              </button>
            )}
          </>
        )}
      </section>

      {/* ④ 阶段详情 + 脚本 / 日志查看器 */}
      <section className="card sbox">
        <div className="sbhead">
          {stageObj ? (
            <>
              <span className="sbt">{stageLabel(stageObj)}</span>
              <span className={clsx('pill', stageObj.status)}>{t('rlSt_' + stageObj.status)}</span>
              {stageObj.approvalNo && <span className="apno">{stageObj.approvalNo}</span>}
              {/* 两种人工闸共用一个"继续"入口:manual 是流程里配置的确认点,
                  execute 是内置执行闸(必须有人点击才落库)。按钮文案区分语义。 */}
              {(stageObj.type === 'manual' || isExecStage(stageObj.type)) && stageObj.status === 'waiting' && (
                <VButton variant="primary" height="30px" onClick={() => onContinue(open, stageObj)}>
                  <Play size={13} />{isExecStage(stageObj.type) ? t('rlConfirmExec') : t('rlContinue')}
                </VButton>
              )}
            </>
          ) : <span className="sbhint">{t('rlPickStage')}</span>}
        </div>

        {/* 深色 IDE 风格:亮暗主题下都一样,代码与日志读起来不随页面换底色 */}
        <div className="ide">
          <div className="ide-bar">
            <div className="ide-tabs" role="tablist">
              <button type="button" role="tab" className={clsx('ide-tab', codeTab === 'sql' && 'on')} aria-selected={codeTab === 'sql'} onClick={() => onCodeTab('sql')}>
                <FileCode size={13} />{t('rlTabSql')}
              </button>
              <button type="button" role="tab" className={clsx('ide-tab', codeTab === 'log' && 'on')} aria-selected={codeTab === 'log'} onClick={() => onCodeTab('log')}>
                <ScrollText size={13} />{t('rlTabLog')}
              </button>
            </div>
            {codeLines > 0 && <span className="ide-meta">{t('rlLines', { n: codeLines })}</span>}
            <button type="button" className="ide-copy" disabled={!codeText} title={t('copy')} onClick={() => copy(codeText)}>
              <Copy size={13} />{t('copy')}
            </button>
          </div>
          {codeText ? (
            <div className="ide-body">
              <div className="ide-gutter" aria-hidden="true">{gutterText}</div>
              {codeTab === 'sql'
                ? <pre key="sql" className="ide-code sqlsrc" dangerouslySetInnerHTML={{ __html: sqlHtml }} />
                : <pre key="log" className="ide-code log">{logText}</pre>}
            </div>
          ) : null}
          {codeText && codeTab === 'sql' && sqlView.truncated && (
            <div className="ide-trunc" data-testid="rl-sql-truncated">{t('rlSqlTruncated', { total: codeLines, shown: shownLines })}</div>
          )}
          {codeText ? null : (
            <div className="ide-empty">
              {codeTab === 'sql' ? t('rlScriptRef') : stageObj ? t('rlNoLog') : t('rlPickStage')}
            </div>
          )}
        </div>

        {/* review findings */}
        {openReview && (
          <>
            <div className="fsum">
              {t('srCounts', {
                s: openReview.statements, e: openReview.errors,
                w: openReview.warnings, i: openReview.infos,
              })}
            </div>
            {/* 按规则分组:一条规则一行 + 命中总数 + 前 5 条示例 */}
            {openGroups.map((g) => (
              <div key={g.code + g.level} className={clsx('find', g.level)}>
                <div className="fname">{ruleName(g.code, g.name)} <span className="fcount">{t('srRuleHits', { n: g.count })}</span></div>
                <div className="fmsg">{localizeText(g.message, g.messageRef)}</div>
                {g.examples.map((fd, i) => (
                  <div key={i} className="fex">
                    <span className="floc">{t('srStmtAt', { n: fd.stmt, line: fd.line })}</span>
                    {fd.message !== g.message && <div className="fmsg">{localizeText(fd.message, fd.messageRef)}</div>}
                    <div className="fsql">{fd.sql}</div>
                  </div>
                ))}
                {!!g.more && <div className="fmore">{t('srMoreHits', { n: g.more })}</div>}
              </div>
            ))}
          </>
        )}
      </section>
    </div>
  )
}
