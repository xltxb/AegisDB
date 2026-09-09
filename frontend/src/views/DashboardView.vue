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
  ScrollText, ArrowRight, ShieldAlert, Play, Rocket,
} from 'lucide-vue-next'
import api from '@/api'
import { useAuthStore } from '@/stores/auth'
import { useEnvTierStore } from '@/stores/envtier'
import { useUIStore } from '@/stores/ui'
import { awaitsExecution, humanGateOf } from '@/lib/pendingWork'
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

/** 此刻开着的窗口。active 由后端用与判定完全相同的逻辑算出,前端不自己算。 */
const openWindows = computed(() => windows.value.filter((w) => w.active))

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

function windowScope(w: ExecWindow) {
  const c = conns.value.find((x) => x.id === w.connectionId)
  return (c?.name || '#' + w.connectionId) + ' / ' + w.database
}

function go(path: string) { router.push(path) }
</script>

<template>
  <div class="scy page">
    <div class="head">
      <div>
        <div class="eyebrow">OVERVIEW</div>
        <div class="sub">{{ $t('dashGreeting', { name: auth.me?.name || '' }) }}</div>
      </div>
      <button v-if="can('terminal')" class="cta" @click="go('/terminal')">
        <SquareTerminal :size="15" />{{ $t('dashOpenTerminal') }}
      </button>
    </div>

    <div class="stats">
      <div v-if="can('approve')" class="stat click" @click="go('/approvals')">
        <div class="si"><ClipboardCheck :size="15" /></div>
        <div class="sv">{{ myTodo.length }}</div>
        <div class="sl">{{ $t('dashStatTodo') }}</div>
      </div>
      <div v-if="can('approve') || can('pipeline')" class="stat click" :class="{ warn: runCount }" @click="go(pendingRun.length ? '/approvals' : '/releases')">
        <div class="si"><Play :size="15" /></div>
        <div class="sv">{{ runCount }}</div>
        <div class="sl">{{ $t('dashStatToRun') }}</div>
      </div>
      <div class="stat" :class="{ warn: openWindows.length }">
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
        <div class="sv">{{ stats ? stats.p50Ms.toFixed(1) + 'ms' : '—' }}</div>
        <div class="sl">{{ stats?.online ? $t('dashStatGwOn') : $t('dashStatGwOff') }}</div>
      </div>
    </div>

    <!-- 开着的执行窗口。它排在所有列表前面,而且用警示色:窗口开着的这几个小时里,
         本来要审批的中高风险语句是直接放行的 —— 那是这个系统里唯一一种"门开着而
         没人站在门口"的状态,进来第一眼就该看见。 -->
    <div v-if="openWindows.length" class="alert">
      <div class="ai"><ShieldAlert :size="17" /></div>
      <div class="grow">
        <div class="at">{{ $t('dashWindowOpen', { n: openWindows.length }) }}</div>
        <div v-for="w in openWindows" :key="w.id" class="aw">
          <b>{{ w.name }}</b><span class="sep"> · </span>{{ windowScope(w) }}
          <template v-if="w.reason"><span class="sep"> · </span>{{ w.reason }}</template>
        </div>
      </div>
    </div>

    <!-- 待执行:批完了、但还得有人去按下那一下的单子。它排在"等我审批"前面 ——
         审批是在等别人,这些是在等你。 -->
    <div v-if="runCount" class="card">
      <div class="chead">
        <div class="cic"><Play :size="17" color="var(--accent-text)" /></div>
        <div><div class="ct">{{ $t('dashRunTitle') }}</div><div class="cs">{{ $t('dashRunSub') }}</div></div>
      </div>
      <div class="rows">
        <div v-for="a in pendingRun" :key="'ap' + a.id" class="row click" @click="go('/approvals')">
          <div class="grow">
            <div class="rn">
              <span class="mono">{{ a.apNo }}</span>
              <span class="tag">{{ a.env }}</span>
              <span class="on">{{ a.instance }}</span>
            </div>
            <div class="rm"><span class="clip mono">{{ a.command }}</span></div>
          </div>
          <span class="go">{{ $t('dashRunGo') }}<ArrowRight :size="13" /></span>
        </div>
        <div v-for="r in pendingRelease" :key="'rel' + r.id" class="row click" @click="go('/releases')">
          <div class="grow">
            <div class="rn">
              <Rocket :size="12" color="var(--text-faint)" />
              <span class="mono">{{ r.relNo }}</span>
              <span class="tag">{{ r.env }}</span>
              <span class="on">{{ r.instance }} / {{ r.database }}</span>
            </div>
            <!-- 停在哪一个节点上要说出来:一张升级单可能停在执行闸,也可能停在流程里
                 配置的确认点,点进去要找的东西不一样。 -->
            <div class="rm"><span class="clip">{{ r.title }} · {{ $t('dashRunStage', { stage: humanGateOf(r)?.name || '' }) }}</span></div>
          </div>
          <span class="go">{{ $t('dashRunGo') }}<ArrowRight :size="13" /></span>
        </div>
      </div>
    </div>

    <div class="cols">
      <div v-if="can('approve')" class="card">
        <div class="chead">
          <div class="cic"><ClipboardCheck :size="17" color="var(--accent-text)" /></div>
          <div><div class="ct">{{ $t('dashTodoTitle') }}</div><div class="cs">{{ $t('dashTodoSub') }}</div></div>
        </div>
        <div class="rows">
          <div v-for="a in myTodo.slice(0, 5)" :key="a.id" class="row click" @click="go('/approvals')">
            <div class="grow">
              <div class="rn"><span class="mono">{{ a.apNo }}</span><span class="tag">{{ a.env }}</span></div>
              <div class="rm"><span class="clip mono">{{ a.command }}</span></div>
            </div>
            <span class="go">{{ when(a.createdAt) }}<ArrowRight :size="13" /></span>
          </div>
          <div v-if="!myTodo.length" class="empty">{{ $t('dashTodoEmpty') }}</div>
        </div>
      </div>

      <div v-if="can('audit')" class="card">
        <div class="chead">
          <div class="cic"><ScrollText :size="17" color="var(--accent-text)" /></div>
          <div><div class="ct">{{ $t('dashAuditTitle') }}</div><div class="cs">{{ $t('dashAuditSub') }}</div></div>
        </div>
        <div class="rows">
          <div v-for="r in audit.slice(0, 6)" :key="r.id" class="row click" @click="go('/audit')">
            <div class="grow">
              <div class="rn">
                {{ r.actor }}
                <span class="on">{{ r.instance }}</span>
                <span class="tag" :class="r.risk">{{ r.risk }}</span>
                <span v-if="r.result !== 'executed'" class="tag" :class="r.result">{{ r.result }}</span>
              </div>
              <div class="rm"><span class="clip mono">{{ r.command }}</span></div>
            </div>
            <span class="go">{{ when(r.occurredAt) }}<ArrowRight :size="13" /></span>
          </div>
          <div v-if="!audit.length" class="empty">{{ $t('dashAuditEmpty') }}</div>
        </div>
      </div>
    </div>

    <div class="card">
      <div class="chead">
        <div class="cic"><Database :size="17" color="var(--accent-text)" /></div>
        <div><div class="ct">{{ $t('dashInstTitle') }}</div><div class="cs">{{ $t('dashInstSub') }}</div></div>
      </div>
      <div class="rows">
        <div
          v-for="g in byTier" :key="g.code"
          class="row" :class="{ click: can('terminal') }"
          @click="can('terminal') && go('/terminal')"
        >
          <span class="d" :class="envtier.dotFor(g.code)" />
          <div class="grow">
            <div class="rn">{{ g.label }}<span v-if="g.danger" class="tag high">{{ $t('dashTierGated') }}</span></div>
            <div class="rm"><span class="clip">{{ g.conns.map((c) => c.name).join(' · ') }}</span></div>
          </div>
          <span class="cnt">{{ g.conns.length }}</span>
        </div>
        <div v-if="!byTier.length" class="empty">{{ $t('dashInstEmpty') }}</div>
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
.head { display: flex; align-items: center; gap: 12px; margin-bottom: 2px; }
.eyebrow { font: 500 11px var(--font-mono); letter-spacing: 0.12em; color: var(--text-faint); text-transform: uppercase; }
.sub { font: 500 13px var(--font-body); color: var(--text-muted); margin-top: 4px; }
.cta {
  margin-left: auto; display: inline-flex; align-items: center; gap: 7px; padding: 0 16px; height: var(--control-md);
  border: 0; border-radius: 11px; background: var(--accent); color: #fff; cursor: pointer;
  font: 600 12.5px var(--font-body); transition: background var(--dur-fast, 0.15s) var(--ease-out, ease);
}
.cta:hover { background: var(--accent-hover); }

.stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 14px; }
.stat { position: relative; padding: 16px 20px; border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-xs); }
.stat.click { cursor: pointer; }
/* 有窗口开着的时候这张牌换成警示色。数字是 0 的时候它和别的牌一样安静 —— 一张
   长期亮着的警示牌很快就没人看了。 */
.stat.warn { background: var(--warning-subtle); }
.stat.warn .sv { color: var(--warning-text); }
.si { position: absolute; top: 16px; right: 18px; color: var(--text-faint); }
.sv { font: 700 26px var(--font-display); color: var(--text-strong); }
.sl { margin-top: 2px; font: 500 11.5px var(--font-body); color: var(--text-muted); }

.alert { display: flex; gap: 12px; padding: 14px 18px; border-radius: var(--radius-lg); background: var(--warning-subtle); border: 1px solid var(--warning); }
.ai { color: var(--warning-text); flex-shrink: 0; margin-top: 1px; }
.at { font: 600 13px var(--font-body); color: var(--warning-text); }
.aw { margin-top: 4px; font: 500 12px var(--font-body); color: var(--text-muted); }
.aw b { color: var(--text-strong); font-weight: 600; }
.sep { color: var(--text-faint); }

/* 两栏在放不下时自己变一栏,而不是把命令文本挤成一列窄条。 */
.cols { display: grid; grid-template-columns: repeat(auto-fit, minmax(340px, 1fr)); gap: 16px; align-items: start; }
.card { border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-xs); overflow: hidden; }
.chead { display: flex; align-items: center; gap: 12px; padding: 16px 20px; border-bottom: 1px solid var(--border-subtle); }
.cic { width: 34px; height: 34px; border-radius: 10px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; }
.ct { font: 600 14px var(--font-display); color: var(--text-strong); }
.cs { font: 500 12px var(--font-body); color: var(--text-muted); margin-top: 2px; }

/* 行样式与项目页同源 —— 同一个产品里的列表行只该有一种长相。 */
.rows { padding: 14px 20px; display: flex; flex-direction: column; gap: 8px; }
.row {
  display: flex; align-items: center; gap: 12px; padding: 10px 12px;
  border: 1px solid var(--border-default); border-radius: 11px; background: var(--surface-sunken);
  transition: border-color var(--dur-fast, 0.15s) var(--ease-out, ease);
}
.row.click { cursor: pointer; }
.row.click:hover { border-color: var(--accent-text); }
.grow { flex: 1; min-width: 0; }
.rn { display: flex; align-items: center; gap: 8px; font: 600 13px var(--font-body); color: var(--text-strong); }
.rm { margin-top: 3px; font: 500 11.5px var(--font-body); color: var(--text-muted); }
/* 命令可以很长,单行截断 —— 落地页给的是"有这么一条",细节在它自己的页面上。 */
.clip { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.mono { font-family: var(--font-mono); }
.tag { padding: 1px 7px; border-radius: 999px; background: var(--surface-sunken); border: 1px solid var(--border-subtle); font: 600 10px var(--font-mono); color: var(--text-muted); text-transform: uppercase; }
.tag.high, .tag.rejected { background: var(--danger-subtle); border-color: transparent; color: var(--danger-text); }
.tag.mid, .tag.pending, .tag.warn { background: var(--warning-subtle); border-color: transparent; color: var(--warning-text); }
.on { font: 500 11.5px var(--font-mono); color: var(--text-muted); }
.cnt { font: 600 12px var(--font-mono); color: var(--text-muted); }
.d { width: 7px; height: 7px; border-radius: 50%; flex-shrink: 0; background: var(--text-faint); }
.d.danger { background: var(--danger); }
.d.warning { background: var(--warning); }
.d.success { background: var(--success); }
.d.info { background: var(--accent); }
.d.muted { background: var(--text-faint); }
.go { display: inline-flex; align-items: center; gap: 5px; font: 600 11.5px var(--font-mono); color: var(--text-faint); }
.row.click:hover .go { color: var(--accent-text); }
.empty { padding: 10px 2px; font: 500 12px var(--font-body); color: var(--text-faint); }
</style>
