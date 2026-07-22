<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { Search, ChevronDown, ChevronRight, Database, FolderOpen, Table2, PanelLeftClose } from 'lucide-vue-next'
import api from '@/api'
import type { Connection, ConnectionSchema } from '@/types'

const props = defineProps<{ connections: Connection[]; selectedId: number; selectedDb?: string }>()
const emit = defineEmits<{ select: [number]; selectDb: [number, string]; collapse: [] }>()

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
const collapsed = ref<Record<string, boolean>>({ staging: true, dev: true })

// Per-database expand state (collapsed by default so a cluster with many databases
// stays scannable). Keyed by connection:database so switching instances resets it.
const dbOpen = ref<Record<string, boolean>>({})
const dbKey = (cid: number, name: string) => `${cid}:${name}`
const isDbOpen = (cid: number, name: string) => !!dbOpen.value[dbKey(cid, name)]
function toggleDb(cid: number, name: string) {
  const k = dbKey(cid, name)
  dbOpen.value[k] = !dbOpen.value[k]
  if (dbOpen.value[k]) loadDbTables(cid, name)
}
// Clicking a database selects it as the target and expands it to reveal its tables.
function selectDb(cid: number, name: string) {
  dbOpen.value[dbKey(cid, name)] = true
  loadDbTables(cid, name)
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
}

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

const envMeta: Record<string, { label: string; dot: string }> = {
  prod: { label: 'prodEnv', dot: 'danger' },
  staging: { label: 'stagingEnv', dot: 'warning' },
  dev: { label: 'devEnv', dot: 'success' },
}

// risk tag derived from policy
function tag(c: Connection) {
  if (c.policy === 'strict') return { text: 'highTag', cls: 'danger' }
  if (c.policy === 'approve-1') return { text: 'limitedTag', cls: 'warn' }
  return null
}

const grouped = computed(() => {
  const q = search.value.trim().toLowerCase()
  const byEnv: Record<string, Connection[]> = { prod: [], staging: [], dev: [] }
  for (const c of props.connections) {
    if (q && !c.name.toLowerCase().includes(q)) continue
    if (byEnv[c.env]) byEnv[c.env].push(c)
  }
  return byEnv
})

const searching = computed(() => search.value.trim().length > 0)
function toggle(env: string) { collapsed.value[env] = !collapsed.value[env] }
function isOpen(env: string) { return searching.value || !collapsed.value[env] }

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
    </div>
    <div class="scy body">
      <template v-for="env in ['prod', 'staging', 'dev']" :key="env">
        <div class="env" :class="{ muted: env !== 'prod' }" @click="toggle(env)">
          <component :is="isOpen(env) ? ChevronDown : ChevronRight" :size="14" color="var(--text-muted)" />
          <span class="d" :class="envMeta[env].dot" />{{ $t(envMeta[env].label as any) }}
          <span class="cnt">{{ grouped[env].length }}</span>
        </div>
        <div v-if="isOpen(env)" class="ind">
          <template v-for="c in grouped[env]" :key="c.id">
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
                  <div v-if="dbLoading[dbKey(c.id, d.name)]" class="tbl empty">加载中…</div>
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
                        <div v-if="!sc.tables.length" class="tbl empty">— 空 schema —</div>
                      </div>
                    </template>
                  </template>
                  <!-- database → tables (MySQL / SQLite) -->
                  <template v-else>
                    <div v-for="tb in d.tables" :key="tb.name" class="tbl"><Table2 :size="12" color="var(--text-faint)" />{{ tb.name }}</div>
                    <div v-if="!d.tables.length" class="tbl empty">— 空库 —</div>
                  </template>
                </div>
              </template>
            </div>
          </template>
          <div v-if="!grouped[env].length" class="empty">— 无匹配实例 —</div>
        </div>
      </template>
    </div>
    <div class="foot">{{ $t('treeFooter') }}</div>
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
.d.warning { background: var(--warning); }
.d.success { background: var(--success); }
.ind { padding-left: 14px; }
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
</style>
