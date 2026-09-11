import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import clsx from 'clsx'
import { ListX, Plus, X } from 'lucide-react'
import {
  useRiskCommands, useGatewayStats, useUpsertRiskCommand, usePatchRiskCommand, useDeleteRiskCommand,
} from '@/hooks/useRiskRules'
import { envTiersQueryOptions } from '@/api/modules/envtier'
import { tierLabel } from '@/lib/envTierLabels'
import { Card, CardHead } from '@/components/common/Card'
import { Segmented } from '@/components/common/Segmented'
import { Switch } from '@/components/common/Switch'
import { Button } from '@/components/common/Button'
import { Modal } from '@/components/common/Modal'
import { Loading, ErrorState, Empty } from '@/components/common/States'
import type { EnvTier, RiskCommandView } from '@/types'

/** 等级循环:拦截 → 审批 → 放行 → 拦截。 */
const NEXT_LEVEL: Record<string, string> = { high: 'mid', mid: 'off', off: 'high' }
const LEVELS = ['high', 'mid', 'off']
const LEVEL_KEY: Record<string, string> = { high: 'rrLvHigh', mid: 'rrLvMid', off: 'rrLvOff' }

/** 命令名只收大写字母、下划线与空格 —— 判定层按这套词形匹配。 */
const normalize = (s: string) => s.trim().toUpperCase().replace(/[^A-Z_ ]/g, '')

export default function RiskRulesPage() {
  const { t } = useTranslation()
  const { data: cmds, isLoading, error, refetch } = useRiskCommands()
  const { data: tiers } = useQuery(envTiersQueryOptions())
  const { data: stats } = useGatewayStats()
  const upsert = useUpsertRiskCommand()
  const patch = usePatchRiskCommand()
  const del = useDeleteRiskCommand()

  const [picked, setPicked] = useState('')
  const [draft, setDraft] = useState('')
  const [addLevels, setAddLevels] = useState<Record<string, string> | null>(null)

  const tierList: EnvTier[] = tiers ?? []
  const list: RiskCommandView[] = cmds ?? []

  /**
   * 当前分层。**由分层列表决定,不由前端的默认值决定** —— 选中的那个被删掉之后,
   * 页面要落到一个还存在的分层上,而不是停在一个空字典上说"这里什么都没有"。
   */
  const active = tierList.some((x) => x.code === picked) ? picked : tierList[0]?.code ?? ''

  const levelOn = (c: RiskCommandView, tier: string) => c.tiers[tier] || 'off'
  const highs = list.filter((c) => levelOn(c, active) === 'high')
  const mids = list.filter((c) => levelOn(c, active) === 'mid')
  const gated = list.filter((c) => levelOn(c, active) !== 'off')
  const coverage = list.length ? Math.round((gated.length / list.length) * 1000) / 10 : 0
  /** 无 WHERE 的 DELETE/UPDATE:开关挂在分层上,这一页只照实说它在哪几层开着。 */
  const strictTiers = tierList.filter((x) => x.strictNoWhere)

  function openAdd() {
    const name = normalize(draft)
    if (!name) return
    if (list.some((c) => c.command === name)) { setDraft(''); return }
    /*
      预填成服务端本来就会选的那一份,所以常见情况只需要按一下确认。但每一格都在
      屏幕上、都改得动 —— 服务端替人猜的那一下,事后在任何页面上都看不出来。
    */
    const pre: Record<string, string> = {}
    for (const x of tierList) pre[x.code] = x.scanBaseline || x.requireMfa ? 'high' : 'off'
    setAddLevels(pre)
  }

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>{t('rrTitle')}</h1>
          <p>{t('rrSub')}</p>
        </div>
      </header>

      {isLoading && <Loading />}
      {error && <ErrorState error={error} retry={() => refetch()} />}

      {!isLoading && !error && !tierList.length && <Empty hint={t('rrEmptyNoTier')} />}

      {!isLoading && !error && !!tierList.length && (
        <>
          <Card>
            <CardHead
              icon={<ListX size={17} />}
              title={t('rrDictTitle')}
              sub={t('rrDictSub')}
              actions={
                /*
                  分段控件由分层列表渲染,不写死五个:新建一个分层之后它必须自己
                  出现在这里,否则那一层的字典就没有入口,而"没有行"在判定层读作
                  "放行"。
                */
                <Segmented
                  value={active}
                  options={tierList.map((x) => ({ value: x.code, label: x.code.toUpperCase() }))}
                  onChange={setPicked}
                />
              }
            />

            <div className="rr-chips">
              {list.map((c) => {
                const lv = levelOn(c, active)
                return (
                  <div key={c.command} className={clsx('rr-chip', `lv-${lv}`)}>
                    <span className="rr-cmd">{c.command}</span>
                    <button
                      type="button"
                      className="rr-lv"
                      title={t('rrCycleHint')}
                      disabled={patch.isPending}
                      onClick={() => patch.mutate({ command: c.command, tier: active, level: NEXT_LEVEL[lv] })}
                    >
                      {t(LEVEL_KEY[lv])}
                    </button>
                    <button
                      type="button"
                      className="rr-x"
                      title={t('rrRemoveHint')}
                      disabled={del.isPending}
                      onClick={() => del.mutate(c.command)}
                    >
                      <X size={12} />
                    </button>
                  </div>
                )
              })}

              <div className="rr-addchip">
                <input
                  value={draft}
                  placeholder={t('rrAddPh')}
                  onChange={(e) => setDraft(e.target.value)}
                  onKeyDown={(e) => { if (e.key === 'Enter') openAdd() }}
                />
                <button type="button" className="rr-addbtn" title={t('rrAddHint')} onClick={openAdd}>
                  <Plus size={14} />
                </button>
              </div>

              {!list.length && <Empty hint={t('rrEmptyDict')} />}
            </div>
          </Card>

          <div className="rr-stats">
            <Stat n={highs.length} label={t('rrStatBlock')} tone="danger" />
            <Stat n={mids.length} label={t('rrStatApprove')} tone="warning" />
            <Stat n={stats?.intercepts ?? 0} label={t('rrStatHits')} />
            <Stat n={`${coverage}%`} label={t('rrStatCover')} />
          </div>

          {/*
            下面这些卡是**由字典和分层算出来的**,不是另一套可以单独开关的规则 ——
            所以它们的开关是只读的:真正改得动的地方是上面的命令等级,以及分层页上
            的「无 WHERE 拦截」。给一个点得动却什么也不改的开关,比没有开关更糟。
          */}
          <div className="rr-rules">
            {!!highs.length && (
              <RuleCard
                level="high"
                name={t('rrPolHigh')}
                tag={t('rrLvHigh')}
                expr={`tier=${active.toUpperCase()} AND cmd IN (${highs.map((c) => c.command).join(', ')})`}
                hint={t('rrPolFromDict')}
                on
              />
            )}
            <RuleCard
              level={strictTiers.length ? 'high' : 'off'}
              name={t('rrPolStrict')}
              tag={strictTiers.length ? t('rrLvHigh') : t('rrLvOff')}
              expr={
                strictTiers.length
                  ? `tier IN (${strictTiers.map((x) => x.code.toUpperCase()).join(', ')}) AND cmd IN (DELETE, UPDATE) AND NOT contains(WHERE)`
                  : 'cmd IN (DELETE, UPDATE) AND NOT contains(WHERE) → allow'
              }
              hint={t('rrPolStrictWhere')}
              on={!!strictTiers.length}
            />
            {!!mids.length && (
              <RuleCard
                level="mid"
                name={t('rrPolAppr')}
                tag={t('rrLvMid')}
                expr={`tier=${active.toUpperCase()} AND cmd IN (${mids.map((c) => c.command).join(', ')})`}
                hint={t('rrPolFromDict')}
                on
              />
            )}
          </div>
        </>
      )}

      {addLevels && (
        <AddCommandModal
          command={normalize(draft)}
          tiers={tierList}
          levels={addLevels}
          busy={upsert.isPending}
          onChange={setAddLevels}
          onClose={() => setAddLevels(null)}
          onSubmit={() =>
            upsert.mutate(
              { command: normalize(draft), tiers: addLevels },
              { onSuccess: () => { setDraft(''); setAddLevels(null) } },
            )
          }
        />
      )}
    </div>
  )
}

function Stat({ n, label, tone }: { n: number | string; label: string; tone?: 'danger' | 'warning' }) {
  return (
    <div className="rr-stat">
      <div className={clsx('rr-stat-n', tone && `t-${tone}`)}>{n}</div>
      <div className="rr-stat-l">{label}</div>
    </div>
  )
}

function RuleCard({
  level, name, tag, expr, hint, on,
}: {
  level: string
  name: string
  tag: string
  expr: string
  hint: string
  on: boolean
}) {
  return (
    <div className={clsx('rr-rule', `lv-${level}`)}>
      <div className="rr-rule-body">
        <div className="rr-rule-top">
          <span className="rr-rule-name">{name}</span>
          <span className={clsx('rr-rule-tag', `lv-${level}`)}>{tag}</span>
        </div>
        <div className="rr-rule-expr">{expr}</div>
        <div className="rr-rule-hint">{hint}</div>
      </div>
      <Switch checked={on} disabled onChange={() => {}} />
    </div>
  )
}

/**
 * 新增命令时**逐分层**指定等级。
 *
 * 每个分层都列出来,包括保持放行的那些:一条命令在某一层不适用是个决定,它该像
 * 个决定那样被看见,而不是靠"少了一行"去推断。
 */
function AddCommandModal({
  command, tiers, levels, busy, onChange, onClose, onSubmit,
}: {
  command: string
  tiers: EnvTier[]
  levels: Record<string, string>
  busy: boolean
  onChange: (v: Record<string, string>) => void
  onClose: () => void
  onSubmit: () => void
}) {
  const { t } = useTranslation()
  return (
    <Modal
      open
      title={t('rrAddTitle', { cmd: command })}
      sub={t('rrAddSub')}
      width={560}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>{t('cancel')}</Button>
          <Button variant="primary" disabled={busy || !command} onClick={onSubmit}>
            {t('rrAddConfirm')}
          </Button>
        </>
      }
    >
      {tiers.map((x) => (
        <div key={x.code} className="rr-arow">
          <span className="rr-acode">{x.code.toUpperCase()}</span>
          <span className="rr-aname">{tierLabel(x.code, tiers, t)}</span>
          <div className="rr-aseg">
            {LEVELS.map((lv) => (
              <button
                key={lv}
                type="button"
                className={clsx('rr-aseg-i', `lv-${lv}`, levels[x.code] === lv && 'on')}
                onClick={() => onChange({ ...levels, [x.code]: lv })}
              >
                {t(LEVEL_KEY[lv])}
              </button>
            ))}
          </div>
        </div>
      ))}
    </Modal>
  )
}
