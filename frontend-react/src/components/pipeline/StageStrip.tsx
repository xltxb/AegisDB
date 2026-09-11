import clsx from 'clsx'
import { useTranslation } from 'react-i18next'
import {
  Bell, Bot, CircleCheckBig, CircleX, ClipboardCheck, DatabaseBackup, Hourglass,
  LoaderCircle, Minus, Search, ShieldCheck, Terminal,
} from 'lucide-react'
import type { RunStatus, StageType } from '@/types'

/**
 * 阶段条 —— 变更工单与流程配置共用的那一块。
 *
 * 两页看的是同一个东西的两面:配置页看**顺序**(阶段还没有状态),工单页看
 * **走到哪了**(每个阶段带着一次真实运行的状态)。所以 `status` 是可选的:
 * 不给就是模板预览,给了才画状态。写成两个组件的话,某一天改了节点间距,
 * 另一页就会跟着对不上。
 */

export const STAGE_TYPES: StageType[] = [
  'review', 'approve', 'backup', 'execute', 'verify', 'manual', 'notify',
]

const TYPE_ICON: Record<StageType, typeof ShieldCheck> = {
  review: ShieldCheck,
  approve: ClipboardCheck,
  backup: DatabaseBackup,
  execute: Terminal,
  verify: Search,
  manual: Hourglass,
  notify: Bell,
}

/** 阶段类型的图标。类型是后端给的字符串,拿不准的画个机器人,总比空着强。 */
export function StageTypeIcon({ type, size = 13 }: { type: StageType; size?: number }) {
  const Icon = TYPE_ICON[type] ?? Bot
  return <Icon size={size} />
}

interface StatusLook {
  Icon: typeof ShieldCheck
  /** 语义 token 名,节点的底色与图标色都从它来 */
  tone: 'accent' | 'success' | 'warning' | 'danger' | 'muted'
  spin: boolean
}

/**
 * 状态的样子。
 *
 * 原型只画了两种(转圈 / 打勾),但真实运行里还有"等人"和"失败"——
 * 给一个失败的阶段画绿勾是在说谎,所以这两种各留了自己的图标与色调,
 * 其余按原型收敛。
 */
export function stageStatusLook(status: RunStatus): StatusLook {
  switch (status) {
    case 'running': return { Icon: LoaderCircle, tone: 'accent', spin: true }
    case 'success': return { Icon: CircleCheckBig, tone: 'success', spin: false }
    case 'waiting': return { Icon: Hourglass, tone: 'warning', spin: false }
    case 'failed': return { Icon: CircleX, tone: 'danger', spin: false }
    case 'aborted': return { Icon: CircleX, tone: 'muted', spin: false }
    default: return { Icon: Minus, tone: 'muted', spin: false }
  }
}

export interface StageNode {
  key: string | number
  name: string
  type: StageType
  /** 不给 = 模板预览,只画顺序不画状态 */
  status?: RunStatus
  /** 节点底下的第二行,通常是耗时 */
  meta?: string
}

/**
 * 两个阶段之间那根线报的是**已经流过去多少**:跑完的那段实心,正在跑的那段
 * 有动画,前面还没轮到的是一条灰线。统一画一条细线的话,一条还没开始的流程
 * 和一条跑了一半的流程长得一模一样。
 */
function connectorClass(stages: StageNode[], i: number): string {
  const prev = stages[i - 1]?.status
  if (prev !== 'success' && prev !== 'skipped') return ''
  return stages[i].status === 'running' ? 'live' : 'done'
}

export function StageStrip({
  stages, selectedKey, onSelect, compact,
}: {
  stages: StageNode[]
  selectedKey?: string | number
  onSelect?: (s: StageNode) => void
  /** 列表里的缩略版:只有图标,没有名字与状态文字 */
  compact?: boolean
}) {
  const { t } = useTranslation()

  if (compact) {
    return (
      <span className="sg-mini">
        {stages.map((s) => (
          <span key={s.key} className="sg-mini-dot"><StageTypeIcon type={s.type} size={11} /></span>
        ))}
      </span>
    )
  }

  return (
    <div className="sg-strip">
      {stages.map((s, i) => {
        const look = s.status ? stageStatusLook(s.status) : null
        return (
          <div className="sg-cell" key={s.key}>
            {i > 0 && <div className={clsx('sg-conn', connectorClass(stages, i))} />}
            <button
              type="button"
              className={clsx('sg-node', selectedKey === s.key && 'on', !onSelect && 'static')}
              onClick={onSelect ? () => onSelect(s) : undefined}
            >
              <span
                className={clsx('sg-dot', look ? `tone-${look.tone}` : 'tone-plain', s.status === 'running' && 'halo')}
              >
                {look
                  ? <look.Icon size={16} className={clsx(look.spin && 'spin')} />
                  : <StageTypeIcon type={s.type} size={16} />}
              </span>
              <span className="sg-name">
                <StageTypeIcon type={s.type} size={11} />
                {s.name || t(`stType_${s.type}`)}
              </span>
              {s.status && (
                <span className={clsx('sg-st', look && `tone-${look.tone}`)}>
                  {t(`stRun_${s.status}`)}
                  {s.meta && <span className="sg-meta"> · {s.meta}</span>}
                </span>
              )}
            </button>
          </div>
        )
      })}
    </div>
  )
}
