import { useEffect, useRef, useState } from 'react'
import type { CSSProperties, KeyboardEvent as ReactKeyboardEvent, PointerEvent as ReactPointerEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import {
  Activity, ClipboardPaste, Command, Download, LogOut, Maximize2, Minimize2,
  PanelLeftOpen, PanelRightOpen, Plug, RotateCw, ShieldAlert, Table2,
} from 'lucide-react'
import clsx from 'clsx'
import { connectionsQueryOptions, connectionSchemaQueryOptions } from '@/api/modules/connections'
import { terminalApi } from '@/api/modules/terminal'
import { CODE_MFA_REQUIRED } from '@/api/codes'
import { translateMetaSql, translateDescribe, type NoticeRef } from '@/lib/metaCommand'
import { classifyExecEnvelope, type ExecEnvelope } from '@/lib/execOutcome'
import { renderRule as renderRuleIn, renderOutText } from '@/lib/ruleText'
import { ANSI, c, isSelect, synthTable, buildTable, renderTable, renderVertical } from '@/lib/sqlResult'
import { Transcript } from '@/lib/transcript'
import { countStatements } from '@/lib/sqlCount'
import { needsArming, slotFromEvent, slotLabel, snippetPreview, snippetSubmitText } from '@/lib/snippet'
import { useTerminalSession, type WsMessage } from '@/hooks/useTerminalSession'
import { useEnvTier } from '@/hooks/useEnvTier'
import { useSnippets, snippetsBySlot } from '@/hooks/useSnippets'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import { Badge } from '@/components/common/Badge'
import { Button } from '@/components/common/Button'
import { Modal } from '@/components/common/Modal'
import { DbTree } from './DbTree'
import { ResultGrid, type GridResult } from './ResultGrid'
import { SourceViewer, type SourceTarget } from './SourceViewer'
import { SnippetModal } from './SnippetModal'
import { PasteModal } from './PasteModal'
import { Completion, META_COMMANDS, type AcItem } from './Completion'
import type { Connection, ConnectionSchema, RuleRef, SchemaDB } from '@/types'
import '@xterm/xterm/css/xterm.css'
import { engineFamily } from '@/lib/engines'

// ---- 面板宽度的边界 ----
// 存像素而不是百分比:树和执行上下文装的是**定宽的东西**(实例名、字段标签),
// 它们需要的宽度不随窗口变;而结果表要占的是"剩下的一半",所以那边存比例。
const TREE_W_KEY = 'aegis_tree_w'
const INSP_W_KEY = 'aegis_insp_w'
const TREE_MIN = 180, TREE_MAX = 560
const INSP_MIN = 240, INSP_MAX = 620
// 中间那栏的下限。拖动可以把两侧拉宽,但不能把终端挤到开始逐字断行。
const TERM_MIN = 420

const GRID_H_KEY = 'aegis_termgrid_h'
const GRID_H_DEFAULT = 42, GRID_H_MIN = 15, GRID_H_MAX = 80
const GRID_VIEW_KEY = 'aegis_termgrid'

/** 高危分层上的快捷键要在这个窗口内按第二次才执行。 */
const ARM_MS = 4000
/** 补全候选最多给这么多条:再多就不是"选一个",是又读一份清单。 */
const AC_MAX = 8

const readPx = (k: string) => {
  const v = Number(localStorage.getItem(k))
  return Number.isFinite(v) && v > 0 ? v : null
}
const clampH = (v: number) => Math.min(GRID_H_MAX, Math.max(GRID_H_MIN, v))

type Side = 'tree' | 'insp'

/**
 * 终端页:数据库树 · 终端 · 执行上下文。
 *
 * 单会话(每次连一台实例),不是多标签 —— 判断"进入生产要不要再问一次"因此按
 * **实例**算,这是 Vue 版"每个标签页问一次"在这个结构下的等价物。
 */
export default function TerminalPage() {
  const { t, i18n } = useTranslation()
  // 动态 key(服务端给的 notice id、分层代码)要一个宽松签名的 t。
  const tr = t as unknown as (k: string, p?: Record<string, unknown>) => string
  const ruleI18n = { t: tr, te: (k: string) => i18n.exists(k) }
  const renderRule = (ref: RuleRef | undefined, canonical: string) => renderRuleIn(ref, canonical, ruleI18n)
  const renderOut = (ref: RuleRef | undefined, canonical: string) => renderOutText(ref, canonical, ruleI18n)

  const { data: conns } = useQuery(connectionsQueryOptions())
  const { data: snipList } = useSnippets()
  const envtier = useEnvTier()
  const notify = useUIStore((s) => s.notify)
  const me = useAuthStore((s) => s.me)

  const list = (conns ?? []) as Connection[]
  const [connId, setConnId] = useState(0)
  const [database, setDatabase] = useState('')
  const conn = list.find((x) => x.id === connId) ?? null
  const tier = conn ? envtier.tierOf(conn.env) : undefined
  // 判据是**分层的 dangerBanner**,不是名字叫不叫 prod:第二套生产环境和第一套
  // 一样危险,按名字挑会让人最不熟悉的那些集群拿到最弱的警告。
  const danger = !!tier?.dangerBanner
  // 解析不出分层的按中档处理:没有分层意味着管控级别**未知**,不是无害。
  const safetyLevel = !conn ? '' : danger ? 'danger' : (!tier || tier.requireMfa) ? 'warn' : 'ok'

  // ---- 会话内的一次性状态 ----
  const transcript = useRef(new Transcript())
  const [canExportLog, setCanExportLog] = useState(false)
  const pendingFollow = useRef<{ titleKey: string; sql: string } | null>(null)
  const pendingEmptyNotice = useRef<NoticeRef | null>(null)
  const pendingVertical = useRef(false)
  const expandedMode = useRef(false)
  const pendingSql = useRef('')
  const lastExec = useRef({ sql: '', reason: '' })
  const armedSlot = useRef(0)
  const armTimer = useRef(0)

  // ---- 弹窗 ----
  const [intercept, setIntercept] = useState<{ sql: string; rule: string } | null>(null)
  const [reason, setReason] = useState('')
  const [mfa, setMfa] = useState<{ sql: string; reason: string } | null>(null)
  const [mfaCode, setMfaCode] = useState('')
  const [mfaErr, setMfaErr] = useState('')
  const [paste, setPaste] = useState<{ open: boolean; text: string }>({ open: false, text: '' })
  const [snipOpen, setSnipOpen] = useState(false)
  const [src, setSrc] = useState<SourceTarget | null>(null)
  // 进入生产实例的红色确认。终端里那行红字是**被动**的,人扫一眼就滑过去了;
  // 这个弹窗挡在路中间。每台实例问一次。
  const [dangerAck, setDangerAck] = useState<Record<number, boolean>>({})
  const dangerOpen = danger && !!connId && !dangerAck[connId]

  // ---- 布局 ----
  const gridRef = useRef<HTMLDivElement | null>(null)
  const [treeW, setTreeW] = useState<number | null>(readPx(TREE_W_KEY))
  const [inspW, setInspW] = useState<number | null>(readPx(INSP_W_KEY))
  const [treeCollapsed, setTreeCollapsed] = useState(false)
  const [inspCollapsed, setInspCollapsed] = useState(false)
  const [zen, setZen] = useState(false)
  const [resizing, setResizing] = useState<Side | null>(null)
  const [gridView, setGridView] = useState(localStorage.getItem(GRID_VIEW_KEY) === '1')
  const [gridH, setGridH] = useState(clampH(Number(localStorage.getItem(GRID_H_KEY)) || GRID_H_DEFAULT))
  const [dragging, setDragging] = useState(false)
  const [result, setResult] = useState<GridResult | null>(null)

  // ---- 补全 ----
  const [ac, setAc] = useState<{ items: AcItem[]; index: number; top: number; word: string }>(
    { items: [], index: 0, top: 40, word: '' },
  )
  const acOpen = ac.items.length > 0

  const bySlot = snippetsBySlot(snipList)

  // ---------------------------------------------------------------- 输出
  /** 打一行:终端上一份,会话日志里一份。日志在这里记,而不是事后从 xterm 里读回来
   *  —— 渲染器的回滚有上限,也不带时间戳,到那时文本的结构已经没了。 */
  function out(line = '') {
    transcript.current.output(line, new Date())
    setCanExportLog(true)
    session.term.current?.write(line + '\r\n')
  }
  const outLines = (arr: string[]) => arr.forEach((l) => out(l))
  /**
   * 打在提示符**上方**,并同样记进会话日志。
   *
   * 两件事必须一起做:屏幕上出现过的,导出的文件里也要有 —— 否则"上膛提示""粘贴
   * 改走弹窗"这类只在屏幕上说过一次的话,事后复盘时就查无此事。
   *
   * 编辑器忙的时候直接打:那时提示符已经不在屏幕上了,printAbove 的倒卷会擦掉
   * 输出而不是输入。
   */
  function noticeLines(lines: string[]) {
    const now = new Date()
    for (const l of lines) transcript.current.output(l, now)
    setCanExportLog(true)
    const ed = session.editor.current
    if (!ed || ed.running) for (const l of lines) session.term.current?.write(l + '\r\n')
    else ed.printAbove(lines)
  }
  const notice = (line: string) => noticeLines([line])
  const msLabel = (ms?: number) => (ms && ms > 0 ? String(ms) : '<1')
  const truncHint = (n: number) => c(ANSI.gray, tr('resultTruncatedHint', { n, bs: '\\' }))

  // ---------------------------------------------------------------- 提示符
  // 提示符里写着当前的库,于是回滚里的每一条命令都记着它是对哪个库跑的。
  const promptText = () => {
    const name = conn?.name ?? 'aegis'
    const head = database
      ? c(ANSI.green, name) + c(ANSI.gray, '/') + c(ANSI.cyan, database)
      : c(ANSI.green, name)
    return head + ' ' + ANSI.bold + c(ANSI.green, '❯') + ANSI.reset + ' '
  }
  const promptLen = () => (conn?.name ?? 'aegis').length + (database ? database.length + 1 : 0) + 3

  // ---------------------------------------------------------------- 执行
  function execRest(sql: string, rsn: string, code = '') {
    // 每一次回退到 REST 都**必须**带上目标库:后端只有在 database 非空时才覆盖
    // 连接的默认 schema,漏掉它就是把语句悄悄发到另一个库上 —— 而审批单会把那个
    // 错库一起冻进去。所以所有回退都从这一个口子出去。
    return terminalApi.exec(connId, sql, rsn, code, database)
  }
  function sendExec(sql: string, rsn: string, code = ''): 'ws' | 'rest' {
    lastExec.current = { sql, reason: rsn }
    pendingSql.current = sql
    if (session.send({ type: 'exec', connectionId: connId, sql, reason: rsn, mfaCode: code, database })) return 'ws'
    return 'rest'
  }

  function abandonBatch() {
    const ed = session.editor.current
    if (!ed) return
    if (ed.discardQueued() > 0) out(c(ANSI.yellow, '· ' + t('termBatchAbandoned')))
    ed.resume()
  }

  function requestMfa(sql: string, rsn: string) {
    setMfaCode('')
    setMfaErr('')
    setMfa({ sql, reason: rsn })
  }

  async function submitMfa() {
    const code = mfaCode.trim()
    if (code.length !== 6) { setMfaErr(t('mfaStepDesc')); return }
    const p = mfa
    if (!p) return
    setMfa(null)
    if (sendExec(p.sql, p.reason, code) === 'ws') return
    try {
      const env = await execRest(p.sql, p.reason, code)
      if (env.code === CODE_MFA_REQUIRED) { requestMfa(p.sql, p.reason); setMfaErr(t('mfaBadCode')); return }
      renderExecEnvelope(env)
    } catch { out(c(ANSI.red, t('termExecFail'))) }
    session.editor.current?.resume()
  }

  function cancelMfa() {
    setMfa(null)
    out(c(ANSI.gray, t('termCancelled')))
    abandonBatch()
  }

  // ---------------------------------------------------------------- 渲染应答
  function renderIntercept(m: { rule?: string; ruleRef?: RuleRef; approvalNo?: string }) {
    out(c(ANSI.red, t('termIntercepted')))
    out(c(ANSI.gray, tr('termHitRuleLine', { rule: renderRule(m.ruleRef, m.rule || '') || '-' })))
    out(c(ANSI.gray, tr('termApprovalLine', { no: m.approvalNo || '-' })))
  }

  function renderOutput(m: {
    text?: string; outputRef?: RuleRef; rows?: number; ms?: number
    columns?: string[]; data?: string[][]; truncated?: boolean
  }) {
    const rows = m.rows || 0
    const term = session.term.current
    if (m.columns && m.columns.length) {
      const data = m.data || []
      // 有些命令的问题是"零行"答不上来的:描述一张不存在的关系会返回空列清单,
      // 把空表画出来读起来是"它存在,只是没有列"。translateMetaSql 会告诉我们
      // 什么时候适用。
      if (data.length === 0 && pendingEmptyNotice.current) {
        const n = pendingEmptyNotice.current
        out(c(ANSI.yellow, '· ' + tr(n.id, n.params ?? {})))
        pendingEmptyNotice.current = null
        pendingFollow.current = null // 表都没找到,再查它的索引没有意义
        return
      }
      setResult({ columns: m.columns, rows: data })
      // 表格视图开着时数据在下面那张 HTML 表里,终端只留摘要 —— 同一批行不打两遍。
      // \G / \x 是人明确要求的竖排显示,照旧。
      if (pendingVertical.current || expandedMode.current) outLines(renderVertical(m.columns, data))
      else if (!gridView) outLines(renderTable(buildTable(m.columns, data), term?.cols ?? 0, truncHint))
      const more = m.truncated ? tr('termTruncated', { n: data.length }) : ''
      out(c(ANSI.gray, tr('termRows', { n: data.length, more, ms: msLabel(m.ms) })))
      void runFollow()
    } else if (isSelect(pendingSql.current) && rows > 0) {
      // 模拟连接(没有凭据):照 SQL 合成一份预览,并说清只显示了前几行。
      const tb = synthTable(pendingSql.current, rows)
      setResult({ columns: tb.columns, rows: tb.rows })
      if (pendingVertical.current || expandedMode.current) outLines(renderVertical(tb.columns, tb.rows))
      else if (!gridView) outLines(renderTable(tb, term?.cols ?? 0, truncHint))
      const more = tb.rows.length < rows ? tr('termShownFirst', { n: tb.rows.length }) : ''
      out(c(ANSI.gray, tr('termRows', { n: rows, more, ms: msLabel(m.ms) })))
    } else if (m.text) {
      // 写操作用 text 汇报,所以耗时在这里补;而"提示"不是一次执行,不给耗时 ——
      // 什么都没跑,没有东西可计时。两者靠开头的 `·` 区分。
      const line = renderOut(m.outputRef, m.text)
      const isNotice = line.trimStart().startsWith('·')
      out(isNotice ? c(ANSI.yellow, line) : c(ANSI.green, '✓ ' + line) + c(ANSI.gray, tr('termTook', { ms: msLabel(m.ms) })))
    } else {
      out(c(ANSI.green, t('termExecOk')) + c(ANSI.gray, tr('termTook', { ms: msLabel(m.ms) })))
    }
  }

  /** 打出服务端**真正**的裁决。先分类再渲染:被拒绝的应答没有载荷,当成"一份空结果"
   *  去渲染就会给一条被拒的命令印一行绿色的成功。 */
  function renderExecEnvelope(env: ExecEnvelope) {
    const outcome = classifyExecEnvelope(env)
    switch (outcome.kind) {
      case 'intercepted':
        renderIntercept({ rule: outcome.rule, ruleRef: outcome.ruleRef, approvalNo: outcome.approvalNo })
        return
      case 'failed':
        out(c(ANSI.red, '· ' + (outcome.message || t('termExecFail'))))
        return
      case 'mfa':
        requestMfa(lastExec.current.sql, lastExec.current.reason)
        return
      default:
        renderOutput(outcome.data)
    }
  }

  /** 目录命令的第二段(psql 的 \d 先列列、再列索引)。先清掉再发,否则第二段自己
   *  也会走到这里,无限套下去。它照常经过网关判定与审计 —— 确实跑了第二条查询。 */
  async function runFollow() {
    const f = pendingFollow.current
    if (!f) return
    pendingFollow.current = null
    out('')
    out(c(ANSI.bold, tr(f.titleKey)))
    if (sendExec(f.sql, '') === 'ws') return
    try { renderExecEnvelope(await execRest(f.sql, '')) }
    catch { out(c(ANSI.gray, t('termExecFail'))) }
  }

  // ---------------------------------------------------------------- 本地命令
  /** `USE <db>`:后端按 DSN 逐条指定库,服务端的 USE 不会跨连接池留下来 —— 所以
   *  在客户端切换目标库(提示符和之后每一条命令都跟着变)。 */
  function parseUseDb(sql: string): string | null {
    const m = /^\s*use\s+[`"']?([A-Za-z0-9_$-]+)[`"']?\s*$/i.exec(sql)
    return m ? m[1] : null
  }
  function switchDb(db: string) {
    setDatabase(db)
    out(c(ANSI.green, tr('termSwitchedDb', { db })))
  }

  function printMetaHelp() {
    const engine = conn?.engine ?? ''
    const isPG = engineFamily(engine) === 'postgres'
    const isOra = engineFamily(engine) === 'oracle'
    const pad = (s: string) => (s + '            ').slice(0, 12)
    const line = (token: string, descKey: string) => '  ' + c(ANSI.cyan, pad(token)) + c(ANSI.gray, tr(descKey))
    const lines = [c(ANSI.bold, t('termMetaTitle'))]
    lines.push(line('\\?', 'metaHelpHelp'))
    lines.push(line('\\conns', 'metaHelpConns'))
    lines.push(line('\\c <db>', 'metaHelpUse'))
    lines.push(line('\\clear', 'metaHelpClear'))
    lines.push(line('\\l', 'metaHelpL'))
    lines.push(line('\\dt', 'metaHelpDt'))
    lines.push(line('\\dv', 'metaHelpDv'))
    lines.push(line('\\dn', 'metaHelpDn'))
    lines.push(line('\\di', 'metaHelpDi'))
    if (isPG || isOra) lines.push(line('\\du', 'metaHelpDu'))
    if (isPG || isOra) lines.push(line('\\ds', 'metaHelpDs'))
    if (isPG) lines.push(line('\\df', 'metaHelpDf'))
    lines.push(line('\\d <obj>', 'metaHelpD'))
    lines.push(line('\\x', 'metaHelpX'))
    lines.push(line('\\conninfo', 'metaHelpConninfo'))
    lines.push(c(ANSI.gray, tr('termMetaSql', { bs: '\\' })))
    outLines(lines)
  }

  function metaCommand(raw: string) {
    const parts = raw.slice(1).trim().split(/\s+/)
    const cmd = (parts[0] || '').toLowerCase()
    const arg = parts.slice(1).join(' ').replace(/;$/, '').replace(/["'`]/g, '')
    const term = session.term.current
    if (cmd === '?' || cmd === 'h' || cmd === 'help') printMetaHelp()
    else if (cmd === 'conns' || cmd === 'connlist') {
      outLines(list.map((cn) => `  ${String(cn.id).padStart(3)}  ${cn.env}-${cn.name}  ${c(ANSI.gray, cn.defaultRole)}`))
    } else if (cmd === 'clear') term?.clear()
    else if (cmd === 'c' || cmd === 'connect' || cmd === 'u' || cmd === 'use') {
      if (arg) switchDb(arg); else term?.clear()
    } else if (cmd === 'x') {
      const a = arg.toLowerCase()
      expandedMode.current = a === 'on' || a === 'auto' ? true : a === 'off' ? false : !expandedMode.current
      out(c(ANSI.gray, t(expandedMode.current ? 'metaExpandOn' : 'metaExpandOff')))
    } else out(c(ANSI.gray, tr('termUnknownCmd', { bs: '\\', cmd })))
  }

  // ---------------------------------------------------------------- 提交
  async function handleSubmit(stmt: string): Promise<boolean> {
    const ed = session.editor.current
    // 先记下人**提交的原文**,再做下面任何改写 —— 日志该显示他敲了什么,不是归一后的样子。
    transcript.current.command(stmt.trim(), new Date())
    setCanExportLog(true)
    if (!connId) {
      out(c(ANSI.yellow, '· ' + t('termPickConn')))
      ed?.resume()
      return true
    }
    const trimmed = stmt.trim()
    // \G(竖排)/ \g(横排)是 MySQL 客户端的显示终止符,不是 SQL:发之前剥掉,
    // 并记住这一条要不要竖着画。
    pendingVertical.current = /\\G\s*$/.test(trimmed)
    // 每条命令开头重置:上一条 \d 留下的 notice 会被下一条合法返回零行的查询借去用。
    pendingEmptyNotice.current = null
    pendingFollow.current = null
    const raw = trimmed.replace(/\\[gG]\s*$/, '').replace(/;+\s*$/, '').trim()

    /*
     * 两类客户端命令一条通道:psql 风格的 \dt/\d/\dn,以及 SQL*Plus 的 DESC。
     * 翻译之后**照常送进判定** —— 判定看到的是真正要跑的那条 SQL,而不是 `\dt`
     * 这三个字符。反过来做等于给自己开了一条不过闸的路。
     */
    const meta = raw.startsWith('\\')
      ? translateMetaSql(raw, conn?.engine ?? '')
      : translateDescribe(raw, conn?.engine ?? '')
    if (meta) {
      pendingEmptyNotice.current = meta.emptyNotice ?? null
      pendingFollow.current = meta.follow ?? null
      if (sendExec(meta.sql, '') === 'ws') return true
      try { renderExecEnvelope(await execRest(meta.sql, '')) }
      catch { out(c(ANSI.red, t('termExecFail'))) }
      ed?.resume()
      return true
    }
    if (raw.startsWith('\\')) { metaCommand(raw); ed?.resume(); return true }
    if (!raw) { ed?.resume(); return true }
    const useDb = parseUseDb(raw)
    if (useDb) { switchDb(useDb); ed?.resume(); return true }

    try {
      const v = await terminalApi.riskCheck(connId, raw, database)
      if (v.action === 'deny') {
        out(c(ANSI.red, t('termDeniedCap')))
        abandonBatch()
        return true
      }
      if (v.requiresApproval) {
        // 命中高危就**不下发**,而是开提交卡让人填原因 —— 这正是"拦截"的意思:
        // 命令没有跑,也不会因为界面上少点一步就偷偷跑掉。
        const rule = renderRule(v.matchedRuleRef, v.matchedRule)
        setIntercept({ sql: raw, rule })
        out(c(ANSI.yellow, tr('termHitRule', { rule: rule || '-' })))
        return true
      }
      if (sendExec(raw, '') === 'ws') return true
      renderExecEnvelope(await execRest(raw, ''))
      ed?.resume()
    } catch {
      // 预检失败不等于放行:说出来,并把提示符还回去。
      out(c(ANSI.yellow, '· ' + t('termPrecheckFailed')))
      ed?.resume()
    }
    return true
  }

  async function submitApproval() {
    const it = intercept
    if (!it) return
    setIntercept(null)
    const rsn = reason
    setReason('')
    if (sendExec(it.sql, rsn) === 'ws') return
    try {
      const env = await execRest(it.sql, rsn)
      // 也可能根本没被拦(预检到提交之间规则被放宽了),那服务端就直接跑了它。
      // 什么都不打会让人完全看不到发生过什么。
      renderExecEnvelope(env)
    } catch { out(c(ANSI.red, t('termExecFail'))) }
    session.editor.current?.resume()
  }

  function cancelApproval() {
    setIntercept(null)
    setReason('')
    out(c(ANSI.gray, t('termCancelSubmit')))
    abandonBatch()
  }

  // ---------------------------------------------------------------- 快捷脚本
  function disarm() {
    armedSlot.current = 0
    if (armTimer.current) { clearTimeout(armTimer.current); armTimer.current = 0 }
  }

  /**
   * 按下 Alt+N。
   *
   * 快捷键做的是**敲**,不是执行:脚本正文像一次粘贴一样喂给行编辑器,于是它照样
   * 从 handleSubmit 出去,过风险预检、审批与 MFA —— 按当前这台实例判,按下键的那
   * 一刻判。服务端刻意没有"执行这个脚本"的接口:那会是通向网关的第二条路,而判定
   * 长在第一条上。
   */
  function runSlot(slot: number) {
    const ed = session.editor.current
    const s = bySlot[slot]
    if (!s) { notice(c(ANSI.gray, tr('snipNoBinding', { key: slotLabel(slot) }))); return }
    // 正在跑的时候按下去,文本会排在它后面,几秒之后对着一个已经变了的会话执行。
    // 粘贴那样做是因为人明确要求了;一次误触的快捷键没有。
    if (!ed || ed.running) { notice(c(ANSI.yellow, '· ' + t('snipBusy'))); return }
    const text = snippetSubmitText(s.body)
    if (!text) return
    // 快捷键又快又盲:在开发上是日常的那个键,在生产上也只有一下之遥,而肌肉记忆
    // 不读提示符。带红色警告的分层上,第一下只**上膛**并说清它要在哪台实例上跑
    // 什么,4 秒内的第二下才真的执行。
    if (needsArming(tier) && armedSlot.current !== slot) {
      disarm()
      armedSlot.current = slot
      armTimer.current = window.setTimeout(() => { armedSlot.current = 0; armTimer.current = 0 }, ARM_MS)
      noticeLines([
        ANSI.bold + ANSI.yellow + tr('snipArm', { key: slotLabel(slot), name: s.name, env: (conn?.env ?? '').toUpperCase(), inst: conn?.name ?? '' }) + ANSI.reset,
        c(ANSI.gray, '  ' + snippetPreview(s.body, 100)),
        c(ANSI.gray, tr('snipArmHint', { key: slotLabel(slot) })),
      ])
      return
    }
    disarm()
    noticeLines([c(ANSI.cyan, `${slotLabel(slot)} · ${s.name}`)])
    ed.feed(text)
  }

  // ---------------------------------------------------------------- 会话日志
  /**
   * 把这次会话存成文件。
   *
   * 文件是用已经显示过的内容拼的,没有什么要重新取,也没有闸要过 —— 但这次导出
   * **要记审计**:/export 记的是同一件事,而一个把生产输出悄悄写到磁盘上的终端,
   * 正是两者之间的那道缝。
   */
  async function exportLog() {
    const ts = transcript.current
    if (ts.isEmpty || !conn) return
    const meta = {
      instance: `${conn.env}-${conn.name}`,
      database: database || conn.database || '',
      user: me?.name || '',
      exportedAt: new Date(),
    }
    const name = ts.filename(meta)
    // renderFile 而不是 render:文件带 BOM,打开它的编辑器才不会猜成 GBK,把每
    // 一行中文显示成乱码。
    const blob = new Blob([ts.renderFile(meta)], { type: 'text/plain;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = name
    document.body.appendChild(a)
    a.click()
    a.remove()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
    // 审计放在文件已经交给浏览器之后:一次失败的审计调用不该让人丢掉他要的文件,
    // 但它仍然要说出来。
    try {
      await terminalApi.recordTranscriptExport({
        connectionId: conn.id, filename: name, lines: ts.length, dropped: ts.droppedCount, database: meta.database,
      })
    } catch {
      notify(t('logExportAuditFailed'), 'error')
    }
  }

  // ---------------------------------------------------------------- 补全
  /** 光标那一行在终端里的像素位置。xterm 把字画在 canvas 上,光标不是 DOM 节点,
   *  只能按行高换算 —— 够用,并且不依赖 xterm 的内部实现。 */
  function caretTop(): number {
    const term = session.term.current
    const host = session.hostRef.current
    if (!term || !host) return 40
    const rowH = host.clientHeight / Math.max(1, term.rows)
    return Math.round((term.buffer.active.cursorY + 1) * rowH) + 30
  }

  /** 光标前那个还没敲完的词。 */
  function wordAt(line: string, cur: number): string {
    const left = line.slice(0, cur)
    const m = /[\\A-Za-z0-9_$.]+$/.exec(left)
    return m ? m[0] : ''
  }

  // 两个字符起才弹。一个字符能匹配上的东西太多,而浮层一开,Enter 的含义就从
  // "执行这条语句"变成了"采纳候选" —— 这个代价只有在候选确实收敛时才划算。
  function buildCandidates(word: string): AcItem[] {
    if (word.length < 2) return []
    const w = word.toLowerCase()
    const items: AcItem[] = []
    if (word.startsWith('\\')) {
      for (const m of META_COMMANDS) {
        if (m.toLowerCase().startsWith(w) && m.toLowerCase() !== w) items.push({ label: m, kind: 'meta' })
      }
      return items.slice(0, AC_MAX)
    }
    // 表名只从**已经探查到**的那一份 schema 里来 —— 补全不该成为一个会偷偷去连
    // 生产库的功能。
    for (const name of tableNames) {
      if (name.toLowerCase().startsWith(w) && name.toLowerCase() !== w) items.push({ label: name, kind: 'table' })
      if (items.length >= AC_MAX) break
    }
    return items
  }

  function acceptCompletion(i: number) {
    const it = ac.items[i]
    if (!it) return
    session.editor.current?.complete(ac.word.length, it.label)
    setAc({ items: [], index: 0, top: 40, word: '' })
  }

  // ---------------------------------------------------------------- 会话
  const session = useTerminalSession({
    prompt: promptText,
    promptLen,
    contPrompt: () => ' '.repeat(Math.max(0, promptLen() - 2)) + c(ANSI.gray, '· '),
    contPromptLen: promptLen,
    onSubmit: handleSubmit,
    onChange: (line, cur) => {
      const word = wordAt(line, cur)
      const items = buildCandidates(word)
      setAc((prev) => ({ items, index: items.length ? Math.min(prev.index, items.length - 1) : 0, top: caretTop(), word }))
    },
    onKey: (e) => {
      if (acOpen) {
        if (e.key === 'ArrowDown') { e.preventDefault(); setAc((p) => ({ ...p, index: (p.index + 1) % p.items.length })); return false }
        if (e.key === 'ArrowUp') { e.preventDefault(); setAc((p) => ({ ...p, index: (p.index - 1 + p.items.length) % p.items.length })); return false }
        if (e.key === 'Tab' || e.key === 'Enter') { e.preventDefault(); acceptCompletion(ac.index); return false }
        if (e.key === 'Escape') { e.preventDefault(); setAc({ items: [], index: 0, top: 40, word: '' }); return false }
      }
      const slot = slotFromEvent(e)
      if (slot === null) return true
      e.preventDefault()
      runSlot(slot)
      return false
    },
    onPaste: (text) => {
      // 粘一句最顺手的仍然是直接进编辑器;粘二十句就是**在看不清的情况下开始对生产
      // 下发** —— 想中途停下只能 Ctrl+C,而那时前面几条已经跑完了。所以多条改走弹窗。
      // countStatements 是界面用的启发式,数错了两个方向都无害,没有语句能因此绕过网关。
      const n = countStatements(text)
      if (n <= 1) return false
      const ed = session.editor.current
      if (ed?.running) { notice(c(ANSI.yellow, '· ' + t('pasteBusy'))); return true }
      setPaste({ open: true, text })
      notice(c(ANSI.cyan, '· ' + tr('pasteAutoOpened', { n })))
      return true
    },
    onMessage: (m: WsMessage) => {
      const ed = session.editor.current
      if (m.type === 'output') {
        // \d 的第二段由 renderOutput → runFollow 发出去;它自己的回执会再走一遍
        // 这里,所以下面那次 resume 是幂等的,不必为它分一条路。
        renderOutput(m as Parameters<typeof renderOutput>[0])
      } else if (m.type === 'intercept') {
        renderIntercept({ rule: String(m.rule ?? ''), ruleRef: m.ruleRef as RuleRef, approvalNo: String(m.approvalNo ?? '') })
      } else if (m.type === 'mfa_required') {
        requestMfa(lastExec.current.sql, lastExec.current.reason)
        return
      } else if (m.type === 'error') {
        out(c(ANSI.red, '· ' + (String(m.message ?? '') || t('termExecFail'))))
      } else if (m.type === 'session_revoked') {
        out(c(ANSI.red, '· ' + (String(m.message ?? '') || t('wsDisconnected'))))
        ed?.resume()
        return
      } else return
      ed?.resume()
    },
    onCancel: () => {
      // 取消**不等于**没执行:服务端发出去的是 KILL QUERY / cancel request,最终
      // 那句话由回执来说。这里只说"已请求",不替它下结论,编辑器也继续 busy ——
      // 释放它的只有回执,而取消成功与否都会有回执。
      const ed = session.editor.current
      if (!ed?.isBusy()) return
      if (session.send({ type: 'cancel' })) out(c(ANSI.yellow, t('termCancelSent')))
    },
  })

  // 补全的候选池:当前实例已经探查到的表,当前库优先。
  //
  // 用的是树那一份 schema 的**同一个 query key** —— 两边看到的必须是同一批表,
  // 否则会出现"树上有、补全里没有"这种说不清的差别;也因此补全不会多打一次目标库。
  const { data: schemaForAc } = useQuery(connectionSchemaQueryOptions(connId))
  const tableNames = tableIndex(schemaForAc, database)

  // ---------------------------------------------------------------- 布局拖拽
  /** 拖动时这一侧的上限:先受自身上限约束,再受"中间必须留够"约束。 */
  function maxFor(side: Side): number {
    const el = gridRef.current
    const hard = side === 'tree' ? TREE_MAX : INSP_MAX
    if (!el) return hard
    // 另一侧此刻**实际渲染**的宽度,直接从解析后的列宽读,不去猜它落在哪个断点上。
    const cols = getComputedStyle(el).gridTemplateColumns.split(' ').map(parseFloat)
    const other = side === 'tree' ? (cols[2] ?? 0) : (cols[0] ?? 0)
    return Math.min(hard, el.getBoundingClientRect().width - other - TERM_MIN)
  }
  function setW(side: Side, px: number) {
    const min = side === 'tree' ? TREE_MIN : INSP_MIN
    const v = Math.round(Math.min(maxFor(side), Math.max(min, px)))
    if (side === 'tree') { setTreeW(v); localStorage.setItem(TREE_W_KEY, String(v)) }
    else { setInspW(v); localStorage.setItem(INSP_W_KEY, String(v)) }
  }
  /** 双击 / 回车:还原成默认宽度,也就是把这一侧交还给样式表。 */
  function resetW(side: Side) {
    if (side === 'tree') { setTreeW(null); localStorage.removeItem(TREE_W_KEY) }
    else { setInspW(null); localStorage.removeItem(INSP_W_KEY) }
  }
  function onEdgeKey(side: Side, e: ReactKeyboardEvent) {
    const step = e.shiftKey ? 32 : 8
    const cur = side === 'tree' ? treeW : inspW
    let base = cur
    if (base == null && gridRef.current) {
      const cols = getComputedStyle(gridRef.current).gridTemplateColumns.split(' ').map(parseFloat)
      base = side === 'tree' ? cols[0] : cols[2]
    }
    if (base == null || !Number.isFinite(base)) return
    const grow = side === 'tree' ? 'ArrowRight' : 'ArrowLeft'
    const shrink = side === 'tree' ? 'ArrowLeft' : 'ArrowRight'
    if (e.key === grow) setW(side, base + step)
    else if (e.key === shrink) setW(side, base - step)
    else if (e.key === 'Enter' || e.key === ' ') resetW(side)
    else return
    e.preventDefault()
  }

  function persistGridH(v: number) {
    const clamped = clampH(v)
    setGridH(clamped)
    localStorage.setItem(GRID_H_KEY, String(Math.round(clamped)))
  }
  function onSplitKey(e: ReactKeyboardEvent) {
    const step = e.shiftKey ? 8 : 2
    if (e.key === 'ArrowUp') persistGridH(gridH + step)
    else if (e.key === 'ArrowDown') persistGridH(gridH - step)
    else if (e.key === 'Home') persistGridH(GRID_H_MAX)
    else if (e.key === 'End') persistGridH(GRID_H_MIN)
    else if (e.key === 'Enter' || e.key === ' ') persistGridH(GRID_H_DEFAULT)
    else return
    e.preventDefault()
  }

  // 窗口变窄后,拖出来的宽度可能已经把终端挤过了下限 —— 重新夹一次。存着的值不动:
  // 窗口再拉回来时,人自己设的那个宽度应该回来。
  useEffect(() => {
    const onResize = () => {
      setTreeW((w) => (w ? Math.round(Math.min(w, Math.max(TREE_MIN, maxFor('tree')))) : w))
      setInspW((w) => (w ? Math.round(Math.min(w, Math.max(INSP_MIN, maxFor('insp')))) : w))
    }
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])

  /**
   * 开场白:这条会话连的是谁、以什么角色、按什么策略,以及怎么求助。
   *
   * 只打一次,按实例记(banneredFor)。StrictMode 下这个 effect 会跑两遍,而这两
   * 遍之间没有任何东西变过 —— 第二遍再打一次就是屏幕上凭空多出一段开场白。
   *
   * 刻意**不**在这里印环境红字:它现在是终端上方一条常驻的横幅。打在屏幕里的那
   * 行字只在会话开头出现一次,滚几屏就再也看不见了 —— 而"你正在生产库上"这件事,
   * 恰恰是越往后越需要提醒的。
   */
  const banneredFor = useRef(0)
  useEffect(() => {
    if (!conn || banneredFor.current === conn.id) return
    const ed = session.editor.current
    if (!ed) return
    banneredFor.current = conn.id
    // 换了实例就是换了一次会话:上一台的记录不该混进这一台导出的文件里。
    //
    // 屏幕跟着一起清。只清日志会留下一个**看得见却导不出**的落差 —— 屏幕上还挂着
    // 上一台的输出,而导出的文件从这条开场白才开始,头部却只写着当前这台实例。
    // 两者必须说同一件事。
    transcript.current.clear()
    session.term.current?.clear()
    setCanExportLog(false)
    noticeLines([
      c(ANSI.gray, tr('termConnected', {
        conn: `${conn.env}-${conn.name}`, role: conn.defaultRole, policy: conn.policy, user: me?.name || '',
      })),
      c(ANSI.gray, tr('termHelpLine', { bs: '\\' })),
    ])
  }, [conn?.id])

  // 快捷键的定时器要还回去 —— StrictMode 下这个 effect 会跑两遍,而一个挂在那里
  // 的 4 秒定时器足以让"上膛"状态跨过组件的一生。
  useEffect(() => () => { if (armTimer.current) clearTimeout(armTimer.current) }, [])

  // 默认打开哪台实例:优先挑一台带红色警告的 —— 这一页存在的理由就是管住它们。
  //
  // 只挑**一次**。不加这道闸的话,在生产确认弹窗上点「走错了,退出」会把 connId
  // 清成 0,而这个 effect 立刻又把同一台选回来 —— 那个按钮就永远按不动。
  const autoPicked = useRef(false)
  useEffect(() => {
    if (autoPicked.current || connId || !list.length || envtier.loading) return
    autoPicked.current = true
    const first = list.find((x) => envtier.tierOf(x.env)?.dangerBanner) ?? list[0]
    setConnId(first.id)
    setDatabase(first.database || '')
  }, [list.length, envtier.loading, connId])

  const gridVars: CSSProperties = {}
  if (treeW) (gridVars as Record<string, string>)['--tree-w'] = `${treeW}px`
  if (inspW) (gridVars as Record<string, string>)['--insp-w'] = `${inspW}px`

  const edgeHandlers = (side: Side) => ({
    onPointerDown: (e: ReactPointerEvent) => {
      setResizing(side)
      ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
      e.preventDefault()
    },
    onPointerMove: (e: ReactPointerEvent) => {
      if (resizing !== side || !gridRef.current) return
      const box = gridRef.current.getBoundingClientRect()
      setW(side, side === 'tree' ? e.clientX - box.left : box.right - e.clientX)
    },
    onPointerUp: (e: ReactPointerEvent) => {
      if (!resizing) return
      setResizing(null)
      ;(e.currentTarget as HTMLElement).releasePointerCapture?.(e.pointerId)
    },
  })

  return (
    <div
      ref={gridRef}
      className={clsx('tv-grid', treeCollapsed && 'tree-collapsed', inspCollapsed && 'insp-collapsed', zen && 'zen', resizing && 'resizing')}
      style={gridVars}
    >
      {(treeCollapsed || zen)
        ? <div className="tv-rail left" title={t('treeExpand')} onClick={() => { setTreeCollapsed(false); setZen(false) }}><PanelLeftOpen size={16} /></div>
        : (
          <DbTree
            connections={list}
            selectedId={connId}
            selectedDb={database}
            onSelect={(id) => { setConnId(id); setDatabase(list.find((x) => x.id === id)?.database || '') }}
            onSelectDb={(id, db) => { setConnId(id); setDatabase(db) }}
            onCollapse={() => setTreeCollapsed(true)}
            onOpenSource={setSrc}
          />
        )}

      <section className="tv-main">
        {/* 两根竖把手贴在终端栏的左右内边缘,而不是自己占一列 —— 多一列就要在每条
            断点规则里多写一个数,而那串规则本来就是这个视图里最容易写歪的地方。 */}
        {!treeCollapsed && !zen && (
          <div className="tv-edge left" role="separator" aria-orientation="vertical" tabIndex={0} title={t('resizePanel')}
            {...edgeHandlers('tree')} onDoubleClick={() => resetW('tree')} onKeyDown={(e) => onEdgeKey('tree', e)}>
            <span className="tv-grip" />
          </div>
        )}
        {!inspCollapsed && !zen && (
          <div className="tv-edge right" role="separator" aria-orientation="vertical" tabIndex={0} title={t('resizePanel')}
            {...edgeHandlers('insp')} onDoubleClick={() => resetW('insp')} onKeyDown={(e) => onEdgeKey('insp', e)}>
            <span className="tv-grip" />
          </div>
        )}

        <div className="tv-bar">
          <span className={clsx('tv-dot', session.status)} />
          <span className="tv-target">{conn ? `${conn.env}-${conn.name}` : t('termNoConn')}</span>
          {conn && <span className="tv-curdb">{database || '—'}</span>}
          <span className="grow" />
          <button className="tv-act" title={t('pasteBtnTitle')} onClick={() => setPaste({ open: true, text: '' })}>
            <ClipboardPaste size={13} />{t('pasteBtn')}
          </button>
          <button className="tv-act" title={t('snipBtnTitle')} onClick={() => setSnipOpen(true)}>
            <Command size={13} />{t('snipBtn')}
          </button>
          <button className={clsx('tv-act', !canExportLog && 'off')} title={t('logExportHint')} disabled={!canExportLog} onClick={() => void exportLog()}>
            <Download size={13} />{t('logExport')}
          </button>
          <button className={clsx('tv-act', gridView && 'on')} title={t('gridToggle')}
            onClick={() => { const v = !gridView; setGridView(v); localStorage.setItem(GRID_VIEW_KEY, v ? '1' : '0') }}>
            <Table2 size={13} />{t('gridToggle')}
          </button>
          <button className="tv-act" title={zen ? t('zenExit') : t('zenEnter')} onClick={() => setZen(!zen)}>
            {zen ? <Minimize2 size={14} /> : <Maximize2 size={14} />}
          </button>
          <Button variant="ghost" title={t('termReconnect')} onClick={() => session.reconnect()}><RotateCw size={14} /></Button>
        </div>

        {/* 生产安全横幅。它取代了原先打在终端里的那一行红字 —— 那行字会被滚屏顶走,
            而"你正在生产库上"这件事不该只在会话开头说一次。 */}
        {conn && safetyLevel !== 'ok' && (
          <div className={clsx('tv-safety', safetyLevel)}>
            <ShieldAlert size={15} />
            <span>
              <b>{safetyLevel === 'danger' ? t('bannerProd') : t('bannerCaution')}</b>
              <span className="sep">·</span>{t('bannerInst')} <code>{conn.env.toUpperCase()} / {conn.name}</code>
              <span className="sep">·</span>{t('bannerRole')} <code>{conn.defaultRole}</code>
              <span className="sep">·</span>{t('bannerAudit')}
            </span>
            <button className="tv-safety-btn" onClick={() => { setConnId(0); setDatabase('') }}>
              <LogOut size={13} />{t('bannerEnd')}
            </button>
          </div>
        )}

        <div className="tv-split">
          <div className="tv-host-wrap">
            <div className="tv-host" ref={session.hostRef} />
            <Completion items={ac.items} index={ac.index} top={ac.top} onPick={acceptCompletion} />
          </div>
          {gridView && result && (
            <>
              <div
                className={clsx('tv-splitter', dragging && 'dragging')}
                role="separator" aria-orientation="horizontal" tabIndex={0}
                aria-valuenow={Math.round(gridH)} aria-valuemin={GRID_H_MIN} aria-valuemax={GRID_H_MAX}
                title={t('splitHint')}
                onPointerDown={(e) => { setDragging(true); (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId); e.preventDefault() }}
                onPointerMove={(e) => {
                  if (!dragging) return
                  const host = (e.currentTarget as HTMLElement).parentElement
                  if (!host) return
                  const box = host.getBoundingClientRect()
                  if (box.height <= 0) return
                  // 结果表在下面,所以它的高度是"容器底边减去指针位置"。
                  persistGridH(((box.bottom - e.clientY) / box.height) * 100)
                }}
                onPointerUp={(e) => { setDragging(false); (e.currentTarget as HTMLElement).releasePointerCapture?.(e.pointerId) }}
                onDoubleClick={() => persistGridH(GRID_H_DEFAULT)}
                onKeyDown={onSplitKey}
              ><span className="tv-grip" /></div>
              <div className="tv-gridpanel" style={{ flexBasis: `${gridH}%` }}>
                <ResultGrid columns={result.columns} rows={result.rows} onClose={() => { setGridView(false); localStorage.setItem(GRID_VIEW_KEY, '0') }} />
              </div>
            </>
          )}
        </div>

        <div className="tv-status">
          <Plug size={12} />
          {t(`ws_${session.status}`)}
          {conn && <span className="hl">{conn.engine.toUpperCase()}</span>}
          {conn && <span>{conn.defaultRole}</span>}
          {conn && <span>{conn.policy}</span>}
          <span className="grow" />
          <span className="keyhint">
            <kbd>↑</kbd><kbd>↓</kbd> {t('sbHistory')}
            <kbd>Ctrl</kbd><kbd>L</kbd> {t('sbClear')}
            <kbd>Ctrl</kbd><kbd>C</kbd> {t('sbCancel')}
          </span>
          <span className="dim">UTF-8 · {t('termAuditOn')}</span>
        </div>
      </section>

      {(inspCollapsed && !zen) && (
        <div className="tv-rail right" title={t('ctxExpand')} onClick={() => setInspCollapsed(false)}><PanelRightOpen size={16} /></div>
      )}
      {!inspCollapsed && !zen && (
        <aside className="tv-insp">
          <div className="tv-panel-head">
            <ShieldAlert size={14} />{t('termInspector')}
            <span className="grow" />
            <button className="tv-collapse" title={t('ctxCollapse')} onClick={() => setInspCollapsed(true)}><PanelRightOpen size={14} /></button>
          </div>
          <dl className="term-kv">
            <div><dt>{t('termTargetInst')}</dt><dd>{conn ? `${conn.env}-${conn.name}` : '—'}</dd></div>
            <div><dt>{t('termDatabase')}</dt><dd>{database || '—'}</dd></div>
            <div><dt>{t('termTier')}</dt><dd>{tier ? tier.displayName : '—'}</dd></div>
            <div><dt>{t('termPolicy')}</dt><dd>{conn?.policy ?? '—'}</dd></div>
            <div><dt>{t('wsStatusLabel')}</dt><dd><Activity size={12} /> {t(`ws_${session.status}`)}</dd></div>
          </dl>
          {danger && <Badge tone="danger">{t('termDangerTier')}</Badge>}
        </aside>
      )}

      {/* ---- 拦截提交卡 ---- */}
      <Modal
        open={!!intercept}
        title={t('termInterceptTitle')}
        sub={t('termInterceptSub')}
        onClose={cancelApproval}
        footer={
          <>
            <Button variant="ghost" onClick={cancelApproval}>{t('cancel')}</Button>
            <Button variant="primary" disabled={!reason.trim()} onClick={() => void submitApproval()}>{t('termSubmitApproval')}</Button>
          </>
        }
      >
        <pre className="cmd">{intercept?.sql}</pre>
        {intercept?.rule && <div className="notice danger"><ShieldAlert size={14} />{intercept.rule}</div>}
        <div className="fld">
          <label>{t('termReason')}</label>
          <textarea value={reason} onChange={(e) => setReason(e.target.value)} />
        </div>
      </Modal>

      {/* ---- MFA 步进 ---- */}
      <Modal
        open={!!mfa}
        width={420}
        title={t('mfaStepTitle')}
        sub={t('mfaStepDesc')}
        onClose={cancelMfa}
        footer={
          <>
            <Button variant="ghost" onClick={cancelMfa}>{t('cancel')}</Button>
            <Button variant="primary" onClick={() => void submitMfa()}>{t('mfaVerify')}</Button>
          </>
        }
      >
        <input
          className="mfa-code"
          autoFocus
          inputMode="numeric"
          autoComplete="one-time-code"
          placeholder="000000"
          value={mfaCode}
          onChange={(e) => setMfaCode(e.target.value.replace(/\D/g, '').slice(0, 6))}
          onKeyDown={(e) => { if (e.key === 'Enter') void submitMfa() }}
        />
        {mfaErr && <div className="notice danger">{mfaErr}</div>}
      </Modal>

      {/* ---- 进入生产的确认 ---- */}
      <Modal
        open={dangerOpen}
        width={480}
        title={t('prodWarnTitle')}
        sub={t('prodWarnDesc')}
        onClose={() => { setConnId(0); setDatabase('') }}
        footer={
          <>
            <Button variant="ghost" onClick={() => { setConnId(0); setDatabase('') }}>{t('prodWarnLeave')}</Button>
            <Button variant="danger" onClick={() => { setDangerAck((m) => ({ ...m, [connId]: true })); session.focus() }}>{t('prodWarnGo')}</Button>
          </>
        }
      >
        <div className="tv-dtarget">
          <span className="dt-env">{conn?.env.toUpperCase()}</span>
          <span className="dt-name">{conn?.name}</span>
          {database && <span className="dt-db">/ {database}</span>}
        </div>
      </Modal>

      <PasteModal
        open={paste.open}
        text={paste.text}
        env={(conn?.env ?? '').toUpperCase()}
        inst={conn?.name ?? ''}
        tierCode={conn ? envtier.tierCodeOf(conn.env) : ''}
        onChange={(v) => setPaste({ open: true, text: v })}
        onClose={() => { setPaste({ open: false, text: '' }); session.focus() }}
        onSubmit={() => {
          // 与快捷脚本同一套归一:CRLF → LF,末条没有终止符就补一个,免得最后一行
          // 停在编辑器里等回车。
          const text = snippetSubmitText(paste.text)
          setPaste({ open: false, text: '' })
          session.focus()
          // **整批一次提交**,不按 ; 拆开逐条送:一批里只要有一条高危,逐条送会让
          // 前面安全的那几条先跑完,审批人拿到的是一条脱离上下文的语句,而库里已经
          // 变了一半。判定与执行仍然逐条,只是都发生在服务端。
          if (text) session.editor.current?.submitBatch(text)
        }}
      />

      <SnippetModal open={snipOpen} onClose={() => { setSnipOpen(false); session.focus() }} />
      <SourceViewer target={src} onClose={() => setSrc(null)} />
    </div>
  )
}

/** 一台实例已探查到的表名,当前库的排在前面。 */
function tableIndex(sc: ConnectionSchema | undefined, database: string): string[] {
  if (!sc?.databases?.length) return []
  const names: string[] = []
  const push = (db: SchemaDB) => {
    for (const tb of db.tables ?? []) names.push(tb.name)
    for (const s of db.schemas ?? []) for (const tb of s.tables ?? []) names.push(tb.name)
  }
  const cur = sc.databases.find((d) => d.name === database)
  if (cur) push(cur)
  for (const d of sc.databases) if (d !== cur) push(d)
  return Array.from(new Set(names))
}
