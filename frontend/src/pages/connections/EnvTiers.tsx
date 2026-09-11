import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { Boxes, Layers, Pencil, Plus, ShieldCheck, Trash2 } from 'lucide-react'
import { envTiersQueryOptions, environmentsQueryOptions } from '@/api/modules/envtier'
import {
  useCreateEnvTier, useCreateEnvironment, useDeleteEnvTier, useDeleteEnvironment,
  useEnvironmentUsage, useUpdateEnvTier, useUpdateEnvironment,
} from '@/hooks/useEnvTier'
import { dotFor } from '@/lib/envTierLabels'
import { toneOfDot } from '@/lib/tierTone'
import { confirmAction } from '@/lib/confirm'
import { Badge } from '@/components/common/Badge'
import { Button } from '@/components/common/Button'
import { Modal } from '@/components/common/Modal'
import { Segmented } from '@/components/common/Segmented'
import { Switch } from '@/components/common/Switch'
import { Empty } from '@/components/common/States'
import type { EnvTier, Environment } from '@/types'

/**
 * 分层与环境。
 *
 * **分层持有规则,环境只决定实例归属**,一个分层底下可以挂很多环境 —— 所以加一个
 * 生产集群是在 prod 分层下新建一个环境:不复制任何规则行,建好的那一刻就受完整管控。
 *
 * 这一块上几乎每个控件都是闸门而不是便利功能:两处规则查表都把"查不到"当成放行,
 * 于是一个没有规则行的分层、或者一台指着已删除环境的实例,就是一台长得和受管控实例
 * 一模一样的、不受任何管控的生产库。服务端逐条拦着,界面把理由先说出来,免得人从
 * 一次被拒的请求里才发现。
 *
 * 显示名一律取**库里存的那一串**,不按 code 翻译:管理员改过名之后,界面上就该是
 * 他改的那个名字 —— 这是把分层做成数据所要付的代价,也是它的意义。
 */
export function EnvTiers() {
  const { t } = useTranslation()
  const [tab, setTab] = useState<'tiers' | 'envs'>('tiers')
  const { data: tierData } = useQuery(envTiersQueryOptions())
  const { data: envData } = useQuery(environmentsQueryOptions())

  const tiers: EnvTier[] = tierData ?? []
  const envs: Environment[] = envData ?? []

  return (
    <>
      <div className="conn-et-tabs">
        <Segmented
          value={tab}
          onChange={setTab}
          options={[
            { value: 'tiers', label: `${t('etTierTitle')} · ${tiers.length}` },
            { value: 'envs', label: `${t('etEnvTitle')} · ${envs.length}` },
          ]}
        />
      </div>
      <p className="conn-et-intro">{t('etIntro')}</p>
      {tab === 'tiers' ? <TierTable tiers={tiers} envs={envs} /> : <EnvTable tiers={tiers} envs={envs} />}
    </>
  )
}

// ------------------------------------------------------------------- 分层

const TIER_COLS = '1.6fr 88px 88px 88px 88px 120px 1.2fr 76px'

function TierTable({ tiers, envs }: { tiers: EnvTier[]; envs: Environment[] }) {
  const { t } = useTranslation()
  const update = useUpdateEnvTier()
  const del = useDeleteEnvTier()
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<EnvTier | null>(null)

  const boundEnvs = (code: string) => envs.filter((e) => e.tierCode === code)

  /** 能不能删,以及删不了的**理由** —— 一个点不动又不说为什么的按钮比没有更糟。 */
  function blockReason(tier: EnvTier): string {
    if (tier.scanBaseline) return t('etBlockedBaseline')
    const n = boundEnvs(tier.code).length
    if (n > 0) return t('etBlockedBound', { n })
    if (tiers.length <= 1) return t('etBlockedLast')
    return ''
  }

  /**
   * 基准分层是**单选**,不是开关:同一时刻只有一个分层持有它,而且它永远不能被
   * 关掉,只能转移 —— 没有基准时脚本扫描什么都匹配不上,于是每一份上传的脚本都
   * 被报成干净的,而且不会在任何地方报错。
   */
  function makeBaseline(tier: EnvTier) {
    if (tier.scanBaseline) return
    if (!confirmAction(t('etBaselineConfirm', { code: tier.code }))) return
    update.mutate({ tier, patch: { scanBaseline: true } })
  }

  function remove(tier: EnvTier) {
    if (blockReason(tier)) return
    if (!confirmAction(t('etTierDeleteConfirm', { code: tier.code }))) return
    del.mutate(tier.code)
  }

  return (
    <>
      <div className="conn-et-head">
        <span className="c-card-icon"><Layers size={16} /></span>
        <div>
          <h2>{t('etTierTitle')}</h2>
          <p>{t('etTierSub')}</p>
        </div>
        <Button variant="secondary" onClick={() => setCreating(true)}>
          <Plus size={14} />{t('etNewTier')}
        </Button>
      </div>

      <div className="c-table">
        <div className="c-thead" style={{ gridTemplateColumns: TIER_COLS }}>
          <div>{t('etColTier')}</div>
          <div className="ctr">{t('etColMfa')}</div>
          <div className="ctr">{t('etColBanner')}</div>
          <div className="ctr">{t('etColPending')}</div>
          <div className="ctr" title={t('etColStrictHint')}>{t('etColStrict')}</div>
          <div className="ctr">{t('etColBaseline')}</div>
          <div>{t('etColBound')}</div>
          <div />
        </div>

        {!tiers.length && <div className="c-empty">{t('etNoTier')}</div>}

        {tiers.map((tier) => {
          const reason = blockReason(tier)
          const bound = boundEnvs(tier.code)
          return (
            <div key={tier.code} className="c-trow" style={{ gridTemplateColumns: TIER_COLS }}>
              <div className="c-td conn-et-name">
                <Badge tone={toneOfDot(dotFor(tier.code, tiers))}>{tier.code}</Badge>
                <span>
                  <span className="cell-strong">{tier.displayName || tier.code}</span>
                  <span className="cell-sub">{tier.connLayer} · {tier.defaultRole}</span>
                </span>
              </div>
              <div className="c-td ctr">
                <Switch
                  checked={tier.requireMfa}
                  disabled={update.isPending}
                  onChange={(v) => update.mutate({ tier, patch: { requireMfa: v } })}
                />
              </div>
              <div className="c-td ctr">
                <Switch
                  checked={tier.dangerBanner}
                  disabled={update.isPending}
                  onChange={(v) => update.mutate({ tier, patch: { dangerBanner: v } })}
                />
              </div>
              <div className="c-td ctr">
                <Switch
                  checked={tier.countsInPending}
                  disabled={update.isPending}
                  onChange={(v) => update.mutate({ tier, patch: { countsInPending: v } })}
                />
              </div>
              <div className="c-td ctr" title={t('etColStrictHint')}>
                <Switch
                  checked={tier.strictNoWhere}
                  disabled={update.isPending}
                  onChange={(v) => update.mutate({ tier, patch: { strictNoWhere: v } })}
                />
              </div>
              <div className="c-td ctr">
                {tier.scanBaseline ? (
                  <Badge tone="success" icon={<ShieldCheck size={12} />}>{t('etBaselineOn')}</Badge>
                ) : (
                  <button
                    type="button"
                    className="conn-linkbtn"
                    disabled={update.isPending}
                    onClick={() => makeBaseline(tier)}
                  >
                    {t('etBaselineSet')}
                  </button>
                )}
              </div>
              <div className="c-td conn-chipline">
                {bound.length
                  ? bound.map((e) => <span key={e.code} className="conn-chip">{e.code}</span>)
                  : <span className="cell-sub">{t('etNoEnv')}</span>}
              </div>
              <div className="c-td row-ops">
                <Button variant="ghost" title={t('etRenameTier')} onClick={() => setEditing(tier)}>
                  <Pencil size={14} />
                </Button>
                <Button
                  variant="ghost"
                  title={reason || t('etDelete')}
                  disabled={!!reason || del.isPending}
                  onClick={() => remove(tier)}
                >
                  <Trash2 size={14} />
                </Button>
              </div>
            </div>
          )
        })}
      </div>

      {creating && <TierForm tiers={tiers} onClose={() => setCreating(false)} />}
      {editing && <TierEdit tier={editing} onClose={() => setEditing(null)} />}
    </>
  )
}

/** 新建分层。模板是**必选**的 —— 见 etCloneNote:没有规则行的分层等于全放行。 */
function TierForm({ tiers, onClose }: { tiers: EnvTier[]; onClose: () => void }) {
  const { t } = useTranslation()
  const create = useCreateEnvTier()
  const [f, setF] = useState({
    code: '', displayName: '', templateCode: tiers[0]?.code ?? '',
    connLayer: '', defaultRole: 'dba_l2', sortOrder: tiers.length,
    requireMfa: false, dangerBanner: false, countsInPending: false, strictNoWhere: true,
  })
  const set = (patch: Partial<typeof f>) => setF((p) => ({ ...p, ...patch }))

  return (
    <Modal
      open
      title={t('etNewTier')}
      sub={t('etTierSub')}
      width={620}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>{t('cancel')}</Button>
          <Button
            variant="primary"
            disabled={create.isPending || !f.code.trim() || !f.displayName.trim() || !f.templateCode}
            onClick={() => create.mutate(
              { ...f, code: f.code.trim(), displayName: f.displayName.trim(), scanBaseline: false },
              { onSuccess: onClose },
            )}
          >
            {t('save')}
          </Button>
        </>
      }
    >
      <div className="fld-2">
        <div className="fld">
          <label>{t('etFCode')}</label>
          <input value={f.code} placeholder="prod-hk" onChange={(e) => set({ code: e.target.value })} />
        </div>
        <div className="fld">
          <label>{t('etFName')}</label>
          <input
            value={f.displayName}
            placeholder={t('etFNamePh')}
            onChange={(e) => set({ displayName: e.target.value })}
          />
        </div>
      </div>
      <div className="fld-2">
        <div className="fld">
          <label>{t('etFTemplate')}</label>
          <select value={f.templateCode} onChange={(e) => set({ templateCode: e.target.value })}>
            {tiers.map((x) => (
              <option key={x.code} value={x.code}>{x.code} · {x.displayName || x.code}</option>
            ))}
          </select>
        </div>
        <div className="fld">
          <label>{t('etFLayer')}</label>
          <input
            value={f.connLayer}
            placeholder={t('etLayerPh')}
            onChange={(e) => set({ connLayer: e.target.value })}
          />
        </div>
      </div>

      {/* 把克隆这件事说出来,正是"模板必选"的全部意义:操作的人是在复制一套规则,
          不是在起一个标签名。 */}
      <div className="notice warn">{t('etCloneNote', { from: f.templateCode || '—' })}</div>

      <div className="conn-et-switches">
        <label><Switch checked={f.requireMfa} onChange={(v) => set({ requireMfa: v })} />{t('etColMfa')}</label>
        <label><Switch checked={f.dangerBanner} onChange={(v) => set({ dangerBanner: v })} />{t('etColBanner')}</label>
        <label><Switch checked={f.countsInPending} onChange={(v) => set({ countsInPending: v })} />{t('etColPending')}</label>
        <label title={t('etColStrictHint')}>
          <Switch checked={f.strictNoWhere} onChange={(v) => set({ strictNoWhere: v })} />{t('etColStrict')}
        </label>
      </div>
    </Modal>
  )
}

/**
 * 改一个分层的显示名(以及层级 / 默认角色)。
 *
 * `code` 不可改:它是规则行、审批与审计记录引用的那一串,改掉等于把历史记录指向
 * 一个不存在的分层。要换名字改的是**显示名** —— 它正是界面上到处显示的那一个。
 */
function TierEdit({ tier, onClose }: { tier: EnvTier; onClose: () => void }) {
  const { t } = useTranslation()
  const update = useUpdateEnvTier()
  const [f, setF] = useState({
    displayName: tier.displayName, connLayer: tier.connLayer, defaultRole: tier.defaultRole,
  })
  const set = (patch: Partial<typeof f>) => setF((p) => ({ ...p, ...patch }))

  return (
    <Modal
      open
      title={t('etRenameTier')}
      sub={tier.code}
      width={520}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>{t('cancel')}</Button>
          <Button
            variant="primary"
            disabled={update.isPending || !f.displayName.trim()}
            onClick={() => update.mutate(
              { tier, patch: { ...f, displayName: f.displayName.trim() } },
              { onSuccess: onClose },
            )}
          >
            {t('save')}
          </Button>
        </>
      }
    >
      <div className="fld">
        <label>{t('etFName')}</label>
        <input value={f.displayName} onChange={(e) => set({ displayName: e.target.value })} />
      </div>
      <div className="fld-2">
        <div className="fld">
          <label>{t('etFLayer')}</label>
          <input value={f.connLayer} onChange={(e) => set({ connLayer: e.target.value })} />
        </div>
        <div className="fld">
          <label>{t('etFRole')}</label>
          <input value={f.defaultRole} onChange={(e) => set({ defaultRole: e.target.value })} />
        </div>
      </div>
      <div className="notice">{t('etCodeFixed', { code: tier.code })}</div>
    </Modal>
  )
}

// ------------------------------------------------------------------- 环境

const ENV_COLS = '1.6fr 1.6fr 120px 96px'

function EnvTable({ tiers, envs }: { tiers: EnvTier[]; envs: Environment[] }) {
  const { t } = useTranslation()
  const { data: usage } = useEnvironmentUsage()
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<Environment | null>(null)
  const [deleting, setDeleting] = useState<Environment | null>(null)

  const count = (code: string) => usage?.[code] ?? 0

  return (
    <>
      <div className="conn-et-head">
        <span className="c-card-icon"><Boxes size={16} /></span>
        <div>
          <h2>{t('etEnvTitle')}</h2>
          <p>{t('etEnvSub')}</p>
        </div>
        <Button variant="secondary" disabled={!tiers.length} onClick={() => setCreating(true)}>
          <Plus size={14} />{t('etNewEnv')}
        </Button>
      </div>

      <div className="c-table">
        <div className="c-thead" style={{ gridTemplateColumns: ENV_COLS }}>
          <div>{t('etColEnv')}</div>
          <div>{t('etColTierBind')}</div>
          <div className="ctr">{t('etColInsts')}</div>
          <div />
        </div>

        {!envs.length && <div className="c-empty">{t('connEmptyNoEnv')}</div>}

        {envs.map((e) => {
          const tier = tiers.find((x) => x.code === e.tierCode)
          return (
            <div key={e.code} className="c-trow" style={{ gridTemplateColumns: ENV_COLS }}>
              <div className="c-td conn-et-name">
                <Badge tone={toneOfDot(dotFor(e.tierCode, tiers))}>{e.code}</Badge>
                <span className="cell-strong">{e.displayName || e.code}</span>
              </div>
              <div className="c-td conn-et-name">
                <span className="conn-chip">{e.tierCode}</span>
                {/* 分层已被删时这里没有名字可显示 —— 照实说,而不是显示一个空格。 */}
                <span className="cell-sub">{tier?.displayName || t('etTierGone')}</span>
              </div>
              <div className="c-td ctr mono">{t('etInstN', { n: count(e.code) })}</div>
              <div className="c-td row-ops">
                <Button variant="ghost" title={t('etRebind')} onClick={() => setEditing(e)}>
                  <Pencil size={14} />
                </Button>
                <Button
                  variant="ghost"
                  title={envs.length <= 1 ? t('etBlockedLastEnv') : t('etDelete')}
                  disabled={envs.length <= 1}
                  onClick={() => setDeleting(e)}
                >
                  <Trash2 size={14} />
                </Button>
              </div>
            </div>
          )
        })}
      </div>

      {creating && <EnvForm tiers={tiers} count={envs.length} onClose={() => setCreating(false)} />}
      {editing && (
        <EnvEdit env={editing} tiers={tiers} n={count(editing.code)} onClose={() => setEditing(null)} />
      )}
      {deleting && (
        <EnvDelete
          env={deleting}
          envs={envs}
          n={count(deleting.code)}
          onClose={() => setDeleting(null)}
        />
      )}
    </>
  )
}

function EnvForm({ tiers, count, onClose }: { tiers: EnvTier[]; count: number; onClose: () => void }) {
  const { t } = useTranslation()
  const create = useCreateEnvironment()
  const [f, setF] = useState({
    code: '', displayName: '', tierCode: tiers[0]?.code ?? '', sortOrder: count,
  })
  const set = (patch: Partial<typeof f>) => setF((p) => ({ ...p, ...patch }))

  return (
    <Modal
      open
      title={t('etNewEnv')}
      sub={t('etEnvSub')}
      width={560}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>{t('cancel')}</Button>
          <Button
            variant="primary"
            disabled={create.isPending || !f.code.trim() || !f.displayName.trim() || !f.tierCode}
            onClick={() => create.mutate(
              { ...f, code: f.code.trim(), displayName: f.displayName.trim() },
              { onSuccess: onClose },
            )}
          >
            {t('save')}
          </Button>
        </>
      }
    >
      <div className="fld-2">
        <div className="fld">
          <label>{t('etFCode')}</label>
          <input value={f.code} placeholder="prod-hk" onChange={(e) => set({ code: e.target.value })} />
        </div>
        <div className="fld">
          <label>{t('etFName')}</label>
          <input
            value={f.displayName}
            placeholder={t('etFEnvNamePh')}
            onChange={(e) => set({ displayName: e.target.value })}
          />
        </div>
      </div>
      <div className="fld">
        <label>{t('etColTierBind')}</label>
        <select value={f.tierCode} onChange={(e) => set({ tierCode: e.target.value })}>
          {tiers.map((x) => (
            <option key={x.code} value={x.code}>{x.code} · {x.displayName || x.code}</option>
          ))}
        </select>
      </div>
      {/* 新环境不复制任何规则行:它底下的实例直接沿用所绑分层的完整管控,
          不存在一段"还没配规则"的空窗期。 */}
      <div className="notice">{t('etEnvCreateNote', { tier: f.tierCode || '—' })}</div>
    </Modal>
  )
}

/**
 * 改环境的显示名,或把它改绑到另一个分层。
 *
 * 改绑立刻改变这个环境下每一台实例此后被怎么判,所以要二次确认并说清影响多少台。
 * 它**不改写历史**:已有的审批与审计行保留当时判定所依据的分层。
 */
function EnvEdit({
  env, tiers, n, onClose,
}: { env: Environment; tiers: EnvTier[]; n: number; onClose: () => void }) {
  const { t } = useTranslation()
  const update = useUpdateEnvironment()
  const [f, setF] = useState({ displayName: env.displayName, tierCode: env.tierCode })

  function submit() {
    const rebinding = f.tierCode !== env.tierCode
    if (rebinding && !confirmAction(
      t('etRebindConfirm', { code: env.code, from: env.tierCode, to: f.tierCode, n }),
    )) return
    update.mutate(
      { env, patch: { displayName: f.displayName.trim(), tierCode: f.tierCode } },
      { onSuccess: onClose },
    )
  }

  return (
    <Modal
      open
      title={t('etEditEnv')}
      sub={env.code}
      width={520}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>{t('cancel')}</Button>
          <Button
            variant="primary"
            disabled={update.isPending || !f.displayName.trim()}
            onClick={submit}
          >
            {t('save')}
          </Button>
        </>
      }
    >
      <div className="fld">
        <label>{t('etFName')}</label>
        <input
          value={f.displayName}
          onChange={(e) => setF((p) => ({ ...p, displayName: e.target.value }))}
        />
      </div>
      <div className="fld">
        <label>{t('etColTierBind')}</label>
        <select
          value={f.tierCode}
          onChange={(e) => setF((p) => ({ ...p, tierCode: e.target.value }))}
        >
          {tiers.map((x) => (
            <option key={x.code} value={x.code}>{x.code} · {x.displayName || x.code}</option>
          ))}
          {/* 绑的分层被删掉时,原样留一项 —— 否则下拉会退回第一个分层,
              而"打开看一眼再保存"就把一组实例悄悄换了管控等级。 */}
          {!tiers.some((x) => x.code === env.tierCode) && (
            <option value={env.tierCode}>{env.tierCode}</option>
          )}
        </select>
      </div>
      <div className="notice">{t('etCodeFixed', { code: env.code })}</div>
    </Modal>
  )
}

/**
 * 删除环境 —— 它底下的实例被**迁走**,而不是留成孤儿。
 *
 * 一台指着不存在环境的实例解析不到分层,也就没有任何规则,而它在列表上和一台
 * 受完整管控的实例长得一模一样。所以迁移目标是必填的。
 */
function EnvDelete({
  env, envs, n, onClose,
}: { env: Environment; envs: Environment[]; n: number; onClose: () => void }) {
  const { t } = useTranslation()
  const del = useDeleteEnvironment()
  const targets = envs.filter((e) => e.code !== env.code)
  const [moveTo, setMoveTo] = useState(targets[0]?.code ?? '')

  return (
    <Modal
      open
      title={t('etDeleteEnvTitle', { code: env.code })}
      width={520}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>{t('cancel')}</Button>
          <Button
            variant="danger"
            disabled={del.isPending || !moveTo}
            onClick={() => del.mutate({ code: env.code, moveTo }, { onSuccess: onClose })}
          >
            {t('etDelete')}
          </Button>
        </>
      }
    >
      <div className="notice danger">{t('etDeleteEnvWarn', { n })}</div>
      <div className="fld">
        <label>{t('etMoveTo')}</label>
        <select value={moveTo} onChange={(e) => setMoveTo(e.target.value)}>
          {targets.map((e) => (
            <option key={e.code} value={e.code}>{e.code} · {e.displayName || e.code}</option>
          ))}
        </select>
      </div>
      {!targets.length && <Empty hint={t('etBlockedLastEnv')} />}
    </Modal>
  )
}
