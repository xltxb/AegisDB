<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Inbox, GitPullRequestArrow, CircleCheckBig, CircleX, X, Play, ChevronLeft, ChevronRight } from 'lucide-vue-next'
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
const pageSize = 50
const pages = computed(() => Math.max(1, Math.ceil(total.value / pageSize)))

const pending = computed(() => list.value.filter((a) => a.status === 'pending').length)

async function load() {
  // M14: 加载失败以 toast 呈现，避免静默失败
  try {
    const res = await api.approvals(scope.value, page.value, pageSize)
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
  if (a.status === 'approved' && !a.executedAt && !a.releaseId) return 'waitrun'
  // 最终状态以**库那边收没收下**为准。批准是人的决定,而这一列说的是那次下发的
  // 结果 —— 一条跑挂的 DROP 显示成"已通过",读的人会以为变更已经生效了。
  //
  // 这里不动 a.status:跑挂了不该把一张已批准的工单变回没批准,能不能执行、按状态
  // 筛选走的都是它。行内状态一直是这样合成出来的(waitrun 也是),多这一档而已。
  if (a.status === 'approved' && a.execStatus === 'failed') return 'failed'
  return a.status
}
function rowLabel(a: Approval) {
  switch (rowState(a)) {
    case 'pending': return 'apPending'
    case 'waitrun': return 'apStWaitRun'
    case 'failed': return 'apStFailed'
    // 历史工单没有 execStatus(这一列是后加的),它们的成败无从得知 —— 照旧只说
    // "已执行",不替它们编一个结果。
    case 'approved': return a.executedAt ? 'apStRan' : 'apStDone'
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
    <div class="head">
      <div>
        <div class="eyebrow">APPROVAL INBOX</div>
        <div class="sub">{{ pending }} {{ $t('awaitingYou') }} {{ $t('apprSubTail') }}</div>
      </div>
      <div class="tabs">
        <div class="tab" :class="{ active: scope === 'mine' }" @click="setScope('mine')"><Inbox :size="14" />{{ $t('mineAppr') }}</div>
        <div class="tab" :class="{ active: scope === 'all' }" @click="setScope('all')">{{ $t('allAppr') }}</div>
      </div>
    </div>

    <div class="table">
      <div class="thead">
        <span>{{ $t('apNo') }}</span><span>{{ $t('apRisk') }}</span><span>{{ $t('apCommand') }}</span>
        <span>{{ $t('apInitiator') }}</span><span>{{ $t('apTarget') }}</span><span>{{ $t('apTime') }}</span>
        <span>{{ $t('apStatus') }}</span><span class="acth">{{ $t('apActions') }}</span>
      </div>
      <div v-for="a in list" :key="a.id" class="tr" @click="openDetail(a)">
        <span class="mono apno click" :title="$t('apDetail')">#{{ a.apNo }}</span>
        <span><span class="badge" :style="{ background: riskMeta(a).bg, color: riskMeta(a).c }">{{ $t(riskMeta(a).t as any) }}</span></span>
        <span class="mono cmd1" :title="a.command">{{ tablesOf(a) }}</span>
        <span class="who"><span class="ava">{{ initialsOf(a.initiator) }}</span>{{ a.initiator }}</span>
        <span class="mono mute">{{ a.env.toUpperCase() }} · {{ a.instance }}</span>
        <span class="mono mute">{{ new Date(a.createdAt).toLocaleString('zh', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }) }}</span>
        <span class="st" :class="rowState(a)">
          <CircleCheckBig v-if="rowState(a) === 'approved'" :size="13" />
          <CircleX v-else-if="rowState(a) === 'rejected'" :size="13" />
          {{ $t(rowLabel(a) as any) }}
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
          <template v-if="a.status === 'pending' && canDecide(a)">
            <button class="rowbtn rej" :title="$t('apReject')" @click.stop="decide(a, false)">
              <CircleX :size="13" />{{ $t('apReject') }}
            </button>
            <button class="rowbtn ok" :title="$t('apApprove')" @click.stop="decide(a, true)">
              <CircleCheckBig :size="13" />{{ $t('apApprove') }}
            </button>
          </template>
          <!-- 执行同样放到行上。它和审批不会同时出现:一张单要么在等人决定,要么已经
               批了在等发起人跑 —— canExecute 由服务端算(service.CanExecuteApproved),
               所以这个按钮只对该去按的那个人亮,而不是"看起来能点"。 -->
          <button
            v-else-if="a.canExecute" class="rowbtn run" :disabled="running"
            :title="$t('apExecute')" @click.stop="runApproved(a)"
          >
            <Play :size="13" />{{ $t('apExecute') }}
          </button>
        </span>
      </div>
      <div v-if="!list.length" class="empty">{{ $t('apEmpty') }}</div>
    </div>

    <div v-if="list.length" class="pfootbar">
      <span class="ptotal">{{ $t('apTotal', { n: total }) }}</span>
      <div class="pager">
        <button class="pg" :disabled="page <= 1" @click="goto(page - 1)"><ChevronLeft :size="15" /></button>
        <span class="pgn">{{ $t('auditPageOf', { p: page, n: pages }) }}</span>
        <button class="pg" :disabled="page >= pages" @click="goto(page + 1)"><ChevronRight :size="15" /></button>
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
          <span v-else class="waitc">{{ detail.status === 'approved' ? $t('apDone') : $t('apRejected') }}</span>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.pfootbar { display: flex; align-items: center; gap: 14px; margin-top: 12px; }
.ptotal { font: 500 12px var(--font-body); color: var(--text-muted); }
.pager { margin-left: auto; display: flex; align-items: center; gap: 8px; }
.pg { width: 30px; height: 28px; display: grid; place-items: center; border: 1px solid var(--border-subtle);
  border-radius: 8px; background: var(--surface-card); color: var(--text-body); cursor: pointer; }
.pg:disabled { opacity: .4; cursor: default; }
.pgn { font: 500 12px var(--font-mono); color: var(--text-muted); }

.table { border: none; border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-xs); overflow: hidden; }
/* 风险与状态两列按内容取宽(min-content),不再是按中文文案量出来的死值:
   "Privilege P2"、"Rejected" 一到英文就顶破 88px / 96px,徽章在 22px 高的胶囊里
   折行,整张表的行高参差不齐。富余宽度全部留给命令列那一个 1fr。 */
/* 每一行是**各自独立**的一个 grid(.thead 和每个 .tr 分别声明 display:grid),所以
   列宽必须是与内容无关的定值 —— 一旦写 min-content / max-content / auto,每行会按
   自己那一行的内容各算一套,表头和各行的分栏就对不齐了。

   定值按**英文**量,不是中文:原先 88px / 96px 是照着"风险""状态"两个中文词定的,
   "Privilege P2"、"Rejected · not executed · requester notified" 一上去就顶破,
   徽章在 22px 高的定高胶囊里折行,行高从 46 变到 80,整张表看着散架。

   末列是操作列,按最满的那一行(驳回+通过,英文 Reject+Approve)留够并留一点余量,
   这样每一行的按钮左右边界都在同一条线上,而不是随当前页有没有待审的单子伸缩。 */
.thead, .tr { display: grid; grid-template-columns: 104px 108px minmax(140px, 1fr) 130px 176px 116px 104px 184px; gap: 10px; align-items: center; padding: 10px 14px; }
.thead { background: var(--surface-page); font: 600 11px var(--font-body); color: var(--text-faint); text-transform: uppercase; letter-spacing: .06em; }
.tr { border-top: 1px solid var(--border-subtle); font: 500 12px var(--font-body); color: var(--text-body); cursor: pointer; }
.tr:hover { background: var(--surface-page); }
.mono { font-family: var(--font-mono); }
.mute { color: var(--text-muted); }
.apno { color: #8facff; }
.cmd1 { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; color: var(--text-strong); }
.who { display: flex; align-items: center; gap: 6px; }
.ava { width: 20px; height: 20px; border-radius: 6px; background: var(--accent-subtle); color: var(--accent-text);
  font: 600 10px var(--font-mono); display: grid; place-items: center; }
.st { display: flex; align-items: center; gap: 4px; font-weight: 600; white-space: nowrap; }
.st.pending { color: var(--warning-text); }
.st.approved { color: var(--success-text); }
/* 待执行借用 pending 的告警色:它和"待审批"一样,是一件还没做完、有人得动手的事 */
.st.waitrun { color: var(--warning-text); }
.st.rejected { color: var(--danger-text); }
/* 执行失败与被驳回同色:两者对读的人是同一件事 —— 这条变更没有生效 */
.st.failed { color: var(--danger-text); }
/* 操作列和状态列之间多留一段:只隔着 10px 的网格间距时,"待执行 [执行] [详情]"
   连成一片,看着像按钮长在状态那一列里。 */
.acth { text-align: right; padding-left: 18px; }
/* 定高,而不是让内容撑。拿掉「详情」之后,没有任何操作的行少了一个撑高度的元素,
   有按钮的行就比没按钮的高一点点;而按钮自身的高度还随语言变 —— 中文行盒比英文
   高 1px,于是中文下是 47/46 两种行高、英文下反而是齐的。按字体度量去配这个数配
   不准,索性把按钮和这一格都定死在同一个高度上。 */
.acts { display: inline-flex; align-items: center; gap: 6px; justify-content: flex-end; padding-left: 18px; min-height: 26px; }
.rowbtn {
  display: inline-flex; align-items: center; gap: 4px; height: 26px; padding: 0 9px; cursor: pointer;
  border: 1px solid var(--border-subtle); border-radius: 7px; background: var(--surface-card);
  font: 600 11px var(--font-body); color: var(--text-muted); white-space: nowrap;
  transition: color .12s, border-color .12s, background .12s;
}
.rowbtn.ok:hover { color: var(--success-text); border-color: var(--success); background: var(--success-subtle); }
.rowbtn.rej:hover { color: var(--danger-text); border-color: var(--danger); background: var(--danger-subtle); }
/* 执行是这一行里唯一真的会落到库上的动作,所以它比另外两个显眼一档 */
.rowbtn.run { color: #fff; background: var(--accent); border-color: var(--accent); }
.rowbtn.run:hover:not(:disabled) { background: var(--accent-hover); border-color: var(--accent-hover); }
.rowbtn:disabled { opacity: .55; cursor: default; }
.apno.click { text-decoration: underline; text-decoration-color: transparent; text-underline-offset: 3px; }
.tr:hover .apno.click { text-decoration-color: currentColor; }
.rhead { display: flex; align-items: center; gap: 8px; margin-bottom: 6px; font: 600 12px var(--font-body); }
.rhead.success { color: var(--success-text); }
.rhead.failed { color: var(--danger-text); }
.rhead.unknown { color: var(--text-muted); }
.rmeta { font: 500 11px var(--font-mono); color: var(--text-faint); }
.dcmd.out.bad { border-color: var(--danger); }
.waitc.bad { color: var(--danger-text); }
.empty { padding: 28px; text-align: center; color: var(--text-faint); font-size: 13px; }

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
