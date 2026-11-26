// 跨环境表结构对比(.scratch/schema-diff/PRD.md)。
//
// 左右各选"实例 + 库",只有同一引擎族能互比;服务端两边实时读目录后对比。这一页只负责把结果
// 说清楚:三组(只在左 / 只在右 / 结构不同)、每张不同表的左右对照、导出。
import { Fragment, useRef, useState } from 'react'
import clsx from 'clsx'
import { ArrowLeftRight, Play, ChevronRight, Download, FileText, TriangleAlert, CircleCheck, Search } from 'lucide-react'
import VButton from '@/components/common/VButton'
import VSelect from '@/components/common/VSelect'
import api from '@/api'
import type { SchemaDiffConn, SchemaDiffResp, SchemaDiffOptions, RuleRef } from '@/types'
import { diffToMarkdown, diffToCSV, columnPairs, indexMarks, showIndexDef } from '@/lib/schemaDiffExport'
import { saveBlob } from '@/lib/download'
import { copyText } from '@/lib/clipboard'
import { useUIStore } from '@/stores/ui'
import { useI18n } from '@/hooks/useI18n'
import { useEvent } from '@/hooks/useEvent'
import { useMountEffect, useUpdateEffect } from '@/hooks/effects'
import { useStateRef } from '@/hooks/useStateRef'
import './SchemaDiffView.scoped.css'

type DbList = { names: string[]; error?: string; loading?: boolean }

const connLabel = (c: SchemaDiffConn) => `${c.name} · ${(c.env ?? '').toUpperCase()}`
const schKey = (id: number, db: string) => `${id}|${db.trim()}`
const fmtTime = (s: string) => {
  const x = new Date(s)
  return isNaN(x.getTime()) ? s : x.toLocaleString('sv').slice(0, 19)
}

export default function SchemaDiffView() {
  const { t, te } = useI18n()
  const tt = (k: string, n?: Record<string, unknown>) => t(k, n || {})

  const [conns, setConns] = useState<SchemaDiffConn[]>([])
  const [loadErr, setLoadErr] = useState('')
  const byLabel = (l: string) => conns.find((c) => connLabel(c) === l)

  const [leftId, setLeftId] = useState(0)
  const [rightId, setRightId] = useState(0)
  const [leftDb, setLeftDb] = useState('')
  const [rightDb, setRightDb] = useState('')
  // PG / DWS:库下面还有一层 schema。空 = 库内全部非系统 schema;填了只比这一个(表名不带前缀)。
  const [leftSchema, setLeftSchema] = useState('')
  const [rightSchema, setRightSchema] = useState('')
  const [opts, setOpts] = useState<SchemaDiffOptions>({ ignoreColumnOrder: false, ignoreComments: false, ignoreIndexNames: false })

  const left = conns.find((c) => c.id === leftId)
  const right = conns.find((c) => c.id === rightId)
  const isPg = left?.family === 'postgres'
  // 右边只列同一引擎族的实例:其它环境的排在前面(通常要比的就是另一个环境),左边自己排最后 ——
  // 同一个实例里的两个库 / 两个 schema(orders 与 orders_uat、app 与 app_uat)也要能比。
  function peersOf(l: SchemaDiffConn | undefined): SchemaDiffConn[] {
    if (!l) return []
    const rank = (c: SchemaDiffConn) => (c.id === l.id ? 2 : c.env === l.env ? 1 : 0)
    return conns
      .filter((c) => c.family === l.family)
      .sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name))
  }
  const rightChoices = peersOf(left)

  // 每个实例上的库名(库名下拉),按实例缓存。读不到(连不上 / 没权限 / 列表为空)就退回手填 ——
  // 下拉只是帮忙选,不能挡住对比。
  const [dbLists, setDbLists, dbListsRef] = useStateRef<Record<number, DbList>>({})
  async function loadDbs(id: number) {
    if (!id || dbListsRef.current[id]) return
    const put = (v: DbList) => setDbLists((m) => ({ ...m, [id]: v }))
    put({ names: [], loading: true })
    try {
      const r = await api.schemaDiffDatabases(id)
      put({ names: r.databases || [], error: r.error })
    } catch (e: any) {
      put({ names: [], error: e?.message || t('loadFailed') })
    }
  }
  const dbNames = (id: number) => dbLists[id]?.names || []
  // 加载中也先给下拉(可以直接输入);读完没有任何库才换成输入框
  const dbPickable = (id: number) => !!dbLists[id]?.loading || dbNames(id).length > 0
  const dbError = (id: number) => dbLists[id]?.error || ''
  useUpdateEffect(() => { void loadDbs(rightId) }, [rightId])

  // PG / DWS 库里的 schema(schema 下拉),按"实例 + 库"缓存。第一项「全部 schema」对应留空。
  const [schLists, setSchLists, schListsRef] = useStateRef<Record<string, DbList>>({})
  async function loadSchemas(id: number, db: string) {
    const k = schKey(id, db)
    if (!id || !db.trim() || schListsRef.current[k]) return
    const put = (v: DbList) => setSchLists((m) => ({ ...m, [k]: v }))
    put({ names: [], loading: true })
    try {
      const r = await api.schemaDiffSchemas(id, db.trim())
      put({ names: r.schemas || [], error: r.error })
    } catch (e: any) {
      put({ names: [], error: e?.message || t('loadFailed') })
    }
  }
  const schOf = (id: number, db: string) => schLists[schKey(id, db)]
  const schPickable = (id: number, db: string) => !!schOf(id, db)?.loading || (schOf(id, db)?.names.length ?? 0) > 0
  const schOptions = (id: number, db: string) => [t('sdAllSchemas'), ...(schOf(id, db)?.names || [])]
  const fromSchOption = (v: string) => (v === t('sdAllSchemas') ? '' : v)
  // 库名是手填的时候每敲一个字都会变:停下来再读
  const schTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const loadBothSchemas = useEvent(() => {
    void loadSchemas(leftId, leftDb)
    void loadSchemas(rightId, rightDb)
  })
  useUpdateEffect(() => {
    if (!isPg) return
    clearTimeout(schTimer.current)
    schTimer.current = setTimeout(loadBothSchemas, 300)
  }, [isPg, leftId, leftDb, rightId, rightDb])
  useMountEffect(() => () => clearTimeout(schTimer.current))

  useUpdateEffect(() => {
    const l = left
    if (!l) return
    void loadDbs(l.id)
    setLeftDb(l.database)
    // 换了左边:右边不再合法就换成第一个候选;库名默认与左边同名(比的是"同一个库在不同环境")
    let r = right
    if (!rightChoices.some((c) => c.id === rightId)) {
      r = rightChoices[0]
      setRightId(r?.id ?? 0)
    }
    setRightDb(l.family === 'oracle' ? (r?.database || l.database) : l.database)
  }, [leftId])
  useUpdateEffect(() => {
    if (right && left?.family === 'oracle') setRightDb(right.database)
  }, [rightId])

  useMountEffect(() => {
    void (async () => {
      try {
        const list = (await api.schemaDiffConnections()) ?? []
        setConns(list)
        if (list.length) setLeftId(list[0].id)
      } catch (e: any) {
        setLoadErr(e?.message || t('loadFailed'))
      }
    })()
  })

  // ---------------------------------------------------------------- 对比

  const [running, setRunning, runningRef] = useStateRef(false)
  const [result, setResult] = useState<SchemaDiffResp | null>(null)
  const [failed, setFailed] = useState('')
  const canRun = !!left && !!right && !running

  async function run() {
    if (!canRun || runningRef.current) return
    setRunning(true)
    setFailed('')
    setResult(null)
    setExpanded(new Set())
    try {
      const r = await api.schemaDiff({
        left: { connectionId: leftId, database: leftDb.trim(), schema: isPg ? leftSchema.trim() : '' },
        right: { connectionId: rightId, database: rightDb.trim(), schema: isPg ? rightSchema.trim() : '' },
        options: opts,
      })
      if (r.error) setFailed(readFailure(r))
      else setResult(r)
    } catch (e: any) {
      // 对比前的拒绝(无权访问一边、引擎不同……)同样带原因代码
      setFailed(reasonText(e?.data?.reasonRef, e?.message || t('actionFailed')))
    } finally {
      setRunning(false)
    }
  }

  // 原因按读者语言说:认得 code 就用词条,认不得回落到服务端的中文原话。side 参数(left / right)
  // 换成读者语言的"左边 / 右边"。
  function reasonText(ref: RuleRef | undefined, fallback: string, side?: string): string {
    const key = ref ? `sdErr_${ref.code}` : ''
    if (!ref || !te(key)) return fallback
    const s = side || ref.args?.side
    return t(key, { ...(ref.args || {}), side: s ? t(s === 'right' ? 'sdRight' : 'sdLeft') : '' })
  }
  const readFailure = (r: SchemaDiffResp) => reasonText(r.errorRef, r.error || '', r.errorSide)

  // ---------------------------------------------------------------- 结果

  const [kw, setKw] = useState('')
  const q = kw.trim().toLowerCase()
  const match = (name: string) => !q || name.toLowerCase().includes(q)
  const d = result?.diff
  const changed = (d?.changed || []).filter((x) => match(x.table))
  const onlyLeft = (d?.onlyLeft || []).filter(match)
  const onlyRight = (d?.onlyRight || []).filter(match)
  const same = (d?.same || []).filter(match)
  const [showSame, setShowSame] = useState(false)
  const [expanded, setExpanded] = useState(() => new Set<string>())
  function toggle(name: string) {
    setExpanded((old) => {
      const s = new Set(old)
      if (s.has(name)) s.delete(name)
      else s.add(name)
      return s
    })
  }
  // MySQL 的列名 / 索引名不区分大小写(与服务端同一个规则)
  const ci = left?.family === 'mysql'
  const showDefault = (c?: { hasDefault: boolean; default: string }) => (!c ? '' : !c.hasDefault ? t('sdNoDefault') : c.default === '' ? "''" : c.default)
  const allSame = !!d && !d.onlyLeft?.length && !d.onlyRight?.length && !d.changed?.length

  // ---------------------------------------------------------------- 导出

  function fileBase(r: SchemaDiffResp) {
    return `schema-diff_${r.left.instance}_${r.right.instance}_${r.left.database}`.replace(/[^\w.-]+/g, '_')
  }
  function exportMd() {
    if (!result) return
    saveBlob(new Blob([diffToMarkdown(result, tt)], { type: 'text/markdown;charset=utf-8' }), fileBase(result) + '.md')
  }
  async function copyMd() {
    if (result && await copyText(diffToMarkdown(result, tt))) useUIStore.getState().notify(t('sdCopied'), 'success')
  }
  function exportCsv() {
    if (!result) return
    saveBlob(new Blob([diffToCSV(result, tt)], { type: 'text/csv;charset=utf-8' }), fileBase(result) + '.csv')
  }

  if (loadErr) return <div className="scy page"><div className="sd-msg err">{loadErr}</div></div>
  if (!conns.length) return <div className="scy page"><div className="sd-msg" data-testid="sd-no-conns">{t('sdNoConns')}</div></div>

  // 选择条的一边:实例之外的那两格(库、PG 的 schema)
  function dbAndSchema(side: 'left' | 'right') {
    const [id, db, setDb, sch, setSch] = side === 'left'
      ? [leftId, leftDb, setLeftDb, leftSchema, setLeftSchema] as const
      : [rightId, rightDb, setRightDb, rightSchema, setRightSchema] as const
    return (
      <>
        {dbPickable(id)
          ? <VSelect value={db} onChange={setDb} options={dbNames(id)} searchable creatable height="36px"
              placeholder={t('sdDbPh')} searchPlaceholder={t('sdDbSearchPh')} data-testid={`sd-${side}-db`} />
          : <input value={db ?? ''} onChange={(e) => setDb(e.target.value)} className="db" placeholder={t('sdDbPh')} title={dbError(id)} data-testid={`sd-${side}-db`} />}
        {isPg && (schPickable(id, db)
          ? <VSelect value={sch} onChange={(v) => setSch(fromSchOption(v))} options={schOptions(id, db)} searchable creatable
              height="36px" placeholder={t('sdSchemaPh')} searchPlaceholder={t('sdSchemaSearchPh')} data-testid={`sd-${side}-schema`} />
          : <input value={sch} onChange={(e) => setSch(e.target.value)} className="db" placeholder={t('sdSchemaPh')} title={schOf(id, db)?.error || ''} data-testid={`sd-${side}-schema`} />)}
      </>
    )
  }
  const optBox = (k: keyof SchemaDiffOptions, label: string) => (
    <label className="ck"><input checked={opts[k]} onChange={(e) => { const on = e.target.checked; setOpts((o) => ({ ...o, [k]: on })) }} type="checkbox" />{t(label)}</label>
  )

  return (
    <div className="scy page">
      <div className="v-head">
        <div className="v-head-l">
          <div className="v-eyebrow">SCHEMA DIFF</div>
          <div className="v-h1">{t('t_schemadiff')}</div>
          <div className="v-sub">{t('t_schemadiffSub')}</div>
        </div>
      </div>
      {/* 选择条 */}
      <section className="card sd-pick">
        <div className="side">
          <div className="sl">{t('sdLeft')}</div>
          <VSelect value={left ? connLabel(left) : ''} options={conns.map(connLabel)} searchable height="36px"
            data-testid="sd-left" onChange={(l) => setLeftId(byLabel(l)?.id ?? 0)} />
          {dbAndSchema('left')}
        </div>
        <ArrowLeftRight size={18} className="arrow" />
        <div className="side">
          <div className="sl">{t('sdRight')}</div>
          <VSelect value={right ? connLabel(right) : t('sdPickRight')} options={rightChoices.map(connLabel)} searchable height="36px"
            data-testid="sd-right" onChange={(l) => setRightId(byLabel(l)?.id ?? 0)} />
          {dbAndSchema('right')}
        </div>
        <div className="acts">
          {optBox('ignoreColumnOrder', 'sdIgnoreOrder')}
          {optBox('ignoreComments', 'sdIgnoreComments')}
          {optBox('ignoreIndexNames', 'sdIgnoreIndexNames')}
          <VButton variant="primary" height="36px" disabled={!canRun} data-testid="sd-run" onClick={run}>
            <Play size={13} />{running ? t('sdRunning') : t('sdRun')}
          </VButton>
        </div>
        {left && !rightChoices.length && <div className="hint">{t('sdNoPeer', { engine: left.engine })}</div>}
      </section>

      {failed && <div className="sd-msg err" data-testid="sd-error"><TriangleAlert size={14} />{failed}</div>}

      {result && d && (
        <>
          {/* 汇总 */}
          <section className="card sd-sum" data-testid="sd-summary">
            <div className="meta">
              <div><span className="tag l">{t('sdLeft')}</span><b>{result.left.instance}</b> · {(result.left.env ?? '').toUpperCase()} / {result.left.database}{result.left.schema ? '.' + result.left.schema : ''}{' '}
                <span className="dim">{t('sdTablesN', { n: d.summary.left })} · {t('sdReadAt', { t: fmtTime(result.left.readAt) })}</span></div>
              <div><span className="tag r">{t('sdRight')}</span><b>{result.right.instance}</b> · {(result.right.env ?? '').toUpperCase()} / {result.right.database}{result.right.schema ? '.' + result.right.schema : ''}{' '}
                <span className="dim">{t('sdTablesN', { n: d.summary.right })} · {t('sdReadAt', { t: fmtTime(result.right.readAt) })}</span></div>
            </div>
            <div className="counts">
              <span className="cnt same">{t('sdSame')} <b>{d.summary.same}</b></span>
              <span className="cnt ol">{t('sdOnlyLeft')} <b>{d.summary.onlyLeft}</b></span>
              <span className="cnt or">{t('sdOnlyRight')} <b>{d.summary.onlyRight}</b></span>
              <span className="cnt ch">{t('sdChanged')} <b>{d.summary.changed}</b></span>
            </div>
            <div className="tools">
              <div className="sbox"><Search size={13} /><input value={kw} onChange={(e) => setKw(e.target.value)} placeholder={t('sdSearchPh')} /></div>
              <VButton variant="secondary" height="32px" data-testid="sd-copy-md" onClick={copyMd}><FileText size={13} />{t('sdCopyMd')}</VButton>
              <VButton variant="secondary" height="32px" data-testid="sd-export-md" onClick={exportMd}><Download size={13} />Markdown</VButton>
              <VButton variant="secondary" height="32px" data-testid="sd-export-csv" onClick={exportCsv}><Download size={13} />CSV</VButton>
            </div>
          </section>

          {result.emptySide && <div className="sd-msg warn" data-testid="sd-empty"><TriangleAlert size={14} />{t('sdEmpty_' + result.emptySide)}</div>}
          {allSame && !result.emptySide && <div className="sd-msg ok" data-testid="sd-all-same"><CircleCheck size={15} />{t('sdAllSame', { n: d.summary.same })}</div>}

          {/* 结构不同 */}
          {changed.length > 0 && (
            <section className="card grp" data-testid="sd-changed">
              <div className="gh ch">{t('sdChanged')} · {changed.length}</div>
              {changed.map((td) => (
                <div key={td.table} className="tbl">
                  <button className="th" data-testid="sd-changed-table" onClick={() => toggle(td.table)}>
                    <ChevronRight size={14} className={clsx('chev', expanded.has(td.table) && 'open')} />
                    <span className="tn">{td.table}</span>
                    <span className="dim">{t('sdDiffCount', { n: (td.columns?.length || 0) + (td.indexes?.length || 0) + (td.kind ? 1 : 0) + (td.comment ? 1 : 0) + (td.primaryKey ? 1 : 0) })}</span>
                  </button>
                  {expanded.has(td.table) && (
                    <div className="cmp" data-testid="sd-detail">
                      {/* 表属性:表 / 视图、表注释、主键 —— 有差异的格子高亮 */}
                      <table className="dt">
                        <thead><tr><th className="w-obj"></th><th>{t('sdLeft')} · {result.left.instance}</th><th>{t('sdRight')} · {result.right.instance}</th></tr></thead>
                        <tbody>
                          <tr><td className="obj">{t('sdAttr_kind')}</td>
                            <td className={clsx(td.kind && 'hl')}>{td.leftDef?.kind}</td><td className={clsx(td.kind && 'hl')}>{td.rightDef?.kind}</td></tr>
                          <tr><td className="obj">{t('sdAttr_comment')}</td>
                            <td className={clsx(td.comment && 'hl')}>{td.leftDef?.comment || '—'}</td><td className={clsx(td.comment && 'hl')}>{td.rightDef?.comment || '—'}</td></tr>
                          <tr><td className="obj">{t('sdPrimaryKey')}</td>
                            <td className={clsx(td.primaryKey && 'hl')}>{td.leftDef?.primaryKey?.length ? '(' + td.leftDef.primaryKey.join(', ') + ')' : '—'}</td>
                            <td className={clsx(td.primaryKey && 'hl')}>{td.rightDef?.primaryKey?.length ? '(' + td.rightDef.primaryKey.join(', ') + ')' : '—'}</td></tr>
                        </tbody>
                      </table>

                      {/* 列:左右对照,不同的属性格高亮,只在一边的整行标色 */}
                      <table className="dt cols">
                        <thead>
                          <tr><th rowSpan={2} className="w-obj">{t('sdColumn')}</th>
                            <th colSpan={4} className="sh-l">{t('sdLeft')} · {result.left.instance}</th>
                            <th colSpan={4} className="sh-r">{t('sdRight')} · {result.right.instance}</th></tr>
                          <tr><th>{t('sdAttr_type')}</th><th>{t('sdAttr_nullable')}</th><th>{t('sdAttr_default')}</th><th>{t('sdAttr_comment')}</th>
                            <th>{t('sdAttr_type')}</th><th>{t('sdAttr_nullable')}</th><th>{t('sdAttr_default')}</th><th>{t('sdAttr_comment')}</th></tr>
                        </thead>
                        <tbody>
                          {columnPairs(td, ci).map((row) => (
                            <tr key={row.name} className={row.only ? 'only-' + row.only : ''} data-testid="sd-col-row">
                              <td className="obj">{row.name}{row.diff.has('position') && <span className="pos">{t('sdMoved')}</span>}</td>
                              {([['left', row.left], ['right', row.right]] as const).map(([side, c]) => (
                                <Fragment key={side}>
                                  {c ? (
                                    <>
                                      <td className={clsx(row.diff.has('type') && 'hl')}>{c.dataType}</td>
                                      <td className={clsx(row.diff.has('nullable') && 'hl')}>{c.nullable ? 'NULL' : 'NOT NULL'}</td>
                                      <td className={clsx(row.diff.has('default') && 'hl')}>{showDefault(c)}</td>
                                      <td className={clsx(row.diff.has('comment') && 'hl')}>{c.comment}</td>
                                    </>
                                  ) : <td colSpan={4} className="miss">{t('sdAbsent')}</td>}
                                </Fragment>
                              ))}
                            </tr>
                          ))}
                        </tbody>
                      </table>

                      {/* 索引:两边各列自己的,按服务端的差异标色(不在前端按名字配对) */}
                      <div className="idx">
                        {Object.entries(indexMarks(td, ci)).map(([side, list]) => (
                          <div key={side} className="idxcol">
                            <div className="idxh">{t('sdIndex')} · {side === 'left' ? result.left.instance : result.right.instance}</div>
                            {!list.length && <div className="idxi miss">—</div>}
                            {list.map((m) => (
                              <div key={m.index.name} className={clsx('idxi', m.status)} data-testid="sd-index">
                                <span className="nm2">{m.index.name}</span><span>{showIndexDef(m.index)}</span>
                              </div>
                            ))}
                          </div>
                        ))}
                      </div>
                    </div>
                  )}
                </div>
              ))}
            </section>
          )}

          <div className="two">
            {onlyLeft.length > 0 && (
              <section className="card grp" data-testid="sd-only-left">
                <div className="gh ol">{t('sdOnlyLeftOf', { n: result.left.instance })} · {onlyLeft.length}</div>
                <div className="names">{onlyLeft.map((n) => <span key={n} className="nm">{n}</span>)}</div>
              </section>
            )}
            {onlyRight.length > 0 && (
              <section className="card grp" data-testid="sd-only-right">
                <div className="gh or">{t('sdOnlyRightOf', { n: result.right.instance })} · {onlyRight.length}</div>
                <div className="names">{onlyRight.map((n) => <span key={n} className="nm">{n}</span>)}</div>
              </section>
            )}
          </div>

          {same.length > 0 && (
            <section className="card grp">
              <button className="gh same btn" onClick={() => setShowSame((v) => !v)}>
                <ChevronRight size={14} className={clsx('chev', showSame && 'open')} />{t('sdSame')} · {same.length}
              </button>
              {showSame && <div className="names">{same.map((n) => <span key={n} className="nm">{n}</span>)}</div>}
            </section>
          )}
        </>
      )}
    </div>
  )
}
