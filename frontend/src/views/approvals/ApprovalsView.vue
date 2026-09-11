<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Inbox, GitPullRequestArrow, CircleCheckBig, CircleX, X, Play, Search, RotateCw, ChevronLeft, ChevronRight, Undo2 } from 'lucide-vue-next'
import VSelect from '@/components/common/VSelect.vue'
import VButton from '@/components/common/VButton.vue'
import api from '@/api'
import { confirmAction } from '@/lib/confirm'
import { initialsOf } from '@/lib/initials'
import { extractTables, tablesLabel } from '@/lib/sqlTables'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import type { Approval } from '@/types'

const ui = useUIStore()
const auth = useAuthStore()
const route = useRoute()
const router = useRouter()
const { t } = useI18n()

// The row shows a one-line summary; the full command, reason, chain and result
// live in a detail card. A command can be a whole migration script, so putting
// it in the row would either truncate it or wreck the list.
const detail = ref<Approval | null>(null)
function openDetail(a: Approval) { detail.value = a }
function closeDetail() {
  detail.value = null
  // Drop the deep-link parameter so a later reload doesn't reopen the card.
  if (route.query.ap) router.replace({ name: 'approvals', query: {} })
}
const scope = ref<'mine' | 'all'>('all')
const list = ref<Approval[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const pageSizes = [20, 50, 100]
const pages = computed(() => Math.max(1, Math.ceil(total.value / pageSize.value)))

// 搜索与状态筛选都在**服务端**做(见 api.approvals 的注释)。列表是分页的,只筛
// 当前页的搜索框会对一张躺在第三页的工单回答"没有" —— 而人会据此认为它不存在。
const q = ref('')
const statusFilter = ref('')
// 状态筛选用的是服务端**真实存在**的四个状态。界面上还有一个"待执行",但那是由
// status + executedAt 合成出来的显示态,库里没有这一列 —— 拿它当筛选条件,分页
// 的总数就会和筛出来的行对不上。
const statusOpts = computed(() => [
  { v: '', label: t('apFilterAll') },
  { v: 'pending', label: t('apPending') },
  { v: 'approved', label: t('apStDone') },
  { v: 'rejected', label: t('apStRejected') },
  { v: 'expired', label: t('apStExpired') },
  // cancelled 是这一版新增的真实状态(发起人撤回)。不列进来的话,撤回过的单子在
  // "全部"里看得见、却没有任何一个筛选能单独找出来 —— 而"我上周撤了哪几张"正是
  // 事后最常问的一句。
  { v: 'cancelled', label: t('apStCancelled') },
])
const statusLabel = computed({
  get: () => statusOpts.value.find((o) => o.v === statusFilter.value)?.label || '',
  set: (l: string) => {
    const hit = statusOpts.value.find((o) => o.label === l)
    if (hit) { statusFilter.value = hit.v; page.value = 1; load() }
  },
})
const statusLabels = computed(() => statusOpts.value.map((o) => o.label))

// 输入时不要每敲一个字就打一次请求 —— 但也不能等失焦,那样人会以为搜索没反应。
let searchTimer: ReturnType<typeof setTimeout> | null = null
function onSearch() {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => { page.value = 1; load() }, 300)
}
function clearSearch() { q.value = ''; page.value = 1; load() }

const refreshing = ref(false)
async function refresh() {
  refreshing.value = true
  try { await load() } finally { refreshing.value = false }
}
function setPageSize(n: number) { pageSize.value = n; page.value = 1; load() }

const pending = computed(() => list.value.filter((a) => a.status === 'pending').length)

async function load() {
  // M14: 加载失败以 toast 呈现，避免静默失败
  try {
    const res = await api.approvals(scope.value, page.value, pageSize.value, statusFilter.value, q.value.trim())
    list.value = res.items
    total.value = res.total
    // Server-side count: the badge must not be limited to the current page.
    auth.pendingCount = res.pending
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  }
}
onMounted(async () => {
  await load()
  await openFromQuery()
})

// The audit log links a ticket by number (?ap=AP-1234). Such a ticket may not be
// in the current tab — "mine" only lists what awaits this user — so widen to all
// before giving up, and say so plainly if it still cannot be found rather than
// leaving the click looking broken.
async function openFromQuery() {
  const ap = String(route.query.ap || '')
  if (!ap) return
  // Ask the server for this one ticket. Searching the loaded page would only
  // work while it happened to be on it — with paging that is mostly false, and
  // the link would appear broken exactly as it did before paging existed.
  try {
    const hit = await api.approvalByNo(ap)
    if (hit) openDetail(hit)
    else ui.notify(t('apNotFound', { ap }), 'info')
  } catch {
    ui.notify(t('apNotFound', { ap }), 'info')
  }
}

async function setScope(s: 'mine' | 'all') {
  scope.value = s
  page.value = 1 // a scope change invalidates the current position
  await load()
}

async function goto(p: number) {
  if (p < 1 || p > pages.value || p === page.value) return
  page.value = p
  await load()
}

// 谁能决定这张单,由服务端说了算(service.DecideBlockFor)。这里**不再自己判
// 一遍**:此前前端只看审批链成员,而服务端还有"不能审自己发起的"这一条 ——
// 两份判断不一致的结果,就是一个亮着却点不动的按钮。
function canDecide(a: Approval) {
  return a.canDecide === true
}
async function decide(a: Approval, approve: boolean) {
  if (!canDecide(a)) return
  // B3: 两个方向都不可逆(通过会解锁一次真实执行,驳回会关掉这张单)——二次确认防误点
  const verb = approve ? t('apApprove') : t('apReject')
  if (!confirmAction(t('apConfirm', { verb, cmd: a.command }))) return
  // M14: 审批/驳回失败以 toast 呈现。api 层经 ok() 把业务级拒绝抛成 Error(msg),
  // 所以服务端说的理由会原样出现在 toast 上 —— 此前这里只 catch 抛出的异常,而
  // 拒绝是正常返回的信封,于是点了没反应,也没人说得出为什么。
  try {
    if (approve) await api.approve(a.id)
    else await api.reject(a.id)
    await load()
    // Reflect the new state in the open card instead of leaving a stale one.
    if (detail.value) detail.value = list.value.find((x) => x.id === a.id) || null
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
    // 被拒的常见原因之一是"已被别人处理过",刷一次比让人对着过期卡片发呆好。
    await load()
  }
}

// 执行是**发起人**的动作,不是审批的副作用。
//
// 审批人按下的是"我同意",不是"现在就跑" —— 业务低峰、应用是否已停、备份是否
// 就绪,只有发起人知道。所以这里是一个独立的按钮,而且它只对该去按的人亮:
// canExecute 由服务端算(service.CanExecuteApproved),前端照着它走,免得出现
// 一个亮着却点不动的按钮。
const running = ref(false)
// 行内状态比 a.status 多分了一档:approved 里"还没跑"和"跑过了"是两回事,而
// 前者往往正等着看这一列的人去处理。挤在一个"已通过"里,他就看不见了。
function rowState(a: Approval) {
  // 升级单和**窗口申请单**都不走手动执行:前者归流水线的执行阶段,后者批准即生效
  // (它的 Command 是一句描述,不是可执行语句)。把它们显示成"待执行",人会一直在
  // 等一个永远不该按的按钮。
  if (a.status === 'approved' && !a.executedAt && !a.releaseId && !a.windowId) return 'waitrun'
  // 最终状态以**库那边收没收下**为准。批准是人的决定,而这一列说的是那次下发的
  // 结果 —— 一条跑挂的 DROP 显示成"已通过",读的人会以为变更已经生效了。
  //
  // 这里不动 a.status:跑挂了不该把一张已批准的工单变回没批准,能不能执行、按状态
  // 筛选走的都是它。行内状态一直是这样合成出来的(waitrun 也是),多这一档而已。
  if (a.status === 'approved' && a.execStatus === 'failed') return 'failed'
  return a.status
}
/**
 * 撤回自己发起的待审批工单。
 *
 * 和 decide() 分开写,因为它不是一次审批决定 —— 界面上也不该让它长得像。确认文案
 * 里写清"撤回后这条命令不会被执行",因为发起人此刻要判断的正是这件事。
 */
async function cancelAp(a: Approval) {
  if (!confirmAction(t('apCancelConfirm', { no: a.apNo }))) return
  running.value = true
  try {
    await api.cancelApproval(a.id)
    ui.notify(t('apCancelled', { no: a.apNo }), 'success')
    await load()
    if (detail.value) detail.value = list.value.find((x) => x.id === a.id) || null
  } catch (e) {
    // 服务端说的理由原样出现在 toast 上 —— "去驳回它"与"已被处理过"要人做的事不同。
    ui.notifyError(e, t('actionFailed'))
    await load()
  } finally { running.value = false }
}

function rowLabel(a: Approval) {
  switch (rowState(a)) {
    case 'pending': return 'apPending'
    case 'waitrun': return 'apStWaitRun'
    case 'failed': return 'apStFailed'
    // 历史工单没有 execStatus(这一列是后加的),它们的成败无从得知 —— 照旧只说
    // "已执行",不替它们编一个结果。
    case 'approved': return a.executedAt ? 'apStRan' : 'apStDone'
    // 已失效 ≠ 已驳回。前者是超时没人管、被清扫自动作废的(见 sweepStaleApprovals),
    // 后者是有人看过并且说了不行。原先它们共用 default 分支,于是一张过期单在列表上
    // 显示成"已驳回" —— 而按状态筛"已驳回"又筛不到它,同一行的两处说法自相矛盾。
    case 'expired': return 'apStExpired'
    // 已撤回 ≠ 已驳回:前者是发起人自己收回的,没有人对它做过判断。共用一个标签
    // 的话,记录上就看不出到底有没有人拒绝过什么。
    case 'cancelled': return 'apStCancelled'
    // 用短标签,不是 apRejected 那一句。"已驳回 · 命令未执行 · 已通知发起人" 是一句
    // 话,塞进状态列会折成三四行,把那一行撑得比别人高一倍 —— 英文下尤其明显。
    // 那句话留在详情卡底部,那里有一整行宽度。
    default: return 'apStRejected'
  }
}

// 结果那一栏的抬头。历史工单没有 execStatus(这一列是后加的),它们的成败无从得知 ——
// 只说"已执行",不替它们编一个结果。
function execHeadKey(a: Approval) {
  if (a.execStatus === 'failed') return 'apResultFailed'
  if (a.execStatus === 'success') return 'apResultOk'
  return 'apResultUnknown'
}

const shortTime = (s: string) =>
  new Date(s).toLocaleString('zh', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
async function runApproved(a: Approval) {
  if (a.canExecute !== true || running.value) return
  // 这一下是真的落到库上,而且只有一次机会 —— 确认文案要把这两点都说出来。
  if (!confirmAction(t('apExecuteConfirm', { cmd: a.command }))) return
  running.value = true
  try {
    await api.executeApproval(a.id)
    await load()
    if (detail.value) detail.value = list.value.find((x) => x.id === a.id) || null
    // 目标库拒绝这条语句时,接口仍然是一次成功的调用(业务结果在信封里) —— 从前
    // 这里无条件弹绿色的"执行完成",而库那边其实什么都没变。提示按刚落库的那个
    // 结论走,和列表上显示的是同一个来源。
    const after = list.value.find((x) => x.id === a.id)
    if (after?.execStatus === 'failed') ui.notify(t('apExecuteFailed'), 'error')
    else ui.notify(t('apExecuteDone'), 'success')
  } catch (e) {
    // 服务端的拒绝理由原样透出:等审批、找发起人、看发布单、重新提交 —— 四种
    // 拒绝要人做的事完全不同。
    ui.notifyError(e, t('actionFailed'))
    await load()
  } finally {
    running.value = false
  }
}

// The row answers "which data does this touch"; the command itself is in the
// detail card. A command can be a whole script, so putting it in the row means
// truncating it — and the truncated part is often the part that matters.
function tablesOf(a: Approval) { return tablesLabel(extractTables(a.command)) }

function riskMeta(a: Approval) {
  return a.riskLevel === 'high'
    ? { t: 'highP1', bg: 'var(--danger-subtle)', c: 'var(--danger-text)' }
    : { t: 'privP2', bg: 'var(--warning-subtle)', c: 'var(--warning-text)' }
}
// approval chain from real step data: initiator → approvers
function chainText(a: Approval) {
  const names = (a.steps || []).map((s) => s.approver).filter(Boolean)
  return [a.initiator, ...names].join(' → ')
}
</script>

<template>
  <div class="scy page">
    <div class="card">
      <!-- 卡片头:标题 + 一句弱化的说明 + 右侧控制区(搜索/状态/范围/刷新) -->
      <div class="chead">
        <div class="ctitle">
          <div class="h1">{{ $t('t_approve') }}</div>
          <div class="hint"><Inbox :size="12" />{{ $t('apprSubTail') }}</div>
        </div>
        <div class="ctrls">
          <div class="searchbox">
            <Search :size="14" color="var(--text-faint)" />
            <input v-model="q" :placeholder="$t('apSearchPh')" spellcheck="false" @input="onSearch">
            <button v-if="q" class="sclear" :title="$t('apSearchClear')" @click="clearSearch"><X :size="13" /></button>
          </div>
          <VSelect v-model="statusLabel" :options="statusLabels" height="32px" class="stsel" />
          <div class="tabs">
            <div class="tab" :class="{ active: scope === 'mine' }" @click="setScope('mine')">{{ $t('mineAppr') }}</div>
            <div class="tab" :class="{ active: scope === 'all' }" @click="setScope('all')">{{ $t('allAppr') }}</div>
          </div>
          <button class="refresh" :disabled="refreshing" :title="$t('btnRefresh')" @click="refresh">
            <RotateCw :size="14" :class="{ spin: refreshing }" />
          </button>
        </div>
      </div>

      <div class="table">
      <div class="thead">
        <span>{{ $t('apNo') }}</span><span>{{ $t('apCommand') }}</span>
        <span>{{ $t('apInitiator') }}</span><span class="right">{{ $t('apTime') }}</span>
        <span>{{ $t('apStatus') }}</span><span class="acth">{{ $t('apActions') }}</span>
      </div>
      <div v-for="a in list" :key="a.id" class="tr" @click="openDetail(a)">
        <!-- 单号与优先级同格:它们回答的是同一件事 —— "这是哪一张单,有多要紧" -->
        <span class="idcell">
          <span class="mono apno click" :title="$t('apDetail')">#{{ a.apNo }}</span>
          <span class="badge" :class="a.riskLevel === 'high' ? 'p1' : 'p2'">{{ $t(riskMeta(a).t as any) }}</span>
        </span>
        <!-- 目标表用代码块;取不出表名时给一个居中的弱灰短横,而不是留空 -->
        <span class="tgtcell">
          <span class="tbl" :title="a.command"><code v-if="tablesOf(a)">{{ tablesOf(a) }}</code><span v-else class="none">—</span></span>
          <span class="inst"><span class="envtag" :class="a.tierCode || 'unknown'">{{ a.env.toUpperCase() }}</span>{{ a.instance }}<template v-if="a.database"> / {{ a.database }}</template></span>
        </span>
        <span class="who"><span class="ava">{{ initialsOf(a.initiator) }}</span>{{ a.initiator }}</span>
        <span class="timecell">{{ new Date(a.createdAt).toLocaleString('sv').slice(5, 16) }}</span>
        <span class="stcell">
          <span class="stbadge" :class="rowState(a)">
            <span class="sdot" /><span>{{ $t(rowLabel(a) as any) }}</span>
          </span>
        </span>
        <!-- 操作列里只放**操作**。这里原先还有一个"详情"按钮,而它做的事和点这一
             整行一模一样(.tr 自己就带 @click="openDetail") —— 一个纯冗余的按钮,
             却把这一列撑宽了三分之一。看一眼不是一次操作,它不该占这里的位置。
             进详情的入口是整行,以及左边那个单号。

             代办项直接在行上决定,不必先开详情卡:待审的单子通常是一眼就能判的,
             而为每一张都开一次卡再关掉,是这一页最常做也最没必要的一次点击。
             判断谁能决定仍然只看服务端算好的 canDecide,确认弹窗与失败提示与卡片里
             那一对按钮共用同一个 decide()。 -->
        <span class="acts">
          <!-- 撤回排在最左、样式最轻:它是发起人的退路,不是这一列的主操作。
               条件只看 canCancel,不再自己判 status —— 服务端已经把"等审批的"和
               "已批准但没跑的"两种都算进去了,前端再判一遍就是第二套会跑偏的规则。 -->
          <button
            v-if="a.canCancel" class="rowbtn cxl" :disabled="running"
            :title="$t('apCancel')" @click.stop="cancelAp(a)"
          >
            <Undo2 :size="13" />{{ $t('apCancel') }}
          </button>
          <template v-if="a.status === 'pending' && canDecide(a)">
            <button class="rowbtn rej" :title="$t('apReject')" @click.stop="decide(a, false)">
              <CircleX :size="13" />{{ $t('apReject') }}
            </button>
            <button class="rowbtn ok" :title="$t('apApprove')" @click.stop="decide(a, true)">
              <CircleCheckBig :size="13" />{{ $t('apApprove') }}
            </button>
          </template>
          <!-- 执行同样放到行上。它和审批不会同时出现:一张单要么在等人决定,要么已经
               批了在等人跑 —— canExecute 由服务端算(service.CanExecuteApproved),
               所以这个按钮只对**此刻真的能跑它**的人亮,而不是"看起来能点"。
               能跑它的不再只有发起人:够得到那台实例、能力矩阵也放行的同事都可以
               接手(ADR 0010 的 2026-09-09 修订)。 -->
          <button
            v-else-if="a.canExecute" class="rowbtn run" :disabled="running"
            :title="$t('apExecute')" @click.stop="runApproved(a)"
          >
            <Play :size="13" />{{ $t('apExecute') }}
          </button>
        </span>
      </div>
      <!-- 空态要分清"确实没有"和"筛没了":后者的出口是清掉条件,不是等着 -->
      <div v-if="!list.length" class="empty">
        <div class="eic"><Inbox :size="22" color="var(--text-faint)" /></div>
        <div class="et">{{ (q || statusFilter) ? $t('apEmptyFiltered') : $t('apEmpty') }}</div>
        <button v-if="q || statusFilter" class="eclear" @click="q = ''; statusFilter = ''; page = 1; load()">{{ $t('apClearFilters') }}</button>
      </div>
      </div>

      <!-- 分页收进卡片内部,靠一条分割线和表格分开 -->
      <div class="pfootbar">
        <span class="ptotal">{{ $t('apTotal', { n: total }) }}</span>
        <div class="pager">
          <span class="psize">
            {{ $t('apPerPage') }}
            <select :value="pageSize" @change="setPageSize(Number(($event.target as HTMLSelectElement).value))">
              <option v-for="n in pageSizes" :key="n" :value="n">{{ n }}</option>
            </select>
          </span>
          <button class="pg" :disabled="page <= 1" @click="goto(page - 1)"><ChevronLeft :size="15" /></button>
          <span class="pgn">{{ $t('auditPageOf', { p: page, n: pages }) }}</span>
          <button class="pg" :disabled="page >= pages" @click="goto(page + 1)"><ChevronRight :size="15" /></button>
        </div>
      </div>
    </div>

    <!-- detail card: the full command and everything needed to decide on it -->
    <div v-if="detail" class="overlay">
      <div class="mask" @click="closeDetail" />
      <div class="dcard">
        <div class="dhead">
          <span class="badge" :style="{ background: riskMeta(detail).bg, color: riskMeta(detail).c }">{{ $t(riskMeta(detail).t as any) }}</span>
          <span class="dtitle">{{ detail.keyword }} {{ $t('apprCardTail') }}</span>
          <span class="mono apno">#{{ detail.apNo }}</span>
          <X :size="18" class="dx" @click="closeDetail" />
        </div>
        <div class="dbody">
          <div class="dlbl">{{ $t('apCommand') }}</div>
          <pre class="dcmd">{{ detail.command }}</pre>
          <div class="dgrid">
            <div><span class="ml">{{ $t('apInitiator') }}</span><div class="who"><span class="ava">{{ initialsOf(detail.initiator) }}</span>{{ detail.initiator }}</div></div>
            <div><span class="ml">{{ $t('apTarget') }}</span><div class="tgt">{{ detail.env.toUpperCase() }} · {{ detail.instance }}<template v-if="detail.database"> · {{ detail.database }}</template></div></div>
            <!-- The tier the command was JUDGED under, snapshotted when the ticket
                 was raised. Shown separately from the environment because the two
                 can diverge later, and this is the one that explains the verdict.
                 Blank on tickets predating tiers — reported as unknown rather than
                 resolved from today's binding, which would be a guess about the
                 past presented as a fact. -->
            <div><span class="ml">{{ $t('apTier') }}</span><div class="tgt">{{ detail.tierCode ? detail.tierCode.toUpperCase() : $t('apTierUnknown') }}</div></div>
          </div>
          <div class="ml">{{ $t('apReason') }}</div><div class="rt">{{ detail.reason || '—' }}</div>
          <div class="ml">{{ $t('apChain') }}</div>
          <div class="chain"><GitPullRequestArrow :size="14" color="#8facff" />{{ chainText(detail) }}</div>
          <!-- 执行结果。判据是 executedAt,不是"result 有没有内容":
               一次失败可能什么输出都没留下,而那恰恰是最需要看到"它跑过、并且挂了"
               的时候;反过来,一张刚批准还没跑的工单,result 里躺着的是审批时写下的
               占位文字("· 已批准,等待发起人执行")—— 拿它当"有结果"会让详情页对着
               一条还没下发的命令说"已执行"。跑没跑过,只有 executedAt 说了算。 -->
          <template v-if="detail.executedAt">
            <div class="ml">{{ $t('apResult') }}</div>
            <div class="rhead" :class="detail.execStatus || 'unknown'">
              <CircleX v-if="detail.execStatus === 'failed'" :size="14" />
              <CircleCheckBig v-else-if="detail.execStatus === 'success'" :size="14" />
              <span>{{ $t(execHeadKey(detail) as any) }}</span>
              <span v-if="detail.executedAt" class="rmeta">{{ shortTime(detail.executedAt) }}</span>
              <span v-if="detail.execStatus === 'success'" class="rmeta">{{ $t('apResultRows', { n: detail.resultRows }) }}</span>
            </div>
            <pre v-if="detail.result" class="dcmd out" :class="{ bad: detail.execStatus === 'failed' }">{{ detail.result }}</pre>
            <div v-else class="rt">{{ $t('apResultNoLog') }}</div>
          </template>
        </div>
        <div class="dfoot">
          <template v-if="detail.status === 'pending' && canDecide(detail)">
            <VButton variant="danger" height="38px" @click="decide(detail, false)">{{ $t('apReject') }}</VButton>
            <VButton variant="primary" height="38px" @click="decide(detail, true)">{{ $t('apApprove') }}</VButton>
          </template>
          <span v-else-if="detail.status === 'pending'" class="waitc">{{ detail.blockReason || $t('apWaitChain') }}</span>
          <!-- 通过之后命令还没跑。这一格要说清它现在停在哪一步:等我执行 /
               已经执行过了 / 归流水线执行 / 等的是别人。 -->
          <template v-else-if="detail.status === 'approved' && detail.canExecute">
            <span class="waitc grow">{{ $t('apExecuteMine') }}</span>
            <VButton variant="primary" height="38px" :disabled="running" @click="runApproved(detail)">{{ $t('apExecute') }}</VButton>
          </template>
          <!-- 跑挂了也是"执行过了":一次批准只换一次执行,失败不退回重来(命令到底
               跑没跑是不确定的,见 service.ExecuteApproved 的占位注释)。所以这里说的
               是结果,不是一个可以再点一次的按钮。 -->
          <span v-else-if="detail.status === 'approved' && detail.executedAt" class="waitc" :class="{ bad: detail.execStatus === 'failed' }">
            {{ detail.execStatus === 'failed'
              ? $t('apExecutedFailed', { at: shortTime(detail.executedAt) })
              : $t('apExecuted', { at: shortTime(detail.executedAt) }) }}
          </span>
          <span v-else-if="detail.status === 'approved' && detail.releaseId" class="waitc">{{ $t('apExecuteByPipeline') }}</span>
          <span v-else-if="detail.status === 'approved' && detail.windowId" class="waitc">{{ $t('apWindowLive') }}</span>
          <span v-else class="waitc">{{ detail.status === 'approved' ? $t('apDone') : $t('apRejected') }}</span>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* 卡片把整张表包起来:表头、行、分页都在同一张白卡上,而不是几块内容各自浮在
   页面底色上。页面底色由外层壳提供(--surface-page),卡片是它上面唯一的白。 */
.card { border: 1px solid var(--border-subtle); border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-sm); }
.chead { display: flex; align-items: center; gap: 16px; flex-wrap: wrap; padding: 16px 18px; border-bottom: 1px solid var(--border-subtle); }
.ctitle { min-width: 0; }
.h1 { font: 600 17px var(--font-display); color: var(--text-strong); }
/* 那句"待办审批后会自动发起…"降成一行小灰字带图标:它是背景说明,不是标题 */
.hint { margin-top: 3px; display: inline-flex; align-items: center; gap: 5px; font: 500 12px var(--font-body); color: var(--text-faint); }
.ctrls { margin-left: auto; display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.searchbox { display: flex; align-items: center; gap: 7px; height: 32px; padding: 0 10px; border: 1px solid var(--border-default); border-radius: 9px; background: var(--surface-sunken); }
.searchbox input { width: 190px; border: none; outline: none; background: transparent; color: var(--text-strong); font: 500 12px var(--font-body); }
.sclear { display: grid; place-items: center; width: 18px; height: 18px; border: none; border-radius: 5px; background: transparent; color: var(--text-faint); cursor: pointer; }
.sclear:hover { color: var(--text-body); }
.stsel { width: 132px; }
.tabs { display: flex; border: 1px solid var(--border-default); border-radius: 9px; overflow: hidden; }
.tab { display: flex; align-items: center; height: 32px; padding: 0 13px; cursor: pointer; font: 600 12px var(--font-body); color: var(--text-muted); border-left: 1px solid var(--border-subtle); white-space: nowrap; }
.tab:first-child { border-left: none; }
.tab.active { background: var(--accent-subtle); color: var(--accent-text); }
.refresh { display: grid; place-items: center; width: 32px; height: 32px; border: 1px solid var(--border-default); border-radius: 9px; background: var(--surface-card); color: var(--text-muted); cursor: pointer; }
.refresh:hover:not(:disabled) { color: var(--accent-text); border-color: var(--accent-text); }
.refresh:disabled { opacity: .6; cursor: default; }
.spin { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }

/* 每一行是**各自独立**的一个 grid(.thead 和每个 .tr 分别声明 display:grid),所以
   列宽必须是与内容无关的定值 —— 一旦写 min-content / max-content / auto,每行会按
   自己那一行的内容各算一套,表头和各行的分栏就对不齐了。

   定值按**英文**量,不是中文:照着中文词量出来的宽度,"Privilege P2"、"Rejected"
   一上去就顶破,徽章在定高胶囊里折行,整张表的行高参差不齐。 */
.thead, .tr { display: grid; grid-template-columns: 208px minmax(200px, 1fr) 150px 104px 116px 184px; gap: 12px; align-items: center; padding: 0 18px; }
.thead { height: 42px; background: var(--surface-page); border-bottom: 1px solid var(--border-subtle); font: 500 11px var(--font-body); color: var(--text-muted); text-transform: uppercase; letter-spacing: .05em; }
.thead .right { text-align: right; }
.tr { min-height: 56px; padding-top: 8px; padding-bottom: 8px; border-bottom: 1px solid var(--border-subtle); font: 500 12px var(--font-body); color: var(--text-body); cursor: pointer; transition: background var(--dur-fast, .15s) var(--ease-out, ease); }
.tr:last-child { border-bottom: none; }
.tr:hover { background: var(--surface-page); }
.mono { font-family: var(--font-mono); }

/* 单号 + 优先级 */
.idcell { display: flex; align-items: center; gap: 8px; min-width: 0; }
.apno { color: var(--accent-text); font: 600 12.5px var(--font-mono); text-decoration: underline; text-decoration-color: transparent; text-underline-offset: 3px; }
.tr:hover .apno { text-decoration-color: currentColor; }
.badge { flex-shrink: 0; display: inline-flex; align-items: center; height: 20px; padding: 0 8px; border-radius: 999px; font: 600 10px var(--font-mono); white-space: nowrap; border: 1px solid transparent; }
.badge.p1 { background: var(--danger-subtle); color: var(--danger-text); }
.badge.p2 { background: var(--surface-sunken); color: var(--text-muted); border-color: var(--border-subtle); }

/* 目标表 / 实例 / 库 */
.tgtcell { min-width: 0; }
.tbl { display: block; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tbl code { padding: 2px 6px; border-radius: 5px; background: var(--surface-sunken); color: var(--text-strong); font: 500 11.5px var(--font-mono); }
.none { color: var(--text-faint); }
.inst { margin-top: 4px; display: flex; align-items: center; gap: 6px; min-width: 0; font: 500 11px var(--font-mono); color: var(--text-faint); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* 环境标签按**分层**着色,不按环境名 —— 名字不决定它是什么环境,绑的分层才决定 */
.envtag { flex-shrink: 0; padding: 1px 6px; border-radius: 4px; font: 600 9.5px var(--font-mono); background: var(--surface-sunken); color: var(--text-muted); border: 1px solid var(--border-subtle); }
.envtag.prod { background: var(--danger-subtle); color: var(--danger-text); border-color: transparent; }
.envtag.gli, .envtag.staging { background: var(--warning-subtle); color: var(--warning-text); border-color: transparent; }

.who { display: flex; align-items: center; gap: 7px; min-width: 0; color: var(--text-strong); }
.ava { flex-shrink: 0; width: 24px; height: 24px; border-radius: 50%; background: var(--accent-subtle); color: var(--accent-text);
  font: 600 10px var(--font-mono); display: grid; place-items: center; }
.timecell { text-align: right; font: 500 11.5px var(--font-mono); color: var(--text-faint); white-space: nowrap; }

/* 状态胶囊。等待类的带一颗呼吸的点 —— 它是唯一一种"还会自己变"的状态。 */
.stcell { min-width: 0; }
.stbadge { display: inline-flex; align-items: center; gap: 6px; height: 24px; padding: 0 10px; border-radius: 999px; font: 600 11px var(--font-body); white-space: nowrap; }
.sdot { width: 6px; height: 6px; border-radius: 50%; background: currentColor; flex-shrink: 0; }
.stbadge.pending, .stbadge.waitrun { background: var(--warning-subtle); color: var(--warning-text); }
.stbadge.pending .sdot, .stbadge.waitrun .sdot { animation: breathe 1.8s ease-in-out infinite; }
.stbadge.approved { background: var(--success-subtle); color: var(--success-text); }
.stbadge.rejected, .stbadge.failed { background: var(--danger-subtle); color: var(--danger-text); }
/* 已失效是"没人管过",不是"被拒绝" —— 用中性灰,不占用红色 */
.stbadge.expired { background: var(--surface-sunken); color: var(--text-faint); border: 1px solid var(--border-subtle); }
@keyframes breathe { 0%, 100% { opacity: 1; } 50% { opacity: .25; } }

.acth { text-align: right; padding-left: 18px; }
.acts { display: inline-flex; align-items: center; gap: 6px; justify-content: flex-end; padding-left: 18px; min-height: 28px; }
.rowbtn {
  display: inline-flex; align-items: center; gap: 4px; height: 28px; padding: 0 10px; cursor: pointer;
  border: 1px solid var(--border-subtle); border-radius: 8px; background: var(--surface-card);
  font: 600 11px var(--font-body); color: var(--text-muted); white-space: nowrap;
  transition: color .12s, border-color .12s, background .12s;
}
.rowbtn.ok:hover { color: var(--success-text); border-color: var(--success); background: var(--success-subtle); }
.rowbtn.cxl { color: var(--text-muted); }
.rowbtn.cxl:hover:not(:disabled) { color: var(--text-strong); border-color: var(--border-strong); background: var(--surface-sunken); }
.rowbtn.rej:hover { color: var(--danger-text); border-color: var(--danger); background: var(--danger-subtle); }
/* 执行是这一行里唯一真的会落到库上的动作,所以它比另外两个显眼一档 */
.rowbtn.run { color: #fff; background: var(--accent); border-color: var(--accent); }
.rowbtn.run:hover:not(:disabled) { background: var(--accent-hover); border-color: var(--accent-hover); }
.rowbtn:disabled { opacity: .55; cursor: default; }

/* 空态与分页 */
.empty { padding: 52px 24px; display: flex; flex-direction: column; align-items: center; gap: 10px; }
.eic { width: 46px; height: 46px; border-radius: 14px; background: var(--surface-sunken); display: grid; place-items: center; }
.et { font: 600 13px var(--font-body); color: var(--text-muted); }
.eclear { padding: 6px 12px; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-card); color: var(--accent-text); font: 600 11.5px var(--font-body); cursor: pointer; }
.pfootbar { display: flex; align-items: center; gap: 14px; padding: 12px 18px; border-top: 1px solid var(--border-subtle); }
.ptotal { font: 500 12px var(--font-body); color: var(--text-muted); }
.pager { margin-left: auto; display: flex; align-items: center; gap: 8px; }
.psize { display: inline-flex; align-items: center; gap: 6px; font: 500 11.5px var(--font-body); color: var(--text-faint); }
.psize select { height: 28px; padding: 0 6px; border: 1px solid var(--border-subtle); border-radius: 7px; background: var(--surface-sunken); color: var(--text-body); font: 500 11.5px var(--font-mono); cursor: pointer; outline: none; }
.pg { width: 30px; height: 28px; display: grid; place-items: center; border: 1px solid var(--border-subtle);
  border-radius: 8px; background: var(--surface-card); color: var(--text-body); cursor: pointer; }
.pg:disabled { opacity: .4; cursor: default; }
.pgn { font: 500 12px var(--font-mono); color: var(--text-muted); }

.overlay { position: fixed; inset: 0; z-index: 60; display: grid; place-items: center; }
.mask { position: absolute; inset: 0; background: rgba(0,0,0,.45); }
.dcard { position: relative; width: min(760px, 92vw); max-height: 86vh; display: flex; flex-direction: column;
  background: var(--surface-card); border: 1px solid var(--border-subtle); border-radius: 14px; overflow: hidden; }
.dhead { display: flex; align-items: center; gap: 10px; padding: 14px 16px; border-bottom: 1px solid var(--border-subtle); }
.dtitle { font: 600 14px var(--font-display); color: var(--text-strong); }
.dx { margin-left: auto; cursor: pointer; color: var(--text-muted); }
.dbody { padding: 14px 16px; overflow: auto; }
.dlbl, .ml { font: 600 11px var(--font-body); color: var(--text-faint); text-transform: uppercase; letter-spacing: .06em; margin-top: 10px; }
.dcmd { margin: 6px 0 0; padding: 10px 12px; border-radius: 9px; background: var(--surface-page);
  border: 1px solid var(--border-subtle); font: 400 12px/1.65 var(--font-mono); color: var(--text-strong);
  white-space: pre-wrap; word-break: break-word; }
.dcmd.out { color: var(--text-muted); }
.dgrid { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; margin-top: 10px; }
.tgt, .rt { font: 500 12px var(--font-body); color: var(--text-body); margin-top: 4px; }
.chain { display: flex; align-items: center; gap: 6px; font: 500 12px var(--font-body); color: var(--text-muted); margin-top: 4px; }
.dfoot { display: flex; justify-content: flex-end; align-items: center; gap: 10px; padding: 12px 16px;
  border-top: 1px solid var(--border-subtle); }
.waitc { font: 500 12px var(--font-body); color: var(--text-muted); }

.page { flex: 1; min-height: 0; padding: var(--page-pad); max-width: var(--page-max); margin-inline: auto; width: 100%; }
.head { display: flex; align-items: center; margin-bottom: 18px; }
.eyebrow { font: 500 11px var(--font-mono); letter-spacing: 0.12em; color: var(--text-faint); text-transform: uppercase; }
.sub { font: 500 13px var(--font-body); color: var(--text-muted); margin-top: 4px; }
.tabs { margin-left: auto; display: flex; gap: 10px; align-items: center; }
.tab {
  display: flex; align-items: center; gap: 8px; height: 36px; padding: 0 12px; border: 1px solid var(--border-default);
  border-radius: 10px; font: 500 12px var(--font-mono); color: var(--text-muted); cursor: pointer;
}
.tab.active { border-color: var(--accent-subtle-border); background: var(--accent-subtle); color: var(--accent-text); font-weight: 600; }
/* 定高的胶囊里不能折行,否则文字会溢出到胶囊外面 */
.badge { display: inline-flex; align-items: center; height: 22px; padding: 0 9px; border-radius: 999px; font: 600 11px var(--font-mono); white-space: nowrap; }
.apno { margin-left: auto; font: 600 12px var(--font-mono); color: #8facff; }
.ml { font: 500 11px var(--font-body); color: var(--text-faint); }
.who { display: flex; align-items: center; gap: 7px; margin-top: 3px; font: 600 12px var(--font-body); color: var(--text-body); }
.ava { width: 22px; height: 22px; border-radius: 50%; background: #232838; border: 1px solid var(--border-default); display: flex; align-items: center; justify-content: center; font: 600 9px var(--font-body); color: var(--text-muted); }
.tgt { font: 600 12px var(--font-mono); color: var(--text-body); margin-top: 5px; }
.rt { font: 400 12.5px/1.5 var(--font-body); color: var(--text-body); margin-top: 3px; }
.chain { display: flex; align-items: center; gap: 7px; font: 500 11px var(--font-mono); color: var(--text-muted); }
.waitc { margin-left: auto; font: 600 11.5px var(--font-mono); color: var(--text-faint); }
</style>
