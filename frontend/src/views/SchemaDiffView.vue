<script setup lang="ts">
// 跨环境表结构对比(.scratch/schema-diff/PRD.md)。
//
// 左右各选"实例 + 库",只有同一引擎族能互比;服务端两边实时读目录后对比。这一页只负责把结果
// 说清楚:三组(只在左 / 只在右 / 结构不同)、每张不同表的左右对照、导出。
import { ref, computed, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ArrowLeftRight, Play, ChevronRight, Download, FileText, TriangleAlert, CircleCheck, Search } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSelect from '@/components/common/VSelect.vue'
import api from '@/api'
import type { SchemaDiffConn, SchemaDiffResp, SchemaDiffOptions, RuleRef } from '@/types'
import { diffToMarkdown, diffToCSV, columnPairs, indexMarks, showIndexDef } from '@/lib/schemaDiffExport'
import { saveBlob } from '@/lib/download'
import { copyText } from '@/lib/clipboard'
import { useUIStore } from '@/stores/ui'

const { t, te } = useI18n()
const ui = useUIStore()
const tt = (k: string, n?: Record<string, unknown>) => t(k, n || {})

const conns = ref<SchemaDiffConn[]>([])
const loadErr = ref('')
const connLabel = (c: SchemaDiffConn) => `${c.name} · ${c.env.toUpperCase()}`
const byLabel = (l: string) => conns.value.find((c) => connLabel(c) === l)

const leftId = ref(0)
const rightId = ref(0)
const leftDb = ref('')
const rightDb = ref('')
// PG / DWS:库下面还有一层 schema。空 = 库内全部非系统 schema;填了只比这一个(表名不带前缀)。
const leftSchema = ref('')
const rightSchema = ref('')
const opts = ref<SchemaDiffOptions>({ ignoreColumnOrder: false, ignoreComments: false, ignoreIndexNames: false })

const left = computed(() => conns.value.find((c) => c.id === leftId.value))
const right = computed(() => conns.value.find((c) => c.id === rightId.value))
const isPg = computed(() => left.value?.family === 'postgres')
// 右边只列同一引擎族的实例:其它环境的排在前面(通常要比的就是另一个环境),左边自己排最后 ——
// 同一个实例里的两个库 / 两个 schema(orders 与 orders_uat、app 与 app_uat)也要能比。
const rightChoices = computed(() => {
  const l = left.value
  if (!l) return []
  const rank = (c: SchemaDiffConn) => (c.id === l.id ? 2 : c.env === l.env ? 1 : 0)
  return conns.value
    .filter((c) => c.family === l.family)
    .sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name))
})

// 每个实例上的库名(库名下拉),按实例缓存。读不到(连不上 / 没权限 / 列表为空)就退回手填 ——
// 下拉只是帮忙选,不能挡住对比。
type DbList = { names: string[]; error?: string; loading?: boolean }
const dbLists = ref<Record<number, DbList>>({})
async function loadDbs(id: number) {
  if (!id || dbLists.value[id]) return
  dbLists.value[id] = { names: [], loading: true }
  try {
    const r = await api.schemaDiffDatabases(id)
    dbLists.value[id] = { names: r.databases || [], error: r.error }
  } catch (e: any) {
    dbLists.value[id] = { names: [], error: e?.message || t('loadFailed') }
  }
}
const dbNames = (id: number) => dbLists.value[id]?.names || []
// 加载中也先给下拉(可以直接输入);读完没有任何库才换成输入框
const dbPickable = (id: number) => !!dbLists.value[id]?.loading || dbNames(id).length > 0
const dbError = (id: number) => dbLists.value[id]?.error || ''
watch(rightId, (id) => loadDbs(id))

// PG / DWS 库里的 schema(schema 下拉),按"实例 + 库"缓存。第一项「全部 schema」对应留空。
const schLists = ref<Record<string, DbList>>({})
const schKey = (id: number, db: string) => `${id}|${db.trim()}`
async function loadSchemas(id: number, db: string) {
  const k = schKey(id, db)
  if (!id || !db.trim() || schLists.value[k]) return
  schLists.value[k] = { names: [], loading: true }
  try {
    const r = await api.schemaDiffSchemas(id, db.trim())
    schLists.value[k] = { names: r.schemas || [], error: r.error }
  } catch (e: any) {
    schLists.value[k] = { names: [], error: e?.message || t('loadFailed') }
  }
}
const schOf = (id: number, db: string) => schLists.value[schKey(id, db)]
const schPickable = (id: number, db: string) => !!schOf(id, db)?.loading || (schOf(id, db)?.names.length ?? 0) > 0
const schOptions = (id: number, db: string) => [t('sdAllSchemas'), ...(schOf(id, db)?.names || [])]
const fromSchOption = (v: string) => (v === t('sdAllSchemas') ? '' : v)
// 库名是手填的时候每敲一个字都会变:停下来再读
let schTimer: ReturnType<typeof setTimeout> | undefined
watch([isPg, leftId, leftDb, rightId, rightDb], () => {
  if (!isPg.value) return
  clearTimeout(schTimer)
  schTimer = setTimeout(() => {
    loadSchemas(leftId.value, leftDb.value)
    loadSchemas(rightId.value, rightDb.value)
  }, 300)
})

watch(leftId, () => {
  const l = left.value
  if (!l) return
  loadDbs(l.id)
  leftDb.value = l.database
  // 换了左边:右边不再合法就换成第一个候选;库名默认与左边同名(比的是"同一个库在不同环境")
  if (!rightChoices.value.some((c) => c.id === rightId.value)) rightId.value = rightChoices.value[0]?.id ?? 0
  rightDb.value = l.family === 'oracle' ? (right.value?.database || l.database) : l.database
})
watch(rightId, () => {
  const r = right.value
  if (r && left.value?.family === 'oracle') rightDb.value = r.database
})

onMounted(async () => {
  try {
    conns.value = await api.schemaDiffConnections()
    if (conns.value.length) leftId.value = conns.value[0].id
  } catch (e: any) {
    loadErr.value = e?.message || t('loadFailed')
  }
})

// ---------------------------------------------------------------- 对比

const running = ref(false)
const result = ref<SchemaDiffResp | null>(null)
const failed = ref('')
const canRun = computed(() => !!left.value && !!right.value && !running.value)

async function run() {
  if (!canRun.value) return
  running.value = true
  failed.value = ''
  result.value = null
  expanded.value = new Set()
  try {
    const r = await api.schemaDiff({
      left: { connectionId: leftId.value, database: leftDb.value.trim(), schema: isPg.value ? leftSchema.value.trim() : '' },
      right: { connectionId: rightId.value, database: rightDb.value.trim(), schema: isPg.value ? rightSchema.value.trim() : '' },
      options: opts.value,
    })
    if (r.error) failed.value = readFailure(r)
    else result.value = r
  } catch (e: any) {
    // 对比前的拒绝(无权访问一边、引擎不同……)同样带原因代码
    failed.value = reasonText(e?.data?.reasonRef, e?.message || t('actionFailed'))
  } finally {
    running.value = false
  }
}

// 原因按读者语言说:认得 code 就用词条,认不得回落到服务端的中文原话。side 参数(left / right)
// 换成读者语言的"左边 / 右边"。
function reasonText(ref: RuleRef | undefined, fallback: string, side?: string): string {
  const key = ref ? `sdErr_${ref.code}` : ''
  if (!ref || !te(key)) return fallback
  const s = side || ref.args?.side
  return t(key, { ...(ref.args || {}), side: s ? t(s === 'right' ? 'sdRight' : 'sdLeft') : '' })
}
const readFailure = (r: SchemaDiffResp) => reasonText(r.errorRef, r.error || '', r.errorSide)

// ---------------------------------------------------------------- 结果

const kw = ref('')
const match = (name: string) => !kw.value.trim() || name.toLowerCase().includes(kw.value.trim().toLowerCase())
const d = computed(() => result.value?.diff)
const changed = computed(() => (d.value?.changed || []).filter((x) => match(x.table)))
const onlyLeft = computed(() => (d.value?.onlyLeft || []).filter(match))
const onlyRight = computed(() => (d.value?.onlyRight || []).filter(match))
const same = computed(() => (d.value?.same || []).filter(match))
const showSame = ref(false)
const expanded = ref(new Set<string>())
function toggle(name: string) {
  const s = new Set(expanded.value)
  if (s.has(name)) s.delete(name)
  else s.add(name)
  expanded.value = s
}
// MySQL 的列名 / 索引名不区分大小写(与服务端同一个规则)
const ci = computed(() => left.value?.family === 'mysql')
const showDefault = (c?: { hasDefault: boolean; default: string }) => (!c ? '' : !c.hasDefault ? t('sdNoDefault') : c.default === '' ? "''" : c.default)
const allSame = computed(() => !!d.value && !d.value.onlyLeft.length && !d.value.onlyRight.length && !d.value.changed.length)
const fmtTime = (s: string) => {
  const x = new Date(s)
  return isNaN(x.getTime()) ? s : x.toLocaleString('sv').slice(0, 19)
}

// ---------------------------------------------------------------- 导出

function fileBase() {
  const r = result.value!
  return `schema-diff_${r.left.instance}_${r.right.instance}_${r.left.database}`.replace(/[^\w.-]+/g, '_')
}
function exportMd() {
  if (!result.value) return
  saveBlob(new Blob([diffToMarkdown(result.value, tt)], { type: 'text/markdown;charset=utf-8' }), fileBase() + '.md')
}
async function copyMd() {
  if (result.value && await copyText(diffToMarkdown(result.value, tt))) ui.notify(t('sdCopied'), 'success')
}
function exportCsv() {
  if (!result.value) return
  saveBlob(new Blob([diffToCSV(result.value, tt)], { type: 'text/csv;charset=utf-8' }), fileBase() + '.csv')
}
</script>

<template>
  <div class="scy page">
    <div v-if="loadErr" class="sd-msg err">{{ loadErr }}</div>
    <div v-else-if="!conns.length" class="sd-msg" data-testid="sd-no-conns">{{ $t('sdNoConns') }}</div>

    <template v-else>
      <!-- 选择条 -->
      <section class="card sd-pick">
        <div class="side">
          <div class="sl">{{ $t('sdLeft') }}</div>
          <VSelect :model-value="left ? connLabel(left) : ''" :options="conns.map(connLabel)" searchable height="36px"
                   data-testid="sd-left" @update:model-value="(l: string) => (leftId = byLabel(l)?.id ?? 0)" />
          <VSelect v-if="dbPickable(leftId)" v-model="leftDb" :options="dbNames(leftId)" searchable creatable height="36px"
                   :placeholder="$t('sdDbPh')" :search-placeholder="$t('sdDbSearchPh')" data-testid="sd-left-db" />
          <input v-else v-model="leftDb" class="db" :placeholder="$t('sdDbPh')" :title="dbError(leftId)" data-testid="sd-left-db" />
          <template v-if="isPg">
            <VSelect v-if="schPickable(leftId, leftDb)" :model-value="leftSchema" :options="schOptions(leftId, leftDb)" searchable creatable
                     height="36px" :placeholder="$t('sdSchemaPh')" :search-placeholder="$t('sdSchemaSearchPh')" data-testid="sd-left-schema"
                     @update:model-value="(v: string) => (leftSchema = fromSchOption(v))" />
            <input v-else v-model="leftSchema" class="db" :placeholder="$t('sdSchemaPh')" :title="schOf(leftId, leftDb)?.error || ''" data-testid="sd-left-schema" />
          </template>
        </div>
        <ArrowLeftRight :size="18" class="arrow" />
        <div class="side">
          <div class="sl">{{ $t('sdRight') }}</div>
          <VSelect :model-value="right ? connLabel(right) : $t('sdPickRight')" :options="rightChoices.map(connLabel)" searchable height="36px"
                   data-testid="sd-right" @update:model-value="(l: string) => (rightId = byLabel(l)?.id ?? 0)" />
          <VSelect v-if="dbPickable(rightId)" v-model="rightDb" :options="dbNames(rightId)" searchable creatable height="36px"
                   :placeholder="$t('sdDbPh')" :search-placeholder="$t('sdDbSearchPh')" data-testid="sd-right-db" />
          <input v-else v-model="rightDb" class="db" :placeholder="$t('sdDbPh')" :title="dbError(rightId)" data-testid="sd-right-db" />
          <template v-if="isPg">
            <VSelect v-if="schPickable(rightId, rightDb)" :model-value="rightSchema" :options="schOptions(rightId, rightDb)" searchable creatable
                     height="36px" :placeholder="$t('sdSchemaPh')" :search-placeholder="$t('sdSchemaSearchPh')" data-testid="sd-right-schema"
                     @update:model-value="(v: string) => (rightSchema = fromSchOption(v))" />
            <input v-else v-model="rightSchema" class="db" :placeholder="$t('sdSchemaPh')" :title="schOf(rightId, rightDb)?.error || ''" data-testid="sd-right-schema" />
          </template>
        </div>
        <div class="acts">
          <label class="ck"><input v-model="opts.ignoreColumnOrder" type="checkbox" />{{ $t('sdIgnoreOrder') }}</label>
          <label class="ck"><input v-model="opts.ignoreComments" type="checkbox" />{{ $t('sdIgnoreComments') }}</label>
          <label class="ck"><input v-model="opts.ignoreIndexNames" type="checkbox" />{{ $t('sdIgnoreIndexNames') }}</label>
          <VButton variant="primary" height="36px" :disabled="!canRun" data-testid="sd-run" @click="run">
            <Play :size="13" />{{ running ? $t('sdRunning') : $t('sdRun') }}
          </VButton>
        </div>
        <div v-if="left && !rightChoices.length" class="hint">{{ $t('sdNoPeer', { engine: left.engine }) }}</div>
      </section>

      <div v-if="failed" class="sd-msg err" data-testid="sd-error"><TriangleAlert :size="14" />{{ failed }}</div>

      <template v-if="result && d">
        <!-- 汇总 -->
        <section class="card sd-sum" data-testid="sd-summary">
          <div class="meta">
            <div><span class="tag l">{{ $t('sdLeft') }}</span><b>{{ result.left.instance }}</b> · {{ result.left.env.toUpperCase() }} / {{ result.left.database }}{{ result.left.schema ? "." + result.left.schema : "" }}
              <span class="dim">{{ $t('sdTablesN', { n: d.summary.left }) }} · {{ $t('sdReadAt', { t: fmtTime(result.left.readAt) }) }}</span></div>
            <div><span class="tag r">{{ $t('sdRight') }}</span><b>{{ result.right.instance }}</b> · {{ result.right.env.toUpperCase() }} / {{ result.right.database }}{{ result.right.schema ? "." + result.right.schema : "" }}
              <span class="dim">{{ $t('sdTablesN', { n: d.summary.right }) }} · {{ $t('sdReadAt', { t: fmtTime(result.right.readAt) }) }}</span></div>
          </div>
          <div class="counts">
            <span class="cnt same">{{ $t('sdSame') }} <b>{{ d.summary.same }}</b></span>
            <span class="cnt ol">{{ $t('sdOnlyLeft') }} <b>{{ d.summary.onlyLeft }}</b></span>
            <span class="cnt or">{{ $t('sdOnlyRight') }} <b>{{ d.summary.onlyRight }}</b></span>
            <span class="cnt ch">{{ $t('sdChanged') }} <b>{{ d.summary.changed }}</b></span>
          </div>
          <div class="tools">
            <div class="sbox"><Search :size="13" /><input v-model="kw" :placeholder="$t('sdSearchPh')" /></div>
            <VButton variant="secondary" height="32px" data-testid="sd-copy-md" @click="copyMd"><FileText :size="13" />{{ $t('sdCopyMd') }}</VButton>
            <VButton variant="secondary" height="32px" data-testid="sd-export-md" @click="exportMd"><Download :size="13" />Markdown</VButton>
            <VButton variant="secondary" height="32px" data-testid="sd-export-csv" @click="exportCsv"><Download :size="13" />CSV</VButton>
          </div>
        </section>

        <div v-if="result.emptySide" class="sd-msg warn" data-testid="sd-empty"><TriangleAlert :size="14" />{{ $t('sdEmpty_' + result.emptySide) }}</div>
        <div v-if="allSame && !result.emptySide" class="sd-msg ok" data-testid="sd-all-same"><CircleCheck :size="15" />{{ $t('sdAllSame', { n: d.summary.same }) }}</div>

        <!-- 结构不同 -->
        <section v-if="changed.length" class="card grp" data-testid="sd-changed">
          <div class="gh ch">{{ $t('sdChanged') }} · {{ changed.length }}</div>
          <div v-for="td in changed" :key="td.table" class="tbl">
            <button class="th" data-testid="sd-changed-table" @click="toggle(td.table)">
              <ChevronRight :size="14" class="chev" :class="{ open: expanded.has(td.table) }" />
              <span class="tn">{{ td.table }}</span>
              <span class="dim">{{ $t('sdDiffCount', { n: (td.columns?.length || 0) + (td.indexes?.length || 0) + (td.kind ? 1 : 0) + (td.comment ? 1 : 0) + (td.primaryKey ? 1 : 0) }) }}</span>
            </button>
            <div v-if="expanded.has(td.table)" class="cmp" data-testid="sd-detail">
              <!-- 表属性:表 / 视图、表注释、主键 —— 有差异的格子高亮 -->
              <table class="dt">
                <thead><tr><th class="w-obj"></th><th>{{ $t('sdLeft') }} · {{ result.left.instance }}</th><th>{{ $t('sdRight') }} · {{ result.right.instance }}</th></tr></thead>
                <tbody>
                  <tr><td class="obj">{{ $t('sdAttr_kind') }}</td>
                    <td :class="{ hl: td.kind }">{{ td.leftDef?.kind }}</td><td :class="{ hl: td.kind }">{{ td.rightDef?.kind }}</td></tr>
                  <tr><td class="obj">{{ $t('sdAttr_comment') }}</td>
                    <td :class="{ hl: td.comment }">{{ td.leftDef?.comment || '—' }}</td><td :class="{ hl: td.comment }">{{ td.rightDef?.comment || '—' }}</td></tr>
                  <tr><td class="obj">{{ $t('sdPrimaryKey') }}</td>
                    <td :class="{ hl: td.primaryKey }">{{ td.leftDef?.primaryKey?.length ? '(' + td.leftDef.primaryKey.join(', ') + ')' : '—' }}</td>
                    <td :class="{ hl: td.primaryKey }">{{ td.rightDef?.primaryKey?.length ? '(' + td.rightDef.primaryKey.join(', ') + ')' : '—' }}</td></tr>
                </tbody>
              </table>

              <!-- 列:左右对照,不同的属性格高亮,只在一边的整行标色 -->
              <table class="dt cols">
                <thead>
                  <tr><th rowspan="2" class="w-obj">{{ $t('sdColumn') }}</th>
                    <th colspan="4" class="sh-l">{{ $t('sdLeft') }} · {{ result.left.instance }}</th>
                    <th colspan="4" class="sh-r">{{ $t('sdRight') }} · {{ result.right.instance }}</th></tr>
                  <tr><th>{{ $t('sdAttr_type') }}</th><th>{{ $t('sdAttr_nullable') }}</th><th>{{ $t('sdAttr_default') }}</th><th>{{ $t('sdAttr_comment') }}</th>
                    <th>{{ $t('sdAttr_type') }}</th><th>{{ $t('sdAttr_nullable') }}</th><th>{{ $t('sdAttr_default') }}</th><th>{{ $t('sdAttr_comment') }}</th></tr>
                </thead>
                <tbody>
                  <tr v-for="row in columnPairs(td, ci)" :key="row.name" :class="row.only ? 'only-' + row.only : ''" data-testid="sd-col-row">
                    <td class="obj">{{ row.name }}<span v-if="row.diff.has('position')" class="pos">{{ $t('sdMoved') }}</span></td>
                    <template v-for="(c, side) in { left: row.left, right: row.right }" :key="side">
                      <template v-if="c">
                        <td :class="{ hl: row.diff.has('type') }">{{ c.dataType }}</td>
                        <td :class="{ hl: row.diff.has('nullable') }">{{ c.nullable ? 'NULL' : 'NOT NULL' }}</td>
                        <td :class="{ hl: row.diff.has('default') }">{{ showDefault(c) }}</td>
                        <td :class="{ hl: row.diff.has('comment') }">{{ c.comment }}</td>
                      </template>
                      <td v-else colspan="4" class="miss">{{ $t('sdAbsent') }}</td>
                    </template>
                  </tr>
                </tbody>
              </table>

              <!-- 索引:两边各列自己的,按服务端的差异标色(不在前端按名字配对) -->
              <div class="idx">
                <div v-for="(list, side) in indexMarks(td, ci)" :key="side" class="idxcol">
                  <div class="idxh">{{ $t('sdIndex') }} · {{ side === 'left' ? result.left.instance : result.right.instance }}</div>
                  <div v-if="!list.length" class="idxi miss">—</div>
                  <div v-for="m in list" :key="m.index.name" class="idxi" :class="m.status" data-testid="sd-index">
                    <span class="nm2">{{ m.index.name }}</span><span>{{ showIndexDef(m.index) }}</span>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </section>

        <div class="two">
          <section v-if="onlyLeft.length" class="card grp" data-testid="sd-only-left">
            <div class="gh ol">{{ $t('sdOnlyLeftOf', { n: result.left.instance }) }} · {{ onlyLeft.length }}</div>
            <div class="names"><span v-for="n in onlyLeft" :key="n" class="nm">{{ n }}</span></div>
          </section>
          <section v-if="onlyRight.length" class="card grp" data-testid="sd-only-right">
            <div class="gh or">{{ $t('sdOnlyRightOf', { n: result.right.instance }) }} · {{ onlyRight.length }}</div>
            <div class="names"><span v-for="n in onlyRight" :key="n" class="nm">{{ n }}</span></div>
          </section>
        </div>

        <section v-if="same.length" class="card grp">
          <button class="gh same btn" @click="showSame = !showSame">
            <ChevronRight :size="14" class="chev" :class="{ open: showSame }" />{{ $t('sdSame') }} · {{ same.length }}
          </button>
          <div v-if="showSame" class="names"><span v-for="n in same" :key="n" class="nm">{{ n }}</span></div>
        </section>
      </template>
    </template>
  </div>
</template>

<style scoped>
.page { flex: 1; min-height: 0; padding: var(--page-pad); max-width: var(--page-max); margin-inline: auto; width: 100%; box-sizing: border-box; display: flex; flex-direction: column; gap: 14px; }
/* 页面是纵向 flex:卡片不许被压扁(否则展开的对照表被 overflow 裁掉),多出来的由页面滚动 */
.page > * { flex-shrink: 0; }
.card { border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-xs); }
.sd-msg { display: flex; align-items: center; gap: 8px; padding: 12px 14px; border-radius: 10px; border: 1px dashed var(--border-default); font: 500 13px/1.6 var(--font-body); color: var(--text-muted); }
.sd-msg.err { border: none; background: var(--danger-subtle); color: var(--danger-text); }
.sd-msg.ok { border: none; background: var(--success-subtle); color: var(--success-text); }
.sd-msg.warn { border: none; background: var(--warning-subtle); color: var(--warning-text); }

.sd-pick { display: grid; grid-template-columns: minmax(0, 1fr) auto minmax(0, 1fr); gap: 14px 16px; align-items: end; padding: 16px 18px; }
.side { display: flex; flex-direction: column; gap: 8px; min-width: 0; }
.sl { font: 600 12px var(--font-body); color: var(--text-muted); }
.db { height: 36px; padding: 0 12px; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-sunken); color: var(--text-strong); font: 500 12.5px var(--font-mono); outline: none; }
.db:focus { border-color: var(--accent-text); }
.arrow { color: var(--text-faint); margin-bottom: 50px; }
.acts { grid-column: 1 / -1; display: flex; align-items: center; gap: 16px; flex-wrap: wrap; justify-content: flex-end; }
.ck { display: inline-flex; align-items: center; gap: 6px; font: 500 12.5px var(--font-body); color: var(--text-body); cursor: pointer; }
.hint { grid-column: 1 / -1; font: 500 12px var(--font-body); color: var(--warning-text); }

.sd-sum { padding: 14px 18px; display: flex; flex-direction: column; gap: 12px; }
.meta { display: flex; flex-direction: column; gap: 6px; font: 500 12.5px var(--font-body); color: var(--text-body); }
.meta b { font-family: var(--font-mono); color: var(--text-strong); }
.tag { display: inline-block; min-width: 34px; text-align: center; margin-right: 8px; padding: 1px 6px; border-radius: 6px; font: 600 11px var(--font-body); }
.tag.l { background: var(--accent-subtle); color: var(--accent-text); }
.tag.r { background: var(--warning-subtle); color: var(--warning-text); }
.dim { margin-left: 8px; color: var(--text-faint); font: 500 11.5px var(--font-body); }
.counts { display: flex; gap: 10px; flex-wrap: wrap; }
.cnt { padding: 5px 12px; border-radius: 999px; font: 500 12px var(--font-body); background: var(--surface-sunken); color: var(--text-muted); }
.cnt b { margin-left: 4px; font-family: var(--font-mono); color: var(--text-strong); }
.cnt.ch { background: var(--danger-subtle); color: var(--danger-text); }
.cnt.ol { background: var(--accent-subtle); color: var(--accent-text); }
.cnt.or { background: var(--warning-subtle); color: var(--warning-text); }
.tools { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.sbox { display: flex; align-items: center; gap: 6px; height: 32px; padding: 0 10px; margin-right: auto; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-sunken); color: var(--text-faint); }
.sbox input { border: none; outline: none; background: transparent; color: var(--text-strong); font: 500 12.5px var(--font-body); width: 200px; }

.grp { padding: 6px 0; overflow: hidden; }
.gh { display: flex; align-items: center; gap: 6px; padding: 10px 18px; font: 600 13px var(--font-body); color: var(--text-strong); }
.gh.btn { width: 100%; border: none; background: transparent; cursor: pointer; text-align: left; }
.gh.ch { color: var(--danger-text); }
.gh.ol { color: var(--accent-text); }
.gh.or { color: var(--warning-text); }
.tbl { border-top: 1px solid var(--border-subtle); }
.th { display: flex; align-items: center; gap: 8px; width: 100%; padding: 10px 18px; border: none; background: transparent; cursor: pointer; text-align: left; }
.th:hover { background: var(--surface-sunken); }
.tn { font: 600 13px var(--font-mono); color: var(--text-strong); }
.chev { color: var(--text-faint); transition: transform .12s; flex-shrink: 0; }
.chev.open { transform: rotate(90deg); }
.cmp { padding: 0 18px 14px; display: flex; flex-direction: column; gap: 10px; overflow-x: auto; }
.dt { width: 100%; border-collapse: collapse; font: 500 12px var(--font-mono); }
.dt th { text-align: left; padding: 6px 10px; font: 600 11.5px var(--font-body); color: var(--text-muted); border-bottom: 1px solid var(--border-subtle); background: var(--surface-sunken); }
.dt td { padding: 6px 10px; border-bottom: 1px solid var(--border-subtle); color: var(--text-body); vertical-align: top; word-break: break-word; }
.dt .obj { color: var(--text-strong); white-space: nowrap; }
.dt .attr { color: var(--text-muted); font-family: var(--font-body); white-space: nowrap; }
.dt .w-obj { width: 160px; }
.dt th.sh-l { color: var(--accent-text); }
.dt th.sh-r { color: var(--warning-text); }
.dt td.hl { background: var(--warning-subtle); color: var(--warning-text); font-weight: 600; }
.dt td.miss { color: var(--text-faint); font-family: var(--font-body); font-style: italic; }
.dt tr.only-left td { background: color-mix(in srgb, var(--accent-subtle) 70%, transparent); }
.dt tr.only-right td { background: color-mix(in srgb, var(--warning-subtle) 70%, transparent); }
.dt tr.only-left td.miss, .dt tr.only-right td.miss { background: transparent; }
.pos { margin-left: 6px; padding: 0 5px; border-radius: 4px; background: var(--warning-subtle); color: var(--warning-text); font: 600 10px var(--font-body); }
.idx { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.idxh { padding: 6px 10px; font: 600 11.5px var(--font-body); color: var(--text-muted); background: var(--surface-sunken); }
.idxi { display: flex; gap: 10px; padding: 5px 10px; border-bottom: 1px solid var(--border-subtle); font: 500 12px var(--font-mono); color: var(--text-body); }
.idxi .nm2 { color: var(--text-strong); min-width: 140px; }
.idxi.only { background: color-mix(in srgb, var(--accent-subtle) 70%, transparent); }
.idxi.changed { background: var(--warning-subtle); color: var(--warning-text); }
.idxi.miss { color: var(--text-faint); }
.two { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }
.names { display: flex; flex-wrap: wrap; gap: 6px; padding: 4px 18px 12px; }
.nm { padding: 3px 9px; border-radius: 6px; background: var(--surface-sunken); font: 500 12px var(--font-mono); color: var(--text-body); }
@media (max-width: 900px) {
  .sd-pick { grid-template-columns: minmax(0, 1fr); }
  .arrow { display: none; }
  .two { grid-template-columns: 1fr; }
}
</style>
