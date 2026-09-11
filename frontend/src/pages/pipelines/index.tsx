import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { ChevronDown, ChevronUp, GitBranch, Plus, Trash2, X } from 'lucide-react'
import { meQueryOptions } from '@/api/modules/auth'
import { envtierApi } from '@/api/modules/envtier'
import { permissionsApi } from '@/api/modules/permissions'
import { useDeletePipeline, usePipelines, useSavePipeline } from '@/hooks/usePipeline'
import { STAGE_TYPES, StageStrip, StageTypeIcon, type StageNode } from '@/components/pipeline/StageStrip'
import { Badge } from '@/components/common/Badge'
import { Button } from '@/components/common/Button'
import { Modal } from '@/components/common/Modal'
import { Segmented } from '@/components/common/Segmented'
import { Switch } from '@/components/common/Switch'
import { Empty, ErrorState, Loading } from '@/components/common/States'
import { isAdminOf } from '@/stores/auth'
import { useUIStore } from '@/stores/ui'
import type { Pipeline, PipelineStage, StageType } from '@/types'

/** 新流程的起手式:审查一道,执行一道。名字留空,由服务端按类型给出规范值。 */
const BLANK: Pipeline = {
  id: 0, name: '', description: '', tierCode: '', enabled: true, isDefault: false,
  stages: [
    { name: '', type: 'review', config: '{"failOn":"error"}', onFailure: 'abort' },
    { name: '', type: 'execute', config: '', onFailure: 'abort' },
  ],
}

export default function PipelinesPage() {
  const { t } = useTranslation()
  const notify = useUIStore((s) => s.notify)
  const { data: me } = useQuery(meQueryOptions())
  const isAdmin = isAdminOf(me ?? null)

  const list = usePipelines()
  const save = useSavePipeline()
  const del = useDeletePipeline()

  const [editing, setEditing] = useState<Pipeline | null>(null)
  const [dirty, setDirty] = useState(false)
  // 有未保存改动时点了别的模板:先问,别把人刚排好的顺序悄悄扔掉。
  const [pending, setPending] = useState<Pipeline | null>(null)
  const [delAsk, setDelAsk] = useState(false)

  const items = list.data ?? []
  /**
   * 还没选过的时候落在第一条上,和工单页一个规矩。
   *
   * 这个兜底是**算出来的**,不是渲染时再 setState —— 后者要多跑一轮渲染,而且
   * 列表刚回来的那一帧里编辑器会先空一下。改动一旦发生,`editing` 就接管。
   */
  const current = editing ?? (items[0] ? clone(items[0]) : null)

  function patch(p: Partial<Pipeline>) {
    if (!current) return
    setEditing({ ...current, ...p })
    setDirty(true)
  }
  function patchStages(fn: (s: PipelineStage[]) => PipelineStage[]) {
    if (!current) return
    setEditing({ ...current, stages: fn(current.stages) })
    setDirty(true)
  }

  function pick(p: Pipeline) {
    if (dirty && current && current.id !== p.id) { setPending(p); return }
    setEditing(clone(p))
    setDirty(false)
  }

  function submit() {
    if (!current) return
    if (!current.name.trim()) { notify(t('plNeedName'), 'error'); return }
    if (!current.stages.length) { notify(t('plNeedStage'), 'error'); return }
    save.mutate(current, {
      onSuccess: (saved) => { setEditing(saved); setDirty(false) },
    })
  }

  const preview: StageNode[] = (current?.stages ?? []).map((s, i) => ({
    key: i, name: s.name || t(`stType_${s.type}`), type: s.type,
  }))

  return (
    <div className="page pl">
      <header className="page-head">
        <div>
          <h1>{t('plTitle')}</h1>
          <p>{t('plSub')}</p>
        </div>
        <div className="grow">
          {isAdmin
            ? (
              <Button variant="primary" onClick={() => { setEditing(clone(BLANK)); setDirty(true) }}>
                <Plus size={15} />{t('plNew')}
              </Button>
            )
            : <Badge tone="neutral">{t('plReadOnly')}</Badge>}
        </div>
      </header>

      <div className="pl-wrap">
        <aside className="pl-list">
          {list.isLoading && <Loading />}
          {list.error && <ErrorState error={list.error} retry={() => list.refetch()} />}
          {!list.isLoading && !list.error && !items.length && <Empty hint={t('plEmpty')} />}
          {items.map((p) => (
            <button
              key={p.id}
              type="button"
              className={`pl-item${current?.id === p.id ? ' on' : ''}`}
              onClick={() => pick(p)}
            >
              <span className="pl-item-top">
                <span className="pl-name">{p.name}</span>
                {p.isDefault && <Badge tone="accent">{t('plDefault')}</Badge>}
                {!p.enabled && <Badge tone="neutral">{t('plDisabled')}</Badge>}
              </span>
              <span className="pl-desc">{p.description || '—'}</span>
              <span className="pl-item-meta">
                <span className="chg-tag">{p.tierCode ? p.tierCode.toUpperCase() : t('plAllTiers')}</span>
                <span className="dim">{t('plStageCount', { n: p.stages.length })}</span>
              </span>
              <StageStrip compact stages={p.stages.map((s, i) => ({ key: i, name: s.name, type: s.type }))} />
            </button>
          ))}
        </aside>

        {!current && <section className="pl-editor"><Empty hint={t('plPick')} /></section>}
        {current && (
          <section className="pl-editor">
            <header className="chg-dhead">
              <span className="chg-dicon"><GitBranch size={17} /></span>
              <div className="chg-dtitles">
                <div className="chg-dt">{current.id ? t('plEdit') : t('plNew')}</div>
                <div className="chg-ds">{t('plEditSub')}</div>
              </div>
              {isAdmin && current.id > 0 && (
                <Button variant="danger" onClick={() => setDelAsk(true)}>
                  <Trash2 size={14} />{t('plDelete')}
                </Button>
              )}
              {isAdmin && (
                <Button variant="primary" disabled={save.isPending || !dirty} onClick={submit}>
                  {t('plSave')}
                </Button>
              )}
            </header>

            <div className="pl-egrid">
              <div className="pl-stages">
                <div className="pl-flabel">{t('plStages')}</div>
                {current.stages.map((st, i) => (
                  <StageRow
                    key={i}
                    index={i}
                    total={current.stages.length}
                    stage={st}
                    editable={isAdmin}
                    onChange={(next) => patchStages((s) => s.map((x, j) => (j === i ? next : x)))}
                    onMove={(d) => patchStages((s) => swap(s, i, i + d))}
                    onRemove={() => patchStages((s) => s.filter((_, j) => j !== i))}
                  />
                ))}
                {!current.stages.length && <div className="chg-hint">{t('plNeedStage')}</div>}

                {isAdmin && (
                  <div className="pl-addbar">
                    <span className="dim">{t('plAddStage')}</span>
                    {STAGE_TYPES.map((ty) => (
                      <button
                        key={ty}
                        type="button"
                        className="pl-add"
                        onClick={() => patchStages((s) => [
                          ...s, { name: '', type: ty, config: defaultConfig(ty), onFailure: 'abort' },
                        ])}
                      >
                        <StageTypeIcon type={ty} size={12} />{t(`stType_${ty}`)}
                      </button>
                    ))}
                  </div>
                )}
              </div>

              <aside className="pl-props">
                <div className="fld">
                  <label>{t('plName')}</label>
                  <input
                    value={current.name} disabled={!isAdmin}
                    onChange={(e) => patch({ name: e.target.value })}
                  />
                </div>
                <div className="fld">
                  <label>{t('plDesc')}</label>
                  <input
                    value={current.description} disabled={!isAdmin}
                    onChange={(e) => patch({ description: e.target.value })}
                  />
                </div>
                <TierPicker
                  value={current.tierCode}
                  disabled={!isAdmin}
                  onChange={(v) => patch({ tierCode: v })}
                />
                <div className="pl-toggle">
                  <span>{t('plEnabled')}</span>
                  <Switch checked={current.enabled} disabled={!isAdmin} onChange={(v) => patch({ enabled: v })} />
                </div>
                <div className="pl-toggle">
                  <span>{t('plIsDefault')}</span>
                  <Switch checked={current.isDefault} disabled={!isAdmin} onChange={(v) => patch({ isDefault: v })} />
                </div>
                <div className="pl-flabel">{t('plPreview')}</div>
                <StageStrip stages={preview} />
              </aside>
            </div>
          </section>
        )}
      </div>

      <Modal
        open={Boolean(pending)}
        title={t('plDiscardTitle')}
        sub={t('plDiscard')}
        onClose={() => setPending(null)}
        footer={
          <>
            <Button variant="ghost" onClick={() => setPending(null)}>{t('cancel')}</Button>
            <Button
              variant="danger"
              onClick={() => {
                if (pending) { setEditing(clone(pending)); setDirty(false) }
                setPending(null)
              }}
            >
              {t('plDiscardGo')}
            </Button>
          </>
        }
      >
        <p className="chg-hint">{current?.name}</p>
      </Modal>

      <Modal
        open={delAsk}
        title={t('plDelTitle')}
        sub={t('plDelConfirm')}
        onClose={() => setDelAsk(false)}
        footer={
          <>
            <Button variant="ghost" onClick={() => setDelAsk(false)}>{t('cancel')}</Button>
            <Button
              variant="danger"
              disabled={del.isPending}
              onClick={() => current && del.mutate(current.id, {
                onSuccess: () => { setDelAsk(false); setEditing(null); setDirty(false) },
              })}
            >
              {t('plDelete')}
            </Button>
          </>
        }
      >
        <p className="chg-hint">{current?.name}</p>
      </Modal>
    </div>
  )
}

/**
 * 一个阶段。
 *
 * 排序用上下箭头而不是拖拽:一条流程通常只有三五个阶段,箭头点一下就是一格,
 * 键盘也够得着;拖拽在这个体量上只是多一种失手的方式。
 */
function StageRow({
  index, total, stage, editable, onChange, onMove, onRemove,
}: {
  index: number
  total: number
  stage: PipelineStage
  editable: boolean
  onChange: (s: PipelineStage) => void
  onMove: (delta: number) => void
  onRemove: () => void
}) {
  const { t } = useTranslation()
  const setCfg = (key: string, value: string) => onChange({ ...stage, config: cfgSet(stage.config, key, value) })

  return (
    <div className="pl-stage">
      <span className="pl-sidx">{index + 1}</span>
      <span className="pl-sic"><StageTypeIcon type={stage.type} size={15} /></span>
      <div className="pl-sbody">
        <div className="pl-srow">
          {/* 阶段名 = 阶段类型。这个名字会进快照与通知正文,所以存的那份由服务端
              按类型给出规范值,这里只挑类型。 */}
          <select
            className="pl-sel" value={stage.type} disabled={!editable}
            onChange={(e) => {
              const ty = e.target.value as StageType
              onChange({ ...stage, type: ty, config: defaultConfig(ty) })
            }}
          >
            {STAGE_TYPES.map((ty) => <option key={ty} value={ty}>{t(`stType_${ty}`)}</option>)}
          </select>
        </div>

        {stage.type === 'review' && (
          <div className="pl-srow">
            <span className="pl-clabel">{t('plFailOn')}</span>
            <Segmented<string>
              value={cfgGet(stage.config, 'failOn') || 'error'}
              options={['error', 'warn', 'none'].map((m) => ({ value: m, label: t(`plFailOn_${m}`) }))}
              onChange={(v) => editable && setCfg('failOn', v)}
            />
          </div>
        )}

        {(stage.type === 'backup' || stage.type === 'verify') && (
          <div className="pl-srow">
            <span className="pl-clabel">SQL</span>
            <input
              className="pl-in" disabled={!editable} value={cfgGet(stage.config, 'sql')}
              placeholder={stage.type === 'backup' ? t('plBackupPh') : t('plVerifyPh')}
              onChange={(e) => setCfg('sql', e.target.value)}
            />
          </div>
        )}

        {stage.type === 'manual' && (
          <div className="pl-srow">
            <span className="pl-clabel">{t('plNote')}</span>
            <input
              className="pl-in" disabled={!editable} value={cfgGet(stage.config, 'note')}
              placeholder={t('plNotePh')}
              onChange={(e) => setCfg('note', e.target.value)}
            />
          </div>
        )}

        {/* 三个人工节点同一套取值:审批用 approverRole,两道确认门用 confirmRole。 */}
        {(stage.type === 'approve' || stage.type === 'manual' || stage.type === 'execute') && (
          <RolePicker
            label={stage.type === 'approve' ? t('plApprover') : t('plConfirmer')}
            fallback={stage.type === 'approve' ? t('plRoleDefault') : t('plConfirmDefault')}
            value={cfgGet(stage.config, stage.type === 'approve' ? 'approverRole' : 'confirmRole')}
            disabled={!editable}
            onChange={(code) => setCfg(stage.type === 'approve' ? 'approverRole' : 'confirmRole', code)}
          />
        )}

        <div className="pl-shint">{t(`plHint_${stage.type}`)}</div>
      </div>

      {editable && (
        <div className="pl-sacts">
          <button type="button" className="pl-sb" title={t('plMoveUp')} disabled={index === 0} onClick={() => onMove(-1)}>
            <ChevronUp size={13} />
          </button>
          <button type="button" className="pl-sb" title={t('plMoveDown')} disabled={index === total - 1} onClick={() => onMove(1)}>
            <ChevronDown size={13} />
          </button>
          <button type="button" className="pl-sb danger" title={t('plRemoveStage')} onClick={onRemove}>
            <X size={13} />
          </button>
        </div>
      )}
    </div>
  )
}

/**
 * 审批 / 确认角色。
 *
 * 角色表在这里是**只读的参考数据**,而权限页才是它的主人 —— 所以不在这里建
 * `rolesQueryOptions`,免得和那一页各出一份同名导出;共用 queryKey 就够了,
 * 缓存本来就是按 key 合并的。取不到就只剩"默认",而不是把整页拦下来。
 */
function RolePicker({
  label, fallback, value, disabled, onChange,
}: {
  label: string
  fallback: string
  value: string
  disabled: boolean
  onChange: (code: string) => void
}) {
  const { data: roles } = useQuery({
    queryKey: ['roles'] as const, queryFn: permissionsApi.roles, staleTime: 60_000, retry: false,
  })
  return (
    <div className="pl-srow">
      <span className="pl-clabel">{label}</span>
      <select className="pl-sel" value={value} disabled={disabled} onChange={(e) => onChange(e.target.value)}>
        <option value="">{fallback}</option>
        {(roles ?? []).map((r) => <option key={r.id} value={r.code}>{r.name}</option>)}
      </select>
    </div>
  )
}

/** 适用分层。空 = 全部分层;选了某一层,别的分层的变更就挑不到这条流程。 */
function TierPicker({
  value, disabled, onChange,
}: { value: string; disabled: boolean; onChange: (v: string) => void }) {
  const { t } = useTranslation()
  const { data: tiers } = useQuery({
    queryKey: ['env-tiers'] as const, queryFn: envtierApi.envTiers, staleTime: 60_000, retry: false,
  })
  return (
    <div className="fld">
      <label>{t('plTier')}</label>
      <select value={value} disabled={disabled} onChange={(e) => onChange(e.target.value)}>
        <option value="">{t('plAllTiers')}</option>
        {(tiers ?? []).map((tr) => (
          <option key={tr.code} value={tr.code}>{tr.displayName || tr.code.toUpperCase()}</option>
        ))}
      </select>
    </div>
  )
}

// ---- 小工具 ----

/** 编辑器改的是一份副本;没点保存就切走,原来那条流程不该已经变了样。 */
function clone<T>(v: T): T {
  return JSON.parse(JSON.stringify(v)) as T
}

function swap(stages: PipelineStage[], i: number, j: number): PipelineStage[] {
  if (j < 0 || j >= stages.length) return stages
  const next = [...stages]
  ;[next[i], next[j]] = [next[j], next[i]]
  return next
}

function defaultConfig(type: StageType): string {
  if (type === 'review') return '{"failOn":"error"}'
  if (type === 'backup' || type === 'verify') return '{"sql":""}'
  return ''
}

/** 阶段配置整个存成一段 JSON;这两个函数只动其中一个键,别的键原样留着。 */
function cfgGet(config: string, key: string): string {
  try { return (JSON.parse(config || '{}')[key] ?? '') as string } catch { return '' }
}
function cfgSet(config: string, key: string, value: string): string {
  let obj: Record<string, unknown> = {}
  try { obj = JSON.parse(config || '{}') } catch { obj = {} }
  obj[key] = value
  return JSON.stringify(obj)
}
