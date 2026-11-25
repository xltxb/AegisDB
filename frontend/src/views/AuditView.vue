<script setup lang="ts">
/**
 * 审计日志。
 *
 * 这一页要回答的是同一句话的六个部分:**什么时候、谁、从哪台机器、对哪个库、做了什么、
 * 结果如何**。2026-09 的重构围着这句话重排:
 *
 *   · 筛选散在页头的一排控件收进一张卡,并且改成"改条件 → 按查询"。逐个即时生效在
 *     这一页是负担:排障时常常要同时定住时间段、实例和人,即时生效等于每改一格就打
 *     一次请求,而中间那几次结果没有人要看。
 *   · 一行里把"谁"和"从哪台机器"并成一格、把实例与库表并成一格 —— 它们本来就是一句
 *     话,拆成两列只会让眼睛在行内来回找。
 *   · 语句原文回到行上(等宽、单行截断、可复制),完整的、排过版的那份在右侧抽屉里。
 *     此前行上只有"动词 + 表名",而一条命令到底做了什么,动词和表名说不全。
 *
 * 颜色一律走语义令牌(--text-strong / --surface-card / --success-subtle …),不写死
 * 十六进制:这套令牌本身就是 Slate 那一档,而且深色主题整套换值 —— 写死的浅灰在深色
 * 下会变成看不见的字。
 */
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { sessionHeaders } from '@/lib/session'
import { useRouter, useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
  Search, Calendar, CircleCheck, Hourglass, CircleX, TriangleAlert, Download, FlaskConical,
  ChevronLeft, ChevronRight, X, Copy, Check, RotateCcw, ScrollText, ShieldCheck, ShieldAlert, Layers, Undo2,
} from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSelect from '@/components/common/VSelect.vue'
import VMultiSelect from '@/components/common/VMultiSelect.vue'
import VDateTime from '@/components/common/VDateTime.vue'
import api from '@/api'
import { localizeText } from '@/api/http'
import { useAuthStore } from '@/stores/auth'
import { useUIStore } from '@/stores/ui'
import { extractTables, tablesLabel } from '@/lib/sqlTables'
import { formatSQL, looksLikeSQL } from '@/lib/sqlFormat'
import { copyText } from '@/lib/clipboard'
import { auditResult, type AuditTone } from '@/lib/auditResult'
import type { AuditRow, AuditQuery } from '@/types'

const { t } = useI18n()
const ui = useUIStore()
const router = useRouter()
const authStore = useAuthStore()
// 用户后台的 /audit(route.meta.mine)没有审计链校验、导出与单据轨迹 —— 那些是管理后台全量审计
// (/admin/audit)的事(管理后台拆分,工单 04)。列表里看到谁的记录:
//   - 能看全量审计的人(管理员 / 审计员 / DBA 负责人,auth.seesAllAudit):默认所有人的,标题「操作记录」,
//     另有「只看我的」切换(2026-10-14 用户确认 DBA 负责人;2026-10-26 回归排查 09 补上管理员 / 审计员 ——
//     审计员从总览「审计 → 详情」点进来的就是这页,从前落到只有自己的空表);
//   - 其他人:只有自己的,「我的操作记录」。
// 用户后台这页不带 full:管理后台空闲 / 白名单是管理后台的事,不该让这页报 40304 把人赶走。
const route = useRoute()
const userPortal = computed(() => route.meta.mine === true)
// DBA 负责人在用户后台:审计导出、链校验、单据轨迹都给(2026-10-26 讨论 18,后端 middleware.AuditSupervisor)。
// 管理员 / 审计员这三样仍在管理后台用(后端对他们照旧套管理后台的白名单与空闲超时)。
const ownerSupervises = computed(() => userPortal.value && authStore.isDBALead && !authStore.canEnterAdmin)
const seesAll = computed(() => userPortal.value && authStore.seesAllAudit)
const onlyMine = ref(false)
const team = computed(() => seesAll.value && !onlyMine.value)
const mine = computed(() => userPortal.value && !team.value)
function toggleOnlyMine() {
  onlyMine.value = !onlyMine.value
  page.value = 1
  load()
  loadInstances()
}

// 审计行带着授权它的工单号,顺着点过去是很自然的下一问("谁批的?")。只对进得了审批
// 页的人给这个入口 —— 否则这一下会落在守卫上,看起来像坏了。
const canOpenApproval = computed(() => !!authStore.menus.approve)
function openApproval(apNo?: string) {
  if (!apNo || !canOpenApproval.value) return
  router.push({ name: 'approvals', query: { ap: apNo } })
}

// ---------------------------------------------------------------- 筛选
//
// draft 是人正在填的那一份,applied 是列表**此刻真的按着筛**的那一份。分开两份,是为了
// 让「查询」有确切的含义:按下之前,列表还是上一次的结果,而不是一个改到一半的条件拼
// 出来的中间态。两份不一致时在按钮旁说一句,免得改完忘了按。
type Draft = {
  risk: string
  events: string[]
  actions: string[]
  sources: string[]
  instance: string
  keyword: string
  range: string
  from: string
  to: string
}
const emptyDraft = (): Draft => ({
  risk: 'all', events: [], actions: [], sources: [], instance: '', keyword: '', range: '24h', from: '', to: '',
})
const draft = ref<Draft>(emptyDraft())
const applied = ref<Draft>(emptyDraft())
const dirty = computed(() => JSON.stringify(draft.value) !== JSON.stringify(applied.value))

const rows = ref<AuditRow[]>([])
// 按审批单合并:同一张单的操作(拦截 → 审批 → 执行)在列表里合成一条,显示最新的一步;
// groups 是本页每张单共几条。点开详情看这张单的全部操作(时间线)。
const grouped = ref(true)
const groups = ref<Record<string, number>>({})
const total = ref(0)
const page = ref(1)
const pageSize = ref(50)
const pageSizes = [20, 50, 100, 200]
const pages = computed(() => Math.max(1, Math.ceil(total.value / pageSize.value)))
const loading = ref(false)
// 筛过之后的空,和"这段时间本来就没有记录"要给不同的出口:前者该给一个清空条件的按钮。
const filtered = computed(() => {
  const a = applied.value
  return !!(a.keyword || a.instance || a.events.length || a.actions.length || a.sources.length || a.risk !== 'all' || a.from || a.to)
})

function queryOf(a: Draft): AuditQuery {
  return {
    risk: a.risk,
    event: a.events,
    action: a.actions,
    source: a.sources,
    instance: a.instance,
    q: a.keyword.trim(),
    // 绝对区间一旦填了,服务端就以它为准;相对区间这时不发,免得两套条件同时在场。
    range: a.from || a.to ? '' : a.range,
    from: a.from,
    to: a.to,
    group: grouped.value ? 'approval' : '',
    mine: mine.value,
    full: !userPortal.value, // 管理后台的全量审计:白名单外 / 空闲超时就拒,不悄悄降级成自己的
    page: page.value,
    pageSize: pageSize.value,
  }
}

async function load() {
  loading.value = true
  try {
    const res = await api.audit(queryOf(applied.value))
    rows.value = res.items
    groups.value = res.groups || {}
    total.value = res.total
    page.value = res.page
  } catch (e) {
    ui.notifyError(e, t('loadFailed'))
  } finally {
    loading.value = false
  }
}

// 实例下拉的可选值:审计里**真实出现过**的那些。照着连接表列的话,下拉里会有他够不到
// 的实例,而一个筛出来永远是空的选项,读起来像"这台机器上什么都没发生过"。
const instances = ref<string[]>([])
async function loadInstances() {
  try { instances.value = await api.auditInstances(mine.value, !userPortal.value) } catch { instances.value = [] }
}

onMounted(async () => {
  await Promise.all([load(), loadInstances()])
})

function apply() {
  applied.value = JSON.parse(JSON.stringify(draft.value))
  page.value = 1
  load()
}
// 合并开关:立即生效(它不是筛选条件,不用等「查询」),回到第一页 —— 条目数变了,原来的页码没有意义。
function toggleGrouped() {
  grouped.value = !grouped.value
  page.value = 1
  load()
}
function reset() {
  draft.value = emptyDraft()
  apply()
}
async function goto(p: number) {
  const np = Math.min(pages.value, Math.max(1, p))
  if (np === page.value) return
  page.value = np
  await load()
}
function setPageSize(n: number) {
  pageSize.value = n
  page.value = 1
  load()
}

// ---- 各个下拉的取值 ↔ 显示文案 ----
// VSelect 认的是显示文案,所以单选的那两个要把标签映射回内部取值;多选的用 VMultiSelect,
// 它直接收 { v, label },不必来回映射。
const riskKeys = ['all', 'high', 'mid', 'low']
const riskOpts = computed(() => [t('allRisk'), t('highTag'), t('med'), t('low')])
const riskLabel = computed<string>({
  get: () => riskOpts.value[riskKeys.indexOf(draft.value.risk)] ?? riskOpts.value[0],
  set: (l) => {
    const i = riskOpts.value.indexOf(l)
    if (i >= 0) draft.value.risk = riskKeys[i]
  },
})

const rangeKeys = ['1h', '24h', '7d', '30d', '']
const rangeOpts = computed(() => [t('t1h'), t('t24h'), t('t7d'), t('t30d'), t('tAll')])
const rangeLabel = computed<string>({
  get: () => rangeOpts.value[Math.max(0, rangeKeys.indexOf(draft.value.range))],
  set: (l) => {
    const i = rangeOpts.value.indexOf(l)
    if (i < 0) return
    draft.value.range = rangeKeys[i]
    // 选了预设就清掉绝对区间 —— 否则服务端以绝对区间为准,预设看着选中了却不生效。
    draft.value.from = ''
    draft.value.to = ''
  },
})

const instOpts = computed(() => [t('auditAllInstances'), ...instances.value])
const instLabel = computed<string>({
  get: () => draft.value.instance || t('auditAllInstances'),
  set: (l) => { draft.value.instance = l === t('auditAllInstances') ? '' : l },
})

const eventOpts = computed(() => [
  { v: 'intercept', label: t('auditEv_intercept') },
  { v: 'approve', label: t('auditEv_approve') },
  { v: 'exec', label: t('auditEv_exec') },
  { v: 'login', label: t('auditEv_login') },
])
const actionOpts = computed(() => [
  { v: 'create', label: t('auditAct_create') },
  { v: 'delete', label: t('auditAct_delete') },
  { v: 'update', label: t('auditAct_update') },
  { v: 'read', label: t('auditAct_read') },
  { v: 'other', label: t('auditAct_other') },
])

// 入口来源(迁移 0063)。顺序与后端 model.AuditSources 一致,最后一项筛旧记录。
const SOURCES = ['query', 'terminal', 'script', 'export', 'approval', 'release', 'api', 'auth', 'admin', 'system'] as const
const sourceOpts = computed(() => [
  ...SOURCES.map((v) => ({ v, label: t('auditSrc_' + v) })),
  { v: 'unknown', label: t('auditSrc_unknown') },
])
function srcMeta(s?: string) {
  // 空 = 0063 之前写下的行:如实说「未知 / 旧记录」,不按命令去猜它当时从哪个页面来。
  if (!s || !(SOURCES as readonly string[]).includes(s)) return { t: t('auditSrc_unknown'), cls: 'src-none' }
  return { t: t('auditSrc_' + s), cls: 'src-' + s }
}

// ---------------------------------------------------------------- 一行怎么画
// 时间拆成两行:日期一行、时刻一行。170px 里塞一整个 "2026-09-16 14:32:07" 只能靠缩字号
// 或者听任它折行 —— 折出来的第二行从哪儿断是浏览器说了算,十几行下来参差不齐。
function fmtDate(s: string) {
  const d = new Date(s)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}
function fmtClock(s: string) {
  const d = new Date(s)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

// 这一行"做了什么"的抬头,按风险着色 —— 一屏扫过去,红色的那几个就是要找的东西。
//
// 默认取命令的第一个词,但只认**像动词的那种**:纯字母/点/下划线,且不超过 20 个字符
// (admin.role.menus 这样的内部动作名要留得下)。
// 审计行里躺着的不都是 SQL,还有"开启执行窗口「发版班车 · 演示」"这类整句描述 —— 按空
// 白切出来的第一段是半句中文,塞进一枚徽标里既读不懂也撑破格子。认不出动词就退回说风险
// 等级:那一格总得有个东西,而"高危"至少是这一行真实的属性。
const VERB_RE = /^[A-Za-z][A-Za-z0-9_.]{0,19}$/
function verbOf(cmd: string, risk: string) {
  const first = cmd.trim().split(/\s+/)[0] || ''
  return VERB_RE.test(first) ? first.toUpperCase() : riskText(risk)
}
function riskCls(r: string) { return r === 'high' ? 'v-high' : r === 'mid' ? 'v-mid' : 'v-low' }
function riskText(r: string) { return r === 'high' ? t('highTag') : r === 'mid' ? t('med') : t('low') }

// 库与表并成一格:orders_db.t_pay_log。取不出表名时只显示库名,两个都没有给一个短横 ——
// 登录、配置变更这类行本来就不落在某张表上。
function targetOf(r: AuditRow) {
  // tablesLabel 取不到表名时给的是一个短横,不是空串 —— 那是给"表"那一格用的占位符,
  // 在这里会被拼进 "orders_db.—",所以先还原成空。
  const label = tablesLabel(extractTables(r.command))
  const tables = label === '—' ? '' : label
  if (r.database && tables) return `${r.database}.${tables}`
  return r.database || tables || ''
}

function evMeta(ev?: string) {
  switch (ev) {
    case 'intercept': return { t: t('auditEv_intercept'), cls: 'ev-intercept' }
    case 'approve': return { t: t('auditEv_approve'), cls: 'ev-approve' }
    case 'exec': return { t: t('auditEv_exec'), cls: 'ev-exec' }
    case 'login': return { t: t('auditEv_login'), cls: 'ev-login' }
    // 空 = 迁移 0039 之前写下的行。如实一个短横,不替它猜 —— 在一张哈希链审计表上猜等于造证据。
    default: return { t: '—', cls: 'ev-none' }
  }
}
function actMeta(a?: string) {
  switch (a) {
    case 'create': return { t: t('auditAct_create'), cls: 'act-create' }
    case 'delete': return { t: t('auditAct_delete'), cls: 'act-delete' }
    case 'update': return { t: t('auditAct_update'), cls: 'act-update' }
    case 'read': return { t: t('auditAct_read'), cls: 'act-read' }
    case 'other': return { t: t('auditAct_other'), cls: 'act-other' }
    default: return { t: '—', cls: 'act-none' } // 0040 之前的行,同上
  }
}

// 结果:成 / 败 / 悬而未决,各一个胶囊。已导出与"模拟(未执行)"都是中性的 —— 没有任何
// 东西出错,把它们画成告警是不实的。
// 同一个 pending 在"拦截"与"审批"两种事件上意思相反(等人审 / 已经审过),说法见 lib/auditResult。
const TONE_ICON: Record<AuditTone, unknown> = {
  ok: CircleCheck, pass: ShieldCheck, wait: Hourglass, bad: CircleX, info: Download, mute: FlaskConical, warn: TriangleAlert,
}
function resMeta(r: { result: string; eventType?: string }) {
  const v = auditResult(r.result, r.eventType)
  const icon = v.key === 'rCancelled' ? Undo2 : TONE_ICON[v.tone]
  return { t: t(v.key), cls: 'st-' + v.tone, icon }
}

// ---------------------------------------------------------------- 复制 / 抽屉
const copiedId = ref<number | string>('')
async function copyCmd(r: AuditRow | null, key: number | string) {
  if (!r) return
  // 复制的永远是**原文**,不是抽屉里排过版的那份:排版只为看,复制出去的东西要能原样
  // 对得上审计行。
  if (await copyText(r.command)) {
    copiedId.value = key
    setTimeout(() => { if (copiedId.value === key) copiedId.value = '' }, 1600)
  } else {
    ui.notify(t('objCopyFail'), 'error')
  }
}

const detail = ref<AuditRow | null>(null)
const pretty = ref(true)
// 这张审批单的全部操作(按时间先后)。点时间线上的一条,抽屉切到那一条,时间线不重取。
const trail = ref<AuditRow[]>([])
const trailFor = ref('')
function showRow(r: AuditRow) {
  detail.value = r
  pretty.value = looksLikeSQL(r.command) // 不是 SQL 的行没有"格式化"可言,默认就给原文
}
async function openDetail(r: AuditRow) {
  showRow(r)
  trail.value = []
  trailFor.value = r.approvalNo || ''
  // 单据轨迹接口给管理员 / 审计员(在管理后台看)与 DBA 负责人(进不了管理后台,在用户后台看)
  if (!r.approvalNo || (userPortal.value && !ownerSupervises.value)) return
  try {
    const rows = await api.auditTrail(r.approvalNo)
    if (trailFor.value === r.approvalNo) trail.value = rows // 等的时候又点了别的行:作废
  } catch { /* 取不到就不显示时间线,详情本身照常 */ }
}
function closeDetail() { detail.value = null; trail.value = []; trailFor.value = '' }
const detailText = computed(() => {
  const d = detail.value
  if (!d) return ''
  return pretty.value ? formatSQL(d.command) : d.command
})
// 抽屉开着时按 Esc 关掉,而不是让人回头去找那个叉。
//
// 监听**开着的整段时间**都挂着,关掉时摘下。不能图省事用 { once: true }:那是"下一次
// 按键之后就摘",而不是"按到 Esc 之后就摘" —— 在抽屉里按一下方向键或者 Ctrl,Esc 就
// 再也不管用了,而这种失灵没有任何迹象,人只会觉得这一下没按准。
const onDrawerKey = (e: KeyboardEvent) => { if (e.key === 'Escape') closeDetail() }
watch(detail, (d) => {
  if (d) window.addEventListener('keydown', onDrawerKey)
  else window.removeEventListener('keydown', onDrawerKey)
})
// 组件被卸载时抽屉可能还开着(点了侧栏走人),监听得跟着走。
onUnmounted(() => window.removeEventListener('keydown', onDrawerKey))

// ---------------------------------------------------------------- 哈希链校验
//
// 这一页此前只是**展示**了每行的链式校验值,没有任何地方去验它 —— 那种状态下"不可篡改"
// 是一句没人检查的话。这个按钮就是那另一半。
//
// 只对监督角色亮:校验要扫全表(链是一条),它天然越过了"没有监督角色只看得见自己"那条
// 线,服务端也照这个口径挡(service.CanVerifyAuditChain)。前端跟着亮/不亮,免得出现一个
// 按下去必然 403 的按钮。
const canVerify = computed(() => ownerSupervises.value || (!userPortal.value &&
  (authStore.me?.roleCodes || []).some((c) => c === 'admin' || c === 'audit')))
const verifying = ref(false)
const verdict = ref<{ ok: boolean; text: string } | null>(null)
async function verifyChain() {
  if (verifying.value) return
  verifying.value = true
  verdict.value = null
  try {
    const { report, reasonText, reasonRef } = await api.verifyAudit()
    verdict.value = report.ok
      ? {
          ok: true,
          text: t('auditVerifyOK', { n: report.checked, total: report.total })
            + (report.rolloverRows > 0 ? t('auditVerifyRollover', { n: report.rolloverRows }) : ''),
        }
      : {
          ok: false,
          // 断在哪一行、是哪一种断法 —— 两者要人做的事不同(去看那一行原来是什么,还是
          // 去找那一行去哪儿了),所以理由原样从服务端带下来,前端不照着三个取值各写一份。
          text: t('auditVerifyBad', { id: report.badId, at: fmtDate(report.badAt) + ' ' + fmtClock(report.badAt), why: localizeText(reasonText, reasonRef) }),
        }
  } catch (e) {
    ui.notifyError(e, t('auditVerifyFailed'))
  } finally {
    verifying.value = false
  }
}

// ---------------------------------------------------------------- 导出
const exported = ref('')
async function doExport() {
  try {
    // 导出跟着**列表此刻真的按着的**那份条件走(applied,不是 draft):人是看着这张列表
    // 按下导出的,给他一份改到一半的条件筛出来的东西就是另一回事了。
    const res = await fetch(api.auditExportUrl(queryOf(applied.value)), { headers: sessionHeaders() })
    const blob = await res.blob()
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = 'audit_export.csv'
    a.click()
    URL.revokeObjectURL(url)
    exported.value = '✓ ' + t('auditExported', { n: total.value })
    setTimeout(() => (exported.value = ''), 2600)
  } catch (e) {
    ui.notifyError(e, t('exportFailed'))
  }
}
</script>

<template>
  <div class="scy page">
    <div class="v-head">
      <div class="v-head-l">
        <div class="v-eyebrow">AUDIT LOG</div>
        <div class="v-h1">{{ team ? $t('auditTeamTitle') : mine ? $t('auditMineTitle') : $t('m_audit') }}</div>
        <div class="v-sub sub"><ScrollText :size="12" />{{ team ? $t('auditTeamSub') : mine ? $t('auditMineSub') : $t('auditSub') }}</div>
      </div>
      <div v-if="exported" class="v-head-acts"><span class="exp">{{ exported }}</span></div>
    </div>

    <!-- 筛选栏:一行紧凑表单 + 右侧操作区 -->
    <div class="card fbar" data-testid="audit-fbar">
      <div class="fields">
        <div class="f">
          <span class="fl"><Calendar :size="11" />{{ $t('colTime') }}</span>
          <VSelect v-model="rangeLabel" :options="rangeOpts" height="36px" />
        </div>
        <div class="f wide">
          <span class="fl">{{ $t('auditFrom') }} — {{ $t('auditTo') }}</span>
          <div class="rangebox">
            <VDateTime v-model="draft.from" :placeholder="$t('auditFrom')" />
            <span class="dash">—</span>
            <VDateTime v-model="draft.to" :placeholder="$t('auditTo')" />
            <X
              v-if="draft.from || draft.to" class="clr" :size="13"
              :title="$t('auditClearRange')" @click="draft.from = ''; draft.to = ''"
            />
          </div>
        </div>
        <div class="f">
          <span class="fl">{{ $t('mInst') }}</span>
          <VSelect v-model="instLabel" :options="instOpts" searchable height="36px" />
        </div>
        <div class="f" data-testid="audit-event-filter">
          <span class="fl">{{ $t('colEvent') }}</span>
          <VMultiSelect
            v-model="draft.events" :options="eventOpts" height="36px"
            :placeholder="$t('auditAllEvents')" :summary="$t('auditPickedN', { n: '{n}' })"
          />
        </div>
        <div class="f" data-testid="audit-action-filter">
          <span class="fl">{{ $t('colAction') }}</span>
          <VMultiSelect
            v-model="draft.actions" :options="actionOpts" height="36px"
            :placeholder="$t('auditAllActions')" :summary="$t('auditPickedN', { n: '{n}' })"
          />
        </div>
        <div class="f" data-testid="audit-source-filter">
          <span class="fl">{{ $t('colSource') }}</span>
          <VMultiSelect
            v-model="draft.sources" :options="sourceOpts" height="36px"
            :placeholder="$t('auditAllSources')" :summary="$t('auditPickedN', { n: '{n}' })"
          />
        </div>
        <div class="f">
          <span class="fl">{{ $t('colRisk') }}</span>
          <VSelect v-model="riskLabel" :options="riskOpts" height="36px" />
        </div>
      </div>
      <!-- 第二行:搜索框占满左边,操作按钮靠右。搜索与按钮同一行,回车或点查询都在手边。 -->
      <div class="fbot">
        <label class="f grow" :aria-label="$t('colWho') + ' / IP'">
          <div class="searchbox">
            <Search :size="14" color="var(--text-faint)" />
            <span class="sk">{{ $t('colWho') }} / IP</span>
            <!-- 回车即查询:填完一个搜索框的下一个动作是回车,不是去找按钮 -->
            <input v-model="draft.keyword" :placeholder="$t('auditSearchPh')" spellcheck="false" @keyup.enter="apply">
          </div>
        </label>
        <span v-if="dirty" class="dirty"><i class="ddot" />{{ $t('auditDirty') }}</span>
        <div class="fbtns">
          <VButton variant="primary" height="36px" data-testid="audit-search" @click="apply">{{ $t('auditQuery') }}</VButton>
          <button class="gbtn" @click="reset"><RotateCcw :size="13" />{{ $t('auditReset') }}</button>
          <button v-if="canVerify" class="gbtn" :disabled="verifying" @click="verifyChain">
            <ShieldCheck :size="13" />{{ verifying ? $t('auditVerifying') : $t('auditVerify') }}
          </button>
          <button class="gbtn" :class="{ on: grouped }" :title="$t('auditGroupToggleTip')" data-testid="audit-group-toggle" @click="toggleGrouped">
            <Layers :size="13" />{{ $t('auditGroupToggle') }}
          </button>
          <button v-if="seesAll" class="gbtn" :class="{ on: onlyMine }" data-testid="audit-mine-toggle" @click="toggleOnlyMine">
            <ScrollText :size="13" />{{ $t('auditOnlyMine') }}
          </button>
          <button v-if="!userPortal || ownerSupervises" class="gbtn" data-testid="audit-export" @click="doExport"><Download :size="13" />{{ $t('exportCsv') }}</button>
        </div>
      </div>
    </div>

    <!-- 校验结论。摆在筛选与表格之间,而不是弹一下就没 —— 它是一句要被人抄走的结论。 -->
    <div v-if="verdict" class="vbar" :class="{ bad: !verdict.ok }">
      <ShieldCheck v-if="verdict.ok" :size="14" />
      <ShieldAlert v-else :size="14" />
      <span>{{ verdict.text }}</span>
      <X :size="14" class="vx" @click="verdict = null" />
    </div>

    <!-- 表格 -->
    <div class="card">
      <div class="thead">
        <span>{{ $t('colTime') }}</span>
        <span>{{ $t('colWho') }}</span>
        <span>{{ $t('colSource') }}</span>
        <span>{{ $t('colTarget') }}</span>
        <span>{{ $t('colSql') }}</span>
        <span>{{ $t('colCategory') }}</span>
        <span>{{ $t('colStatus') }}</span>
        <span class="right">{{ $t('apActions') }}</span>
      </div>

      <!-- 加载态用骨架行,不是一个转圈:表格的形状先立住,内容再填进来,视线不必重新找位置 -->
      <template v-if="loading">
        <div v-for="n in 8" :key="'sk' + n" class="tr sk">
          <span><i class="bar w60" /><i class="bar w40" /></span>
          <span><i class="bar w70" /><i class="bar w50" /></span>
          <span><i class="bar w60" /></span>
          <span><i class="bar w80" /><i class="bar w60" /></span>
          <span><i class="bar w90" /></span>
          <span><i class="bar w50" /></span>
          <span><i class="bar w60" /></span>
          <span><i class="bar w40" /></span>
        </div>
      </template>

      <template v-else>
        <div
          v-for="r in rows" :key="r.id" class="tr" :class="{ high: r.risk === 'high' }"
          @click="openDetail(r)"
        >
          <!-- 时间:日期一行、时刻一行,固定 170px,永远不折 -->
          <span class="tcell">
            <span class="clock">{{ fmtClock(r.occurredAt) }}</span>
            <span class="date">{{ fmtDate(r.occurredAt) }}</span>
          </span>

          <!-- 谁 + 从哪台机器。它们是一句话的两半,拆成两列眼睛要在行内来回找 -->
          <span class="who">
            <span class="wname">{{ r.actor }}</span>
            <span class="wip" :title="r.clientIp ? '' : $t('auditNoIp')">{{ r.clientIp || '—' }}</span>
          </span>

          <!-- 入口来源:同样一条 SELECT,从数据查询来还是从终端来,一眼分开 -->
          <span class="srccell">
            <span class="srcbadge" :class="srcMeta(r.source).cls" data-testid="audit-source">{{ srcMeta(r.source).t }}</span>
          </span>

          <!-- 实例(按分层着色的胶囊)+ 库.表 -->
          <span class="tgt">
            <!-- 没有实例的行(登录、配置变更)不画空胶囊:一个里面只有短横的标签,看着
                 像"有这么一台实例、只是名字没取到"。 -->
            <span v-if="r.instance" class="envtag" :class="r.tierCode || 'unknown'">{{ r.instance }}</span>
            <span v-if="targetOf(r)" class="dbtbl">{{ targetOf(r) }}</span>
            <span v-if="!r.instance && !targetOf(r)" class="dbtbl">—</span>
          </span>

          <!-- 动词徽标 + 语句原文(等宽、单行截断)+ 复制 -->
          <span class="sqlcell">
            <span class="verb" :class="riskCls(r.risk)" :title="riskText(r.risk)">{{ verbOf(r.command, r.risk) }}</span>
            <code class="sql" :title="r.command">{{ r.command }}</code>
            <button class="cp" :title="$t('auditCopySql')" @click.stop="copyCmd(r, r.id)">
              <Check v-if="copiedId === r.id" :size="12" />
              <Copy v-else :size="12" />
            </button>
          </span>

          <!-- 分类:这一步在流程里是什么 / 它动了数据没有。两件正交的事,叠在一格里 -->
          <span class="cat">
            <span class="evbadge" :class="evMeta(r.eventType).cls">{{ evMeta(r.eventType).t }}</span>
            <span class="actbadge" :class="actMeta(r.dataAction).cls">{{ actMeta(r.dataAction).t }}</span>
          </span>

          <!-- 结果 + 授权它的工单 -->
          <span class="stcell">
            <span class="stbadge" :class="resMeta(r).cls">
              <component :is="resMeta(r).icon" :size="11" />{{ resMeta(r).t }}
            </span>
            <span
              v-if="r.approvalNo" class="ap" :class="{ link: canOpenApproval }"
              :title="canOpenApproval ? $t('apOpenTicket') : ''" @click.stop="openApproval(r.approvalNo)"
            >#{{ r.approvalNo }}</span>
            <span
              v-if="grouped && r.approvalNo && (groups[r.approvalNo] || 0) > 1" class="gcount"
              :title="$t('auditGroupCountTip')" data-testid="audit-group-count"
            >{{ $t('auditGroupCount', { n: groups[r.approvalNo] }) }}</span>
          </span>

          <span class="acts">
            <button class="lnk" @click.stop="openDetail(r)">{{ $t('auditDetail') }}</button>
          </span>
        </div>

        <!-- 空态分两种:筛没了的出口是清空条件,而不是干等 -->
        <div v-if="!rows.length" class="empty">
          <div class="eic"><ScrollText :size="22" color="var(--text-faint)" /></div>
          <div class="et">{{ filtered ? $t('auditEmptyFiltered') : $t('auditEmpty') }}</div>
          <button v-if="filtered" class="eclear" @click="reset">{{ $t('auditClearFilters') }}</button>
        </div>
      </template>

      <div class="pfoot">
        <span class="ptotal">{{ $t('auditTotal', { n: total }) }}</span>
        <div class="pager">
          <span class="psize">
            {{ $t('apPerPage') }}
            <select :value="pageSize" @change="setPageSize(Number(($event.target as HTMLSelectElement).value))">
              <option v-for="n in pageSizes" :key="n" :value="n">{{ n }}</option>
            </select>
          </span>
          <button class="pg" :disabled="page <= 1 || loading" @click="goto(page - 1)"><ChevronLeft :size="15" /></button>
          <span class="pgn">{{ $t('auditPageOf', { p: page, n: pages }) }}</span>
          <button class="pg" :disabled="page >= pages || loading" @click="goto(page + 1)"><ChevronRight :size="15" /></button>
        </div>
      </div>
    </div>

    <!-- 详情抽屉:完整语句 + 这一行的全部上下文 -->
    <div v-if="detail" class="overlay">
      <div class="mask" @click="closeDetail" />
      <aside class="drawer">
        <header class="dhead">
          <span class="verb" :class="riskCls(detail.risk)">{{ verbOf(detail.command, detail.risk) }}</span>
          <span class="dtitle">{{ $t('auditDetailTitle') }}</span>
          <X :size="18" class="dx" @click="closeDetail" />
        </header>
        <div class="dbody scy">
          <div class="dsec">
            <div class="dlbl">
              {{ $t('colCmd') }}
              <span class="dtools">
                <!-- 只有像 SQL 的命令才给这个开关:登录、配置变更那类行排版只会更难读 -->
                <span v-if="looksLikeSQL(detail.command)" class="seg">
                  <button :class="{ on: pretty }" @click="pretty = true">{{ $t('auditSqlFormatted') }}</button>
                  <button :class="{ on: !pretty }" @click="pretty = false">{{ $t('auditSqlRaw') }}</button>
                </span>
                <button class="cp2" @click="copyCmd(detail, 'drawer')">
                  <Check v-if="copiedId === 'drawer'" :size="12" /><Copy v-else :size="12" />
                  {{ copiedId === 'drawer' ? $t('copied') : $t('copy') }}
                </button>
              </span>
            </div>
            <pre class="dcmd">{{ detailText }}</pre>
          </div>

          <div class="dgrid">
            <div><span class="ml">{{ $t('colTime') }}</span><div class="dv mono">{{ fmtDate(detail.occurredAt) }} {{ fmtClock(detail.occurredAt) }}</div></div>
            <div><span class="ml">{{ $t('colWho') }}</span><div class="dv">{{ detail.actor }}</div></div>
            <div>
              <span class="ml">{{ $t('colClientIp') }}</span>
              <div class="dv mono" :class="{ none: !detail.clientIp }">{{ detail.clientIp || $t('auditNoIp') }}</div>
            </div>
            <div><span class="ml">{{ $t('mInst') }}</span><div class="dv mono">{{ detail.instance || '—' }}</div></div>
            <div><span class="ml">{{ $t('colTables') }}</span><div class="dv mono">{{ targetOf(detail) || '—' }}</div></div>
            <div><span class="ml">{{ $t('colRisk') }}</span><div class="dv">{{ riskText(detail.risk) }}</div></div>
            <div><span class="ml">{{ $t('colEvent') }}</span><div class="dv">{{ evMeta(detail.eventType).t }}</div></div>
            <div><span class="ml">{{ $t('colAction') }}</span><div class="dv">{{ actMeta(detail.dataAction).t }}</div></div>
            <div><span class="ml">{{ $t('colSource') }}</span><div class="dv" data-testid="audit-detail-source">{{ srcMeta(detail.source).t }}</div></div>
            <div>
              <span class="ml">{{ $t('colResult') }}</span>
              <div class="dv">
                <span class="stbadge" :class="resMeta(detail).cls">
                  <component :is="resMeta(detail).icon" :size="11" />{{ resMeta(detail).t }}
                </span>
              </div>
            </div>
            <div v-if="detail.operator"><span class="ml">{{ $t('colOperator') }}</span><div class="dv">{{ detail.operator }}</div></div>
            <div v-if="detail.approvalNo">
              <span class="ml">{{ $t('colAp') }}</span>
              <div class="dv ap" :class="{ link: canOpenApproval }" @click="openApproval(detail.approvalNo)">#{{ detail.approvalNo }}</div>
            </div>
          </div>

          <!-- 这张审批单的全部操作:拦截 → 审批 → 执行,按时间先后。点一条看那一条 -->
          <div v-if="trail.length" class="dsec">
            <div class="dlbl">{{ $t('auditTrail', { ap: trailFor }) }}</div>
            <ol class="trail">
              <li
                v-for="x in trail" :key="x.id" class="titem" :class="{ on: x.id === detail.id }"
                data-testid="audit-trail-item" @click="showRow(x)"
              >
                <span class="tdot" />
                <span class="ttime mono">{{ fmtDate(x.occurredAt) }} {{ fmtClock(x.occurredAt) }}</span>
                <span class="evbadge" :class="evMeta(x.eventType).cls">{{ evMeta(x.eventType).t }}</span>
                <span class="stbadge" :class="resMeta(x).cls">
                  <component :is="resMeta(x).icon" :size="11" />{{ resMeta(x).t }}
                </span>
                <span class="twho">{{ x.operator || x.actor }}</span>
              </li>
            </ol>
          </div>

          <!-- 链式校验值:这一行"没被改过"的凭据本身 -->
          <div class="dsec">
            <div class="dlbl">{{ $t('auditChainHash') }}</div>
            <div class="hash">{{ detail.hash }}</div>
          </div>
        </div>
      </aside>
    </div>
  </div>
</template>

<style scoped>
/* 页面底色由外层壳给(--surface-page,即 slate-50);这一页上的每一块内容都在白卡里,
   卡与底之间靠这一级色差分开,而不是靠给每块内容各画一条线。 */
.page { flex: 1; min-height: 0; padding: var(--page-pad); max-width: var(--page-max); margin-inline: auto; width: 100%; }
.card {
  background: var(--surface-card);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-lg, 10px);
  box-shadow: var(--shadow-sm);
}
.sub { display: inline-flex; align-items: center; gap: 6px; }
.exp { margin-left: auto; font: 600 12px var(--font-mono); color: var(--success-text); }

/* ---- 筛选栏 ----
   两行:上面一格一格的筛选项按网格排齐(换行也对得上列),下面是搜索框 + 操作按钮。
   从前所有东西挤在一个 flex-wrap 里,加了「来源」以后第一行五格、第二行两格再跟着
   搜索和按钮,参差不齐。 */
.fbar { display: flex; flex-direction: column; gap: 0; padding: 16px 18px; margin-bottom: 14px; }
.fields {
  display: grid; grid-template-columns: repeat(auto-fit, minmax(132px, 1fr));
  gap: 12px 14px; min-width: 0;
}
.f { display: flex; flex-direction: column; gap: 6px; min-width: 0; }
.f.wide { grid-column: span 2; }
/* 每个控件顶上一句小标签。没有它,七个下拉摆成一排全靠占位文字自我介绍,而占位文字
   在选过之后就消失了 —— 于是"这一格筛的是什么"要靠回想。 */
.fl { display: inline-flex; align-items: center; gap: 5px; font: 600 11.5px var(--font-body); color: var(--text-muted); letter-spacing: .02em; }
.f :deep(.vsel) { min-width: 0; width: 100%; }
.f :deep(.control) { height: 36px; border-radius: 9px; background: var(--surface-card); }
.rangebox, .searchbox {
  display: flex; align-items: center; gap: 6px; height: 36px; padding: 0 10px;
  border: 1px solid var(--border-default); border-radius: 9px; background: var(--surface-card);
  transition: border-color .15s, box-shadow .15s;
}
.rangebox:focus-within, .searchbox:focus-within { border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-subtle); }
/* 起止两个日期选择器本身各带一层框,摆进这个框里就成了框中框。这里去掉内层的框,
   让「起始 — 结束」看起来是一个输入。 */
.rangebox { padding: 0 6px; }
.rangebox :deep(.vdt) { flex: 1 1 0; min-width: 0; }
.rangebox :deep(.field) { height: 30px; border: none; background: transparent; padding: 0 6px; border-radius: 6px; }
.rangebox :deep(.field:hover), .rangebox :deep(.field.on) { background: var(--surface-sunken); }
.rangebox .dash { color: var(--text-faint); flex: none; }
.rangebox .clr { color: var(--text-faint); cursor: pointer; flex: none; }
.rangebox .clr:hover { color: var(--danger-text); }
.fbot {
  display: flex; align-items: center; gap: 12px; flex-wrap: wrap;
  margin-top: 14px; padding-top: 14px; border-top: 1px dashed var(--border-subtle);
}
.fbot .f.grow { flex: 1 1 320px; }
.searchbox { padding: 0 12px; gap: 8px; }
.searchbox .sk { flex: none; padding-right: 8px; border-right: 1px solid var(--border-subtle); font: 600 11.5px var(--font-body); color: var(--text-muted); white-space: nowrap; }
.searchbox input { flex: 1; min-width: 0; border: none; outline: none; background: transparent; color: var(--text-strong); font: 500 13px var(--font-body); }
.fbtns { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-left: auto; }
/* 改了条件还没按查询时说一句。没有它,一个改到一半的筛选栏配着上一次的结果,看起来
   就像"筛选没生效"。 */
.dirty {
  display: inline-flex; align-items: center; gap: 6px; height: 26px; padding: 0 10px; border-radius: 999px;
  background: var(--warning-subtle, rgba(245, 158, 11, .12)); color: var(--warning-text);
  font: 600 11.5px var(--font-body); white-space: nowrap;
}
.ddot { width: 6px; height: 6px; border-radius: 50%; background: currentColor; }
.gbtn {
  display: inline-flex; align-items: center; gap: 6px; height: 36px; padding: 0 13px;
  border: 1px solid var(--border-default); border-radius: 9px; background: var(--surface-card);
  color: var(--text-body); font: 600 12.5px var(--font-body); cursor: pointer;
  transition: border-color .15s, color .15s, background .15s;
}
.gbtn:hover { border-color: var(--accent); color: var(--accent-text); }
.gbtn.on { border-color: var(--accent); color: var(--accent-text); background: var(--accent-subtle); }
.gbtn:disabled { opacity: .6; cursor: default; }
.gbtn { white-space: nowrap; flex: none; }
.fbtns :deep(.vbtn) { min-width: 76px; }
/* 窄屏:筛选项两列,「起始 — 结束」独占一行;手机宽度一行一个,按钮区自己换行。
   从前「起始 — 结束」带着 300px 的最小宽度冲出卡片,按钮被挤成逐字竖排(黑盒 2026-10 工单 05)。 */
@media (max-width: 760px) {
  .fields { grid-template-columns: minmax(0, 1fr); }
  .f.wide { grid-column: auto; }
  .fbot { flex-direction: column; align-items: stretch; }
  .fbot .f.grow { flex: none; width: 100%; }
  .searchbox .sk { display: none; }
  .fbtns { margin-left: 0; }
}

/* 合并条目上的"共 N 条":点行打开详情,时间线里是全部 */
.gcount { font: 600 10.5px var(--font-body); color: var(--accent-text); background: var(--accent-subtle); border-radius: 999px; padding: 1px 7px; }

/* 审批单操作记录:一条竖线串起来的几步,当前打开的那一步高亮 */
.trail { list-style: none; margin: 0; padding: 0 0 0 4px; border-left: 2px solid var(--border-subtle); }
.titem { position: relative; display: flex; align-items: center; gap: 8px; flex-wrap: wrap; padding: 7px 10px; margin-left: 10px; border-radius: 8px; cursor: pointer; }
.titem:hover { background: var(--surface-sunken); }
.titem.on { background: var(--accent-subtle); }
.tdot { position: absolute; left: -17px; width: 10px; height: 10px; border-radius: 50%; background: var(--surface-card); border: 2px solid var(--border-strong, var(--border-default)); }
.titem.on .tdot { border-color: var(--accent); background: var(--accent); }
.ttime { font: 500 11.5px var(--font-mono); color: var(--text-muted); }
.twho { font: 500 12px var(--font-body); color: var(--text-body); margin-left: auto; }

/* 校验结论横幅。绿是"验过了",红是"这里断了" —— 后者要能被原样抄进事故记录里。 */
.vbar {
  display: flex; align-items: center; gap: 8px; margin-bottom: 14px; padding: 10px 14px;
  border-radius: var(--radius-lg, 10px); border: 1px solid transparent;
  background: var(--success-subtle); color: var(--success-text);
  font: 600 12.5px var(--font-body);
}
.vbar.bad { background: var(--danger-subtle); color: var(--danger-text); }
.vx { margin-left: auto; cursor: pointer; opacity: .7; }

/* ---- 表格 ----
   .thead 与每个 .tr 各自是一个 grid,所以列宽必须与内容无关:写 auto / max-content
   的话每一行按自己那行的内容各算一套,表头和行就对不齐了。
   时间列钉死 170px(两行排版,永不折);SQL 列是唯一可伸缩的那一列。 */
.thead, .tr {
  display: grid;
  grid-template-columns: 170px 152px 92px 190px minmax(220px, 1fr) 108px 116px 66px;
  gap: 12px; align-items: center; padding: 0 16px;
}
.thead {
  height: 40px; background: var(--surface-sunken); border-bottom: 1px solid var(--border-subtle);
  border-radius: var(--radius-lg, 10px) var(--radius-lg, 10px) 0 0;
  /* 表头用 slate-900 + 600,不是过去那种弱灰小字:它是每一列的名字,不是注脚 */
  font: 600 11.5px var(--font-body); color: var(--text-strong); letter-spacing: .02em;
}
.thead .right { text-align: right; }
.tr {
  min-height: 58px; padding-top: 9px; padding-bottom: 9px;
  border-bottom: 1px solid var(--border-subtle);
  font: 500 12.5px var(--font-body); color: var(--text-body);
  cursor: pointer; transition: background var(--dur-fast, .15s) var(--ease-out, ease);
}
.tr:last-of-type { border-bottom: none; }
.tr:hover { background: var(--surface-page); }
/* 高危那一行在左边留一道竖杠。扫一屏时它比任何一枚徽标都先被看见。 */
.tr.high { box-shadow: inset 3px 0 0 var(--danger); }

.tcell { display: flex; flex-direction: column; gap: 2px; white-space: nowrap; }
.clock { font: 600 13px var(--font-mono); color: var(--text-strong); font-variant-numeric: tabular-nums; }
.date { font: 500 12px var(--font-mono); color: var(--text-muted); font-variant-numeric: tabular-nums; }

.who { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.wname { font: 600 12.5px var(--font-body); color: var(--text-strong); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.wip { font: 500 11.5px var(--font-mono); color: var(--text-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.tgt { display: flex; flex-direction: column; gap: 4px; min-width: 0; align-items: flex-start; }
/* 实例胶囊按**分层**着色,不按环境名 —— 名字不决定它是什么环境,绑的分层才决定 */
.envtag {
  max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  padding: 2px 8px; border-radius: 6px; font: 600 11px var(--font-mono);
  background: var(--surface-sunken); color: var(--text-body); border: 1px solid var(--border-subtle);
}
.envtag.prod { background: var(--danger-subtle); color: var(--danger-text); border-color: transparent; }
.envtag.gli, .envtag.staging { background: var(--warning-subtle); color: var(--warning-text); border-color: transparent; }
.dbtbl { max-width: 100%; font: 500 11.5px var(--font-mono); color: var(--text-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.sqlcell { display: flex; align-items: center; gap: 8px; min-width: 0; }
.verb {
  flex: none; padding: 2px 7px; border-radius: 5px; font: 700 10.5px var(--font-mono);
  letter-spacing: .03em; white-space: nowrap;
}
.verb.v-high { background: var(--danger-subtle); color: var(--danger-text); }
.verb.v-mid { background: var(--warning-subtle); color: var(--warning-text); }
.verb.v-low { background: var(--accent-subtle); color: var(--accent-text); }
/* 语句原文:等宽 12px,一行,溢出省略。完整的那份在抽屉里 —— 行里铺开会把表格撑垮,
   而截断掉的往往正是要紧的那一段。 */
.sql { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font: 500 12px var(--font-mono); color: var(--text-body); }
.cp {
  flex: none; display: grid; place-items: center; width: 24px; height: 24px; border-radius: 6px;
  border: 1px solid transparent; background: transparent; color: var(--text-faint); cursor: pointer;
}
.tr:hover .cp { border-color: var(--border-subtle); color: var(--text-muted); }
.cp:hover { color: var(--accent-text) !important; border-color: var(--accent) !important; }

.cat { display: flex; flex-direction: column; gap: 3px; align-items: flex-start; }
.evbadge, .actbadge { padding: 1px 7px; border-radius: 5px; font: 600 10.5px var(--font-body); white-space: nowrap; }
.evbadge.ev-intercept { background: var(--warning-subtle); color: var(--warning-text); }
.evbadge.ev-approve { background: var(--accent-subtle); color: var(--accent-text); }
.evbadge.ev-exec { background: var(--success-subtle); color: var(--success-text); }
.evbadge.ev-login { background: var(--surface-sunken); color: var(--text-muted); }
.actbadge.act-create { background: var(--success-subtle); color: var(--success-text); }
.actbadge.act-delete { background: var(--danger-subtle); color: var(--danger-text); }
.actbadge.act-update { background: var(--warning-subtle); color: var(--warning-text); }
.actbadge.act-read { background: var(--accent-subtle); color: var(--accent-text); }
.actbadge.act-other { background: var(--surface-sunken); color: var(--text-muted); }
/* "没记"是一个短横,和"记了、结论是这一类"必须一眼分得开 */
/* 入口来源:数据通路(查询 / 终端 / 脚本 / 导出)各一种色相,流程类(审批 / 发布 / 开放接口)
   偏冷,非数据操作中性;旧行是淡色的一句话,不画成某一类。 */
.srccell { display: flex; align-items: center; min-width: 0; }
.srcbadge { padding: 2px 8px; border-radius: 5px; font: 600 11px var(--font-body); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 100%; background: var(--surface-sunken); color: var(--text-muted); }
.srcbadge.src-query { background: var(--accent-subtle); color: var(--accent-text); }
.srcbadge.src-terminal { background: var(--warning-subtle); color: var(--warning-text); }
.srcbadge.src-script { background: var(--danger-subtle); color: var(--danger-text); }
.srcbadge.src-export { background: var(--success-subtle); color: var(--success-text); }
.srcbadge.src-approval, .srcbadge.src-release, .srcbadge.src-api { background: var(--surface-sunken); color: var(--text-strong); border: 1px solid var(--border-subtle); }
.srcbadge.src-none { background: transparent; color: var(--text-faint); padding-left: 0; font-weight: 500; }
.evbadge.ev-none, .actbadge.act-none { background: transparent; color: var(--text-faint); padding-left: 0; }

.stcell { display: flex; flex-direction: column; gap: 4px; align-items: flex-start; min-width: 0; }
.stbadge {
  display: inline-flex; align-items: center; gap: 5px; padding: 3px 9px; border-radius: 999px;
  font: 600 11px var(--font-body); white-space: nowrap;
}
.st-ok { background: var(--success-subtle); color: var(--success-text); }
.st-bad { background: var(--danger-subtle); color: var(--danger-text); }
.st-warn { background: var(--warning-subtle); color: var(--warning-text); }
.st-wait { background: var(--warning-subtle); color: var(--warning-text); }
.st-info { background: var(--accent-subtle); color: var(--accent-text); }
.st-pass { background: var(--accent-subtle); color: var(--accent-text); }
.st-mute { background: var(--surface-sunken); color: var(--text-muted); border: 1px solid var(--border-subtle); }
.ap { font: 600 11px var(--font-mono); color: var(--text-faint); }
.ap.link { color: var(--accent-text); cursor: pointer; text-decoration: underline dotted; text-underline-offset: 3px; }

.acts { display: flex; justify-content: flex-end; }
.lnk { border: none; background: transparent; padding: 0; cursor: pointer; font: 600 12px var(--font-body); color: var(--accent-text); }
.lnk:hover { text-decoration: underline; }

/* 骨架行:形状先立住,内容再填进来 */
.tr.sk { cursor: default; }
.tr.sk:hover { background: transparent; }
.sk span { display: flex; flex-direction: column; gap: 5px; }
.bar { height: 10px; border-radius: 5px; background: var(--surface-sunken); animation: pulse 1.4s ease-in-out infinite; }
.w40 { width: 40%; } .w50 { width: 50%; } .w60 { width: 60%; } .w70 { width: 70%; } .w80 { width: 80%; } .w90 { width: 90%; }
@keyframes pulse { 0%, 100% { opacity: 1; } 50% { opacity: .45; } }

.empty { padding: 56px 24px; display: flex; flex-direction: column; align-items: center; gap: 10px; }
.eic { width: 46px; height: 46px; border-radius: 14px; background: var(--surface-sunken); display: grid; place-items: center; }
.et { font: 600 13px var(--font-body); color: var(--text-muted); }
.eclear { padding: 6px 12px; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-card); color: var(--accent-text); font: 600 11.5px var(--font-body); cursor: pointer; }

/* 分页靠右,和总数分列两端 */
.pfoot { display: flex; align-items: center; gap: 14px; padding: 11px 16px; border-top: 1px solid var(--border-subtle); }
.ptotal { font: 500 12px var(--font-body); color: var(--text-muted); }
.pager { margin-left: auto; display: flex; align-items: center; gap: 8px; }
.psize { display: inline-flex; align-items: center; gap: 6px; font: 500 11.5px var(--font-body); color: var(--text-muted); }
.psize select { height: 28px; padding: 0 6px; border: 1px solid var(--border-subtle); border-radius: 7px; background: var(--surface-sunken); color: var(--text-body); font: 500 11.5px var(--font-mono); cursor: pointer; outline: none; }
.pg { width: 30px; height: 28px; display: grid; place-items: center; border: 1px solid var(--border-subtle); border-radius: 8px; background: var(--surface-card); color: var(--text-body); cursor: pointer; }
.pg:disabled { opacity: .4; cursor: default; }
.pgn { min-width: 84px; text-align: center; font: 500 12px var(--font-mono); color: var(--text-muted); }

/* ---- 详情抽屉 ---- */
.overlay { position: fixed; inset: 0; z-index: 60; }
.mask { position: absolute; inset: 0; background: var(--surface-overlay); }
.drawer {
  position: absolute; top: 0; right: 0; bottom: 0; width: min(640px, 94vw);
  display: flex; flex-direction: column; background: var(--surface-card);
  border-left: 1px solid var(--border-subtle); box-shadow: var(--shadow-lg);
  animation: slidein .18s var(--ease-out, ease);
}
@keyframes slidein { from { transform: translateX(24px); opacity: .6; } to { transform: none; opacity: 1; } }
.dhead { display: flex; align-items: center; gap: 10px; padding: 14px 16px; border-bottom: 1px solid var(--border-subtle); }
.dtitle { font: 600 14px var(--font-display); color: var(--text-strong); }
.dx { margin-left: auto; cursor: pointer; color: var(--text-muted); }
.dbody { padding: 14px 16px 24px; overflow: auto; }
.dsec { margin-bottom: 14px; }
/* 元信息网格之后的那一段(链式校验值)要和网格拉开 —— 挨着的话,"结果"那一格的胶囊
   和下面的小标题会读成同一块。 */
.dgrid + .dsec { margin-top: 18px; margin-bottom: 0; }
.dlbl { display: flex; align-items: center; gap: 10px; font: 600 11px var(--font-body); color: var(--text-muted); text-transform: uppercase; letter-spacing: .06em; }
.dtools { margin-left: auto; display: flex; align-items: center; gap: 8px; text-transform: none; letter-spacing: 0; }
.seg { display: inline-flex; border: 1px solid var(--border-subtle); border-radius: 7px; overflow: hidden; }
.seg button { border: none; background: var(--surface-card); color: var(--text-muted); font: 600 11px var(--font-body); padding: 4px 9px; cursor: pointer; }
.seg button.on { background: var(--accent-subtle); color: var(--accent-text); }
.cp2 { display: inline-flex; align-items: center; gap: 4px; padding: 4px 9px; border: 1px solid var(--border-subtle); border-radius: 7px; background: var(--surface-card); color: var(--text-muted); font: 600 11px var(--font-body); cursor: pointer; }
.cp2:hover { color: var(--accent-text); border-color: var(--accent); }
.dcmd {
  margin: 7px 0 0; padding: 12px 14px; border-radius: 9px; background: var(--surface-page);
  border: 1px solid var(--border-subtle); font: 400 12.5px/1.7 var(--font-mono); color: var(--text-strong);
  white-space: pre-wrap; word-break: break-word; max-height: 44vh; overflow: auto;
}
.dgrid { display: grid; grid-template-columns: 1fr 1fr; gap: 12px 16px; }
.ml { font: 600 11px var(--font-body); color: var(--text-muted); text-transform: uppercase; letter-spacing: .06em; }
.dv { margin-top: 4px; font: 500 12.5px var(--font-body); color: var(--text-body); word-break: break-word; }
.dv.mono { font-family: var(--font-mono); }
.dv.none { color: var(--text-faint); font-size: 11.5px; }
.dv.ap.link { color: var(--accent-text); cursor: pointer; }
.hash { margin-top: 6px; padding: 8px 10px; border-radius: 8px; background: var(--surface-sunken); font: 400 11px/1.6 var(--font-mono); color: var(--text-muted); word-break: break-all; }
</style>
