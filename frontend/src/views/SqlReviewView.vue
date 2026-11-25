<script setup lang="ts">
// 数据库规范审查规则库 + 手工自查。
//
// The page keeps the library and the checker together on purpose: a rule whose
// effect you cannot try is a rule nobody trusts enough to enable. Editing is
// admin-only (the server enforces it too); running a check is open to anyone who
// can use the terminal, because self-checking your own change before submitting
// it is what makes the standards useful rather than punitive.
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { reviewRuleName } from '@/lib/builtinNames'
import { reviewGroups } from '@/lib/reviewGroups'
import {
  paramSpecsFor, effectiveDefault, formatParamValue, paramsToDraft, draftToParams, effectiveParams, specForDialect,
} from '@/lib/reviewParams'
import {
  SpellCheck, Play, Plus, X, Trash2, CircleAlert, TriangleAlert, Info,
  Search, Settings2, ShieldAlert, ShieldCheck, Wand2, CircleCheck, FlaskConical,
} from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSwitch from '@/components/common/VSwitch.vue'
import VSelect from '@/components/common/VSelect.vue'
import api from '@/api'
import { envError, localizeText } from '@/api/http'
import { confirmAction } from '@/lib/confirm'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import type { Connection, ReviewCatalog, ReviewParamSpec, ReviewResult, ReviewRule } from '@/types'

const { t, te } = useI18n()
// 内置规则名按读者语言显示;改过名的原样显示(lib/builtinNames reviewRuleName)。
const zhText = (k: string) => (te(k, 'zh') ? t(k, {}, { locale: 'zh' }) : undefined)
const ruleName = (code: string, stored: string) => reviewRuleName(code, stored, t, zhText)
const ui = useUIStore()
const auth = useAuthStore()
// 规则库的维护在管理后台(管理后台拆分,工单 05):同一页,在用户后台只读 + 自查,在管理后台
// (/admin/sql-review)才出现编辑控件。管理员在用户后台也是只读,给一个去管理后台的入口。
const route = useRoute()
const router = useRouter()
const adminUser = computed(() => auth.me?.roleCode === 'admin' || (auth.me?.roleCodes || []).includes('admin'))
const isAdmin = computed(() => adminUser.value && route.meta.portal === 'admin')

const rules = ref<ReviewRule[]>([])
const catalog = ref<ReviewCatalog>({ dialects: ['all'], categories: [], levels: ['error', 'warn', 'info'] })
const dialect = ref('all')
const category = ref('')
// 按规范分级筛选 —— 上线前最常问的一句是“这次改动碰了哪几条【高危】”,
// 而不是“碰了哪几条 dml 规则”。'' = 全部,'none' = 规范未覆盖的平台内置规则。
const spec = ref('')
const conns = ref<Connection[]>([])

// ---- library ----
// 规则名 / code 搜索。这里在客户端筛是**对的**:规则库是一次性全量拉下来的
// (api.reviewRules 不分页),搜索看到的就是全部 —— 和审批页那个必须走服务端的
// 搜索框不是一回事,那边列表分页,只筛当前页会对第三页的工单谎称"没有"。
const q = ref('')
const inDialect = (r: ReviewRule) => dialect.value === 'all' || r.dialect === 'all' || r.dialect.split(',').includes(dialect.value)
// 出处与分级按所选的库显示:MySQL 页上不拿 DWS 的【強制】去说一条 MySQL 规范没写的规则。
const srcOf = (r: ReviewRule) => specForDialect(r, dialect.value)
const shown = computed(() => {
  const kw = q.value.trim().toLowerCase()
  return rules.value.filter((r) => {
    const dialectOk = inDialect(r)
    const catOk = !category.value || r.category === category.value
    const sp = srcOf(r).spec
    const specOk = !spec.value || (spec.value === 'none' ? !sp : sp === spec.value)
    const kwOk = !kw || r.name.toLowerCase().includes(kw) || ruleName(r.code, r.name).toLowerCase().includes(kw) || r.code.toLowerCase().includes(kw)
    return dialectOk && catOk && specOk && kwOk
  })
})
const filtered = computed(() => !!(q.value.trim() || category.value || spec.value || dialect.value !== 'all'))
function clearFilters() { q.value = ''; category.value = ''; spec.value = ''; dialect.value = 'all' }
// 拦截/警告只数**启用中**的:一条关掉的规则拦不下任何东西,把它算进"拦截规则"
// 这张牌,牌面上的数字就不是此刻真正生效的那个数。
const stats = computed(() => ({
  total: rules.value.length,
  on: rules.value.filter((r) => r.enabled).length,
  err: rules.value.filter((r) => r.enabled && r.level === 'error').length,
  warn: rules.value.filter((r) => r.enabled && r.level === 'warn').length,
  custom: rules.value.filter((r) => r.kind === 'regex').length,
  spec: rules.value.filter((r) => !!r.spec).length,
}))

// 分级筛选页签。计数放在标签上 —— 一条规范分级下面有几条规则是可以直接看到的事实,
// 不该要人先点进去才知道。
const specTabs = computed(() => {
  const order = catalog.value.specs || ['critical', 'mandatory', 'recommended']
  // 计数跟着所选的库走,和下面列表里的分级徽章说的是同一件事。
  const pool = rules.value.filter(inDialect).map((r) => srcOf(r).spec)
  const tabs = order.map((s) => ({ key: s, n: pool.filter((x) => x === s).length }))
  tabs.push({ key: 'none', n: pool.filter((x) => !x).length })
  return tabs.filter((t) => t.n > 0)
})

function specMeta(s: string) {
  switch (s) {
    case 'critical': return { bg: 'var(--danger-subtle)', c: 'var(--danger-text)' }
    case 'mandatory': return { bg: 'var(--warn-subtle)', c: 'var(--warn-text)' }
    case 'recommended': return { bg: 'var(--accent-subtle)', c: 'var(--accent-text)' }
    default: return { bg: 'var(--surface-sunken)', c: 'var(--text-faint)' }
  }
}

async function load() {
  try {
    rules.value = await api.reviewRules()
    ui.pageSub = { key: 'srSub2', params: { n: rules.value.length } }
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
    if (env.code !== 0) { ui.notifyError(envError(env), t('actionFailed')); return }
    Object.assign(r, env.data)
  } catch (e) { ui.notifyError(e, t('actionFailed')) }
}

// params are the rule's knobs (a naming pattern, a length cap). Editing them
// inline keeps the value visible next to the rule it governs.
const editing = ref(0)
const draftParams = ref<Record<string, string>>({})
const draftMsg = ref('')
// 出处可改:规范会改版、章节会挪,运维自己写的规则也该能标出自哪一条。
const draftSpec = ref<ReviewRule['spec']>('')
const draftRef = ref('')
function openParams(r: ReviewRule) {
  if (!isAdmin.value) return
  editing.value = editing.value === r.id ? 0 : r.id
  draftParams.value = paramsToDraft(r.params || '', specsOf(r))
  draftMsg.value = r.message || ''
  draftSpec.value = r.spec || ''
  draftRef.value = r.specRef || ''
}
async function saveParams(r: ReviewRule) {
  await patch(r, {
    params: draftToParams(draftParams.value, specsOf(r), r.params || ''), message: draftMsg.value.trim(),
    spec: draftSpec.value, specRef: draftRef.value.trim(),
  })
  editing.value = 0
}

// ---- 参数表单 ----
// 说明书来自 /sql-review/catalog 的 params(后端 review/params_spec.go)。不在其中的内置规则
// 没有可调参数 —— 编辑区直接这么说,而不是摆一个空文本框让人以为漏配了。
const specsOf = (r: ReviewRule) => paramSpecsFor(catalog.value, r)
const prmKey = (r: ReviewRule, key: string) => (r.kind === 'regex' ? '_regex' : r.code.replace(/\./g, '_')) + '__' + key
const prmLabel = (r: ReviewRule, key: string) => (te('srPrm.' + prmKey(r, key)) ? t('srPrm.' + prmKey(r, key)) : key)
const prmHelp = (r: ReviewRule, key: string) => (te('srPrmH.' + prmKey(r, key)) ? t('srPrmH.' + prmKey(r, key)) : '')
const prmOpt = (o: string) => (te('srPrmOpt_' + o) ? t('srPrmOpt_' + o) : o)
const DIALECT_LABEL: Record<string, string> = { mysql: 'MySQL', tidb: 'TiDB', oracle: 'Oracle', dws: 'DWS', generic: 'Generic' }
// 默认值写成人读的样子:列表用逗号连,开关用是/否,枚举用选项名。
function showValue(ps: ReviewParamSpec, v: unknown): string {
  if (ps.type === 'list') return Array.isArray(v) ? v.join(', ') : String(v ?? '')
  if (ps.type === 'bool') return v === true ? t('srParamYes') : t('srParamNo')
  if (ps.type === 'enum') return prmOpt(String(v ?? ''))
  return String(v ?? '')
}
// 编辑框的占位符:留空时真正生效的值(已选库就按该库)。
const placeholderOf = (ps: ReviewParamSpec) => formatParamValue(ps, effectiveDefault(ps, dialect.value))
const listRows = (ps: ReviewParamSpec) => Math.min(6, Math.max(2, placeholderOf(ps).split('\n').length))
// 某些库上默认值不同 —— 在说明里逐个点出来,免得人以为 Oracle 也按 64 判。
function dialectNotes(ps: ReviewParamSpec): string[] {
  return Object.entries(ps.dialectDefaults || {}).map(
    ([d, v]) => t('srParamDefaultFor', { d: DIALECT_LABEL[d] || d }) + ': ' + showValue(ps, v),
  )
}
// 列表行里的一行摘要:每个参数的生效值,没填的就是默认值。
function paramSummary(r: ReviewRule): string {
  return effectiveParams(r.params || '', specsOf(r), dialect.value)
    .map((e) => {
      let v = showValue(e.spec, e.value)
      if (v.length > 48) v = v.slice(0, 46) + '…'
      return prmLabel(r, e.spec.key) + ' ' + v
    })
    .join(' · ')
}
function resetParams(r: ReviewRule) {
  for (const ps of specsOf(r)) draftParams.value[ps.key] = ''
}

async function removeRule(r: ReviewRule) {
  if (!isAdmin.value) return
  if (!(await confirmAction(t('srDelConfirm', { name: ruleName(r.code, r.name) })))) return
  try {
    const env = await api.deleteReviewRule(r.id)
    if (env.code !== 0) { ui.notifyError(envError(env), t('actionFailed')); return }
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
    if (env.code !== 0) { ui.notifyError(envError(env), t('actionFailed')); return }
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
const groups = computed(() => reviewGroups(result.value))
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
    <div class="v-head">
      <div class="v-head-l">
        <div class="v-eyebrow">SQL REVIEW RULES</div>
        <div class="v-sub">{{ $t('srSub') }}</div>
      </div>
      <VButton v-if="isAdmin" variant="primary" @click="openForm"><Plus :size="15" />{{ $t('srNewRule') }}</VButton>
      <VButton v-else-if="adminUser" variant="secondary" data-testid="sr-go-admin" @click="router.push('/admin/sql-review')">{{ $t('srManageInAdmin') }}</VButton>
      <span v-else class="roflag">{{ $t('readOnlyPerms') }}</span>
    </div>

    <!-- 概览指标。数字上面是它在问什么,下面是答案 —— 四个孤立的数字并排时,
         读的人得先猜每个数字量的是什么。 -->
    <div class="stats">
      <div class="stat">
        <div class="sl">{{ $t('srStatOn') }}<span class="sicon accent"><ShieldCheck :size="15" /></span></div>
        <div class="sn">{{ stats.on }}</div>
        <div class="sf">{{ $t('srStatOfTotal', { n: stats.total }) }}</div>
      </div>
      <div class="stat">
        <div class="sl">{{ $t('srStatErr') }}<span class="sicon danger"><ShieldAlert :size="15" /></span></div>
        <div class="sn danger">{{ stats.err }}</div>
        <div class="sf">{{ $t('srStatErrHint') }}</div>
      </div>
      <div class="stat">
        <div class="sl">{{ $t('srStatWarn') }}<span class="sicon warn"><TriangleAlert :size="15" /></span></div>
        <div class="sn warn">{{ stats.warn }}</div>
        <div class="sf">{{ $t('srStatWarnHint') }}</div>
      </div>
      <div class="stat">
        <div class="sl">{{ $t('srStatCustom') }}<span class="sicon muted"><Wand2 :size="15" /></span></div>
        <div class="sn muted">{{ stats.custom }}</div>
        <div class="sf">{{ $t('srStatCustomHint') }}</div>
      </div>
    </div>

    <div class="wrap">
      <!-- ---------------------------------------------- 左:规则库 -->
      <div class="card lib">
        <div class="chead">
          <div class="cic"><SpellCheck :size="17" color="var(--accent-text)" /></div>
          <div class="grow"><div class="ct">{{ $t('srLibTitle') }}</div><div class="cs">{{ $t('srLibSub') }}</div></div>
        </div>

        <!-- 第一行:引擎分段器 + 搜索。引擎是"这条规则管哪种库",它和下面那排
             标签不是一组开关,所以分开一行、用完全不同的控件形态。 -->
        <div class="frow">
          <div class="tabs scx">
            <div v-for="d in dialectTabs" :key="d" class="tab" :class="{ active: dialect === d }" @click="dialect = d">
              {{ d === 'all' ? $t('srAllDialects') : d.toUpperCase() }}
            </div>
          </div>
          <div class="searchbox">
            <Search :size="14" color="var(--text-faint)" />
            <input v-model="q" :placeholder="$t('srSearchPh')" spellcheck="false">
            <button v-if="q" class="sclear" :title="$t('apSearchClear')" @click="q = ''"><X :size="13" /></button>
          </div>
        </div>

        <!-- 第二行:标签过滤条。规范分级与规则分类合成一行(原先是两行,叠在一起
             很乱),但中间留一道竖线 —— 它们回答的仍是两个问题:"规范怎么定性"和
             "查的是哪一类写法"。不加分隔就等于说它们是同一组开关。 -->
        <div class="tagbar">
          <span class="tg" :class="{ on: !spec && !category }" @click="spec = ''; category = ''">{{ $t('srAllRules') }}</span>
          <span class="tsep" />
          <span
            v-for="sp in specTabs" :key="sp.key" class="tg" :class="{ on: spec === sp.key }"
            :style="spec === sp.key ? { background: specMeta(sp.key).bg, color: specMeta(sp.key).c, borderColor: 'transparent' } : {}"
            @click="spec = spec === sp.key ? '' : sp.key"
          >{{ $t('srSpec_' + sp.key) }}<b>{{ sp.n }}</b></span>
          <span class="tsep" />
          <span
            v-for="c in catalog.categories" :key="c" class="tg" :class="{ on: category === c }"
            @click="category = category === c ? '' : c"
          >{{ $t('srCat_' + c) }}</span>
        </div>

        <div class="rules">
          <div v-for="r in shown" :key="r.id" class="rule" :class="{ off: !r.enabled }">
            <div class="rmain">
              <div class="rtop">
                <span class="rname">{{ ruleName(r.code, r.name) }}</span>
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
              <div class="rmsg">{{ r.message || paramSummary(r) || '—' }}</div>
              <!-- 出处:被这条规则拦下来的人要能回去读原文。没有出处的写"平台内置",
                   免得有人拿平台的默认值当规范原文去引用。 -->
              <div class="rsrc">
                <span class="sbadge" :style="{ background: specMeta(srcOf(r).spec).bg, color: specMeta(srcOf(r).spec).c }">
                  {{ $t('srSpec_' + (srcOf(r).spec || 'none')) }}
                </span>
                <span v-if="srcOf(r).specRef" class="sref">{{ srcOf(r).specRef }}</span>
              </div>
            </div>

            <!-- 级别仍然是**可点的**,不是一个只读徽章:改级别是这一页的核心操作
                 之一,没有别的入口。当前级别用语义色实心标出,其余两档留作淡底 ——
                 既看得出现在是哪一档,也还点得动。 -->
            <div class="lvseg">
              <span
                v-for="lv in ['error', 'warn', 'info']" :key="lv"
                class="lv" :class="{ on: r.level === lv, ['lv-' + lv]: true, ro: !isAdmin }"
                @click="setLevel(r, lv as any)"
              >{{ $t('srLv_' + lv) }}</span>
            </div>
            <VSwitch :model-value="r.enabled" :disabled="!isAdmin" @update:model-value="toggle(r)" />
            <span v-if="isAdmin" class="rbtn" :title="$t('srEditParams')" @click="openParams(r)"><Settings2 :size="14" /></span>
            <span v-if="isAdmin && r.kind === 'regex'" class="rbtn danger" :title="$t('delete')" @click="removeRule(r)">
              <Trash2 :size="13" />
            </span>

            <!-- inline params editor -->
            <div v-if="editing === r.id" class="pedit">
              <div class="fl">{{ $t('srParamsTitle') }}</div>
              <div v-if="!specsOf(r).length" class="pnone">{{ $t('srParamsNone') }}</div>
              <template v-else>
                <div class="phint">
                  {{ $t('srParamsHint') }}
                  <button class="preset" @click="resetParams(r)">{{ $t('srParamReset') }}</button>
                </div>
                <div v-for="ps in specsOf(r)" :key="ps.key" class="pfield" :data-key="ps.key">
                  <div class="plabel">{{ prmLabel(r, ps.key) }} <span class="pkey">{{ ps.key }}</span></div>
                  <textarea
                    v-if="ps.type === 'list'" v-model="draftParams[ps.key]" class="pbox" spellcheck="false"
                    :placeholder="placeholderOf(ps)" :rows="listRows(ps)"
                  />
                  <select v-else-if="ps.type === 'bool' || ps.type === 'enum'" v-model="draftParams[ps.key]" class="in">
                    <option value="">{{ $t('srParamDefault') }}: {{ showValue(ps, effectiveDefault(ps, dialect)) }}</option>
                    <template v-if="ps.type === 'bool'">
                      <option value="true">{{ $t('srParamYes') }}</option>
                      <option value="false">{{ $t('srParamNo') }}</option>
                    </template>
                    <template v-else>
                      <option v-for="o in ps.options || []" :key="o" :value="o">{{ prmOpt(o) }}</option>
                    </template>
                  </select>
                  <input
                    v-else v-model="draftParams[ps.key]" class="in" spellcheck="false"
                    :inputmode="ps.type === 'int' ? 'numeric' : undefined" :placeholder="placeholderOf(ps)"
                  >
                  <div class="phelp">
                    <span v-if="prmHelp(r, ps.key)">{{ prmHelp(r, ps.key) }}</span>
                    <span v-if="ps.type === 'list'">{{ $t('srParamList') }}</span>
                    <span v-if="ps.type === 'regex'">{{ $t('srParamRegex') }}</span>
                    <span v-if="ps.type !== 'list' && ps.type !== 'bool' && ps.type !== 'enum'">{{ $t('srParamDefault') }}: {{ showValue(ps, effectiveDefault(ps, dialect)) || '—' }}</span>
                    <span v-for="n in dialectNotes(ps)" :key="n">{{ n }}</span>
                  </div>
                </div>
              </template>
              <div class="fl">{{ $t('srMessage') }}</div>
              <input v-model="draftMsg" class="in" :placeholder="$t('srMessagePh')" />
              <div class="fl">{{ $t('srSpecLevel') }}</div>
              <select v-model="draftSpec" class="in">
                <option value="">{{ $t('srSpec_none') }}</option>
                <option v-for="sp in (catalog.specs || ['critical', 'mandatory', 'recommended'])" :key="sp" :value="sp">
                  {{ $t('srSpec_' + sp) }}
                </option>
              </select>
              <div class="fl">{{ $t('srSpecRef') }}</div>
              <input v-model="draftRef" class="in" :placeholder="$t('srSpecRefPh')" />
              <div class="pacts">
                <VButton height="30px" @click="editing = 0">{{ $t('btnCancel') }}</VButton>
                <VButton variant="primary" height="30px" @click="saveParams(r)">{{ $t('save') }}</VButton>
              </div>
            </div>
          </div>
          <div v-if="!shown.length" class="empty">
            <div class="et">{{ filtered ? $t('srNoRulesFiltered') : $t('srNoRules') }}</div>
            <button v-if="filtered" class="eclear" @click="clearFilters">{{ $t('apClearFilters') }}</button>
          </div>
        </div>
      </div>

      <!-- ---------------------------------------------- 右:上线前自查 -->
      <div class="card check">
        <div class="chead">
          <div class="cic ok"><FlaskConical :size="16" color="var(--success-text)" /></div>
          <div class="grow"><div class="ct">{{ $t('srCheckTitle') }}</div><div class="cs">{{ $t('srCheckSub') }}</div></div>
          <span class="toolflag">{{ $t('srSandbox') }}</span>
        </div>
        <div class="field">
          <div class="fl">{{ $t('srTarget') }}</div>
          <VSelect v-model="connLabel" :options="connLabels" searchable height="38px" />
        </div>
        <div v-if="!checkConn" class="field">
          <div class="fl">{{ $t('srDialect') }}</div>
          <VSelect v-model="checkDialect" :options="catalog.dialects.filter((d) => d !== 'all')" height="38px" />
        </div>
        <div class="field">
          <div class="fl">SQL</div>
          <textarea v-model="checkSQL" class="sqlbox" spellcheck="false" :placeholder="$t('srSqlPh')" />
        </div>
        <VButton class="runbtn" variant="primary" height="38px" :disabled="checking" @click="runCheck">
          <Play :size="14" />{{ checking ? $t('srChecking') : $t('srRunCheck') }}
        </VButton>

        <!-- 结果面板。没跑过的时候留一块占位,而不是让按钮下面空着 —— 那块空白
             会让人以为点了没反应。 -->
        <div v-if="result" class="res">
          <div class="rsum" :class="result.passed ? 'ok' : 'bad'">
            <component :is="result.passed ? CircleCheck : CircleAlert" :size="15" />
            <span class="rst">{{ result.passed ? $t('srPassed') : $t('srBlocked') }}</span>
            <span class="rcnt">{{ $t('srCounts', { s: result.statements, e: result.errors, w: result.warnings, i: result.infos }) }}</span>
          </div>
          <!-- 按规则分组:一条规则一行 + 命中总数 + 前 5 条示例(大脚本不再逐条画几十万行) -->
          <div v-for="g in groups" :key="g.code + g.level" class="find" :class="g.level">
            <component :is="levelMeta(g.level).icon" :size="14" class="fi" />
            <div class="fbody">
              <div class="fname">{{ ruleName(g.code, g.name) }} <span class="fcount">{{ $t('srRuleHits', { n: g.count }) }}</span></div>
              <div class="fmsg">{{ localizeText(g.message, g.messageRef) }}</div>
              <div v-for="(f, i) in g.examples" :key="i" class="fex">
                <span class="floc">{{ $t('srStmtAt', { n: f.stmt, line: f.line }) }}</span>
                <div v-if="f.message !== g.message" class="fmsg">{{ localizeText(f.message, f.messageRef) }}</div>
                <div class="fsql">{{ f.sql }}</div>
              </div>
              <div v-if="g.more" class="fmore">{{ $t('srMoreHits', { n: g.more }) }}</div>
            </div>
          </div>
          <div v-if="!groups.length" class="nofind">{{ $t('srNoFindings') }}</div>
        </div>
        <div v-else class="resempty">{{ $t('srResultIdle') }}</div>
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
.page { flex: 1; min-height: 0; padding: var(--page-pad); max-width: var(--page-max); margin-inline: auto; width: 100%; }
.head :deep(.vbtn), .head .roflag { margin-left: auto; }
.roflag { display: inline-flex; align-items: center; height: 26px; padding: 0 11px; border-radius: 999px; background: var(--surface-sunken); border: 1px solid var(--border-subtle); font: 600 11px var(--font-mono); color: var(--text-muted); }

/* ---------------- 概览指标 ---------------- */
/* 四张牌等宽,窄屏自己换行。语义色只上在数字和右上角的小图标上 —— 整张牌都染色
   会让四张牌互相抢,而它们本来是并列的。 */
.stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(190px, 1fr)); gap: 16px; margin-bottom: 20px; }
.stat { border: 1px solid var(--border-subtle); border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-sm); padding: 15px 17px; }
.sl { display: flex; align-items: center; gap: 8px; font: 500 12px var(--font-body); color: var(--text-muted); }
.sicon { margin-left: auto; width: 28px; height: 28px; border-radius: 9px; display: grid; place-items: center; }
.sicon.accent { background: var(--accent-subtle); color: var(--accent-text); }
.sicon.danger { background: var(--danger-subtle); color: var(--danger-text); }
.sicon.warn { background: var(--warning-subtle); color: var(--warning-text); }
.sicon.muted { background: var(--surface-sunken); color: var(--text-faint); }
.sn { margin-top: 8px; font: 700 26px var(--font-display); color: var(--text-strong); }
.sn.danger { color: var(--danger-text); }
.sn.warn { color: var(--warning-text); }
.sn.muted { color: var(--text-muted); }
.sf { margin-top: 2px; font: 500 11.5px var(--font-body); color: var(--text-faint); }

/* ---------------- 两栏工作台 ---------------- */
.wrap { display: grid; grid-template-columns: minmax(0, 1.9fr) minmax(380px, 1fr); gap: 24px; align-items: start; }
@media (max-width: 1080px) { .wrap { grid-template-columns: 1fr; } }
.card { border: 1px solid var(--border-subtle); border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-sm); padding: 16px 18px 18px; }
.chead { display: flex; align-items: center; gap: 11px; margin-bottom: 14px; }
.cic { width: 32px; height: 32px; border-radius: 9px; background: var(--accent-subtle); display: grid; place-items: center; flex-shrink: 0; }
.cic.ok { background: var(--success-subtle); }
.grow { flex: 1; min-width: 0; }
.ct { font: 600 14px var(--font-display); color: var(--text-strong); }
.cs { font: 500 12px var(--font-body); color: var(--text-muted); margin-top: 1px; }
.toolflag { flex-shrink: 0; display: inline-flex; align-items: center; height: 22px; padding: 0 9px; border-radius: 999px; background: var(--success-subtle); color: var(--success-text); font: 600 10.5px var(--font-mono); }

/* ---------------- 筛选:两行,形态刻意不同 ---------------- */
.frow { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; margin-bottom: 10px; }
.tabs { display: flex; width: fit-content; max-width: 100%; flex-shrink: 0; border: 1px solid var(--border-default); border-radius: var(--radius-md); overflow: hidden; }
/* 用 flex 居中,不用 line-height —— `font:` 简写会把 line-height 一并重置成
   normal,后者会把前者吹掉,文字于是贴着盒子上边。 */
.tab { display: flex; align-items: center; height: 32px; padding: 0 14px; cursor: pointer; font: 600 11px var(--font-mono); color: var(--text-muted); border-left: 1px solid var(--border-subtle); white-space: nowrap; }
.tab:first-child { border-left: none; }
.tab.active { background: var(--accent-subtle); color: var(--accent-text); }
.searchbox { margin-left: auto; display: flex; align-items: center; gap: 7px; height: 32px; padding: 0 10px; border: 1px solid var(--border-default); border-radius: 9px; background: var(--surface-sunken); }
.searchbox input { width: 180px; border: none; outline: none; background: transparent; color: var(--text-strong); font: 500 12px var(--font-body); }
.sclear { display: grid; place-items: center; width: 18px; height: 18px; border: none; border-radius: 5px; background: transparent; color: var(--text-faint); cursor: pointer; }
.sclear:hover { color: var(--text-body); }

.tagbar { display: flex; align-items: center; gap: 7px; flex-wrap: wrap; padding-bottom: 14px; margin-bottom: 2px; border-bottom: 1px solid var(--border-subtle); }
.tg { display: inline-flex; align-items: center; gap: 5px; height: 26px; padding: 0 11px; border: 1px solid transparent; border-radius: 999px; background: var(--surface-sunken); color: var(--text-muted); font: 600 11px var(--font-body); cursor: pointer; white-space: nowrap; transition: color .12s, background .12s; }
.tg:hover { color: var(--accent-text); }
.tg.on { background: var(--accent-subtle); color: var(--accent-text); }
.tg b { font: 700 10px var(--font-mono); opacity: .7; }
/* 一道竖线,不是装饰:这一排里其实是两组问题(规范怎么定性 / 查的是哪一类写法),
   合成一行是为了不再堆叠,但不加分隔就等于说它们是同一组开关。 */
.tsep { width: 1px; height: 16px; background: var(--border-default); margin: 0 3px; }

/* ---------------- 规则条目 ---------------- */
.rules { display: flex; flex-direction: column; max-height: 66vh; overflow-y: auto; }
.rule { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; padding: 13px 10px; border-bottom: 1px solid var(--border-subtle); border-radius: 8px; transition: background var(--dur-fast, .15s) var(--ease-out, ease); }
.rule:last-child { border-bottom: none; }
.rule:hover { background: var(--surface-page); }
.rule.off { opacity: 0.55; }
.rmain { flex: 1; min-width: 0; }
.rtop { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.rname { font: 600 13px var(--font-body); color: var(--text-strong); }
.rcode { font: 500 11px var(--font-mono); color: var(--text-faint); }
.rkind { padding: 1px 7px; border-radius: 5px; background: var(--accent-subtle); color: var(--accent-text); font: 600 10px var(--font-mono); }
.rdias { display: inline-flex; gap: 4px; }
.rdia { padding: 1px 6px; border-radius: 4px; background: var(--surface-sunken); border: 1px solid var(--border-subtle); color: var(--text-muted); font: 600 9.5px var(--font-mono); }
/* 说明单行截断:一条规则的解释可以很长,而列表要能一眼扫过去。完整内容在
   hover 的 title 上,以及"…"里的编辑框里。 */
.rmsg { margin-top: 4px; font: 500 11.5px var(--font-body); color: var(--text-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.rsrc { margin-top: 5px; display: flex; align-items: center; gap: 7px; flex-wrap: wrap; }
.sbadge { padding: 1px 7px; border-radius: 5px; font: 600 10px var(--font-body); }
.sref { font: 500 10.5px var(--font-body); color: var(--text-faint); }

.lvseg { flex-shrink: 0; display: flex; gap: 4px; }
.lv { display: inline-flex; align-items: center; height: 24px; padding: 0 10px; border: 1px solid var(--border-subtle); border-radius: 999px; background: var(--surface-card); font: 600 10.5px var(--font-mono); color: var(--text-faint); cursor: pointer; white-space: nowrap; }
.lv.ro { cursor: default; }
.lv.on.lv-error { background: var(--danger-subtle); color: var(--danger-text); border-color: transparent; }
.lv.on.lv-warn { background: var(--warning-subtle); color: var(--warning-text); border-color: transparent; }
.lv.on.lv-info { background: var(--accent-subtle); color: var(--accent-text); border-color: transparent; }
.rbtn { flex-shrink: 0; width: 26px; height: 26px; display: grid; place-items: center; border-radius: 8px; border: 1px solid var(--border-subtle); color: var(--text-muted); cursor: pointer; }
.rbtn:hover { color: var(--accent-text); border-color: var(--accent-text); }
.rbtn.danger { color: var(--danger-text); }
.rbtn.danger:hover { border-color: var(--danger); }

.pedit { flex-basis: 100%; margin-top: 10px; padding: 12px; border-radius: 10px; background: var(--surface-sunken); }
.fl { font: 600 11px var(--font-mono); color: var(--text-muted); margin-bottom: 6px; }
.pedit .fl { margin-top: 10px; }
.pedit .fl:first-child { margin-top: 0; }
.pbox { width: 100%; box-sizing: border-box; min-height: 60px; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-card); padding: 8px 10px; font: 500 11.5px var(--font-mono); color: var(--text-body); outline: none; resize: vertical; }
.in { width: 100%; box-sizing: border-box; height: 34px; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-card); padding: 0 10px; font: 500 12px var(--font-mono); color: var(--text-body); outline: none; }
.pbox:focus, .in:focus { border-color: var(--accent-text); }
.pnone, .phint { font: 500 12px var(--font-body); color: var(--text-muted); }
.phint { display: flex; align-items: center; justify-content: space-between; gap: 8px; margin-bottom: 4px; }
.preset { border: 0; background: none; padding: 0; font: 600 11.5px var(--font-body); color: var(--accent-text); cursor: pointer; }
.pfield { margin-top: 10px; }
.plabel { font: 600 12px var(--font-body); color: var(--text-body); margin-bottom: 5px; }
.pkey { margin-left: 6px; font: 500 10.5px var(--font-mono); color: var(--text-faint); }
.phelp { margin-top: 4px; display: flex; flex-wrap: wrap; gap: 4px 12px; font: 500 11px var(--font-body); color: var(--text-faint); }
.pacts { margin-top: 12px; display: flex; justify-content: flex-end; gap: 8px; }

.empty { padding: 34px 18px; display: flex; flex-direction: column; align-items: center; gap: 10px; }
.et { font: 500 12.5px var(--font-body); color: var(--text-faint); }
.eclear { padding: 6px 12px; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-card); color: var(--accent-text); font: 600 11.5px var(--font-body); cursor: pointer; }

/* ---------------- 自查沙箱 ---------------- */
.field { margin-bottom: 14px; }
/* 语句框给足高度:自查贴进来的常常是一整段变更,150px 的框要一直滚。 */
.sqlbox { width: 100%; box-sizing: border-box; min-height: 240px; border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-sunken); padding: 11px 13px; font: 500 12.5px/1.7 var(--font-mono); color: var(--text-strong); outline: none; resize: vertical; }
.sqlbox::placeholder { color: var(--text-faint); }
.sqlbox:focus { border-color: var(--accent-text); box-shadow: 0 0 0 3px var(--accent-subtle); }
.runbtn { width: 100%; }

.res { margin-top: 14px; display: flex; flex-direction: column; gap: 8px; max-height: 46vh; overflow-y: auto; }
.rsum { display: flex; align-items: center; gap: 8px; padding: 10px 12px; border-radius: 10px; font: 600 12px var(--font-body); flex-wrap: wrap; }
.rsum.ok { background: var(--success-subtle); color: var(--success-text); }
.rsum.bad { background: var(--danger-subtle); color: var(--danger-text); }
.rcnt { font: 500 11px var(--font-mono); opacity: .8; }
/* 违规条目按级别着色,和左边规则列表上的级别胶囊用同一套语义色 —— 两处说的是
   同一件事,颜色不一致就得让人在脑子里再翻译一次。 */
.find { display: flex; gap: 9px; padding: 10px 12px; border-radius: 10px; border-left: 3px solid transparent; }
.find.error { background: var(--danger-subtle); color: var(--danger-text); border-left-color: var(--danger); }
.find.warn { background: var(--warning-subtle); color: var(--warning-text); border-left-color: var(--warning); }
.find.info { background: var(--surface-sunken); color: var(--text-muted); border-left-color: var(--border-default); }
.fi { flex-shrink: 0; margin-top: 1px; }
.fbody { min-width: 0; }
.fname { font: 600 12px var(--font-body); }
.floc { font: 500 10.5px var(--font-mono); opacity: .75; }
.fmsg { margin-top: 3px; font: 500 11.5px/1.6 var(--font-body); }
.fsql { margin-top: 5px; padding: 6px 8px; border-radius: 6px; background: rgba(0, 0, 0, .06); font: 500 11px var(--font-mono); overflow-x: auto; white-space: pre-wrap; word-break: break-word; }
.fcount { margin-left: 6px; font: 600 11px var(--font-mono); opacity: .85; }
.fex { margin-top: 6px; }
.fmore { margin-top: 6px; font: 500 11px var(--font-body); opacity: .75; }
.nofind { padding: 14px; text-align: center; font: 500 12px var(--font-body); color: var(--text-faint); }
/* 还没跑过时给一块占位,而不是让按钮底下空着 —— 那块空白会让人以为点了没反应 */
.resempty { margin-top: 14px; padding: 22px 14px; border: 1px dashed var(--border-default); border-radius: 10px; text-align: center; font: 500 11.5px var(--font-body); color: var(--text-faint); }

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
/* 用 flex 居中,不用 line-height —— `font:` 简写会把 line-height 一并重置成
   normal,而这里原先正是 `line-height: 36px; ... font: 600 ...`,后者把前者吹掉了,
   于是文字在盒子里贴着上边。改成 flex 之后,字号怎么调都不会再把居中弄丢。 */
.si { flex: 1; display: flex; align-items: center; justify-content: center; height: 36px; font: 600 12px var(--font-mono); color: var(--text-muted); border-left: 1px solid var(--border-subtle); cursor: pointer; }
.si:first-child { border-left: none; }
.si.active { background: var(--accent-subtle); color: var(--accent-text); }
.hint { margin-top: 10px; font: 500 11px var(--font-body); color: var(--text-faint); }
.mfoot { display: flex; justify-content: flex-end; gap: 10px; padding: 12px 22px; border-top: 1px solid var(--border-subtle); background: var(--surface-raised); }
</style>
