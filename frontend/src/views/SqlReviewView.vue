<script setup lang="ts">
// 数据库规范审查规则库 + 手工自查。
//
// The page keeps the library and the checker together on purpose: a rule whose
// effect you cannot try is a rule nobody trusts enough to enable. Editing is
// admin-only (the server enforces it too); running a check is open to anyone who
// can use the terminal, because self-checking your own change before submitting
// it is what makes the standards useful rather than punitive.
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { SpellCheck, Play, Plus, X, Trash2, CircleAlert, TriangleAlert, Info } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSwitch from '@/components/common/VSwitch.vue'
import VSelect from '@/components/common/VSelect.vue'
import api from '@/api'
import { confirmAction } from '@/lib/confirm'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import type { Connection, ReviewCatalog, ReviewResult, ReviewRule } from '@/types'

const { t } = useI18n()
const ui = useUIStore()
const auth = useAuthStore()
const isAdmin = computed(() => auth.me?.roleCode === 'admin' || (auth.me?.roleCodes || []).includes('admin'))

const rules = ref<ReviewRule[]>([])
const catalog = ref<ReviewCatalog>({ dialects: ['all'], categories: [], levels: ['error', 'warn', 'info'] })
const dialect = ref('all')
const category = ref('')
const conns = ref<Connection[]>([])

// ---- library ----
const shown = computed(() =>
  rules.value.filter((r) => {
    const dialectOk = dialect.value === 'all' || r.dialect === 'all' || r.dialect.split(',').includes(dialect.value)
    const catOk = !category.value || r.category === category.value
    return dialectOk && catOk
  }),
)
const stats = computed(() => ({
  total: rules.value.length,
  on: rules.value.filter((r) => r.enabled).length,
  err: rules.value.filter((r) => r.enabled && r.level === 'error').length,
  custom: rules.value.filter((r) => r.kind === 'regex').length,
}))

async function load() {
  try {
    rules.value = await api.reviewRules()
    ui.pageSub = t('srSub2', { n: rules.value.length })
  } catch (e) { ui.notifyError(e, t('loadFailed')) }
  try { catalog.value = await api.reviewCatalog() } catch { /* keep defaults */ }
  try { conns.value = await api.connections() } catch { /* the dialect picker still works */ }
}
onMounted(load)

// A rule's level and enabled state are the two things an operator actually
// decides; both write through immediately so there is no "save" step to forget.
async function setLevel(r: ReviewRule, level: 'error' | 'warn' | 'info') {
  if (!isAdmin.value || r.level === level) return
  await patch(r, { level })
}
async function toggle(r: ReviewRule) {
  if (!isAdmin.value) return
  await patch(r, { enabled: !r.enabled })
}
async function patch(r: ReviewRule, fields: Partial<ReviewRule>) {
  const body = {
    name: r.name, dialect: r.dialect, category: r.category, level: r.level,
    enabled: r.enabled, params: r.params, message: r.message, sortOrder: r.sortOrder, ...fields,
  }
  try {
    const env = await api.saveReviewRule(r.id, body)
    if (env.code !== 0) { ui.notifyError(new Error(env.msg), t('actionFailed')); return }
    Object.assign(r, env.data)
  } catch (e) { ui.notifyError(e, t('actionFailed')) }
}

// params are the rule's knobs (a naming pattern, a length cap). Editing them
// inline keeps the value visible next to the rule it governs.
const editing = ref(0)
const draftParams = ref('')
const draftMsg = ref('')
function openParams(r: ReviewRule) {
  if (!isAdmin.value) return
  editing.value = editing.value === r.id ? 0 : r.id
  draftParams.value = r.params || ''
  draftMsg.value = r.message || ''
}
async function saveParams(r: ReviewRule) {
  await patch(r, { params: draftParams.value.trim(), message: draftMsg.value.trim() })
  editing.value = 0
}

async function removeRule(r: ReviewRule) {
  if (!isAdmin.value) return
  if (!confirmAction(t('srDelConfirm', { name: r.name }))) return
  try {
    const env = await api.deleteReviewRule(r.id)
    if (env.code !== 0) { ui.notifyError(new Error(env.msg), t('actionFailed')); return }
    rules.value = rules.value.filter((x) => x.id !== r.id)
  } catch (e) { ui.notifyError(e, t('actionFailed')) }
}

// ---- custom rule form ----
const formOpen = ref(false)
const nf = ref({ code: '', name: '', dialect: 'all', category: 'dml', level: 'warn', pattern: '', message: '' })
function openForm() {
  nf.value = { code: '', name: '', dialect: 'all', category: catalog.value.categories[0] || 'dml', level: 'warn', pattern: '', message: '' }
  formOpen.value = true
}
async function createRule() {
  const f = nf.value
  if (!f.code.trim() || !f.name.trim() || !f.pattern.trim()) {
    ui.notify(t('srFormIncomplete'), 'error'); return
  }
  try {
    const env = await api.saveReviewRule(0, {
      code: f.code.trim(), name: f.name.trim(), dialect: f.dialect, category: f.category,
      level: f.level as ReviewRule['level'], enabled: true, message: f.message.trim(),
      params: JSON.stringify({ pattern: f.pattern.trim(), mode: 'forbid' }),
    })
    if (env.code !== 0) { ui.notifyError(new Error(env.msg), t('actionFailed')); return }
    rules.value.push(env.data)
    formOpen.value = false
  } catch (e) { ui.notifyError(e, t('actionFailed')) }
}

// ---- manual check ----
const checkSQL = ref('')
const checkConn = ref(0)
const checkDialect = ref('mysql')
const checking = ref(false)
const result = ref<ReviewResult | null>(null)
const connLabels = computed(() => [t('srNoInstance'), ...conns.value.map((c) => `${c.env}-${c.name}`)])
const connLabel = computed({
  get: () => {
    const c = conns.value.find((x) => x.id === checkConn.value)
    return c ? `${c.env}-${c.name}` : t('srNoInstance')
  },
  set: (l: string) => {
    const c = conns.value.find((x) => `${x.env}-${x.name}` === l)
    checkConn.value = c ? c.id : 0
  },
})

async function runCheck() {
  if (!checkSQL.value.trim()) return
  checking.value = true
  try {
    const r = await api.reviewCheck({
      connectionId: checkConn.value || undefined,
      dialect: checkConn.value ? undefined : checkDialect.value,
      sql: checkSQL.value,
    })
    result.value = r.result
  } catch (e) { ui.notifyError(e, t('actionFailed')) } finally { checking.value = false }
}

function levelMeta(l: string) {
  if (l === 'error') return { bg: 'var(--danger-subtle)', c: 'var(--danger-text)', icon: CircleAlert }
  if (l === 'warn') return { bg: 'var(--warning-subtle)', c: 'var(--warning-text)', icon: TriangleAlert }
  return { bg: 'var(--surface-sunken)', c: 'var(--text-muted)', icon: Info }
}
const dialectTabs = computed(() => catalog.value.dialects)
// A rule's dialect scope as a LIST. Stored as "mysql,tidb" (or "all"), which
// rendered raw was an uppercased run-on — "MYSQL,TIDB" reads as one word.
function dialectsOf(r: ReviewRule) {
  if (!r.dialect || r.dialect === 'all') return []
  return r.dialect.split(',').map((d) => d.trim()).filter(Boolean)
}
</script>

<template>
  <div class="scy page">
    <div class="head">
      <div>
        <div class="eyebrow">SQL REVIEW RULES</div>
        <div class="sub">{{ $t('srSub') }}</div>
      </div>
      <span v-if="!isAdmin" class="roflag">{{ $t('readOnlyPerms') }}</span>
      <VButton v-else variant="primary" @click="openForm"><Plus :size="15" />{{ $t('srNewRule') }}</VButton>
    </div>

    <div class="stats">
      <div class="stat"><div class="n">{{ stats.total }}</div><div class="l">{{ $t('srStatTotal') }}</div></div>
      <div class="stat"><div class="n">{{ stats.on }}</div><div class="l">{{ $t('srStatOn') }}</div></div>
      <div class="stat"><div class="n danger">{{ stats.err }}</div><div class="l">{{ $t('srStatErr') }}</div></div>
      <div class="stat"><div class="n">{{ stats.custom }}</div><div class="l">{{ $t('srStatCustom') }}</div></div>
    </div>

    <div class="wrap">
      <!-- rule library -->
      <div class="card lib">
        <div class="chead">
          <div class="cic"><SpellCheck :size="17" color="var(--accent-text)" /></div>
          <div class="grow"><div class="ct">{{ $t('srLibTitle') }}</div><div class="cs">{{ $t('srLibSub') }}</div></div>
          <!-- The dialect tabs belong to the header, like the tier tabs on the
               risk-rules page. As a block of their own they stretched the whole
               card and left a long empty bordered strip past ORACLE. -->
          <div class="tabs scx">
            <div v-for="d in dialectTabs" :key="d" class="tab" :class="{ active: dialect === d }" @click="dialect = d">
              {{ d === 'all' ? $t('srAllDialects') : d.toUpperCase() }}
            </div>
          </div>
        </div>
        <div class="cats">
          <span class="cat" :class="{ on: !category }" @click="category = ''">{{ $t('srAllCats') }}</span>
          <span v-for="c in catalog.categories" :key="c" class="cat" :class="{ on: category === c }" @click="category = c">
            {{ $t('srCat_' + c) }}
          </span>
        </div>

        <div class="rules">
          <div v-for="r in shown" :key="r.id" class="rule" :class="{ off: !r.enabled }">
            <div class="rmain">
              <div class="rtop">
                <span class="rname">{{ r.name }}</span>
                <span class="rcode">{{ r.code }}</span>
                <span v-if="r.kind === 'regex'" class="rkind">{{ $t('srCustom') }}</span>
                <!-- Only SCOPED rules carry a badge. A rule that applies to every
                     dialect saying so on every row was 24 identical chips down the
                     list; the absence is the statement, and the ten dialect-specific
                     rules now stand out instead of drowning. -->
                <span v-if="dialectsOf(r).length" class="rdias">
                  <span v-for="d in dialectsOf(r)" :key="d" class="rdia">{{ d.toUpperCase() }}</span>
                </span>
              </div>
              <div v-if="r.message" class="rmsg">{{ r.message }}</div>
              <div v-else-if="r.params" class="rparams">{{ r.params }}</div>
            </div>
            <div class="lvseg">
              <span v-for="lv in ['error', 'warn', 'info']" :key="lv" class="lv" :class="{ on: r.level === lv, ['lv-' + lv]: true }"
                    :style="{ cursor: isAdmin ? 'pointer' : 'default' }" @click="setLevel(r, lv as any)">{{ $t('srLv_' + lv) }}</span>
            </div>
            <VSwitch :model-value="r.enabled" :disabled="!isAdmin" @update:model-value="toggle(r)" />
            <span v-if="isAdmin" class="rbtn" :title="$t('srEditParams')" @click="openParams(r)">…</span>
            <span v-if="isAdmin && r.kind === 'regex'" class="rbtn danger" :title="$t('delete')" @click="removeRule(r)">
              <Trash2 :size="13" />
            </span>

            <!-- inline params editor -->
            <div v-if="editing === r.id" class="pedit">
              <div class="fl">{{ $t('srParams') }}</div>
              <textarea v-model="draftParams" class="pbox" spellcheck="false" />
              <div class="fl">{{ $t('srMessage') }}</div>
              <input v-model="draftMsg" class="in" :placeholder="$t('srMessagePh')" />
              <div class="pacts">
                <VButton height="30px" @click="editing = 0">{{ $t('btnCancel') }}</VButton>
                <VButton variant="primary" height="30px" @click="saveParams(r)">{{ $t('save') }}</VButton>
              </div>
            </div>
          </div>
          <div v-if="!shown.length" class="empty">{{ $t('srNoRules') }}</div>
        </div>
      </div>

      <!-- manual check -->
      <div class="card check">
        <div class="chead">
          <div class="cic ok"><Play :size="16" color="var(--success-text)" /></div>
          <div class="grow"><div class="ct">{{ $t('srCheckTitle') }}</div><div class="cs">{{ $t('srCheckSub') }}</div></div>
        </div>
        <div class="fl">{{ $t('srTarget') }}</div>
        <VSelect v-model="connLabel" :options="connLabels" searchable />
        <template v-if="!checkConn">
          <div class="fl">{{ $t('srDialect') }}</div>
          <VSelect v-model="checkDialect" :options="catalog.dialects.filter((d) => d !== 'all')" />
        </template>
        <div class="fl">SQL</div>
        <textarea v-model="checkSQL" class="sqlbox" spellcheck="false" :placeholder="$t('srSqlPh')" />
        <div class="acts">
          <VButton variant="primary" height="36px" :disabled="checking" @click="runCheck">
            <Play :size="14" />{{ checking ? $t('srChecking') : $t('srRunCheck') }}
          </VButton>
        </div>

        <div v-if="result" class="res">
          <div class="rsum" :class="result.passed ? 'ok' : 'bad'">
            {{ result.passed ? $t('srPassed') : $t('srBlocked') }} ·
            {{ $t('srCounts', { s: result.statements, e: result.errors, w: result.warnings, i: result.infos }) }}
          </div>
          <div v-for="(f, i) in result.findings" :key="i" class="find"
               :style="{ background: levelMeta(f.level).bg, color: levelMeta(f.level).c }">
            <component :is="levelMeta(f.level).icon" :size="13" />
            <div class="fbody">
              <div class="fname">{{ f.name }} <span class="floc">{{ $t('srStmtAt', { n: f.stmt, line: f.line }) }}</span></div>
              <div class="fmsg">{{ f.message }}</div>
              <div class="fsql">{{ f.sql }}</div>
            </div>
          </div>
          <div v-if="!result.findings.length" class="empty">{{ $t('srNoFindings') }}</div>
        </div>
      </div>
    </div>

    <!-- custom rule modal -->
    <div v-if="formOpen" class="overlay">
      <div class="mask" @click="formOpen = false" />
      <div class="modal">
        <div class="mhead">
          <div class="mic"><Plus :size="18" color="var(--accent-text)" /></div>
          <div><div class="mt">{{ $t('srNewRule') }}</div><div class="ms">{{ $t('srNewRuleSub') }}</div></div>
          <X :size="18" class="mx" @click="formOpen = false" />
        </div>
        <div class="mbody">
          <div class="grid2">
            <div><div class="fl">{{ $t('srCode') }}</div><input v-model="nf.code" class="in" placeholder="no-select-star" /></div>
            <div><div class="fl">{{ $t('srName') }}</div><input v-model="nf.name" class="in" /></div>
            <div><div class="fl">{{ $t('srDialect') }}</div><VSelect v-model="nf.dialect" :options="catalog.dialects" /></div>
            <div><div class="fl">{{ $t('srCategory') }}</div><VSelect v-model="nf.category" :options="catalog.categories" /></div>
          </div>
          <div><div class="fl">{{ $t('srLevel') }}</div>
            <div class="seg">
              <div v-for="lv in ['error', 'warn', 'info']" :key="lv" class="si" :class="{ active: nf.level === lv }" @click="nf.level = lv">
                {{ $t('srLv_' + lv) }}
              </div>
            </div>
          </div>
          <div><div class="fl">{{ $t('srPattern') }}</div><input v-model="nf.pattern" class="in" placeholder="SQL_NO_CACHE" /></div>
          <div><div class="fl">{{ $t('srMessage') }}</div><input v-model="nf.message" class="in" :placeholder="$t('srMessagePh')" /></div>
          <div class="hint">{{ $t('srPatternHint') }}</div>
        </div>
        <div class="mfoot">
          <VButton @click="formOpen = false">{{ $t('btnCancel') }}</VButton>
          <VButton variant="primary" @click="createRule">{{ $t('srCreate') }}</VButton>
        </div>
      </div>
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
.stats { display: flex; gap: 14px; margin-bottom: 18px; }
.stat { flex: 1; border: none; border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-xs); padding: 14px 16px; }
.stat .n { font: 700 24px var(--font-display); color: var(--text-strong); }
.stat .n.danger { color: var(--danger-text); }
.stat .l { font: 500 11.5px var(--font-mono); color: var(--text-muted); margin-top: 2px; }
.wrap { display: grid; grid-template-columns: minmax(0, 1.35fr) minmax(340px, 0.85fr); gap: 16px; align-items: start; }
@media (max-width: 1100px) { .wrap { grid-template-columns: 1fr; } }
.card { border: none; border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-xs); padding: 16px 18px; }
.chead { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; row-gap: 12px; margin-bottom: 14px; }
.cic { width: 32px; height: 32px; border-radius: 9px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; }
.cic.ok { background: var(--success-subtle); }
.grow { flex: 1; min-width: 0; }
.ct { font: 600 14px var(--font-display); color: var(--text-strong); }
.cs { font: 500 12px var(--font-body); color: var(--text-muted); margin-top: 1px; }
.tabs { display: flex; width: fit-content; max-width: 100%; flex-shrink: 0; border: 1px solid var(--border-default); border-radius: var(--radius-md); overflow: hidden; }
.tab { height: 32px; line-height: 32px; padding: 0 14px; cursor: pointer; font: 600 11px var(--font-mono); color: var(--text-muted); border-left: 1px solid var(--border-subtle); white-space: nowrap; }
.tab:first-child { border-left: none; }
.tab.active { background: var(--accent-subtle); color: var(--accent-text); }
.cats { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 12px; }
.cat { padding: 4px 10px; border-radius: 999px; border: 1px solid var(--border-subtle); font: 600 10.5px var(--font-mono); color: var(--text-muted); cursor: pointer; }
.cat.on { background: var(--accent-subtle); color: var(--accent-text); border-color: transparent; }
.rules { display: flex; flex-direction: column; gap: 8px; max-height: 62vh; overflow-y: auto; }
.rule { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; padding: 10px 12px; border: 1px solid var(--border-subtle); border-radius: 10px; background: var(--surface-sunken); }
.rule.off { opacity: 0.55; }
.rmain { flex: 1; min-width: 220px; }
.rtop { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.rname { font: 600 13px var(--font-body); color: var(--text-strong); }
.rcode { font: 500 10.5px var(--font-mono); color: var(--text-faint); }
.rkind { font: 700 9px var(--font-mono); padding: 2px 6px; border-radius: 999px; background: var(--accent-subtle); color: var(--accent-text); }
.rdias { display: inline-flex; align-items: center; gap: 4px; }
.rdia { font: 600 9.5px var(--font-mono); padding: 2px 6px; border-radius: var(--radius-sm); background: var(--surface-card); color: var(--text-muted); border: 1px solid var(--border-subtle); }
.rmsg, .rparams { margin-top: 3px; font: 500 11px var(--font-mono); color: var(--text-muted); word-break: break-all; }
.lvseg { display: flex; gap: 4px; }
.lv { padding: 3px 9px; border: 1px solid var(--border-subtle); border-radius: 7px; font: 600 10px var(--font-mono); color: var(--text-muted); }
.lv.on.lv-error { background: var(--danger-subtle); color: var(--danger-text); border-color: var(--danger-text); }
.lv.on.lv-warn { background: var(--warning-subtle); color: var(--warning-text); border-color: var(--warning-text); }
.lv.on.lv-info { background: var(--surface-card); color: var(--text-body); border-color: var(--border-strong); }
.rbtn { width: 24px; height: 24px; display: inline-flex; align-items: center; justify-content: center; border-radius: 7px; border: 1px solid var(--border-subtle); color: var(--text-muted); cursor: pointer; font: 700 13px var(--font-mono); }
.rbtn.danger { color: var(--danger-text); }
.pedit { flex-basis: 100%; margin-top: 8px; padding-top: 10px; border-top: 1px dashed var(--border-default); }
.pbox { width: 100%; box-sizing: border-box; min-height: 62px; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-card); padding: 8px 10px; font: 500 11.5px var(--font-mono); color: var(--text-body); outline: none; resize: vertical; }
.pacts { display: flex; justify-content: flex-end; gap: 8px; margin-top: 8px; }
.fl { font: 500 11px var(--font-body); color: var(--text-faint); margin: 10px 0 6px; }
.in { width: 100%; box-sizing: border-box; height: 38px; border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-sunken); padding: 0 12px; font: 400 13px var(--font-body); color: var(--text-body); outline: none; }
.sqlbox { width: 100%; box-sizing: border-box; min-height: 150px; border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-sunken); padding: 10px 12px; font: 500 12px var(--font-mono); color: var(--text-body); outline: none; resize: vertical; }
.acts { display: flex; justify-content: flex-end; margin-top: 10px; }
.res { margin-top: 12px; display: flex; flex-direction: column; gap: 7px; max-height: 46vh; overflow-y: auto; }
.rsum { padding: 8px 11px; border-radius: 9px; font: 600 12px var(--font-mono); }
.rsum.ok { background: var(--success-subtle); color: var(--success-text); }
.rsum.bad { background: var(--danger-subtle); color: var(--danger-text); }
.find { display: flex; gap: 8px; padding: 8px 11px; border-radius: 9px; }
.fbody { min-width: 0; }
.fname { font: 600 12px var(--font-body); }
.floc { font: 500 10px var(--font-mono); opacity: 0.75; margin-left: 6px; }
.fmsg { font: 500 11.5px var(--font-body); margin-top: 2px; }
.fsql { font: 500 10.5px var(--font-mono); opacity: 0.8; margin-top: 3px; word-break: break-all; }
.empty { padding: 18px; text-align: center; font: 500 12px var(--font-body); color: var(--text-faint); }
.overlay { position: fixed; inset: 0; z-index: 50; }
.mask { position: absolute; inset: 0; background: rgba(4, 6, 12, 0.7); backdrop-filter: blur(3px); }
.modal { position: absolute; left: 50%; top: 50%; transform: translate(-50%, -50%); width: 620px; max-width: 92vw; max-height: 88vh; overflow-y: auto; background: var(--surface-card); border: 1px solid var(--border-default); border-radius: 18px; box-shadow: var(--shadow-xl); }
.mhead { display: flex; align-items: center; gap: 12px; padding: 16px 22px; border-bottom: 1px solid var(--border-subtle); }
.mic { width: 34px; height: 34px; border-radius: 10px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; }
.mt { font: 700 15px var(--font-display); color: var(--text-strong); }
.ms { font: 500 11.5px var(--font-body); color: var(--text-muted); }
.mx { color: var(--text-muted); margin-left: auto; cursor: pointer; }
.mbody { padding: 6px 22px 18px; }
.grid2 { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
.seg { display: flex; border: 1px solid var(--border-default); border-radius: 10px; overflow: hidden; }
.si { flex: 1; text-align: center; height: 36px; line-height: 36px; font: 600 12px var(--font-mono); color: var(--text-muted); border-left: 1px solid var(--border-subtle); cursor: pointer; }
.si:first-child { border-left: none; }
.si.active { background: var(--accent-subtle); color: var(--accent-text); }
.hint { margin-top: 10px; font: 500 11px var(--font-body); color: var(--text-faint); }
.mfoot { display: flex; justify-content: flex-end; gap: 10px; padding: 12px 22px; border-top: 1px solid var(--border-subtle); background: var(--surface-raised); }
</style>
