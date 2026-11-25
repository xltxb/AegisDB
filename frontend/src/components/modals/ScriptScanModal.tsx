import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import clsx from 'clsx'
import { FileSearch, X, ShieldAlert, ShieldCheck, TriangleAlert, Hourglass, FolderArchive, Database, CircleCheck, Loader2 } from 'lucide-react'
import VButton from '@/components/common/VButton'
import { useI18n } from '@/hooks/useI18n'
import type { ScriptProgress, ScriptScanResp } from '@/types'
import { localizeText } from '@/api/http'
import './ScriptScanModal.scoped.css'

interface Props {
  open: boolean
  scan: ScriptScanResp | null
  submitted: boolean
  savePath?: string
  instance?: string
  databases?: string[]
  targetDb?: string
  /** 正在下发中 —— 按钮要立刻锁住,而不是等响应回来 */
  running?: boolean
  /** 这次弹窗里已经跑过/提交过了。带上时刻与执行人,让人看得出是"刚刚自己点的"。 */
  done?: { at: string; by: string; kind: 'ran' | 'submitted' } | null
  /** 下发中的进度(轮询 /scripts/progress 得来;还没拿到时为 null,遮罩退回转圈) */
  progress?: ScriptProgress | null
  onClose: () => void
  onRun: () => void
  onUpdateTargetDb: (v: string) => void
}

function badgeMeta(r: string) {
  if (r === 'high') return { bg: 'var(--danger-subtle)', c: 'var(--danger-text)', t: 'scHigh' }
  if (r === 'mid') return { bg: 'var(--warning-subtle)', c: 'var(--warning-text)', t: 'scMid' }
  return { bg: 'var(--surface-sunken)', c: 'var(--text-muted)', t: 'scSafe' }
}

export default function ScriptScanModal({ open, scan, ...rest }: Props) {
  return open && scan ? <Content scan={scan} {...rest} /> : null
}

function Content({
  scan, submitted, savePath, instance, databases, targetDb, running, done, progress, onClose, onRun, onUpdateTargetDb,
}: Omit<Props, 'open' | 'scan'> & { scan: ScriptScanResp }) {
  const { t } = useI18n()

  const risky = scan.hasRisky ?? false
  // A target database must be chosen before the script can be dispatched.
  const db = targetDb || ''
  // 能不能点执行:选了库、没有正在跑、这次还没跑过。
  // 三个条件缺一不可 —— 少了后两个,连点两下就是两次真的执行。
  // 有语句被禁止:整份脚本不会执行,也不会送审 —— 按钮直接灰掉,原因写在横幅上,
  // 不让人点下去才收到一句拒绝。
  const blocked = scan.blocked ?? false
  const canRun = !!db && !running && !done && !blocked

  // 下发期间整个弹窗都不接受操作 —— 关闭也不行。
  //
  // 按钮变灰挡得住"再点一次执行",挡不住"关掉再来一次":重新扫一遍脚本会把
  // scRunning / scDone 一起重置(见 TerminalSession 里 scriptScan 之后那两行),
  // 于是上一次还在飞的下发就没人记得了。这个遮罩把这条路一起封上。
  //
  // 不怕把人困住:scriptExecute 带的是 SCRIPT_TIMEOUT_MS 这个有界超时,
  // 无论成败 finally 都会把 running 放掉,遮罩跟着消失。
  function tryClose() {
    if (running) return
    onClose()
  }
  const banner = blocked
    ? { text: 'scBlocked', bg: 'var(--danger-subtle)', color: 'var(--danger-text)', icon: ShieldAlert }
    : risky
      ? { text: 'scRisky', bg: 'var(--danger-subtle)', color: 'var(--danger-text)', icon: ShieldAlert }
      // 本该审批的语句由执行窗口放行:说出是哪个窗口,而不是笼统的"未检出需审批的语句"
      // —— 高危条数照样显示在上面,两句话放一起读才不矛盾。
      : scan.window
        ? { text: 'scWindow', bg: 'var(--success-subtle)', color: 'var(--success-text)', icon: ShieldCheck }
        : { text: 'scClean', bg: 'var(--success-subtle)', color: 'var(--success-text)', icon: ShieldCheck }
  const BannerIcon = banner.icon

  // 执行进度卡片(2026-10):下发期间的遮罩不只转圈,说出跑到第几条、正在跑哪条、跑了多久。
  // 复扫(scan / judge)与逐条执行(run)两段:复扫很快,条到 run 才是主要的那一段。
  const runPct = !progress || !progress.total ? 0 : Math.min(100, Math.round((progress.done / progress.total) * 100))
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!running) return
    setNow(Date.now())
    const tick = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(tick)
  }, [running])
  const startedAt = progress?.startedAt ? Date.parse(progress.startedAt) : NaN
  const runElapsed = Number.isNaN(startedAt) ? 0 : Math.max(0, Math.floor((now - startedAt) / 1000))

  // 选中的库不在候选里时(会话开着时新建的库、连接上写死的库),控件留空。React 的受控 select
  // 在没有匹配项时会退到第一个可选项 —— 看着像选了 A,实际下发到 B,所以每次渲染后纠正。
  const sel = useRef<HTMLSelectElement>(null)
  useLayoutEffect(() => {
    if (sel.current && sel.current.value !== db) sel.current.selectedIndex = -1
  })

  return (
    <div className="scan-overlay">
      <div className="scan-mask" onClick={tryClose} />
      <div className="modal">
        <div className="topbar" />
        <div className="pad">
          <div className="hdr">
            <div className="ic"><FileSearch size={21} color="var(--accent-text)" /></div>
            <div className="grow">
              <div className="titlerow"><div className="title">{t('scTitle')}</div><span className="fn">{scan.filename}</span></div>
              <div className="desc">{t('scSub')}</div>
            </div>
            <div className={clsx('x', running && 'off')} onClick={tryClose}><X size={18} /></div>
          </div>

          <div className="stats">
            <div className="stat"><div className="n">{scan.total}</div><div className="l">{t('scTotal')}</div></div>
            <div className="stat danger"><div className="n">{scan.high}</div><div className="l">{t('scHigh')}</div></div>
            <div className="stat warn"><div className="n">{scan.mid}</div><div className="l">{t('scMid')}</div></div>
            <div className="stat"><div className="n green">{scan.safe}</div><div className="l">{t('scSafe')}</div></div>
          </div>

          <div className="banner" style={{ background: banner.bg }}>
            <BannerIcon size={17} color={banner.color} />
            <span style={{ color: banner.color }} data-testid="sc-banner">{t(banner.text, { w: scan.window ?? '' })}{blocked && scan.blockedRule && <> · {localizeText(scan.blockedRule, scan.blockedRuleRef)}</>}</span>
          </div>

          {savePath && <div className="archive"><FolderArchive size={13} /><span className="al">{t('scArchiveAt')}</span><code className="ap">{savePath}</code></div>}

          {/* required: choose the target database within the instance */}
          <div className="target">
            <Database size={14} color="var(--accent-text)" />
            <span className="tl">{t('scTargetDb')}</span>
            <select ref={sel} value={db} onChange={(e) => onUpdateTargetDb(e.target.value)} className="tsel">
              <option value="" disabled>{t('scPickDb')}</option>
              {(databases || []).map((d) => <option key={d} value={d}>{d}</option>)}
            </select>
            <span className="tinst">{instance}</span>
          </div>

          <div className="eyebrow">{t('scStmtList')}</div>
        </div>

        <div className="scy list-wrap">
          <div className="list">
            {(scan.statements ?? []).map((s) => (
              <div key={s.index} className="srow" style={{ background: s.risk === 'high' ? 'rgba(240,71,62,.05)' : 'transparent' }}>
                <span className="idx">{String(s.index).padStart(2, '0')}</span>
                <div className="sgrow">
                  <div className="sql">{s.sql}</div>
                  {s.noWhere && <div className="nw"><TriangleAlert size={11} />{t('scNoWhere')}</div>}
                </div>
                <span className="bdg" style={{ background: badgeMeta(s.risk).bg, color: badgeMeta(s.risk).c }}>{t(badgeMeta(s.risk).t)}</span>
              </div>
            ))}
          </div>
        </div>

        {submitted && <div className="submitted"><Hourglass size={16} color="var(--warning-text)" /><span>{t('scSubmitted')}</span></div>}

        <div className="footer">
          <div className="fl">
            {canRun
              ? <span className="confirm">{t('scConfirm', { i: instance, d: db })}</span>
              : <span className="mustpick">{t('scMustPick')}</span>}
          </div>
          <div className="acts">
            {/* 已经跑过就把话说清楚:什么时候、谁点的。一个只是变灰的按钮,人只会
                再点两下然后以为界面卡住了。 */}
            {done && (
              <span className="ranmark">
                <CircleCheck size={13} />
                {' '}{done.kind === 'submitted' ? t('scAlreadySubmitted', { at: done.at, by: done.by }) : t('scAlreadyRan', { at: done.at, by: done.by })}
              </span>
            )}
            <VButton variant="secondary" disabled={running} onClick={tryClose}>{t('scClose')}</VButton>
            <VButton variant="primary" disabled={!canRun} onClick={() => { if (canRun) onRun() }}>
              {running ? t('scRunning') : done ? t('scDone') : risky ? t('scSubmitAppr') : t('scRun')}
            </VButton>
          </div>
        </div>

        {/* 下发中的遮罩。它盖住的是**整个弹窗**,不只是那颗按钮:这段时间里没有
            任何一个操作是有意义的,而一颗变灰的按钮旁边仍然可点的关闭/下拉框,
            只会让人以为界面卡住了去乱点。 */}
        {running && (
          <div className="busy" data-testid="sc-busy">
            {/* 拿到进度且在逐条执行:进度卡片;送审、复扫、还没拿到进度时退回转圈 */}
            {!risky && progress && progress.total ? (
              <div className="pcard" data-testid="sc-progress">
                <div className="ph">
                  <Loader2 className="spin" size={16} color="var(--accent-text)" />
                  <span className="pt">{progress.phase === 'run' ? t('scProgRun', { d: progress.done, n: progress.total }) : t('scProgCheck', { d: progress.done, n: progress.total })}</span>
                  <span className="pp">{progress.phase === 'run' ? runPct + '%' : ''}</span>
                </div>
                <div className="pbar"><div className="pfill" style={{ width: (progress.phase === 'run' ? runPct : 0) + '%' }} /></div>
                {progress.current && <div className="pcur" title={progress.current}>{progress.phase === 'run' ? '#' + Math.min(progress.done + 1, progress.total) + ' ' : ''}{progress.current}</div>}
                <div className="pmeta"><span>{t('scProgElapsed', { s: runElapsed })}</span><span>{t('scBusyHint')}</span></div>
              </div>
            ) : (
              <>
                <Loader2 className="spin" size={26} color="var(--accent-text)" />
                <div className="bt">{risky ? t('scBusySubmit') : t('scBusyRun')}</div>
                <div className="bs">{t('scBusyHint')}</div>
              </>
            )}
          </div>
        )}
      </div>
    </div>
  )
}
