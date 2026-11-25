import { useEffect, useState } from 'react'
import clsx from 'clsx'
import { FileUp, CircleCheck, Loader2, Circle } from 'lucide-react'
import VButton from '@/components/common/VButton'
import { useI18n } from '@/hooks/useI18n'
import type { ScriptProgress } from '@/types'
import './ScriptPrepModal.scoped.css'

// 选好脚本文件之后、扫描报告出来之前的进度页(2026-10)。
//
// 从前选完文件界面上什么都没有:浏览器读文件、把整份脚本发上去、服务端逐条扫描判定,一份几 MB
// 的脚本要十几秒,人只能干等,分不清是在跑还是没反应。这里把三步摆出来,各自给进度:
//   读取文件(浏览器本地,百分比)→ 上传(请求体,百分比)→ 扫描(服务端逐条,第 i / N 条)。
// 从「已上传脚本」里选的、示例脚本,文件已经在服务器上或就在页面里,只有扫描这一步。
interface Props {
  open: boolean
  filename: string
  /** 当前在哪一步 */
  step: StepKey
  readPct: number
  uploadPct: number
  /** 是否有"读取文件 / 上传"两步(选本地文件时才有) */
  fileSteps: boolean
  /** 服务端扫描进度(轮询来的;还没拿到时为 null) */
  progress: ScriptProgress | null
  onCancel: () => void
}

type StepKey = 'read' | 'upload' | 'scan'
const order: StepKey[] = ['read', 'upload', 'scan']

export default function ScriptPrepModal({ open, ...rest }: Props) {
  return open ? <Content {...rest} /> : null
}

function Content({ filename, step, readPct, uploadPct, fileSteps, progress, onCancel }: Omit<Props, 'open'>) {
  const { t } = useI18n()

  const steps = fileSteps ? order : (['scan'] as StepKey[])
  function state(s: StepKey): 'done' | 'active' | 'todo' {
    const cur = order.indexOf(step)
    const i = order.indexOf(s)
    return i < cur ? 'done' : i === cur ? 'active' : 'todo'
  }

  // 扫描分两段:字典扫描(scan)之后逐条判定(judge)。两段各占一半进度条,人看到的是一根一直往前走的条。
  function scanPct() {
    if (!progress || !progress.total) return 0
    const part = Math.min(1, progress.done / progress.total)
    return Math.round((progress.phase === 'judge' ? 0.5 + part / 2 : part / 2) * 100)
  }
  function pctOf(s: StepKey) {
    if (state(s) === 'done') return 100
    if (state(s) === 'todo') return 0
    return s === 'read' ? readPct : s === 'upload' ? uploadPct : scanPct()
  }

  // 已用时间:每秒走一格。开着才走,关了就停 —— 不在后台空转。
  const [openedAt] = useState(() => Date.now())
  const [now, setNow] = useState(openedAt)
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [])
  const elapsed = Math.max(0, Math.floor((now - openedAt) / 1000))

  function valueText(s: StepKey) {
    const st = state(s)
    if (st === 'done') return t('spDone')
    if (st !== 'active') return null
    if (s !== 'scan') return `${pctOf(s)}%`
    if (progress && progress.total) return t(progress.phase === 'judge' ? 'spJudging' : 'spScanning', { d: progress.done, n: progress.total })
    return t('spWaiting')
  }

  return (
    <div className="prep-overlay" data-testid="script-prep">
      <div className="prep-mask" />
      <div className="modal">
        <div className="topbar" />
        <div className="pad">
          <div className="hdr">
            <div className="ic"><FileUp size={21} color="var(--accent-text)" /></div>
            <div className="grow">
              <div className="titlerow"><div className="title">{t('spTitle')}</div><span className="fn">{filename}</span></div>
              <div className="desc">{t('spSub')}</div>
            </div>
          </div>

          <div className="steps">
            {steps.map((s) => (
              <div key={s} className={clsx('step', state(s))} data-testid={'sp-step-' + s}>
                <div className="srow">
                  {state(s) === 'done' ? <CircleCheck size={16} className="si done" />
                    : state(s) === 'active' ? <Loader2 size={16} className="si spin" />
                      : <Circle size={16} className="si" />}
                  <span className="sname">{t('spStep_' + s)}</span>
                  <span className="sval" data-testid={'sp-val-' + s}>{valueText(s)}</span>
                </div>
                <div className="bar"><div className="fill" style={{ width: pctOf(s) + '%' }} /></div>
                {s === 'scan' && state(s) === 'active' && progress?.current && <div className="cur" title={progress.current}>{progress.current}</div>}
              </div>
            ))}
          </div>
        </div>
        <div className="footer">
          <span className="el" data-testid="sp-elapsed">{t('spElapsed', { s: elapsed })}</span>
          <VButton variant="secondary" data-testid="sp-cancel" onClick={onCancel}>{t('spCancel')}</VButton>
        </div>
      </div>
    </div>
  )
}
