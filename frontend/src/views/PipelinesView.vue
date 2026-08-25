<script setup lang="ts">
// 发布流程编排 — 自定义每个流程的阶段与顺序。
//
// A template is a decision about how much ceremony a change needs, so the editor
// shows the whole flow at once rather than hiding stages behind a wizard: the
// thing worth reviewing is the SEQUENCE. Stage configuration is per type and
// stored as JSON, but the common knobs (review strictness, backup/verify SQL,
// manual note) get real inputs — an operator should not have to know the schema
// to set the one field their stage needs.
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  GitBranch, Plus, X, Trash2, ArrowUp, ArrowDown, ShieldCheck, ClipboardCheck,
  DatabaseBackup, Terminal, Search, Hourglass, Bell,
} from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSwitch from '@/components/common/VSwitch.vue'
import VSelect from '@/components/common/VSelect.vue'
import api from '@/api'
import { confirmAction } from '@/lib/confirm'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import { useEnvTierStore } from '@/stores/envtier'
import type { Pipeline, PipelineStage, StageType } from '@/types'

const { t } = useI18n()
const ui = useUIStore()
const auth = useAuthStore()
const envtier = useEnvTierStore()
const isAdmin = computed(() => auth.me?.roleCode === 'admin' || (auth.me?.roleCodes || []).includes('admin'))

const pipelines = ref<Pipeline[]>([])
const editing = ref<Pipeline | null>(null)
const dirty = ref(false)

const STAGE_TYPES: StageType[] = ['review', 'approve', 'backup', 'execute', 'verify', 'manual', 'notify']
const stageIcon: Record<string, any> = {
  review: ShieldCheck, approve: ClipboardCheck, backup: DatabaseBackup,
  execute: Terminal, verify: Search, manual: Hourglass, notify: Bell,
}
const typeLabels = computed(() => STAGE_TYPES.map((s) => t('plType_' + s)))
function typeOf(label: string): StageType {
  return STAGE_TYPES.find((s) => t('plType_' + s) === label) || 'review'
}

// The tier picker offers "every tier" plus the tiers that exist; a template
// scoped to a tier cannot be chosen for a release on another one.
const tierOptions = computed(() => [t('plAllTiers'), ...envtier.tierCodes])

async function load() {
  try {
    pipelines.value = await api.pipelines()
    ui.pageSub = t('plSub2', { n: pipelines.value.length })
    if (!editing.value && pipelines.value.length) select(pipelines.value[0])
  } catch (e) { ui.notifyError(e, t('loadFailed')) }
}
onMounted(async () => {
  try { await envtier.load() } catch { /* the tier picker degrades to "every tier" */ }
  await load()
})

function select(p: Pipeline) {
  if (dirty.value && !confirmAction(t('plDiscardConfirm'))) return
  editing.value = JSON.parse(JSON.stringify(p))
  dirty.value = false
}
function newPipeline() {
  editing.value = {
    id: 0, name: '', description: '', tierCode: '', enabled: true, isDefault: false,
    stages: [
      { name: t('plType_review'), type: 'review', config: '{"failOn":"error"}', onFailure: 'abort' },
      { name: t('plType_execute'), type: 'execute', config: '', onFailure: 'abort' },
    ],
  }
  dirty.value = true
}

function addStage(type: StageType) {
  if (!editing.value) return
  editing.value.stages.push({ name: t('plType_' + type), type, config: defaultConfig(type), onFailure: 'abort' })
  dirty.value = true
}
function defaultConfig(type: StageType) {
  if (type === 'review') return '{"failOn":"error"}'
  if (type === 'backup' || type === 'verify') return '{"sql":""}'
  return ''
}
function move(i: number, d: number) {
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
    if (env.code !== 0) { ui.notifyError(new Error(env.msg), t('actionFailed')); return }
    dirty.value = false
    editing.value = env.data
    await load()
    ui.notify(t('plSaved'), 'success')
  } catch (e) { ui.notifyError(e, t('actionFailed')) }
}

async function remove(p: Pipeline) {
  if (!confirmAction(t('plDelConfirm', { name: p.name }))) return
  try {
    const env = await api.deletePipeline(p.id)
    if (env.code !== 0) { ui.notifyError(new Error(env.msg), t('actionFailed')); return }
    editing.value = null
    dirty.value = false
    await load()
  } catch (e) { ui.notifyError(e, t('actionFailed')) }
}
</script>

<template>
  <div class="scy page">
    <div class="head">
      <div>
        <div class="eyebrow">RELEASE FLOWS</div>
        <div class="sub">{{ $t('plSub') }}</div>
      </div>
      <span v-if="!isAdmin" class="roflag">{{ $t('readOnlyPerms') }}</span>
      <VButton v-else variant="primary" @click="newPipeline"><Plus :size="15" />{{ $t('plNew') }}</VButton>
    </div>

    <div class="wrap">
      <!-- template list -->
      <div class="list">
        <div v-for="p in pipelines" :key="p.id" class="pitem" :class="{ on: editing && editing.id === p.id }" @click="select(p)">
          <div class="ptop">
            <span class="pname">{{ p.name }}</span>
            <span v-if="p.isDefault" class="badge">{{ $t('plDefault') }}</span>
            <span v-if="!p.enabled" class="badge off">{{ $t('plDisabled') }}</span>
          </div>
          <div class="pdesc">{{ p.description || '—' }}</div>
          <div class="pmeta">
            <span class="tag">{{ p.tierCode ? p.tierCode.toUpperCase() : $t('plAllTiers') }}</span>
            <span class="dim">{{ $t('plStageCount', { n: p.stages.length }) }}</span>
          </div>
          <div class="mini">
            <span v-for="(s, i) in p.stages" :key="i" class="ms">
              <component :is="stageIcon[s.type] || Terminal" :size="11" />
            </span>
          </div>
        </div>
        <div v-if="!pipelines.length" class="empty">{{ $t('plEmpty') }}</div>
      </div>

      <!-- editor -->
      <div v-if="editing" class="editor">
        <div class="ehead">
          <div class="eic"><GitBranch :size="18" color="var(--accent-text)" /></div>
          <div class="grow">
            <div class="et">{{ editing.id ? $t('plEdit') : $t('plNew') }}</div>
            <div class="es">{{ $t('plEditSub') }}</div>
          </div>
          <VButton v-if="isAdmin && editing.id" @click="remove(editing)"><Trash2 :size="14" />{{ $t('delete') }}</VButton>
          <VButton v-if="isAdmin" variant="primary" @click="save">{{ $t('btnSave') }}</VButton>
        </div>

        <div class="grid2">
          <div><div class="fl">{{ $t('plName') }}</div>
            <input v-model="editing.name" class="in" :disabled="!isAdmin" @input="dirty = true" /></div>
          <div><div class="fl">{{ $t('plTier') }}</div><VSelect v-model="tierLabel" :options="tierOptions" /></div>
        </div>
        <div><div class="fl">{{ $t('plDesc') }}</div>
          <input v-model="editing.description" class="in" :disabled="!isAdmin" @input="dirty = true" /></div>
        <div class="toggles">
          <div class="tg"><VSwitch v-model="editing.enabled" :disabled="!isAdmin" @update:model-value="dirty = true" /><span>{{ $t('plEnabled') }}</span></div>
          <div class="tg"><VSwitch v-model="editing.isDefault" :disabled="!isAdmin" @update:model-value="dirty = true" /><span>{{ $t('plIsDefault') }}</span></div>
        </div>

        <div class="fl">{{ $t('plStages') }}</div>
        <div class="stages">
          <div v-for="(st, i) in editing.stages" :key="i" class="stage">
            <div class="sidx">{{ i + 1 }}</div>
            <div class="sic"><component :is="stageIcon[st.type] || Terminal" :size="15" /></div>
            <div class="sbody">
              <div class="srow">
                <input v-model="st.name" class="in small" :disabled="!isAdmin" @input="dirty = true" />
                <VSelect
                  :model-value="$t('plType_' + st.type)" :options="typeLabels" height="34px"
                  @update:model-value="(v: string) => { st.type = typeOf(v); st.config = defaultConfig(st.type); dirty = true }"
                />
              </div>
              <!-- per-type configuration -->
              <div v-if="st.type === 'review'" class="srow">
                <span class="cl">{{ $t('plFailOn') }}</span>
                <div class="seg">
                  <div v-for="m in ['error', 'warn', 'none']" :key="m" class="si"
                       :class="{ active: cfgGet(st, 'failOn', 'error') === m }"
                       @click="isAdmin && cfgSet(st, 'failOn', m)">{{ $t('plFailOn_' + m) }}</div>
                </div>
              </div>
              <div v-else-if="st.type === 'backup' || st.type === 'verify'" class="srow">
                <span class="cl">SQL</span>
                <input
                  class="in small" :disabled="!isAdmin"
                  :value="cfgGet(st, 'sql')" :placeholder="st.type === 'backup' ? $t('plBackupPh') : $t('plVerifyPh')"
                  @input="cfgSet(st, 'sql', ($event.target as HTMLInputElement).value)"
                />
              </div>
              <div v-else-if="st.type === 'manual'" class="srow">
                <span class="cl">{{ $t('plNote') }}</span>
                <input
                  class="in small" :disabled="!isAdmin" :value="cfgGet(st, 'note')" :placeholder="$t('plNotePh')"
                  @input="cfgSet(st, 'note', ($event.target as HTMLInputElement).value)"
                />
              </div>
              <div class="shint">{{ $t('plHint_' + st.type) }}</div>
            </div>
            <div v-if="isAdmin" class="sacts">
              <span class="sb" @click="move(i, -1)"><ArrowUp :size="13" /></span>
              <span class="sb" @click="move(i, 1)"><ArrowDown :size="13" /></span>
              <span class="sb danger" @click="removeStage(i)"><X :size="13" /></span>
            </div>
          </div>
        </div>

        <div v-if="isAdmin" class="addbar">
          <span class="al">{{ $t('plAddStage') }}</span>
          <span v-for="ty in STAGE_TYPES" :key="ty" class="ab" @click="addStage(ty)">
            <component :is="stageIcon[ty]" :size="12" />{{ $t('plType_' + ty) }}
          </span>
        </div>
      </div>
      <div v-else class="editor empty">{{ $t('plPick') }}</div>
    </div>
  </div>
</template>

<style scoped>
.page { flex: 1; min-height: 0; padding: 24px 28px; }
.head { display: flex; align-items: center; margin-bottom: 18px; }
.head :deep(.vbtn), .head .roflag { margin-left: auto; }
.eyebrow { font: 500 11px var(--font-mono); letter-spacing: 0.12em; color: var(--text-faint); text-transform: uppercase; }
.sub { font: 500 13px var(--font-body); color: var(--text-muted); margin-top: 4px; }
.roflag { display: inline-flex; align-items: center; height: 26px; padding: 0 11px; border-radius: 999px; background: var(--surface-sunken); border: 1px solid var(--border-subtle); font: 600 11px var(--font-mono); color: var(--text-muted); }
.wrap { display: grid; grid-template-columns: minmax(230px, 0.55fr) minmax(0, 2fr); gap: 16px; align-items: start; }
@media (max-width: 1100px) { .wrap { grid-template-columns: 1fr; } }
.list { display: flex; flex-direction: column; gap: 8px; }
.pitem { border: none; border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-xs); padding: 11px 13px; cursor: pointer; }
.pitem.on { border-color: var(--accent); box-shadow: 0 0 0 1px var(--accent-subtle); }
.ptop { display: flex; align-items: center; gap: 7px; }
.pname { font: 600 13.5px var(--font-body); color: var(--text-strong); }
.badge { padding: 1px 7px; border-radius: 999px; background: var(--accent-subtle); color: var(--accent-text); font: 700 9px var(--font-mono); }
.badge.off { background: var(--surface-sunken); color: var(--text-faint); }
.pdesc { font: 500 11.5px var(--font-body); color: var(--text-muted); margin-top: 3px; }
.pmeta { display: flex; align-items: center; gap: 8px; margin-top: 5px; font: 500 10.5px var(--font-mono); color: var(--text-muted); }
.tag { padding: 1px 6px; border-radius: 5px; background: var(--surface-sunken); border: 1px solid var(--border-subtle); font: 600 9.5px var(--font-mono); }
.dim { color: var(--text-faint); }
.mini { display: flex; gap: 4px; margin-top: 7px; color: var(--text-faint); }
.ms { width: 20px; height: 20px; border-radius: 6px; background: var(--surface-sunken); display: inline-flex; align-items: center; justify-content: center; }
.editor { border: none; border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-xs); padding: 16px 18px; min-height: 320px; }
.ehead { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; }
.eic { width: 34px; height: 34px; border-radius: 10px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; }
.grow { flex: 1; min-width: 0; }
.et { font: 600 14.5px var(--font-display); color: var(--text-strong); }
.es { font: 500 11.5px var(--font-body); color: var(--text-muted); margin-top: 1px; }
.grid2 { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
.fl { font: 500 11px var(--font-body); color: var(--text-faint); margin: 10px 0 6px; }
.in { width: 100%; box-sizing: border-box; height: 38px; border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-sunken); padding: 0 12px; font: 400 13px var(--font-body); color: var(--text-body); outline: none; }
.in.small { height: 34px; font-size: 12.5px; }
.in:disabled { opacity: 0.6; }
.toggles { display: flex; gap: 22px; margin: 12px 0 4px; }
.tg { display: flex; align-items: center; gap: 8px; font: 500 12px var(--font-body); color: var(--text-body); }
.stages { display: flex; flex-direction: column; gap: 8px; }
.stage { display: flex; align-items: flex-start; gap: 10px; padding: 10px 12px; border: 1px solid var(--border-subtle); border-radius: 11px; background: var(--surface-sunken); }
.sidx { width: 20px; height: 20px; border-radius: 50%; background: var(--surface-card); border: 1px solid var(--border-default); display: flex; align-items: center; justify-content: center; font: 700 10px var(--font-mono); color: var(--text-muted); margin-top: 7px; }
.sic { width: 30px; height: 30px; border-radius: 9px; background: var(--surface-card); display: flex; align-items: center; justify-content: center; color: var(--accent-text); margin-top: 2px; }
.sbody { flex: 1; min-width: 0; }
.srow { display: flex; align-items: center; gap: 8px; margin-bottom: 6px; }
.srow > .in { flex: 1; }
.srow :deep(.vsel) { min-width: 140px; }
.cl { font: 600 10.5px var(--font-mono); color: var(--text-faint); min-width: 52px; }
.shint { font: 500 10.5px var(--font-body); color: var(--text-faint); }
.sacts { display: flex; flex-direction: column; gap: 4px; }
.sb { width: 24px; height: 24px; display: flex; align-items: center; justify-content: center; border-radius: 7px; border: 1px solid var(--border-subtle); background: var(--surface-card); color: var(--text-muted); cursor: pointer; }
.sb.danger { color: var(--danger-text); }
.seg { display: flex; border: 1px solid var(--border-default); border-radius: 9px; overflow: hidden; }
.si { padding: 0 12px; height: 30px; line-height: 30px; font: 600 11px var(--font-mono); color: var(--text-muted); border-left: 1px solid var(--border-subtle); cursor: pointer; }
.si:first-child { border-left: none; }
.si.active { background: var(--accent-subtle); color: var(--accent-text); }
.addbar { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; margin-top: 12px; }
.al { font: 500 11px var(--font-body); color: var(--text-faint); margin-right: 4px; }
.ab { display: inline-flex; align-items: center; gap: 5px; padding: 5px 10px; border: 1px dashed var(--border-default); border-radius: 8px; font: 600 11px var(--font-mono); color: var(--text-body); cursor: pointer; }
.ab:hover { border-color: var(--accent); color: var(--accent-text); }
.empty { padding: 22px; text-align: center; font: 500 12px var(--font-body); color: var(--text-faint); }
</style>
