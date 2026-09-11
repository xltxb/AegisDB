<script lang="ts">
// Named so <keep-alive include="TerminalView"> in AppLayout preserves this view
// (its tabs, xterm sessions and sockets) when the user visits another menu.
export default { name: 'TerminalView' }
</script>

<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted, onActivated } from 'vue'
import { useUIStore } from '@/stores/ui'
import {
  X, PanelLeftOpen, PanelRightOpen, Table2, Maximize2, Minimize2, Activity, ShieldAlert, LogOut,
} from 'lucide-vue-next'
import { useEnvTierStore } from '@/stores/envtier'
import DbTree from '@/components/terminal/DbTree.vue'
import RiskInspector from '@/components/terminal/RiskInspector.vue'
import TerminalSession from '@/components/terminal/TerminalSession.vue'
import ResultGrid from '@/components/terminal/ResultGrid.vue'
import api from '@/api'
import type { Connection, Member, RiskCommandView } from '@/types'
import type { WsStatus } from '@/lib/wsTerminal'

const ui = useUIStore()
const envtier = useEnvTierStore()

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
// 右侧执行上下文**默认收起**。它是辅助信息(风险判定的结论终端里照样会打印),而
// 终端本身不可替代 —— 要对着一屏宽结果核数据时,这 340px 让出来最值。
//
// 判的是 !== '0' 而不是 === '1':没存过 = 收起(新的默认),而**明确展开过**的人
// 存的是 '0',那份选择继续算数。把默认写成"没存过就展开"才会把人的选择吃掉。
const inspCollapsed = ref(localStorage.getItem('vela_insp_collapsed') !== '0')
function toggleInsp() {
  inspCollapsed.value = !inspCollapsed.value
  localStorage.setItem('vela_insp_collapsed', inspCollapsed.value ? '1' : '0')
}

// ---- 两侧边栏的宽度:可拖 ----
//
// 默认宽度由下面的媒体查询给,那套逐级让位的规则(先收窄执行上下文、再整块收起、
// 最后连树也收成导轨)是为了"窗口一窄,被挤没的不能是终端本身"。拖动不推翻它,
// 只是把某一侧的宽度换成人自己定的那个值:CSS 里每一处都写成 var(--tree-w, 268px),
// 没拖过就走默认,拖过了就在每个断点上都听人的。
//
// 存像素而不是百分比 —— 这跟终端/结果表那根横向分隔条相反,是故意的:树和执行上下文
// 装的是**定宽的东西**(实例名、字段标签),它们需要的宽度不随窗口变;而结果表要占
// 的是"剩下的一半",所以那边存比例。
const TREE_W_KEY = 'vela_tree_w'
const INSP_W_KEY = 'vela_insp_w'
const TREE_MIN = 180
const TREE_MAX = 560
const INSP_MIN = 240
const INSP_MAX = 620
// 中间那栏的下限。拖动可以把两侧拉宽,但不能把终端挤到开始逐字断行 —— 这正是
// 媒体查询那一串在防的事,手动拖动没有理由成为它的后门。
const TERM_MIN = 420

const readW = (k: string) => {
  const v = Number(localStorage.getItem(k))
  return Number.isFinite(v) && v > 0 ? v : null
}
const treeW = ref<number | null>(readW(TREE_W_KEY))
const inspW = ref<number | null>(readW(INSP_W_KEY))
const gridEl = ref<HTMLElement>()

// 只有拖过的那一侧才写变量;没写的那侧留给 CSS 默认值(含媒体查询)。
const gridVars = computed(() => {
  const s: Record<string, string> = {}
  if (treeW.value) s['--tree-w'] = `${treeW.value}px`
  if (inspW.value) s['--insp-w'] = `${inspW.value}px`
  return s
})

type Side = 'tree' | 'insp'
const resizing = ref<Side | null>(null)

/** 拖动时那一侧的上限:先受自身上限约束,再受"中间必须留够"约束。 */
function maxFor(side: Side): number {
  const el = gridEl.value
  const hard = side === 'tree' ? TREE_MAX : INSP_MAX
  if (!el) return hard
  // 另一侧此刻**实际渲染**的宽度,直接从解析后的列宽读,不去猜它落在哪个断点上。
  const cols = getComputedStyle(el).gridTemplateColumns.split(' ').map(parseFloat)
  const other = side === 'tree' ? (cols[2] ?? 0) : (cols[0] ?? 0)
  return Math.min(hard, el.getBoundingClientRect().width - other - TERM_MIN)
}

function setW(side: Side, px: number) {
  const min = side === 'tree' ? TREE_MIN : INSP_MIN
  const v = Math.round(Math.min(maxFor(side), Math.max(min, px)))
  if (side === 'tree') { treeW.value = v; localStorage.setItem(TREE_W_KEY, String(v)) }
  else { inspW.value = v; localStorage.setItem(INSP_W_KEY, String(v)) }
}

/** 双击 / 回车:还原成默认宽度,也就是把这一侧交还给媒体查询。 */
function resetW(side: Side) {
  if (side === 'tree') { treeW.value = null; localStorage.removeItem(TREE_W_KEY) }
  else { inspW.value = null; localStorage.removeItem(INSP_W_KEY) }
}

// 指针事件 + setPointerCapture,跟结果表那根分隔条同一套:一套代码覆盖鼠标、
// 触控板和触摸屏,并且指针拖出把手之外仍然跟手 —— 拖动时它几乎一定会跑出去。
function onEdgeDown(side: Side, e: PointerEvent) {
  resizing.value = side
  ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
  e.preventDefault()
}
function onEdgeMove(side: Side, e: PointerEvent) {
  if (resizing.value !== side || !gridEl.value) return
  const box = gridEl.value.getBoundingClientRect()
  setW(side, side === 'tree' ? e.clientX - box.left : box.right - e.clientX)
}
function onEdgeUp(e: PointerEvent) {
  if (!resizing.value) return
  resizing.value = null
  ;(e.currentTarget as HTMLElement).releasePointerCapture?.(e.pointerId)
}

// 键盘也能调:把手是可聚焦的 separator。没拖过时先从当前实际宽度起步,
// 否则第一次按键会把面板跳到某个凭空的数字上。
function onEdgeKey(side: Side, e: KeyboardEvent) {
  const step = e.shiftKey ? 32 : 8
  const cur = side === 'tree' ? treeW.value : inspW.value
  let base = cur
  if (base == null && gridEl.value) {
    const cols = getComputedStyle(gridEl.value).gridTemplateColumns.split(' ').map(parseFloat)
    base = side === 'tree' ? cols[0] : cols[2]
  }
  if (base == null || !Number.isFinite(base)) return
  const grow = side === 'tree' ? 'ArrowRight' : 'ArrowLeft'
  const shrink = side === 'tree' ? 'ArrowLeft' : 'ArrowRight'
  if (e.key === grow) setW(side, base + step)
  else if (e.key === shrink) setW(side, base - step)
  else if (e.key === 'Enter' || e.key === ' ') resetW(side)
  else return
  e.preventDefault()
}

// 窗口变窄后,拖出来的宽度可能已经把终端挤过了下限 —— 重新夹一次。存着的值不动:
// 窗口再拉回来时,人自己设的那个宽度应该回来。
function reclamp() {
  if (treeW.value) treeW.value = Math.round(Math.min(treeW.value, Math.max(TREE_MIN, maxFor('tree'))))
  if (inspW.value) inspW.value = Math.round(Math.min(inspW.value, Math.max(INSP_MIN, maxFor('insp'))))
}
onMounted(() => window.addEventListener('resize', reclamp))
onUnmounted(() => window.removeEventListener('resize', reclamp))

// 沉浸模式:把左右两栏一起收掉,只留终端。不是浏览器全屏 —— 那会把顶栏和导航
// 一起吞掉,而人还需要知道自己在哪个系统里。
const zen = ref(false)
function toggleZen() { zen.value = !zen.value }
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
// 当前会话的安全等级。触发条件是**分层的 dangerBanner**,不是名字叫不叫 prod ——
// 第二套生产环境和第一套一样危险,而按名字挑会让人最不熟悉的那些集群拿到最弱的
// 警告。这和终端里那条红线、进入实例时那个弹窗用的是同一个判据。
const safety = computed(() => {
  const cn = activeTab.value?.conn
  if (!cn) return null
  const tier = envtier.tierOf(cn.env)
  // 解析不出分层的按中档处理:没有分层意味着这台实例的管控级别**未知**,不是无害。
  const level = tier?.dangerBanner ? 'danger' : (!tier || tier.requireMfa) ? 'warn' : 'ok'
  return { level, env: cn.env.toUpperCase(), name: cn.name, role: cn.defaultRole, policy: cn.policy }
})

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
  // 默认选中哪台实例要看分层的 dangerBanner,所以分层得先到手 —— 拉不到就退回
  // 列表里的第一台,而不是把整页卡住。
  await envtier.load().catch(() => {})
  try { riskCommands.value = await api.riskCommands() } catch { /* optional reference data */ }
  try { chain.value = (await api.approvalChain()).chain } catch { /* ignore */ }
  try { const sc = await api.scriptConfig(); scriptEnabled.value = sc.enabled; scriptSavePath.value = sc.savePath } catch { /* ignore */ }
  const first = conns.value.find((c) => envtier.tierOf(c.env)?.dangerBanner) ?? conns.value[0]
  if (first) openConn(first.id)
})
</script>

<template>
  <div ref="gridEl" class="grid" :class="{ 'tree-collapsed': treeCollapsed, 'insp-collapsed': inspCollapsed, zen, resizing }" :style="gridVars">
    <div v-if="treeCollapsed || zen" class="rail left" :title="$t('treeExpand')" @click="treeCollapsed = false; zen = false"><PanelLeftOpen :size="16" /></div>
    <DbTree v-else :connections="conns" :selected-id="activeTab?.conn.id || 0" :selected-db="activeTab?.db" @select="openConn" @select-db="openDb" @collapse="treeCollapsed = true" />

    <div class="term">
      <!-- 两根竖把手。它们贴在终端栏的左右内边缘,而不是自己占一列:多一列就要在
           上面每一条媒体查询里多写一个数,而那串规则本来就是这个视图里最容易写歪的
           地方。收起的那一侧不渲染把手 —— 30px 的导轨没有宽度可调。 -->
      <div
        v-if="!treeCollapsed && !zen" class="edge left" role="separator" aria-orientation="vertical"
        tabindex="0" :title="$t('resizePanel')"
        @pointerdown="onEdgeDown('tree', $event)" @pointermove="onEdgeMove('tree', $event)"
        @pointerup="onEdgeUp" @pointercancel="onEdgeUp"
        @dblclick="resetW('tree')" @keydown="onEdgeKey('tree', $event)"
      ><span class="grip" /></div>
      <div
        v-if="!inspCollapsed && !zen" class="edge right" role="separator" aria-orientation="vertical"
        tabindex="0" :title="$t('resizePanel')"
        @pointerdown="onEdgeDown('insp', $event)" @pointermove="onEdgeMove('insp', $event)"
        @pointerup="onEdgeUp" @pointercancel="onEdgeUp"
        @dblclick="resetW('insp')" @keydown="onEdgeKey('insp', $event)"
      ><span class="grip" /></div>
      <!-- 第一行:标签页。一个标签 = 一个会话,标题写清"哪台实例的哪个库" —— 只写
           实例名时,同一台上开两个库的两个标签长得一模一样。 -->
      <div class="tabstrip">
        <div
          v-for="tab in tabs" :key="tab.id"
          class="tab" :class="{ active: tab.id === activeId }"
          @click="activeId = tab.id"
        >
          <span class="dot" :class="{ off: tab.wsStatus !== 'open' }" />
          <span class="tname">{{ tab.conn.name }}<span v-if="tab.db" class="tdb">: {{ tab.db }}</span></span>
          <span class="close" :title="$t('tabClose')" @click.stop="closeTab(tab.id)"><X :size="12" /></span>
        </div>
        <div class="tabspacer" />
        <!-- 右侧是**这个会话**的连接状态。网关整体的 p50 延迟顶栏上已经有了,
             在这里再放一份是同一个数字说两遍。 -->
        <span v-if="activeTab" class="wschip" :class="activeTab.wsStatus">
          <Activity :size="12" />{{ $t('wsStatus_' + activeTab.wsStatus) }}
        </span>
        <button class="tbtn" :class="{ on: gridView }" :title="$t('gridToggle')" @click="toggleGrid"><Table2 :size="14" />{{ $t('gridToggle') }}</button>
        <button class="tbtn" :title="zen ? $t('zenExit') : $t('zenEnter')" @click="toggleZen">
          <component :is="zen ? Minimize2 : Maximize2" :size="14" />
        </button>
      </div>

      <!-- 生产安全横幅。它取代了原先打在终端里的那一行红字 —— 那行字会被滚屏
           顶走,而"你正在生产库上"这件事不该只在会话开头说一次。 -->
      <div v-if="safety && safety.level !== 'ok'" class="safety" :class="safety.level">
        <ShieldAlert :size="15" class="sbi" />
        <span class="sbtxt">
          <b>{{ safety.level === 'danger' ? $t('bannerProd') : $t('bannerCaution') }}</b>
          <span class="sbsep">·</span>{{ $t('bannerInst') }} <code>{{ safety.env }} / {{ safety.name }}</code>
          <span class="sbsep">·</span>{{ $t('bannerRole') }} <code>{{ safety.role }}</code>
          <span class="sbsep">·</span>{{ $t('bannerAudit') }}
        </span>
        <button class="sbbtn" :title="$t('bannerEnd')" @click="activeTab && closeTab(activeTab.id)">
          <LogOut :size="13" />{{ $t('bannerEnd') }}
        </button>
      </div>

      <div v-if="!tabs.length" class="empty">{{ $t('tabEmpty') }}</div>
      <div class="termsplit">
        <TerminalSession
          v-for="tab in tabs" v-show="tab.id === activeId" :key="tab.id"
          :conn="tab.conn" :active="tab.id === activeId" :db="tab.db" :grid-view="gridView"
          :conns="conns" :chain="chain" :script-enabled="scriptEnabled" :script-save-path="scriptSavePath"
          @update:risk="(v) => setRisk(tab.id, v)" @update:ws-status="(v) => setWs(tab.id, v)"
          @update:db="(v) => setDb(tab.id, v)" @result="(r) => setResult(tab.id, r)"
          @close="closeTab(tab.id)"
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

    <div v-if="inspCollapsed && !zen" class="rail right" :title="$t('ctxExpand')" @click="toggleInsp"><PanelRightOpen :size="16" /></div>
    <RiskInspector
      v-else-if="!zen"
      :risk="activeTab?.risk || 'idle'" :risk-commands="riskCommands"
      :conn="activeTab?.conn || null" :chain="chain" @collapse="toggleInsp"
    />
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
/* 每一处列宽都写成 var(--x, 默认):没拖过就是下面这套逐级让位的默认值,拖过了
   就在每个断点上都听人的。变量只在拖过的那一侧由内联样式给出。 */
.grid { flex: 1; min-height: 0; display: grid; grid-template-columns: var(--tree-w, 268px) 1fr var(--insp-w, 340px); grid-template-rows: minmax(0, 1fr); }
.grid.tree-collapsed { grid-template-columns: 30px 1fr var(--insp-w, 340px); }
.grid.insp-collapsed { grid-template-columns: var(--tree-w, 268px) 1fr 30px; }
.grid.tree-collapsed.insp-collapsed { grid-template-columns: 30px 1fr 30px; }
/* 拖动时别让指针划过终端就选中里面的文字。 */
.grid.resizing { cursor: col-resize; user-select: none; }
/* 沉浸模式:两侧一起收成导轨,只留终端。 */
.grid.zen { grid-template-columns: 30px 1fr !important; }

/* 第一步:先收窄右侧的执行上下文,它的内容本来就是窄栏排布。 */
@media (max-width: 1500px) {
  .grid { grid-template-columns: var(--tree-w, 248px) 1fr var(--insp-w, 296px); }
  .grid.tree-collapsed { grid-template-columns: 30px 1fr var(--insp-w, 296px); }
  .grid.insp-collapsed { grid-template-columns: var(--tree-w, 248px) 1fr 30px; }
  .grid.tree-collapsed.insp-collapsed { grid-template-columns: 30px 1fr 30px; }
}
/* 第二步:整块收起执行上下文。它是辅助信息 —— 风险判定的结论终端里照样会打印,
   而终端本身不可替代。让出这 296px,中间那栏差不多翻倍。 */
@media (max-width: 1280px) {
  .grid { grid-template-columns: var(--tree-w, 248px) 1fr; }
  .grid.tree-collapsed { grid-template-columns: 30px 1fr; }
  .grid > :last-child { display: none; }
  /* 执行上下文这一档整块没了,右边那根把手也就没有东西可调。 */
  .edge.right { display: none; }
}
/* 第三步:树也收成导轨,点一下还能展开。到这个宽度,保住终端比同时看见三样东西要紧。 */
@media (max-width: 1040px) {
  .grid { grid-template-columns: 30px 1fr; }
  .edge.left { display: none; }
}
/* 收起后的导轨:只留一个能点回来的图标。左右两侧共用一套样式,只是边框在哪一侧不同。 */
.rail { display: flex; justify-content: center; padding-top: 14px; background: var(--surface-sunken); color: var(--text-muted); cursor: pointer; }
.rail:hover { color: var(--accent-text); }
.rail.left { border-right: 1px solid var(--border-subtle); }
.rail.right { border-left: 1px solid var(--border-subtle); }
.term { position: relative; display: flex; flex-direction: column; min-width: 0; background: var(--surface-page); }
/* 把手:平时只是一条看不见的 6px 热区,悬停/聚焦时才显出那道竖线 —— 一条常驻的
   竖线会在这一屏上多出两条与内容无关的分隔,而边界本来就已经有边框了。 */
.edge {
  position: absolute; top: 0; bottom: 0; width: 6px; z-index: 5;
  display: flex; align-items: center; justify-content: center;
  cursor: col-resize; background: transparent; border: none; padding: 0;
}
.edge.left { left: 0; }
.edge.right { right: 0; }
.edge .grip { width: 2px; height: 40px; border-radius: 2px; background: transparent; }
.edge:hover .grip, .edge:focus-visible .grip { background: var(--accent-text); }
.edge:focus-visible { outline: none; }
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
.tabspacer { flex: 1; min-width: 8px; }
.tbtn { display: inline-flex; align-items: center; gap: 6px; align-self: center; height: 26px; padding: 0 10px; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-sunken); color: var(--text-muted); font: 600 11px var(--font-mono); cursor: pointer; flex-shrink: 0; white-space: nowrap; }
.tbtn:hover { color: var(--text-body); }
.tbtn.on { background: var(--accent-subtle); color: var(--accent-text); border-color: var(--accent-text); }
/* 这个会话此刻连没连上。整站的网关延迟在顶栏,这里说的是**这一条 socket**。 */
.wschip { display: inline-flex; align-items: center; gap: 5px; align-self: center; height: 26px; padding: 0 9px; border-radius: 8px; font: 600 10.5px var(--font-mono); flex-shrink: 0; white-space: nowrap; }
.wschip.open { background: var(--success-subtle); color: var(--success-text); }
.wschip.connecting { background: var(--warning-subtle); color: var(--warning-text); }
.wschip.closed { background: var(--danger-subtle); color: var(--danger-text); }

/* 生产安全横幅:常驻在终端上方,不会被滚屏顶走。 */
.safety { display: flex; align-items: center; gap: 10px; padding: 8px 14px; border-bottom: 1px solid; font: 500 11.5px var(--font-body); }
.safety.danger { background: var(--danger-subtle); border-color: var(--danger); color: var(--danger-text); }
.safety.warn { background: var(--warning-subtle); border-color: var(--warning); color: var(--warning-text); }
.safety .sbi { flex-shrink: 0; }
.sbtxt { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.sbtxt b { font-weight: 700; }
.sbtxt code { font: 600 11px var(--font-mono); }
.sbsep { margin: 0 6px; opacity: .5; }
.sbbtn { flex-shrink: 0; display: inline-flex; align-items: center; gap: 5px; height: 24px; padding: 0 9px; border: 1px solid currentColor; border-radius: 7px; background: transparent; color: inherit; font: 600 10.5px var(--font-body); cursor: pointer; }
.sbbtn:hover { background: rgba(255, 255, 255, .35); }
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
.tname { max-width: 220px; overflow: hidden; text-overflow: ellipsis; }
.tdb { color: var(--text-faint); font-weight: 400; }
.tab.active .tdb { color: var(--text-muted); }
.close { display: flex; align-items: center; justify-content: center; width: 17px; height: 17px; border-radius: 5px; color: var(--text-faint); }
.close:hover { background: var(--danger-subtle); color: var(--danger-text); }
.empty { flex: 1; display: flex; align-items: center; justify-content: center; font: 500 13px var(--font-mono); color: var(--text-faint); }
</style>
