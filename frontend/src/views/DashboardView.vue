<script setup lang="ts">
// 总览 —— 登录后的落地页。
//
// 从前进来直接就是 Web 命令行:一个连着生产库、光标在等你敲字的提示符。那是这个
// 产品里唯一能改动真实数据的地方,把它当默认页,等于每次开工都先站到闸门里面,再
// 想起来自己本来是要去看审批的。落地页应该先让人知道**此刻有什么要管**,再由人
// 决定走进哪一扇门。
//
// 这一页不新增任何接口,也不新增任何权限面:每块数据都是用户本来就能自己去拉的那
// 一个接口,而那些接口各自带着菜单闸。所以卡片按 auth.menus 决定出不出现 —— 页面
// 上不会出现一个用户本来取不到的数字,漏了判断也只会得到 403 后的空卡片,而不是越权。
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import {
  SquareTerminal, ClipboardCheck, Database, Activity, CalendarClock,
  ScrollText, ArrowRight, ShieldAlert, Play, Rocket, RotateCw, CircleCheckBig, CircleX, CheckCheck, Undo2,
} from 'lucide-vue-next'
import api from '@/api'
import { useAuthStore } from '@/stores/auth'
import { useEnvTierStore } from '@/stores/envtier'
import { useUIStore } from '@/stores/ui'
import { awaitsExecution, humanGateOf } from '@/lib/pendingWork'
import { confirmAction } from '@/lib/confirm'
import { initialsOf } from '@/lib/initials'
import type { Approval, AuditRow, Connection, ExecWindow, Release } from '@/types'

const { t } = useI18n()
const router = useRouter()
const auth = useAuthStore()
const envtier = useEnvTierStore()
const ui = useUIStore()

const can = (key: string) => !!auth.menus[key]

const conns = ref<Connection[]>([])
const windows = ref<ExecWindow[]>([])
const approvals = ref<Approval[]>([])
// 待执行的两种单据分开取:它们的"还没做完"是两种状态,一个是通过待执行,一个是
// 流水线停在人工闸上。
const toRun = ref<Approval[]>([])
const toRelease = ref<Release[]>([])
const audit = ref<AuditRow[]>([])
const stats = ref<{ online: boolean; p50Ms: number; p95Ms: number; intercepts: number } | null>(null)

// ---- 派生 ----

/**
 * 班车(执行窗口)在这一页上分三档,顺序就是它们要人做的事:
 *
 *   开着的   —— 此刻本该审批的中/高风险语句正在被直接放行。这是这个系统里唯一一种
 *              "门开着而没人站在门口"的状态,所以它排最前、用警示色。
 *   等审批的 —— 有人申请了一扇门,还没人签字。它是**待办**,不是状态。
 *   排着的   —— 已批准、还没到点。它回答"今晚/这周会不会有一段免审批时间"。
 *
 * `active` 由后端用与判定完全相同的逻辑算出(并且已经把"没批准"算进去了),
 * 前端不自己重算跨午夜和时区 —— 那两处写两遍迟早分叉,而分叉的表现是界面说开着、
 * 网关说没开。
 */
const openWindows = computed(() => windows.value.filter((w) => w.active))
const pendingWindows = computed(() => windows.value.filter((w) => w.status === 'pending'))
/** 已批准、启用中、但此刻没开 —— 也就是"接下来会开"的那些。 */
const queuedWindows = computed(() =>
  windows.value.filter((w) => w.status === 'approved' && w.enabled && !w.active))

/** 等我决定的单子 —— canDecide 也是服务端算好的,前端不再拼一遍那三个条件。 */
const myTodo = computed(() => approvals.value.filter((a) => a.status === 'pending' && a.canDecide))

/**
 * 待执行:批了但还没跑的工单,加上停在人工闸上的升级单。
 *
 * 两半不会重叠,这是后端保证的:canExecuteApproved 明确拒绝属于升级单的工单
 * (ReleaseID > 0 —— "执行由发布流水线的执行阶段完成"),所以一次变更只会出现在
 * 它真正该被点的那一边。
 */
const pendingRun = computed(() => toRun.value.filter((a) => a.canExecute))
const pendingRelease = computed(() => toRelease.value.filter(awaitsExecution))
const runCount = computed(() => pendingRun.value.length + pendingRelease.value.length)

/** 实例按分层归堆,顺序跟着分层表走,这样生产永远排在最上面。 */
const byTier = computed(() => {
  const groups = new Map<string, { code: string; label: string; danger: boolean; conns: Connection[] }>()
  for (const c of conns.value) {
    const tier = envtier.tierOf(c.env)
    const code = tier?.code ?? ''
    if (!groups.has(code)) {
      groups.set(code, {
        code,
        label: code ? envtier.tierLabel(code, t) : t('dashTierUnknown'),
        danger: !!tier?.dangerBanner || !!tier?.requireMfa,
        conns: [],
      })
    }
    groups.get(code)!.conns.push(c)
  }
  // 分层表的顺序即展示顺序;认不出分层的实例排在最后,而不是被藏起来 —— 一台落在
  // 没有规则的分层上的实例,恰恰是最该被看见的那种。
  const order = envtier.tierCodes
  return [...groups.values()].sort((a, b) => {
    const ia = order.indexOf(a.code)
    const ib = order.indexOf(b.code)
    return (ia < 0 ? 99 : ia) - (ib < 0 ? 99 : ib)
  })
})

const dangerConns = computed(() =>
  byTier.value.filter((g) => g.danger).reduce((n, g) => n + g.conns.length, 0))

/**
 * 这条审计记的是不是一次对库的动作。
 *
 * 登录、登出这类会话事件也进同一条审计链,但它们不是"经网关执行的命令",而且比命令
 * 频繁得多。区分靠的是**实例为空**这个结构事实,不是去匹配 "login" 这个词 —— 命令
 * 文本是给人读的,会随文案改,而"没有目标实例"这件事不会。
 */
function isCommandRow(r: AuditRow) { return !!r.instance }

// ---- 载入 ----
//
// 全部并发,而且各自失败各自算:一块卡片取不到数据不该让整页空掉。落地页最没有资格
// 因为某个接口抖了一下就变成一张错误页 —— 人是来看"有没有事"的。
async function load() {
  const jobs: Promise<unknown>[] = [
    envtier.load().catch(() => {}),
    api.connections().then((r) => { conns.value = r }).catch(() => {}),
    api.execWindows().then((r) => { windows.value = r }).catch(() => {}),
    api.gatewayStats().then((r) => { stats.value = r }).catch(() => {}),
  ]
  if (can('approve')) {
    jobs.push(api.approvals('all', 1, 20, 'pending').then((r) => { approvals.value = r.items }).catch(() => {}))
    // 已通过的单独取一遍,并且是服务端按状态筛的 —— 见 api.approvals 的注释:
    // 通过了的工单不会过期,自己筛会漏。
    jobs.push(api.approvals('all', 1, 20, 'approved').then((r) => { toRun.value = r.items }).catch(() => {}))
  }
  if (can('pipeline')) {
    jobs.push(api.releases('all', 'waiting', 1, 20).then((r) => { toRelease.value = r.items }).catch(() => {}))
  }
  if (can('audit')) {
    // 取一大页再自己筛:审计里混着登录这类会话事件,而它们比命令频繁得多 —— 直接取
    // 前 8 条,这张卡片就成了一列 login,把它本该回答的"网关上刚刚发生了什么"挤没了。
    jobs.push(api.audit({ risk: '', page: 1, pageSize: 40 })
      .then((r) => { audit.value = r.items.filter(isCommandRow) })
      .catch(() => {}))
  }
  await Promise.all(jobs)
  ui.pageSub = { key: 'dashPageSub', params: { n: conns.value.length } }
}
onMounted(load)

// ---- 展示助手 ----

/** 与其余页面同一套写法:'sv' 给出 ISO 样式,不随界面语言改变数字顺序。 */
function when(s?: string | null) {
  return s ? new Date(s).toLocaleString('sv').slice(5, 16) : '—'
}

/** 一句话的时间表。与「班车」页上的 whenLabel 同一个口径,只是更短。 */
function windowWhen(w: ExecWindow) {
  const hm = (n: number) => `${String(Math.floor(n / 60) % 24).padStart(2, '0')}:${String(n % 60).padStart(2, '0')}`
  if (w.kind === 'once') {
    const f = (s?: string) => (s ? new Date(s).toLocaleString('sv').slice(5, 16) : '—')
    return `${f(w.startsAt)} → ${f(w.endsAt)}`
  }
  const days = w.weekdays.trim()
    ? w.weekdays.split(',').map((n) => t(`dow${Number(n)}` as any)).join('')
    : t('ewEveryDay')
  return `${days} ${hm(w.startMin)}-${hm(w.endMin)}`
}

function windowScope(w: ExecWindow) {
  const c = conns.value.find((x) => x.id === w.connectionId)
  return (c?.name || '#' + w.connectionId) + ' / ' + w.database
}

function go(path: string) { router.push(path) }

// ---- 行上的动作 ----
//
// 落地页上直接执行/审批,省掉的是"进另一页、找到同一行、再点一次"。但它省不掉的是
// **看清楚要放行的是什么**:这一行里的命令是截断显示的,所以两个动作都先弹一次确认,
// 而确认文案里带的是完整命令。能不能点仍然只看服务端算好的 canExecute / canDecide ——
// 前端不自己拼那几个条件,否则按钮亮不亮和点下去放不放行迟早两套说法。
const busy = ref(false)

async function runNow(a: Approval) {
  if (!a.canExecute || busy.value) return
  if (!confirmAction(t('dashRunConfirm', { no: a.apNo, cmd: a.command }))) return
  busy.value = true
  try {
    await api.executeApproval(a.id)
    ui.notify(t('dashRunDone', { no: a.apNo }), 'success')
    await load()
  } catch (e) { ui.notifyError(e, t('actionFailed')) } finally { busy.value = false }
}

// 放弃这次下发。与 runNow 成对:确认文案里同样带完整命令,因为行里是截断的。
async function cancelNow(a: Approval) {
  if (busy.value) return
  if (!confirmAction(t('apCancelConfirm', { no: a.apNo }))) return
  busy.value = true
  try {
    await api.cancelApproval(a.id)
    ui.notify(t('apCancelled', { no: a.apNo }), 'success')
    await load()
  } catch (e) { ui.notifyError(e, t('actionFailed')); await load() } finally { busy.value = false }
}

async function decideNow(a: Approval, approve: boolean) {
  if (busy.value) return
  const verb = approve ? t('apApprove') : t('apReject')
  if (!confirmAction(t('apConfirm', { verb, cmd: a.command }))) return
  busy.value = true
  try {
    if (approve) await api.approve(a.id)
    else await api.reject(a.id)
    await load()
  } catch (e) { ui.notifyError(e, t('actionFailed')); await load() } finally { busy.value = false }
}

/**
 * 相对时间("10 分钟前")。完整时刻放进 title —— 相对时间好扫,但要对齐日志、
 * 对齐别人的截图时,需要的是那个绝对时刻。
 */
function relTime(s?: string | null) {
  if (!s) return '—'
  const ms = Date.now() - new Date(s).getTime()
  const m = Math.floor(ms / 60000)
  if (m < 1) return t('relJustNow')
  if (m < 60) return t('relMin', { n: m })
  const h = Math.floor(m / 60)
  if (h < 24) return t('relHour', { n: h })
  return t('relDay', { n: Math.floor(h / 24) })
}
/** 绝对时刻,给 title 用。 */
function absTime(s?: string | null) {
  return s ? new Date(s).toLocaleString('sv').slice(0, 19) : '—'
}

/** 审计结论的色档。executed 是常态,不上色 —— 满屏绿色和没有颜色是一个效果。 */
function resultCls(r: string) {
  if (r === 'rejected') return 'bad'
  if (r === 'pending') return 'wait'
  if (r === 'cancelled' || r === 'exported') return 'neutral'
  if (r === 'executed') return 'ok'
  return 'wait'
}
</script>

<template>
  <div class="scy page">
    <!-- 页眉:标题与欢迎语在左,主操作在右。刷新是这一页真正需要的第二个动作 ——
         它上面每一块都是"此刻的状态",而人会盯着它等状态变。 -->
    <div class="head">
      <div class="hleft">
        <div class="eyebrow">OVERVIEW</div>
        <div class="htitle">{{ $t('t_dashboard') }}</div>
        <div class="sub">{{ $t('dashGreeting', { name: auth.me?.name || '' }) }}</div>
      </div>
      <button class="ghostbtn" :disabled="busy" :title="$t('dashRefresh')" @click="load()">
        <RotateCw :size="14" />{{ $t('dashRefresh') }}
      </button>
      <button v-if="can('terminal')" class="cta" @click="go('/terminal')">
        <SquareTerminal :size="15" />{{ $t('dashOpenTerminal') }}
      </button>
    </div>

    <!-- KPI。每张牌:大号等宽数字 + 一行说明 + 右上角一块淡底图标。
         只有**要人动手**的两张(等我审批、待我执行)在大于 0 时变琥珀并加左侧竖条:
         实例数和延迟无论多少都不是待办,把它们一起点亮等于没有重点。 -->
    <div class="stats">
      <div v-if="can('approve')" class="stat click" :class="{ act: myTodo.length }" @click="go('/approvals')">
        <div class="si"><ClipboardCheck :size="15" /></div>
        <div class="sv">{{ myTodo.length }}</div>
        <div class="sl">{{ $t('dashStatTodo') }}</div>
      </div>
      <div
        v-if="can('approve') || can('pipeline')" class="stat click" :class="{ act: runCount }"
        @click="go(pendingRun.length ? '/approvals' : '/releases')"
      >
        <div class="si"><Play :size="15" /></div>
        <div class="sv">{{ runCount }}</div>
        <div class="sl">{{ $t('dashStatToRun') }}</div>
      </div>
      <div v-if="can('execwindow')" class="stat click" :class="{ warn: openWindows.length }" @click="go('/exec-windows')">
        <div class="si"><CalendarClock :size="15" /></div>
        <div class="sv">{{ openWindows.length }}</div>
        <div class="sl">{{ $t('dashStatWindows') }}</div>
      </div>
      <div class="stat">
        <div class="si"><Database :size="15" /></div>
        <div class="sv">{{ conns.length }}</div>
        <div class="sl">{{ $t('dashStatInstances', { n: dangerConns }) }}</div>
      </div>
      <div class="stat">
        <div class="si"><Activity :size="15" /></div>
        <!-- 标的是 p50(中位数)。接口只给 p50 / p95,写成 P90 是编一个没人算过的数。 -->
        <div class="sv">{{ stats ? stats.p50Ms.toFixed(1) + 'ms' : '—' }}</div>
        <div class="sl">{{ stats?.online ? $t('dashStatGwOn') : $t('dashStatGwOff') }}</div>
      </div>
    </div>

    <!-- 当前执行窗口。这是这一页唯一的"门开着"状态,所以它是一整块面板,不是一条提示:
         窗口开着的这几个小时里,本来要审批的中/高风险语句会一条不落地直接下发。
         呼吸点表示"此刻正开着",它是这块面板存在的全部理由。 -->
    <div v-if="openWindows.length" class="panel">
      <div class="phead">
        <span class="pulse" />
        <div class="grow">
          <div class="pt">{{ $t('dashWindowLive') }}</div>
          <div class="ps">{{ $t('dashWindowOpen', { n: openWindows.length }) }}</div>
        </div>
        <button class="ghostbtn sm" @click="go('/exec-windows')">{{ $t('dashWindowManage') }}<ArrowRight :size="13" /></button>
      </div>
      <!-- 每扇门两列:左边是"哪个库",右边是"到什么时候关"。两件事都堆在左边时,
           人要在一行里数着分隔点找那个时间。 -->
      <div class="pbody">
        <div v-for="w in openWindows" :key="w.id" class="pw">
          <div class="pwl">
            <div class="pwn">{{ w.name }}</div>
            <div class="pwr">{{ w.reason || $t('ewNoReason') }}</div>
          </div>
          <div class="pwm"><span class="chip">{{ windowScope(w) }}</span></div>
          <div class="pwm"><span class="chip">{{ windowWhen(w) }}</span></div>
        </div>
      </div>
    </div>

    <!-- 待执行队列。批完了、还等着有人去按下那一下的单子;排在"等我审批"前面 ——
         审批是在等别人,这些是在等你。 -->
    <div v-if="runCount" class="card">
      <div class="chead">
        <div class="cic"><Play :size="17" color="var(--accent-text)" /></div>
        <div class="grow"><div class="ct">{{ $t('dashRunTitle') }}</div><div class="cs">{{ $t('dashRunSub') }}</div></div>
        <span class="cnum">{{ runCount }}</span>
      </div>
      <div class="rows">
        <div v-for="a in pendingRun" :key="'ap' + a.id" class="row">
          <div class="rleft">
            <span class="apno" :title="a.apNo">{{ a.apNo }}</span>
            <span class="tag" :class="{ high: envtier.tierOf(a.env)?.dangerBanner }">{{ a.env }}</span>
            <span class="tag">{{ a.instance }}</span>
          </div>
          <!-- 命令用代码片显示,单行截断;完整命令在确认框里(那才是要看清的地方)。 -->
          <code class="code" :title="a.command">{{ a.command }}</code>
          <div class="rright">
            <span class="who" :title="absTime(a.createdAt)">{{ a.initiator }} · {{ relTime(a.createdAt) }}</span>
            <button v-if="a.canExecute" class="btn primary" :disabled="busy" @click.stop="runNow(a)">
              <Play :size="12" />{{ $t('apExecute') }}
            </button>
            <!-- 批是批了,但可以不跑。撤回只对**没跑过**的单子亮(canCancel 由服务端算),
                 所以这一列上它和"执行"是一对:要么下发,要么放弃这次下发。 -->
            <button v-if="a.canCancel" class="btn ghost" :disabled="busy" @click.stop="cancelNow(a)">
              <Undo2 :size="12" />{{ $t('apCancel') }}
            </button>
            <button class="btn ghost" @click.stop="go('/approvals')">{{ $t('dashDetail') }}</button>
          </div>
        </div>
        <div v-for="r in pendingRelease" :key="'rel' + r.id" class="row">
          <div class="rleft">
            <Rocket :size="12" color="var(--text-faint)" />
            <span class="apno">{{ r.relNo }}</span>
            <span class="tag" :class="{ high: envtier.tierOf(r.env)?.dangerBanner }">{{ r.env }}</span>
            <span class="tag">{{ r.instance }} / {{ r.database }}</span>
          </div>
          <!-- 停在哪一个节点上要说出来:一张升级单可能停在执行闸,也可能停在流程里
               配置的确认点,点进去要找的东西不一样。 -->
          <code class="code plain" :title="r.title">{{ r.title }} · {{ $t('dashRunStage', { stage: humanGateOf(r)?.name || '' }) }}</code>
          <div class="rright">
            <button class="btn ghost" @click.stop="go('/releases')">{{ $t('dashDetail') }}<ArrowRight :size="12" /></button>
          </div>
        </div>
      </div>
    </div>

    <!-- 班车:等审批的排在前面(它要人去做一件事),已批准还没到点的排后面。
         已经开着的不在这里重复 —— 上面那块面板已经用更重的方式说过了。 -->
    <div v-if="can('execwindow') && (pendingWindows.length || queuedWindows.length)" class="card">
      <div class="chead">
        <div class="cic"><CalendarClock :size="17" color="var(--accent-text)" /></div>
        <div class="grow"><div class="ct">{{ $t('dashWinTitle') }}</div><div class="cs">{{ $t('dashWinSub') }}</div></div>
      </div>
      <div class="rows">
        <div v-for="w in [...pendingWindows, ...queuedWindows]" :key="'w' + w.id" class="row click" @click="go('/exec-windows')">
          <div class="rleft">
            <span class="wtag" :class="w.status === 'pending' ? 'pend' : 'ok'">
              {{ w.status === 'pending' ? $t('ewStPending') : $t('dashWinQueued') }}
            </span>
            <span class="rname">{{ w.name }}</span>
            <span v-if="w.apNo && w.status === 'pending'" class="apno">{{ w.apNo }}</span>
          </div>
          <code class="code plain">{{ windowScope(w) }}</code>
          <div class="rright"><span class="chip">{{ windowWhen(w) }}</span></div>
        </div>
      </div>
    </div>

    <!-- 下半区 5:7。左边是要你决定的事(短、动作重),右边是刚发生的事(长、只读)。 -->
    <div class="cols">
      <div v-if="can('approve')" class="card">
        <div class="chead">
          <div class="cic"><ClipboardCheck :size="17" color="var(--accent-text)" /></div>
          <div class="grow"><div class="ct">{{ $t('dashTodoTitle') }}</div><div class="cs">{{ $t('dashTodoSub') }}</div></div>
          <span v-if="myTodo.length" class="cnum">{{ myTodo.length }}</span>
        </div>
        <div class="rows">
          <div v-for="a in myTodo.slice(0, 5)" :key="a.id" class="trow">
            <div class="tmeta">
              <span class="av">{{ initialsOf(a.initiator) }}</span>
              <div class="grow">
                <div class="rleft">
                  <span class="apno">{{ a.apNo }}</span>
                  <span class="tag" :class="{ high: envtier.tierOf(a.env)?.dangerBanner }">{{ a.env }}</span>
                </div>
                <div class="who" :title="absTime(a.createdAt)">{{ a.initiator }} · {{ relTime(a.createdAt) }}</div>
              </div>
            </div>
            <code class="code" :title="a.command">{{ a.command }}</code>
            <div v-if="a.reason" class="why">{{ a.reason }}</div>
            <div class="tacts">
              <button class="btn ghost" :disabled="busy" @click.stop="decideNow(a, false)">
                <CircleX :size="12" />{{ $t('apReject') }}
              </button>
              <button class="btn ok" :disabled="busy" @click.stop="decideNow(a, true)">
                <CircleCheckBig :size="12" />{{ $t('apApprove') }}
              </button>
            </div>
          </div>
          <!-- 空态说的是"都处理完了",不是"没有数据"。这两句话对读的人意义完全不同。 -->
          <div v-if="!myTodo.length" class="blank">
            <div class="bic"><CheckCheck :size="20" /></div>
            <div class="bt">{{ $t('dashTodoEmpty') }}</div>
            <div class="bs">{{ $t('dashTodoEmptySub') }}</div>
          </div>
        </div>
      </div>

      <div v-if="can('audit')" class="card">
        <div class="chead">
          <div class="cic"><ScrollText :size="17" color="var(--accent-text)" /></div>
          <div class="grow"><div class="ct">{{ $t('dashAuditTitle') }}</div><div class="cs">{{ $t('dashAuditSub') }}</div></div>
          <button class="ghostbtn sm" @click="go('/audit')">{{ $t('dashDetail') }}<ArrowRight :size="13" /></button>
        </div>
        <!-- 时间轴:一条竖线串起来,左边是人,右边是那一刻发生的事。 -->
        <div class="tl">
          <div v-for="r in audit.slice(0, 7)" :key="r.id" class="tli click" @click="go('/audit')">
            <span class="av sm">{{ initialsOf(r.actor) }}</span>
            <div class="grow">
              <div class="rleft">
                <span class="rname">{{ r.actor }}</span>
                <span class="chip">{{ r.instance }}</span>
                <span v-if="r.database" class="chip">{{ r.database }}</span>
                <span class="cap" :class="resultCls(r.result)">{{ r.result }}</span>
                <span class="ago" :title="absTime(r.occurredAt)">{{ relTime(r.occurredAt) }}</span>
              </div>
              <code class="code" :title="r.command">{{ r.command }}</code>
            </div>
          </div>
          <div v-if="!audit.length" class="blank">
            <div class="bic"><ScrollText :size="20" /></div>
            <div class="bt">{{ $t('dashAuditEmpty') }}</div>
          </div>
        </div>
      </div>
    </div>

    <div class="card">
      <div class="chead">
        <div class="cic"><Database :size="17" color="var(--accent-text)" /></div>
        <div class="grow"><div class="ct">{{ $t('dashInstTitle') }}</div><div class="cs">{{ $t('dashInstSub') }}</div></div>
      </div>
      <div class="rows">
        <div
          v-for="g in byTier" :key="g.code"
          class="row" :class="{ click: can('terminal') }"
          @click="can('terminal') && go('/terminal')"
        >
          <div class="rleft">
            <span class="d" :class="envtier.dotFor(g.code)" />
            <span class="rname">{{ g.label }}</span>
            <span v-if="g.danger" class="tag high">{{ $t('dashTierGated') }}</span>
          </div>
          <code class="code plain">{{ g.conns.map((c) => c.name).join(' · ') }}</code>
          <div class="rright"><span class="cnum">{{ g.conns.length }}</span></div>
        </div>
        <div v-if="!byTier.length" class="blank"><div class="bt">{{ $t('dashInstEmpty') }}</div></div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* 与其余内容页同源:留白、最大宽度、超宽屏居中都来自同两个 token。 */
.page { flex: 1; min-height: 0; padding: var(--page-pad); max-width: var(--page-max); margin-inline: auto; width: 100%; display: flex; flex-direction: column; gap: 16px; }
/* 见 ProjectsView:弹性子项默认可压缩,而卡片是 overflow:hidden 的,不钉住就会被压扁
   到刚好填满视口,后面的行看着像"只显示前几个"。让页面去滚,卡片保持自身高度。 */
.page > * { flex-shrink: 0; }

/* ---------------- 页眉 ---------------- */
.head { display: flex; align-items: flex-start; gap: 10px; margin-bottom: 2px; }
.hleft { flex: 1; min-width: 0; }
.eyebrow { font: 500 11px var(--font-mono); letter-spacing: 0.12em; color: var(--text-faint); text-transform: uppercase; }
.htitle { margin-top: 4px; font: 700 20px var(--font-display); color: var(--text-strong); letter-spacing: -0.02em; }
.sub { margin-top: 3px; font: 500 12.5px var(--font-body); color: var(--text-muted); }
.cta {
  display: inline-flex; align-items: center; gap: 7px; padding: 0 16px; height: var(--control-md);
  border: 0; border-radius: var(--radius-md); background: var(--accent); color: #fff; cursor: pointer;
  font: 600 12.5px var(--font-body); transition: background var(--dur-fast) var(--ease-out);
}
.cta:hover { background: var(--accent-hover); }
.ghostbtn {
  display: inline-flex; align-items: center; gap: 6px; padding: 0 12px; height: var(--control-md);
  border: 1px solid var(--border-default); border-radius: var(--radius-md);
  background: var(--surface-card); color: var(--text-muted); cursor: pointer; font: 600 12px var(--font-body);
}
.ghostbtn:hover:not(:disabled) { color: var(--accent-text); border-color: var(--accent-text); }
.ghostbtn:disabled { opacity: 0.55; cursor: default; }
.ghostbtn.sm { height: 28px; padding: 0 10px; font-size: 11.5px; }

/* ---------------- KPI ---------------- */
.stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(190px, 1fr)); gap: 14px; }
.stat {
  position: relative; padding: 15px 18px; border-radius: var(--radius-lg);
  background: var(--surface-card); border: 1px solid var(--border-subtle);
  transition: box-shadow var(--dur-fast) var(--ease-out), border-color var(--dur-fast) var(--ease-out);
}
.stat.click { cursor: pointer; }
.stat.click:hover { box-shadow: var(--shadow-sm); border-color: var(--border-default); }
/* 数字用等宽:五张牌并排时,比例字体下"11"和"0.7ms"的基线宽度不一样,一列数字
   扫下去会左右跳。 */
.sv { font: 700 25px/1.1 var(--font-mono); color: var(--text-strong); letter-spacing: -0.02em; }
.sl { margin-top: 5px; font: 500 11.5px var(--font-body); color: var(--text-muted); }
/* 图标坐在一小块淡底里,而不是裸挂在角上 —— 裸图标和数字抢的是同一种注意力。 */
.si {
  position: absolute; top: 14px; right: 16px; width: 28px; height: 28px; border-radius: 9px;
  display: grid; place-items: center; background: var(--surface-sunken); color: var(--text-muted);
}
/* act:要人动手的两张牌。左边一条竖条 + 一层极淡的底,数字换成强调色。
   为 0 时它和别的牌一模一样 —— 一张长期亮着的警示牌很快就没人看了。 */
.stat.act { background: var(--accent-subtle); border-color: var(--accent-subtle-border); }
.stat.act::before { content: ''; position: absolute; left: 0; top: 12px; bottom: 12px; width: 3px; border-radius: 0 3px 3px 0; background: var(--accent); }
.stat.act .sv { color: var(--accent-text); }
.stat.act .si { background: var(--surface-card); color: var(--accent-text); }
/* warn:门开着。它比"有活要干"更重一档,所以用琥珀。 */
.stat.warn { background: var(--warning-subtle); border-color: transparent; }
.stat.warn::before { content: ''; position: absolute; left: 0; top: 12px; bottom: 12px; width: 3px; border-radius: 0 3px 3px 0; background: var(--warning); }
.stat.warn .sv { color: var(--warning-text); }
.stat.warn .si { background: var(--surface-card); color: var(--warning-text); }

/* ---------------- 当前执行窗口面板 ---------------- */
.panel { border-radius: var(--radius-lg); background: var(--surface-card); border: 1px solid var(--warning); overflow: hidden; }
.phead { display: flex; align-items: center; gap: 11px; padding: 13px 18px; background: var(--warning-subtle); }
.pt { font: 700 13.5px var(--font-body); color: var(--warning-text); }
.ps { margin-top: 2px; font: 500 11.5px var(--font-body); color: var(--text-muted); }
/* 呼吸点:它表示"此刻正开着",而这块面板存在的全部理由就是这件事。 */
.pulse { position: relative; width: 9px; height: 9px; border-radius: 50%; background: var(--warning); flex-shrink: 0; }
.pulse::after { content: ''; position: absolute; inset: -4px; border-radius: 50%; border: 2px solid var(--warning); animation: ping 1.8s cubic-bezier(0, 0, 0.2, 1) infinite; }
@keyframes ping { 0% { transform: scale(0.7); opacity: 0.9; } 100% { transform: scale(1.6); opacity: 0; } }
@media (prefers-reduced-motion: reduce) { .pulse::after { animation: none; opacity: 0.35; } }
.pbody { padding: 6px 18px 14px; }
.pw { display: grid; grid-template-columns: minmax(0, 1.4fr) minmax(0, 1fr) minmax(0, 1fr); gap: 12px; align-items: center; padding: 10px 0; border-bottom: 1px solid var(--border-subtle); }
.pw:last-child { border-bottom: none; }
.pwn { font: 600 13px var(--font-body); color: var(--text-strong); }
.pwr { margin-top: 2px; font: 500 11.5px var(--font-body); color: var(--text-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pwm { min-width: 0; }

/* ---------------- 卡片 ---------------- */
.cols { display: grid; grid-template-columns: minmax(0, 5fr) minmax(0, 7fr); gap: 16px; align-items: start; }
/* 1366 及以下换成一栏:两栏各剩 600px 出头时,右边那条命令已经只剩几个字。 */
@media (max-width: 1366px) { .cols { grid-template-columns: minmax(0, 1fr); } }
.card { border-radius: var(--radius-lg); background: var(--surface-card); border: 1px solid var(--border-subtle); overflow: hidden; }
.chead { display: flex; align-items: center; gap: 12px; padding: 14px 18px; border-bottom: 1px solid var(--border-subtle); }
.cic { width: 32px; height: 32px; border-radius: 10px; background: var(--accent-subtle); display: grid; place-items: center; flex-shrink: 0; }
.ct { font: 700 13.5px var(--font-display); color: var(--text-strong); }
.cs { font: 500 11.5px var(--font-body); color: var(--text-muted); margin-top: 2px; }
.cnum { display: inline-flex; align-items: center; height: 20px; padding: 0 8px; border-radius: var(--radius-full); background: var(--surface-sunken); border: 1px solid var(--border-subtle); font: 700 11px var(--font-mono); color: var(--text-muted); }
.grow { flex: 1; min-width: 0; }

/* ---------------- 行 ---------------- */
.rows { padding: 8px 10px; display: flex; flex-direction: column; }
.row {
  display: grid; grid-template-columns: minmax(0, auto) minmax(0, 1fr) minmax(0, auto);
  align-items: center; gap: 14px; padding: 11px 10px; border-radius: var(--radius-md);
  transition: background var(--dur-fast) var(--ease-out);
}
.row + .row { border-top: 1px solid var(--border-subtle); border-radius: 0; }
.row.click { cursor: pointer; }
.row:hover { background: var(--surface-sunken); }
.rleft { display: flex; align-items: center; gap: 7px; min-width: 0; }
.rright { display: flex; align-items: center; gap: 8px; justify-content: flex-end; }
.rname { font: 600 12.5px var(--font-body); color: var(--text-strong); white-space: nowrap; }
/* 单号是等宽小徽章:它是要被念出来、被复制、被粘到聊天里的东西。 */
.apno { padding: 1px 7px; border-radius: var(--radius-sm); background: var(--accent-subtle); color: var(--accent-text); font: 700 11px var(--font-mono); white-space: nowrap; }
.tag { padding: 1px 7px; border-radius: var(--radius-full); background: var(--surface-sunken); border: 1px solid var(--border-subtle); font: 600 10px var(--font-mono); color: var(--text-muted); text-transform: uppercase; white-space: nowrap; }
.tag.high { background: var(--danger-subtle); border-color: transparent; color: var(--danger-text); }
.chip { display: inline-block; max-width: 100%; padding: 2px 8px; border-radius: var(--radius-sm); background: var(--surface-sunken); font: 500 11px var(--font-mono); color: var(--text-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* 命令用代码片。它在这一页永远是**一行**:落地页给的是"有这么一条",要读全文
   得进它自己的页面(或者看确认框)。 */
.code { display: block; min-width: 0; padding: 4px 9px; border-radius: var(--radius-sm); background: var(--surface-sunken); border: 1px solid var(--border-subtle); font: 500 11.5px var(--font-mono); color: var(--text-body); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.code.plain { background: transparent; border-color: transparent; color: var(--text-muted); padding-left: 0; }
.who { font: 500 11px var(--font-body); color: var(--text-faint); white-space: nowrap; }
.wtag { display: inline-flex; align-items: center; height: 18px; padding: 0 7px; border-radius: var(--radius-full); font: 700 10px var(--font-mono); white-space: nowrap; }
.wtag.pend { background: var(--warning-subtle); color: var(--warning-text); }
.wtag.ok { background: var(--surface-sunken); color: var(--text-muted); border: 1px solid var(--border-subtle); }
.d { width: 7px; height: 7px; border-radius: 50%; flex-shrink: 0; background: var(--text-faint); }
.d.danger { background: var(--danger); }
.d.warning { background: var(--warning); }
.d.success { background: var(--success); }
.d.info { background: var(--accent); }
.d.muted { background: var(--text-faint); }

/* ---------------- 行内按钮 ---------------- */
.btn {
  display: inline-flex; align-items: center; gap: 5px; height: 27px; padding: 0 11px;
  border: 1px solid var(--border-default); border-radius: var(--radius-sm);
  background: var(--surface-card); color: var(--text-muted); font: 600 11.5px var(--font-body);
  cursor: pointer; white-space: nowrap;
}
.btn:disabled { opacity: 0.55; cursor: default; }
/* 执行是这一页唯一会真的落到库上的动作,所以只有它是实色。 */
.btn.primary { background: var(--accent); border-color: var(--accent); color: #fff; }
.btn.primary:hover:not(:disabled) { background: var(--accent-hover); }
.btn.ok:hover:not(:disabled) { color: var(--success-text); border-color: var(--success); background: var(--success-subtle); }
.btn.ghost:hover:not(:disabled) { color: var(--accent-text); border-color: var(--accent-text); }

/* ---------------- 待我审批:一行一张小卡 ---------------- */
.trow { padding: 12px 10px; display: flex; flex-direction: column; gap: 8px; }
.trow + .trow { border-top: 1px solid var(--border-subtle); }
.tmeta { display: flex; align-items: center; gap: 10px; }
.av { width: 28px; height: 28px; flex-shrink: 0; border-radius: 50%; display: grid; place-items: center; background: var(--accent-subtle); color: var(--accent-text); font: 700 11px var(--font-body); }
.av.sm { width: 24px; height: 24px; font-size: 10px; }
.why { font: 500 11.5px/1.6 var(--font-body); color: var(--text-muted); }
.tacts { display: flex; justify-content: flex-end; gap: 8px; }

/* ---------------- 审计时间轴 ---------------- */
.tl { padding: 10px 18px 14px; position: relative; }
.tli { display: flex; gap: 11px; padding: 9px 0; cursor: pointer; }
.tli + .tli { border-top: 1px solid var(--border-subtle); }
.tli:hover .rname { color: var(--accent-text); }
.ago { margin-left: auto; font: 500 10.5px var(--font-mono); color: var(--text-faint); white-space: nowrap; }
/* 状态胶囊。executed 用翠绿,pending 琥珀,撤回/导出中性 —— 常态不该是满屏彩色。 */
.cap { padding: 1px 7px; border-radius: var(--radius-full); font: 700 9.5px var(--font-mono); text-transform: uppercase; }
.cap.ok { background: var(--success-subtle); color: var(--success-text); }
.cap.wait { background: var(--warning-subtle); color: var(--warning-text); }
.cap.bad { background: var(--danger-subtle); color: var(--danger-text); }
.cap.neutral { background: var(--surface-sunken); color: var(--text-muted); border: 1px solid var(--border-subtle); }

/* ---------------- 空态 ---------------- */
/* 说的是"都处理完了",不是"没有数据" —— 对读的人这是两句完全不同的话。 */
.blank { padding: 28px 12px; display: flex; flex-direction: column; align-items: center; gap: 6px; text-align: center; }
.bic { width: 40px; height: 40px; border-radius: 50%; display: grid; place-items: center; background: var(--surface-sunken); color: var(--text-faint); }
.bt { font: 600 12.5px var(--font-body); color: var(--text-muted); }
.bs { font: 500 11.5px var(--font-body); color: var(--text-faint); }

/* 窄屏:行内三段改成上下堆叠,免得命令被挤成几个字。 */
@media (max-width: 720px) {
  .row { grid-template-columns: minmax(0, 1fr); gap: 8px; }
  .rright { justify-content: flex-start; }
  .pw { grid-template-columns: minmax(0, 1fr); gap: 6px; }
}
</style>
