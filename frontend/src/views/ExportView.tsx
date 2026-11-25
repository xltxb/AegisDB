import { useState, type ComponentType } from 'react'
import clsx from 'clsx'
import { useAuthStore } from '@/stores/auth'
import {
  DatabaseZap, KeyRound, Download, Copy, Check, Eye, EyeOff, FolderCog, TriangleAlert,
  Loader, CircleCheck, CircleX, Clock, Trash2, RotateCw, ChevronDown, Inbox, Ban, Square, CheckCheck,
} from 'lucide-react'
import VButton from '@/components/common/VButton'
import VSelect from '@/components/common/VSelect'
import api from '@/api'
import { CODE_OK, CODE_EXPORT_PATH_UNSET, localizeText } from '@/api/http'
import { confirmAction } from '@/lib/confirm'
import { useMountEffect } from '@/hooks/effects'
import { useI18n } from '@/hooks/useI18n'
import { nav } from '@/hooks/useNav'
import { useStateRef } from '@/hooks/useStateRef'
import type { Connection, ExportJob } from '@/types'
import './ExportView.scoped.css'

// The instance picker is a searchable VSelect, which works on display labels, so
// the selection round-trips through `env-name` (the same label the terminal and
// the async-exec page use). A deployment can carry hundreds of instances; the
// label is what an operator actually types to find one.
const connLabel = (c: Connection) => `${c.env}-${c.name}`
const isActive = (j: ExportJob) => j.status === 'pending' || j.status === 'running'

const partsOf = (j: ExportJob) => (j.files ? j.files.split('\n').map((f) => f.trim()).filter(Boolean) : [])
// 下载过的分卷(服务端记的,换台电脑也在)。全下过了按钮改成「再次下载」—— 照样能下,文件可能丢了。
const downloadedOf = (j: ExportJob) => {
  const done = new Set((j.downloadedFiles || '').split('\n').map((f) => f.trim()).filter(Boolean))
  return partsOf(j).filter((f) => done.has(f)).length
}
const fullyDownloaded = (j: ExportJob) => partsOf(j).length > 0 && downloadedOf(j) === partsOf(j).length
const cancellable = (j: ExportJob) => j.status === 'pending' || j.status === 'awaiting' || j.status === 'running'

async function downloadOne(file: string) {
  const blob = await api.exportDownload(file)
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = file.split(/[\\/]/).pop() || 'export.zip'
  a.click()
  URL.revokeObjectURL(url)
}

const goSettings = () => { void nav.push('/admin/settings') }
const kb = (n: number) => (n < 1024 ? n + ' B' : n < 1048576 ? (n / 1024).toFixed(1) + ' KB' : (n / 1048576).toFixed(2) + ' MB')
const stMeta: Record<string, { t: string; icon: ComponentType<any>; cls: string }> = {
  pending: { t: 'exportStPending', icon: Clock, cls: 'wait' },
  running: { t: 'exportStRunning', icon: Loader, cls: 'run' },
  done: { t: 'exportStDone', icon: CircleCheck, cls: 'ok' },
  failed: { t: 'exportStFailed', icon: CircleX, cls: 'bad' },
  // 归档已被保留策略清理:任务行还在,文件没了。
  expired: { t: 'exportStExpired', icon: Trash2, cls: 'gone' },
  // 含敏感字段的导出在批准之前停在这里，不进队列。借用"等待"的样式：它和排队中
  // 一样是"还没开始"，但等的是人不是机器。
  awaiting: { t: 'exportStAwaiting', icon: Clock, cls: 'wait' },
  // 被人终止:和清理掉的一样是灰色 —— 它不是出错,是有人决定不要了。
  cancelled: { t: 'exportStCancelled', icon: Ban, cls: 'gone' },
}
const meta = (s: string) => stMeta[s] || stMeta.pending

function detectTable(sql: string): string {
  const q = sql.replace(/--[^\n]*|\/\*[\s\S]*?\*\//g, ' ')
  if (/\bjoin\b|\bwith\b|\bunion\b/i.test(q)) return ''
  const m = /\bfrom\s+([`"]?[\p{L}_][\p{L}\p{N}_$#]*[`"]?(?:\.[`"]?[\p{L}_][\p{L}\p{N}_$#]*[`"]?)?)\s*([^\s(,][\s\S]*)?$/iu.exec(q)
  if (!m || (m[2] && /^,/.test(m[2].trim()))) return ''
  return m[1].replace(/[`"]/g, '')
}

const toggled = (prev: Set<number>, id: number) => {
  const s = new Set(prev)
  if (s.has(id)) s.delete(id)
  else s.add(id)
  return s
}

export default function ExportView() {
  const { t } = useI18n()
  const isAdmin = useAuthStore((s) => s.isAdmin)
  const [conns, setConns, connsRef] = useStateRef<Connection[]>([])
  const [connId, setConnId] = useState(0)
  const [db, setDb] = useState('')                       // target database within the instance ('' = connection default)
  const [dbOptions, setDbOptions, dbOptionsRef] = useStateRef<string[]>([])
  const [sql, setSql] = useState('SELECT id, name, status, created_at FROM users')
  const [name, setName] = useState('')
  // 文件名必填(2026-10-29):列表里靠它认出是哪一份导出。点过提交才标红,不一进页面就满屏红框。
  const [nameTouched, setNameTouched] = useState(false)
  const nameMissing = nameTouched && !name.trim()
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [needPath, setNeedPath] = useState(false)
  const [jobs, setJobs, jobsRef] = useStateRef<ExportJob[]>([])
  const [copiedId, setCopiedId] = useState(0)
  // 首次拉取任务列表时为 true —— 空列表和"还没拉到"长得一样,而前者是一句结论、
  // 后者只是还没问完。骨架屏存在的意义就是不让这两件事混在一起。
  const [loading, setLoading] = useState(true)
  // 服务器上归档保留几天(0=永久)。来自 /export/config,不是前端猜的默认值 ——
  // 提交页要说的保留期,必须和真正执行删除的那个设置是同一个数。
  const [retentionDays, setRetentionDays] = useState(0)
  // 要不要敏感字段的原值。默认 false —— 打码是常态，放开才需要理由。
  const [includeSensitive, setIncludeSensitive, includeSensitiveRef] = useStateRef(false)
  // 导出格式。SQL = 写成可直接执行的 INSERT,要有目标表名:单表查询认得出 FROM 后的那张表
  // (提示里写出来,留空就用它,服务端按同样的规则再认一次);联表、子查询认不出,必须填。
  const [format, setFormat] = useState<'csv' | 'sql'>('csv')
  const [targetTable, setTargetTable] = useState('')
  const detectedTable = detectTable(sql)
  const targetPlaceholder = detectedTable ? t('exportTargetAuto', { table: detectedTable }) : t('exportTargetNeed')
  // 上一次提交是否停在了待审批（用于提交后的那句提示）。
  const [awaiting, setAwaiting] = useState(false)
  // 没勾原值却进了审批:是查询结构的缘故,提示要说清"批准后仍按脱敏导出"
  const [awaitingStructural, setAwaitingStructural] = useState(false)

  const activeConn = conns.find((c) => c.id === connId) || null
  const anyActive = jobs.some(isActive)

  async function loadJobs() {
    try {
      const l = await api.exportJobs()
      setJobs(Array.isArray(l) ? l : [])
    } catch { /* ignore */ }
  }

  // 手动刷新。轮询只在有任务真的在跑的时候才发请求(见下面的 timer),所以一张全是
  // 历史记录的列表是不会自己动的 —— 那时候要看有没有新东西,得有个地方能点。
  const [refreshing, setRefreshing] = useState(false)
  async function refresh() {
    setRefreshing(true)
    try { await loadJobs() } finally { setRefreshing(false) }
  }

  // Load the selectable databases for the chosen instance (same live introspection
  // the terminal uses). Defaults to the connection's own database when it has one.
  async function loadDbs(id: number) {
    setDb('')
    setDbOptions([])
    if (!id) return
    try {
      const sc = await api.connectionSchema(id)
      const opts = sc.databases.map((d) => d.name)
      setDbOptions(opts)
      const c = connsRef.current.find((x) => x.id === id)
      // Pre-select a real database so an export isn't submitted with no schema (which
      // fails on the target with "No database selected"): the connection's own
      // database if it has one, else the first introspected database.
      if (c?.database && opts.includes(c.database)) setDb(c.database)
      else if (opts.length) setDb(opts[0])
    } catch { /* leave on default */ }
  }
  const connLabels = conns.map(connLabel)
  const selectedLabel = activeConn ? connLabel(activeConn) : ''
  function pickLabel(l: string) {
    const c = conns.find((x) => connLabel(x) === l)
    if (c) { setConnId(c.id); void loadDbs(c.id) }
  }

  useMountEffect(() => {
    let gone = false
    let timer: ReturnType<typeof setInterval> | null = null
    void (async () => {
      try {
        const l = await api.connections()
        const list = Array.isArray(l) ? l : []
        setConns(list)
        const first = list.find((c) => c.env === 'prod') ?? list[0]
        if (first) { setConnId(first.id); await loadDbs(first.id) }
      } catch { /* ignore */ }
      try { setRetentionDays((await api.exportConfig()).retentionDays ?? 0) } catch { /* 取不到就不提保留期,别编一个 */ }
      await loadJobs()
      setLoading(false)
      if (gone) return
      timer = setInterval(() => { if (jobsRef.current.some(isActive)) void loadJobs() }, 2000)
    })()
    return () => { gone = true; if (timer) clearInterval(timer) }
  })

  async function submit() {
    const q = sql.trim()
    if (!connId) { setErr(t('pickInstance')); return }
    setNameTouched(true)
    if (!name.trim()) { setErr(t('exportNameRequired')); return }
    if (!q) { setErr(t('enterExportSql')); return }
    setBusy(true)
    setErr('')
    setNeedPath(false)
    try {
      const env = await api.exportData(connId, q, name.trim(), db, includeSensitive,
        format, format === 'sql' ? targetTable.trim() : '')
      if (env.code === CODE_EXPORT_PATH_UNSET) { setNeedPath(true); return }
      if (env.code === CODE_OK) {
        // 停在待审批的任务不会自己开始。不说这一句,人会一直等在下载那一栏。
        // 按**后端返回的状态**判,不按复选框:没勾原值的查询也可能因为结构(子查询 /
        // UNION / CTE)被转去审批 —— 那时复选框是没勾的,靠它判就什么提示都没有,人只看到
        // 任务停着,以为是坏了。
        const aw = env.data?.status === 'awaiting'
        setAwaiting(aw)
        setAwaitingStructural(aw && !includeSensitiveRef.current)
        await loadJobs()
        return
      }
      setErr(env.msg || t('submitFailed'))
    } catch { setErr(t('submitFailed')) } finally { setBusy(false) }
  }

  /**
   * 把一条任务的参数填回左侧表单。
   *
   * 失败和过期的任务需要一个出口,而这里**没有**"重跑"这回事:一次导出跑的是提交
   * 时那条语句、那个库,重跑等于凭旧参数再建一个任务,而参数是不是还对(库还在吗、
   * 语句改过吗)只有人知道。所以填回表单、由人按下提交 —— 不新增接口,也不替人做
   * 那次决定。
   */
  async function reuse(j: ExportJob) {
    const c = connsRef.current.find((x) => x.name === j.instance)
    if (c) {
      setConnId(c.id)
      await loadDbs(c.id)
      if (j.database && dbOptionsRef.current.includes(j.database)) setDb(j.database)
    }
    setSql(j.sql)
    if (j.name) setName(j.name)
    setErr('')
    setAwaiting(false)
    setAwaitingStructural(false)
    setNeedPath(false)
  }

  // Export passwords are masked by default and only shown on explicit reveal, so
  // they don't sit in the DOM across the 2s job polling (L4).
  const [shownPw, setShownPw] = useState<Set<number>>(new Set())

  // 展开的错误。默认单行截断:一屏里三条失败任务各占五行报错,列表就没法看了;
  // 但截断不能等于看不到 —— 报错原文是排查的起点,所以点一下能展开。
  const [openErr, setOpenErr] = useState<Set<number>>(new Set())

  async function copyPw(j: ExportJob) {
    try {
      await navigator.clipboard.writeText(j.password)
      setCopiedId(j.id)
      setTimeout(() => setCopiedId(0), 1600)
    } catch { /* ignore */ }
  }

  // Download every part file of a finished job (sequentially to avoid popup blocks).
  async function download(j: ExportJob) {
    try {
      for (const f of partsOf(j)) {
        await downloadOne(f)
        await new Promise((r) => setTimeout(r, 250))
      }
    } catch { setErr(t('downloadFailed')) }
    await loadJobs() // 服务端记下了下载(迁移 0057),刷新出「已下载」标记
  }

  /**
   * 终止一个还没结束的导出(排队中 / 待审批 / 执行中)。执行中的,服务端会同时终止目标库上的
   * 查询 —— 所以要先确认:这不是"隐藏这一行",是让数据库停下正在跑的语句。
   */
  const [cancelling, setCancelling] = useState<Set<number>>(new Set())
  async function cancelJob(j: ExportJob) {
    if (!(await confirmAction(t('exportCancelConfirm', { id: j.id })))) return
    setCancelling((s) => new Set(s).add(j.id))
    try {
      await api.cancelExport(j.id)
    } catch (e: any) {
      setErr(e?.message || t('exportCancelFailed'))
    } finally {
      setCancelling((prev) => {
        const s = new Set(prev)
        s.delete(j.id)
        return s
      })
      await loadJobs() // 可能恰好在这一刻跑完了:以服务端为准
    }
  }

  const sizeLine = (j: ExportJob) => (
    <span className="jstat">{t('exportRowsN', { n: j.rows })}<span className="dot">·</span>{kb(j.bytes)}<span className="dot">·</span>{j.parts} {t('exportParts')}</span>
  )

  function renderAction(j: ExportJob) {
    if (j.status === 'done') {
      return (
        <VButton variant={fullyDownloaded(j) ? 'secondary' : 'primary'} height="30px" data-testid="export-download" onClick={() => download(j)}>
          <Download size={13} />{fullyDownloaded(j) ? t('exportDownloadAgain') : t('exportDownload')}{j.parts > 1 ? ` (${j.parts})` : ''}
        </VButton>
      )
    }
    if (cancellable(j)) {
      return (
        <VButton variant="secondary" height="30px" disabled={cancelling.has(j.id)} title={t('exportCancelTip')} data-testid="export-cancel" onClick={() => cancelJob(j)}>
          <Square size={12} />{t('exportCancel')}
        </VButton>
      )
    }
    if (j.status === 'failed' || j.status === 'expired' || j.status === 'cancelled') {
      return (
        <VButton variant="secondary" height="30px" data-testid="export-reuse" onClick={() => reuse(j)}>
          <RotateCw size={13} />{t('exportReuse')}
        </VButton>
      )
    }
    return null
  }

  // 执行详情 / 错误:占满第二行,因为语句和报错都长
  function renderDetail(j: ExportJob) {
    if (j.status === 'done') {
      const PwIcon = shownPw.has(j.id) ? EyeOff : Eye
      const CopyIcon = copiedId === j.id ? Check : Copy
      const dl = downloadedOf(j)
      return (
        <div className="jdet">
          {sizeLine(j)}
          {/* 下载过的分卷(服务端记的):放在详情行,不挤操作列 */}
          {dl > 0 && (
            <span className="jdl" title={j.downloadedAt ? t('exportDownloadedAt', { t: j.downloadedAt.slice(5, 16).replace('T', ' ') }) : ''} data-testid="job-downloaded"><CheckCheck size={12} />{partsOf(j).length > 1 ? t('exportDownloadedParts', { n: dl, total: partsOf(j).length }) : t('exportDownloaded')}{j.downloadedAt && <span className="jdlt">{j.downloadedAt.slice(5, 16).replace('T', ' ')}</span>}</span>
          )}
          <span className="jpw">
            <KeyRound size={12} /><span className="pl">{t('exportPassword')}</span>
            <code className="pw">{shownPw.has(j.id) ? j.password : '••••••••••'}</code>
            <button className="iconbtn" title={t('tipTogglePw')} onClick={() => setShownPw((s) => toggled(s, j.id))}><PwIcon size={14} /></button>
            <button className="iconbtn" title={t('copy')} onClick={() => copyPw(j)}><CopyIcon size={14} /></button>
          </span>
        </div>
      )
    }
    // 过期:文件被保留策略删了,但导出了什么仍然留在这里 —— 审计问的是这个,不是文件。
    if (j.status === 'expired') {
      return (
        <div className="jdet">
          {sizeLine(j)}
          <span className="gtip"><Trash2 size={12} />{t('exportExpiredHint', { d: retentionDays })}</span>
        </div>
      )
    }
    if (j.status === 'failed' || j.status === 'cancelled') {
      const ErrIcon = j.status === 'cancelled' ? Ban : CircleX
      return (
        <div className="jdet">
          <div className={clsx('jerr', openErr.has(j.id) && 'open', j.status === 'cancelled' && 'muted')} onClick={() => setOpenErr((s) => toggled(s, j.id))}>
            <ErrIcon size={13} className="ei" />
            <span className="etext">{localizeText(j.error, j.errorRef) || t(j.status === 'cancelled' ? 'exportStCancelled' : 'exportStFailed')}</span>
            <ChevronDown size={13} className={clsx('ec', openErr.has(j.id) && 'up')} />
          </div>
        </div>
      )
    }
    return null
  }

  return (
    <div className="scy page">
      <div className="v-head">
        <div className="v-head-l">
          <div className="v-eyebrow">DATA EXPORT</div>
        </div>
      </div>
      <div className="col">
        {/* ------------------------------------------------ 左:新建导出 */}
        {/* 表单是单列的:实例和目标库横着挤在一行时,两个都短得放不下完整的名字,
            而它们恰恰是最需要看清的两项。竖排之后每一项都拿到整个卡片的宽度。 */}
        <section className="card formcard">
          <div className="shead">
            <div className="sic"><DatabaseZap size={18} color="var(--accent-text)" /></div>
            <div><div className="st">{t('exportTitle')}</div><div className="ss">{t('exportSub')}</div></div>
          </div>
          <div className="body">
            {needPath && (
              <div className="notice">
                <FolderCog size={15} /><span>{t('exportNoPath')}</span>
                {/* 系统设置在管理后台,只有平台管理员进得去;别人点了会被守卫挡回原页,看着像没反应(2026-10 审查) */}
                {isAdmin
                  ? <VButton variant="secondary" height="30px" onClick={goSettings}>{t('scPathGo')}</VButton>
                  : <span className="askadmin" data-testid="path-ask-admin">{t('scPathAskAdmin')}</span>}
              </div>
            )}

            <div className="field">
              <div className="lbl">{t('exportConn')}</div>
              <VSelect value={selectedLabel} onChange={pickLabel} options={connLabels} searchable height="38px" />
            </div>
            <div className="field">
              <div className="lbl">{t('exportDb')}</div>
              <select value={db} onChange={(e) => setDb(e.target.value)} className="sel">
                <option value="">{t('exportDbDefault')}</option>
                {dbOptions.map((d) => <option key={d} value={d}>{d}</option>)}
              </select>
            </div>
            <div className="field">
              <div className="lbl">{t('exportName')} <span className="req">*</span></div>
              <input value={name} onChange={(e) => setName(e.target.value)} className={clsx('nameinput', nameMissing && 'invalid')} maxLength={64} placeholder={t('exportNamePh')} data-testid="export-name" />
            </div>
            {activeConn?.env === 'prod' && <div className="prodwarn"><TriangleAlert size={13} />{t('opWarnPrefix')} <b>PROD · {activeConn.name}</b> · {t('opWarnCaution')}</div>}

            <div className="field">
              <div className="lbl">{t('exportSql')}</div>
              <textarea
                value={sql} onChange={(e) => setSql(e.target.value)} className="sqlarea" spellCheck={false}
                placeholder={"SELECT id, name, created_at\nFROM users\nWHERE created_at >= '2026-01-01'"}
              />
            </div>

            <div className="field">
              <div className="lbl">{t('exportFormat')}</div>
              <div className="fmtseg">
                <button type="button" className={clsx(format === 'csv' && 'on')} data-testid="export-format-csv" onClick={() => setFormat('csv')}>CSV</button>
                <button type="button" className={clsx(format === 'sql' && 'on')} data-testid="export-format-sql" onClick={() => setFormat('sql')}>SQL</button>
              </div>
            </div>
            {format === 'sql' && (
              <div className="field">
                <div className="lbl">{t('exportTargetTable')}</div>
                <input value={targetTable} onChange={(e) => setTargetTable(e.target.value)} className="nameinput" data-testid="export-target" placeholder={targetPlaceholder} />
                <div className="fmthint">{t('exportSqlHint')}</div>
              </div>
            )}

            <label className="senschk">
              <input checked={includeSensitive} onChange={(e) => setIncludeSensitive(e.target.checked)} type="checkbox" />
              <span className="senstxt">{t('exportSensitive')}</span>
            </label>
            {includeSensitive && <div className="senshint"><TriangleAlert size={13} />{t('exportSensitiveHint')}</div>}
            {awaiting && <div className="notice">{t(awaitingStructural ? 'exportAwaitingStructural' : 'exportAwaitingNotice')}</div>}
            {err && <div className="err">{err}</div>}

            <VButton className="submit" variant="primary" disabled={busy} onClick={submit}>
              {busy ? t('exportRunning') : t('exportSubmit')}
            </VButton>
            {/* 保留期写在提交按钮下面,而不是藏进帮助文档:导出成功后才知道"三天后
                文件没了",那三天就已经被当成永久的了。 */}
            <div className="subhint">
              <span>{t('exportEncrypted')}</span>
              {retentionDays > 0 && <><span className="dot">·</span><span>{t('exportRetentionNote', { d: retentionDays })}</span></>}
            </div>
          </div>
        </section>

        {/* ------------------------------------------------ 右:导出任务 */}
        <section className="card">
          <div className="shead">
            <div className="sic"><Download size={18} color="var(--accent-text)" /></div>
            <div className="grow"><div className="st">{t('exportTasks')}</div><div className="ss">{t('exportTasksSub')}</div></div>
            {/* 轮询只在有任务真的在跑时才发请求,所以这里说的是"此刻会不会自己动",
                而不是一个长亮的转圈。 */}
            {anyActive && <span className="poll"><Loader size={13} className="spin" />{t('exportPolling')}</span>}
            <button className="refresh" disabled={refreshing} title={t('btnRefresh')} data-testid="export-refresh" onClick={refresh}>
              <RotateCw size={14} className={clsx(refreshing && 'spin')} />{t('btnRefresh')}
            </button>
          </div>

          <div className="jhead">
            <span>{t('exportColStatus')}</span>
            <span>{t('exportColJob')}</span>
            <span className="right">{t('exportColTime')}</span>
            <span className="right">{t('exportColAct')}</span>
          </div>

          {/* 加载中:骨架而不是空列表 —— "还没问完"和"确实没有"是两件事 */}
          {loading ? (
            <div className="jobs">
              {[1, 2, 3].map((n) => (
                <div key={n} className="jrow skel">
                  <span className="sk sk-badge" /><span className="sk sk-main" /><span className="sk sk-time" /><span className="sk sk-act" />
                </div>
              ))}
            </div>
          ) : !jobs.length ? (
            <div className="empty">
              <div className="eic"><Inbox size={22} color="var(--text-faint)" /></div>
              <div className="et">{t('exportNoTasks')}</div>
              <div className="es">{t('exportEmptySub')}</div>
            </div>
          ) : (
            <div className="jobs">
              {jobs.map((j) => {
                const m = meta(j.status)
                return (
                  <div key={j.id} className="jrow">
                    {/* 状态 */}
                    <span className={clsx('jbadge', m.cls)}>
                      <m.icon size={12} className={clsx(j.status === 'running' && 'spin')} />{t(m.t)}
                    </span>

                    {/* 实例与语句 */}
                    <div className="jmain">
                      <div className={clsx('jname', !j.name && 'unnamed')} title={j.name} data-testid="job-name">{j.name || t('exportUnnamed')}</div>
                      <div className="jinst">{j.instance}{j.database && <span className="jdb"> / {j.database}</span>}{j.format === 'sql' && <span className="jfmt" data-testid="job-format-sql" title={j.targetTable}>SQL</span>}</div>
                      <div className="jsql" title={j.sql}>{j.sql}</div>
                    </div>

                    <span className="jtime">#{j.id}<br />{j.createdAt?.slice(5, 16).replace('T', ' ')}</span>

                    {/* 操作 */}
                    <span className="jact">{renderAction(j)}</span>

                    {renderDetail(j)}
                  </div>
                )
              })}
            </div>
          )}
        </section>
      </div>
    </div>
  )
}
