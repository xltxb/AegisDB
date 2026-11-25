<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { EyeOff, Shield, GitPullRequestArrow, Lock, Bell, Palette, ImageUp, KeyRound, Webhook, DatabaseZap, SearchCode, FolderKanban } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSwitch from '@/components/common/VSwitch.vue'
import VSelect from '@/components/common/VSelect.vue'
import MfaModal from '@/components/modals/MfaModal.vue'
import IpAllowlistModal from '@/components/modals/IpAllowlistModal.vue'
import WebhookPanel from '@/components/settings/WebhookPanel.vue'
import ApiClientsPanel from '@/components/settings/ApiClientsPanel.vue'
import SensitiveColumnsPanel from '@/components/settings/SensitiveColumnsPanel.vue'
import ProjectsPanel from '@/components/settings/ProjectsPanel.vue'
import BrandPanel from '@/components/settings/BrandPanel.vue'
import BrandMark from '@/components/BrandMark.vue'
import { brand, brandText } from '@/lib/brand'
import { useI18n } from 'vue-i18n'
import { APPROVAL_TIMEOUT_KEYS, SESSION_TTL_KEYS, labelOf, keyOf, keyForLabel } from '@/lib/settingOptions'
import { useRouter, useRoute } from 'vue-router'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import { confirmAction } from '@/lib/confirm'
import { useUnsavedFlag } from '@/lib/unsavedChanges'
import api from '@/api'
import type { Member } from '@/types'

const router = useRouter()
const route = useRoute()
// The approval chain is the "DBA 负责人" role membership — managed on the
// Permissions page, not here (settings only displays it).
function goPerms() { router.push('/admin/permissions') }

const { t } = useI18n()
const ui = useUIStore()
const auth = useAuthStore()

// Tabbed sections — only the active panel shows, so the page stays compact. 页签按主题分组
// 竖排在左侧:十个页签横排一行放不下,折到第二行的那一个像是漏掉的。
// own = 这一页自己保存(独立接口)或即时生效,底部的「保存设置」对它不起作用,就不显示,
// 免得人以为按一下能保存那一页的改动。
const tabGroups = [
  { label: 'setGrpCore', tabs: [
    { id: 'gateway', icon: Shield, label: 'setGw' },
    { id: 'approval', icon: GitPullRequestArrow, label: 'setAppr' },
    { id: 'meta', icon: DatabaseZap, label: 'setMeta' },
    { id: 'brand', icon: ImageUp, label: 'setBrand', own: true },
  ] },
  { label: 'setGrpSec', tabs: [
    { id: 'security', icon: Lock, label: 'setSec' },
    { id: 'sensitive', icon: EyeOff, label: 'sensTitle', own: true },
  ] },
  { label: 'setGrpBiz', tabs: [
    { id: 'bizquery', icon: SearchCode, label: 'setBiz' },
    { id: 'projects', icon: FolderKanban, label: 'prTitle', own: true },
  ] },
  { label: 'setGrpInteg', tabs: [
    { id: 'notify', icon: Bell, label: 'setNotify' },
    { id: 'webhook', icon: Webhook, label: 'setWebhookTab', own: true },
    { id: 'openapi', icon: KeyRound, label: 'setOpenApi', own: true },
  ] },
  { label: 'setGrpPersonal', tabs: [
    { id: 'appearance', icon: Palette, label: 'setAppearance', own: true },
  ] },
]
const allTabs = tabGroups.flatMap((g) => g.tabs)
// 当前页签记在地址里(?tab=),刷新、分享链接都回到同一页。
const initialTab = String(route.query.tab || '')
const activeTab = ref(allTabs.some((tb) => tb.id === initialTab) ? initialTab : 'gateway')
watch(activeTab, (id) => { if (route.query.tab !== id) router.replace({ query: { ...route.query, tab: id } }) })
// 已在本页时改地址(浏览器后退、点站内链接)不会重建组件,要跟着地址切页签。
watch(() => route.query.tab, (q) => { if (typeof q === 'string' && allTabs.some((tb) => tb.id === q)) activeTab.value = q })
const showSave = computed(() => !allTabs.find((tb) => tb.id === activeTab.value)?.own)

// per-user MFA enrollment (distinct from the requireMFA policy toggle)
const mfaModalOpen = ref(false)
const mfaBound = computed(() => auth.me?.mfaEnabled ?? false)
async function onMfaChanged() {
  try { await auth.fetchMe() } catch { /* ignore */ }
}

// IP allowlist
const ipAllowEnabled = ref(false)
// SRD 授权登录(公司 SSO,只做认证)。存 tbl_setting 的 sso.*,即改即生效。
const ssoEnabled = ref(false)
const ssoBaseUrl = ref('')
const ssoCookie = ref('CS-Token')
// 部署配置里拉了 SSO 总闸:这里存什么都不生效,所有人走口令登录。
const ssoForcedOff = ref(false)
const ipModalOpen = ref(false)
// 管理后台 IP 白名单(工单 08):只作用于管理接口;列表为空不限制,本机始终放行。
const adminIpEnabled = ref(false)
const adminIpList = ref('')
const adminIpModalOpen = ref(false)
// 部署配置拉了总闸(AEGISDB_ADMIN_IP_ALLOWLIST_DISABLED):这里存什么都不生效
const adminIpForcedOff = ref(false)
// 两个白名单都是**先存、存成了再改界面**。从前反过来:先把徽标改成「已启用」、关掉弹窗,再去存,
// 存失败也不报 —— 页面说白名单开着,其实一条都没生效(2026-10 审查)。失败时弹窗留着,改动不丢。
async function onAdminIpSave(payload: { enabled: boolean; list: string }) {
  try {
    await api.saveSettings({ 'security.adminIpAllowEnabled': payload.enabled, 'security.adminIpAllowlist': payload.list })
  } catch (e) {
    ui.notifyError(e, t('saveFailed'))
    return
  }
  adminIpEnabled.value = payload.enabled
  adminIpList.value = payload.list
  adminIpModalOpen.value = false
  ui.notify(t('settingsSaved'), 'success')
}
async function onIpSave(payload: { enabled: boolean; list: string }) {
  try {
    await api.saveSettings({ 'security.ipAllowEnabled': payload.enabled, 'security.ipAllowlist': payload.list })
  } catch (e) {
    ui.notifyError(e, t('saveFailed'))
    return
  }
  ipAllowEnabled.value = payload.enabled
  ipAllow.value = payload.list
  ipModalOpen.value = false
  ui.notify(t('settingsSaved'), 'success')
}

const policy = ref('strict')
const apprTimeout = ref('')
const escalate = ref(true)
const allowSelf = ref(false)
const ttl = ref('')
const idle = ref(true)
const idleMinutes = ref(15)
// 管理后台空闲多少分钟退回用户后台(工单 09);后端再按会话空闲锁定时长封顶
const adminIdleMinutes = ref(5)
const mfa = ref(true)
// Force every account to enrol before it may touch PROD. Off by default: turning
// it on blocks production work for anyone not yet enrolled, so it is a rollout
// decision rather than a default.
const mfaMandatory = ref(false)
// How long one PROD step-up vouches for a session on that instance.
const mfaGrace = ref(30)
const lark = ref(true)
const email = ref(false)
const push = ref(true)
// Lark (飞书) channel config — the approval card is pushed to this bot webhook.
const larkWebhook = ref('')
const larkSecret = ref('')
const hasLarkSecret = ref(false)
const consoleURL = ref('')
const larkTest = ref<{ state: 'idle' | 'sending' | 'ok' | 'err'; msg: string }>({ state: 'idle', msg: '' })
async function testLark() {
  if (larkTest.value.state === 'sending') return
  larkTest.value = { state: 'sending', msg: '' }
  try {
    await api.saveSettings({ 'notify.larkWebhook': larkWebhook.value.trim(), 'notify.larkSecret': larkSecret.value.trim(), 'notify.consoleURL': consoleURL.value.trim() })
    const r = await api.testLark()
    larkTest.value = { state: r.ok ? 'ok' : 'err', msg: r.message }
  } catch { larkTest.value = { state: 'err', msg: t('larkTestFail') } }
}
// 清除一个已配置的密钥(工单 02 第 8 条):后端约定 null = 清除,"" = 不改。即点即存,不等底部的保存 ——
// 清除是一个明确的动作,与"这次没填"是两回事。
async function clearSecret(key: 'notify.larkSecret' | 'approval.external.token' | 'approval.external.callbackSecret') {
  if (!(await confirmAction(t('secretClearConfirm')))) return
  try {
    await api.saveSettings({ [key]: null })
    if (key === 'notify.larkSecret') { hasLarkSecret.value = false; larkSecret.value = '' }
    else if (key === 'approval.external.token') { hasExtToken.value = false; extToken.value = '' }
    else { hasExtCallbackSecret.value = false; extCallbackSecret.value = '' }
    ui.notify(t('secretCleared'), 'success')
  } catch (e) {
    ui.notifyError(e, t('saveFailed'))
  }
}
// External 飞书审批(审批魔方)对接配置(ADR-0003)
const extEnabled = ref(false)
const extRelease = ref(false)
const extBaseURL = ref('')
const extToken = ref('')
const hasExtToken = ref(false)
const extAiGroup = ref('')
const extCallbackBaseURL = ref('')
const extCallbackSecret = ref('')
const hasExtCallbackSecret = ref(false)
const extAllowIPs = ref('')
// The callback URL to register with the vendor. The vendor authenticates by
// sending the callback secret as `Authorization: Bearer <secret>` (configured on
// their side); the URL itself carries no secret.
const extCallbackURL = computed(() => {
  const b = extCallbackBaseURL.value.trim().replace(/\/+$/, '')
  return b ? `${b}/api/v1/approvals/lark/callback` : ''
})
const saved = ref(false)
const approvers = ref<Member[]>([])
const ipAllow = ref('')
const execTimeout = ref(30) // command execution timeout, seconds
// Web 命令行每批显示多少行;超出的"加载更多"(后端暂存,一条查询最多读 5 万行)
const resultPageSize = ref(1000)
const approvalExecTimeout = ref(3600) // 审批单执行每条语句的时限(秒),与终端的分开
// 在线变更(生产大表加 / 删索引):多大的表改走在线变更、拷贝的块大小、库端繁忙时暂停的阈值
const oscMinRows = ref(1000000)
const oscMinGB = ref(1) // 界面按 GB 填,存的是字节
const oscMaxThreads = ref(50)
const oscMaxLag = ref(10) // 从库复制延迟阈值(秒),0 = 不看
const oscChunk = ref(1000)
const scriptPath = ref('')
// AI 巡检报告保存路径(.scratch/inspection-report);留空则不接收报告
const inspectionPath = ref('')
const exportPath = ref('')
const exportMaxRows = ref(5000000) // per-job export caps; 0 = unlimited
const exportMaxBytes = ref(2000000000)
const exportTimeout = ref(1800) // per-job export execution budget, seconds
const exportRetention = ref(3)  // 归档在服务器上保留几天,0 = 永久保留
// 元数据同步。默认**关着** —— 打开它意味着这台网关会周期性地登录每一台实例,
// 那必须是一次明确的决定,不能因为升级了一个版本就自己开始跑。
const metaEnabled = ref(false)
const metaIntervalHrs = ref(24)
// 业务方数据查询的全局值。保存时服务端整体校验(默认不超过上限、上限不超过硬边界),
// 不合法的一项都不存 —— 这里只做输入框层面的整数处理,范围以服务端为准。
// 申请数据查询权限的默认页面,与后端 service.DefaultBizApplyURL 一致(没保存过这一项时显示它)
const BIZ_APPLY_URL = 'https://srd.bssrvc66.com/service-center/query/db-gateway-grant'
const biz = ref({ defaultMaxRows: 1000, maxRowsCap: 10000, defaultTimeoutSec: 30, timeoutCapSec: 300, maxGrantDays: 90,
  maxConcurrentPerUser: 2, maxConcurrentPerSource: 5, maxDailyPerUser: 0, exportEnabled: false, applyUrl: BIZ_APPLY_URL })
const bizInt = (v: unknown, def: number) => { const n = Math.round(Number(v)); return Number.isFinite(n) ? n : def }
const metaConcurrency = ref(2)
// 连通性巡检。也默认**关着** —— 它同样会周期性地登录每一台实例,只是比抓元数据轻。
const connCheckEnabled = ref(false)
const connCheckInterval = ref(10)
const connCheckConcurrency = ref(4)

const policyOpts = ['strict', 'approve-1', 'audit-only']

// 数值设置的读与存都过它:默认值和上下界各只写一次。以前每个数值设置把默认值写在
// 三处(ref 初始化、读取、保存)、上下界写在两处(保存、模板的 min/max),漏改一处就
// 悄悄不一致 —— 而服务端还会再夹一遍,这里夹是为了让人当场看见被改成了什么。
function clampInt(v: unknown, def: number, min: number, max: number) {
  return Math.max(min, Math.min(max, Math.round(Number(v) || def)))
}

function parse<T>(raw: string | undefined, def: T): T {
  if (raw === undefined) return def
  try { return JSON.parse(raw) as T } catch { return def }
}

onMounted(async () => {
  apprTimeout.value = 'auto-escalate'
  ttl.value = '8h'
  try {
    const s = await api.settings()
    const g = s.settings || {}
    policy.value = parse(g['gateway.defaultPolicy'], 'strict')
    escalate.value = parse(g['approval.escalate'], true)
    allowSelf.value = parse(g['approval.allowSelfApprove'], false)
    mfa.value = parse(g['security.requireMFA'], true)
    mfaMandatory.value = parse(g['security.mfaMandatory'], false)
    mfaGrace.value = Number(parse(g['security.mfaGraceMinutes'], 30)) || 30
    idle.value = parse(g['security.idleLock'], true)
    idleMinutes.value = Number(parse(g['security.idleMinutes'], 15)) || 15
    adminIdleMinutes.value = Number(parse(g['security.adminIdleMinutes'], 5)) || 5
    lark.value = parse(g['notify.lark'], true)
    email.value = parse(g['notify.email'], false)
    push.value = parse(g['notify.push'], true)
    larkWebhook.value = parse<string>(g['notify.larkWebhook'], '')
    // The secret is never returned; keep the field blank (blank on save == keep
    // unchanged) and just remember whether one is on file. (R3)
    larkSecret.value = ''
    hasLarkSecret.value = !!s.secretsSet?.['notify.larkSecret']
    consoleURL.value = parse<string>(g['notify.consoleURL'], '')
    apprTimeout.value = keyOf(parse<string>(g['approval.onTimeout'], 'auto-escalate'), APPROVAL_TIMEOUT_KEYS, 'auto-escalate')
    ttl.value = keyOf(parse<string>(g['security.sessionTTL'], '8h'), SESSION_TTL_KEYS, '8h')
    ipAllow.value = parse<string>(g['security.ipAllowlist'], '')
    ipAllowEnabled.value = parse<boolean>(g['security.ipAllowEnabled'], false)
    adminIpEnabled.value = parse<boolean>(g['security.adminIpAllowEnabled'], false)
    adminIpList.value = parse<string>(g['security.adminIpAllowlist'], '')
    adminIpForcedOff.value = !!(s as any).adminIpAllowlistForcedOff
    ssoEnabled.value = parse<boolean>(g['sso.enabled'], false)
    ssoBaseUrl.value = parse<string>(g['sso.baseUrl'], '')
    ssoCookie.value = parse<string>(g['sso.cookieName'], 'CS-Token') || 'CS-Token'
    api.ssoConfig().then((c) => { ssoForcedOff.value = !!c.forcedOff }).catch(() => { /* 读不到就不提示 */ })
    scriptPath.value = parse<string>(g['script.savePath'], '')
    inspectionPath.value = parse<string>(g['inspection.savePath'], '')
    exportPath.value = parse<string>(g['export.savePath'], '')
    exportMaxRows.value = Number(parse(g['export.maxRows'], 5000000))
    exportMaxBytes.value = Number(parse(g['export.maxBytes'], 2000000000))
    exportTimeout.value = Number(parse(g['export.execTimeout'], 1800)) || 1800
    // `|| 3` 会把用户设的 0(永久保留)悄悄改回 3,所以这里不能用它兜底。
    exportRetention.value = Math.max(0, Number(parse(g['export.retentionDays'], 3)))
    execTimeout.value = clampInt(parse(g['gateway.execTimeout'], 30), 30, 1, 3600)
    resultPageSize.value = clampInt(parse(g['terminal.resultPageSize'], 1000), 1000, 100, 50000)
    approvalExecTimeout.value = clampInt(parse(g['gateway.approvalExecTimeout'], 3600), 3600, 1, 86400)
    oscMinRows.value = Math.max(1, Number(parse(g['osc.minRows'], 1000000)) || 1000000)
    oscMinGB.value = Math.max(0.01, Number(parse(g['osc.minBytes'], 1 << 30)) / (1 << 30) || 1)
    oscMaxThreads.value = clampInt(parse(g['osc.maxThreadsRunning'], 50), 50, 0, 10000)
    oscMaxLag.value = clampInt(parse(g['osc.maxReplicaLagSec'], 10), 10, 0, 86400)
    oscChunk.value = clampInt(parse(g['osc.chunkSize'], 1000), 1000, 1, 100000)
    metaEnabled.value = parse(g['meta.sync.enabled'], false)
    metaIntervalHrs.value = clampInt(parse(g['meta.sync.intervalHours'], 24), 24, 1, 720)
    biz.value = {
      defaultMaxRows: bizInt(parse(g['bizQuery.defaultMaxRows'], 1000), 1000), maxRowsCap: bizInt(parse(g['bizQuery.maxRowsCap'], 10000), 10000),
      defaultTimeoutSec: bizInt(parse(g['bizQuery.defaultTimeoutSec'], 30), 30), timeoutCapSec: bizInt(parse(g['bizQuery.timeoutCapSec'], 300), 300),
      maxGrantDays: bizInt(parse(g['bizQuery.maxGrantDays'], 90), 90),
      maxConcurrentPerUser: bizInt(parse(g['bizQuery.maxConcurrentPerUser'], 2), 2), maxConcurrentPerSource: bizInt(parse(g['bizQuery.maxConcurrentPerSource'], 5), 5),
      maxDailyPerUser: bizInt(parse(g['bizQuery.maxDailyPerUser'], 0), 0),
      exportEnabled: parse(g['bizQuery.exportEnabled'], false),
      applyUrl: parse<string>(g['bizQuery.applyUrl'], BIZ_APPLY_URL),
    }
    metaConcurrency.value = clampInt(parse(g['meta.sync.concurrency'], 2), 2, 1, 8)
    connCheckEnabled.value = parse(g['conn.check.enabled'], false)
    connCheckInterval.value = clampInt(parse(g['conn.check.intervalMinutes'], 10), 10, 1, 1440)
    connCheckConcurrency.value = clampInt(parse(g['conn.check.concurrency'], 4), 4, 1, 16)
    // External approval — secrets (token/callbackSecret) are never returned; keep
    // the fields blank (blank on save == keep unchanged) and just note presence.
    extEnabled.value = parse(g['approval.external.enabled'], false)
    extRelease.value = parse(g['approval.external.releaseEnabled'], false)
    extBaseURL.value = parse<string>(g['approval.external.baseURL'], '')
    extAiGroup.value = parse<string>(g['approval.external.aiGroup'], '')
    extCallbackBaseURL.value = parse<string>(g['approval.external.callbackBaseURL'], '')
    extAllowIPs.value = parse<string>(g['approval.external.callbackAllowIPs'], '')
    extToken.value = ''
    hasExtToken.value = !!s.secretsSet?.['approval.external.token']
    extCallbackSecret.value = ''
    hasExtCallbackSecret.value = !!s.secretsSet?.['approval.external.callbackSecret']
  } catch { /* ignore */ }
  takeBaseline()
  try { approvers.value = (await api.approvalChain()).chain } catch { /* ignore */ }
})

// 底部「保存设置」要存的那一整份。保存与"改没改过"(管理后台空闲锁屏,2026-10-26 回归排查 10)共用它。
function settingsPayload() {
  // The model holds the stored key, so saving needs no reverse lookup against
  // the current language (EF11).
  const onTimeout = keyOf(apprTimeout.value, APPROVAL_TIMEOUT_KEYS, 'auto-escalate')
  const ttlKey = keyOf(ttl.value, SESSION_TTL_KEYS, '8h')
  return {
    'gateway.defaultPolicy': policy.value,
    'approval.onTimeout': onTimeout,
    'approval.escalate': escalate.value,
    'approval.allowSelfApprove': allowSelf.value,
    'gateway.execTimeout': clampInt(execTimeout.value, 30, 1, 3600),
    'terminal.resultPageSize': clampInt(resultPageSize.value, 1000, 100, 50000),
    'gateway.approvalExecTimeout': clampInt(approvalExecTimeout.value, 3600, 1, 86400),
    'osc.minRows': Math.max(1, Math.round(Number(oscMinRows.value) || 1000000)),
    'osc.minBytes': Math.max(1 << 20, Math.round((Number(oscMinGB.value) || 1) * (1 << 30))),
    'osc.maxThreadsRunning': clampInt(oscMaxThreads.value, 50, 0, 10000),
    'osc.maxReplicaLagSec': clampInt(oscMaxLag.value, 10, 0, 86400),
    'osc.chunkSize': clampInt(oscChunk.value, 1000, 1, 100000),
    'script.savePath': scriptPath.value.trim(),
    'inspection.savePath': inspectionPath.value.trim(),
    'export.savePath': exportPath.value.trim(),
    // 0 = unlimited; negative input is meaningless, clamp to 0
    'export.maxRows': Math.max(0, Math.round(Number(exportMaxRows.value) || 0)),
    'export.maxBytes': Math.max(0, Math.round(Number(exportMaxBytes.value) || 0)),
    'export.execTimeout': Math.max(1, Math.round(Number(exportTimeout.value) || 1800)),
    // 0 = 永久保留,和 maxRows 的 0=不限一致;负数是笔误,归零
    'export.retentionDays': Math.max(0, Math.round(Number(exportRetention.value) || 0)),
    'meta.sync.enabled': metaEnabled.value,
    'meta.sync.intervalHours': clampInt(metaIntervalHrs.value, 24, 1, 720),
    'bizQuery.defaultMaxRows': bizInt(biz.value.defaultMaxRows, 1000),
    'bizQuery.maxRowsCap': bizInt(biz.value.maxRowsCap, 10000),
    'bizQuery.defaultTimeoutSec': bizInt(biz.value.defaultTimeoutSec, 30),
    'bizQuery.timeoutCapSec': bizInt(biz.value.timeoutCapSec, 300),
    'bizQuery.maxGrantDays': bizInt(biz.value.maxGrantDays, 90),
    'bizQuery.maxConcurrentPerUser': bizInt(biz.value.maxConcurrentPerUser, 2),
    'bizQuery.maxConcurrentPerSource': bizInt(biz.value.maxConcurrentPerSource, 5),
    'bizQuery.maxDailyPerUser': bizInt(biz.value.maxDailyPerUser, 0),
    'bizQuery.exportEnabled': biz.value.exportEnabled,
    'bizQuery.applyUrl': (biz.value.applyUrl || '').trim(),
    'meta.sync.concurrency': clampInt(metaConcurrency.value, 2, 1, 8),
    'conn.check.enabled': connCheckEnabled.value,
    'conn.check.intervalMinutes': clampInt(connCheckInterval.value, 10, 1, 1440),
    'conn.check.concurrency': clampInt(connCheckConcurrency.value, 4, 1, 16),
    'security.sessionTTL': ttlKey,
    'security.requireMFA': mfa.value,
    'security.mfaMandatory': mfaMandatory.value,
    'security.mfaGraceMinutes': mfaGrace.value,
    'security.idleLock': idle.value,
    'security.idleMinutes': Math.max(1, Math.round(Number(idleMinutes.value) || 15)),
    'security.adminIdleMinutes': Math.max(1, Math.round(Number(adminIdleMinutes.value) || 5)),
    'security.ipAllowlist': ipAllow.value,
    'security.ipAllowEnabled': ipAllowEnabled.value,
    'sso.enabled': ssoEnabled.value,
    'sso.baseUrl': ssoBaseUrl.value.trim(),
    'sso.cookieName': ssoCookie.value.trim() || 'CS-Token',
    'notify.lark': lark.value,
    'notify.email': email.value,
    'notify.push': push.value,
    'notify.larkWebhook': larkWebhook.value.trim(),
    'notify.larkSecret': larkSecret.value.trim(),
    'notify.consoleURL': consoleURL.value.trim(),
    // External 飞书审批(审批魔方) — empty token/secret keeps the stored one.
    'approval.external.enabled': extEnabled.value,
    'approval.external.releaseEnabled': extRelease.value,
    'approval.external.baseURL': extBaseURL.value.trim(),
    'approval.external.token': extToken.value.trim(),
    'approval.external.aiGroup': extAiGroup.value.trim(),
    'approval.external.callbackBaseURL': extCallbackBaseURL.value.trim(),
    'approval.external.callbackSecret': extCallbackSecret.value.trim(),
    'approval.external.callbackAllowIPs': extAllowIPs.value.trim(),
  }
}
// 改没改过:和读进来 / 上次保存时的那一份比。两个 IP 白名单在各自的弹窗里即存即生效,不算在内。
const baseline = ref('')
function dirtyView() {
  const p: Record<string, unknown> = settingsPayload()
  delete p['security.ipAllowlist']
  delete p['security.ipAllowEnabled']
  return JSON.stringify(p)
}
function takeBaseline() { baseline.value = dirtyView() }
useUnsavedFlag(() => !!baseline.value && dirtyView() !== baseline.value)

async function save() {
  try {
    await api.saveSettings(settingsPayload())
    takeBaseline()
    saved.value = true
    setTimeout(() => (saved.value = false), 2200)
    ui.notify(t('settingsSaved'), 'success')
  } catch (e) {
    ui.notifyError(e, t('saveFailed')) // R28: no more silent failure
  }
}
</script>

<template>
  <div class="scy page">
    <!-- Tab bar: switch between setting groups instead of one long scroll.
         刻意放在 .col 外面 —— 见下方 .tabs 的注释。 -->
    <div class="v-head">
      <div class="v-head-l">
        <div class="v-eyebrow">SETTINGS</div>
      </div>
    </div>
    <div class="layout">
    <nav class="tabs">
      <template v-for="g in tabGroups" :key="g.label">
        <div class="tgroup">{{ $t(g.label) }}</div>
        <button v-for="tb in g.tabs" :key="tb.id" class="tab" :class="{ active: activeTab === tb.id }" @click="activeTab = tb.id">
          <component :is="tb.icon" :size="15" /><span>{{ $t(tb.label) }}</span>
        </button>
      </template>
      <!-- About -->
      <div class="about">
        <div class="alogo" :class="{ custom: !!brand.logo }"><BrandMark :size="15" /></div>
        <div><div class="an">{{ brandText(ui.lang) }}</div><div class="av">{{ $t('verLabel') }} 2.4.1</div><div class="av">{{ $t('buildLabel') }} 20260624</div></div>
      </div>
    </nav>
    <div class="col">

      <!-- Gateway -->
      <section v-show="activeTab === 'gateway'" class="card">
        <div class="shead"><div class="sic"><Shield :size="17" color="var(--accent-text)" /></div><div><div class="st">{{ $t('setGw') }}</div><div class="ss">{{ $t('setGwSub') }}</div></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setDefPolicy') }}</div><div class="rd">{{ $t('setDefPolicyD') }}</div></div><div class="w180"><VSelect v-model="policy" :options="policyOpts" /></div></div>
        <!-- 无 WHERE 的 DELETE / UPDATE 按分层开关(迁移 0030),开关在【环境分层】页 —— 
             判定的另外两层也在那套按分层的规则里,不该有第二个能改同一件事的地方。 -->
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setStrict') }}</div><div class="rd">{{ $t('setStrictD') }}</div></div><RouterLink class="srlink" to="/admin/env-tiers">{{ $t('pStrictGo') }}</RouterLink></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setTimeout') }}</div><div class="rd">{{ $t('setTimeoutD') }}</div></div><div class="w160"><input v-model.number="execTimeout" type="number" min="1" max="3600" class="lkin" /></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setPageSize') }}</div><div class="rd">{{ $t('setPageSizeD') }}</div></div><div class="w160"><input v-model.number="resultPageSize" type="number" min="100" max="50000" class="lkin" data-testid="result-page-size" /></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setApprovalTimeout') }}</div><div class="rd">{{ $t('setApprovalTimeoutD') }}</div></div><div class="w160"><input v-model.number="approvalExecTimeout" type="number" min="1" max="86400" class="lkin" data-testid="set-approval-timeout" /></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setOscMinRows') }}</div><div class="rd">{{ $t('setOscMinRowsD') }}</div></div><div class="w160"><input v-model.number="oscMinRows" type="number" min="1" class="lkin" data-testid="set-osc-rows" /></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setOscMinGB') }}</div><div class="rd">{{ $t('setOscMinGBD') }}</div></div><div class="w160"><input v-model.number="oscMinGB" type="number" min="0.01" step="0.1" class="lkin" data-testid="set-osc-gb" /></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setOscThreads') }}</div><div class="rd">{{ $t('setOscThreadsD') }}</div></div><div class="w160"><input v-model.number="oscMaxThreads" type="number" min="0" class="lkin" data-testid="set-osc-threads" /></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setOscLag') }}</div><div class="rd">{{ $t('setOscLagD') }}</div></div><div class="w160"><input v-model.number="oscMaxLag" type="number" min="0" class="lkin" data-testid="set-osc-lag" /></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setOscChunk') }}</div><div class="rd">{{ $t('setOscChunkD') }}</div></div><div class="w160"><input v-model.number="oscChunk" type="number" min="1" class="lkin" data-testid="set-osc-chunk" /></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setScriptPath') }}</div><div class="rd">{{ $t('setScriptPathD') }}</div></div><input v-model="scriptPath" class="pathinput" :placeholder="$t('setScriptPathPlaceholder')" /></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setInspectionPath') }}</div><div class="rd">{{ $t('setInspectionPathD') }}</div></div><input v-model="inspectionPath" class="pathinput" data-testid="inspection-path" :placeholder="$t('setInspectionPathPlaceholder')" /></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setExportPath') }}</div><div class="rd">{{ $t('setExportPathD') }}</div></div><input v-model="exportPath" class="pathinput" :placeholder="$t('setExportPathPlaceholder')" /></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setExportMaxRows') }}</div><div class="rd">{{ $t('setExportMaxRowsD') }}</div></div><div class="w160"><input v-model.number="exportMaxRows" type="number" min="0" class="lkin" /></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setExportMaxBytes') }}</div><div class="rd">{{ $t('setExportMaxBytesD') }}</div></div><div class="w160"><input v-model.number="exportMaxBytes" type="number" min="0" class="lkin" /></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setExportTimeout') }}</div><div class="rd">{{ $t('setExportTimeoutD') }}</div></div><div class="w160"><input v-model.number="exportTimeout" type="number" min="1" class="lkin" /></div></div>
        <div class="srow last"><div class="grow"><div class="rt">{{ $t('setExportRetention') }}</div><div class="rd">{{ $t('setExportRetentionD') }}</div></div><div class="w160"><input v-model.number="exportRetention" type="number" min="0" class="lkin" /></div></div>
      </section>

      <!-- 业务方数据查询 -->
      <section v-show="activeTab === 'bizquery'" class="card" data-testid="biz-settings">
        <div class="shead"><div class="sic"><SearchCode :size="17" color="var(--accent-text)" /></div><div><div class="st">{{ $t('setBiz') }}</div><div class="ss">{{ $t('setBizSub') }}</div></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setBizRows') }}</div><div class="rd">{{ $t('setBizRowsD') }}</div></div>
          <div class="pair"><input v-model.number="biz.defaultMaxRows" type="number" min="1" class="lkin" data-testid="biz-default-rows" /><span>/</span><input v-model.number="biz.maxRowsCap" type="number" min="1" max="100000" class="lkin" /></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setBizTimeout') }}</div><div class="rd">{{ $t('setBizTimeoutD') }}</div></div>
          <div class="pair"><input v-model.number="biz.defaultTimeoutSec" type="number" min="1" class="lkin" /><span>/</span><input v-model.number="biz.timeoutCapSec" type="number" min="1" max="3600" class="lkin" /></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setBizDays') }}</div><div class="rd">{{ $t('setBizDaysD') }}</div></div><div class="w160"><input v-model.number="biz.maxGrantDays" type="number" min="1" max="365" class="lkin" /></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setBizConc') }}</div><div class="rd">{{ $t('setBizConcD') }}</div></div>
          <div class="pair"><input v-model.number="biz.maxConcurrentPerUser" type="number" min="1" max="20" class="lkin" /><span>/</span><input v-model.number="biz.maxConcurrentPerSource" type="number" min="1" max="100" class="lkin" /></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setBizDaily') }}</div><div class="rd">{{ $t('setBizDailyD') }}</div></div><div class="w160"><input v-model.number="biz.maxDailyPerUser" type="number" min="0" max="100000" class="lkin" data-testid="biz-daily" /></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setBizExport') }}</div><div class="rd">{{ $t('setBizExportD') }}</div></div><VSwitch v-model="biz.exportEnabled" data-testid="biz-export-switch" /></div>
        <div class="srow last"><div class="grow"><div class="rt">{{ $t('setBizApply') }}</div><div class="rd">{{ $t('setBizApplyD') }}</div></div>
          <div class="wurl"><input v-model="biz.applyUrl" class="lkin" placeholder="https://…" data-testid="biz-apply-url" /></div></div>
      </section>

      <!-- 元数据同步 -->
      <section v-show="activeTab === 'meta'" class="card">
        <div class="shead"><div class="sic"><DatabaseZap :size="17" color="var(--accent-text)" /></div><div><div class="st">{{ $t('setMeta') }}</div><div class="ss">{{ $t('setMetaSub') }}</div></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setMetaEnabled') }}</div><div class="rd">{{ $t('setMetaEnabledD') }}</div></div><VSwitch v-model="metaEnabled" /></div>
        <div v-if="metaEnabled" class="srow"><div class="grow"><div class="rt">{{ $t('setMetaInterval') }}</div><div class="rd">{{ $t('setMetaIntervalD') }}</div></div><div class="w160"><input v-model.number="metaIntervalHrs" type="number" min="1" max="720" class="lkin" /></div></div>
        <div v-if="metaEnabled" class="srow"><div class="grow"><div class="rt">{{ $t('setMetaConcurrency') }}</div><div class="rd">{{ $t('setMetaConcurrencyD') }}</div></div><div class="w160"><input v-model.number="metaConcurrency" type="number" min="1" max="8" class="lkin" /></div></div>
        <!-- 连通性巡检。和元数据同步放在一页,是因为它们回答的是同一个问题:
             这台网关会不会主动去连你的库。 -->
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setConnCheck') }}</div><div class="rd">{{ $t('setConnCheckD') }}</div></div><VSwitch v-model="connCheckEnabled" /></div>
        <div v-if="connCheckEnabled" class="srow"><div class="grow"><div class="rt">{{ $t('setConnCheckInterval') }}</div><div class="rd">{{ $t('setConnCheckIntervalD') }}</div></div><div class="w160"><input v-model.number="connCheckInterval" type="number" min="1" max="1440" class="lkin" /></div></div>
        <div v-if="connCheckEnabled" class="srow"><div class="grow"><div class="rt">{{ $t('setConnCheckConcurrency') }}</div><div class="rd">{{ $t('setConnCheckConcurrencyD') }}</div></div><div class="w160"><input v-model.number="connCheckConcurrency" type="number" min="1" max="16" class="lkin" /></div></div>
        <div v-if="connCheckEnabled" class="srow"><div class="grow"><div class="rt">{{ $t('setConnCheckSkip') }}</div><div class="rd">{{ $t('setConnCheckSkipD') }}</div></div></div>

        <!-- 这三句不是客套话,是这个功能真实的三处行为。少说任何一句,人都会等在
             一个不会发生的事情上,或者以为某台实例是空的。 -->
        <div class="srow last"><div class="grow"><div class="rt">{{ $t('setMetaNote') }}</div>
          <div class="rd">{{ $t('setMetaNoteFirst') }}</div>
          <div class="rd">{{ $t('setMetaNoteSkip') }}</div>
          <div class="rd">{{ $t('setMetaNoteStale') }}</div>
        </div><RouterLink class="srlink" to="/admin/connections">{{ $t('setMetaGo') }}</RouterLink></div>
      </section>

      <!-- Approval -->
      <section v-show="activeTab === 'approval'" class="card">
        <div class="shead"><div class="sic"><GitPullRequestArrow :size="17" color="var(--accent-text)" /></div><div><div class="st">{{ $t('setAppr') }}</div><div class="ss">{{ $t('setApprSub') }}</div></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setApprTimeout') }}</div><div class="rd">{{ $t('setApprTimeoutD') }}</div></div><div class="w180"><VSelect :model-value="labelOf(apprTimeout, $t)" :options="APPROVAL_TIMEOUT_KEYS.map((k) => labelOf(k, $t))" @update:model-value="(l: string) => (apprTimeout = keyForLabel(l, APPROVAL_TIMEOUT_KEYS, $t, 'auto-escalate'))" /></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setDefApprovers') }}</div><div class="rd">{{ $t('setDefApproversD') }}</div></div><div class="approvers"><span v-for="a in approvers" :key="a.id" class="apv"><span class="ava">{{ a.initials }}</span>{{ a.name }}</span><span v-if="!approvers.length" class="apv-empty">{{ $t('setApproversEmpty') }}</span><a class="apv-manage" @click="goPerms">{{ $t('setApproversManage') }}</a></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setEscalate') }}</div><div class="rd">{{ $t('setEscalateD') }}</div></div><VSwitch v-model="escalate" /></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setSelfApprove') }}</div><div class="rd">{{ $t('setSelfApproveD') }}</div></div><VSwitch v-model="allowSelf" /></div>
        <!-- External 飞书审批(审批魔方) -->
        <div class="srow" :class="{ last: !extEnabled }"><div class="grow"><div class="rt">{{ $t('setExtAppr') }}</div><div class="rd">{{ $t('setExtApprD') }}</div></div><VSwitch v-model="extEnabled" /></div>
        <div v-if="extEnabled" class="srow" data-testid="ext-release-row"><div class="grow"><div class="rt">{{ $t('setExtRelease') }}</div><div class="rd">{{ $t('setExtReleaseD') }}</div></div><VSwitch v-model="extRelease" /></div>
        <div v-if="extEnabled" class="larkcfg">
          <div class="lkrow"><label class="lkl">{{ $t('extBaseURL') }}</label><input v-model="extBaseURL" class="lkin" placeholder="https://approval.example.com" /></div>
          <div class="lkrow"><label class="lkl">{{ $t('extToken') }}</label><input v-model="extToken" type="password" class="lkin" :placeholder="hasExtToken ? $t('secretConfigured') : $t('extTokenPh')" /><VButton v-if="hasExtToken" height="36px" @click="clearSecret('approval.external.token')">{{ $t('secretClear') }}</VButton></div>
          <div class="lkrow"><label class="lkl">{{ $t('extAiGroup') }}</label><input v-model="extAiGroup" class="lkin" placeholder="K8S_AI" /></div>
          <div class="lkrow"><label class="lkl">{{ $t('extCallbackBase') }}</label><input v-model="extCallbackBaseURL" class="lkin" placeholder="https://gw.corp.io" /></div>
          <div v-if="extCallbackURL" class="lkhint">{{ $t('extCallbackFull') }}<span class="mono">{{ extCallbackURL }}</span></div>
          <div class="lkrow"><label class="lkl">{{ $t('extCallbackSecret') }}</label><input v-model="extCallbackSecret" type="password" class="lkin" :placeholder="hasExtCallbackSecret ? $t('secretConfigured') : $t('extCallbackSecretPh')" /><VButton v-if="hasExtCallbackSecret" height="36px" @click="clearSecret('approval.external.callbackSecret')">{{ $t('secretClear') }}</VButton></div>
          <div class="lkrow"><label class="lkl">{{ $t('extAllowIPs') }}</label><input v-model="extAllowIPs" class="lkin" :placeholder="$t('extAllowIPsPh')" /></div>
        </div>
      </section>

      <!-- Security -->
      <section v-show="activeTab === 'security'" class="card">
        <div class="shead"><div class="sic"><Lock :size="17" color="var(--accent-text)" /></div><div><div class="st">{{ $t('setSec') }}</div><div class="ss">{{ $t('setSecSub') }}</div></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setTtl') }}</div><div class="rd">{{ $t('setTtlD') }}</div></div><div class="w160"><VSelect :model-value="labelOf(ttl, $t)" :options="SESSION_TTL_KEYS.map((k) => labelOf(k, $t))" @update:model-value="(l: string) => (ttl = keyForLabel(l, SESSION_TTL_KEYS, $t, '8h'))" /></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setIdle') }}</div><div class="rd">{{ $t('setIdleD') }}</div></div><VSwitch v-model="idle" /></div>
        <div v-if="idle" class="srow"><div class="grow"><div class="rt">{{ $t('setIdleMinutes') }}</div><div class="rd">{{ $t('setIdleMinutesDesc') }}</div></div><div class="w160"><input v-model.number="idleMinutes" type="number" min="1" max="1440" class="lkin" /></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setAdminIdle') }}</div><div class="rd">{{ $t('setAdminIdleD') }}</div></div><div class="w160"><input v-model.number="adminIdleMinutes" type="number" min="1" max="1440" class="lkin" data-testid="admin-idle-minutes" /></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setMfa') }}</div><div class="rd">{{ $t('setMfaD') }}</div></div><VSwitch v-model="mfa" /></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setMfaEnroll') }}</div><div class="rd">{{ $t('setMfaEnrollD') }}</div></div>
          <div class="ipwrap">
            <span class="badge" :class="mfaBound ? 'on' : 'off'"><KeyRound :size="12" />{{ mfaBound ? $t('mfaBound') : $t('mfaUnbound') }}</span>
            <VButton variant="secondary" height="34px" @click="mfaModalOpen = true">{{ mfaBound ? $t('manage') : $t('mfaBind') }}</VButton>
          </div>
        </div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setIpAllow') }}</div><div class="rd">{{ $t('setIpAllowD') }}</div></div>
          <div class="ipwrap">
            <span class="badge" :class="ipAllowEnabled ? 'on' : 'off'">{{ ipAllowEnabled ? $t('enabledTag') : $t('disabledTag') }}</span>
            <span class="mono">{{ ipAllow }}</span>
            <VButton variant="secondary" height="34px" @click="ipModalOpen = true">{{ $t('manage') }}</VButton>
          </div>
        </div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setAdminIpAllow') }}</div><div class="rd">{{ $t('setAdminIpAllowD') }}</div></div>
          <div class="ipwrap">
            <span class="badge" :class="adminIpEnabled ? 'on' : 'off'" data-testid="admin-ip-badge">{{ adminIpEnabled ? $t('enabledTag') : $t('disabledTag') }}</span>
            <span class="mono">{{ adminIpList }}</span>
            <VButton variant="secondary" height="34px" data-testid="admin-ip-manage" @click="adminIpModalOpen = true">{{ $t('manage') }}</VButton>
          </div>
        </div>
        <div v-if="adminIpForcedOff" class="ssowarn" data-testid="admin-ip-forced-off">{{ $t('setAdminIpForcedOff') }}</div>
        <!-- 公司 SSO:只做认证,授权仍由角色 / 菜单 / 能力矩阵决定 -->
        <div class="srow" :class="{ last: !ssoEnabled }"><div class="grow"><div class="rt">{{ $t('setSso') }}</div><div class="rd">{{ $t('setSsoD') }}</div></div><VSwitch v-model="ssoEnabled" data-testid="sso-enabled" /></div>
        <div v-if="ssoForcedOff" class="ssowarn" data-testid="sso-forced-off">{{ $t('setSsoForcedOff') }}</div>
        <div v-if="ssoEnabled" class="larkcfg">
          <div class="lkrow"><label class="lkl">{{ $t('setSsoBase') }}</label><input v-model="ssoBaseUrl" class="lkin" placeholder="https://srd.bssrvc66.com" data-testid="sso-base" /></div>
          <div class="lkrow"><label class="lkl">{{ $t('setSsoCookie') }}</label><input v-model="ssoCookie" class="lkin" placeholder="CS-Token" /></div>
          <div class="ssowarn">{{ $t('setSsoBindHint') }}</div>
        </div>
      </section>

      <!-- Notifications -->
      <section v-show="activeTab === 'notify'" class="card">
        <div class="shead"><div class="sic"><Bell :size="17" color="var(--accent-text)" /></div><div><div class="st">{{ $t('setNotify') }}</div><div class="ss">{{ $t('setNotifySub') }}</div></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setLark') }}</div><div class="rd">{{ $t('setLarkD') }}</div></div><VSwitch v-model="lark" /></div>
        <div v-if="lark" class="larkcfg">
          <div class="lkrow"><label class="lkl">{{ $t('larkWebhook') }}</label><input v-model="larkWebhook" class="lkin" placeholder="https://open.feishu.cn/open-apis/bot/v2/hook/xxxx" /></div>
          <div class="lkrow"><label class="lkl">{{ $t('larkSecret') }}</label><input v-model="larkSecret" type="password" class="lkin" :placeholder="hasLarkSecret ? $t('secretConfiguredPh') : $t('larkSecretPh')" /><VButton v-if="hasLarkSecret" height="36px" @click="clearSecret('notify.larkSecret')">{{ $t('secretClear') }}</VButton></div>
          <div class="lkrow"><label class="lkl">{{ $t('larkConsole') }}</label><input v-model="consoleURL" class="lkin" placeholder="https://gw.corp.io" /></div>
          <div class="lkfoot">
            <span class="lkmsg" :class="larkTest.state">{{ larkTest.state === 'sending' ? $t('larkSending') : larkTest.msg }}</span>
            <VButton variant="secondary" height="34px" :disabled="!larkWebhook.trim()" @click="testLark">{{ $t('larkSendTest') }}</VButton>
          </div>
        </div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setEmail') }}</div><div class="rd">{{ $t('setEmailD') }}</div></div><VSwitch v-model="email" /></div>
        <div class="srow last"><div class="grow"><div class="rt">{{ $t('setPush') }}</div><div class="rd">{{ $t('setPushD') }}</div></div><VSwitch v-model="push" /></div>
      </section>

      <!-- Webhook (audit event forwarding) -->
      <WebhookPanel v-show="activeTab === 'webhook'" />

      <!-- 敏感字段:规则在这里维护,脱敏在服务端做 -->
      <SensitiveColumnsPanel v-show="activeTab === 'sensitive'" />

      <!-- 项目的增删改:管理接口,只在管理后台做;用户后台的项目页只读 -->
      <ProjectsPanel v-show="activeTab === 'projects'" />
      <BrandPanel v-if="activeTab === 'brand'" />

      <!-- 开放接口凭据 -->
      <ApiClientsPanel v-show="activeTab === 'openapi'" />

      <!-- Appearance -->
      <section v-show="activeTab === 'appearance'" class="card">
        <div class="shead"><div class="sic"><Palette :size="17" color="var(--accent-text)" /></div><div><div class="st">{{ $t('setAppearance') }}</div><div class="ss">{{ $t('setAppearanceSub') }}</div></div></div>
        <div class="srow"><div class="grow"><div class="rt">{{ $t('setLangRow') }}</div></div><div class="seg"><div class="si" :class="{ active: ui.lang === 'zh' }" @click="ui.setLang('zh')">中文</div><div class="si" :class="{ active: ui.lang === 'en' }" @click="ui.setLang('en')">English</div></div></div>
        <div class="srow last"><div class="grow"><div class="rt">{{ $t('setThemeRow') }}</div></div><div class="seg"><div class="si" :class="{ active: ui.theme === 'dark' }" @click="ui.setTheme('dark')">{{ $t('themeDark') }}</div><div class="si" :class="{ active: ui.theme === 'light' }" @click="ui.setTheme('light')">{{ $t('themeLight') }}</div><div class="si" :class="{ active: ui.theme === 'system' }" @click="ui.setTheme('system')">{{ $t('themeSystem') }}</div></div></div>
      </section>

      <!-- 吸底保存栏:网关、审批、安全、通知、数据查询等页的改动一起保存 -->
      <div v-if="showSave" class="savebar">
        <div class="grow sbhint">{{ $t('setSaveHint') }}</div>
        <span v-if="saved" class="savedtag">{{ $t('saved') }}</span>
        <VButton variant="primary" @click="save">{{ $t('saveSettings') }}</VButton>
      </div>
    </div>
    </div>

    <MfaModal :open="mfaModalOpen" :enabled="mfaBound" @close="mfaModalOpen = false" @changed="onMfaChanged" />
    <IpAllowlistModal :open="ipModalOpen" :enabled="ipAllowEnabled" :list="ipAllow" @close="ipModalOpen = false" @save="onIpSave" />
    <IpAllowlistModal :open="adminIpModalOpen" :enabled="adminIpEnabled" :list="adminIpList" title-key="adminIpModalTitle" sub-key="adminIpModalSub"
      @close="adminIpModalOpen = false" @save="onAdminIpSave" />
  </div>
</template>

<style scoped>
.page { flex: 1; min-height: 0; padding: var(--page-pad); max-width: var(--page-max-narrow); margin-inline: auto; width: 100%; }
/* 左导航 + 右正文。导航吸顶,正文长了滚动时页签一直够得着。 */
.layout { display: grid; grid-template-columns: 208px minmax(0, 1fr); gap: 24px; align-items: start; }
.col { min-width: 0; display: flex; flex-direction: column; gap: 18px; }
/* 页签竖排分组。仍用 .tabs / .tab 这两个类名 —— e2e 靠它们找页签。 */
.tabs { position: sticky; top: 0; display: flex; flex-direction: column; gap: 2px; }
.tgroup { padding: 14px 12px 6px; font: 600 10.5px var(--font-mono); letter-spacing: .06em; text-transform: uppercase; color: var(--text-faint); }
.tgroup:first-child { padding-top: 0; }
.pair { display: flex; align-items: center; gap: 6px; width: 220px; }
.pair .lkin { width: 100%; }
.pair span { color: var(--text-faint); }
.tab {
  position: relative; display: flex; align-items: center; gap: 9px; width: 100%; height: 36px; padding: 0 12px;
  border: none; border-radius: 9px; background: transparent; text-align: left;
  font: 500 13px var(--font-body); color: var(--text-muted); cursor: pointer;
  transition: color .12s, background .12s;
}
.tab span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tab:hover { color: var(--text-body); background: var(--surface-sunken); }
.tab.active { background: var(--accent-subtle); color: var(--accent-text); font-weight: 600; }
.tab.active::before { content: ''; position: absolute; left: 0; top: 9px; bottom: 9px; width: 3px; border-radius: 3px; background: var(--accent); }
.card { border: none; border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-xs); overflow: hidden; }
.shead { display: flex; align-items: center; gap: 12px; padding: 18px 22px; border-bottom: 1px solid var(--border-subtle); background: linear-gradient(180deg, var(--accent-subtle), transparent 140%); }
.sic { width: 36px; height: 36px; flex-shrink: 0; border-radius: 10px; background: var(--surface-card); border: 1px solid var(--accent-subtle-border); display: flex; align-items: center; justify-content: center; }
.st { font: 650 15px var(--font-display); color: var(--text-strong); }
.ss { font: 500 12px var(--font-body); color: var(--text-muted); margin-top: 2px; }
.srlink { flex-shrink: 0; font: 600 12px var(--font-body); color: var(--accent-text); text-decoration: none; white-space: nowrap; }
.srlink:hover { text-decoration: underline; }
.srow { display: flex; align-items: center; gap: 24px; padding: 16px 22px; border-bottom: 1px solid var(--border-subtle); transition: background .12s; }
.srow:hover { background: color-mix(in srgb, var(--surface-sunken) 55%, transparent); }
.larkcfg { padding: 6px 22px 16px; border-bottom: 1px solid var(--border-subtle); display: flex; flex-direction: column; gap: 10px; }
.lkrow { display: flex; align-items: center; gap: 12px; }
.lkl { width: 128px; flex-shrink: 0; font: 500 12px var(--font-body); color: var(--text-muted); }
/* min-width:0 —— 没有它,flex 项目不会缩到 input 的固有宽度(number 约 197px)以下,
   160px 的槽位就装不下,数值框会压出卡片边缘。 */
.lkin { flex: 1; min-width: 0; height: 36px; box-sizing: border-box; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-sunken); padding: 0 12px; font: 500 12.5px var(--font-mono); color: var(--text-strong); outline: none; }
.lkin:focus { border-color: var(--accent-text); }
.lkhint { margin: -4px 0 2px 140px; font: 500 11.5px var(--font-body); color: var(--text-faint); display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.lkhint .mono { font: 500 11.5px var(--font-mono); color: var(--text-muted); }
.lkfoot { display: flex; align-items: center; gap: 12px; margin-top: 2px; }
.lkmsg { flex: 1; font: 600 12px var(--font-mono); color: var(--text-muted); }
.lkmsg.ok { color: var(--success-text); }
.lkmsg.err { color: var(--danger-text); }
.lkmsg.sending { color: var(--accent-text); }
.srow.last { border-bottom: none; }
.grow { flex: 1; min-width: 0; }
.rt { font: 600 13px var(--font-body); color: var(--text-strong); }
.rd { font: 400 12px/1.55 var(--font-body); color: var(--text-muted); margin-top: 3px; }
.w180 { width: 180px; }
/* 里面的 input 是 flex:1,父级不是 flex 容器时它按 type=number 的默认宽度
   撑到 ~177px,溢出卡片右边 —— 四个导出数值框一直是歪的。 */
.w160 { width: 160px; display: flex; flex-shrink: 0; }
.wurl { width: min(460px, 50%); display: flex; flex-shrink: 0; }
.wurl .lkin { width: 100%; }
.w160 > * { flex: 1; min-width: 0; }
.chipval { font: 600 13px var(--font-mono); color: var(--text-body); background: var(--surface-sunken); border: 1px solid var(--border-default); border-radius: 8px; padding: 7px 14px; }
.pathinput { width: 300px; height: 34px; padding: 0 12px; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-sunken); color: var(--text-strong); font: 500 12px var(--font-mono); outline: none; }
.pathinput:focus { border-color: var(--accent-text); }
.approvers { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; justify-content: flex-end; }
.apv-empty { font: 500 12px var(--font-body); color: var(--warning-text); }
.apv-manage { font: 600 12px var(--font-body); color: var(--accent-text); cursor: pointer; white-space: nowrap; }
.apv-manage:hover { text-decoration: underline; }
.apv { display: inline-flex; align-items: center; gap: 6px; padding: 5px 11px 5px 5px; border: 1px solid var(--border-default); border-radius: 999px; font: 600 11px var(--font-body); color: var(--text-body); }
.ava { width: 20px; height: 20px; border-radius: 50%; background: var(--surface-sunken); display: flex; align-items: center; justify-content: center; font: 600 9px var(--font-body); color: var(--text-muted); }
.ipwrap { display: flex; align-items: center; gap: 10px; }
.ipwrap .mono { font: 600 12px var(--font-mono); color: var(--text-muted); }
.badge { display: inline-flex; align-items: center; gap: 5px; height: 22px; padding: 0 9px; border-radius: 999px; font: 600 10px var(--font-mono); }
.badge.on { background: var(--success-subtle); color: var(--success-text); }
.badge.off { background: var(--surface-sunken); color: var(--text-faint); border: 1px solid var(--border-subtle); }
.seg { display: flex; border: 1px solid var(--border-default); border-radius: 9px; overflow: hidden; }
/* 用 flex 居中,不用 line-height —— `font:` 简写会把 line-height 一并重置成
   normal,而这里原先正是 `line-height: 34px; ... font: 600 ...`,后者把前者吹掉了,
   于是文字在盒子里贴着上边。改成 flex 之后,字号怎么调都不会再把居中弄丢。 */
.si { display: flex; align-items: center; height: 34px; padding: 0 16px; cursor: pointer; font: 600 12px var(--font-body); color: var(--text-muted); border-left: 1px solid var(--border-subtle); }
.si:first-child { border-left: none; }
.si.active { background: var(--accent-subtle); color: var(--accent-text); }
.about { display: flex; align-items: center; gap: 10px; margin-top: 18px; padding: 14px 12px 0; border-top: 1px solid var(--border-subtle); }
.alogo { width: 28px; height: 28px; flex-shrink: 0; border-radius: 8px; background: linear-gradient(135deg, #3b6ef6, #2dcde6); display: flex; align-items: center; justify-content: center; }
.alogo.custom { background: none; }
.an { font: 700 11.5px var(--font-display); color: var(--text-strong); letter-spacing: .02em; }
.av { font: 500 10.5px var(--font-mono); color: var(--text-faint); margin-top: 2px; }
/* 吸底保存栏:内容长到要滚动时,保存按钮也一直在手边。 */
.savebar {
  position: sticky; bottom: 0; z-index: 2; display: flex; align-items: center; justify-content: flex-end; gap: 14px; padding: 12px 16px 12px 22px;
  border: 1px solid var(--border-subtle); border-radius: var(--radius-lg);
  background: color-mix(in srgb, var(--surface-card) 88%, transparent); backdrop-filter: blur(8px); box-shadow: var(--shadow-md);
}
.sbhint { font: 500 12px var(--font-body); color: var(--text-muted); }
.savedtag { font: 600 12px var(--font-mono); color: var(--success-text); }

.ssowarn { margin-top: 8px; padding: 8px 10px; border-radius: 8px; font: 500 12px/1.55 var(--font-body); color: var(--warning-text); background: var(--warning-subtle); }
/* 窄屏:导航改成顶部横向滚动条,不再占一列。 */
@media (max-width: 900px) {
  .layout { grid-template-columns: minmax(0, 1fr); gap: 14px; }
  .tabs { position: static; flex-direction: row; overflow-x: auto; gap: 6px; padding-bottom: 4px; }
  .tgroup, .about { display: none; }
  .tab { width: auto; flex-shrink: 0; }
  .tab.active::before { display: none; }
  .srow { flex-wrap: wrap; gap: 12px; }
  .sbhint { display: none; }
}
</style>
