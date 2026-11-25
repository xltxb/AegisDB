// 实例名旁的巡检标记(.scratch/inspection-report 工单 05):只在最近一份报告是严重 / 警告时出现,
// 正常不显示。悬停看日期与问题摘要;点一下在新标签页打开报告 —— 不打断当前页面(终端会话)。
import type { HTMLAttributes } from 'react'
import clsx from 'clsx'
import { CircleAlert, TriangleAlert } from 'lucide-react'
import { useI18n } from '@/hooks/useI18n'
import type { InspectionReport } from '@/types'
import './InspectionBadge.scoped.css'

interface Props extends HTMLAttributes<HTMLSpanElement> { report?: InspectionReport | null }

export default function InspectionBadge({ report: r, className, ...rest }: Props) {
  const { t } = useI18n()
  if (!r || (r.level !== 'critical' && r.level !== 'warning')) return null
  const head = t('inspBadgeTip', { date: r.reportDate, level: t('inspLevel_' + r.level), c: r.criticalCount, w: r.warningCount })
  return (
    <span
      {...rest}
      className={clsx('ibadge', r.level, className)}
      title={r.topFinding ? head + '\n' + r.topFinding : head}
      data-testid="insp-badge"
      onClick={(e) => {
        e.stopPropagation()
        window.open(`#/inspection?instance=${r.connectionId}&report=${r.id}`, '_blank')
      }}
    >
      {r.level === 'critical' ? <CircleAlert size={12} /> : <TriangleAlert size={12} />}
    </span>
  )
}
