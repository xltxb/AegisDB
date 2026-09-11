import { useTranslation } from 'react-i18next'
import clsx from 'clsx'
import { CircleCheck, Lock, Minus } from 'lucide-react'
import type { CapLevel } from '@/types'

/**
 * 八个能力维度,顺序照后端 `model.Capabilities`。
 *
 * **八个,不是七个**。`release` 是最后加的一维,它管的是"能不能在这个分层**发起**
 * 一张发布单",和单子里那条语句能不能跑是两个问题(流水线执行时还会再问一次
 * write/ddl)。Vue 版的 capKeys 漏了它 —— 标签数组写了八个、键数组写了七个,于是
 * 表上永远只有七行,而 release 那一行的判定照常在网关里生效:界面上看不见,也就
 * 改不了(GitHub issue #39)。这份清单与 CAP_LABEL 的键必须一一对应。
 */
export const CAPABILITIES = [
  'select', 'write', 'ddl', 'grant', 'conn', 'approve', 'explain', 'release',
] as const

export type Capability = (typeof CAPABILITIES)[number]

export const CAP_LABEL: Record<Capability, string> = {
  select: 'capSelect',
  write: 'capWrite',
  ddl: 'capDdl',
  grant: 'capGrant',
  conn: 'capConn',
  approve: 'capApprove',
  explain: 'capExplain',
  release: 'capRelease',
}

/** 点一下走到下一档。三档循环,没有"清空"这个状态 —— 见 levelOf。 */
const CYCLE: CapLevel[] = ['allow', 'approve', 'deny']

/**
 * 一格的当前档位。
 *
 * 矩阵里查不到的格子读作 **allow**,这是服务端的口径(repo.MatrixForRole 只存
 * 显式写过的行,判定层查不到就放行),不是这里挑的默认值。把它显示成"未设置"
 * 会更诚实一点,但那需要第四种视觉状态,而人真正要知道的是"此刻这格准不准" ——
 * 答案就是准。
 */
export function levelOf(
  matrix: Record<string, Record<string, string>> | undefined,
  cap: string,
  tier: string,
): CapLevel {
  return (matrix?.[cap]?.[tier] as CapLevel) || 'allow'
}

export function nextLevel(cur: CapLevel): CapLevel {
  return CYCLE[(CYCLE.indexOf(cur) + 1) % CYCLE.length]
}

const SYM: Record<CapLevel, { Icon: typeof CircleCheck; tip: string }> = {
  allow: { Icon: CircleCheck, tip: 'capTipAllow' },
  approve: { Icon: Lock, tip: 'capTipApprove' },
  deny: { Icon: Minus, tip: 'capTipDeny' },
}

export interface MatrixTier {
  code: string
  label: string
}

/**
 * 能力矩阵:行=能力,列=分层,一格一个判定。
 *
 * 抽成组件是因为它有两个读者:权限页在这里**改**它,用户页在详情里**只读地**看
 * 多角色合并之后的结果。两处的格子必须长得一模一样,否则同一个判定在两页看着像
 * 两回事。只读态传 `onCycle=undefined`。
 */
export function CapabilityMatrix({
  matrix, tiers, onCycle, colWidth = 'minmax(88px, 1fr)', headWidth = 'minmax(150px, 2fr)',
}: {
  matrix: Record<string, Record<string, string>> | undefined
  tiers: MatrixTier[]
  /** 不传 = 只读。只读时格子仍然显示,只是点不动 —— 藏起来等于不告诉人他有什么。 */
  onCycle?: (cap: Capability, tier: string, next: CapLevel) => void
  colWidth?: string
  headWidth?: string
}) {
  const { t } = useTranslation()
  const tpl = `${headWidth} repeat(${tiers.length}, ${colWidth})`

  return (
    <div className="cap-wrap">
      {/* 分层多起来之后这张表自己横向滚,页面主体不跟着横滚。 */}
      <div className="cap-scroll">
        <div className="cap-grid">
          <div className="cap-head" style={{ gridTemplateColumns: tpl }}>
            <span>{t('colCap')}</span>
            {tiers.map((tier) => (
              <span key={tier.code} className="cap-th">{tier.label}</span>
            ))}
          </div>
          {CAPABILITIES.map((cap) => (
            <div key={cap} className="cap-row" style={{ gridTemplateColumns: tpl }}>
              <span className="cap-name">{t(CAP_LABEL[cap])}</span>
              {tiers.map((tier) => {
                const level = levelOf(matrix, cap, tier.code)
                const { Icon, tip } = SYM[level]
                return (
                  <span key={tier.code} className="cap-cellwrap">
                    <button
                      type="button"
                      className={clsx('cap-cell', level, !onCycle && 'ro')}
                      title={t(tip)}
                      aria-label={`${t(CAP_LABEL[cap])} · ${tier.label} · ${t(tip)}`}
                      disabled={!onCycle}
                      onClick={() => onCycle?.(cap, tier.code, nextLevel(level))}
                    >
                      <Icon size={14} />
                    </button>
                  </span>
                )
              })}
            </div>
          ))}
        </div>
      </div>
      <div className="cap-legend">
        {(['allow', 'approve', 'deny'] as CapLevel[]).map((l) => {
          const { Icon, tip } = SYM[l]
          return (
            <span key={l} className="cap-lg">
              <span className={clsx('cap-cell sm', l)}><Icon size={12} /></span>
              {t(tip)}
            </span>
          )
        })}
      </div>
    </div>
  )
}

export default CapabilityMatrix
