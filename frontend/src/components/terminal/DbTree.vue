<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Search, ChevronDown, ChevronRight, Database, FolderOpen, Table2, PanelLeftClose, X } from 'lucide-vue-next'
import api from '@/api'
import ObjectGroups from './ObjectGroups.vue'
import { engineDisplay, engineLabels } from '@/lib/engines'
import { useEnvTierStore } from '@/stores/envtier'
import type { Connection, ConnectionSchema, DbObjects } from '@/types'

const props = defineProps<{ connections: Connection[]; selectedId: number; selectedDb?: string }>()
const emit = defineEmits<{ select: [number]; selectDb: [number, string]; collapse: [] }>()
const { t } = useI18n()
const envtier = useEnvTierStore()
// Tiers/environments drive the section structure; a failed load leaves the lists
// empty and every instance falls into the unresolved group rather than vanishing.
envtier.load().catch(() => {})

// Schema (db → tables) for the selected instance, introspected live from the
// gateway. Real introspection can take a moment and may fail (bad creds / network),
// so track a loading flag and surface the returned error instead of a silent empty.
const schema = ref<ConnectionSchema | null>(null)
const schemaLoading = ref(false)
watch(() => props.selectedId, async (id) => {
  schema.value = null
  if (!id) return
  schemaLoading.value = true
  try {
    schema.value = await api.connectionSchema(id)
  } catch {
    schema.value = null
  } finally {
    schemaLoading.value = false
  }
}, { immediate: true })

const search = ref('')
// Starts EMPTY. It used to seed { gli, staging, dev } — three environment codes
// written into the component, which is exactly what the tier model removes. The
// defaults are set from the loaded environments instead (see the watch below),
// and a key that never gets one reads as expanded: a group nothing could file
// (an environment that no longer resolves) is the one worth showing open.
const collapsed = ref<Record<string, boolean>>({})

// Per-database expand state (collapsed by default so a cluster with many databases
// stays scannable). Keyed by connection:database so switching instances resets it.
const dbOpen = ref<Record<string, boolean>>({})
const dbKey = (cid: number, name: string) => `${cid}:${name}`
const isDbOpen = (cid: number, name: string) => !!dbOpen.value[dbKey(cid, name)]
function toggleDb(cid: number, name: string) {
  const k = dbKey(cid, name)
  dbOpen.value[k] = !dbOpen.value[k]
  if (dbOpen.value[k]) loadDbTables(cid, name).then(() => maybeLoadDbObjects(cid, name))
}
// Clicking a database selects it as the target and expands it to reveal its tables.
function selectDb(cid: number, name: string) {
  dbOpen.value[dbKey(cid, name)] = true
  loadDbTables(cid, name).then(() => maybeLoadDbObjects(cid, name))
  emit('selectDb', cid, name)
}

// Schema-level (database → schema → tables) collapse, for engines with schemas
// (PostgreSQL). Keyed by connection:database:schema.
const schemaOpen = ref<Record<string, boolean>>({})
const schemaKey = (cid: number, db: string, sc: string) => `${cid}:${db}:${sc}`
const isSchemaOpen = (cid: number, db: string, sc: string) => !!schemaOpen.value[schemaKey(cid, db, sc)]
function toggleSchema(cid: number, db: string, sc: string) {
  const k = schemaKey(cid, db, sc)
  schemaOpen.value[k] = !schemaOpen.value[k]
  // Objects live per schema for schema-ful engines — load on first expand.
  if (schemaOpen.value[k]) ensureObjects(cid, db, sc)
}

// ---- programmable objects (functions / procedures / packages / triggers) ----
//
// Loaded lazily per database (flat engines: MySQL by database, Oracle by owner,
// SQLite single namespace) or per schema (PostgreSQL family), alongside the
// tables. The scope string sent to the server is the schema when there is one,
// else the database/owner name — which is exactly the namespace each engine
// keys its catalog on.
const objects = ref<Record<string, DbObjects | null>>({}) // key → loaded set (null = loading)
const objKey = (cid: number, db: string, sc = '') => `${cid}:${db}:${sc}`
function objFor(cid: number, db: string, sc = ''): DbObjects | null | undefined {
  return objects.value[objKey(cid, db, sc)]
}
async function ensureObjects(cid: number, db: string, sc = '') {
  const k = objKey(cid, db, sc)
  if (k in objects.value) return
  objects.value[k] = null
  try {
    objects.value[k] = await api.connectionObjects(cid, sc || db)
  } catch (e: any) {
    objects.value[k] = { functions: [], procedures: [], packages: [], triggers: [], error: e?.message || t('objLoadFail') }
  }
}
// A database that turned out flat (no schema layer) carries its objects itself.
function maybeLoadDbObjects(cid: number, name: string) {
  const d = schema.value?.databases.find((x) => x.name === name)
  if (d && !(d.schemas && d.schemas.length)) ensureObjects(cid, name)
}

// ---- source viewer ----
const src = ref({ open: false, name: '', type: '', text: '', loading: false, err: '' })
async function openSource(cid: number, scope: string, type: string, name: string) {
  src.value = { open: true, name, type, text: '', loading: true, err: '' }
  try {
    const r = await api.objectSource(cid, scope, type, name)
    src.value.text = r.source
  } catch (e: any) {
    src.value.err = e?.message || t('objLoadFail')
  } finally {
    src.value.loading = false
  }
}
function closeSource() { src.value.open = false }

// Lazily load a database's contents on first expand. Needed for PostgreSQL, where
// the top level lists databases (no tables/schemas) introspected on demand; a
// database already populated (MySQL, or a loaded PG db) is skipped.
const dbLoading = ref<Record<string, boolean>>({})
async function loadDbTables(cid: number, name: string) {
  const d = schema.value?.databases.find((x) => x.name === name)
  if (!d || d.tables.length || d.schemas?.length || dbLoading.value[dbKey(cid, name)]) return
  dbLoading.value[dbKey(cid, name)] = true
  try {
    const sc = await api.connectionSchema(cid, name)
    const loaded = sc.databases.find((x) => x.name === name) || sc.databases[0]
    if (loaded) {
      d.tables = loaded.tables
      d.schemas = loaded.schemas
      // Auto-expand the sole schema (usually "public") so its tables are visible.
      if (loaded.schemas?.length === 1) schemaOpen.value[schemaKey(cid, name, loaded.schemas[0].name)] = true
    }
  } catch { /* leave empty on failure */ }
  finally { dbLoading.value[dbKey(cid, name)] = false }
}

// risk tag derived from policy
function tag(c: Connection) {
  if (c.policy === 'strict') return { text: 'highTag', cls: 'danger' }
  if (c.policy === 'approve-1') return { text: 'limitedTag', cls: 'warn' }
  return null
}

// The tree groups environment → database type → instance. Those are the two
// questions an operator answers before touching anything ("which cluster", "which
// engine"), and an estate with several engines per cluster cannot be navigated by
// name alone. The axis is still switchable to a flat by-type view, which answers
// a different question: every instance of one engine, across all environments.
type GroupMode = 'env' | 'type'
const groupMode = ref<GroupMode>('env')

/** A second-level bucket: one database type, or the whole group in type mode. */
interface TreeChild { key: string; label: string; conns: Connection[] }
/** A first-level section: one environment, or an engine type. */
interface TreeGroup {
  key: string
  label: string
  dot: string
  /** The tier governing this environment, shown on hover — see below. */
  hint: string
  count: number
  /** true → render the children's instances without their own sub-header. */
  flat: boolean
  children: TreeChild[]
}

// Search matches the instance name, its environment, or its database type. With
// several production environments and several engines in each, the name alone no
// longer says where an instance lives or what it is — "hk" and "postgres" are both
// realistic ways to look for one.
const matching = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return props.connections.slice()
  return props.connections.filter((c) =>
    c.name.toLowerCase().includes(q) ||
    c.env.toLowerCase().includes(q) ||
    envtier.envLabel(c.env).toLowerCase().includes(q) ||
    engineDisplay(c.engine).toLowerCase().includes(q))
})

/** Types ordered by the engine catalogue, with anything unrecognised last. */
function byCatalogueOrder(a: string, b: string): number {
  const labels = engineLabels()
  const ia = labels.indexOf(a)
  const ib = labels.indexOf(b)
  if (ia === ib) return a.localeCompare(b)
  if (ia < 0) return 1
  if (ib < 0) return -1
  return ia - ib
}

/** Bucket a set of instances by database type, in catalogue order. */
function typeChildren(conns: Connection[]): TreeChild[] {
  const byType: Record<string, Connection[]> = {}
  for (const c of conns) (byType[engineDisplay(c.engine)] ||= []).push(c)
  return Object.keys(byType).sort(byCatalogueOrder)
    .map((k) => ({ key: k, label: k, conns: byType[k] }))
}

/**
 * Sections in display order.
 *
 * The top level is the ENVIRONMENT, not the control tier. The tier still decides
 * how the instance is governed, and it still supplies the environment row's
 * colour — losing the "which of these is production" cue was the one thing this
 * layout could not afford — but it no longer occupies a level of its own. Its
 * name is on the row's tooltip for when the colour is not enough.
 *
 * The last group is the catch-all for instances whose environment no longer
 * resolves. Without it they would simply not be listed — the instance would still
 * exist and still be reachable, just invisible here, which is the worst of the
 * available outcomes.
 */
const groups = computed<TreeGroup[]>(() => {
  if (groupMode.value === 'type') {
    const byType: Record<string, Connection[]> = {}
    for (const c of matching.value) (byType[engineDisplay(c.engine)] ||= []).push(c)
    return Object.keys(byType).sort(byCatalogueOrder).map((k) => ({
      key: k, label: k, dot: 'info', hint: '', count: byType[k].length, flat: true,
      children: [{ key: k, label: k, conns: byType[k] }],
    }))
  }

  const byEnv: Record<string, Connection[]> = {}
  for (const c of matching.value) (byEnv[c.env] ||= []).push(c)

  const out: TreeGroup[] = []
  const claimed = new Set<string>()
  // Environments in their own display order, which follows their tier's — so the
  // production clusters still come first.
  for (const tier of envtier.tiers) {
    for (const e of envtier.envsByTier[tier.code] ?? []) {
      claimed.add(e.code)
      const conns = byEnv[e.code] ?? []
      out.push({
        key: e.code,
        label: envtier.envLabel(e.code),
        dot: envtier.dotForEnv(e.code),
        hint: envtier.tierLabel(tier.code, t as any),
        count: conns.length,
        flat: false,
        children: typeChildren(conns),
      })
    }
  }

  const orphans = Object.keys(byEnv).filter((k) => !claimed.has(k)).sort()
  for (const k of orphans) {
    out.push({
      key: k,
      label: k,
      dot: 'muted',
      hint: t('treeUnknownEnv'),
      count: byEnv[k].length,
      flat: false,
      children: typeChildren(byEnv[k]),
    })
  }
  return out
})

const searching = computed(() => search.value.trim().length > 0)
function toggle(key: string) { collapsed.value[key] = !collapsed.value[key] }
// Searching expands everything: a hit two levels down is useless if the levels
// above it stay shut.
function isOpen(key: string) { return searching.value || !collapsed.value[key] }
function setGroupMode(m: GroupMode) { groupMode.value = m }

// Environments on the FIRST tier start expanded, the rest collapsed. That is the
// old "prod open, everything else closed" default, restated for a world where
// the top level is environments: every production cluster is open, so adding a
// second one does not push the first out of view or bury it behind a chevron.
watch([() => envtier.tiers, () => envtier.environments], () => {
  const first = envtier.tiers[0]?.code
  for (const e of envtier.environments) {
    if (!(e.code in collapsed.value)) collapsed.value[e.code] = e.tierCode !== first
  }
}, { immediate: true, deep: true })

// Per-instance collapse of its database list. Clicking an instance selects it and
// expands the list; clicking the already-selected instance collapses/expands it.
const instCollapsed = ref<Record<number, boolean>>({})
const instOpen = (id: number) => id === props.selectedId && !instCollapsed.value[id]
function clickInst(id: number) {
  if (id === props.selectedId) instCollapsed.value[id] = !instCollapsed.value[id]
  else { instCollapsed.value[id] = false; emit('select', id) }
}
</script>

<template>
  <div class="tree">
    <div class="head">
      <div class="eyebrow">{{ $t('treeTitle') }}<PanelLeftClose class="collapse" :size="15" :title="$t('treeCollapse')" @click="emit('collapse')" /></div>
      <div class="searchbox"><Search :size="14" color="var(--text-faint)" /><input v-model="search" :placeholder="$t('search')" /></div>
      <div class="gmode">
        <button :class="{ on: groupMode === 'env' }" @click="setGroupMode('env')">{{ $t('groupByEnv') }}</button>
        <button :class="{ on: groupMode === 'type' }" @click="setGroupMode('type')">{{ $t('groupByType') }}</button>
      </div>
    </div>
    <div class="scy body">
      <template v-for="g in groups" :key="g.key">
        <!-- First level: the environment. The dot carries its TIER colour — the
             tier no longer has a row of its own, and losing the "which of these
             is production" cue was the one thing this layout could not afford.
             The tier name is on the tooltip for when the colour is not enough. -->
        <div class="env" :class="{ muted: g.dot !== 'danger' }" :title="g.hint" @click="toggle(g.key)">
          <component :is="isOpen(g.key) ? ChevronDown : ChevronRight" :size="14" color="var(--text-muted)" />
          <span class="d" :class="g.dot" />{{ g.label }}
          <span class="cnt">{{ g.count }}</span>
        </div>
        <template v-if="isOpen(g.key)">
        <div v-if="!g.children.length" class="ind"><div class="empty">{{ $t('treeNoMatch') }}</div></div>
        <template v-for="ch in g.children" :key="ch.key">
        <!-- Second level: database type. Always shown, even for a single type —
             which engine an instance speaks decides what its commands may even
             mean, so it is worth a line rather than being inferred from a name. -->
        <div v-if="!g.flat" class="envsub" @click="toggle(g.key + ':' + ch.key)">
          <component :is="isOpen(g.key + ':' + ch.key) ? ChevronDown : ChevronRight" :size="12" color="var(--text-faint)" />
          {{ ch.label }}<span class="cnt">{{ ch.conns.length }}</span>
        </div>
        <div v-if="g.flat || isOpen(g.key + ':' + ch.key)" class="ind" :class="{ ind1: !g.flat }">
          <template v-for="c in ch.conns" :key="c.id">
            <div class="inst" :class="{ active: c.id === selectedId }" @click="clickInst(c.id)">
              <component :is="instOpen(c.id) ? ChevronDown : ChevronRight" :size="12" color="var(--text-faint)" />
              <Database :size="14" />{{ c.name }}
              <span v-if="tag(c)" class="tag" :class="tag(c)!.cls">{{ $t(tag(c)!.text as any) }}</span>
            </div>
            <!-- live schema (db → tables) for the selected, expanded instance -->
            <div v-if="instOpen(c.id)" class="ind2">
              <div v-if="schemaLoading" class="shint">{{ $t('schemaLoading') }}</div>
              <div v-else-if="schema && schema.error" class="shint err">{{ schema.error }}</div>
              <div v-else-if="schema && !schema.databases.length" class="shint">{{ $t('schemaEmpty') }}</div>
              <template v-else-if="schema" v-for="d in schema.databases" :key="d.name">
                <div class="db" :class="{ sel: d.name === selectedDb }" @click.stop="selectDb(c.id, d.name)">
                  <component :is="isDbOpen(c.id, d.name) ? ChevronDown : ChevronRight" :size="12" color="var(--text-faint)" @click.stop="toggleDb(c.id, d.name)" />
                  <FolderOpen :size="13" />{{ d.name }}
                  <span v-if="d.tables.length" class="tcnt">{{ d.tables.length }}</span>
                </div>
                <div v-if="isDbOpen(c.id, d.name)" class="ind3">
                  <div v-if="dbLoading[dbKey(c.id, d.name)]" class="tbl empty">{{ $t('treeLoading') }}</div>
                  <!-- database → schema → tables (PostgreSQL). Keep the condition and
                       the loop on SEPARATE templates — v-else-if + v-for on one node
                       breaks the if/else chain in Vue 3. -->
                  <template v-else-if="d.schemas && d.schemas.length">
                    <template v-for="sc in d.schemas" :key="sc.name">
                      <div class="sch" @click.stop="toggleSchema(c.id, d.name, sc.name)">
                        <component :is="isSchemaOpen(c.id, d.name, sc.name) ? ChevronDown : ChevronRight" :size="11" color="var(--text-faint)" />
                        <FolderOpen :size="12" />{{ sc.name }}<span class="tcnt">{{ sc.tables.length }}</span>
                      </div>
                      <div v-if="isSchemaOpen(c.id, d.name, sc.name)" class="ind4">
                        <div v-for="tb in sc.tables" :key="tb.name" class="tbl"><Table2 :size="12" color="var(--text-faint)" />{{ tb.name }}</div>
                        <div v-if="!sc.tables.length" class="tbl empty">{{ $t('treeEmptySchema') }}</div>
                        <ObjectGroups :objects="objFor(c.id, d.name, sc.name)"
                                      @open="(ty, nm) => openSource(c.id, sc.name, ty, nm)" />
                      </div>
                    </template>
                  </template>
                  <!-- database → tables (MySQL / SQLite / Oracle-by-owner) -->
                  <template v-else>
                    <div v-for="tb in d.tables" :key="tb.name" class="tbl"><Table2 :size="12" color="var(--text-faint)" />{{ tb.name }}</div>
                    <div v-if="!d.tables.length" class="tbl empty">{{ $t('treeEmptyDb') }}</div>
                    <ObjectGroups :objects="objFor(c.id, d.name)"
                                  @open="(ty, nm) => openSource(c.id, d.name, ty, nm)" />
                  </template>
                </div>
              </template>
            </div>
          </template>
          <div v-if="!ch.conns.length" class="empty">{{ $t('treeNoMatch') }}</div>
        </div>
        </template>
        </template>
      </template>
    </div>
    <div class="foot">{{ $t('treeFooter') }}</div>

    <!-- source viewer: one programmable object's definition, read-only -->
    <div v-if="src.open" class="src-overlay">
      <div class="src-mask" @click="closeSource" />
      <div class="src-modal">
        <div class="src-hdr">
          <div class="src-title">{{ src.name }}</div>
          <span class="src-type">{{ src.type }}</span>
          <div class="src-x" @click="closeSource"><X :size="16" /></div>
        </div>
        <div class="src-body scy">
          <div v-if="src.loading" class="shint">{{ $t('treeLoading') }}</div>
          <div v-else-if="src.err" class="shint err">{{ src.err }}</div>
          <pre v-else>{{ src.text }}</pre>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* min-height/overflow let this grid cell shrink to the row height so .body can
   actually scroll (a grid item defaults to min-height:auto = content height). */
.tree { min-height: 0; overflow: hidden; border-right: 1px solid var(--border-subtle); display: flex; flex-direction: column; background: var(--surface-sunken); }
.head { padding: 16px 14px 10px; }
.eyebrow { display: flex; align-items: center; font: 600 11px var(--font-mono); letter-spacing: 0.14em; color: var(--text-faint); text-transform: uppercase; margin-bottom: 10px; }
.collapse { margin-left: auto; color: var(--text-faint); cursor: pointer; }
.collapse:hover { color: var(--accent-text); }
.gmode { display: flex; gap: 4px; margin-top: 8px; }
.gmode button { flex: 1; padding: 4px 0; border: 1px solid var(--border-subtle); border-radius: 7px;
  background: transparent; color: var(--text-muted); font: 500 11px var(--font-body); cursor: pointer; }
.gmode button.on { background: var(--surface-card); color: var(--text-strong); border-color: var(--accent-text); }
.searchbox {
  display: flex; align-items: center; gap: 8px; height: 34px; padding: 0 11px;
  background: var(--surface-card); border: 1px solid var(--border-subtle); border-radius: 10px;
}
.searchbox input { flex: 1; background: transparent; border: none; outline: none; font: 400 12px var(--font-body); color: var(--text-body); }
.body { flex: 1; min-height: 0; padding: 2px 8px 12px; }
.env { display: flex; align-items: center; gap: 7px; padding: 7px 8px; font: 600 12px var(--font-body); color: var(--text-body); cursor: pointer; }
.env.muted { color: var(--text-muted); margin-top: 4px; }
.cnt { margin-left: auto; font: 600 10px var(--font-mono); color: var(--text-faint); }
.d { width: 7px; height: 7px; border-radius: 50%; }
.d.danger { background: var(--danger); }
.d.info { background: #3b82f6; }
.d.warning { background: var(--warning); }
.d.success { background: var(--success); }
/* A code that no longer resolves to a tier: neutral on purpose. Reusing a
   palette colour would assert a control level nobody can vouch for. */
.d.muted { background: var(--text-faint); }
/* Second level — environments inside a tier. Deliberately quieter than the tier
   row so the top level stays the structure you scan. */
.envsub {
  display: flex; align-items: center; gap: 6px; padding: 5px 8px 5px 14px;
  font: 600 11px var(--font-mono); color: var(--text-faint); cursor: pointer;
}
.envsub:hover { color: var(--text-muted); }
.ind { padding-left: 14px; }
.ind.ind1 { padding-left: 26px; }
.inst {
  display: flex; align-items: center; gap: 8px; padding: 6px 8px; border-radius: 8px;
  font: 600 12px var(--font-mono); color: var(--text-muted); cursor: pointer;
}
.inst:hover { color: var(--text-body); }
.inst.active { background: var(--accent-subtle); border: 1px solid var(--accent-subtle-border); color: var(--accent-text); }
.tag { margin-left: auto; font: 600 10px var(--font-mono); }
.tag.danger { color: var(--danger-text); }
.tag.warn { color: var(--warning-text); }
.ind2 { padding-left: 20px; padding-top: 2px; }
.db { display: flex; align-items: center; gap: 6px; padding: 5px 8px; border-radius: 7px; font: 400 12px var(--font-mono); color: var(--text-muted); cursor: pointer; }
.tcnt { margin-left: auto; font: 600 10px var(--font-mono); color: var(--text-faint); }
.db:hover { color: var(--text-body); background: rgba(255, 255, 255, 0.03); }
.db.sel { background: var(--accent-subtle); color: var(--accent-text); font-weight: 600; }
.ind3 { padding-left: 16px; }
.sch { display: flex; align-items: center; gap: 6px; padding: 4px 8px; border-radius: 7px; font: 400 12px var(--font-mono); color: var(--text-muted); cursor: pointer; }
.sch:hover { color: var(--text-body); background: rgba(255, 255, 255, 0.03); }
.ind4 { padding-left: 16px; }
.tbl { display: flex; align-items: center; gap: 7px; padding: 4px 8px; font: 400 12px var(--font-mono); color: var(--text-muted); }
.tbl.sel { border-radius: 6px; background: rgba(255, 255, 255, 0.04); color: var(--text-strong); }
.empty { padding: 6px 8px; font: 500 11px var(--font-mono); color: var(--text-faint); }
.shint { padding: 5px 8px; font: 500 11px var(--font-mono); color: var(--text-faint); }
.shint.err { color: var(--danger-text); white-space: normal; word-break: break-word; }
.foot { padding: 12px 14px; border-top: 1px solid var(--border-subtle); font: 500 11px var(--font-mono); color: var(--text-faint); }
.src-overlay { position: fixed; inset: 0; z-index: 50; display: flex; align-items: center; justify-content: center; }
.src-mask { position: absolute; inset: 0; background: rgba(0, 0, 0, 0.55); }
.src-modal {
  position: relative; width: min(760px, 92vw); max-height: 78vh; display: flex; flex-direction: column;
  background: var(--surface-card); border: 1px solid var(--border-subtle); border-radius: 12px; overflow: hidden;
}
.src-hdr { display: flex; align-items: center; gap: 10px; padding: 12px 16px; border-bottom: 1px solid var(--border-subtle); }
.src-title { font: 600 13px var(--font-mono); color: var(--text-strong); }
.src-type { font: 600 10px var(--font-mono); color: var(--accent-text); text-transform: uppercase; letter-spacing: 0.08em; }
.src-x { margin-left: auto; color: var(--text-faint); cursor: pointer; display: flex; }
.src-x:hover { color: var(--text-strong); }
.src-body { flex: 1; min-height: 0; overflow: auto; padding: 14px 16px; }
.src-body pre { margin: 0; font: 400 12px/1.6 var(--font-mono); color: var(--text-body); white-space: pre-wrap; word-break: break-word; }
</style>
