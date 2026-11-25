import { useEffect, useState } from 'react'
import clsx from 'clsx'
import { TriangleAlert, Hammer, X, CircleCheck, CircleX, Layers } from 'lucide-react'
import api from '@/api'
import { confirmAction } from '@/lib/confirm'
import { CODE_MFA_REQUIRED } from '@/api/http'
import { useUIStore } from '@/stores/ui'
import { useEvent } from '@/hooks/useEvent'
import { useI18n } from '@/hooks/useI18n'
import { useStateRef } from '@/hooks/useStateRef'
import type { InvalidObject, RecompileReport } from '@/types'
import './InvalidObjects.scoped.css'

// 一个 Oracle schema 下的 INVALID 对象,以及"一键全编"。
//
// 只在**有**无效对象时出现。一行"0 个无效对象"挂在每个 schema 下面,在一棵已经很密的
// 树里是纯噪音;而这一行是红的、有数字、能点 —— 它出现本身就是那条信息。
//
// 编译是一批 DDL,所以点下去之前要确认:后端会把整批一起判定,判不过整批都不执行,
// 但"我以为只是刷新一下清单"这种误触,得在前端就挡住。
interface Props { cid: number; scope: string; database?: string; oracle: boolean }

const keyOf = (o: InvalidObject) => o.kind + '|' + o.name

export default function InvalidObjects({ cid, scope, database, oracle }: Props) {
  const { t } = useI18n()

  const [items, setItems] = useState<InvalidObject[] | null>(null) // null = 还没查 / 正在查
  const [err, setErr] = useState('')
  const [busy, setBusy, busyRef] = useStateRef(false)
  const [report, setReport] = useState<RecompileReport | null>(null)
  // 生产实例上批量重编译同样要二次验证 —— 它一次改的对象比单个编译更多,没有理由更宽松。
  // 后端答 42800 时亮出输码框重试,而不是把它当成一条普通错误。
  const [needMfa, setNeedMfa] = useState(false)
  const [mfaCode, setMfaCode, mfaCodeRef] = useStateRef('')

  // 勾选:全量重编译是变更窗口里的动作,勾几个是定点修复 —— 后端早就分这两种
  // (RecompileReq.names),界面上原来只给了"全部"。勾了就编勾的,一个没勾就是全部。
  const [picked, setPicked] = useState<Set<string>>(new Set())
  function toggle(o: InvalidObject) {
    const k = keyOf(o)
    const next = new Set(picked)
    if (next.has(k)) next.delete(k)
    else next.add(k)
    setPicked(next)
  }
  const selected = (items || []).filter((o) => picked.has(keyOf(o)))

  const load = useEvent(async () => {
    if (!oracle) return
    setErr('')
    try {
      const r = await api.invalidObjects(cid, scope, database || '')
      const list = r.items || []
      setItems(list)
      // 清单变了,勾选只保留还在清单里的:编好的那几个不该还"勾着"。
      const alive = new Set(list.map(keyOf))
      setPicked((p) => new Set([...p].filter((k) => alive.has(k))))
    } catch (e: any) {
      // 清点失败要说出来,而不是静默当成"没有" —— 静默的表现是这一行不出现,
      // 读起来正好是"这个 schema 很干净"。
      setItems([])
      setErr(e?.message || t('invLoadFail'))
    }
  })
  useEffect(() => { void load() }, [cid, scope, database, oracle, load])

  async function recompile() {
    if (busyRef.current || !items?.length) return
    const targets = selected.length ? selected : items
    const schema = items[0].owner
    const msg = selected.length
      ? t('invConfirmSel', { n: targets.length, total: items.length, schema })
      : t('invConfirm', { n: targets.length, schema })
    if (!(await confirmAction(msg))) return
    setBusy(true)
    try {
      const code = mfaCodeRef.current.trim()
      const r = await api.recompileInvalid(cid, {
        scope, database: database || '',
        names: selected.length ? targets.map((o) => o.name) : undefined,
        mfaCode: code,
      })
      if (r.code === CODE_MFA_REQUIRED) {
        // 输过一次还回 42800 就是码不对;第一次则只是亮出输入框,不该报成错误。
        if (code) useUIStore.getState().notify(t('mfaBadCode'), 'error', 4000)
        setNeedMfa(true)
        setMfaCode('')
        return
      }
      if (r.code !== 0) {
        useUIStore.getState().notify(r.msg || t('actionFailed'), 'error', 6000)
        return
      }
      setNeedMfa(false)
      setMfaCode('')
      setReport(r.data.report)
      await load() // 清单按编译后的真实状态重取,而不是按"我以为都修好了"更新
    } catch (e: any) {
      useUIStore.getState().notifyError(e, t('actionFailed'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      {oracle && err ? <div className="inv-hint err">{err}</div> : oracle && items && items.length > 0 ? (
        <div className="inv">
          <div className="inv-row">
            <TriangleAlert size={12} />
            <span className="inv-t">{t('invTitle')}</span>
            <span className="inv-n">{items.length}</span>
            <button className="inv-btn" disabled={busy} data-testid="inv-recompile" onClick={(e) => { e.stopPropagation(); void recompile() }}>
              <Hammer size={11} />{busy ? t('invRunning') : selected.length ? t('invRecompileSel', { n: selected.length }) : t('invRecompile')}
            </button>
          </div>
          {/* 生产实例上要二次验证:窄而安静的一条,它是流程里的一步,不是一次报错。 */}
          {needMfa && (
            <div className="inv-mfa">
              <span className="inv-mfa-t">{t('mfaStepTitle')}</span>
              <input
                value={mfaCode} onChange={(e) => setMfaCode(e.target.value)} className="inv-mfa-i" inputMode="numeric" maxLength={6}
                placeholder={t('mfaCodeLabel')} onClick={(e) => e.stopPropagation()} onKeyUp={(e) => { if (e.key === 'Enter') void recompile() }}
              />
              <button className="inv-mfa-b" disabled={busy || mfaCode.trim().length !== 6} onClick={(e) => { e.stopPropagation(); void recompile() }}>
                {t('mfaVerify')}
              </button>
            </div>
          )}
          {items.map((o) => (
            <label key={o.kind + o.name} className={clsx('inv-obj', picked.has(keyOf(o)) && 'on')} title={o.units?.join(' · ')} onClick={(e) => e.stopPropagation()}>
              <input type="checkbox" className="io-ck" checked={picked.has(keyOf(o))} onChange={() => toggle(o)} />
              <span className="io-kind">{o.kind}</span>{o.name}
            </label>
          ))}
        </div>
      ) : null}

      {/* 结果。逐个列,而不是只给一句"完成" —— 编不过的那几个才是要看的东西。 */}
      {report && (
        <div className="rc-overlay" onClick={(e) => { if (e.target === e.currentTarget) setReport(null) }}>
          <div className="rc-mask" />
          <div className="rc-modal">
            <div className="rc-hdr">
              <div className="rc-title">{t('invReportTitle', { schema: report.owner })}</div>
              <div className="rc-x" onClick={() => setReport(null)}><X size={16} /></div>
            </div>
            <div className="rc-sum">
              <span className="rc-chip ok"><CircleCheck size={12} />{t('invFixed', { n: report.fixed })}</span>
              <span className={clsx('rc-chip', report.failed && 'bad')}><CircleX size={12} />{t('invFailed', { n: report.failed })}</span>
              {!!report.skipped && <span className="rc-chip warn">{t('invSkipped', { n: report.skipped })}</span>}
              {!!report.cyclic && <span className="rc-chip warn" title={t('invCyclicTip')}>{t('invCyclic', { n: report.cyclic })}</span>}
              <span className="rc-meta"><Layers size={11} />{t('invLevels', { n: report.levels })} · {t('invPasses', { n: report.passes })} · {report.ms}ms</span>
            </div>
            {/* 依赖图没读到:这次没有分层,只靠多轮。人看到"3 轮还剩很多"时要知道这一点。 */}
            {report.depsNote && <div className="rc-note">{t('invDepsNote')}<span className="rc-note-d">{report.depsNote}</span></div>}
            <div className="rc-body scy">
              {(report.items || []).map((it) => (
                <div key={it.kind + it.name} className={clsx('rc-item', it.status.toLowerCase())}>
                  <div className="ri-head">
                    <span className={clsx('ri-status', it.status.toLowerCase())}>{it.status}</span>
                    <span className="ri-kind">{it.kind}</span>
                    <span className="ri-name">{it.name}</span>
                    <span className={clsx('ri-lv', it.cyclic && 'cyc')} title={it.cyclic ? t('invCyclicTip') : ''}>{t('invLevel', { n: it.level })}{it.passes > 1 && <> · {t('invPassN', { n: it.passes })}</>}</span>
                  </div>
                  {it.err && <div className="ri-err">{it.err}</div>}
                  {(it.errors || []).map((d, i) => (
                    <div key={i} className="ri-diag">
                      <span className="ri-loc">{d.type} {d.line}:{d.position}</span>{d.text}
                    </div>
                  ))}
                </div>
              ))}
            </div>
          </div>
        </div>
      )}
    </>
  )
}
