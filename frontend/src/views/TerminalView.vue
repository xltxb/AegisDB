<script lang="ts">
// Named so <keep-alive include="TerminalView"> in AppLayout preserves this view
// (its tabs, xterm sessions and sockets) when the user visits another menu.
export default { name: 'TerminalView' }
</script>

<script setup lang="ts">
import { ref, computed, watch, onMounted, onActivated } from 'vue'
import { useUIStore } from '@/stores/ui'
import { X } from 'lucide-vue-next'
import DbTree from '@/components/terminal/DbTree.vue'
import RiskInspector from '@/components/terminal/RiskInspector.vue'
import TerminalSession from '@/components/terminal/TerminalSession.vue'
import api from '@/api'
import type { Connection, Member, RiskCommandView } from '@/types'
import type { WsStatus } from '@/lib/wsTerminal'

const ui = useUIStore()

// Shared, fetched once and passed down to every session.
const conns = ref<Connection[]>([])
const riskCommands = ref<RiskCommandView[]>([])
const chain = ref<Member[]>([])
const scriptEnabled = ref(false)
const scriptSavePath = ref('')

// One tab per open connection; each renders an isolated <TerminalSession>.
interface Tab { id: number; conn: Connection; db: string; risk: 'idle' | 'safe' | 'high'; wsStatus: WsStatus }
const tabs = ref<Tab[]>([])
const activeId = ref(0)
let seq = 0

const activeTab = computed(() => tabs.value.find((t) => t.id === activeId.value) || null)
function syncPageSub() {
  const tb = activeTab.value
  ui.pageSub = tb ? `${tb.conn.env}-${tb.conn.name} · ${tb.conn.defaultRole}` : ''
}
watch(activeTab, syncPageSub)
// Refresh the instance list whenever the (kept-alive) view is re-shown, so a
// connection added on the Connections page appears without a full reload.
async function loadConns() {
  try { conns.value = await api.connections() } catch { /* ignore */ }
}
onActivated(() => { syncPageSub(); loadConns() })

// Open (or focus) a tab for a connection. One tab per connection.
function openConn(id: number, db = '') {
  const existing = tabs.value.find((t) => t.conn.id === id)
  if (existing) { if (db) existing.db = db; activeId.value = existing.id; return }
  const cn = conns.value.find((x) => x.id === id)
  if (!cn) return
  const tab: Tab = { id: ++seq, conn: cn, db: db || cn.database || '', risk: 'idle', wsStatus: 'connecting' }
  tabs.value.push(tab)
  activeId.value = tab.id
}
// Selecting a database in the tree opens/focuses its instance tab on that db.
function openDb(id: number, db: string) { openConn(id, db) }

function closeTab(id: number) {
  const idx = tabs.value.findIndex((t) => t.id === id)
  if (idx === -1) return
  tabs.value.splice(idx, 1)
  if (activeId.value === id) {
    const next = tabs.value[idx] || tabs.value[idx - 1] || null
    activeId.value = next ? next.id : 0
  }
}

function setRisk(id: number, v: 'idle' | 'safe' | 'high') {
  const tb = tabs.value.find((t) => t.id === id)
  if (tb) tb.risk = v
}
function setWs(id: number, v: WsStatus) {
  const tb = tabs.value.find((t) => t.id === id)
  if (tb) tb.wsStatus = v
}
// A `use <db>` in the terminal switches its target database — mirror it onto the
// tab so the tree highlights the new database.
function setDb(id: number, db: string) {
  const tb = tabs.value.find((t) => t.id === id)
  if (tb) tb.db = db
}

onMounted(async () => {
  // Fetch independently: a failure of one (e.g. a non-admin lacking access to
  // the risk dictionary) must NOT prevent the connection list from loading.
  await loadConns()
  try { riskCommands.value = await api.riskCommands() } catch { /* optional reference data */ }
  try { chain.value = (await api.approvalChain()).chain } catch { /* ignore */ }
  try { const sc = await api.scriptConfig(); scriptEnabled.value = sc.enabled; scriptSavePath.value = sc.savePath } catch { /* ignore */ }
  const first = conns.value.find((c) => c.env === 'prod') ?? conns.value[0]
  if (first) openConn(first.id)
})
</script>

<template>
  <div class="grid">
    <DbTree :connections="conns" :selected-id="activeTab?.conn.id || 0" :selected-db="activeTab?.db" @select="openConn" @select-db="openDb" />

    <div class="term">
      <!-- tab strip: one chip per open database, isolated sessions behind each -->
      <div class="tabstrip">
        <div
          v-for="tab in tabs" :key="tab.id"
          class="tab" :class="{ active: tab.id === activeId }"
          @click="activeId = tab.id"
        >
          <span class="dot" :class="{ off: tab.wsStatus !== 'open' }" />
          <span class="tname">{{ tab.conn.name }}</span>
          <span class="close" :title="$t('tabClose')" @click.stop="closeTab(tab.id)"><X :size="12" /></span>
        </div>
      </div>

      <div v-if="!tabs.length" class="empty">{{ $t('tabEmpty') }}</div>
      <TerminalSession
        v-for="tab in tabs" v-show="tab.id === activeId" :key="tab.id"
        :conn="tab.conn" :active="tab.id === activeId" :db="tab.db"
        :conns="conns" :chain="chain" :script-enabled="scriptEnabled" :script-save-path="scriptSavePath"
        @update:risk="(v) => setRisk(tab.id, v)" @update:ws-status="(v) => setWs(tab.id, v)"
        @update:db="(v) => setDb(tab.id, v)"
      />
    </div>

    <RiskInspector :risk="activeTab?.risk || 'idle'" :risk-commands="riskCommands" :conn="activeTab?.conn || null" :chain="chain" />
  </div>
</template>

<style scoped>
/* minmax(0,1fr) row caps every cell to the grid height so inner panels scroll
   instead of stretching the row to their content. */
.grid { flex: 1; min-height: 0; display: grid; grid-template-columns: 268px 1fr 340px; grid-template-rows: minmax(0, 1fr); }
.term { display: flex; flex-direction: column; min-width: 0; background: var(--surface-page); }
.tabstrip { display: flex; align-items: stretch; gap: 4px; height: 40px; padding: 6px 10px 0; border-bottom: 1px solid var(--border-subtle); overflow-x: auto; }
.tab {
  display: flex; align-items: center; gap: 8px; height: 34px; padding: 0 10px 0 12px; border-radius: 9px 9px 0 0;
  background: var(--surface-sunken); border: 1px solid var(--border-subtle); border-bottom: none;
  font: 500 12px var(--font-mono); color: var(--text-muted); cursor: pointer; white-space: nowrap;
  transition: color var(--dur-fast) var(--ease-out), background var(--dur-fast) var(--ease-out);
}
.tab:hover { color: var(--text-body); }
.tab.active { background: var(--surface-page); color: var(--text-strong); border-color: var(--border-default); }
.dot { width: 6px; height: 6px; border-radius: 50%; background: var(--success); flex-shrink: 0; }
.dot.off { background: var(--warning); }
.tname { max-width: 160px; overflow: hidden; text-overflow: ellipsis; }
.close { display: flex; align-items: center; justify-content: center; width: 17px; height: 17px; border-radius: 5px; color: var(--text-faint); }
.close:hover { background: var(--danger-subtle); color: var(--danger-text); }
.empty { flex: 1; display: flex; align-items: center; justify-content: center; font: 500 13px var(--font-mono); color: var(--text-faint); }
</style>
