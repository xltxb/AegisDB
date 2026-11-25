<script setup lang="ts">
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
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  GitBranch, Plus, Trash2, ShieldCheck, ClipboardCheck, DatabaseBackup, Terminal, Zap, Search, Hourglass, Bell,
} from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSwitch from '@/components/common/VSwitch.vue'
import VSelect from '@/components/common/VSelect.vue'
import StepCard from '@/components/pipelines/StepCard.vue'
import NodeToolbar from '@/components/pipelines/NodeToolbar.vue'
import api from '@/api'
import { envError } from '@/api/http'
import { confirmAction } from '@/lib/confirm'
import { BUILTIN_FLOW_NAME, BUILTIN_FLOW_DESC, builtinLabel } from '@/lib/builtinNames'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import { useEnvTierStore } from '@/stores/envtier'
import type { Pipeline, PipelineStage, RoleBrief, StageType } from '@/types'

const { t } = useI18n()
const ui = useUIStore()
const auth = useAuthStore()
const envtier = useEnvTierStore()
const isAdmin = computed(() => auth.me?.roleCode === 'admin' || (auth.me?.roleCodes || []).includes('admin'))

const pipelines = ref<Pipeline[]>([])
const editing = ref<Pipeline | null>(null)
const dirty = ref(false)

const STAGE_TYPES: StageType[] = ['review', 'approve', 'backup', 'execute', 'auto_execute', 'verify', 'manual', 'notify']
const stageIcon: Record<string, any> = {
  review: ShieldCheck, approve: ClipboardCheck, backup: DatabaseBackup,
  execute: Terminal, auto_execute: Zap, verify: Search, manual: Hourglass, notify: Bell,
}
const typeLabels = computed(() => STAGE_TYPES.map((s) => t('plType_' + s)))
function typeOf(label: string): StageType {
  return STAGE_TYPES.find((s) => t('plType_' + s) === label) || 'review'
}

// The tier picker offers "every tier" plus the tiers that exist; a template
// scoped to a tier cannot be chosen for a release on another one.
const tierOptions = computed(() => [t('plAllTiers'), ...envtier.tierCodes])

// 「自动执行」只能挂在打开了「允许自动执行」的分层上(后端保存时同样校验,这里只是提前说)。
// 适用全部分层 = 也适用生产,一律不行。
const autoExecBlocked = computed(() => {
  const code = editing.value?.tierCode
  if (!code) return t('plAutoNeedTier')
  if (!envtier.tierByCode[code]?.autoExecute) return t('plAutoTierOff', { tier: code.toUpperCase() })
  return ''
})
const addDisabled = computed<Partial<Record<StageType, string>>>(() =>
  autoExecBlocked.value ? { auto_execute: autoExecBlocked.value } : {})

async function load() {
  try {
    pipelines.value = await api.pipelines()
    ui.pageSub = { key: 'plSub2', params: { n: pipelines.value.length } }
    if (!editing.value && pipelines.value.length) select(pipelines.value[0])
  } catch (e) { ui.notifyError(e, t('loadFailed')) }
}
// 审批人:角色列表 + 全局默认链 + 每个角色的成员名。
//
// 建流程的人此前完全看不出这个审批节点将来会找谁 —— 只能去权限页反推 owner
// 角色里有谁。把它显示出来,是这一块最便宜也最值的改动。
const roles = ref<RoleBrief[]>([])
const defaultChain = ref<string[]>([])
const roleMembers = ref<Record<string, string[]>>({})

onMounted(async () => {
  try { await envtier.load() } catch { /* the tier picker degrades to "every tier" */ }
  try { roles.value = await api.roles() } catch { /* 角色选择器降级为空 */ }
  try { defaultChain.value = (await api.approvalChain()).chain.map((m) => m.name) }
  catch { /* 默认链取不到就不显示,不编造 */ }
  await load()
})

// 角色成员按需取:流程编辑器一次只关心被选中的那几个角色。
async function ensureRoleMembers(code: string) {
  if (!code || code in roleMembers.value) return
  const r = roles.value.find((x) => x.code === code)
  if (!r) return
  roleMembers.value[code] = [] // 占位,避免并发重复请求
  try {
    const d = await api.role(r.id)
    // 与后端 decidableApprovers 同口径:服务账号登录不了控制台,批不了单,
    // 显示出来只会让人以为有人能批。
    roleMembers.value[code] = (d.members || []).filter((m: any) => m.kind !== 'service').map((m: any) => m.name)
  } catch { /* 取不到就显示为空,由下面的提示兜底 */ }
}

const roleLabels = computed(() => [t('plApproverDefault'), ...roles.value.map((r) => r.name)])
const confirmRoleLabels = computed(() => [t('plConfirmDefault'), ...roles.value.map((r) => r.name)])
// 三个人工节点,同一套取值:审批节点用 approverRole,两个确认门用 confirmRole。
const roleKeyOf = (st: PipelineStage) => (st.type === 'approve' ? 'approverRole' : 'confirmRole')
function approverLabel(st: PipelineStage) {
  const r = roles.value.find((x) => x.code === cfgGet(st, roleKeyOf(st)))
  return r ? r.name : (st.type === 'approve' ? t('plApproverDefault') : t('plConfirmDefault'))
}
function setApprover(st: PipelineStage, label: string) {
  const r = roles.value.find((x) => x.name === label)
  cfgSet(st, roleKeyOf(st), r ? r.code : '')
  if (r) ensureRoleMembers(r.code)
}
// 这个节点将来会找谁 —— 已解析出的人名,或者说不出来时的实话。
function approverNames(st: PipelineStage): { names: string[]; empty: boolean } {
  const code = cfgGet(st, roleKeyOf(st))
  // 未配角色时:审批走全局默认链(能列出人);两个确认门走"任何有审批权限的人",
  // 那是一条能力规则而不是一份名单,列不出来也不该编 —— 由文案说明。
  if (!code) {
    if (st.type !== 'approve') return { names: [], empty: false }
    return { names: defaultChain.value, empty: defaultChain.value.length === 0 }
  }
  const names = roleMembers.value[code]
  if (names === undefined) { ensureRoleMembers(code); return { names: [], empty: false } }
  return { names, empty: names.length === 0 }
}

// 出厂自带的流程/阶段名按读者的语言显示;用户建的、改过名的原样显示。
// 它们是**种子数据**不是界面文案 —— 连同输入框为什么不用 v-model,都写在
// lib/builtinNames.ts 里。
const flowName = (v: string) => builtinLabel(BUILTIN_FLOW_NAME, v, t as never)
const flowDesc = (v: string) => builtinLabel(BUILTIN_FLOW_DESC, v, t as never)

async function select(p: Pipeline) {
  if (dirty.value && !(await confirmAction(t('plDiscardConfirm')))) return
  editing.value = JSON.parse(JSON.stringify(p))
  dirty.value = false
}
async function newPipeline() {
  if (dirty.value && !(await confirmAction(t('plDiscardConfirm')))) return
  editing.value = {
    id: 0, name: '', description: '', tierCode: '', enabled: true, isDefault: false,
    // name 留空:阶段名由服务端按类型给出规范值,前端说了不算(见 service/pipeline.go)。
    stages: [
      { name: '', type: 'review', config: '{"failOn":"error"}', onFailure: 'abort' },
      { name: '', type: 'execute', config: '', onFailure: 'abort' },
    ],
  }
  dirty.value = true
}

function addStage(type: StageType) {
  if (!editing.value) return
  editing.value.stages.push({ name: '', type, config: defaultConfig(type), onFailure: 'abort' })
  dirty.value = true
}
function defaultConfig(type: StageType) {
  if (type === 'review') return '{"failOn":"error"}'
  if (type === 'backup' || type === 'verify') return '{"sql":""}'
  return ''
}
function move(i: number, d: -1 | 1) {
  if (!editing.value) return
  const s = editing.value.stages
  const j = i + d
  if (j < 0 || j >= s.length) return
  ;[s[i], s[j]] = [s[j], s[i]]
  dirty.value = true
}
function removeStage(i: number) {
  if (!editing.value) return
  editing.value.stages.splice(i, 1)
  dirty.value = true
}
// 复制:插在原位下面,连配置一起带走。id / stepOrder 不带 —— 那是服务端保存时
// 按顺序重新给的,带着会让两条阶段撞同一个 id。
function duplicateStage(i: number) {
  if (!editing.value) return
  const src = editing.value.stages[i]
  editing.value.stages.splice(i + 1, 0, { name: src.name, type: src.type, config: src.config, onFailure: src.onFailure })
  dirty.value = true
}
function changeType(st: PipelineStage, label: string) {
  st.type = typeOf(label)
  st.config = defaultConfig(st.type)
  dirty.value = true
}

// cfgField reads/writes one key of a stage's JSON config, so the form can offer
// a plain input for the one setting that stage actually has.
function cfgGet(st: PipelineStage, key: string, def = '') {
  try { return (JSON.parse(st.config || '{}')[key] ?? def) as string } catch { return def }
}
function cfgSet(st: PipelineStage, key: string, value: string) {
  let obj: Record<string, unknown> = {}
  try { obj = JSON.parse(st.config || '{}') } catch { obj = {} }
  obj[key] = value
  st.config = JSON.stringify(obj)
  dirty.value = true
}

const tierLabel = computed({
  get: () => (editing.value?.tierCode ? editing.value.tierCode : t('plAllTiers')),
  set: (v: string) => {
    if (!editing.value) return
    editing.value.tierCode = v === t('plAllTiers') ? '' : v
    dirty.value = true
  },
})

async function save() {
  const p = editing.value
  if (!p) return
  if (!p.name.trim()) { ui.notify(t('plNeedName'), 'error'); return }
  if (!p.stages.length) { ui.notify(t('plNeedStage'), 'error'); return }
  try {
    const env = await api.savePipeline(p.id, {
      name: p.name.trim(), description: p.description, tierCode: p.tierCode,
      enabled: p.enabled, isDefault: p.isDefault, stages: p.stages,
    })
    if (env.code !== 0) { ui.notifyError(envError(env), t('actionFailed')); return }
    dirty.value = false
    editing.value = env.data
    await load()
    ui.notify(t('plSaved'), 'success')
  } catch (e) { ui.notifyError(e, t('actionFailed')) }
}

async function remove(p: Pipeline) {
  if (!(await confirmAction(t('plDelConfirm', { name: flowName(p.name) })))) return
  try {
    const env = await api.deletePipeline(p.id)
    if (env.code !== 0) { ui.notifyError(envError(env), t('actionFailed')); return }
    editing.value = null
    dirty.value = false
    await load()
  } catch (e) { ui.notifyError(e, t('actionFailed')) }
}
</script>

<template>
  <div class="page">
    <div class="v-head">
      <div class="v-head-l">
        <div class="v-eyebrow">RELEASE FLOWS</div>
        <div class="v-sub">{{ $t('plSub') }}</div>
      </div>
      <span v-if="!isAdmin" class="roflag">{{ $t('readOnlyPerms') }}</span>
      <VButton v-else variant="primary" height="34px" @click="newPipeline"><Plus :size="15" />{{ $t('plNew') }}</VButton>
    </div>

    <div class="wrap">
      <!-- 流程预设列表 -->
      <div class="list scy">
        <div v-for="p in pipelines" :key="p.id" class="pitem" :class="{ on: editing && editing.id === p.id }" @click="select(p)">
          <div class="ptop">
            <span class="pname">{{ flowName(p.name) }}</span>
            <span v-if="p.isDefault" class="badge">{{ $t('plDefault') }}</span>
            <span v-if="!p.enabled" class="badge off">{{ $t('plDisabled') }}</span>
          </div>
          <div class="pdesc">{{ flowDesc(p.description) || '—' }}</div>
          <!-- 阶段摘要标签化:「人工审批 → 执行变更 → 结果通知」,不再是纯文字折行 -->
          <div class="flow">
            <template v-for="(s, i) in p.stages" :key="i">
              <span v-if="i > 0" class="arrow">→</span>
              <span class="stag"><component :is="stageIcon[s.type] || Terminal" :size="10" />{{ $t('plType_' + s.type) }}</span>
            </template>
          </div>
          <div class="pmeta">
            <span class="tier">{{ p.tierCode ? p.tierCode.toUpperCase() : $t('plAllTiers') }}</span>
            <span class="dim">{{ $t('plStageCount', { n: p.stages.length }) }}</span>
          </div>
        </div>
        <div v-if="!pipelines.length" class="empty">{{ $t('plEmpty') }}</div>
      </div>

      <!-- 编辑器:表头吸顶,中间滚,添加栏吸底 -->
      <div v-if="editing" class="editor">
        <div class="ehead">
          <div class="eic"><GitBranch :size="16" /></div>
          <div class="grow">
            <div class="et">{{ editing.id ? $t('plEdit') : $t('plNew') }}<span v-if="dirty" class="dirty" :title="$t('plUnsaved')" /></div>
            <div class="es">{{ $t('plEditSub') }}</div>
          </div>
          <button v-if="isAdmin && editing.id" type="button" class="del" @click="remove(editing)"><Trash2 :size="14" />{{ $t('delete') }}</button>
          <VButton v-if="isAdmin" variant="primary" height="34px" @click="save">{{ $t('btnSave') }}</VButton>
        </div>

        <div class="ebody scy">
          <!-- 基础配置 -->
          <section class="card">
            <div class="ct">{{ $t('plMeta') }}</div>
            <div class="grid2">
              <div class="field">
                <label class="fl">{{ $t('plName') }}</label>
                <input :value="flowName(editing.name)" class="in" :disabled="!isAdmin"
                       @input="editing.name = ($event.target as HTMLInputElement).value; dirty = true" />
              </div>
              <div class="field">
                <label class="fl">{{ $t('plTier') }}</label>
                <VSelect v-model="tierLabel" :options="tierOptions" height="36px" />
              </div>
            </div>
            <div class="field">
              <label class="fl">{{ $t('plDesc') }}</label>
              <input :value="flowDesc(editing.description)" class="in" :disabled="!isAdmin"
                     @input="editing.description = ($event.target as HTMLInputElement).value; dirty = true" />
            </div>
            <div class="toggle">
              <div class="grow">
                <div class="tt">{{ $t('plEnabled') }}</div>
                <div class="td">{{ $t('plEnabledD') }}</div>
              </div>
              <VSwitch v-model="editing.enabled" :disabled="!isAdmin" @update:model-value="dirty = true" />
            </div>
            <div class="toggle">
              <div class="grow">
                <div class="tt">{{ $t('plIsDefault') }}</div>
                <div class="td">{{ $t('plIsDefaultD') }}</div>
              </div>
              <VSwitch v-model="editing.isDefault" :disabled="!isAdmin" @update:model-value="dirty = true" />
            </div>
          </section>

          <!-- 步骤编排 -->
          <section class="card steps">
            <div class="ct">{{ $t('plStages') }}<span class="cs">{{ $t('plStepsSub') }}</span></div>
            <div class="timeline">
              <StepCard
                v-for="(st, i) in editing.stages" :key="i"
                :stage="st" :index="i" :total="editing.stages.length" :readonly="!isAdmin"
                :type-labels="typeLabels" :role-labels="roleLabels" :confirm-role-labels="confirmRoleLabels"
                :approver-label="approverLabel(st)" :who="approverNames(st)" :auto-warn="autoExecBlocked"
                @move="(d) => move(i, d)" @remove="removeStage(i)" @duplicate="duplicateStage(i)"
                @change-type="(l) => changeType(st, l)" @cfg="(k, v) => cfgSet(st, k, v)"
                @failure="(v) => { st.onFailure = v; dirty = true }"
                @approver="(l) => setApprover(st, l)"
              />
              <div v-if="!editing.stages.length" class="nostep">{{ $t('plNeedStage') }}</div>
            </div>
          </section>
        </div>

        <div v-if="isAdmin" class="efoot">
          <NodeToolbar :types="STAGE_TYPES" :disabled="addDisabled" @add="addStage" />
        </div>
      </div>
      <div v-else class="editor blank">{{ $t('plPick') }}</div>
    </div>
  </div>
</template>

<style scoped>
/* 页面本身不滚:定高 flex,左列与编辑区各自滚。窄屏退回整页滚(见下方媒体查询)。 */
.page { flex: 1; min-height: 0; display: flex; flex-direction: column; padding: var(--page-pad); max-width: var(--page-max); margin-inline: auto; width: 100%; }
.head :deep(.vbtn), .head .roflag { margin-left: auto; }
.roflag { display: inline-flex; align-items: center; height: 26px; padding: 0 11px; border-radius: var(--radius-full); background: var(--surface-sunken); border: 1px solid var(--border-subtle); font: 600 11px var(--font-mono); color: var(--text-muted); }

.wrap { flex: 1; min-height: 0; display: grid; grid-template-columns: minmax(240px, 0.5fr) minmax(0, 2fr); gap: 16px; align-items: stretch; }

/* ---- 左列:流程预设 ---- */
.list { display: flex; flex-direction: column; gap: 8px; min-height: 0; padding-right: 2px; }
.pitem {
  border: 1px solid var(--border-subtle); border-radius: var(--radius-lg); background: var(--surface-card);
  box-shadow: var(--shadow-xs); padding: 11px 13px; cursor: pointer; transition: all 0.15s ease;
}
.pitem:hover { border-color: var(--border-default); box-shadow: var(--shadow-sm); }
.pitem.on { border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-subtle); }
.ptop { display: flex; align-items: center; gap: 7px; }
.pname { font: 500 14px var(--font-body); color: var(--text-strong); }
.badge { padding: 1px 7px; border-radius: var(--radius-full); background: var(--accent-subtle); color: var(--accent-text); font: 600 10px var(--font-body); }
.badge.off { background: var(--surface-sunken); color: var(--text-faint); }
.pdesc { font: 400 12px var(--font-body); color: var(--text-muted); margin-top: 3px; }
.flow { display: flex; align-items: center; flex-wrap: wrap; gap: 4px 3px; margin-top: 8px; }
.stag { display: inline-flex; align-items: center; gap: 4px; padding: 2px 7px; border-radius: 6px; background: var(--surface-sunken); border: 1px solid var(--border-subtle); font: 500 11px var(--font-body); color: var(--text-body); white-space: nowrap; }
.arrow { font: 500 11px var(--font-mono); color: var(--text-faint); }
.pmeta { display: flex; align-items: center; gap: 8px; margin-top: 8px; font: 500 11px var(--font-mono); color: var(--text-muted); }
.tier { padding: 1px 6px; border-radius: 5px; background: var(--accent-subtle); color: var(--accent-text); font: 600 10px var(--font-mono); }
.dim { color: var(--text-faint); }

/* ---- 右侧:编辑器 ---- */
.editor {
  display: flex; flex-direction: column; min-height: 0; min-width: 0;
  border: 1px solid var(--border-subtle); border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-sm);
}
.editor.blank { align-items: center; justify-content: center; font: 500 12.5px var(--font-body); color: var(--text-faint); min-height: 240px; }
.ehead { display: flex; align-items: center; gap: 10px; padding: 12px 16px; border-bottom: 1px solid var(--border-subtle); flex-shrink: 0; }
.eic { width: 30px; height: 30px; border-radius: 8px; background: var(--accent-subtle); color: var(--accent-text); display: flex; align-items: center; justify-content: center; }
.grow { flex: 1; min-width: 0; }
.et { display: flex; align-items: center; gap: 8px; font: 600 16px var(--font-body); color: var(--text-strong); }
.dirty { width: 7px; height: 7px; border-radius: 50%; background: var(--warning-text); }
.es { font: 400 12px var(--font-body); color: var(--text-muted); margin-top: 1px; }
.del {
  display: inline-flex; align-items: center; gap: 6px; height: 34px; padding: 0 12px; border-radius: var(--radius-md);
  border: 1px solid transparent; background: transparent; color: var(--danger-text); font: 500 13px var(--font-body); cursor: pointer;
  transition: all 0.15s ease;
}
.del:hover { background: var(--danger-subtle); border-color: color-mix(in srgb, var(--danger-text) 25%, transparent); }

.ebody { flex: 1; min-height: 0; padding: 14px 16px; display: flex; flex-direction: column; gap: 14px; background: var(--surface-page); }
.card { border: 1px solid var(--border-subtle); border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-xs); padding: 14px 16px; }
.ct { display: flex; align-items: baseline; gap: 8px; font: 500 14px var(--font-body); color: var(--text-strong); margin-bottom: 12px; }
.cs { font: 400 12px var(--font-body); color: var(--text-faint); }
.grid2 { display: grid; grid-template-columns: 1fr 1fr; gap: 12px 16px; }
.field { display: flex; flex-direction: column; gap: 6px; margin-bottom: 12px; min-width: 0; }
.grid2 .field { margin-bottom: 0; }
.grid2 + .field { margin-top: 12px; }
.fl { font: 500 12px var(--font-body); color: var(--text-muted); }
.in {
  width: 100%; box-sizing: border-box; height: 36px; border: 1px solid var(--border-subtle); border-radius: 8px; background: var(--surface-card);
  padding: 0 12px; font: 400 13px var(--font-body); color: var(--text-body); outline: none; transition: all 0.15s ease;
}
.in:focus { border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-subtle); }
.in:disabled { opacity: 0.6; }
.toggle { display: flex; align-items: center; gap: 12px; padding: 10px 0; border-top: 1px solid var(--border-subtle); }
.tt { font: 500 13px var(--font-body); color: var(--text-strong); }
.td { font: 400 12px var(--font-body); color: var(--text-muted); margin-top: 2px; }

.steps { padding-bottom: 6px; }
.timeline { display: flex; flex-direction: column; }
.nostep { padding: 18px; text-align: center; font: 400 12.5px var(--font-body); color: var(--text-faint); border: 1px dashed var(--border-default); border-radius: var(--radius-md); }
.efoot { flex-shrink: 0; padding: 10px 16px; border-top: 1px solid var(--border-subtle); background: var(--surface-card); border-radius: 0 0 var(--radius-lg) var(--radius-lg); }
.empty { padding: 22px; text-align: center; font: 500 12px var(--font-body); color: var(--text-faint); }

@media (max-width: 1080px) {
  .page { overflow-y: auto; display: block; }
  .wrap { grid-template-columns: 1fr; }
  .list, .ebody { overflow: visible; }
  .editor { min-height: 0; }
  .grid2 { grid-template-columns: 1fr; }
}
</style>
