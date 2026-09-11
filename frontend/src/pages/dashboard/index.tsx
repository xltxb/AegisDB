import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import {
  ClipboardCheck, PlayCircle, BusFront, Database, ScrollText, Activity, TriangleAlert,
} from 'lucide-react'
import type { ReactNode } from 'react'
import { meQueryOptions } from '@/api/modules/auth'
import { approvalsQueryOptions } from '@/api/modules/approvals'
import { execWindowsQueryOptions } from '@/api/modules/execWindows'
import { connectionsQueryOptions } from '@/api/modules/connections'
import { auditQueryOptions } from '@/api/modules/audit'
import { terminalApi } from '@/api/modules/terminal'
import { useEnvTier } from '@/hooks/useEnvTier'
import { Badge, toneOfStatus } from '@/components/common/Badge'
import { Loading, ErrorState, Empty } from '@/components/common/States'
import type { Approval, Connection, ExecWindow } from '@/types'

/**
 * 总览 —— 登录落地页。
 *
 * 从前进来直接就是连着生产库、光标在等你敲字的提示符。把唯一能改动真实数据的地方
 * 当默认页,等于每次开工都先站到闸门里面。
 *
 * 三条约束,来自这一页当初的设计:
 *
 * - **不新增任何接口,也不新增任何权限面**。每块数据都是用户本来就能自己去拉的
 *   那一个接口,卡片按 `menus` 收敛。
 * - **各卡片并发加载、各自失败各自算**。一块取不到不该让整页空掉 —— 用 TanStack
 *   Query 时这是自然的:每张卡自己一个 query,自己一份 loading/error。
 * - 卡片只读、只做导航。这一页不提供任何在别处不存在的动作。
 */
export default function DashboardPage() {
  const { t } = useTranslation()
  const { data: me } = useQuery(meQueryOptions())
  const menus = me?.menus ?? {}

  return (
    <div className="page dash">
      <header className="page-head">
        <div>
          <h1>{t('dashTitle')}</h1>
          <p>{t('dashSub', { name: me?.name ?? '' })}</p>
        </div>
      </header>

      <div className="dash-grid">
        {menus.approve && <PendingCard />}
        <ExecutableCard />
        {menus.execwindow && <WindowsCard />}
        {menus.db && <InstancesCard />}
        <GatewayCard />
        {menus.audit && <AuditCard />}
      </div>
    </div>
  )
}

function Card({
  icon, title, to, linkText, children,
}: { icon: ReactNode; title: string; to?: string; linkText?: string; children: ReactNode }) {
  return (
    <section className="dash-card">
      <header>
        <span className="dash-ico">{icon}</span>
        <h2>{title}</h2>
        {to && <Link className="dash-more" to={to}>{linkText}</Link>}
      </header>
      <div className="dash-body">{children}</div>
    </section>
  )
}

/** 待我审批 —— 只有有审批菜单的人才看得到这张卡。 */
function PendingCard() {
  const { t } = useTranslation()
  const { data, isLoading, error, refetch } = useQuery(approvalsQueryOptions('mine', 1))
  const rows = (data?.items ?? []).filter((a: Approval) => a.status === 'pending')
  return (
    <Card icon={<ClipboardCheck size={16} />} title={t('dashPending')} to="/approvals" linkText={t('dashGo')}>
      {isLoading && <Loading />}
      {error && <ErrorState error={error} retry={() => refetch()} />}
      {!isLoading && !error && !rows.length && <Empty hint={t('dashNoPending')} />}
      <ul className="dash-list">
        {rows.slice(0, 5).map((a) => (
          <li key={a.id}>
            <span className="mono">{a.apNo}</span>
            <span className="grow ellip">{a.command}</span>
            <Badge tone="warning">{t('apPending')}</Badge>
          </li>
        ))}
      </ul>
    </Card>
  )
}

/**
 * 等我执行 —— 已经批了但还没跑的单。
 *
 * 这一格正是「通过 ≠ 执行」在界面上的落点:批准之后那张单还停在这里,等一个人
 * 选时机去按。可执行与否读服务端的 `canExecute`,不在这里拼。
 */
function ExecutableCard() {
  const { t } = useTranslation()
  const { data, isLoading, error, refetch } = useQuery(approvalsQueryOptions('all', 1))
  const rows = (data?.items ?? []).filter((a: Approval) => a.canExecute)
  return (
    <Card icon={<PlayCircle size={16} />} title={t('dashExecutable')} to="/approvals" linkText={t('dashGo')}>
      {isLoading && <Loading />}
      {error && <ErrorState error={error} retry={() => refetch()} />}
      {!isLoading && !error && !rows.length && <Empty hint={t('dashNoExecutable')} />}
      <ul className="dash-list">
        {rows.slice(0, 5).map((a) => (
          <li key={a.id}>
            <span className="mono">{a.apNo}</span>
            <span className="grow ellip">{a.instance}{a.database ? ` · ${a.database}` : ''}</span>
            <Badge tone="accent">{t('apWaitingExec')}</Badge>
          </li>
        ))}
      </ul>
    </Card>
  )
}

/** 当前开着的窗口 —— `active` 由后端算,前端不重算跨午夜与时区。 */
function WindowsCard() {
  const { t } = useTranslation()
  const { data, isLoading, error, refetch } = useQuery(execWindowsQueryOptions())
  const rows = (data ?? []).filter((w: ExecWindow) => w.status === 'approved' && w.enabled && w.active)
  return (
    <Card icon={<BusFront size={16} />} title={t('dashWindows')} to="/exec-windows" linkText={t('dashGo')}>
      {isLoading && <Loading />}
      {error && <ErrorState error={error} retry={() => refetch()} />}
      {!isLoading && !error && !rows.length && <Empty hint={t('dashNoWindows')} />}
      <ul className="dash-list">
        {rows.slice(0, 5).map((w) => (
          <li key={w.id}>
            <span className="grow ellip">{w.name}</span>
            <span className="mono dim">{w.database}</span>
            <Badge tone="success">{t('winActive')}</Badge>
          </li>
        ))}
      </ul>
    </Card>
  )
}

/** 实例概况:按分层分组计数,外加维护态的台数 —— 那是今天下不了发的原因。 */
function InstancesCard() {
  const { t } = useTranslation()
  const { data, isLoading, error, refetch } = useQuery(connectionsQueryOptions())
  const envtier = useEnvTier()
  const list = (data ?? []) as Connection[]

  const byTier = new Map<string, number>()
  for (const c of list) {
    const label = envtier.tierLabel(c.env) || t('dashTierUnknown')
    byTier.set(label, (byTier.get(label) ?? 0) + 1)
  }
  const maint = list.filter((c) => c.status === 'maint').length

  return (
    <Card icon={<Database size={16} />} title={t('dashInstances')} to="/connections" linkText={t('dashGo')}>
      {isLoading && <Loading />}
      {error && <ErrorState error={error} retry={() => refetch()} />}
      {!isLoading && !error && (
        <>
          <div className="dash-nums">
            <div><b>{list.length}</b><span>{t('dashTotal')}</span></div>
            <div className={maint ? 'warn' : ''}><b>{maint}</b><span>{t('dashMaint')}</span></div>
          </div>
          <ul className="dash-list">
            {[...byTier].map(([label, n]) => (
              <li key={label}><span className="grow ellip">{label}</span><span className="mono">{n}</span></li>
            ))}
          </ul>
        </>
      )}
    </Card>
  )
}

/** 网关状态 —— p50/p95 与生产拦截计数,取的是顶栏那块胶囊同一个接口。 */
function GatewayCard() {
  const { t } = useTranslation()
  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['gateway-stats'] as const,
    queryFn: terminalApi.gatewayStats,
    refetchInterval: 15_000,
  })
  return (
    <Card icon={<Activity size={16} />} title={t('dashGateway')}>
      {isLoading && <Loading />}
      {error && <ErrorState error={error} retry={() => refetch()} />}
      {data && (
        <div className="dash-nums wide">
          <div><b>{data.p50Ms}<i>ms</i></b><span>p50</span></div>
          <div><b>{data.p95Ms}<i>ms</i></b><span>p95</span></div>
          <div><b>{data.samples}</b><span>{t('dashSamples')}</span></div>
          <div className={data.intercepts ? 'warn' : ''}>
            <b>{data.intercepts}</b><span>{t('dashIntercepts')}</span>
          </div>
        </div>
      )}
    </Card>
  )
}

/** 近期审计 —— 只取第一页的前几条,点进去看全量。 */
function AuditCard() {
  const { t } = useTranslation()
  const { data, isLoading, error, refetch } = useQuery(
    auditQueryOptions({ risk: 'all', range: '24h', page: 1, pageSize: 5 }),
  )
  const rows = data?.items ?? []
  return (
    <Card icon={<ScrollText size={16} />} title={t('dashAudit')} to="/audit" linkText={t('dashGo')}>
      {isLoading && <Loading />}
      {error && <ErrorState error={error} retry={() => refetch()} />}
      {!isLoading && !error && !rows.length && <Empty hint={t('dashNoAudit')} />}
      <ul className="dash-list">
        {rows.slice(0, 5).map((r) => (
          <li key={r.id}>
            {r.risk === 'high' && <TriangleAlert size={13} className="danger" />}
            <span className="grow ellip mono">{r.command}</span>
            <Badge tone={toneOfStatus(r.result)}>{r.result}</Badge>
          </li>
        ))}
      </ul>
    </Card>
  )
}
