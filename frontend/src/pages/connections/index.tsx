import { Fragment, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import clsx from 'clsx'
import {
  Activity, Check, ChevronDown, ChevronsDownUp, ChevronsUpDown, Copy, Database, DatabaseZap,
  Download, FolderKanban, Pencil, Plus, Rows2, Rows3, Search, Upload, X,
} from 'lucide-react'
import {
  useBatchPolicy, useBatchProbe, useConnections, useSaveConnection, useSetConnectionPolicy,
  useSyncConnectionMetadata, useTestConnection, useToggleConnectionStatus,
  type ConnectionDraft,
} from '@/hooks/useConnections'
import { useProjects } from '@/hooks/useProjects'
import { envTiersQueryOptions, environmentsQueryOptions } from '@/api/modules/envtier'
import { engineDisplay, engineLabels } from '@/lib/engines'
import { dotForEnv, envLabel, tierOf } from '@/lib/envTierLabels'
import { toneOfDot } from '@/lib/tierTone'
import { copyText } from '@/lib/clipboard'
import { downloadCsv, toCsv } from '@/lib/csv'
import { confirmAction } from '@/lib/confirm'
import { Badge, toneOfStatus } from '@/components/common/Badge'
import { Button } from '@/components/common/Button'
import { Segmented } from '@/components/common/Segmented'
import { useUIStore } from '@/stores/ui'
import { Loading, ErrorState, Empty } from '@/components/common/States'
import { ConnectionModal, POLICIES, blankDraft, draftOf } from './ConnectionModal'
import { ImportModal } from './ImportModal'
import { ProjectsModal } from './ProjectsModal'
import { EnvTiers } from './EnvTiers'
import { DbPanel } from './DbPanel'
import type { Connection, EnvTier, Environment, Project } from '@/types'

/**
 * 八列的比例来自原型。写成常量,表头与每一行只可能取到同一份。
 *
 * 第一列是那个 26px 的勾选框:批量动作(巡检 / 改策略 / 导出)全都建立在"选了哪几台"
 * 之上,而它必须和数据行对齐,所以它是一列,不是塞进实例名格子里的一个附件。
 */
const COLS = '26px 1.45fr 0.9fr 1.6fr 0.8fr 1fr 1.05fr 112px'

/** 每组先渲染多少行。这一页每行都带着标签、下拉框和可展开的库面板 —— 一次铺开的
 *  不是 N 行文本,而是上千个节点。 */
const PAGE = 25

/** 同时连过去的台数由 hooks/useConnections 里的巡检负责;这里只管"选了哪几台"。 */

const DENSE_KEY = 'vela_conn_dense'

/** 网关策略的色档 —— 严格是红,单人审批是黄,只审计不拦是中性。 */
function policyTone(p: string) {
  if (p === 'strict') return 'danger' as const
  if (p === 'audit-only') return 'neutral' as const
  return 'warning' as const
}

const tagArr = (s: string) => (s ? s.split(',').map((x) => x.trim()).filter(Boolean) : [])

/**
 * 命中高亮。
 *
 * 切成片段用 JSX 渲染,不拼 HTML:实例名、地址、标签都是别人填进库里的,拼进
 * innerHTML 就是把一个配置字段变成脚本注入口。
 */
function Hi({ text, needle }: { text: string; needle: string }) {
  if (!needle || !text) return <>{text}</>
  const low = text.toLowerCase()
  const out: { t: string; on: boolean }[] = []
  let i = 0
  for (;;) {
    const j = low.indexOf(needle, i)
    if (j < 0) { if (i < text.length) out.push({ t: text.slice(i), on: false }); break }
    if (j > i) out.push({ t: text.slice(i, j), on: false })
    out.push({ t: text.slice(j, j + needle.length), on: true })
    i = j + needle.length
  }
  return <>{out.map((p, k) => (p.on ? <mark key={k} className="conn-hit">{p.t}</mark> : <Fragment key={k}>{p.t}</Fragment>))}</>
}

/** 按引擎目录的顺序排,认不出来的排最后。 */
function byCatalogueOrder(a: string, b: string): number {
  const labels = engineLabels()
  const ia = labels.indexOf(a)
  const ib = labels.indexOf(b)
  if (ia === ib) return a.localeCompare(b)
  if (ia < 0) return 1
  if (ib < 0) return -1
  return ia - ib
}

export default function ConnectionsPage() {
  const { t } = useTranslation()
  const notify = useUIStore((s) => s.notify)
  const { data: conns, isLoading, error, refetch } = useConnections()
  // 分层与环境直接读共用的 query key —— 这一页只是它们的消费方,不该再包一层。
  const { data: tiers } = useQuery(envTiersQueryOptions())
  const { data: envs } = useQuery(environmentsQueryOptions())
  const { data: projectData } = useProjects()
  const save = useSaveConnection()
  const toggle = useToggleConnectionStatus()
  const probe = useTestConnection()
  const setPolicy = useSetConnectionPolicy()
  const syncMeta = useSyncConnectionMetadata()
  const batchProbe = useBatchProbe()
  const batchPolicy = useBatchPolicy()

  /** 实例 / 环境与分层。原型 v2 把它们折进同一页 —— 一个是实例挂在哪,另一个是
   *  挂过去之后按什么规矩办,分成两条路由只会让人在两页之间来回对照。 */
  const [view, setView] = useState<'instances' | 'envtiers'>('instances')

  // 输入即时回显,过滤延后 300ms。几十台实例时每敲一个键都重算一遍双层分组、
  // 并把命中组全部展开,是看得见的卡顿;而"边打字边跳"本身也难用。
  const [qInput, setQInput] = useState('')
  const [q, setQ] = useState('')
  useEffect(() => {
    const id = setTimeout(() => setQ(qInput), 300)
    return () => clearTimeout(id)
  }, [qInput])

  const [envFilter, setEnvFilter] = useState('')
  const [statusFilter, setStatusFilter] = useState<'' | 'bad'>('')
  const [engineFilter, setEngineFilter] = useState('')
  // null = 还没被人动过,用"只展开第一个分层"的默认值。
  const [collapsed, setCollapsed] = useState<Set<string> | null>(null)
  const [dense, setDense] = useState(localStorage.getItem(DENSE_KEY) === '1')
  const [budget, setBudget] = useState<Record<string, number>>({})
  const [sel, setSel] = useState<Set<number>>(new Set())
  const [openDbs, setOpenDbs] = useState<Set<number>>(new Set())
  const [copiedId, setCopiedId] = useState(0)
  // 巡检与同步的结论只活在本次会话里:它们是"刚才那一下的结果",和实例的维护态
  // 不是一回事,所以既不写回 status,也不进缓存。
  const [checkResult, setCheckResult] = useState<Record<number, 'ok' | 'bad'>>({})
  const [syncResult, setSyncResult] = useState<Record<number, { ok: boolean; text: string }>>({})
  const [syncingId, setSyncingId] = useState(0)
  const [draft, setDraft] = useState<ConnectionDraft | null>(null)
  const [importOpen, setImportOpen] = useState(false)
  const [projectsOpen, setProjectsOpen] = useState(false)

  const tierList: EnvTier[] = tiers ?? []
  const envList: Environment[] = envs ?? []
  const projects: Project[] = projectData ?? []
  const rows: Connection[] = conns ?? []

  useEffect(() => {
    if (!copiedId) return
    const id = setTimeout(() => setCopiedId(0), 1600)
    return () => clearTimeout(id)
  }, [copiedId])

  function setDensity(v: boolean) {
    setDense(v)
    localStorage.setItem(DENSE_KEY, v ? '1' : '0')
  }

  // ---- 过滤 ----
  const needle = q.trim().toLowerCase()
  const anyFilter = !!(needle || statusFilter || engineFilter)

  /** 一个框全找:名称 / 地址 / 端口 / 库名 / 引擎 / 标签 / 角色 / 层级。
   *  端口单独算一项 —— host:port 那一串里虽然带着它,但只敲 "3306" 也该命中。 */
  function matches(c: Connection): boolean {
    return [c.name, c.host, String(c.port), `${c.host}:${c.port}`, c.database, c.engine, c.tags, c.defaultRole, c.layer]
      .join(' ').toLowerCase().includes(needle)
  }
  /** 除环境外的全部条件 —— 环境计数要按"其它条件都满足"来算。 */
  function passes(c: Connection): boolean {
    if (statusFilter === 'bad' && c.status === 'online') return false
    if (engineFilter && engineDisplay(c.engine) !== engineFilter) return false
    return !needle || matches(c)
  }

  const passing = rows.filter(passes)
  const filtered = envFilter ? passing.filter((c) => c.env === envFilter) : passing

  /** 引擎下拉只列**现有实例真的在用**的引擎,不摆一整本目录。 */
  const engineOpts = [...new Set(rows.map((c) => engineDisplay(c.engine)))].sort(byCatalogueOrder)

  // 环境计数按"搜索后"算:筛选器要回答"命中的都在哪",不是全量分布。
  const envCounts: Record<string, number> = {}
  for (const c of passing) envCounts[c.env] = (envCounts[c.env] || 0) + 1

  // ---- 分组:环境 → 引擎 ----
  const byEnv = new Map<string, Connection[]>()
  for (const c of filtered) {
    const list = byEnv.get(c.env)
    if (list) list.push(c)
    else byEnv.set(c.env, [c])
  }

  const budgetOf = (key: string) => budget[key] ?? PAGE

  /**
   * 预算在**组一级**连续消耗,不是每个引擎各给一份 —— 否则"这一组先出 25 行"
   * 说的其实是 25×引擎数 行。预算用完的引擎仍然留下标题行(带总数),不然
   * "这个引擎存在"这件事会凭空消失。
   */
  function buildGroup(key: string, list: Connection[]) {
    const buckets = new Map<string, Connection[]>()
    for (const c of list) {
      const k = engineDisplay(c.engine)
      const b = buckets.get(k)
      if (b) b.push(c)
      else buckets.set(k, [c])
    }
    let left = budgetOf(key)
    const types: { key: string; rows: Connection[]; total: number }[] = []
    for (const k of [...buckets.keys()].sort(byCatalogueOrder)) {
      const all = buckets.get(k)!
      const take = Math.max(0, Math.min(left, all.length))
      left -= take
      types.push({ key: k, rows: all.slice(0, take), total: all.length })
    }
    const shown = types.reduce((n, x) => n + x.rows.length, 0)
    return {
      types,
      hidden: list.length - shown,
      ok: list.filter((c) => c.status === 'online').length,
      bad: list.filter((c) => c.status !== 'online').length,
    }
  }

  /**
   * 分组:每个环境一组,外加一组"环境已经不存在了"的实例。
   *
   * 后者必须留在表上 —— 这一页正是有人会来把它们挪走的地方,而一台没列出来的实例
   * 是没人修得了的。
   */
  const groups = envList.map((e) => ({
    code: e.code, known: true, count: byEnv.get(e.code)?.length ?? 0,
    ...buildGroup(e.code, byEnv.get(e.code) ?? []),
  }))
  const knownCodes = new Set(envList.map((e) => e.code))
  for (const code of [...byEnv.keys()].filter((k) => !knownCodes.has(k)).sort()) {
    const list = byEnv.get(code)!
    groups.push({ code, known: false, count: list.length, ...buildGroup(code, list) })
  }
  // 过滤态下的空组只是噪音;全量视图仍然显示空环境(它回答"这个环境还没接入实例")。
  const shownGroups = anyFilter || envFilter ? groups.filter((g) => g.count > 0) : groups

  // ---- 折叠 ----
  // 默认只展开**第一个分层**下的环境(通常是生产)。88 套实例平铺出来没人读得完,
  // 而真正天天要看的是生产。人动过之后就以他的选择为准。
  const firstTier = tierList[0]?.code
  const defaultCollapsed = new Set(envList.filter((e) => e.tierCode !== firstTier).map((e) => e.code))
  const collapsedSet = collapsed ?? defaultCollapsed
  // 搜索或筛选时强制展开:命中项藏在折叠组里,等于没搜到。
  const isCollapsed = (key: string) => !anyFilter && collapsedSet.has(key)
  const allCollapsed = shownGroups.length > 0 && shownGroups.every((g) => collapsedSet.has(g.code))

  function toggleGroup(key: string) {
    const next = new Set(collapsedSet)
    if (next.has(key)) next.delete(key)
    else next.add(key)
    setCollapsed(next)
  }

  // ---- 选择 ----
  // 只在**当前可见的行**上做全选。一个能把没在屏幕上的行一起选走的复选框,是批量
  // 操作里最容易出事的一种。
  const visibleIds: number[] = []
  for (const g of shownGroups) {
    if (isCollapsed(g.code)) continue
    for (const ty of g.types) for (const c of ty.rows) visibleIds.push(c.id)
  }
  const allVisibleSelected = visibleIds.length > 0 && visibleIds.every((id) => sel.has(id))
  const selected = rows.filter((c) => sel.has(c.id))

  /**
   * 选中的实例里有多少台在**受管控的分层**上。
   *
   * 判据用 `dangerBanner || requireMfa` —— 和 lib/envTierLabels 里 dotFor 判"这一格
   * 该不该是红的"用的是同一条。不另立一个"什么算生产"的定义:两处说法一旦不一致,
   * 人就会开始不信其中之一。两个标志都关掉的部署里这个数是 0,那不是漏判,是这套
   * 环境自己声明了它没有需要额外提醒的分层。
   */
  const selGuarded = selected.filter((c) => {
    const tier = tierOf(c.env, tierList, envList)
    return !!tier && (tier.dangerBanner || tier.requireMfa)
  }).length

  function toggleSel(id: number) {
    const next = new Set(sel)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    setSel(next)
  }
  function toggleSelAll() {
    const next = new Set(sel)
    if (allVisibleSelected) visibleIds.forEach((id) => next.delete(id))
    else visibleIds.forEach((id) => next.add(id))
    setSel(next)
  }

  function toggleDbs(id: number) {
    const next = new Set(openDbs)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    setOpenDbs(next)
  }

  /**
   * 地址整串复制。
   *
   * 列里是截断显示的 —— 看得见的那一段不等于能粘贴的那一串,所以取的是完整值。
   * 走 lib/clipboard:这个控制台经常被人用局域网 IP 直接打开,那种地址不是安全
   * 上下文,`navigator.clipboard` 在那里根本不存在。
   */
  async function copyHost(c: Connection) {
    if (await copyText(`${c.host}:${c.port}`)) setCopiedId(c.id)
    else notify(t('connCopyFail'), 'error')
  }

  /** 立刻同步一台实例的元数据。结果留在本次会话里,成功说同步到多少张表,失败说原因。 */
  function runSync(c: Connection) {
    setSyncingId(c.id)
    syncMeta.mutate(c, {
      onSuccess: (r) => setSyncResult((p) => ({
        // 0 张是个真实的答案,不是失败 —— 但它和"没同步过"读起来一样,所以说成
        // "0 张表",不留空。
        ...p, [c.id]: { ok: true, text: t('connMetaTables', { n: r.tables }) },
      })),
      onError: (e: Error) => setSyncResult((p) => ({
        ...p, [c.id]: { ok: false, text: e.message || t('connMetaSyncBad') },
      })),
      onSettled: () => setSyncingId(0),
    })
  }

  /** 批量巡检:逐台真的连过去。并发 4,结果只显示,不写回 status。 */
  function runBatchCheck() {
    if (!selected.length) return
    setCheckResult({})
    batchProbe.mutate({
      rows: selected,
      onEach: (id, ok) => setCheckResult((p) => ({ ...p, [id]: ok ? 'ok' : 'bad' })),
    })
  }

  /**
   * 批量改策略。
   *
   * 改的是**这些实例此后怎么被判**,所以先说清"多少台、其中多少台在受管控分层",
   * 再让人按确认 —— 一次点错在这里等于把一批生产库的闸门一起挪了。没有受管控分层
   * 被选中时就不提那一句:"其中 0 台"是句废话,而废话读多了会让人把整段确认一起跳过。
   */
  function runBatchPolicy(policy: string) {
    if (!policy || !selected.length) return
    const msg = selGuarded
      ? t('connBatchConfirmGuarded', { n: selected.length, p: policy, d: selGuarded })
      : t('connBatchConfirm', { n: selected.length, p: policy })
    if (!confirmAction(msg)) return
    batchPolicy.mutate({ rows: selected, policy })
  }

  /** 导出所选。**不含凭据** —— 口令在库里是加密的,而一份落到下载目录里的 CSV
   *  不该是把它们带出网关的那条路。 */
  function exportSelected() {
    if (!selected.length) return
    const head = ['name', 'engine', 'env', 'host', 'port', 'database', 'policy', 'defaultRole', 'status', 'tags']
    downloadCsv(
      `connections-${selected.length}.csv`,
      toCsv(head, selected.map((c) => [
        c.name, c.engine, c.env, c.host, c.port, c.database, c.policy, c.defaultRole, c.status, c.tags,
      ])),
    )
    notify(t('connExportDone', { n: selected.length }), 'ok')
  }

  /**
   * 一句话说清这一组的实例会被怎么判。
   *
   * 每条都是分层上真实的开关,不是文案:分层页上关掉 strictNoWhere,这里那半句就
   * 该消失。没有任何开关的分层照实说"无附加管控",而不是留白 —— 留白读起来像
   * "还没设好",两者的处置完全不同。
   */
  function policyLine(tier: EnvTier | undefined): string {
    if (!tier) return t('connTierUnknown')
    const parts: string[] = []
    if (tier.dangerBanner) parts.push(t('connTierDanger'))
    if (tier.requireMfa) parts.push(t('connTierMfa'))
    if (tier.strictNoWhere) parts.push(t('connTierStrict'))
    if (tier.scanBaseline) parts.push(t('connTierScan'))
    if (!parts.length) parts.push(t('connTierPlain'))
    if (tier.defaultRole) parts.push(t('connTierRole', { role: tier.defaultRole }))
    return parts.join(' · ')
  }

  return (
    <div className={clsx('page', sel.size && 'has-bulkbar')}>
      <header className="page-head">
        <div>
          <h1>{t('connTitle')}</h1>
          <p>{t('connSub')}</p>
        </div>
        <div className="grow">
          <Segmented
            value={view}
            onChange={setView}
            options={[
              { value: 'instances', label: t('connViewInstances') },
              { value: 'envtiers', label: t('connViewEnvTiers') },
            ]}
          />
          {view === 'instances' && (
            <>
              {/* 项目与「库归属」是同一件事的两头:在这里建项目,回到表上就能把库挂过去。 */}
              <Button variant="secondary" onClick={() => setProjectsOpen(true)}>
                <FolderKanban size={15} />{t('prTitle')}
              </Button>
              <Button variant="secondary" onClick={() => setImportOpen(true)}>
                <Upload size={15} />{t('connImport')}
              </Button>
              <Button variant="primary" onClick={() => setDraft(blankDraft(envList[0]?.code ?? ''))}>
                <Plus size={15} />{t('connNew')}
              </Button>
            </>
          )}
        </div>
      </header>

      {view === 'envtiers' && <EnvTiers />}

      {view === 'instances' && (
        <>
          <div className="conn-toolbar">
            <div className="conn-search">
              <Search size={14} />
              <input
                value={qInput}
                placeholder={t('connSearchPh')}
                onChange={(e) => setQInput(e.target.value)}
              />
              {qInput && (
                <button type="button" title={t('cancel')} onClick={() => { setQInput(''); setQ('') }}>
                  <X size={13} />
                </button>
              )}
            </div>

            <select
              className="conn-fsel"
              value={statusFilter}
              title={t('connColStatus')}
              onChange={(e) => setStatusFilter(e.target.value as '' | 'bad')}
            >
              <option value="">{t('connFltStatusAll')}</option>
              <option value="bad">{t('connFltStatusBad')}</option>
            </select>
            <select
              className="conn-fsel"
              value={engineFilter}
              title={t('connColEngine')}
              onChange={(e) => setEngineFilter(e.target.value)}
            >
              <option value="">{t('connFltEngineAll')}</option>
              {engineOpts.map((e) => <option key={e} value={e}>{e}</option>)}
            </select>

            {/* 环境筛选。圆点带的是**分层**的颜色,与树、审批列表同一个来源;它不随
                选中态变,因为"这个环境有多危险"和"我现在筛的是不是它"是两件事。 */}
            <div className="conn-envchips">
              <button
                type="button"
                className={clsx('conn-envchip', !envFilter && 'on')}
                onClick={() => setEnvFilter('')}
              >
                {t('connAllEnv')}<i>{filtered.length}</i>
              </button>
              {envList.map((e) => (
                <button
                  key={e.code}
                  type="button"
                  className={clsx('conn-envchip', envFilter === e.code && 'on')}
                  onClick={() => setEnvFilter(envFilter === e.code ? '' : e.code)}
                >
                  <span className={`conn-dot t-${toneOfDot(dotForEnv(e.code, tierList, envList))}`} />
                  {e.displayName || e.code}<i>{envCounts[e.code] || 0}</i>
                </button>
              ))}
            </div>

            <div className="conn-tbright">
              <button
                type="button"
                className="conn-tbtn"
                onClick={() => setCollapsed(allCollapsed ? new Set() : new Set(shownGroups.map((g) => g.code)))}
              >
                {allCollapsed ? <ChevronsUpDown size={14} /> : <ChevronsDownUp size={14} />}
                {allCollapsed ? t('connExpandAll') : t('connCollapseAll')}
              </button>
              {/* 密度开关。紧凑模式收起副标题与标签行 —— 那些在排障时是噪音,
                  在配置时才是内容,所以是个开关而不是一个决定。 */}
              <div className="conn-dens">
                <button
                  type="button"
                  className={clsx(!dense && 'on')}
                  title={t('connCozy')}
                  onClick={() => setDensity(false)}
                >
                  <Rows2 size={13} />
                </button>
                <button
                  type="button"
                  className={clsx(dense && 'on')}
                  title={t('connDense')}
                  onClick={() => setDensity(true)}
                >
                  <Rows3 size={13} />
                </button>
              </div>
            </div>
          </div>

          {isLoading && <Loading />}
          {error && <ErrorState error={error} retry={() => refetch()} />}
          {!isLoading && !error && !envList.length && <Empty hint={t('connEmptyNoEnv')} />}
          {!isLoading && !error && !!envList.length && !rows.length && <Empty hint={t('connEmpty')} />}
          {!isLoading && !error && !!rows.length && !shownGroups.some((g) => g.count > 0) && (
            <Empty hint={t('connNoMatch')} />
          )}

          {!isLoading && !error && !!rows.length && shownGroups.some((g) => g.count > 0) && (
            /*
              这张表不走 <Table>:分组行是横跨全部八列的一整条,而 Table 的每一行都被
              切成固定比例的格子,没有地方放它。列宽仍用同一个常量,所以表头和数据行
              对得上。
            */
            <div className={clsx('c-table', 'conn-table', dense && 'dense')}>
              <div className="c-thead" style={{ gridTemplateColumns: COLS }}>
                <div>
                  <input
                    type="checkbox"
                    title={t('connSelAllVisible')}
                    checked={allVisibleSelected}
                    onChange={toggleSelAll}
                  />
                </div>
                <div>{t('connColInstance')}</div>
                <div>{t('connColEngine')}</div>
                <div>{t('connColAddr')}</div>
                <div>{t('connColRole')}</div>
                <div>{t('connColPolicy')}</div>
                <div>{t('connColStatus')}</div>
                <div />
              </div>

              {shownGroups.map((g) => {
                const tier = tierOf(g.code, tierList, envList)
                const tone = g.known ? toneOfDot(dotForEnv(g.code, tierList, envList)) : 'neutral'
                const shut = isCollapsed(g.code)
                return (
                  <Fragment key={g.code}>
                    <div
                      className={clsx('conn-grp', `tone-${tone}`, 'clickable')}
                      onClick={() => toggleGroup(g.code)}
                    >
                      <ChevronDown size={13} className={clsx('conn-chev', shut && 'shut')} />
                      <span className="conn-grp-name">
                        {g.known ? envLabel(g.code, envList) : `${g.code} · ${t('connEnvGone')}`}
                      </span>
                      {tier && <Badge tone={tone}>{tier.displayName || tier.code}</Badge>}
                      <span className="conn-grp-n">{t('connGrpCount', { n: g.count })}</span>
                      {/* 不展开也能看出这个环境有没有事。异常为 0 时只说正常数 ——
                          把一个恒定的红色 0 挂在那里,久了谁都不看了。 */}
                      {!!g.count && (
                        <span className="conn-health">
                          <span className="conn-dot t-success" />{g.ok}
                          {!!g.bad && <><span className="conn-dot t-danger" />{g.bad}</>}
                        </span>
                      )}
                      <span className="conn-grp-line">{policyLine(tier)}</span>
                    </div>

                    {!shut && !g.count && <div className="c-empty">{t('connGrpEmpty')}</div>}

                    {!shut && g.types.map((ty) => (
                      <Fragment key={ty.key}>
                        {/* 第二层是引擎:它决定命令怎么被解读、哪个驱动连过去,
                            所以它分组,而不是待在一列里等人逐行扫。 */}
                        {!!ty.rows.length && (
                          <div className="conn-type">{ty.key}<span>{ty.total}</span></div>
                        )}
                        {ty.rows.map((c) => (
                          <Fragment key={c.id}>
                            <div
                              className={clsx('c-trow', 'conn-row', sel.has(c.id) && 'sel')}
                              style={{ gridTemplateColumns: COLS }}
                            >
                              <div className="c-td">
                                <input
                                  type="checkbox"
                                  checked={sel.has(c.id)}
                                  onChange={() => toggleSel(c.id)}
                                />
                              </div>
                              <div className="c-td">
                                <div className="cell-strong"><Hi text={c.name} needle={needle} /></div>
                                {!dense && (
                                  <div className="conn-tags">
                                    {c.layer && <span className="cell-sub">{c.layer}</span>}
                                    {/* 归属是**按库**定的,所以入口在这里展开,而不是行上
                                        一个下拉:一台实例底下的几个库可以分属不同项目。 */}
                                    <button
                                      type="button"
                                      className={clsx('conn-dbtoggle', openDbs.has(c.id) && 'on')}
                                      onClick={() => toggleDbs(c.id)}
                                    >
                                      <Database size={10} />{t('connDbProjects')}
                                    </button>
                                    {tagArr(c.tags).map((tag) => (
                                      <span key={tag} className="conn-chip"><Hi text={tag} needle={needle} /></span>
                                    ))}
                                    {!c.tags && <span className="cell-sub">{t('connNoTags')}</span>}
                                  </div>
                                )}
                              </div>
                              <div className="c-td">{engineDisplay(c.engine)}</div>
                              <div className="c-td conn-host">
                                <span className="mono" title={`${c.host}:${c.port}`}>
                                  <Hi text={`${c.host}:${c.port}`} needle={needle} />
                                </span>
                                <button
                                  type="button"
                                  className="conn-iconbtn"
                                  title={t(copiedId === c.id ? 'connCopied' : 'connCopy')}
                                  onClick={() => copyHost(c)}
                                >
                                  {copiedId === c.id ? <Check size={12} /> : <Copy size={12} />}
                                </button>
                                {!dense && c.database && <div className="cell-sub">{c.database}</div>}
                              </div>
                              <div className="c-td mono">{c.defaultRole || '—'}</div>
                              <div className="c-td">
                                <select
                                  className={clsx('conn-polsel', `t-${policyTone(c.policy)}`)}
                                  value={c.policy}
                                  title={t('connFPolicy')}
                                  disabled={setPolicy.isPending}
                                  onChange={(e) => setPolicy.mutate({ id: c.id, policy: e.target.value })}
                                >
                                  {POLICIES.map((p) => <option key={p} value={p}>{p}</option>)}
                                </select>
                              </div>
                              <div className="c-td conn-status">
                                {/*
                                  徽标本身就是开关:在线 ⇄ 维护是这一页最常按的一下,
                                  再塞一个下拉框只是让它多点一次。
                                */}
                                <button
                                  type="button"
                                  className="conn-status-btn"
                                  title={t('connToggleHint')}
                                  disabled={toggle.isPending}
                                  onClick={() => toggle.mutate({
                                    id: c.id, status: c.status === 'online' ? 'maint' : 'online',
                                  })}
                                >
                                  <Badge tone={toneOfStatus(c.status)}>
                                    {t(c.status === 'online' ? 'connOnline' : 'connMaint')}
                                  </Badge>
                                </button>
                                {checkResult[c.id] && (
                                  <span className={clsx('conn-mark', checkResult[c.id])}>
                                    {t(checkResult[c.id] === 'ok' ? 'connCheckOk' : 'connCheckBad')}
                                  </span>
                                )}
                                {syncResult[c.id] && (
                                  <span
                                    className={clsx('conn-mark', syncResult[c.id].ok ? 'ok' : 'bad')}
                                    title={syncResult[c.id].text}
                                  >
                                    {syncResult[c.id].ok ? syncResult[c.id].text : t('connMetaSyncBad')}
                                  </span>
                                )}
                              </div>
                              <div className="c-td row-ops">
                                <Button
                                  variant="ghost"
                                  title={t('connMetaSync')}
                                  disabled={syncingId === c.id}
                                  onClick={() => runSync(c)}
                                >
                                  <DatabaseZap size={14} className={clsx(syncingId === c.id && 'spin')} />
                                </Button>
                                <Button
                                  variant="ghost"
                                  title={t('connTest')}
                                  disabled={probe.isPending}
                                  onClick={() => probe.mutate(c)}
                                >
                                  <Activity size={14} />
                                </Button>
                                <Button variant="ghost" title={t('connEdit')} onClick={() => setDraft(draftOf(c))}>
                                  <Pencil size={14} />
                                </Button>
                              </div>
                            </div>
                            {openDbs.has(c.id) && <DbPanel conn={c} projects={projects} />}
                          </Fragment>
                        ))}
                      </Fragment>
                    ))}

                    {/* 预算之外的行不进 DOM。 */}
                    {!shut && g.hidden > 0 && (
                      <div className="conn-more">
                        <button
                          type="button"
                          onClick={() => setBudget((p) => ({ ...p, [g.code]: budgetOf(g.code) + PAGE }))}
                        >
                          {t('connShowMore', { n: g.hidden })}
                        </button>
                      </div>
                    )}
                  </Fragment>
                )
              })}
            </div>
          )}
        </>
      )}

      {/* 悬浮批量条。只在选中后出现,并且始终显示"选了几台" —— 批量动作最怕的是
          不知道自己正在对多少东西下手。 */}
      {!!sel.size && view === 'instances' && (
        <div className="conn-bulkbar">
          <span className="conn-bulk-n">{t('connSelected', { n: sel.size })}</span>
          {!!selGuarded && (
            <Badge tone="danger">{t('connSelGuarded', { n: selGuarded })}</Badge>
          )}
          <select
            className="conn-fsel"
            value=""
            disabled={batchPolicy.isPending}
            onChange={(e) => { runBatchPolicy(e.target.value); e.target.value = '' }}
          >
            <option value="">{t('connBatchPolicy')}</option>
            {POLICIES.map((p) => <option key={p} value={p}>{p}</option>)}
          </select>
          <Button variant="secondary" disabled={batchProbe.isPending} onClick={runBatchCheck}>
            <Activity size={13} />{t('connBatchCheck')}
          </Button>
          <Button variant="secondary" onClick={exportSelected}>
            <Download size={13} />{t('connBatchExport')}
          </Button>
          <Button variant="ghost" onClick={() => setSel(new Set())}>
            <X size={13} />{t('connSelClear')}
          </Button>
        </div>
      )}

      {draft && (
        <ConnectionModal
          draft={draft}
          envs={envList}
          busy={save.isPending}
          onClose={() => setDraft(null)}
          onSubmit={(d) => save.mutate(d, { onSuccess: () => setDraft(null) })}
        />
      )}
      {importOpen && <ImportModal envs={envList} onClose={() => setImportOpen(false)} />}
      {projectsOpen && <ProjectsModal onClose={() => setProjectsOpen(false)} />}
    </div>
  )
}
