import type { HTMLAttributes } from 'react'
import clsx from 'clsx'
import { ScanSearch, ShieldCheck, OctagonAlert, PanelRightClose } from 'lucide-react'
import type { Connection, Member, RiskCommandView } from '@/types'
import { useAuthStore } from '@/stores/auth'
import { useI18n } from '@/hooks/useI18n'
import './RiskInspector.scoped.css'

interface Props extends HTMLAttributes<HTMLDivElement> {
  risk: 'idle' | 'safe' | 'high'
  riskCommands: RiskCommandView[]
  conn: Connection | null
  chain: Member[]
  onCollapse: () => void
}

export default function RiskInspector({ risk, riskCommands, conn, chain, onCollapse, className, ...rest }: Props) {
  const { t } = useI18n()
  const me = useAuthStore((s) => s.me)

  const meta = (() => {
    switch (risk) {
      case 'safe':
        return { title: t('lowRiskTitle'), sub: t('lowRiskSub'), icon: ShieldCheck, color: 'var(--success-text)', bg: 'var(--success-subtle)', border: 'rgba(24,179,104,.4)' }
      case 'high':
        return { title: t('highRiskTitle'), sub: t('highRiskSub'), icon: OctagonAlert, color: 'var(--danger-text)', bg: 'var(--danger-subtle)', border: 'rgba(240,71,62,.4)' }
      default:
        return { title: t('idleTitle'), sub: t('idleSub'), icon: ScanSearch, color: 'var(--text-muted)', bg: 'var(--surface-sunken)', border: 'var(--border-subtle)' }
    }
  })()

  // restricted commands for the selected connection's environment (level != off)
  const env = conn?.env || 'prod'
  const prodChips = riskCommands
    .filter((c) => c.tiers?.[env] && c.tiers[env] !== 'off')
    .map((c) => ({ name: c.command, high: c.tiers[env] === 'high' }))
  // 发起人排在最前,后面是审批链上的人。角色标签分开存,不再和名字拼成一行字符串 ——
  // "谁" 和 "他在这条链上是什么身份" 是两件事,挤成一句话时后者最先被读丢。
  const head = me ? [{ i: me.initials, n: me.name, role: t('initiated'), me: true }] : []
  const chainView = [...head, ...chain.map((m) => ({ i: m.initials, n: m.name, role: m.dept || t('chainReviewer'), me: false }))]

  return (
    <div {...rest} className={clsx('scy insp', className)}>
      <div className="ihead">
        <div className="eyebrow">{t('ctxTitle')}</div>
        <button className="collapse" title={t('ctxCollapse')} onClick={() => onCollapse()}><PanelRightClose size={15} /></button>
      </div>

      <div className="riskcard" style={{ borderColor: meta.border, background: meta.bg }}>
        <meta.icon size={22} color={meta.color} />
        <div className="rtxt">
          <div className="rt" style={{ color: meta.color }}>{meta.title}</div>
          <div className="rs">{meta.sub}</div>
        </div>
      </div>

      {/* 目标与角色:紧凑的键值卡片,而不是四行贴着面板边的裸文字 */}
      <div className="kvcard">
        <div className="row"><span>{t('targetInst')}</span><b className="mono">{conn ? conn.env + '-' + conn.name : '—'}</b></div>
        <div className="row"><span>{t('dbLabel')}</span><b className="mono">{conn?.engine || '—'}</b></div>
        <div className="row"><span>{t('connRole')}</span><b className="mono azure">{conn?.defaultRole || '—'}</b></div>
      </div>

      <div className="sechead">
        <span className="eyebrow2">{t('limitList')}</span>
        <span className="envtag">{env.toUpperCase()}</span>
      </div>
      {/* 虚线边框:这些不是可点的按钮,是"碰不得"的清单。实线胶囊在这一栏里会和
          上面那张键值卡片抢,读起来像另一组可操作的东西。 */}
      <div className="chips">
        {prodChips.map((c) => (
          <span
            key={c.name} className={clsx('chip', c.high ? 'danger' : 'warn')}
            title={c.high ? t('limitTipHigh', { cmd: c.name, env: env.toUpperCase() }) : t('limitTipMid', { cmd: c.name, env: env.toUpperCase() })}
          >{c.name}</span>
        ))}
        {!prodChips.length && <span className="nolimit">{t('limitNone')}</span>}
      </div>
      <div className="hint">{t('limitHint')}</div>

      <div className="sechead mt"><span className="eyebrow2">{t('chainTitle')}</span></div>
      <div className="chain">
        {chainView.map((p, i) => (
          <div key={i} className="cr">
            <div className="avawrap">
              <div className={clsx('ava', p.me && 'me')}>{p.i}</div>
              <span className="online" />
            </div>
            <div className="cbody">
              <div className="cn">{p.n}</div>
              <div className="crole">{p.role}</div>
            </div>
            <span className={clsx('rtag', p.me && 'me')}>{p.me ? t('initiated') : t('chainApprover')}</span>
          </div>
        ))}
      </div>
    </div>
  )
}
