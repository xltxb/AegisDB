// 发布流程编排 — 自定义每个流程的阶段与顺序。
//
// A template is a decision about how much ceremony a change needs, so the editor
// shows the whole flow at once rather than hiding stages behind a wizard: the
// thing worth reviewing is the SEQUENCE. Stage configuration is per type and
// stored as JSON, but the common knobs (review strictness, backup/verify SQL,
// manual note) get real inputs — an operator should not have to know the schema
// to set the one field their stage needs.
//
// 页面是编排者:数据、脏标记、审批人成员的按需加载都在这里;每一步的呈现交给
// StepCard,底部的添加入口交给 NodeToolbar。
import { Fragment, useEffect, useState } from 'react'
import clsx from 'clsx'
import {
  GitBranch, Plus, Trash2, ShieldCheck, ClipboardCheck, DatabaseBackup, Terminal, Zap, Search, Hourglass, Bell,
  type LucideIcon,
} from 'lucide-react'
import VButton from '@/components/common/VButton'
import VSwitch from '@/components/common/VSwitch'
import VSelect from '@/components/common/VSelect'
import StepCard from '@/components/pipelines/StepCard'
import NodeToolbar from '@/components/pipelines/NodeToolbar'
import api from '@/api'
import { envError } from '@/api/http'
import { confirmAction } from '@/lib/confirm'
import { BUILTIN_FLOW_NAME, BUILTIN_FLOW_DESC, builtinLabel } from '@/lib/builtinNames'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import { useEnvTierStore } from '@/stores/envtier'
import { useI18n } from '@/hooks/useI18n'
import { useMountEffect } from '@/hooks/effects'
import { useStateRef } from '@/hooks/useStateRef'
import type { Pipeline, PipelineStage, RoleBrief, StageType } from '@/types'
import './PipelinesView.scoped.css'

const STAGE_TYPES: StageType[] = ['review', 'approve', 'backup', 'execute', 'auto_execute', 'verify', 'manual', 'notify']
const stageIcon: Record<string, LucideIcon> = {
  review: ShieldCheck, approve: ClipboardCheck, backup: DatabaseBackup,
  execute: Terminal, auto_execute: Zap, verify: Search, manual: Hourglass, notify: Bell,
}

function defaultConfig(type: StageType) {
  if (type === 'review') return '{"failOn":"error"}'
  if (type === 'backup' || type === 'verify') return '{"sql":""}'
  return ''
}

// cfgGet reads one key of a stage's JSON config, so the form can offer
// a plain input for the one setting that stage actually has.
function cfgGet(st: PipelineStage, key: string, def = '') {
  try { return (JSON.parse(st.config || '{}')[key] ?? def) as string } catch { return def }
}
// 三个人工节点,同一套取值:审批节点用 approverRole,两个确认门用 confirmRole。
const roleKeyOf = (st: PipelineStage) => (st.type === 'approve' ? 'approverRole' : 'confirmRole')

export default function PipelinesView() {
  const { t } = useI18n()
  const ui = useUIStore.getState() // 只用动作,不读状态
  const me = useAuthStore((s) => s.me)
  const tierCodes = useEnvTierStore((s) => s.tierCodes)
  const tierByCode = useEnvTierStore((s) => s.tierByCode)
  const isAdmin = me?.roleCode === 'admin' || (me?.roleCodes || []).includes('admin')

  const [pipelines, setPipelines] = useState<Pipeline[]>([])
  const [editing, setEditing, editingRef] = useStateRef<Pipeline | null>(null)
  const [dirty, setDirty, dirtyRef] = useStateRef(false)

  const typeLabels = STAGE_TYPES.map((s) => t('plType_' + s))
  const typeOf = (label: string): StageType => STAGE_TYPES.find((s) => t('plType_' + s) === label) || 'review'

  // The tier picker offers "every tier" plus the tiers that exist; a template
  // scoped to a tier cannot be chosen for a release on another one.
  const tierOptions = [t('plAllTiers'), ...tierCodes]

  // 「自动执行」只能挂在打开了「允许自动执行」的分层上(后端保存时同样校验,这里只是提前说)。
  // 适用全部分层 = 也适用生产,一律不行。
  const autoExecBlocked = (() => {
    const code = editing?.tierCode
    if (!code) return t('plAutoNeedTier')
    if (!tierByCode[code]?.autoExecute) return t('plAutoTierOff', { tier: code.toUpperCase() })
    return ''
  })()
  const addDisabled: Partial<Record<StageType, string>> = autoExecBlocked ? { auto_execute: autoExecBlocked } : {}

  async function load() {
    try {
      const list = (await api.pipelines()) ?? []
      setPipelines(list)
      ui.setPageSub({ key: 'plSub2', params: { n: list.length } })
      if (!editingRef.current && list.length) void select(list[0])
    } catch (e) { ui.notifyError(e, t('loadFailed')) }
  }
  // 审批人:角色列表 + 全局默认链 + 每个角色的成员名。
  //
  // 建流程的人此前完全看不出这个审批节点将来会找谁 —— 只能去权限页反推 owner
  // 角色里有谁。把它显示出来,是这一块最便宜也最值的改动。
  const [roles, setRoles] = useState<RoleBrief[]>([])
  const [defaultChain, setDefaultChain] = useState<string[]>([])
  const [roleMembers, setRoleMembers, roleMembersRef] = useStateRef<Record<string, string[]>>({})

  useMountEffect(() => {
    void (async () => {
      try { await useEnvTierStore.getState().load() } catch { /* the tier picker degrades to "every tier" */ }
      try { setRoles((await api.roles()) ?? []) } catch { /* 角色选择器降级为空 */ }
      try { setDefaultChain((await api.approvalChain()).chain.map((m) => m.name)) }
      catch { /* 默认链取不到就不显示,不编造 */ }
      await load()
    })()
  })

  // 角色成员按需取:流程编辑器一次只关心被选中的那几个角色。
  async function ensureRoleMembers(code: string) {
    if (!code || code in roleMembersRef.current) return
    const r = roles.find((x) => x.code === code)
    if (!r) return
    setRoleMembers((m) => ({ ...m, [code]: [] })) // 占位,避免并发重复请求
    try {
      const d = await api.role(r.id)
      // 与后端 decidableApprovers 同口径:服务账号登录不了控制台,批不了单,
      // 显示出来只会让人以为有人能批。
      const names = (d.members || []).filter((m) => m.kind !== 'service').map((m) => m.name)
      setRoleMembers((m) => ({ ...m, [code]: names }))
    } catch { /* 取不到就显示为空,由下面的提示兜底 */ }
  }
  // 编辑区里出现了哪些角色,就去取哪些角色的成员。
  useEffect(() => {
    for (const st of editing?.stages ?? []) void ensureRoleMembers(cfgGet(st, roleKeyOf(st)))
    // ensureRoleMembers 只读 roles(已在依赖里)和 ref
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [editing, roles])

  const roleLabels = [t('plApproverDefault'), ...roles.map((r) => r.name)]
  const confirmRoleLabels = [t('plConfirmDefault'), ...roles.map((r) => r.name)]
  function approverLabel(st: PipelineStage) {
    const r = roles.find((x) => x.code === cfgGet(st, roleKeyOf(st)))
    return r ? r.name : (st.type === 'approve' ? t('plApproverDefault') : t('plConfirmDefault'))
  }
  function setApprover(i: number, label: string) {
    const st = editingRef.current?.stages[i]
    if (!st) return
    const r = roles.find((x) => x.name === label)
    cfgSet(i, roleKeyOf(st), r ? r.code : '')
  }
  // 这个节点将来会找谁 —— 已解析出的人名,或者说不出来时的实话。
  function approverNames(st: PipelineStage): { names: string[]; empty: boolean } {
    const code = cfgGet(st, roleKeyOf(st))
    // 未配角色时:审批走全局默认链(能列出人);两个确认门走"任何有审批权限的人",
    // 那是一条能力规则而不是一份名单,列不出来也不该编 —— 由文案说明。
    if (!code) {
      if (st.type !== 'approve') return { names: [], empty: false }
      return { names: defaultChain, empty: defaultChain.length === 0 }
    }
    const names = roleMembers[code]
    if (names === undefined) return { names: [], empty: false }
    return { names, empty: names.length === 0 }
  }

  // 出厂自带的流程/阶段名按读者的语言显示;用户建的、改过名的原样显示。
  // 它们是**种子数据**不是界面文案 —— 连同输入框为什么不做双向绑定,都写在
  // lib/builtinNames.ts 里。
  const flowName = (v: string) => builtinLabel(BUILTIN_FLOW_NAME, v, t)
  const flowDesc = (v: string) => builtinLabel(BUILTIN_FLOW_DESC, v, t)

  async function select(p: Pipeline) {
    if (dirtyRef.current && !(await confirmAction(t('plDiscardConfirm')))) return
    setEditing(JSON.parse(JSON.stringify(p)) as Pipeline)
    setDirty(false)
  }
  async function newPipeline() {
    if (dirtyRef.current && !(await confirmAction(t('plDiscardConfirm')))) return
    setEditing({
      id: 0, name: '', description: '', tierCode: '', enabled: true, isDefault: false,
      // name 留空:阶段名由服务端按类型给出规范值,前端说了不算(见 service/pipeline.go)。
      stages: [
        { name: '', type: 'review', config: '{"failOn":"error"}', onFailure: 'abort' },
        { name: '', type: 'execute', config: '', onFailure: 'abort' },
      ],
    })
    setDirty(true)
  }

  // 改正在编辑的这条流程:一律换新对象,并标脏。
  function edit(fields: Partial<Pipeline>) {
    setEditing((p) => (p ? { ...p, ...fields } : p))
    setDirty(true)
  }
  function editStages(fn: (s: PipelineStage[]) => PipelineStage[]) {
    const p = editingRef.current
    if (p) edit({ stages: fn(p.stages ?? []) })
  }
  const editStage = (i: number, fn: (st: PipelineStage) => PipelineStage) =>
    editStages((s) => s.map((st, k) => (k === i ? fn(st) : st)))

  function addStage(type: StageType) {
    editStages((s) => [...s, { name: '', type, config: defaultConfig(type), onFailure: 'abort' }])
  }
  function move(i: number, d: -1 | 1) {
    const j = i + d
    if (j < 0 || j >= (editingRef.current?.stages.length ?? 0)) return
    editStages((s) => {
      const out = [...s]
      ;[out[i], out[j]] = [out[j], out[i]]
      return out
    })
  }
  function removeStage(i: number) {
    editStages((s) => s.filter((_, k) => k !== i))
  }
  // 复制:插在原位下面,连配置一起带走。id / stepOrder 不带 —— 那是服务端保存时
  // 按顺序重新给的,带着会让两条阶段撞同一个 id。
  function duplicateStage(i: number) {
    editStages((s) => {
      const src = s[i]
      return [
        ...s.slice(0, i + 1),
        { name: src.name, type: src.type, config: src.config, onFailure: src.onFailure },
        ...s.slice(i + 1),
      ]
    })
  }
  function changeType(i: number, label: string) {
    const type = typeOf(label)
    editStage(i, (st) => ({ ...st, type, config: defaultConfig(type) }))
  }
  function cfgSet(i: number, key: string, value: string) {
    editStage(i, (st) => {
      let obj: Record<string, unknown> = {}
      try { obj = JSON.parse(st.config || '{}') } catch { obj = {} }
      obj[key] = value
      return { ...st, config: JSON.stringify(obj) }
    })
  }

  const tierLabel = editing?.tierCode ? editing.tierCode : t('plAllTiers')
  const setTierLabel = (v: string) => edit({ tierCode: v === t('plAllTiers') ? '' : v })

  async function save() {
    const p = editingRef.current
    if (!p) return
    if (!p.name.trim()) { ui.notify(t('plNeedName'), 'error'); return }
    if (!p.stages.length) { ui.notify(t('plNeedStage'), 'error'); return }
    try {
      const env = await api.savePipeline(p.id, {
        name: p.name.trim(), description: p.description, tierCode: p.tierCode,
        enabled: p.enabled, isDefault: p.isDefault, stages: p.stages,
      })
      if (env.code !== 0) { ui.notifyError(envError(env), t('actionFailed')); return }
      setDirty(false)
      setEditing(env.data)
      await load()
      ui.notify(t('plSaved'), 'success')
    } catch (e) { ui.notifyError(e, t('actionFailed')) }
  }

  async function remove(p: Pipeline) {
    if (!(await confirmAction(t('plDelConfirm', { name: flowName(p.name) })))) return
    try {
      const env = await api.deletePipeline(p.id)
      if (env.code !== 0) { ui.notifyError(envError(env), t('actionFailed')); return }
      setEditing(null)
      setDirty(false)
      await load()
    } catch (e) { ui.notifyError(e, t('actionFailed')) }
  }

  const stages = editing?.stages ?? []

  return (
    <div className="page">
      <div className="v-head">
        <div className="v-head-l">
          <div className="v-eyebrow">RELEASE FLOWS</div>
          <div className="v-h1">{t('t_pipelines')}</div>
          <div className="v-sub">{t('plSub')}</div>
        </div>
        {!isAdmin
          ? <span className="roflag">{t('readOnlyPerms')}</span>
          : <VButton variant="primary" height="34px" onClick={newPipeline}><Plus size={15} />{t('plNew')}</VButton>}
      </div>

      <div className="wrap">
        {/* 流程预设列表 */}
        <div className="list scy">
          {pipelines.map((p) => (
            <div key={p.id} className={clsx('pitem', editing && editing.id === p.id && 'on')} onClick={() => select(p)}>
              <div className="ptop">
                <span className="pname">{flowName(p.name)}</span>
                {p.isDefault && <span className="badge">{t('plDefault')}</span>}
                {!p.enabled && <span className="badge off">{t('plDisabled')}</span>}
              </div>
              <div className="pdesc">{flowDesc(p.description) || '—'}</div>
              {/* 阶段摘要标签化:「人工审批 → 执行变更 → 结果通知」,不再是纯文字折行 */}
              <div className="flow">
                {(p.stages ?? []).map((s, i) => {
                  const Icon = stageIcon[s.type] || Terminal
                  return (
                    <Fragment key={i}>
                      {i > 0 && <span className="arrow">→</span>}
                      <span className="stag"><Icon size={10} />{t('plType_' + s.type)}</span>
                    </Fragment>
                  )
                })}
              </div>
              <div className="pmeta">
                <span className="tier">{p.tierCode ? p.tierCode.toUpperCase() : t('plAllTiers')}</span>
                <span className="dim">{t('plStageCount', { n: p.stages?.length ?? 0 })}</span>
              </div>
            </div>
          ))}
          {!pipelines.length && <div className="empty">{t('plEmpty')}</div>}
        </div>

        {/* 编辑器:表头吸顶,中间滚,添加栏吸底 */}
        {editing ? (
          <div className="editor">
            <div className="ehead">
              <div className="eic"><GitBranch size={16} /></div>
              <div className="grow">
                <div className="et">{editing.id ? t('plEdit') : t('plNew')}{dirty && <span className="dirty" title={t('plUnsaved')} />}</div>
                <div className="es">{t('plEditSub')}</div>
              </div>
              {isAdmin && !!editing.id && <button type="button" className="del" onClick={() => remove(editing)}><Trash2 size={14} />{t('delete')}</button>}
              {isAdmin && <VButton variant="primary" height="34px" onClick={save}>{t('btnSave')}</VButton>}
            </div>

            <div className="ebody scy">
              {/* 基础配置 */}
              <section className="card">
                <div className="ct">{t('plMeta')}</div>
                <div className="grid2">
                  <div className="field">
                    <label className="fl">{t('plName')}</label>
                    <input value={flowName(editing.name) ?? ''} className="in" disabled={!isAdmin} onChange={(e) => edit({ name: e.target.value })} />
                  </div>
                  <div className="field">
                    <label className="fl">{t('plTier')}</label>
                    <VSelect value={tierLabel} onChange={setTierLabel} options={tierOptions} height="36px" />
                  </div>
                </div>
                <div className="field">
                  <label className="fl">{t('plDesc')}</label>
                  <input value={flowDesc(editing.description) ?? ''} className="in" disabled={!isAdmin} onChange={(e) => edit({ description: e.target.value })} />
                </div>
                <div className="toggle">
                  <div className="grow">
                    <div className="tt">{t('plEnabled')}</div>
                    <div className="td">{t('plEnabledD')}</div>
                  </div>
                  <VSwitch value={editing.enabled} disabled={!isAdmin} onChange={(v) => edit({ enabled: v })} />
                </div>
                <div className="toggle">
                  <div className="grow">
                    <div className="tt">{t('plIsDefault')}</div>
                    <div className="td">{t('plIsDefaultD')}</div>
                  </div>
                  <VSwitch value={editing.isDefault} disabled={!isAdmin} onChange={(v) => edit({ isDefault: v })} />
                </div>
              </section>

              {/* 步骤编排 */}
              <section className="card steps">
                <div className="ct">{t('plStages')}<span className="cs">{t('plStepsSub')}</span></div>
                <div className="timeline">
                  {stages.map((st, i) => (
                    <StepCard
                      key={i} stage={st} index={i} total={stages.length} readonly={!isAdmin}
                      typeLabels={typeLabels} roleLabels={roleLabels} confirmRoleLabels={confirmRoleLabels}
                      approverLabel={approverLabel(st)} who={approverNames(st)} autoWarn={autoExecBlocked}
                      onMove={(d) => move(i, d)} onRemove={() => removeStage(i)} onDuplicate={() => duplicateStage(i)}
                      onChangeType={(l) => changeType(i, l)} onCfg={(k, v) => cfgSet(i, k, v)}
                      onFailure={(v) => editStage(i, (s) => ({ ...s, onFailure: v }))}
                      onApprover={(l) => setApprover(i, l)}
                    />
                  ))}
                  {!stages.length && <div className="nostep">{t('plNeedStage')}</div>}
                </div>
              </section>
            </div>

            {isAdmin && (
              <div className="efoot">
                <NodeToolbar types={STAGE_TYPES} disabled={addDisabled} onAdd={addStage} />
              </div>
            )}
          </div>
        ) : <div className="editor blank">{t('plPick')}</div>}
      </div>
    </div>
  )
}
