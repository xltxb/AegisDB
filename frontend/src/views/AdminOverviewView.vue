<script setup lang="ts">
// 管理总览:管理后台的首页,一页看全局(管理后台拆分,工单 06)。
//
// 四块:实例连通状态、待审批(最久没人处理的在前)、在线用户、最近的高危操作。全部来自
// 已有接口 —— 实例列表(管理员看得到全部,带上次巡检结论)、审批单列表、在线用户、全量审计 ——
// 没有另开聚合接口:这些接口各自已有平台管理员的校验,另开一个就多一处要守的门。
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Database, ClipboardCheck, ShieldAlert, RotateCw, CircleAlert, CircleCheck, CircleHelp, Wrench, TriangleAlert } from 'lucide-vue-next'
import api from '@/api'
import OnlineUsersPanel from '@/components/perms/OnlineUsersPanel.vue'
import { useEnvTierStore } from '@/stores/envtier'
import { useAuthStore } from '@/stores/auth'
import { useInspectionStore } from '@/stores/inspection'
import { relTime, absTime } from '@/lib/relTime'
import type { Approval, AuditRow, Connection } from '@/types'

const { t } = useI18n()
const router = useRouter()
const envtier = useEnvTierStore()
const auth = useAuthStore()
// 今日巡检(.scratch/inspection-report 工单 05):今天严重 / 警告的实例、今天没收到报告的实例
const insp = useInspectionStore()
const showMissing = ref(false)
const inspToday = computed(() => insp.rows.filter((r) => r.report && r.report.reportDate === insp.today))
const inspIssues = computed(() => inspToday.value
  .filter((r) => r.report!.level !== 'ok')
  .sort((a, b) => (a.report!.level === 'critical' ? 0 : 1) - (b.report!.level === 'critical' ? 0 : 1) || a.instance.localeCompare(b.instance)))
const inspMissing = computed(() => insp.rows.filter((r) => !r.report || r.report.reportDate !== insp.today))
const inspCount = (lv: string) => inspToday.value.filter((r) => r.report!.level === lv).length
const inspTier = (env: string) => (envtier.tierOf(env)?.code || env || '').toUpperCase()
function openInsp(connId: number, reportId?: number) {
  router.push({ path: '/inspection', query: reportId ? { instance: String(connId), report: String(reportId) } : { instance: String(connId) } })
}

const conns = ref<Connection[]>([])
const checkOn = ref<boolean | null>(null) // null = 读不到设置
const pendingTotal = ref(0)
const oldest = ref<Approval[]>([])
const highs = ref<AuditRow[]>([])
const busy = ref(false)

// 实例状态:巡检失败 > 维护中 > 从没探过 > 正常。从没探过不等于正常 —— 不替它担保。
type Health = 'fail' | 'maint' | 'unknown' | 'ok'
function health(c: Connection): Health {
  if (c.lastCheckOk === false) return 'fail'
  if (c.status === 'maint') return 'maint'
  if (c.lastCheckOk === undefined || c.lastCheckOk === null) return 'unknown'
  return 'ok'
}
const rank: Record<Health, number> = { fail: 0, maint: 1, unknown: 2, ok: 3 }
const counts = computed(() => {
  const n = { fail: 0, maint: 0, unknown: 0, ok: 0 }
  for (const c of conns.value) n[health(c)]++
  return n
})
// 按分层归堆(分层表的顺序,生产在上),每堆里异常的置顶
const byTier = computed(() => {
  const groups = new Map<string, { code: string; label: string; conns: Connection[] }>()
  for (const c of conns.value) {
    const code = envtier.tierOf(c.env)?.code ?? ''
    if (!groups.has(code)) groups.set(code, { code, label: code ? envtier.tierLabel(code, t) : t('dashTierUnknown'), conns: [] })
    groups.get(code)!.conns.push(c)
  }
  const order = envtier.tierCodes
  const out = [...groups.values()].sort((a, b) => {
    const ia = order.indexOf(a.code), ib = order.indexOf(b.code)
    return (ia < 0 ? 99 : ia) - (ib < 0 ? 99 : ib)
  })
  for (const g of out) g.conns.sort((a, b) => rank[health(a)] - rank[health(b)] || a.name.localeCompare(b.name))
  return out
})

// 布局(2026-10 用户反馈「页面布局太奇怪」):从前三栏并排,实例一栏把 90 多个实例逐行列到底,
// 旁边两栏只有几行、下面一大片空白,而真正要看的只有那两三个巡检失败的。现在:要处理的(异常
// 实例)整行置顶、按严重程度排并标出分层;正常的默认收起成一个数字,要看再展开成多列网格。
const showOk = ref(false)
const abnormal = computed(() => byTier.value
  .flatMap((g, ti) => g.conns.filter((c) => health(c) !== 'ok').map((c) => ({ c, tier: (g.code || '?').toUpperCase(), ti })))
  .sort((a, b) => rank[health(a.c)] - rank[health(b.c)] || a.ti - b.ti || a.c.name.localeCompare(b.c.name)))
const okByTier = computed(() => byTier.value
  .map((g) => ({ ...g, conns: g.conns.filter((c) => health(c) === 'ok') }))
  .filter((g) => g.conns.length))

async function loadConns() {
  try { await envtier.load() } catch { /* 分层读不到就全部归"未知分层",照样列出来 */ }
  try { conns.value = await api.connections() } catch { conns.value = [] }
  try {
    const s = await api.settings()
    checkOn.value = JSON.parse(s.settings?.['conn.check.enabled'] ?? 'false') === true
  } catch { checkOn.value = null }
}

// 待审批:列表按 id 倒序,最后一页就是最久的那几张。
async function loadPending() {
  try {
    const head = await api.approvals('all', 1, 1, 'pending')
    pendingTotal.value = head.total
    if (!head.total) { oldest.value = []; return }
    const size = 5
    const last = Math.max(1, Math.ceil(head.total / size))
    const page = await api.approvals('all', last, size, 'pending')
    let items = page.items
    if (items.length < size && last > 1) { // 最后一页不满一页:从前一页补齐
      const prev = await api.approvals('all', last - 1, size, 'pending')
      items = [...prev.items, ...items]
    }
    oldest.value = items.slice().sort((a, b) => a.id - b.id).slice(0, size)
  } catch { pendingTotal.value = 0; oldest.value = [] }
}

async function loadHighs() {
  try {
    const r = await api.audit({ risk: 'high', range: '7d', page: 1, pageSize: 8, full: true })
    highs.value = r.items
  } catch { highs.value = [] }
}

async function load() {
  busy.value = true
  await Promise.all([loadConns(), loadPending(), loadHighs(), insp.load(true)])
  busy.value = false
}
onMounted(load)

const ago = (s?: string | null) => relTime(t as never, s)
const clip = (s: string, n = 90) => (s.length > n ? s.slice(0, n) + '…' : s)
</script>

<template>
  <div class="scy page">
    <div class="v-head">
      <div class="v-head-l">
        <div class="v-eyebrow">ADMIN</div>
        <div class="v-h1">{{ $t('t_adminhome') }}</div>
        <div class="v-sub">{{ $t('t_adminhomeSub') }}</div>
      </div>
      <button class="ghostbtn" :disabled="busy" @click="load()"><RotateCw :size="14" />{{ $t('dashRefresh') }}</button>
    </div>

    <div class="stats">
      <div class="stat click" :class="{ bad: counts.fail }" data-testid="ao-stat-conn" @click="router.push('/admin/connections')">
        <div class="si"><Database :size="15" /></div>
        <div class="sv">{{ counts.fail }}<span class="of"> / {{ conns.length }}</span></div>
        <div class="sl">{{ $t('aoStatConnFail') }}</div>
      </div>
      <div class="stat click" :class="{ act: pendingTotal }" data-testid="ao-stat-pending" @click="router.push('/approvals')">
        <div class="si"><ClipboardCheck :size="15" /></div>
        <div class="sv">{{ pendingTotal }}</div>
        <div class="sl">{{ $t('aoStatPending') }}</div>
      </div>
      <div class="stat click" :class="{ bad: highs.length }" data-testid="ao-stat-high" @click="router.push('/admin/audit')">
        <div class="si"><ShieldAlert :size="15" /></div>
        <div class="sv">{{ highs.length }}{{ highs.length >= 8 ? '+' : '' }}</div>
        <div class="sl">{{ $t('aoStatHigh') }}</div>
      </div>
    </div>

    <!-- 实例连通状态:整行。异常的置顶,正常的收起成一个数字 -->
    <div class="card conncard">
      <div class="chead">
        <div class="ct">{{ $t('aoConnTitle') }}</div>
        <div class="cs">
          {{ $t('aoConnSummary', { fail: counts.fail, maint: counts.maint, unknown: counts.unknown, ok: counts.ok }) }}
        </div>
        <span v-if="counts.ok" class="link" data-testid="ao-toggle-ok" @click="showOk = !showOk">
          {{ showOk ? $t('aoHideOk') : $t('aoShowOk', { n: counts.ok }) }}
        </span>
      </div>
      <div v-if="checkOn === false" class="note" data-testid="ao-check-off">{{ $t('aoCheckOff') }}</div>
      <div v-if="!conns.length" class="empty">{{ $t('aoNoConn') }}</div>
      <div v-else-if="!abnormal.length" class="allok" data-testid="ao-all-ok"><CircleCheck :size="15" />{{ $t('aoAllOk', { n: conns.length }) }}</div>
      <div v-if="abnormal.length" class="cgrid">
        <div v-for="x in abnormal" :key="x.c.id" class="crow" :class="health(x.c)" data-testid="ao-conn">
          <CircleAlert v-if="health(x.c) === 'fail'" :size="14" class="hi" />
          <Wrench v-else-if="health(x.c) === 'maint'" :size="14" class="hi" />
          <CircleHelp v-else :size="14" class="hi" />
          <span class="cname">{{ x.c.name }}</span>
          <span class="ctier">{{ x.tier }}</span>
          <span class="ceng">{{ x.c.engine }}</span>
          <span class="cst" :title="x.c.lastCheckErr || absTime(x.c.lastCheckAt)">
            {{ $t('aoHealth_' + health(x.c)) }}<template v-if="x.c.lastCheckAt"> · {{ ago(x.c.lastCheckAt) }}</template>
          </span>
        </div>
      </div>
      <template v-if="showOk">
        <div v-for="g in okByTier" :key="g.code" class="tgroup">
          <div class="tlabel">{{ g.label }} <span class="tn">{{ g.conns.length }}</span></div>
          <div class="cgrid">
            <div v-for="c in g.conns" :key="c.id" class="crow ok" data-testid="ao-conn">
              <CircleCheck :size="14" class="hi" />
              <span class="cname">{{ c.name }}</span>
              <span class="ceng">{{ c.engine }}</span>
              <span class="cst" :title="absTime(c.lastCheckAt)">
                {{ $t('aoHealth_ok') }}<template v-if="c.lastCheckAt"> · {{ ago(c.lastCheckAt) }}</template>
              </span>
            </div>
          </div>
        </div>
      </template>
    </div>

    <!-- 今日巡检:只有能看巡检报告的角色才有这一块 -->
    <div v-if="auth.canViewInspection" class="card conncard" data-testid="ao-insp">
      <div class="chead">
        <div class="ct">{{ $t('aoInspTitle') }}</div>
        <div class="cs">{{ $t('aoInspSub', { got: inspToday.length, total: insp.rows.length, c: inspCount('critical'), w: inspCount('warning') }) }}</div>
        <span class="link" @click="router.push('/inspection')">{{ $t('aoInspViewAll') }}</span>
      </div>
      <div v-if="!insp.rows.some((r) => r.report)" class="empty">{{ $t('aoInspNone') }}</div>
      <template v-else>
        <div v-if="!inspIssues.length && !inspMissing.length" class="allok" data-testid="ao-insp-all-ok"><CircleCheck :size="15" />{{ $t('aoInspAllOk', { n: insp.rows.length }) }}</div>
        <div v-if="inspIssues.length" class="cgrid">
          <div v-for="r in inspIssues" :key="r.connectionId" class="crow click" :class="r.report!.level === 'critical' ? 'fail' : 'maint'" data-testid="ao-insp-row" @click="openInsp(r.connectionId, r.report!.id)">
            <CircleAlert v-if="r.report!.level === 'critical'" :size="14" class="hi" />
            <TriangleAlert v-else :size="14" class="hi" />
            <span class="cname">{{ r.instance }}</span>
            <span class="ctier">{{ inspTier(r.env) }}</span>
            <span class="ceng ifind" :title="r.report!.topFinding">{{ r.report!.topFinding || '' }}</span>
            <span class="cst">{{ $t('inspCounts', { c: r.report!.criticalCount, w: r.report!.warningCount, i: r.report!.infoCount }) }}</span>
          </div>
        </div>
        <div v-if="inspMissing.length" class="missing">
          <span class="link" data-testid="ao-insp-missing" @click="showMissing = !showMissing">
            {{ showMissing ? $t('aoInspMissingHide') : $t('aoInspMissing', { n: inspMissing.length }) }}
          </span>
          <div v-if="showMissing" class="mchips">
            <span v-for="r in inspMissing" :key="r.connectionId" class="mchip" data-testid="ao-insp-missing-item" @click="openInsp(r.connectionId)">{{ r.instance }}</span>
          </div>
        </div>
      </template>
    </div>

    <div class="grid2">
      <!-- 待审批(最久的在前) -->
      <div class="card">
        <div class="chead">
          <div class="ct">{{ $t('aoPendingTitle') }}</div>
          <div class="cs">{{ $t('aoPendingSub', { n: pendingTotal }) }}</div>
          <span class="link" @click="router.push('/approvals')">{{ $t('aoViewAll') }}</span>
        </div>
        <div v-if="!oldest.length" class="empty">{{ $t('aoPendingNone') }}</div>
        <div v-for="a in oldest" :key="a.id" class="arow" data-testid="ao-pending">
          <div class="atop"><b class="mono">{{ a.apNo }}</b><span class="ameta">{{ a.initiator }} · {{ a.instance }}<template v-if="a.database"> / {{ a.database }}</template></span><span class="aage">{{ ago(a.createdAt) }}</span></div>
          <div class="acmd mono">{{ clip(a.command) }}</div>
        </div>
      </div>

      <!-- 最近的高危操作 -->
      <div class="card">
        <div class="chead">
          <div class="ct">{{ $t('aoHighTitle') }}</div>
          <div class="cs">{{ $t('aoHighSub') }}</div>
          <span class="link" @click="router.push('/admin/audit')">{{ $t('aoViewAll') }}</span>
        </div>
        <div v-if="!highs.length" class="empty">{{ $t('aoHighNone') }}</div>
        <div v-for="h in highs" :key="h.id" class="arow" data-testid="ao-high">
          <div class="atop"><b>{{ h.actor }}</b><span class="ameta">{{ h.instance }}<template v-if="h.database"> / {{ h.database }}</template> · {{ h.result }}</span><span class="aage">{{ ago(h.occurredAt) }}</span></div>
          <div class="acmd mono">{{ clip(h.command) }}</div>
        </div>
      </div>
    </div>

    <!-- 在线用户:从权限页移过来 -->
    <div class="card online">
      <div class="chead"><div class="ct">{{ $t('aoOnlineTitle') }}</div></div>
      <div class="opad"><OnlineUsersPanel /></div>
    </div>
  </div>
</template>

<style scoped>
.page { padding: 22px 26px 40px; }
.card { border-radius: var(--radius-lg); background: var(--surface-card); border: 1px solid var(--border-subtle); overflow: hidden; }
.ghostbtn { display: inline-flex; align-items: center; gap: 6px; height: 34px; padding: 0 12px; border: 1px solid var(--border-default); border-radius: var(--radius-md); background: var(--surface-card); color: var(--text-body); font: 600 12.5px var(--font-body); cursor: pointer; }
.ghostbtn:disabled { opacity: 0.6; cursor: default; }
.stats { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; margin-bottom: 14px; }
.stat { position: relative; padding: 14px 16px; border: 1px solid var(--border-subtle); border-radius: var(--radius-lg); background: var(--surface-card); }
.stat.click { cursor: pointer; }
.stat.click:hover { border-color: var(--border-default); }
.stat .si { position: absolute; top: 12px; right: 12px; width: 28px; height: 28px; border-radius: 8px; display: grid; place-items: center; background: var(--surface-sunken); color: var(--text-muted); }
.stat .sv { font: 700 24px var(--font-mono); color: var(--text-strong); }
.stat .sv .of { font-size: 13px; color: var(--text-faint); font-weight: 600; }
.stat .sl { margin-top: 2px; font: 500 12px var(--font-body); color: var(--text-muted); }
.stat.act { border-color: var(--warning-text); }
.stat.act .sv { color: var(--warning-text); }
.stat.bad { border-color: var(--danger-text); }
.stat.bad .sv { color: var(--danger-text); }
/* 两栏等高:一边空、一边满时,空的那张卡片也撑到同高,空状态居中 —— 不再像缺了半块。 */
.grid2 { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; margin-bottom: 12px; align-items: stretch; }
.grid2 > .card { display: flex; flex-direction: column; }
.grid2 > .card > .empty { flex: 1; display: flex; align-items: center; justify-content: center; }
@media (max-width: 1180px) { .grid2, .stats { grid-template-columns: 1fr; } }
.conncard { margin-bottom: 12px; }
/* 实例多列排:宽屏一行三四个,窄屏一列。异常与展开后的正常实例同一套网格。 */
.cgrid { display: grid; grid-template-columns: repeat(auto-fill, minmax(340px, 1fr)); column-gap: 24px; padding: 4px 16px 8px; }
/* 异常实例带分层与报错时间,一格要宽一些,实例名不被挤成省略号 */
.conncard > .cgrid { grid-template-columns: repeat(auto-fill, minmax(460px, 1fr)); }
.tgroup .cgrid { padding: 0; }
.allok { display: flex; align-items: center; gap: 8px; padding: 16px; font: 600 12.5px var(--font-body); color: var(--success-text); }
.crow.click { cursor: pointer; }
.crow.click:hover .cname { color: var(--accent-text); }
.ifind { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.missing { padding: 8px 16px 12px; }
.missing .link { margin-left: 0; }
.mchips { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 8px; }
.mchip { padding: 2px 8px; border-radius: 4px; background: var(--surface-sunken); color: var(--text-muted); font: 500 11.5px var(--font-body); cursor: pointer; }
.mchip:hover { color: var(--accent-text); }
.ctier { flex-shrink: 0; padding: 1px 6px; border-radius: 4px; background: var(--surface-sunken); color: var(--text-muted); font-size: 10.5px; font-weight: 600; }
.chead { display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; padding: 12px 16px; border-bottom: 1px solid var(--border-subtle); }
.ct { font: 700 13.5px var(--font-display); color: var(--text-strong); }
.cs { font: 500 11.5px var(--font-body); color: var(--text-faint); }
.link { margin-left: auto; font: 600 12px var(--font-body); color: var(--accent-text); cursor: pointer; }
.note { margin: 10px 16px 0; padding: 7px 10px; border-radius: 6px; background: var(--warning-subtle); color: var(--warning-text); font: 500 11.5px/1.5 var(--font-body); }
.empty { padding: 22px 16px; text-align: center; font: 500 12px var(--font-body); color: var(--text-faint); }
.tgroup { padding: 8px 16px 4px; }
.tlabel { font: 700 11px var(--font-body); color: var(--text-muted); margin: 4px 0 6px; }
.tn { color: var(--text-faint); font-weight: 600; }
.crow { display: flex; align-items: center; gap: 8px; padding: 6px 0; border-top: 1px dashed var(--border-subtle); font: 500 12px var(--font-body); }
.crow .hi { flex-shrink: 0; color: var(--success-text); }
.crow.fail .hi { color: var(--danger-text); }
.crow.maint .hi { color: var(--warning-text); }
.crow.unknown .hi { color: var(--text-faint); }
.cname { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-strong); font-weight: 600; }
.ceng { flex-shrink: 0; color: var(--text-faint); font-size: 11px; }
.cst { flex-shrink: 0; color: var(--text-muted); font-size: 11px; }
.crow.fail .cst { color: var(--danger-text); }
.arow { padding: 8px 16px; border-top: 1px solid var(--border-subtle); }
.arow:first-of-type { border-top: none; }
.atop { display: flex; align-items: baseline; gap: 8px; font: 600 12.5px var(--font-body); color: var(--text-strong); }
.aage { margin-left: auto; flex-shrink: 0; font: 500 11px var(--font-body); color: var(--text-faint); }
.ameta { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font: 500 11.5px var(--font-body); color: var(--text-muted); }
.acmd { margin-top: 3px; font-size: 11.5px; color: var(--text-body); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.mono { font-family: var(--font-mono); }
.online .opad { padding: 4px 8px 8px; }
</style>
