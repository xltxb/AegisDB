import { Fragment, useState } from 'react'
import { useTranslation } from 'react-i18next'
import clsx from 'clsx'
import { Plus, Upload, Activity, Database } from 'lucide-react'
import {
  useConnections, useSaveConnection, useToggleConnectionStatus, useTestConnection,
  type ConnectionDraft,
} from '@/hooks/useConnections'
import { useQuery } from '@tanstack/react-query'
import { envTiersQueryOptions, environmentsQueryOptions } from '@/api/modules/envtier'
import { ENGINES, engineDisplay } from '@/lib/engines'
import { dotForEnv, envLabel, tierLabel, tierOf } from '@/lib/envTierLabels'
import { toneOfDot } from '@/lib/tierTone'
import { Badge, toneOfStatus } from '@/components/common/Badge'
import { Button } from '@/components/common/Button'
import { Modal } from '@/components/common/Modal'
import { Loading, ErrorState, Empty } from '@/components/common/States'
import type { Connection, EnvTier, Environment } from '@/types'

/** 六列的比例来自原型。写成常量,表头与每一行只可能取到同一份。 */
const COLS = '1.5fr 1fr 1.7fr 1fr 1fr 0.9fr'

const POLICIES = ['strict', 'approve-1', 'audit-only']

/** 网关策略的色档 —— 严格是红,单人审批是黄,只审计不拦是中性。 */
function policyTone(p: string) {
  if (p === 'strict') return 'danger' as const
  if (p === 'audit-only') return 'neutral' as const
  return 'warning' as const
}

const blankDraft = (env: string): ConnectionDraft => ({
  name: '', engine: ENGINES[0].id, host: '', env, policy: 'strict',
  username: '', password: '', database: '', tags: '',
})

function draftOf(c: Connection): ConnectionDraft {
  return {
    id: c.id,
    name: c.name,
    engine: c.engine,
    host: `${c.host}:${c.port}`,
    env: c.env,
    policy: c.policy,
    username: c.username || '',
    // 编辑一律从空口令开始 —— 服务端把空当成"保持原样",而把打码后的假值填进框里
    // 只会诱使人原样提交,把一串星号写成新口令。
    password: '',
    database: c.database || '',
    tags: c.tags || '',
  }
}

export default function ConnectionsPage() {
  const { t } = useTranslation()
  const { data: conns, isLoading, error, refetch } = useConnections()
  // 分层与环境直接读共用的 query key —— 这一页只是它们的消费方,不该再包一层。
  const { data: tiers } = useQuery(envTiersQueryOptions())
  const { data: envs } = useQuery(environmentsQueryOptions())
  const save = useSaveConnection()
  const toggle = useToggleConnectionStatus()
  const probe = useTestConnection()
  const [draft, setDraft] = useState<ConnectionDraft | null>(null)

  const tierList: EnvTier[] = tiers ?? []
  const envList: Environment[] = envs ?? []
  const rows: Connection[] = conns ?? []

  /**
   * 分组:每个环境一组,外加一组"环境已经不存在了"的实例。
   *
   * 后者必须留在表上 —— 这一页正是有人会来把它们挪走的地方,而一台没列出来的实例
   * 是没人修得了的。
   */
  const byEnv = new Map<string, Connection[]>()
  for (const c of rows) {
    const list = byEnv.get(c.env)
    if (list) list.push(c)
    else byEnv.set(c.env, [c])
  }
  const groups = envList.map((e) => ({ code: e.code, known: true, rows: byEnv.get(e.code) ?? [] }))
  const knownCodes = new Set(envList.map((e) => e.code))
  for (const code of [...byEnv.keys()].filter((k) => !knownCodes.has(k)).sort()) {
    groups.push({ code, known: false, rows: byEnv.get(code)! })
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
    <div className="page">
      <header className="page-head">
        <div>
          <h1>{t('connTitle')}</h1>
          <p>{t('connSub')}</p>
        </div>
        <div className="grow">
          {/*
            批量导入在这一版没有搬过来(CSV 解析与逐行建实例仍只在旧控制台里)。
            按钮保留但点不动,并把原因写在 title 上:一个凭空消失的入口会让人以为
            自己找错了页,而一句"还没接"至少是句真话。
          */}
          <Button variant="secondary" disabled title={t('connImportSoon')}>
            <Upload size={15} />{t('connImport')}
          </Button>
          <Button variant="primary" onClick={() => setDraft(blankDraft(envList[0]?.code ?? ''))}>
            <Plus size={15} />{t('connNew')}
          </Button>
        </div>
      </header>

      {isLoading && <Loading />}
      {error && <ErrorState error={error} retry={() => refetch()} />}
      {!isLoading && !error && !envList.length && <Empty hint={t('connEmptyNoEnv')} />}
      {!isLoading && !error && !!envList.length && !rows.length && <Empty hint={t('connEmpty')} />}

      {!isLoading && !error && !!rows.length && (
        /*
          这张表不走 <Table>:分组行是横跨全部六列的一整条,而 Table 的每一行都被
          切成固定比例的格子,没有地方放它。列宽仍用同一个常量,所以表头和数据行
          对得上。
        */
        <div className="c-table">
          <div className="c-thead" style={{ gridTemplateColumns: COLS }}>
            <div>{t('connColInstance')}</div>
            <div>{t('connColEngine')}</div>
            <div>{t('connColAddr')}</div>
            <div>{t('connColRole')}</div>
            <div>{t('connColPolicy')}</div>
            <div>{t('connColStatus')}</div>
          </div>

          {groups.map((g) => {
            const tier = tierOf(g.code, tierList, envList)
            const tone = g.known ? toneOfDot(dotForEnv(g.code, tierList, envList)) : 'neutral'
            return (
              <Fragment key={g.code}>
                <div className={clsx('conn-grp', `tone-${tone}`)}>
                  <span className="conn-grp-name">
                    {g.known ? envLabel(g.code, envList) : `${g.code} · ${t('connEnvGone')}`}
                  </span>
                  {tier && <Badge tone={tone}>{tierLabel(tier.code, tierList, t)}</Badge>}
                  <span className="conn-grp-n">{t('connGrpCount', { n: g.rows.length })}</span>
                  <span className="conn-grp-line">{policyLine(tier)}</span>
                </div>

                {!g.rows.length && <div className="c-empty">{t('connGrpEmpty')}</div>}

                {g.rows.map((c) => (
                  <div
                    key={c.id}
                    className="c-trow clickable"
                    style={{ gridTemplateColumns: COLS }}
                    onClick={() => setDraft(draftOf(c))}
                  >
                    <div className="c-td">
                      <div className="cell-strong">{c.name}</div>
                      <div className="cell-sub">
                        {c.tags ? c.tags : t('connNoTags')}
                      </div>
                    </div>
                    <div className="c-td">{engineDisplay(c.engine)}</div>
                    <div className="c-td mono">
                      {c.host}:{c.port}
                      {c.database ? <div className="cell-sub">{c.database}</div> : null}
                    </div>
                    <div className="c-td mono">{c.defaultRole || '—'}</div>
                    <div className="c-td">
                      <Badge tone={policyTone(c.policy)}>{c.policy}</Badge>
                    </div>
                    <div className="c-td conn-status">
                      {/*
                        徽标本身就是开关:在线 ⇄ 维护是这一页最常按的一下,再塞一个
                        下拉框只是让它多点一次。
                      */}
                      <button
                        type="button"
                        className="conn-status-btn"
                        title={t('connToggleHint')}
                        disabled={toggle.isPending}
                        onClick={(e) => {
                          e.stopPropagation()
                          toggle.mutate({ id: c.id, status: c.status === 'online' ? 'maint' : 'online' })
                        }}
                      >
                        <Badge tone={toneOfStatus(c.status)}>
                          {t(c.status === 'online' ? 'connOnline' : 'connMaint')}
                        </Badge>
                      </button>
                      <Button
                        variant="ghost"
                        title={t('connTest')}
                        disabled={probe.isPending}
                        onClick={(e) => { e.stopPropagation(); probe.mutate(c) }}
                      >
                        <Activity size={14} />
                      </Button>
                    </div>
                  </div>
                ))}
              </Fragment>
            )
          })}
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
    </div>
  )
}

/**
 * 环境下拉的选项。
 *
 * 两台环境重名时把码缀在标签上:下拉绑的是码,但人按的是标签,两个一模一样的
 * 标签里有一个是永远选不中的。
 *
 * `current` 是这台实例现在挂的环境。它已经被删掉时也要出现在列表里 —— 否则下拉
 * 会退回第一项,而"打开看一眼再保存"就会把一台实例悄悄搬进生产。
 */
function envOptions(envs: Environment[], current: string) {
  const dup = new Map<string, number>()
  for (const e of envs) dup.set(e.displayName, (dup.get(e.displayName) ?? 0) + 1)
  const opts = envs.map((e) => ({
    code: e.code,
    label: (dup.get(e.displayName) ?? 0) > 1 ? `${e.displayName} (${e.code})` : e.displayName,
  }))
  if (current && !opts.some((o) => o.code === current)) opts.push({ code: current, label: current })
  return opts
}

/** 引擎下拉。存的是目录里的 id;历史上存了展示名的实例照原样留一项,不被悄悄改写。 */
function engineOptions(current: string) {
  const opts = ENGINES.map((e) => ({ id: e.id, label: e.label }))
  if (current && !opts.some((o) => o.id === current)) opts.push({ id: current, label: engineDisplay(current) })
  return opts
}

function ConnectionModal({
  draft, envs, busy, onClose, onSubmit,
}: {
  draft: ConnectionDraft
  envs: Environment[]
  busy: boolean
  onClose: () => void
  onSubmit: (d: ConnectionDraft) => void
}) {
  const { t } = useTranslation()
  const [f, setF] = useState<ConnectionDraft>(draft)
  const set = (patch: Partial<ConnectionDraft>) => setF((p) => ({ ...p, ...patch }))
  const editing = !!f.id

  return (
    <Modal
      open
      title={t(editing ? 'connEditTitle' : 'connNew')}
      sub={t('connModalSub')}
      width={640}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>{t('cancel')}</Button>
          <Button
            variant="primary"
            disabled={busy || !f.name.trim() || !f.host.trim() || !f.env}
            onClick={() => onSubmit(f)}
          >
            {busy ? t('connSaving') : t('save')}
          </Button>
        </>
      }
    >
      <div className="fld-2">
        <div className="fld">
          <label>{t('connFName')}</label>
          <input value={f.name} onChange={(e) => set({ name: e.target.value })} />
        </div>
        <div className="fld">
          <label>{t('connFEngine')}</label>
          <select value={f.engine} onChange={(e) => set({ engine: e.target.value })}>
            {engineOptions(draft.engine).map((o) => (
              <option key={o.id} value={o.id}>{o.label}</option>
            ))}
          </select>
        </div>
      </div>

      <div className="fld-2">
        <div className="fld">
          <label>{t('connFEnv')}</label>
          <select value={f.env} onChange={(e) => set({ env: e.target.value })}>
            {envOptions(envs, draft.env).map((o) => (
              <option key={o.code} value={o.code}>{o.label}</option>
            ))}
          </select>
        </div>
        <div className="fld">
          {/* 端口不单列一个框:服务端收的就是 host:port 这一串,拆开只会多一处能填错。 */}
          <label>{t('connFHost')}</label>
          <input
            value={f.host}
            placeholder="10.20.30.40:3306"
            onChange={(e) => set({ host: e.target.value })}
          />
        </div>
      </div>

      <div className="fld-2">
        <div className="fld">
          <label>{t('connFPolicy')}</label>
          <select value={f.policy} onChange={(e) => set({ policy: e.target.value })}>
            {POLICIES.map((p) => <option key={p} value={p}>{p}</option>)}
          </select>
        </div>
        <div className="fld">
          {/* Oracle 认的是服务名(或 sid/ 前缀的 SID),不是普通 schema 名。 */}
          <label>{t('connFDatabase')}</label>
          <input
            value={f.database}
            placeholder={/oracle/i.test(f.engine) ? t('connDbHintOracle') : 'orders_db'}
            onChange={(e) => set({ database: e.target.value })}
          />
        </div>
      </div>

      <div className="fld-2">
        <div className="fld">
          <label>{t('connFUser')}</label>
          <input
            value={f.username}
            autoComplete="off"
            onChange={(e) => set({ username: e.target.value })}
          />
        </div>
        <div className="fld">
          <label>{t('connFPassword')}</label>
          <input
            type="password"
            value={f.password}
            autoComplete="new-password"
            placeholder={editing ? t('connPwdKeep') : ''}
            onChange={(e) => set({ password: e.target.value })}
          />
        </div>
      </div>

      <div className="fld">
        {/* 标签决定谁碰得到这台实例(判定层会读),所以它和"归哪个项目"不是一回事。 */}
        <label>{t('connFTags')}</label>
        <input
          value={f.tags}
          placeholder="core,payment"
          onChange={(e) => set({ tags: e.target.value })}
        />
      </div>

      <div className="notice">
        <Database size={15} />
        {t('connSaveProbes')}
      </div>
    </Modal>
  )
}
