<script setup lang="ts">
// 数据库变更发布 (CI/CD) — 发布单列表 + 流水线状态。
//
// The pipeline strip is the point of this page: a release is a sequence of
// stages, and what an operator needs to know at a glance is WHICH stage it is
// sitting on and why. So every stage is drawn even before it runs (pending), a
// stage blocked on a person is drawn differently from one doing work, and
// clicking a stage shows the log or the review findings it produced rather than
// a generic "failed".
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { renderPreText } from '@/lib/ruleText'
import { groupPrecheckIssues, formatStmtRuns } from '@/lib/precheckGroup'
import { oscItemText } from '@/lib/oscText'
import { reviewRuleName } from '@/lib/builtinNames'
import { renderRelLog, renderRelErr } from '@/lib/relLogText'
import {
  Rocket, Plus, X, Check, Loader, Hourglass, CircleX, Minus, Play,
  ShieldCheck, ClipboardCheck, DatabaseBackup, Terminal, Zap, Search, Bell, CircleAlert, RefreshCw, Copy,
  TriangleAlert, CircleDashed, FileCode, ScrollText,
} from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSelect from '@/components/common/VSelect.vue'
import api from '@/api'
import { CODE_MFA_REQUIRED, envError, localizeText } from '@/api/http'
import { confirmAction } from '@/lib/confirm'
import { copyText } from '@/lib/clipboard'
import { highlightSqlHtml } from '@/lib/sqlHighlight'
import { reviewGroups } from '@/lib/reviewGroups'
import { releaseExecutor, type ReleaseExecutor } from '@/lib/releaseExecutor'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import { useEnvTierStore } from '@/stores/envtier'
import type {
  Project, Connection, Pipeline, Release, ReleaseDetail, ReleaseStage, ReleaseStatus, ReviewResult, Precheck, PrecheckIssue,
  EnvType, StepItem,
} from '@/types'

const { t, te, locale } = useI18n()
// 预检结论按读者语言显示;旧单没有 ref,回落到存下的中文原串(lib/ruleText)。
const preI18n = { t: t as never, te: te as never }
// 内置规则名按读者语言显示;改过名的原样显示(lib/builtinNames reviewRuleName)。
const zhText = (k: string) => (te(k, 'zh') ? t(k, {}, { locale: 'zh' }) : undefined)
const ruleName = (code: string, stored: string) => reviewRuleName(code, stored, t, zhText)
// 阶段名是按类型派生的规范中文(后端 stageTypeLabel,进记录所以语言稳定),界面按 plType_* 显示。
// 与该类型的规范中文对不上的(派生之前的老快照里自定义的名字)是用户数据,原样显示。
const stageName = (type: string, stored: string) => {
  const key = 'plType_' + type
  return !stored || zhText(key) === stored ? t(key) : stored
}
const stageLabel = (st: ReleaseStage) => stageName(st.type, st.name)
// 阶段日志与错误条按读者语言渲染;老单没有 refs,显示存下的中文原串(lib/relLogText)。
const logI18n = computed(() => ({ t: t as never, te: te as never, locale: locale.value, stage: stageName, rule: ruleName }))
const stageLog = (st: ReleaseStage) => renderRelLog(st.logRefs, st.log, logI18n.value)
const releaseError = (r: Release) => renderRelErr(r.errorRef, r.error, logI18n.value)
const riskText = (r: string) => (r === 'high' ? t('highTag') : r === 'mid' ? t('med') : t('low'))
// 在线变更前置检查的阻塞 / 警告带着引擎的原话(表名、行数等细节):中文界面照原话,其它语言按 code 翻
const preMsg = (i: PrecheckIssue) => {
  const code = i.ref?.args?.code
  if (code && (i.ref?.code === 'preOscBlocked' || i.ref?.code === 'preOscWarn')) {
    const text = oscItemText({ code, reason: i.ref.args?.reason || '' }, locale.value, t, te)
    return t(i.ref.code === 'preOscBlocked' ? 'preText.preOscBlocked' : 'preText.preOscWarn', { reason: text })
  }
  return renderPreText(i.ref, i.message, preI18n)
}
const preNote = (p: Precheck) => {
  const parts = (p.noteRefs || []).map((r) => renderPreText(r, '', preI18n))
  // 任何一条认不出就整行回落到中文原串,不拼半句。
  return parts.length && parts.every(Boolean) ? parts.join(' ') : (p.note || '')
}
const ui = useUIStore()
const auth = useAuthStore()
// 环境胶囊的颜色跟分层走,和库树、审批列表、分层页同一个来源(envtier.dotFor)。
// 分层表没加载到时一律中性色 —— 不替查不到的分层断言它是生产还是测试。
const envtier = useEnvTierStore()
const envTone = (tierCode: string): EnvType => (tierCode ? envtier.dotFor(tierCode) as EnvType : 'muted')

const releases = ref<Release[]>([])
const openId = ref(0)
// 能看全部发布单的人(L2 及以上、审计员 —— 后端 canSeeAllReleases 说了算)一进来就是「全部」
// (2026-10-30 用户确认);其他人只有「我的」。
const canSeeAll = computed(() => !!auth.me?.canSeeAllReleases)
const scope = ref<'mine' | 'all'>(canSeeAll.value ? 'all' : 'mine')
// 按项目跟进升级单 —— 这正是项目这个维度存在的理由。0 = 全部项目。
const route = useRoute()
const projectFilter = ref(0)
const projects = ref<Project[]>([])
// 按单号 / 环境找单。都在后端筛:列表只取最新 50 张,前端自己过滤会漏掉更早的单。
const keyword = ref('')
const envFilter = ref('') // environment 编码(fat 等),不是分层;'' = 全部环境
const envs = ref<string[]>([])
// 选项来自后端(当前范围和项目下落过的环境)。切了范围 / 项目之后选中的那个可能不在里面,
// 也要留着 —— 否则下拉显示成空白,人却还在按它筛。
const envOptions = computed(() =>
  envFilter.value && !envs.value.includes(envFilter.value) ? [...envs.value, envFilter.value].sort() : envs.value,
)
const filtering = computed(() => !!keyword.value.trim() || !!envFilter.value)
const pipelines = ref<Pipeline[]>([])
const conns = ref<Connection[]>([])
const openStage = ref(0)
let timer: ReturnType<typeof setInterval> | null = null

// 选中那张单的详情单独存一份。列表接口故意不带阶段日志 / 审查结果(体积,见后端
// ListReleases),所以列表一刷新(轮询、切「我的 / 全部」、换项目)就会把摊开的那张盖成
// "该阶段暂无日志" —— 用户在 REL-1 上看到的正是这个。摊开的单只从详情取内容,列表只决定
// 它还在不在当前范围里。
const detail = ref<ReleaseDetail | null>(null)
const open = computed<ReleaseDetail | null>(() => {
  const inList = releases.value.find((r) => r.id === openId.value)
  if (!inList) return null
  return detail.value?.id === inList.id ? detail.value : inList
})
// A run is "live" while it can still change without anyone touching THIS page.
// `waiting` counts: the decision arrives through the approval queue (or the 飞书
// card, or the external callback) and the server-side sweeper resumes the run —
// so a page that stopped polling there would sit on "等待处理" long after the
// pipeline had finished.
const anyLive = computed(() =>
  releases.value.some((r) => r.status === 'pending' || r.status === 'running' || r.status === 'waiting'),
)

// 请求编号:边打字边筛,慢的旧响应不能把新结果盖掉。
let listSeq = 0
// 筛选条件变了:等到第一份新结果回来,再决定摊开的那张还留不留(见 loadList)。
let reselectAfterLoad = false
async function loadList() {
  const seq = ++listSeq
  try {
    const page = await api.releases(scope.value, '', 1, 50, projectFilter.value, keyword.value.trim(), envFilter.value)
    if (seq !== listSeq) return
    releases.value = page.items
    envs.value = page.envs || []
    if (reselectAfterLoad) {
      reselectAfterLoad = false
      // 摊开的那张被筛掉了就换成结果里的第一张,没结果就空着。别留一张列表里没有的单。
      if (!page.items.some((r) => r.id === openId.value)) {
        openId.value = page.items[0]?.id || 0
        openStage.value = 0
        detail.value = null
        refreshOpen()
      }
    }
    if (!openId.value && page.items.length) openId.value = page.items[0].id
    ui.pageSub = { key: 'rlSub2', params: { n: page.total } }
  } catch (e) { ui.notifyError(e, t('loadFailed')) }
}
function applyFilters() {
  reselectAfterLoad = true
  return loadList()
}
// 关键字:停手再查。挂在 watch 上而不是 @input —— 输入法拼写过程中 v-model 不更新,不会拿半截拼音去查。
let searchTimer: ReturnType<typeof setTimeout> | null = null
function cancelSearchTimer() {
  if (searchTimer) { clearTimeout(searchTimer); searchTimer = null }
}
watch(keyword, () => {
  cancelSearchTimer()
  searchTimer = setTimeout(() => { searchTimer = null; applyFilters() }, 300)
}, { flush: 'sync' }) // 同步设定时器:清除 / 回车紧接着取消它,不能多查一次
function searchNow() {
  cancelSearchTimer()
  applyFilters()
}
function clearKeyword() {
  keyword.value = ''
  searchNow()
}
// 每次请求编号:先点 A 再点 B,A 的响应晚到也不能把 B 的详情换掉。
let openSeq = 0
async function refreshOpen() {
  if (!openId.value) return
  const seq = ++openSeq
  try {
    const r = await api.release(openId.value)
    if (seq !== openSeq || r.id !== openId.value) return
    detail.value = r
    const i = releases.value.findIndex((x) => x.id === r.id)
    if (i >= 0) releases.value[i] = r
  } catch { /* the list keeps the last known state */ }
}
function select(r: Release) {
  openId.value = r.id
  openStage.value = 0
  codeTab.value = 'sql' // 换了单先看它要改什么;openStage 本来就是 0 时 watch 不会触发
  refreshOpen()
}

// 手动刷新:列表和摊开那张的详情一起重拿,选中的单和选中的阶段都不动。
const refreshing = ref(false)
async function refreshAll() {
  if (refreshing.value) return
  refreshing.value = true
  try { await Promise.all([loadList(), refreshOpen()]) }
  finally { refreshing.value = false }
}

onMounted(async () => {
  envtier.load().catch(() => { /* 胶囊退回中性色 */ })
  try { conns.value = await api.connections() } catch { /* the form falls back to an empty picker */ }
  try { pipelines.value = await api.pipelines() } catch { /* same */ }
  try { projects.value = await api.projects() } catch { /* 筛选器降级为“全部项目” */ }
  // 从「项目」页点进来时带着 ?project=<id>:直接落到筛过的列表,而不是让人
  // 到了这页再从下拉里把刚才点的那个项目重新找一遍。
  const fromProject = Number(route.query.project)
  if (fromProject > 0) projectFilter.value = fromProject
  // 右上角「待办」里点一条「等你确认」过来时带着 ?rel=<id>:直接把那张单摊开。
  // 在 loadList 之前设,好让它不被"默认选中第一条"盖掉。
  const fromTodo = Number(route.query.rel)
  if (fromTodo > 0) openId.value = fromTodo
  await loadList()
  // 那张单不一定在当前范围/当前页里 —— 确认人常常不是发起人,看不到全部的人默认范围是"我的"。
  // 直接问服务端要这一张,而不是在已加载的列表里找:找不到的表现是页面看起来坏了。
  if (fromTodo > 0 && !releases.value.some((r) => r.id === fromTodo)) {
    try { releases.value.unshift(await api.release(fromTodo)) }
    catch { ui.notify(t('rlNotFound', { rel: fromTodo }), 'info') }
  }
  await refreshOpen()
  timer = setInterval(pollTick, 2500)
})
// 轮询:上一轮(列表 + 详情)没回来就跳过这一拍。
// 不加这道闸,后端一慢(2026-11-10 本地 SQLite 单连接被压住),每 2.5 秒照发一轮,
// 请求在后端排队越堆越长,连登录都排在后面等了 1 分多钟 —— 页面自己把后端拖死。
let polling = false
async function pollTick() {
  if (polling || !anyLive.value) return
  polling = true
  try { await Promise.all([loadList(), refreshOpen()]) }
  finally { polling = false }
}
onUnmounted(() => { if (timer) clearInterval(timer); cancelSearchTimer() })

// ---- stage presentation ----
const stageIcon: Record<string, any> = {
  review: ShieldCheck, approve: ClipboardCheck, backup: DatabaseBackup,
  execute: Terminal, auto_execute: Zap, verify: Search, manual: Hourglass, notify: Bell,
}
// 自动执行在分层不再允许时退回成执行闸(见后端 stageExecute),按钮与执行变更相同
const isExecStage = (ty: string) => ty === 'execute' || ty === 'auto_execute'
function statusMeta(s: string) {
  switch (s) {
    case 'success': return { icon: Check, c: 'var(--success-text)', bg: 'var(--success-subtle)', spin: false }
    case 'running': return { icon: Loader, c: 'var(--accent-text)', bg: 'var(--accent-subtle)', spin: true }
    case 'waiting': return { icon: Hourglass, c: 'var(--warning-text)', bg: 'var(--warning-subtle)', spin: false }
    case 'failed': return { icon: CircleX, c: 'var(--danger-text)', bg: 'var(--danger-subtle)', spin: false }
    case 'skipped': return { icon: Minus, c: 'var(--text-faint)', bg: 'var(--surface-sunken)', spin: false }
    case 'aborted': return { icon: CircleX, c: 'var(--text-muted)', bg: 'var(--surface-sunken)', spin: false }
    default: return { icon: CircleDashed, c: 'var(--text-muted)', bg: 'var(--surface-sunken)', spin: false }
  }
}
// The connector between two stages reports what has actually flowed: solid
// behind finished work, animated into the stage running right now, flat ahead
// of it. Computed from the PAIR, which is why it cannot live in the template.
function connClass(stages: ReleaseStage[], i: number): StepItem['conn'] {
  const prevDone = ['success', 'skipped'].includes(stages[i - 1]?.status)
  if (!prevDone) return ''
  return stages[i].status === 'running' ? 'live' : 'done'
}
function duration(st: ReleaseStage) {
  if (!st.startedAt) return ''
  const end = st.finishedAt ? new Date(st.finishedAt).getTime() : Date.now()
  const ms = end - new Date(st.startedAt).getTime()
  if (ms < 1000) return ms + 'ms'
  return (ms / 1000).toFixed(1) + 's'
}
function fmt(s: string | null) { return s ? new Date(s).toLocaleString('en-GB') : '—' }

// A review stage carries its findings as JSON so a blocked release can still be
// explained after the rule library has moved on.
function findingsOf(st: ReleaseStage): ReviewResult | null {
  if (!st.findings) return null
  try { return JSON.parse(st.findings) as ReviewResult } catch { return null }
}
const openStageObj = computed(() => open.value?.stages.find((s) => s.id === openStage.value) || null)
// 只解析一次:从前模板里 findingsOf() 调了好几遍,每次整段 JSON.parse(老的大单有几十 MB)。
const openReview = computed(() => (openStageObj.value ? findingsOf(openStageObj.value) : null))
const openGroups = computed(() => reviewGroups(openReview.value))
const previewGroups = computed(() => reviewGroups(preview.value))
// 执行变更人:执行阶段点「确认执行」的人;自动执行阶段没经人手写「自动执行」。
const openExecutor = computed(() => releaseExecutor(open.value?.stages))
function executorText(e: ReleaseExecutor): string {
  const parts = [...e.names]
  if (e.auto) parts.push(t('rlExecAuto'))
  return parts.join('、')
}
const executorOf = (r: Release) => executorText(releaseExecutor(r.stages))

// 步骤条的节点。每个节点自带与前一节点之间连线的状态,模板里不再成对地算。
const steps = computed<StepItem[]>(() => {
  const stages = open.value?.stages || []
  return stages.map((st, i) => ({
    stage: st,
    label: stageLabel(st),
    status: st.status as ReleaseStatus,
    duration: duration(st),
    conn: i > 0 ? connClass(stages, i) : '',
  }))
})
function toggleStage(id: number) {
  openStage.value = openStage.value === id ? 0 : id
}

// ---- 脚本 / 日志查看器 ----
// 选中阶段就是想看它干了什么:自动切到日志;取消选中回到脚本。手动点的 tab 不被覆盖,
// 直到下一次换阶段。
const codeTab = ref<'sql' | 'log'>('sql')
watch(openStage, (id) => { codeTab.value = id ? 'log' : 'sql' })
const sqlText = computed(() => open.value?.sql || '')
const sqlHtml = computed(() => highlightSqlHtml(sqlText.value))
const logText = computed(() => (openStageObj.value?.log ? stageLog(openStageObj.value) : ''))
const codeText = computed(() => (codeTab.value === 'sql' ? sqlText.value : logText.value))
// 行号单独一列(不进 <pre> 的文本):复制、选中、断言拿到的都是原文,不夹行号。
const codeLines = computed(() => (codeText.value ? codeText.value.replace(/\n$/, '').split('\n').length : 0))
const gutterText = computed(() => Array.from({ length: codeLines.value }, (_, i) => i + 1).join('\n'))
async function copyCode() {
  if (await copyText(codeText.value)) ui.notify(t('copied'), 'success')
  else ui.notify(t('actionFailed'), 'error')
}

// 收单检查。存在单子上的是一串 JSON —— 解析不出来就当没有,不去猜它的形状。
//
// null 有两种来历,面板上要分开说:0036 之前的老单子没有这一栏,以及这次检查一条都
// 没做成。两者都**不是**"检查通过" —— 一块什么都不显示的区域会被读成没问题。
const precheck = computed<Precheck | null>(() => {
  const raw = open.value?.precheck
  if (!raw) return null
  try { return JSON.parse(raw) as Precheck } catch { return null }
})

// 这份结果覆盖了多少 —— 读它的人最需要的一句话。
// 只说"0 个错误"会让人以为每条语句都验过了,而 DDL 根本没法在不执行的前提下验。
const precheckCoverage = computed(() => {
  const p = precheck.value
  if (!p) return ''
  return t('rlPreCoverage', { checked: p.syntaxChecked, skipped: p.syntaxSkipped })
})

// 对象结论的来源。cache 时把那份缓存的时间一并说出来:一份三天前的缓存说"表不存在",
// 很可能只是那张表是昨天建的。
const precheckSource = computed(() => {
  const p = precheck.value
  if (!p) return ''
  if (p.objectSource === 'cache') {
    return t('rlPreSrcCache', { at: p.cacheSyncedAt ? fmt(p.cacheSyncedAt) : '—' })
  }
  if (p.objectSource === 'live') return t('rlPreSrcLive')
  return t('rlPreSrcNone')
})

// 风险提示块的色调:有错误 = 危险,有警告或根本没有检查结果 = 警示(没结果不等于通过),
// 都没有 = 通过。
const precheckLevel = computed(() => {
  const p = precheck.value
  if (!p) return 'lv-warn'
  if (p.errors) return 'lv-err'
  if (p.warnings || (p.issues || []).some((i) => i.level === 'warn')) return 'lv-warn'
  return 'lv-ok'
})

// 同一结论的条目并成一行(第 1–5、12 条 · 共 6 条),错误排在前面;超过 PRE_FOLD 行先收起。
// 几百条 DDL 的脚本原来是几百行一模一样的"未做语法检查"(2026-11-23)。
const PRE_FOLD = 6
const preAll = ref(false)
const precheckGroups = computed(() => groupPrecheckIssues(precheck.value?.issues || [], preMsg))
const shownPreGroups = computed(() => (preAll.value ? precheckGroups.value : precheckGroups.value.slice(0, PRE_FOLD)))
const stmtRuns = (ns: number[]) => formatStmtRuns(ns, locale.value.startsWith('zh') ? '、' : ', ')
watch(() => open.value?.id, () => { preAll.value = false })

function issueCls(level: string) {
  return level === 'error' ? 'err' : level === 'warn' ? 'warn' : 'info'
}

// ---- actions ----
async function abort(r: Release) {
  if (!(await confirmAction(t('rlAbortConfirm', { no: r.relNo })))) return
  try {
    const env = await api.abortRelease(r.id)
    if (env.code !== 0) { ui.notifyError(envError(env), t('actionFailed')); return }
    await loadList(); await refreshOpen()
  } catch (e) { ui.notifyError(e, t('actionFailed')) }
}
async function continueStage(r: Release, st: ReleaseStage) {
  try {
    const env = await api.continueStage(r.id, st.id)
    if (env.code !== 0) { ui.notifyError(envError(env), t('actionFailed')); return }
    ui.notify(t('rlContinued'), 'success')
    setTimeout(() => { loadList(); refreshOpen() }, 400)
  } catch (e) { ui.notifyError(e, t('actionFailed')) }
}

// ---- create form ----
const formOpen = ref(false)
// changeType: '' = 自动推断;声明了就必须与内容一致(后端强校验,DML/DDL 不得同单)
const f = ref({ title: '', pipelineId: 0, connectionId: 0, database: '', sql: '', changeType: '', reason: '', mfaCode: '' })
const needMfa = ref(false)
const busy = ref(false)
const preview = ref<ReviewResult | null>(null)
const dbOptions = ref<string[]>([])

const connLabels = computed(() => conns.value.map((c) => `${c.env}-${c.name}`))
const connLabel = computed({
  get: () => {
    const c = conns.value.find((x) => x.id === f.value.connectionId)
    return c ? `${c.env}-${c.name}` : ''
  },
  set: (l: string) => {
    const c = conns.value.find((x) => `${x.env}-${x.name}` === l)
    if (c) { f.value.connectionId = c.id; loadDbs(c.id) }
  },
})
const pipeLabels = computed(() => pipelines.value.filter((p) => p.enabled).map(pipeLabel))
function pipeLabel(p: Pipeline) {
  return p.tierCode ? `${p.name} (${p.tierCode.toUpperCase()})` : p.name
}
const pipeLabelSel = computed({
  get: () => {
    const p = pipelines.value.find((x) => x.id === f.value.pipelineId)
    return p ? pipeLabel(p) : ''
  },
  set: (l: string) => {
    const p = pipelines.value.find((x) => pipeLabel(x) === l)
    if (p) f.value.pipelineId = p.id
  },
})

async function loadDbs(id: number) {
  f.value.database = ''; dbOptions.value = []
  try {
    const sc = await api.connectionSchema(id)
    dbOptions.value = sc.databases.map((d) => d.name)
    const c = conns.value.find((x) => x.id === id)
    if (c?.database && dbOptions.value.includes(c.database)) f.value.database = c.database
    else if (dbOptions.value.length) f.value.database = dbOptions.value[0]
  } catch { /* the field stays free text */ }
}

function openForm() {
  const firstPipe = pipelines.value.find((p) => p.enabled && p.isDefault) || pipelines.value.find((p) => p.enabled)
  f.value = {
    title: '', pipelineId: firstPipe?.id || 0, connectionId: conns.value[0]?.id || 0,
    database: '', sql: '', changeType: '', reason: '', mfaCode: '',
  }
  preview.value = null
  needMfa.value = false
  if (f.value.connectionId) loadDbs(f.value.connectionId)
  formOpen.value = true
}

// Pre-flight review: the same library the pipeline's review stage will run, so
// the operator sees what would block them before a release number exists.
async function preflight() {
  if (!f.value.sql.trim() || !f.value.connectionId) return
  try {
    const r = await api.reviewCheck({ connectionId: f.value.connectionId, sql: f.value.sql })
    preview.value = r.result
  } catch (e) { ui.notifyError(e, t('actionFailed')) }
}

async function submit() {
  if (!f.value.title.trim() || !f.value.connectionId || !f.value.sql.trim()) {
    ui.notify(t('rlFormIncomplete'), 'error'); return
  }
  busy.value = true
  try {
    const env = await api.createRelease({
      title: f.value.title.trim(), pipelineId: f.value.pipelineId, connectionId: f.value.connectionId,
      database: f.value.database, sql: f.value.sql, changeType: f.value.changeType, reason: f.value.reason.trim(), mfaCode: f.value.mfaCode.trim(),
    })
    if (env.code === CODE_MFA_REQUIRED) { needMfa.value = true; ui.notify(t('rlNeedMfa'), 'error'); return }
    if (env.code !== 0) { ui.notifyError(envError(env), t('actionFailed')); return }
    formOpen.value = false
    // 刚提的单不一定在当前的单号 / 环境筛选里,而提完要看的就是它。
    keyword.value = ''
    envFilter.value = ''
    cancelSearchTimer()
    await loadList()
    if (env.data?.id) { openId.value = env.data.id; await refreshOpen() }
  } catch (e) { ui.notifyError(e, t('actionFailed')) } finally { busy.value = false }
}

// 上游单号是对端系统(发布平台)那边的编号,跨系统对账、找人时要整串报给对方 —— 给个复制。
async function copyRef(ref: string) {
  if (await copyText(ref)) ui.notify(t('copied'), 'success')
  else ui.notify(t('actionFailed'), 'error')
}

function riskCls(r: string) { return r === 'high' ? 'bad' : r === 'mid' ? 'warn' : 'ok' }
</script>

<template>
  <div class="scy page">
    <div class="v-head">
      <div class="v-head-l">
        <div class="v-eyebrow">RELEASE PIPELINE</div>
        <div class="v-sub">{{ $t('rlSub') }}</div>
      </div>
      <div class="v-head-acts">
        <div class="rsearch">
          <Search :size="13" class="rsicon" />
          <input
            v-model="keyword" class="rsinput" type="text" maxlength="128"
            :placeholder="$t('rlSearchPh')" :aria-label="$t('rlSearchPh')" @keydown.enter="searchNow"
          >
          <button v-if="keyword" type="button" class="rsclear" :title="$t('rlSearchClear')" :aria-label="$t('rlSearchClear')" @click="clearKeyword">
            <X :size="12" />
          </button>
        </div>
        <div v-if="canSeeAll" class="seg small">
          <div class="si" :class="{ active: scope === 'mine' }" @click="scope = 'mine'; applyFilters()">{{ $t('rlMine') }}</div>
          <div class="si" :class="{ active: scope === 'all' }" @click="scope = 'all'; applyFilters()">{{ $t('rlAll') }}</div>
        </div>
        <div class="pfilter">
          <select v-model.number="projectFilter" class="psel" @change="applyFilters()">
            <option :value="0">{{ $t('rlAllProjects') }}</option>
            <option v-for="p in projects" :key="p.id" :value="p.id">{{ p.name }}</option>
          </select>
        </div>
        <div class="pfilter">
          <select v-model="envFilter" class="psel rlenv" :aria-label="$t('rlAllEnvs')" @change="applyFilters()">
            <option value="">{{ $t('rlAllEnvs') }}</option>
            <option v-for="e in envOptions" :key="e" :value="e">{{ e }}</option>
          </select>
        </div>
        <VButton class="rl-refresh" :disabled="refreshing" :title="$t('rlRefresh')" @click="refreshAll">
          <RefreshCw :size="14" :class="{ spin: refreshing }" />{{ refreshing ? $t('rlRefreshing') : $t('rlRefresh') }}
        </VButton>
        <VButton variant="primary" @click="openForm"><Plus :size="15" />{{ $t('rlNew') }}</VButton>
      </div>
    </div>

    <div class="wrap">
      <!-- 左栏:发布单卡片列表 -->
      <div class="list">
        <div
          v-for="r in releases" :key="r.id" class="ritem" :class="{ on: r.id === openId }"
          role="button" tabindex="0" @click="select(r)" @keydown.enter="select(r)"
        >
          <div class="rtop">
            <span class="rno">{{ r.relNo }}</span>
            <span v-if="r.changeType" class="ctb" :class="r.changeType">{{ r.changeType.toUpperCase() }}</span>
            <span v-if="r.projectName" class="prj">{{ r.projectName }}</span>
            <span class="pill" :class="r.status">{{ $t('rlSt_' + r.status) }}</span>
          </div>
          <div class="rtitle">{{ r.title }}</div>
          <!-- 在哪儿改:环境、实例、库。库是这张单真正动的东西,不再压成灰字。 -->
          <div class="rmeta rwhere">
            <span class="tag env" :class="envTone(r.tierCode)" :title="$t('rlKEnv')">{{ r.env || '—' }}</span>
            <span class="rinst">{{ r.instance }}</span>
            <span v-if="r.database" class="rdb" :title="$t('rlKDb')">/ {{ r.database }}</span>
          </div>
          <!-- 从哪儿来:上游单号带标签,一眼和本单号 REL-xx 分开;再是来源系统、发起人。 -->
          <div class="rmeta rfrom">
            <span v-if="r.externalRef" class="xref" :title="$t('rlKRef')"><span class="xk">{{ $t('rlKRefShort') }}</span>{{ r.externalRef }}</span>
            <span v-if="r.source === 'api'" class="api" :title="r.clientName">API</span>
            <span v-if="r.source === 'api' && r.clientName" class="dim">{{ r.clientName }}</span>
            <span class="dim">· {{ r.creator }}</span>
            <span v-if="executorOf(r)" class="dim rexec" :title="$t('rlKExecutor')">→ {{ $t('rlExecShort', { who: executorOf(r) }) }}</span>
          </div>
        </div>
        <div v-if="!releases.length" class="empty">{{ filtering ? $t('rlNoMatch') : $t('rlEmpty') }}</div>
      </div>

      <!-- 右栏:详情,拆成独立卡片 -->
      <div v-if="open" class="detail">
        <!-- ① 头部 + 关键信息 -->
        <section class="card hcard">
          <div class="dhead">
            <div class="dic"><Rocket :size="18" /></div>
            <div class="grow">
              <div class="dt">
                <span class="dno">{{ open.relNo }}</span>
                <span class="dtitle">{{ open.title }}</span>
                <span v-if="open.changeType" class="ctb big" :class="open.changeType">{{ open.changeType.toUpperCase() }}</span>
              </div>
              <div class="ds">{{ open.pipelineName }} · {{ fmt(open.createdAt) }}</div>
            </div>
            <div class="dbadges">
              <span v-if="open.env" class="envcap" :class="envTone(open.tierCode)" :title="$t('rlKEnv')">{{ open.env.toUpperCase() }}</span>
              <span class="ostat" :class="open.status">{{ $t('rlSt_' + open.status) }}</span>
              <span class="pill" :class="riskCls(open.risk)">{{ $t('rlRisk') }} {{ riskText(open.risk) }}</span>
              <VButton v-if="open.status === 'pending' || open.status === 'waiting'" height="32px" @click="abort(open)">
                {{ $t('rlAbort') }}
              </VButton>
            </div>
          </div>

          <!-- 关键信息:审批、执行、事后对账的人先要知道的几件事 —— 改哪个环境的哪个库、
               对应上游哪张单、谁从哪儿提的。 -->
          <div class="kinfo" data-testid="rl-keyinfo">
            <div class="kv" data-k="env">
              <span class="k">{{ $t('rlKEnv') }}</span>
              <span class="v"><b class="envchip" :class="envTone(open.tierCode)">{{ open.env || '—' }}</b><span v-if="open.tierCode" class="tag tier" :class="envTone(open.tierCode)" :title="$t('rlKTier')">{{ open.tierCode.toUpperCase() }}</span></span>
            </div>
            <div class="kv" data-k="inst">
              <span class="k">{{ $t('rlKInst') }}</span>
              <span class="v mono strong">{{ open.instance || '—' }}<span v-if="open.engine" class="dim"> · {{ open.engine }}</span></span>
            </div>
            <div class="kv" data-k="db">
              <span class="k">{{ $t('rlKDb') }}</span>
              <span class="v mono strong">{{ open.database || '—' }}</span>
            </div>
            <div class="kv" data-k="project">
              <span class="k">{{ $t('rlKProject') }}</span>
              <span class="v strong">{{ open.projectName || '—' }}</span>
            </div>
            <div class="kv" data-k="creator">
              <span class="k">{{ $t('rlKCreator') }}</span>
              <span class="v strong">{{ open.creator || '—' }}</span>
            </div>
            <div class="kv" data-k="executor">
              <span class="k">{{ $t('rlKExecutor') }}</span>
              <span v-if="executorText(openExecutor)" class="v strong">{{ executorText(openExecutor) }}</span>
              <span v-else-if="openExecutor.waiting" class="v dim">{{ $t('rlExecWaiting') }}</span>
              <span v-else class="v dim">—</span>
            </div>
            <div class="kv" data-k="source">
              <span class="k">{{ $t('rlKSource') }}</span>
              <span class="v strong">
                <template v-if="open.source === 'api'"><span class="api">API</span> {{ open.clientName || '—' }}</template>
                <template v-else>{{ $t('rlKConsole') }}</template>
              </span>
            </div>
            <div class="kv" data-k="pipeline">
              <span class="k">{{ $t('rlPipeline') }}</span>
              <span class="v strong">{{ open.pipelineName || '—' }}</span>
            </div>
            <div class="kv wide" data-k="ref">
              <span class="k">{{ $t('rlKRef') }}</span>
              <span v-if="open.externalRef" class="v mono strong nowrap">
                <span class="refv">{{ open.externalRef }}</span>
                <button type="button" class="kcopy" :title="$t('copy')" :aria-label="$t('copy')" @click="copyRef(open.externalRef)"><Copy :size="12" /></button>
              </span>
              <span v-else class="v dim">—</span>
            </div>
            <div class="kv wide" data-k="time">
              <span class="k">{{ $t('rlKTime') }}</span>
              <span class="v mono"><span class="nw">{{ fmt(open.createdAt) }}</span><template v-if="open.finishedAt"><span class="dim">→</span><span class="nw">{{ fmt(open.finishedAt) }}</span></template></span>
            </div>
            <div v-if="open.reason" class="reason">{{ $t('reason') }}: {{ open.reason }}</div>
          </div>
        </section>

        <!-- ② 流水线步骤条 -->
        <section class="card">
          <div class="ctitle"><Rocket :size="14" />{{ $t('rlCardPipeline') }}</div>
          <div class="pipe scx">
            <template v-for="(s, i) in steps" :key="s.stage.id">
              <div v-if="i > 0" class="conn" :class="s.conn" />
              <div
                class="node" :class="[s.status, { sel: openStage === s.stage.id }]"
                role="button" tabindex="0" @click="toggleStage(s.stage.id)" @keydown.enter="toggleStage(s.stage.id)"
              >
                <div
                  class="dot" :class="{ halo: s.status === 'running' }"
                  :style="{ background: statusMeta(s.status).bg, color: statusMeta(s.status).c }"
                >
                  <component :is="statusMeta(s.status).icon" :size="17" :class="{ spin: statusMeta(s.status).spin }" />
                </div>
                <div class="nname">
                  <component :is="stageIcon[s.stage.type] || Terminal" :size="12" />
                  {{ s.label }}
                </div>
                <div class="nst" :style="{ color: statusMeta(s.status).c }">{{ $t('rlSt_' + s.status) }}</div>
                <div class="ndur">{{ s.duration || '—' }}</div>
              </div>
            </template>
          </div>
          <div v-if="open.error" class="errbar"><CircleAlert :size="14" />{{ releaseError(open) }}</div>
        </section>

        <!-- ③ 收单检查 / 风险提示:收单那一刻算的,不是现在算的。
             它**不拦发布** —— 有 error 的单子照样会往下走,卡不卡由流程的阶段决定。 -->
        <section class="card pbox" :class="precheckLevel">
          <div class="pbhead">
            <span class="pbic"><component :is="precheckLevel === 'lv-ok' ? ShieldCheck : TriangleAlert" :size="15" /></span>
            <span class="pbt">{{ $t('rlPrecheck') }}</span>
            <template v-if="precheck">
              <span class="pcnt err" :class="{ zero: !precheck.errors }">{{ $t('rlPreErr', { n: precheck.errors }) }}</span>
              <span class="pcnt warn" :class="{ zero: !precheck.warnings }">{{ $t('rlPreWarn', { n: precheck.warnings }) }}</span>
              <span class="pdim">{{ precheckCoverage }}</span>
            </template>
          </div>
          <!-- 没有结果时说清是哪一种"没有",不留白 —— 空白会被读成通过 -->
          <div v-if="!precheck" class="pnone">{{ $t('rlPreAbsent') }}</div>
          <template v-else>
            <div class="psrc">{{ precheckSource }}</div>
            <div v-if="precheck.note" class="psrc">{{ preNote(precheck) }}</div>
            <div v-if="!precheck.issues.length" class="pnone">{{ $t('rlPreNoIssue') }}</div>
            <div v-for="(g, k) in shownPreGroups" :key="k" class="pitem" :class="issueCls(g.level)">
              <span class="pstmt">{{ g.stmts.some((n) => n > 0) ? $t('rlPreStmt', { n: stmtRuns(g.stmts) }) : '·' }}</span>
              <span v-if="g.object" class="pobj">{{ g.object }}</span>
              <span class="pmsg">{{ g.message }}</span>
              <span v-if="g.stmts.length > 1" class="pgcnt">{{ $t('rlPreGroupN', { n: g.stmts.length }) }}</span>
              <span v-if="g.source" class="psrctag">{{ g.source === 'cache' ? $t('rlPreTagCache') : $t('rlPreTagLive') }}</span>
            </div>
            <button v-if="precheckGroups.length > PRE_FOLD" type="button" class="pmore" @click="preAll = !preAll">
              {{ preAll ? $t('rlPreLess') : $t('rlPreMore', { n: precheckGroups.length - PRE_FOLD }) }}
            </button>
          </template>
        </section>

        <!-- ④ 阶段详情 + 脚本 / 日志查看器 -->
        <section class="card sbox">
          <div class="sbhead">
            <template v-if="openStageObj">
              <span class="sbt">{{ stageLabel(openStageObj) }}</span>
              <span class="pill" :class="openStageObj.status">{{ $t('rlSt_' + openStageObj.status) }}</span>
              <span v-if="openStageObj.approvalNo" class="apno">{{ openStageObj.approvalNo }}</span>
              <!-- 两种人工闸共用一个"继续"入口:manual 是流程里配置的确认点,
                   execute 是内置执行闸(必须有人点击才落库)。按钮文案区分语义。 -->
              <VButton
                v-if="(openStageObj.type === 'manual' || isExecStage(openStageObj.type)) && openStageObj.status === 'waiting'"
                variant="primary" height="30px" @click="continueStage(open, openStageObj)"
              >
                <Play :size="13" />{{ isExecStage(openStageObj.type) ? $t('rlConfirmExec') : $t('rlContinue') }}
              </VButton>
            </template>
            <span v-else class="sbhint">{{ $t('rlPickStage') }}</span>
          </div>

          <!-- 深色 IDE 风格:亮暗主题下都一样,代码与日志读起来不随页面换底色 -->
          <div class="ide">
            <div class="ide-bar">
              <div class="ide-tabs" role="tablist">
                <button type="button" role="tab" class="ide-tab" :class="{ on: codeTab === 'sql' }" :aria-selected="codeTab === 'sql'" @click="codeTab = 'sql'">
                  <FileCode :size="13" />{{ $t('rlTabSql') }}
                </button>
                <button type="button" role="tab" class="ide-tab" :class="{ on: codeTab === 'log' }" :aria-selected="codeTab === 'log'" @click="codeTab = 'log'">
                  <ScrollText :size="13" />{{ $t('rlTabLog') }}
                </button>
              </div>
              <span v-if="codeLines" class="ide-meta">{{ $t('rlLines', { n: codeLines }) }}</span>
              <button type="button" class="ide-copy" :disabled="!codeText" :title="$t('copy')" @click="copyCode">
                <Copy :size="13" />{{ $t('copy') }}
              </button>
            </div>
            <div v-if="codeText" class="ide-body">
              <div class="ide-gutter" aria-hidden="true">{{ gutterText }}</div>
              <pre v-if="codeTab === 'sql'" class="ide-code sqlsrc" v-html="sqlHtml" />
              <pre v-else class="ide-code log">{{ logText }}</pre>
            </div>
            <div v-else class="ide-empty">
              {{ codeTab === 'sql' ? $t('rlScriptRef') : openStageObj ? $t('rlNoLog') : $t('rlPickStage') }}
            </div>
          </div>

          <!-- review findings -->
          <template v-if="openReview">
            <div class="fsum">
              {{ $t('srCounts', {
                s: openReview.statements, e: openReview.errors,
                w: openReview.warnings, i: openReview.infos,
              }) }}
            </div>
            <!-- 按规则分组:一条规则一行 + 命中总数 + 前 5 条示例 -->
            <div v-for="g in openGroups" :key="g.code + g.level" class="find" :class="g.level">
              <div class="fname">{{ ruleName(g.code, g.name) }} <span class="fcount">{{ $t('srRuleHits', { n: g.count }) }}</span></div>
              <div class="fmsg">{{ localizeText(g.message, g.messageRef) }}</div>
              <div v-for="(fd, i) in g.examples" :key="i" class="fex">
                <span class="floc">{{ $t('srStmtAt', { n: fd.stmt, line: fd.line }) }}</span>
                <div v-if="fd.message !== g.message" class="fmsg">{{ localizeText(fd.message, fd.messageRef) }}</div>
                <div class="fsql">{{ fd.sql }}</div>
              </div>
              <div v-if="g.more" class="fmore">{{ $t('srMoreHits', { n: g.more }) }}</div>
            </div>
          </template>
        </section>
      </div>
      <div v-else class="detail card empty">{{ $t('rlPickRelease') }}</div>
    </div>

    <!-- create modal -->
    <div v-if="formOpen" class="overlay">
      <div class="mask" @click="formOpen = false" />
      <div class="modal">
        <div class="mhead">
          <div class="mic"><Rocket :size="18" color="var(--accent-text)" /></div>
          <div><div class="mt">{{ $t('rlNew') }}</div><div class="ms">{{ $t('rlNewSub') }}</div></div>
          <X :size="18" class="mx" @click="formOpen = false" />
        </div>
        <div class="mbody">
          <div><div class="fl">{{ $t('rlTitle') }}</div><input v-model="f.title" class="in" :placeholder="$t('rlTitlePh')" /></div>
          <div class="grid2">
            <div><div class="fl">{{ $t('mInst') }}</div><VSelect v-model="connLabel" :options="connLabels" searchable /></div>
            <div>
              <div class="fl">{{ $t('fDatabase') }}</div>
              <VSelect v-if="dbOptions.length" v-model="f.database" :options="dbOptions" />
              <input v-else v-model="f.database" class="in" />
            </div>
          </div>
          <div><div class="fl">{{ $t('rlPipeline') }}</div><VSelect v-model="pipeLabelSel" :options="pipeLabels" /></div>
          <div>
            <div class="fl">{{ $t('rlChangeType') }}</div>
            <div class="ctseg">
              <span class="ct" :class="{ on: f.changeType === '' }" @click="f.changeType = ''">{{ $t('rlCtAuto') }}</span>
              <span class="ct" :class="{ on: f.changeType === 'dml' }" @click="f.changeType = 'dml'">DML</span>
              <span class="ct" :class="{ on: f.changeType === 'ddl' }" @click="f.changeType = 'ddl'">DDL</span>
            </div>
            <div class="hint">{{ $t('rlChangeTypeHint') }}</div>
          </div>
          <div><div class="fl">{{ $t('rlContent') }}</div><textarea v-model="f.sql" class="sqlbox" spellcheck="false" :placeholder="$t('rlSqlPh')" /></div>
          <div><div class="fl">{{ $t('reason') }}</div><input v-model="f.reason" class="in" :placeholder="$t('rlReasonPh')" /></div>
          <div v-if="needMfa"><div class="fl">{{ $t('loginMfaLabel') }}</div><input v-model="f.mfaCode" class="in" :placeholder="$t('loginMfaPh')" /></div>

          <div v-if="preview" class="pv" :class="preview.passed ? 'ok' : 'bad'">
            {{ preview.passed ? $t('srPassed') : $t('srBlocked') }} ·
            {{ $t('srCounts', { s: preview.statements, e: preview.errors, w: preview.warnings, i: preview.infos }) }}
            <div v-for="g in previewGroups.slice(0, 8)" :key="g.code + g.level" class="pvline">
              [{{ g.level.toUpperCase() }}] {{ ruleName(g.code, g.name) }} · {{ $t('srRuleHits', { n: g.count }) }} — {{ localizeText(g.message, g.messageRef) }}
            </div>
          </div>
          <div class="hint">{{ $t('rlSubmitHint') }}</div>
        </div>
        <div class="mfoot">
          <VButton @click="preflight"><ShieldCheck :size="14" />{{ $t('rlPreflight') }}</VButton>
          <VButton variant="primary" :disabled="busy" @click="submit"><Rocket :size="14" />{{ $t('rlSubmit') }}</VButton>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* ------------------------------------------------------------------
   页面级主题变量。颜色全部来自全站 tokens(html[data-theme] 切亮 / 暗),
   这里只把发布页自己的卡片、选中态、代码区收成几个变量,暗色下单独调深投影
   和描边 —— 暗底上光靠阴影分不出卡片,要一条细边。
   ------------------------------------------------------------------ */
.page {
  --rl-card-bg: var(--surface-card);
  --rl-card-border: var(--border-subtle);
  --rl-card-shadow: var(--shadow-sm);
  --rl-card-shadow-hover: var(--shadow-md);
  --rl-sel-bg: color-mix(in oklch, var(--accent-subtle) 55%, var(--surface-card));
  --rl-label: var(--text-muted);
  --rl-value: var(--text-strong);
  /* 代码查看器:亮暗主题下都是深色 IDE 底 */
  --ide-bg: #0f1420;
  --ide-bar: #151b2b;
  --ide-gutter: #0b0f19;
  --ide-border: #232b3d;
  --ide-fg: #d8e0ee;
  --ide-ln: #56607a;
  --ide-muted: #8a95ad;
  --ide-tab-on: #e9eef8;
  /* 风险提示块底色里混进多少语义色;暗底上混多了警告橙会发红,读成危险 */
  --rl-alert-mix: 60%;
  /* 环境实心块上的字色:亮色主题语义色偏深配白字,暗色主题语义色偏亮配深字 */
  --rl-env-ink: #ffffff;
  flex: 1; min-height: 0; padding: var(--page-pad); max-width: var(--page-max); margin-inline: auto; width: 100%;
}
:root[data-theme='dark'] .page {
  --rl-card-shadow: 0 1px 2px rgba(0, 0, 0, 0.5), 0 6px 18px rgba(0, 0, 0, 0.28);
  --rl-card-shadow-hover: 0 8px 24px rgba(0, 0, 0, 0.55);
  --rl-card-border: color-mix(in oklch, var(--border-default) 80%, transparent);
  --rl-sel-bg: color-mix(in oklch, var(--accent-subtle) 70%, var(--surface-card));
  --ide-bg: #0a0d16;
  --ide-bar: #10141f;
  --ide-gutter: #080b12;
  --rl-alert-mix: 22%;
  --rl-env-ink: #0b0f19;
}

/* 双栏:左栏固定宽度的发布单列表,右栏详情占满剩余。窄屏叠成一列。 */
.wrap { display: grid; grid-template-columns: 372px minmax(0, 1fr); gap: 20px; align-items: start; }
@media (max-width: 1080px) { .wrap { grid-template-columns: minmax(0, 1fr); } }

/* ---------------- 左栏:发布单卡片 ---------------- */
.list { display: flex; flex-direction: column; gap: 12px; max-height: calc(100vh - 190px); overflow-y: auto;
  padding: 2px 4px 8px 2px; position: sticky; top: 0; }
@media (max-width: 1080px) { .list { position: static; max-height: 46vh; } }
/* 悬停抬起(你指着我),选中是左侧主色条 + 淡底(你在看我):两个问题,两种信号。
   flex-shrink: 0 不能省:列表是限高的纵向 flex,卡片带 overflow: hidden 时最小高度归 0,
   单子一多就被压成一条细缝(2026-11-10 用户 33 张单时只剩一行单号)。 */
.ritem { flex-shrink: 0; position: relative; overflow: hidden; border: 1px solid var(--rl-card-border); border-radius: var(--radius-lg);
  background: var(--rl-card-bg); box-shadow: var(--rl-card-shadow); padding: 14px 16px 14px 18px; cursor: pointer; outline: none;
  transition: box-shadow var(--dur-fast) var(--ease-out), transform var(--dur-fast) var(--ease-out), background var(--dur-fast) var(--ease-out); }
.ritem:hover { box-shadow: var(--rl-card-shadow-hover); transform: translateY(-1px); }
.ritem:active { transform: translateY(0); }
.ritem:focus-visible { box-shadow: 0 0 0 2px var(--accent); }
.ritem.on { background: var(--rl-sel-bg); border-color: color-mix(in oklch, var(--accent) 35%, var(--rl-card-border)); box-shadow: var(--rl-card-shadow-hover); }
.ritem.on::before { content: ""; position: absolute; left: 0; top: 0; bottom: 0; width: 4px; background: var(--accent); }
.rtop { display: flex; align-items: center; gap: 7px; min-width: 0; }
.rno { font: 600 12px var(--font-mono); font-variant-numeric: tabular-nums; color: var(--text-muted); letter-spacing: 0.02em; }
.ritem.on .rno { color: var(--accent-text); }
.rtitle { font: 600 14px/1.45 var(--font-body); color: var(--text-strong); margin-top: 7px; letter-spacing: -0.005em; overflow-wrap: anywhere; }
.rmeta { display: flex; align-items: center; gap: 7px; margin-top: 8px; font: 500 12px var(--font-body); color: var(--text-muted); flex-wrap: wrap; row-gap: 4px; min-width: 0; }
.rinst { color: var(--text-body); font-family: var(--font-mono); font-size: 11.5px; }
.rdb { font: 600 11.5px var(--font-mono); color: var(--text-strong); word-break: break-all; }
.xref { display: inline-flex; align-items: center; gap: 4px; font: 600 11px var(--font-mono); color: var(--text-body); word-break: break-all; }
.xk { font: 600 10px var(--font-body); color: var(--text-muted); }
.prj { padding: 1px 7px; border-radius: 5px; background: var(--surface-sunken); border: 1px solid var(--border-subtle); font: 600 10px var(--font-body); color: var(--text-muted);
  max-width: 120px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ctb { padding: 1px 7px; border-radius: 5px; font: 700 10px var(--font-mono); letter-spacing: 0.04em; }
.ctb.dml { background: var(--success-subtle); color: var(--success-text); }
.ctb.ddl { background: var(--warning-subtle); color: var(--warning-text); }
.ctb.big { font-size: 10.5px; padding: 2px 8px; }

/* 环境块:改的是哪个环境是这张单最要紧的一件事(2026-11-23 用户:淡色小胶囊太不显眼),
   所以用分层色**实心**填充、大写、等宽 —— prod 红、staging 橙、dev 绿、uat/gli 蓝、查不到中性。
   分层(tier)标签仍是淡底,它是补充信息,不和环境抢。 */
.tag { padding: 1px 6px; border-radius: 5px; background: var(--surface-sunken); border: 1px solid var(--border-subtle); font: 600 10px var(--font-mono); color: var(--text-muted); }
.tag.env, .envcap, .envchip { display: inline-flex; align-items: center; border-radius: 6px; border: 1px solid transparent;
  font-family: var(--font-mono); font-weight: 800; letter-spacing: 0.04em; text-transform: uppercase; white-space: nowrap; }
.tag.env { height: 20px; padding: 0 8px; font-size: 11.5px; }
.envcap { height: 26px; padding: 0 12px; font-size: 12.5px; }
.envchip { height: 30px; padding: 0 14px; font-size: 15px; }
.tag.tier { border-radius: var(--radius-full); padding: 1px 8px; }
:is(.tag.env, .envcap, .envchip).danger { background: var(--danger); color: var(--rl-env-ink); }
:is(.tag.env, .envcap, .envchip).warning { background: var(--warning); color: var(--rl-env-ink); }
:is(.tag.env, .envcap, .envchip).success { background: var(--success); color: var(--rl-env-ink); }
:is(.tag.env, .envcap, .envchip).info { background: var(--accent); color: var(--rl-env-ink); }
:is(.tag.env, .envcap, .envchip).muted { background: var(--surface-sunken); color: var(--text-strong); border-color: var(--border-default); }
.tag.tier.danger { background: var(--danger-subtle); color: var(--danger-text); }
.tag.tier.warning { background: var(--warning-subtle); color: var(--warning-text); }
.tag.tier.success { background: var(--success-subtle); color: var(--success-text); }
.tag.tier.info { background: var(--accent-subtle); color: var(--accent-text); }
.tag.tier.muted { background: var(--surface-sunken); color: var(--text-muted); border-color: var(--border-subtle); }

/* 状态胶囊。前面的圆点和颜色一起表达状态 —— 只靠颜色,分不清红绿的人就读不出来。 */
.pill { margin-left: auto; display: inline-flex; align-items: center; gap: 6px; padding: 3px 10px; border-radius: var(--radius-full);
  font: 600 11px var(--font-body); background: var(--surface-sunken); color: var(--text-muted); white-space: nowrap; }
.pill::before, .ostat::before { content: ""; width: 6px; height: 6px; border-radius: 50%; background: currentColor; flex-shrink: 0; }
:is(.pill, .ostat).success, .pill.ok { background: var(--success-subtle); color: var(--success-text); }
:is(.pill, .ostat).failed, .pill.bad { background: var(--danger-subtle); color: var(--danger-text); }
:is(.pill, .ostat).waiting, .pill.warn { background: var(--warning-subtle); color: var(--warning-text); }
:is(.pill, .ostat).running { background: var(--accent-subtle); color: var(--accent-text); }
/* 执行中是唯一一个要自己说"还在动"的状态,不靠读者盯着数字变化。 */
:is(.pill, .ostat).running::before { animation: statePulse 1.4s var(--ease-out) infinite; }
.api { padding: 1px 6px; border-radius: 5px; background: var(--accent-subtle); color: var(--accent-text); font: 700 9.5px var(--font-mono); }
.dim { color: var(--text-muted); }

/* ---------------- 右栏:卡片 ---------------- */
.detail { display: flex; flex-direction: column; gap: 16px; min-width: 0; }
.card { background: var(--rl-card-bg); border: 1px solid var(--rl-card-border); border-radius: var(--radius-lg);
  box-shadow: var(--rl-card-shadow); padding: 18px 20px; min-width: 0; }
.detail.card.empty { min-height: 320px; display: flex; align-items: center; justify-content: center; }
.ctitle { display: flex; align-items: center; gap: 7px; margin-bottom: 14px; font: 700 13px var(--font-body); color: var(--text-strong); }
.ctitle :deep(svg) { color: var(--accent-text); }

/* ① 头部 */
.dhead { display: flex; align-items: flex-start; gap: 12px; flex-wrap: wrap; }
.dic { width: 38px; height: 38px; flex-shrink: 0; border-radius: 11px; background: var(--accent-subtle); color: var(--accent-text); display: flex; align-items: center; justify-content: center; }
.grow { flex: 1; min-width: 220px; }
.dt { display: flex; align-items: center; flex-wrap: wrap; gap: 4px 10px; font: 650 16px/1.4 var(--font-display); color: var(--text-strong); }
.dno { font: 700 13px var(--font-mono); color: var(--accent-text); background: var(--accent-subtle); padding: 2px 8px; border-radius: 6px; }
.dtitle { overflow-wrap: anywhere; min-width: 0; }
.ds { font: 500 12px var(--font-body); color: var(--text-muted); margin-top: 5px; }
.dbadges { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; justify-content: flex-end; }
.dhead .pill { margin-left: 0; }
.ostat { display: inline-flex; align-items: center; gap: 6px; height: 24px; padding: 0 11px; border-radius: var(--radius-full);
  font: 600 11.5px var(--font-body); background: var(--surface-sunken); color: var(--text-muted); white-space: nowrap; }

/* 元数据:4 列栅格,标签 12px 浅灰在上,值 14px 加粗在下;实例 / 库用等宽字体 */
.kinfo { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); grid-auto-flow: row dense; gap: 14px 20px;
  margin-top: 16px; padding-top: 16px; border-top: 1px solid var(--border-subtle); }
@media (max-width: 1360px) { .kinfo { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
.kv { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
/* 上游单号、起止时间是长串,占两格,不在中间折断 */
.kv.wide { grid-column: span 2; }
.kv .k { font: 500 12px var(--font-body); color: var(--rl-label); }
.kv .v { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; font: 500 14px var(--font-body); color: var(--text-body); overflow-wrap: anywhere; }
.kv .v.nowrap { flex-wrap: nowrap; align-items: flex-start; }
.kv .v.mono { font-family: var(--font-mono); font-size: 13px; }
.kv .v.strong { font-weight: 600; color: var(--rl-value); }
.kv .v .dim { font-weight: 500; }
.refv { min-width: 0; overflow-wrap: anywhere; }
.nw { white-space: nowrap; }
.kcopy { display: inline-flex; padding: 3px; border: none; border-radius: 5px; background: transparent; color: var(--text-muted); cursor: pointer; }
.kcopy:hover { background: var(--surface-sunken); color: var(--text-body); }
.kinfo .reason { grid-column: 1 / -1; padding: 9px 12px; border-radius: 9px; background: var(--surface-sunken); font: 500 12.5px var(--font-body); color: var(--text-body); overflow-wrap: anywhere; }

/* ② 流水线步骤条 */
.pipe { display: flex; align-items: flex-start; padding: 6px 4px 2px; overflow-x: auto; }
.node { display: flex; flex-direction: column; align-items: center; gap: 5px; min-width: 108px; padding: 8px 8px 10px; border-radius: 12px;
  cursor: pointer; outline: none; transition: var(--transition-colors), box-shadow var(--dur-fast) var(--ease-out); }
.node:hover { background: var(--surface-sunken); }
.node:focus-visible { box-shadow: 0 0 0 2px var(--accent); }
.node.sel { background: var(--rl-sel-bg); box-shadow: inset 0 0 0 1px color-mix(in oklch, var(--accent) 40%, transparent); }
.dot { width: 36px; height: 36px; border-radius: 50%; display: flex; align-items: center; justify-content: center; }
/* 还没轮到的节点:虚线圈 + 灰,和"已跳过 / 已终止"的实心灰区分开 */
.node.pending .dot { box-shadow: inset 0 0 0 1.5px var(--border-default); }
.node.success .dot { box-shadow: inset 0 0 0 1.5px color-mix(in oklch, var(--success-text) 45%, transparent); }
.node.failed .dot { box-shadow: inset 0 0 0 1.5px color-mix(in oklch, var(--danger-text) 45%, transparent); }
.nname { display: flex; align-items: center; gap: 4px; margin-top: 2px; font: 600 12.5px var(--font-body); color: var(--text-strong); white-space: nowrap; }
.nst { font: 600 11px var(--font-body); white-space: nowrap; }
.ndur { font: 500 10.5px var(--font-mono); color: var(--text-muted); font-variant-numeric: tabular-nums; }
/* 连线报告真正流过的东西:已完成的后面是实线,正在跑的那一段在流动,还没到的是平的 */
.conn { flex: 1; min-width: 24px; height: 3px; margin-top: 26px; border-radius: var(--radius-full); background: var(--border-subtle); }
.conn.done { background: var(--success); }
.conn.live { background: linear-gradient(90deg, var(--success) 0%, var(--accent) 50%, var(--border-subtle) 50%);
  background-size: 200% 100%; animation: connFlow 1.6s linear infinite; }
@keyframes connFlow { from { background-position: 100% 0; } to { background-position: 0 0; } }
.spin { animation: spin 1s linear infinite; }
/* 正在跑的节点带光晕:转圈说"这个图标在忙",光晕说"流水线停在这儿" */
.dot.halo { animation: nodeHalo 1.8s var(--ease-out) infinite; }
@keyframes nodeHalo {
  0% { box-shadow: 0 0 0 0 color-mix(in oklch, var(--accent) 50%, transparent); }
  70% { box-shadow: 0 0 0 12px transparent; }
  100% { box-shadow: 0 0 0 0 transparent; }
}
@keyframes spin { to { transform: rotate(360deg); } }
.errbar { display: flex; align-items: center; gap: 8px; margin-top: 14px; padding: 10px 13px; border-radius: 10px;
  background: var(--danger-subtle); color: var(--danger-text); border-left: 3px solid var(--danger-text); font: 500 12.5px var(--font-body); overflow-wrap: anywhere; }

/* ③ 风险提示:警示色块,左侧色条 + 图标 */
.pbox { border-left-width: 4px; }
.pbox.lv-warn { border-left-color: var(--warning-text); background: color-mix(in oklch, var(--warning-subtle) var(--rl-alert-mix), var(--rl-card-bg)); }
.pbox.lv-err { border-left-color: var(--danger-text); background: color-mix(in oklch, var(--danger-subtle) var(--rl-alert-mix), var(--rl-card-bg)); }
.pbox.lv-ok { border-left-color: var(--success-text); }
.pbhead { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.pbic { display: inline-flex; width: 26px; height: 26px; border-radius: 8px; align-items: center; justify-content: center; }
.lv-warn .pbic { background: var(--warning-subtle); color: var(--warning-text); }
.lv-err .pbic { background: var(--danger-subtle); color: var(--danger-text); }
.lv-ok .pbic { background: var(--success-subtle); color: var(--success-text); }
.pbt { font: 700 13px var(--font-body); color: var(--text-strong); }
.pcnt { font: 700 11px var(--font-mono); padding: 2px 8px; border-radius: var(--radius-full); }
.pcnt.err { background: var(--danger-subtle); color: var(--danger-text); }
.pcnt.warn { background: var(--warning-subtle); color: var(--warning-text); }
/* 计数为 0 时压暗但**不隐藏**:一个不显示的 0 和"没统计过"在页面上长得一样。 */
.pcnt.zero { background: transparent; color: var(--text-muted); box-shadow: inset 0 0 0 1px var(--border-subtle); }
.pdim, .psrc { font: 500 12px var(--font-body); color: var(--text-muted); }
.pdim { margin-left: auto; }
.psrc { margin-top: 8px; }
.pnone { margin-top: 8px; font: 500 12.5px var(--font-body); color: var(--text-body); }
.pitem { display: flex; align-items: baseline; gap: 9px; margin-top: 8px; padding: 8px 11px; border-radius: 9px; background: var(--rl-card-bg);
  box-shadow: inset 0 0 0 1px var(--border-subtle); font: 500 12.5px var(--font-body); }
.pitem.err .pmsg { color: var(--danger-text); }
.pitem.warn .pmsg { color: var(--warning-text); }
.pitem.info .pmsg { color: var(--text-body); }
.pstmt { flex-shrink: 0; font: 700 11px var(--font-mono); color: var(--text-muted); }
.pobj { flex-shrink: 0; font: 600 11.5px var(--font-mono); color: var(--text-strong); }
.pmsg { word-break: break-word; font-weight: 600; }
.pstmt { max-width: 46%; white-space: normal; overflow-wrap: anywhere; }
.pgcnt { flex-shrink: 0; padding: 0 7px; border-radius: var(--radius-full); background: var(--surface-sunken); font: 600 10.5px var(--font-mono); color: var(--text-muted); }
.pmore { margin-top: 8px; padding: 4px 10px; border: none; border-radius: 7px; background: transparent; color: var(--accent-text); font: 600 12px var(--font-body); cursor: pointer; }
.pmore:hover { background: var(--accent-subtle); }
.psrctag { margin-left: auto; flex-shrink: 0; font: 600 10.5px var(--font-mono); color: var(--text-muted); }

/* ④ 阶段详情 + 代码查看器 */
.sbhead { display: flex; align-items: center; gap: 9px; margin-bottom: 12px; min-height: 30px; flex-wrap: wrap; }
.sbt { font: 700 13.5px var(--font-body); color: var(--text-strong); }
.sbhead .pill { margin-left: 0; }
.sbhint { font: 500 12.5px var(--font-body); color: var(--text-muted); }
.apno { font: 600 11px var(--font-mono); color: var(--accent-text); }
.sbhead :deep(.vbtn) { margin-left: auto; }

.ide { border-radius: 12px; overflow: hidden; background: var(--ide-bg); border: 1px solid var(--ide-border);
  box-shadow: 0 1px 2px rgba(0, 0, 0, 0.18); color-scheme: dark; }
.ide-bar { display: flex; align-items: center; gap: 10px; padding: 0 8px 0 4px; height: 40px; background: var(--ide-bar); border-bottom: 1px solid var(--ide-border); }
.ide-tabs { display: flex; align-self: stretch; }
.ide-tab { display: inline-flex; align-items: center; gap: 6px; padding: 0 14px; border: none; background: transparent; cursor: pointer;
  font: 600 12px var(--font-body); color: var(--ide-muted); border-bottom: 2px solid transparent; margin-bottom: -1px; transition: color var(--dur-fast) var(--ease-out); }
.ide-tab:hover { color: var(--ide-tab-on); }
.ide-tab.on { color: var(--ide-tab-on); border-bottom-color: var(--glow-accent); background: var(--ide-bg); }
.ide-tab:focus-visible { outline: 2px solid var(--glow-accent); outline-offset: -2px; }
.ide-meta { margin-left: auto; font: 500 11px var(--font-mono); color: var(--ide-ln); }
.ide-copy { display: inline-flex; align-items: center; gap: 5px; height: 26px; padding: 0 10px; border-radius: 7px; border: 1px solid var(--ide-border);
  margin-left: auto; background: transparent; color: var(--ide-muted); font: 600 11.5px var(--font-body); cursor: pointer; }
.ide-meta + .ide-copy { margin-left: 0; }
.ide-copy:hover:not(:disabled) { color: var(--ide-tab-on); border-color: var(--ide-muted); }
.ide-copy:disabled { opacity: 0.45; cursor: default; }
/* 行号与代码在同一个滚动容器里,纵向一起滚;代码不折行,长行横向滚,行号永远对得上 */
.ide-body { display: flex; align-items: flex-start; max-height: 420px; overflow: auto; font: 500 12.5px/1.75 var(--font-mono); }
.ide-gutter { position: sticky; left: 0; flex-shrink: 0; min-height: 100%; padding: 12px 12px 12px 14px; text-align: right; white-space: pre;
  color: var(--ide-ln); background: var(--ide-gutter); border-right: 1px solid var(--ide-border); user-select: none; font-variant-numeric: tabular-nums; }
.ide-code { margin: 0; flex: 1; min-width: 0; padding: 12px 16px; background: transparent; border: none; color: var(--ide-fg);
  font: inherit; white-space: pre; tab-size: 4; }
.ide-empty { padding: 28px 16px; text-align: center; font: 500 12.5px var(--font-body); color: var(--ide-muted); }
.sqlsrc :deep(.sqlh-kw) { color: #82aaff; font-weight: 600; }
.sqlsrc :deep(.sqlh-str) { color: #c3e88d; }
.sqlsrc :deep(.sqlh-num) { color: #f78c6c; }
.sqlsrc :deep(.sqlh-cmt) { color: #7a87a3; }
.sqlsrc :deep(.sqlh-meta) { color: #c792ea; }

.fsum { margin-top: 14px; font: 600 12px var(--font-mono); color: var(--text-muted); }
.find { margin-top: 8px; padding: 9px 12px; border-radius: 9px; background: var(--surface-sunken); border-left: 3px solid var(--border-strong); }
.find.error { border-left-color: var(--danger-text); }
.find.warn { border-left-color: var(--warning-text); }
.fname { font: 600 12.5px var(--font-body); color: var(--text-strong); }
.floc { font: 500 10.5px var(--font-mono); color: var(--text-muted); margin-left: 6px; }
.fmsg { font: 500 12px var(--font-body); color: var(--text-body); margin-top: 3px; }
.fsql { font: 500 11px var(--font-mono); color: var(--text-muted); margin-top: 4px; word-break: break-all; }
.fcount { font: 600 11px var(--font-mono); color: var(--text-muted); margin-left: 6px; }
.fex { margin-top: 6px; }
.fex .floc { margin-left: 0; }
.fmore { margin-top: 6px; font: 500 11px var(--font-body); color: var(--text-muted); }

.hint { margin-top: 12px; font: 500 11.5px var(--font-body); color: var(--text-faint); }
.empty { padding: 22px; text-align: center; font: 500 12.5px var(--font-body); color: var(--text-muted); }

/* ---------------- 页头工具条 ---------------- */
.pfilter { display: flex; align-items: center; }
/* 工具条多了搜索框:放不下时整排换到说明下面,而不是把说明挤成一字一行 */
.v-head { flex-wrap: wrap; row-gap: 10px; }
.v-head-l { min-width: 240px; }
.v-head-acts { flex-wrap: wrap; justify-content: flex-end; max-width: 100%; }
.rsearch { position: relative; display: flex; align-items: center; }
.rsicon { position: absolute; left: 9px; color: var(--text-faint); pointer-events: none; }
.rsinput { box-sizing: border-box; width: 250px; height: 30px; padding: 0 26px 0 28px; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-card); color: var(--text-body); font: 500 11.5px var(--font-body); outline: none; }
.rsinput::placeholder { color: var(--text-faint); }
.rsinput:focus { border-color: var(--accent-text); }
.rsclear { position: absolute; right: 5px; display: flex; padding: 3px; border: none; border-radius: 5px; background: transparent; color: var(--text-faint); cursor: pointer; }
.rsclear:hover { background: var(--surface-sunken); color: var(--text-body); }
.psel { height: 30px; max-width: 190px; padding: 0 8px; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-card); color: var(--text-body); font: 600 11.5px var(--font-body); cursor: pointer; }
.psel:hover { border-color: var(--accent-text); color: var(--accent-text); }
.seg { display: flex; border: 1px solid var(--border-default); border-radius: 10px; overflow: hidden; }
.seg.small .si { height: 32px; line-height: 32px; padding: 0 12px; }
.si { text-align: center; font: 600 11.5px var(--font-mono); color: var(--text-muted); border-left: 1px solid var(--border-subtle); cursor: pointer; }
.si:first-child { border-left: none; }
.si.active { background: var(--accent-subtle); color: var(--accent-text); }

/* ---------------- 新建发布弹窗(未改) ---------------- */
.ctseg { display: inline-flex; gap: 6px; }
.ct { padding: 5px 14px; border-radius: 8px; border: 1px solid var(--border-default); background: var(--surface-sunken); font: 600 11.5px var(--font-mono); color: var(--text-muted); cursor: pointer; user-select: none; }
.ct.on { background: var(--accent-subtle); border-color: var(--accent-text); color: var(--accent-text); }
.fl { font: 500 11px var(--font-body); color: var(--text-faint); margin: 10px 0 6px; }
.in { width: 100%; box-sizing: border-box; height: 38px; border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-sunken); padding: 0 12px; font: 400 13px var(--font-body); color: var(--text-body); outline: none; }
.sqlbox { width: 100%; box-sizing: border-box; min-height: 150px; border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-sunken); padding: 10px 12px; font: 500 12px var(--font-mono); color: var(--text-body); outline: none; resize: vertical; }
.overlay { position: fixed; inset: 0; z-index: 50; }
.mask { position: absolute; inset: 0; background: rgba(4, 6, 12, 0.7); backdrop-filter: blur(3px); }
.modal { position: absolute; left: 50%; top: 50%; transform: translate(-50%, -50%); width: 680px; max-width: 94vw; max-height: 90vh; overflow-y: auto; background: var(--surface-card); border: 1px solid var(--border-default); border-radius: 18px; box-shadow: var(--shadow-xl); }
.mhead { display: flex; align-items: center; gap: 12px; padding: 16px 22px; border-bottom: 1px solid var(--border-subtle); }
.mic { width: 34px; height: 34px; border-radius: 10px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; }
.mt { font: 700 15px var(--font-display); color: var(--text-strong); }
.ms { font: 500 11.5px var(--font-body); color: var(--text-muted); }
.mx { color: var(--text-muted); margin-left: auto; cursor: pointer; }
.mbody { padding: 4px 22px 18px; }
.grid2 { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
.pv { margin-top: 12px; padding: 10px 12px; border-radius: 10px; font: 600 11.5px var(--font-mono); }
.pv.ok { background: var(--success-subtle); color: var(--success-text); }
.pv.bad { background: var(--danger-subtle); color: var(--danger-text); }
.pvline { font: 500 11px var(--font-body); margin-top: 4px; }
.mfoot { display: flex; justify-content: flex-end; gap: 10px; padding: 12px 22px; border-top: 1px solid var(--border-subtle); background: var(--surface-raised); }
</style>
