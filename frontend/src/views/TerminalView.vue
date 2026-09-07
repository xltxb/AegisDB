<script lang="ts">
// Named so <keep-alive include="TerminalView"> in AppLayout preserves this view
// (its tabs, xterm sessions and sockets) when the user visits another menu.
export default { name: 'TerminalView' }
</script>

<script setup lang="ts">
import { ref, computed, watch, onMounted, onActivated } from 'vue'
import { useUIStore } from '@/stores/ui'
import { X, PanelLeftOpen, Table2 } from 'lucide-vue-next'
import DbTree from '@/components/terminal/DbTree.vue'
import RiskInspector from '@/components/terminal/RiskInspector.vue'
import TerminalSession from '@/components/terminal/TerminalSession.vue'
import ResultGrid from '@/components/terminal/ResultGrid.vue'
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
interface GridResult { columns: string[]; rows: string[][] }
interface Tab { id: number; conn: Connection; db: string; risk: 'idle' | 'safe' | 'high'; wsStatus: WsStatus; result?: GridResult }
const tabs = ref<Tab[]>([])
const treeCollapsed = ref(false) // collapse the left database-tree panel
// HTML result-grid panel: results render in a real table (horizontal scroll,
// select/copy) instead of an ASCII table in the terminal. Persisted per browser.
const gridView = ref(localStorage.getItem('vela_termgrid') === '1')
function toggleGrid() { gridView.value = !gridView.value; localStorage.setItem('vela_termgrid', gridView.value ? '1' : '0') }

// ---- 终端 / 结果表格的上下分割 ----
//
// 表格视图打开后,屏幕要同时放下两样东西:上面是命令和它的回显,下面是结果表。
// 该给谁多少,取决于当下在做什么 —— 核对一列数据时想把表拉大,顺着报错往回翻
// 时想把终端拉大 —— 这不是能替用户定死的比例,所以给一根可拖的分隔条。
//
// 存的是百分比而不是像素:换一块屏幕、或把浏览器窗口拖成另一个高度时,按比例
// 还原仍然合理,按像素还原会在小屏上把终端压没。
const GRID_H_KEY = 'vela_termgrid_h'
const GRID_H_DEFAULT = 42
const GRID_H_MIN = 15
const GRID_H_MAX = 80
const clampH = (v: number) => Math.min(GRID_H_MAX, Math.max(GRID_H_MIN, v))
const gridH = ref(clampH(Number(localStorage.getItem(GRID_H_KEY)) || GRID_H_DEFAULT))
const dragging = ref(false)
const splitEl = ref<HTMLElement>()

function persistGridH() { localStorage.setItem(GRID_H_KEY, String(Math.round(gridH.value))) }

function setGridH(v: number) {
  gridH.value = clampH(v)
  persistGridH()
}

// 指针事件而不是 mousedown/mousemove:同一套代码覆盖鼠标、触控板和触摸屏,
// 并且 setPointerCapture 让指针拖出分隔条之外也照样跟手 —— 拖动时指针几乎
// 一定会跑到条外面去。
function onSplitDown(e: PointerEvent) {
  const host = splitEl.value?.parentElement
  if (!host) return
  dragging.value = true
  ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
  e.preventDefault()
}
function onSplitMove(e: PointerEvent) {
  if (!dragging.value) return
  const host = splitEl.value?.parentElement
  if (!host) return
  const box = host.getBoundingClientRect()
  if (box.height <= 0) return
  // 结果表在下面,所以它的高度是"容器底边减去指针位置"。
  setGridH(((box.bottom - e.clientY) / box.height) * 100)
}
function onSplitUp(e: PointerEvent) {
  if (!dragging.value) return
  dragging.value = false
  ;(e.currentTarget as HTMLElement).releasePointerCapture?.(e.pointerId)
}

// 键盘也能调。分隔条是可聚焦的 separator,方向键一次 2%,Home/End 到两端,
// 双击或回车恢复默认 —— 拖到一个别扭的比例后,不必再用鼠标一点点试回来。
function onSplitKey(e: KeyboardEvent) {
  const step = e.shiftKey ? 8 : 2
  if (e.key === 'ArrowUp') setGridH(gridH.value + step)
  else if (e.key === 'ArrowDown') setGridH(gridH.value - step)
  else if (e.key === 'Home') setGridH(GRID_H_MAX)
  else if (e.key === 'End') setGridH(GRID_H_MIN)
  else if (e.key === 'Enter' || e.key === ' ') setGridH(GRID_H_DEFAULT)
  else return
  e.preventDefault()
}
function setResult(id: number, r: GridResult) { const tb = tabs.value.find((t) => t.id === id); if (tb) tb.result = r }
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
  <div class="grid" :class="{ 'tree-collapsed': treeCollapsed }">
    <div v-if="treeCollapsed" class="treerail" :title="$t('treeExpand')" @click="treeCollapsed = false"><PanelLeftOpen :size="16" /></div>
    <DbTree v-else :connections="conns" :selected-id="activeTab?.conn.id || 0" :selected-db="activeTab?.db" @select="openConn" @select-db="openDb" @collapse="treeCollapsed = true" />

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
        <div class="gridtoggle" :class="{ on: gridView }" :title="$t('gridToggle')" @click="toggleGrid"><Table2 :size="14" />{{ $t('gridToggle') }}</div>
      </div>

      <div v-if="!tabs.length" class="empty">{{ $t('tabEmpty') }}</div>
      <div class="termsplit">
        <TerminalSession
          v-for="tab in tabs" v-show="tab.id === activeId" :key="tab.id"
          :conn="tab.conn" :active="tab.id === activeId" :db="tab.db" :grid-view="gridView"
          :conns="conns" :chain="chain" :script-enabled="scriptEnabled" :script-save-path="scriptSavePath"
          @update:risk="(v) => setRisk(tab.id, v)" @update:ws-status="(v) => setWs(tab.id, v)"
          @update:db="(v) => setDb(tab.id, v)" @result="(r) => setResult(tab.id, r)"
        />
        <template v-if="gridView && tabs.length && activeTab?.result">
          <div
            ref="splitEl"
            class="splitter" :class="{ dragging }"
            role="separator" aria-orientation="horizontal" tabindex="0"
            :aria-valuenow="Math.round(gridH)" :aria-valuemin="GRID_H_MIN" :aria-valuemax="GRID_H_MAX"
            :title="$t('splitHint')"
            @pointerdown="onSplitDown" @pointermove="onSplitMove"
            @pointerup="onSplitUp" @pointercancel="onSplitUp"
            @dblclick="setGridH(GRID_H_DEFAULT)"
            @keydown="onSplitKey"
          ><span class="grip" /></div>
          <ResultGrid
            :columns="activeTab.result.columns" :rows="activeTab.result.rows"
            class="gridpanel" :style="{ flexBasis: gridH + '%' }" @close="toggleGrid"
          />
        </template>
      </div>
    </div>

    <RiskInspector :risk="activeTab?.risk || 'idle'" :risk-commands="riskCommands" :conn="activeTab?.conn || null" :chain="chain" />
  </div>
</template>

<style scoped>
/* minmax(0,1fr) row caps every cell to the grid height so inner panels scroll
   instead of stretching the row to their content. */
/* 三栏:数据库树 · 终端 · 执行上下文。
   列宽交给媒体查询,不再写死 —— 两侧是固定像素,中间那栏拿到的是"剩下的",
   于是窗口一窄,被挤没的永远是终端本身。1100px 宽的窗口里它只剩约 390px:
   命令回显开始逐字断行(`dba` / `_l2`),工具栏按钮被裁掉一半,状态栏叠成两行。
   所以窄下来的时候由两侧依次让位,而不是让主角一直缩。 */
.grid { flex: 1; min-height: 0; display: grid; grid-template-columns: 268px 1fr 340px; grid-template-rows: minmax(0, 1fr); }
.grid.tree-collapsed { grid-template-columns: 30px 1fr 340px; }

/* 第一步:先收窄右侧的执行上下文,它的内容本来就是窄栏排布。 */
@media (max-width: 1500px) {
  .grid { grid-template-columns: 248px 1fr 296px; }
  .grid.tree-collapsed { grid-template-columns: 30px 1fr 296px; }
}
/* 第二步:整块收起执行上下文。它是辅助信息 —— 风险判定的结论终端里照样会打印,
   而终端本身不可替代。让出这 296px,中间那栏差不多翻倍。 */
@media (max-width: 1280px) {
  .grid { grid-template-columns: 248px 1fr; }
  .grid.tree-collapsed { grid-template-columns: 30px 1fr; }
  .grid > :last-child { display: none; }
}
/* 第三步:树也收成导轨,点一下还能展开。到这个宽度,保住终端比同时看见三样东西要紧。 */
@media (max-width: 1040px) {
  .grid { grid-template-columns: 30px 1fr; }
}
.treerail { display: flex; justify-content: center; padding-top: 14px; border-right: 1px solid var(--border-subtle); background: var(--surface-sunken); color: var(--text-muted); cursor: pointer; }
.treerail:hover { color: var(--accent-text); }
.term { display: flex; flex-direction: column; min-width: 0; background: var(--surface-page); }
.tabstrip { display: flex; align-items: stretch; gap: 4px; height: 40px; padding: 6px 10px 0; border-bottom: 1px solid var(--border-subtle); overflow-x: auto; }
.termsplit { flex: 1; min-height: 0; display: flex; flex-direction: column; }
/* 百分比负责比例,像素下限负责"再拖也不会变成一条缝"。两个都要:百分比在矮窗口
   上会退化 —— 15% 在 600px 高的窗口里只有 90px,连表头带一行都放不下。终端一侧
   同理,见下面 .session 的下限。 */
.gridpanel { flex: 0 0 42%; min-height: 120px; }
.termsplit > :deep(.session) { min-height: 140px; }

/* 分隔条。命中区比看得见的线宽,鼠标不用瞄准 —— 一条 1px 的线拖起来是折磨。 */
.splitter {
  position: relative; flex: 0 0 10px; cursor: row-resize;
  display: flex; align-items: center; justify-content: center;
  background: transparent; border: none; padding: 0;
  touch-action: none; /* 触摸时由脚本接管,不要让浏览器先滚页面 */
}
.splitter .grip {
  width: 44px; height: 3px; border-radius: var(--radius-full);
  background: var(--border-default);
  transition: background var(--dur-fast, 0.15s) var(--ease-out, ease),
              width var(--dur-fast, 0.15s) var(--ease-out, ease);
}
.splitter:hover .grip, .splitter.dragging .grip { background: var(--accent-text); width: 64px; }
.splitter:focus-visible { outline: none; }
.splitter:focus-visible .grip { background: var(--accent-text); width: 64px; box-shadow: 0 0 0 3px var(--accent-subtle); }
/* 拖动时整页禁选,否则一拖就把终端里的文字选中一片。 */
.splitter.dragging { cursor: row-resize; }
:global(body:has(.splitter.dragging)) { cursor: row-resize; user-select: none; }
.gridtoggle { display: inline-flex; align-items: center; gap: 6px; margin-left: auto; align-self: center; height: 26px; padding: 0 10px; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-sunken); color: var(--text-muted); font: 600 11px var(--font-mono); cursor: pointer; flex-shrink: 0; white-space: nowrap; }
.gridtoggle:hover { color: var(--text-body); }
.gridtoggle.on { background: var(--accent-subtle); color: var(--accent-text); border-color: var(--accent-text); }
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
