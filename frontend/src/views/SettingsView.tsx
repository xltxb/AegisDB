import { useState, type InputHTMLAttributes, type ReactNode } from 'react'
import { Link } from 'react-router'
import clsx from 'clsx'
import { EyeOff, Shield, GitPullRequestArrow, Lock, Bell, Palette, ImageUp, KeyRound, Webhook, DatabaseZap, SearchCode, FolderKanban, type LucideIcon } from 'lucide-react'
import VButton from '@/components/common/VButton'
import VSwitch from '@/components/common/VSwitch'
import VSelect from '@/components/common/VSelect'
import MfaModal from '@/components/modals/MfaModal'
import IpAllowlistModal from '@/components/modals/IpAllowlistModal'
import WebhookPanel from '@/components/settings/WebhookPanel'
import ApiClientsPanel from '@/components/settings/ApiClientsPanel'
import SensitiveColumnsPanel from '@/components/settings/SensitiveColumnsPanel'
import ProjectsPanel from '@/components/settings/ProjectsPanel'
import BrandPanel from '@/components/settings/BrandPanel'
import BrandMark from '@/components/BrandMark'
import { brandText } from '@/lib/brand'
import { APPROVAL_TIMEOUT_KEYS, SESSION_TTL_KEYS, labelOf, keyOf, keyForLabel } from '@/lib/settingOptions'
import { confirmAction } from '@/lib/confirm'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import { useBrand } from '@/hooks/useBrand'
import { useI18n } from '@/hooks/useI18n'
import { nav, useRoute } from '@/hooks/useNav'
import { useMountEffect, useUpdateEffect } from '@/hooks/effects'
import { useStateRef } from '@/hooks/useStateRef'
import { useUnsavedFlag } from '@/hooks/useUnsavedFlag'
import api from '@/api'
import type { Member } from '@/types'
import './SettingsView.scoped.css'

// Tabbed sections — only the active panel shows, so the page stays compact. 页签按主题分组
// 竖排在左侧:十个页签横排一行放不下,折到第二行的那一个像是漏掉的。
// own = 这一页自己保存(独立接口)或即时生效,底部的「保存设置」对它不起作用,就不显示,
// 免得人以为按一下能保存那一页的改动。
interface Tab { id: string; icon: LucideIcon; label: string; own?: boolean }
const tabGroups: { label: string; tabs: Tab[] }[] = [
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

// 申请数据查询权限的默认页面,与后端 service.DefaultBizApplyURL 一致(没保存过这一项时显示它)
const BIZ_APPLY_URL = 'https://srd.bssrvc66.com/service-center/query/db-gateway-grant'
const bizInt = (v: unknown, def: number) => { const n = Math.round(Number(v)); return Number.isFinite(n) ? n : def }


/** 数字输入框的值:清空时是 ''(不是 0)—— 保存时由各自的 clamp 兜底。 */
type Num = number | ''
const num = (n: number): Num => n

const INITIAL = {
  apprTimeout: 'auto-escalate' as string,
  allowSelf: false,
  ttl: '8h' as string,
  idle: true,
  idleMinutes: num(15),
  // 管理后台空闲多少分钟退回用户后台(工单 09);后端再按会话空闲锁定时长封顶
  adminIdleMinutes: num(5),
  mfa: true,
  // Force every account to enrol before it may touch PROD. Off by default: turning
  // it on blocks production work for anyone not yet enrolled, so it is a rollout
  // decision rather than a default.
  mfaMandatory: false,
  // How long one PROD step-up vouches for a session on that instance.
  mfaGrace: 30,
  // IP allowlist
  ipAllowEnabled: false,
  ipAllow: '',
  // SRD 授权登录(公司 SSO,只做认证)。存 tbl_setting 的 sso.*,即改即生效。
  ssoEnabled: false,
  ssoBaseUrl: '',
  ssoCookie: 'CS-Token',
  lark: true,
  // Lark (飞书) channel config — the approval card is pushed to this bot webhook.
  larkWebhook: '',
  larkSecret: '',
  consoleURL: '',
  // External 飞书审批(审批魔方)对接配置(ADR-0003)
  extEnabled: false,
  extRelease: false,
  extBaseURL: '',
  extToken: '',
  extAiGroup: '',
  extCallbackBaseURL: '',
  extCallbackSecret: '',
  extAllowIPs: '',
  execTimeout: num(30), // command execution timeout, seconds
  // Web 命令行每批显示多少行;超出的"加载更多"(后端暂存,一条查询最多读 5 万行)
  resultPageSize: num(1000),
  approvalExecTimeout: num(3600), // 审批单执行每条语句的时限(秒),与终端的分开
  // 在线变更(生产大表加 / 删索引):多大的表改走在线变更、拷贝的块大小、库端繁忙时暂停的阈值
  oscMinRows: num(1000000),
  oscMinGB: num(1), // 界面按 GB 填,存的是字节
  oscMaxThreads: num(50),
  oscMaxLag: num(10), // 从库复制延迟阈值(秒),0 = 不看
  oscChunk: num(1000),
  scriptPath: '',
  // AI 巡检报告保存路径(.scratch/inspection-report);留空则不接收报告
  inspectionPath: '',
  exportPath: '',
  exportMaxRows: num(5000000), // per-job export caps; 0 = unlimited
  exportMaxBytes: num(2000000000),
  exportTimeout: num(1800), // per-job export execution budget, seconds
  exportRetention: num(3), // 归档在服务器上保留几天,0 = 永久保留
  // 元数据同步。默认**关着** —— 打开它意味着这台网关会周期性地登录每一台实例,
  // 那必须是一次明确的决定,不能因为升级了一个版本就自己开始跑。
  metaEnabled: false,
  metaIntervalHrs: num(24),
  metaConcurrency: num(2),
  // 连通性巡检。也默认**关着** —— 它同样会周期性地登录每一台实例,只是比抓元数据轻。
  connCheckEnabled: false,
  connCheckInterval: num(10),
  connCheckConcurrency: num(4),
  // 业务方数据查询的全局值。保存时服务端整体校验(默认不超过上限、上限不超过硬边界),
  // 不合法的一项都不存 —— 这里只做输入框层面的整数处理,范围以服务端为准。
  bizDefaultMaxRows: num(1000),
  bizMaxRowsCap: num(10000),
  bizDefaultTimeoutSec: num(30),
  bizTimeoutCapSec: num(300),
  bizMaxGrantDays: num(90),
  bizMaxConcurrentPerUser: num(2),
  bizMaxConcurrentPerSource: num(5),
  bizMaxDailyPerUser: num(0),
  bizExportEnabled: false,
  bizApplyUrl: BIZ_APPLY_URL,
}
type Form = typeof INITIAL
/** 表单里取值为某一类型的那些字段 */
type KeysOf<V> = { [K in keyof Form]: Form[K] extends V ? (V extends Form[K] ? K : never) : never }[keyof Form]

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

/** 服务端存着的那份设置 → 表单。两个 IP 白名单之外的密钥类字段一律留空(见各处注释)。 */
function formOf(g: Record<string, string | undefined>): Form {
  return {
    allowSelf: parse(g['approval.allowSelfApprove'], false),
    mfa: parse(g['security.requireMFA'], true),
    mfaMandatory: parse(g['security.mfaMandatory'], false),
    mfaGrace: Number(parse(g['security.mfaGraceMinutes'], 30)) || 30,
    idle: parse(g['security.idleLock'], true),
    idleMinutes: Number(parse(g['security.idleMinutes'], 15)) || 15,
    adminIdleMinutes: Number(parse(g['security.adminIdleMinutes'], 5)) || 5,
    lark: parse(g['notify.lark'], true),
    larkWebhook: parse<string>(g['notify.larkWebhook'], ''),
    // The secret is never returned; keep the field blank (blank on save == keep
    // unchanged) and just remember whether one is on file. (R3)
    larkSecret: '',
    consoleURL: parse<string>(g['notify.consoleURL'], ''),
    apprTimeout: keyOf(parse<string>(g['approval.onTimeout'], 'auto-escalate'), APPROVAL_TIMEOUT_KEYS, 'auto-escalate'),
    ttl: keyOf(parse<string>(g['security.sessionTTL'], '8h'), SESSION_TTL_KEYS, '8h'),
    ipAllow: parse<string>(g['security.ipAllowlist'], ''),
    ipAllowEnabled: parse<boolean>(g['security.ipAllowEnabled'], false),
    ssoEnabled: parse<boolean>(g['sso.enabled'], false),
    ssoBaseUrl: parse<string>(g['sso.baseUrl'], ''),
    ssoCookie: parse<string>(g['sso.cookieName'], 'CS-Token') || 'CS-Token',
    scriptPath: parse<string>(g['script.savePath'], ''),
    inspectionPath: parse<string>(g['inspection.savePath'], ''),
    exportPath: parse<string>(g['export.savePath'], ''),
    exportMaxRows: Number(parse(g['export.maxRows'], 5000000)),
    exportMaxBytes: Number(parse(g['export.maxBytes'], 2000000000)),
    exportTimeout: Number(parse(g['export.execTimeout'], 1800)) || 1800,
    // `|| 3` 会把用户设的 0(永久保留)悄悄改回 3,所以这里不能用它兜底。
    exportRetention: Math.max(0, Number(parse(g['export.retentionDays'], 3))),
    execTimeout: clampInt(parse(g['gateway.execTimeout'], 30), 30, 1, 3600),
    resultPageSize: clampInt(parse(g['terminal.resultPageSize'], 1000), 1000, 100, 50000),
    approvalExecTimeout: clampInt(parse(g['gateway.approvalExecTimeout'], 3600), 3600, 1, 86400),
    oscMinRows: Math.max(1, Number(parse(g['osc.minRows'], 1000000)) || 1000000),
    oscMinGB: Math.max(0.01, Number(parse(g['osc.minBytes'], 1 << 30)) / (1 << 30) || 1),
    oscMaxThreads: clampInt(parse(g['osc.maxThreadsRunning'], 50), 50, 0, 10000),
    oscMaxLag: clampInt(parse(g['osc.maxReplicaLagSec'], 10), 10, 0, 86400),
    oscChunk: clampInt(parse(g['osc.chunkSize'], 1000), 1000, 1, 100000),
    metaEnabled: parse(g['meta.sync.enabled'], false),
    metaIntervalHrs: clampInt(parse(g['meta.sync.intervalHours'], 24), 24, 1, 720),
    bizDefaultMaxRows: bizInt(parse(g['bizQuery.defaultMaxRows'], 1000), 1000),
    bizMaxRowsCap: bizInt(parse(g['bizQuery.maxRowsCap'], 10000), 10000),
    bizDefaultTimeoutSec: bizInt(parse(g['bizQuery.defaultTimeoutSec'], 30), 30),
    bizTimeoutCapSec: bizInt(parse(g['bizQuery.timeoutCapSec'], 300), 300),
    bizMaxGrantDays: bizInt(parse(g['bizQuery.maxGrantDays'], 90), 90),
    bizMaxConcurrentPerUser: bizInt(parse(g['bizQuery.maxConcurrentPerUser'], 2), 2),
    bizMaxConcurrentPerSource: bizInt(parse(g['bizQuery.maxConcurrentPerSource'], 5), 5),
    bizMaxDailyPerUser: bizInt(parse(g['bizQuery.maxDailyPerUser'], 0), 0),
    bizExportEnabled: parse(g['bizQuery.exportEnabled'], false),
    bizApplyUrl: parse<string>(g['bizQuery.applyUrl'], BIZ_APPLY_URL),
    metaConcurrency: clampInt(parse(g['meta.sync.concurrency'], 2), 2, 1, 8),
    connCheckEnabled: parse(g['conn.check.enabled'], false),
    connCheckInterval: clampInt(parse(g['conn.check.intervalMinutes'], 10), 10, 1, 1440),
    connCheckConcurrency: clampInt(parse(g['conn.check.concurrency'], 4), 4, 1, 16),
    // External approval — secrets (token/callbackSecret) are never returned; keep
    // the fields blank (blank on save == keep unchanged) and just note presence.
    extEnabled: parse(g['approval.external.enabled'], false),
    extRelease: parse(g['approval.external.releaseEnabled'], false),
    extBaseURL: parse<string>(g['approval.external.baseURL'], ''),
    extAiGroup: parse<string>(g['approval.external.aiGroup'], ''),
    extCallbackBaseURL: parse<string>(g['approval.external.callbackBaseURL'], ''),
    extAllowIPs: parse<string>(g['approval.external.callbackAllowIPs'], ''),
    extToken: '',
    extCallbackSecret: '',
  }
}

// 底部「保存设置」要存的那一整份。保存与"改没改过"(管理后台空闲锁屏,2026-10-26 回归排查 10)共用它。
function settingsPayload(f: Form) {
  // The model holds the stored key, so saving needs no reverse lookup against
  // the current language (EF11).
  const onTimeout = keyOf(f.apprTimeout, APPROVAL_TIMEOUT_KEYS, 'auto-escalate')
  const ttlKey = keyOf(f.ttl, SESSION_TTL_KEYS, '8h')
  return {
    'approval.onTimeout': onTimeout,
    'approval.allowSelfApprove': f.allowSelf,
    'gateway.execTimeout': clampInt(f.execTimeout, 30, 1, 3600),
    'terminal.resultPageSize': clampInt(f.resultPageSize, 1000, 100, 50000),
    'gateway.approvalExecTimeout': clampInt(f.approvalExecTimeout, 3600, 1, 86400),
    'osc.minRows': Math.max(1, Math.round(Number(f.oscMinRows) || 1000000)),
    'osc.minBytes': Math.max(1 << 20, Math.round((Number(f.oscMinGB) || 1) * (1 << 30))),
    'osc.maxThreadsRunning': clampInt(f.oscMaxThreads, 50, 0, 10000),
    'osc.maxReplicaLagSec': clampInt(f.oscMaxLag, 10, 0, 86400),
    'osc.chunkSize': clampInt(f.oscChunk, 1000, 1, 100000),
    'script.savePath': f.scriptPath.trim(),
    'inspection.savePath': f.inspectionPath.trim(),
    'export.savePath': f.exportPath.trim(),
    // 0 = unlimited; negative input is meaningless, clamp to 0
    'export.maxRows': Math.max(0, Math.round(Number(f.exportMaxRows) || 0)),
    'export.maxBytes': Math.max(0, Math.round(Number(f.exportMaxBytes) || 0)),
    'export.execTimeout': Math.max(1, Math.round(Number(f.exportTimeout) || 1800)),
    // 0 = 永久保留,和 maxRows 的 0=不限一致;负数是笔误,归零
    'export.retentionDays': Math.max(0, Math.round(Number(f.exportRetention) || 0)),
    'meta.sync.enabled': f.metaEnabled,
    'meta.sync.intervalHours': clampInt(f.metaIntervalHrs, 24, 1, 720),
    'bizQuery.defaultMaxRows': bizInt(f.bizDefaultMaxRows, 1000),
    'bizQuery.maxRowsCap': bizInt(f.bizMaxRowsCap, 10000),
    'bizQuery.defaultTimeoutSec': bizInt(f.bizDefaultTimeoutSec, 30),
    'bizQuery.timeoutCapSec': bizInt(f.bizTimeoutCapSec, 300),
    'bizQuery.maxGrantDays': bizInt(f.bizMaxGrantDays, 90),
    'bizQuery.maxConcurrentPerUser': bizInt(f.bizMaxConcurrentPerUser, 2),
    'bizQuery.maxConcurrentPerSource': bizInt(f.bizMaxConcurrentPerSource, 5),
    'bizQuery.maxDailyPerUser': bizInt(f.bizMaxDailyPerUser, 0),
    'bizQuery.exportEnabled': f.bizExportEnabled,
    'bizQuery.applyUrl': (f.bizApplyUrl || '').trim(),
    'meta.sync.concurrency': clampInt(f.metaConcurrency, 2, 1, 8),
    'conn.check.enabled': f.connCheckEnabled,
    'conn.check.intervalMinutes': clampInt(f.connCheckInterval, 10, 1, 1440),
    'conn.check.concurrency': clampInt(f.connCheckConcurrency, 4, 1, 16),
    'security.sessionTTL': ttlKey,
    'security.requireMFA': f.mfa,
    'security.mfaMandatory': f.mfaMandatory,
    'security.mfaGraceMinutes': f.mfaGrace,
    'security.idleLock': f.idle,
    'security.idleMinutes': Math.max(1, Math.round(Number(f.idleMinutes) || 15)),
    'security.adminIdleMinutes': Math.max(1, Math.round(Number(f.adminIdleMinutes) || 5)),
    'security.ipAllowlist': f.ipAllow,
    'security.ipAllowEnabled': f.ipAllowEnabled,
    'sso.enabled': f.ssoEnabled,
    'sso.baseUrl': f.ssoBaseUrl.trim(),
    'sso.cookieName': f.ssoCookie.trim() || 'CS-Token',
    'notify.lark': f.lark,
    'notify.larkWebhook': f.larkWebhook.trim(),
    'notify.larkSecret': f.larkSecret.trim(),
    'notify.consoleURL': f.consoleURL.trim(),
    // External 飞书审批(审批魔方) — empty token/secret keeps the stored one.
    'approval.external.enabled': f.extEnabled,
    'approval.external.releaseEnabled': f.extRelease,
    'approval.external.baseURL': f.extBaseURL.trim(),
    'approval.external.token': f.extToken.trim(),
    'approval.external.aiGroup': f.extAiGroup.trim(),
    'approval.external.callbackBaseURL': f.extCallbackBaseURL.trim(),
    'approval.external.callbackSecret': f.extCallbackSecret.trim(),
    'approval.external.callbackAllowIPs': f.extAllowIPs.trim(),
  }
}
// 改没改过:和读进来 / 上次保存时的那一份比。两个 IP 白名单在各自的弹窗里即存即生效,不算在内。
function dirtyView(f: Form) {
  const p: Record<string, unknown> = settingsPayload(f)
  delete p['security.ipAllowlist']
  delete p['security.ipAllowEnabled']
  return JSON.stringify(p)
}

/** 数字输入框:能解析就是数字,清空就是 '';失焦时把 "007" 这类写法收成 "7"。 */
function NumInput({ value, onChange, ...rest }: Omit<InputHTMLAttributes<HTMLInputElement>, 'value' | 'onChange'> & { value: Num; onChange: (v: Num) => void }) {
  return (
    <input
      {...rest} type="number" className="lkin" value={value}
      onChange={(e) => { const n = parseFloat(e.target.value); onChange(isNaN(n) ? '' : n) }}
      onBlur={(e) => { if (e.target.value !== String(value)) e.target.value = String(value) }}
    />
  )
}

/** 一行设置:左边标题 + 说明(词条 key),右边控件。 */
function Row({ title, desc, last, children, ...rest }: { title: string; desc?: string; last?: boolean; children?: ReactNode; 'data-testid'?: string }) {
  const { t } = useI18n()
  return (
    <div {...rest} className={clsx('srow', last && 'last')}>
      <div className="grow"><div className="rt">{t(title)}</div>{desc && <div className="rd">{t(desc)}</div>}</div>
      {children}
    </div>
  )
}

/** 一张设置卡的页眉。 */
function Head({ icon: Icon, title, desc }: { icon: LucideIcon; title: string; desc: string }) {
  const { t } = useI18n()
  return (
    <div className="shead"><div className="sic"><Icon size={17} color="var(--accent-text)" /></div><div><div className="st">{t(title)}</div><div className="ss">{t(desc)}</div></div></div>
  )
}

// The approval chain is the "DBA 负责人" role membership — managed on the
// Permissions page, not here (settings only displays it).
function goPerms() { void nav.push('/admin/permissions') }

export default function SettingsView() {
  const { t } = useI18n()
  const route = useRoute()
  const { notify, notifyError, setLang, setTheme } = useUIStore.getState()
  const lang = useUIStore((s) => s.lang)
  const theme = useUIStore((s) => s.theme)
  const { logo: brandLogo } = useBrand()

  // 当前页签记在地址里(?tab=),刷新、分享链接都回到同一页。
  const [activeTab, setActiveTab] = useState(() => {
    const initial = String(route.query.tab || '')
    return allTabs.some((tb) => tb.id === initial) ? initial : 'gateway'
  })
  useUpdateEffect(() => {
    if (route.query.tab !== activeTab) void nav.replace({ query: { ...route.query, tab: activeTab } })
  }, [activeTab])
  // 已在本页时改地址(浏览器后退、点站内链接)不会重建组件,要跟着地址切页签。
  useUpdateEffect(() => {
    const q = route.query.tab
    if (typeof q === 'string' && allTabs.some((tb) => tb.id === q)) setActiveTab(q)
  }, [route.query.tab])
  const showSave = !allTabs.find((tb) => tb.id === activeTab)?.own
  /** 面板一直挂着、只切显隐:切走再切回来,没保存的改动还在。 */
  const showOn = (id: string) => (activeTab === id ? undefined : { display: 'none' })

  // per-user MFA enrollment (distinct from the requireMFA policy toggle)
  const [mfaModalOpen, setMfaModalOpen] = useState(false)
  const mfaBound = useAuthStore((s) => s.me?.mfaEnabled ?? false)
  async function onMfaChanged() {
    try { await useAuthStore.getState().fetchMe() } catch { /* ignore */ }
  }

  const [f, setF, fRef] = useStateRef<Form>(INITIAL)
  const patch = (p: Partial<Form>) => setF((v) => ({ ...v, ...p }))
  const setter = <K extends keyof Form>(k: K) => (v: Form[K]) => patch({ [k]: v } as Pick<Form, K>)

  // 部署配置里拉了 SSO 总闸:这里存什么都不生效,所有人走口令登录。
  const [ssoForcedOff, setSsoForcedOff] = useState(false)
  const [ipModalOpen, setIpModalOpen] = useState(false)
  // 管理后台 IP 白名单(工单 08):只作用于管理接口;列表为空不限制,本机始终放行。
  const [adminIpEnabled, setAdminIpEnabled] = useState(false)
  const [adminIpList, setAdminIpList] = useState('')
  const [adminIpModalOpen, setAdminIpModalOpen] = useState(false)
  // 部署配置拉了总闸(AEGISDB_ADMIN_IP_ALLOWLIST_DISABLED):这里存什么都不生效
  const [adminIpForcedOff, setAdminIpForcedOff] = useState(false)
  // 两个白名单都是**先存、存成了再改界面**。从前反过来:先把徽标改成「已启用」、关掉弹窗,再去存,
  // 存失败也不报 —— 页面说白名单开着,其实一条都没生效(2026-10 审查)。失败时弹窗留着,改动不丢。
  async function onAdminIpSave(payload: { enabled: boolean; list: string }) {
    try {
      await api.saveSettings({ 'security.adminIpAllowEnabled': payload.enabled, 'security.adminIpAllowlist': payload.list })
    } catch (e) {
      notifyError(e, t('saveFailed'))
      return
    }
    setAdminIpEnabled(payload.enabled)
    setAdminIpList(payload.list)
    setAdminIpModalOpen(false)
    notify(t('settingsSaved'), 'success')
  }
  async function onIpSave(payload: { enabled: boolean; list: string }) {
    try {
      await api.saveSettings({ 'security.ipAllowEnabled': payload.enabled, 'security.ipAllowlist': payload.list })
    } catch (e) {
      notifyError(e, t('saveFailed'))
      return
    }
    patch({ ipAllowEnabled: payload.enabled, ipAllow: payload.list })
    setIpModalOpen(false)
    notify(t('settingsSaved'), 'success')
  }

  const [hasLarkSecret, setHasLarkSecret] = useState(false)
  const [hasExtToken, setHasExtToken] = useState(false)
  const [hasExtCallbackSecret, setHasExtCallbackSecret] = useState(false)
  const [larkTest, setLarkTest, larkTestRef] = useStateRef<{ state: 'idle' | 'sending' | 'ok' | 'err'; msg: string }>({ state: 'idle', msg: '' })
  async function testLark() {
    if (larkTestRef.current.state === 'sending') return
    setLarkTest({ state: 'sending', msg: '' })
    try {
      await api.saveSettings({ 'notify.larkWebhook': f.larkWebhook.trim(), 'notify.larkSecret': f.larkSecret.trim(), 'notify.consoleURL': f.consoleURL.trim() })
      const r = await api.testLark()
      setLarkTest({ state: r.ok ? 'ok' : 'err', msg: r.message })
    } catch { setLarkTest({ state: 'err', msg: t('larkTestFail') }) }
  }
  // 清除一个已配置的密钥(工单 02 第 8 条):后端约定 null = 清除,"" = 不改。即点即存,不等底部的保存 ——
  // 清除是一个明确的动作,与"这次没填"是两回事。
  async function clearSecret(key: 'notify.larkSecret' | 'approval.external.token' | 'approval.external.callbackSecret') {
    if (!(await confirmAction(t('secretClearConfirm')))) return
    try {
      await api.saveSettings({ [key]: null })
      if (key === 'notify.larkSecret') { setHasLarkSecret(false); patch({ larkSecret: '' }) }
      else if (key === 'approval.external.token') { setHasExtToken(false); patch({ extToken: '' }) }
      else { setHasExtCallbackSecret(false); patch({ extCallbackSecret: '' }) }
      notify(t('secretCleared'), 'success')
    } catch (e) {
      notifyError(e, t('saveFailed'))
    }
  }
  // The callback URL to register with the vendor. The vendor authenticates by
  // sending the callback secret as `Authorization: Bearer <secret>` (configured on
  // their side); the URL itself carries no secret.
  const extCallbackBase = (f.extCallbackBaseURL ?? '').trim().replace(/\/+$/, '')
  const extCallbackURL = extCallbackBase ? `${extCallbackBase}/api/v1/approvals/lark/callback` : ''
  const [saved, setSaved] = useState(false)
  const [approvers, setApprovers] = useState<Member[]>([])

  const [baseline, setBaseline] = useState('')
  const takeBaseline = () => setBaseline(dirtyView(fRef.current))
  useUnsavedFlag(!!baseline && dirtyView(f) !== baseline)

  useMountEffect(() => {
    void (async () => {
      try {
        const s = await api.settings()
        setF(formOf(s.settings || {}))
        setHasLarkSecret(!!s.secretsSet?.['notify.larkSecret'])
        setAdminIpEnabled(parse<boolean>(s.settings?.['security.adminIpAllowEnabled'], false))
        setAdminIpList(parse<string>(s.settings?.['security.adminIpAllowlist'], ''))
        setAdminIpForcedOff(!!(s as any).adminIpAllowlistForcedOff)
        setHasExtToken(!!s.secretsSet?.['approval.external.token'])
        setHasExtCallbackSecret(!!s.secretsSet?.['approval.external.callbackSecret'])
        api.ssoConfig().then((c) => { setSsoForcedOff(!!c.forcedOff) }).catch(() => { /* 读不到就不提示 */ })
      } catch { /* ignore */ }
      takeBaseline()
      try { setApprovers((await api.approvalChain()).chain ?? []) } catch { /* ignore */ }
    })()
  })

  async function save() {
    try {
      await api.saveSettings(settingsPayload(fRef.current))
      takeBaseline()
      setSaved(true)
      setTimeout(() => setSaved(false), 2200)
      notify(t('settingsSaved'), 'success')
    } catch (e) {
      notifyError(e, t('saveFailed')) // R28: no more silent failure
    }
  }

  // 表单字段 → 控件的绑定
  const n = (k: KeysOf<Num>) => ({ value: f[k] as Num, onChange: setter(k) as (v: Num) => void })
  const sw = (k: KeysOf<boolean>) => ({ value: f[k] as boolean, onChange: setter(k) as (v: boolean) => void })
  const tx = (k: KeysOf<string>) => ({ value: (f[k] as string) ?? '', onChange: (e: { target: { value: string } }) => setter(k)(e.target.value) })

  return (
    <div className="scy page">
      {/* Tab bar: switch between setting groups instead of one long scroll.
          刻意放在 .col 外面 —— 见 SettingsView.scoped.css 里 .tabs 的注释。 */}
      <div className="v-head">
        <div className="v-head-l">
          <div className="v-eyebrow">SETTINGS</div>
        </div>
      </div>
      <div className="layout">
        <nav className="tabs">
          {tabGroups.map((g) => (
            <TabGroup key={g.label} label={g.label} tabs={g.tabs} active={activeTab} onPick={setActiveTab} />
          ))}
          {/* About */}
          <div className="about">
            <div className={clsx('alogo', !!brandLogo && 'custom')}><BrandMark size={15} /></div>
            <div><div className="an">{brandText(lang)}</div><div className="av">{t('verLabel')} 2.4.1</div><div className="av">{t('buildLabel')} 20260624</div></div>
          </div>
        </nav>
        <div className="col">

          {/* Gateway */}
          <section style={showOn('gateway')} className="card">
            <Head icon={Shield} title="setGw" desc="setGwSub" />
            {/* 无 WHERE 的 DELETE / UPDATE 按分层开关(迁移 0030),开关在【环境分层】页 ——
                判定的另外两层也在那套按分层的规则里,不该有第二个能改同一件事的地方。 */}
            <Row title="setStrict" desc="setStrictD"><Link className="srlink" to="/admin/env-tiers">{t('pStrictGo')}</Link></Row>
            <Row title="setTimeout" desc="setTimeoutD"><div className="w160"><NumInput {...n('execTimeout')} min="1" max="3600" /></div></Row>
            <Row title="setPageSize" desc="setPageSizeD"><div className="w160"><NumInput {...n('resultPageSize')} min="100" max="50000" data-testid="result-page-size" /></div></Row>
            <Row title="setApprovalTimeout" desc="setApprovalTimeoutD"><div className="w160"><NumInput {...n('approvalExecTimeout')} min="1" max="86400" data-testid="set-approval-timeout" /></div></Row>
            <Row title="setOscMinRows" desc="setOscMinRowsD"><div className="w160"><NumInput {...n('oscMinRows')} min="1" data-testid="set-osc-rows" /></div></Row>
            <Row title="setOscMinGB" desc="setOscMinGBD"><div className="w160"><NumInput {...n('oscMinGB')} min="0.01" step="0.1" data-testid="set-osc-gb" /></div></Row>
            <Row title="setOscThreads" desc="setOscThreadsD"><div className="w160"><NumInput {...n('oscMaxThreads')} min="0" data-testid="set-osc-threads" /></div></Row>
            <Row title="setOscLag" desc="setOscLagD"><div className="w160"><NumInput {...n('oscMaxLag')} min="0" data-testid="set-osc-lag" /></div></Row>
            <Row title="setOscChunk" desc="setOscChunkD"><div className="w160"><NumInput {...n('oscChunk')} min="1" data-testid="set-osc-chunk" /></div></Row>
            <Row title="setScriptPath" desc="setScriptPathD"><input {...tx('scriptPath')} className="pathinput" placeholder={t('setScriptPathPlaceholder')} /></Row>
            <Row title="setInspectionPath" desc="setInspectionPathD"><input {...tx('inspectionPath')} className="pathinput" data-testid="inspection-path" placeholder={t('setInspectionPathPlaceholder')} /></Row>
            <Row title="setExportPath" desc="setExportPathD"><input {...tx('exportPath')} className="pathinput" placeholder={t('setExportPathPlaceholder')} /></Row>
            <Row title="setExportMaxRows" desc="setExportMaxRowsD"><div className="w160"><NumInput {...n('exportMaxRows')} min="0" /></div></Row>
            <Row title="setExportMaxBytes" desc="setExportMaxBytesD"><div className="w160"><NumInput {...n('exportMaxBytes')} min="0" /></div></Row>
            <Row title="setExportTimeout" desc="setExportTimeoutD"><div className="w160"><NumInput {...n('exportTimeout')} min="1" /></div></Row>
            <Row title="setExportRetention" desc="setExportRetentionD" last><div className="w160"><NumInput {...n('exportRetention')} min="0" /></div></Row>
          </section>

          {/* 业务方数据查询 */}
          <section style={showOn('bizquery')} className="card" data-testid="biz-settings">
            <Head icon={SearchCode} title="setBiz" desc="setBizSub" />
            <Row title="setBizRows" desc="setBizRowsD">
              <div className="pair"><NumInput {...n('bizDefaultMaxRows')} min="1" data-testid="biz-default-rows" /><span>/</span><NumInput {...n('bizMaxRowsCap')} min="1" max="100000" /></div></Row>
            <Row title="setBizTimeout" desc="setBizTimeoutD">
              <div className="pair"><NumInput {...n('bizDefaultTimeoutSec')} min="1" /><span>/</span><NumInput {...n('bizTimeoutCapSec')} min="1" max="3600" /></div></Row>
            <Row title="setBizDays" desc="setBizDaysD"><div className="w160"><NumInput {...n('bizMaxGrantDays')} min="1" max="365" /></div></Row>
            <Row title="setBizConc" desc="setBizConcD">
              <div className="pair"><NumInput {...n('bizMaxConcurrentPerUser')} min="1" max="20" /><span>/</span><NumInput {...n('bizMaxConcurrentPerSource')} min="1" max="100" /></div></Row>
            <Row title="setBizDaily" desc="setBizDailyD"><div className="w160"><NumInput {...n('bizMaxDailyPerUser')} min="0" max="100000" data-testid="biz-daily" /></div></Row>
            <Row title="setBizExport" desc="setBizExportD"><VSwitch {...sw('bizExportEnabled')} data-testid="biz-export-switch" /></Row>
            <Row title="setBizApply" desc="setBizApplyD" last>
              <div className="wurl"><input {...tx('bizApplyUrl')} className="lkin" placeholder="https://…" data-testid="biz-apply-url" /></div></Row>
          </section>

          {/* 元数据同步 */}
          <section style={showOn('meta')} className="card">
            <Head icon={DatabaseZap} title="setMeta" desc="setMetaSub" />
            <Row title="setMetaEnabled" desc="setMetaEnabledD"><VSwitch {...sw('metaEnabled')} /></Row>
            {f.metaEnabled && <Row title="setMetaInterval" desc="setMetaIntervalD"><div className="w160"><NumInput {...n('metaIntervalHrs')} min="1" max="720" /></div></Row>}
            {f.metaEnabled && <Row title="setMetaConcurrency" desc="setMetaConcurrencyD"><div className="w160"><NumInput {...n('metaConcurrency')} min="1" max="8" /></div></Row>}
            {/* 连通性巡检。和元数据同步放在一页,是因为它们回答的是同一个问题:
                这台网关会不会主动去连你的库。 */}
            <Row title="setConnCheck" desc="setConnCheckD"><VSwitch {...sw('connCheckEnabled')} /></Row>
            {f.connCheckEnabled && <Row title="setConnCheckInterval" desc="setConnCheckIntervalD"><div className="w160"><NumInput {...n('connCheckInterval')} min="1" max="1440" /></div></Row>}
            {f.connCheckEnabled && <Row title="setConnCheckConcurrency" desc="setConnCheckConcurrencyD"><div className="w160"><NumInput {...n('connCheckConcurrency')} min="1" max="16" /></div></Row>}
            {f.connCheckEnabled && <Row title="setConnCheckSkip" desc="setConnCheckSkipD" />}

            {/* 这三句不是客套话,是这个功能真实的三处行为。少说任何一句,人都会等在
                一个不会发生的事情上,或者以为某台实例是空的。 */}
            <div className="srow last"><div className="grow"><div className="rt">{t('setMetaNote')}</div>
              <div className="rd">{t('setMetaNoteFirst')}</div>
              <div className="rd">{t('setMetaNoteSkip')}</div>
              <div className="rd">{t('setMetaNoteStale')}</div>
            </div><Link className="srlink" to="/admin/connections">{t('setMetaGo')}</Link></div>
          </section>

          {/* Approval */}
          <section style={showOn('approval')} className="card">
            <Head icon={GitPullRequestArrow} title="setAppr" desc="setApprSub" />
            <Row title="setApprTimeout" desc="setApprTimeoutD"><div className="w180"><VSelect value={labelOf(f.apprTimeout, t)} options={APPROVAL_TIMEOUT_KEYS.map((k) => labelOf(k, t))} onChange={(l) => patch({ apprTimeout: keyForLabel(l, APPROVAL_TIMEOUT_KEYS, t, 'auto-escalate') })} /></div></Row>
            <Row title="setDefApprovers" desc="setDefApproversD"><div className="approvers">{approvers.map((a) => <span key={a.id} className="apv"><span className="ava">{a.initials}</span>{a.name}</span>)}{!approvers.length && <span className="apv-empty">{t('setApproversEmpty')}</span>}<a className="apv-manage" onClick={goPerms}>{t('setApproversManage')}</a></div></Row>
            <Row title="setSelfApprove" desc="setSelfApproveD"><VSwitch {...sw('allowSelf')} /></Row>
            {/* External 飞书审批(审批魔方) */}
            <Row title="setExtAppr" desc="setExtApprD" last={!f.extEnabled}><VSwitch {...sw('extEnabled')} /></Row>
            {f.extEnabled && <Row title="setExtRelease" desc="setExtReleaseD" data-testid="ext-release-row"><VSwitch {...sw('extRelease')} /></Row>}
            {f.extEnabled && (
              <div className="larkcfg">
                <div className="lkrow"><label className="lkl">{t('extBaseURL')}</label><input {...tx('extBaseURL')} className="lkin" placeholder="https://approval.example.com" /></div>
                <div className="lkrow"><label className="lkl">{t('extToken')}</label><input {...tx('extToken')} type="password" className="lkin" placeholder={hasExtToken ? t('secretConfigured') : t('extTokenPh')} />{hasExtToken && <VButton height="36px" onClick={() => clearSecret('approval.external.token')}>{t('secretClear')}</VButton>}</div>
                <div className="lkrow"><label className="lkl">{t('extAiGroup')}</label><input {...tx('extAiGroup')} className="lkin" placeholder="K8S_AI" /></div>
                <div className="lkrow"><label className="lkl">{t('extCallbackBase')}</label><input {...tx('extCallbackBaseURL')} className="lkin" placeholder="https://gw.corp.io" /></div>
                {extCallbackURL && <div className="lkhint">{t('extCallbackFull')}<span className="mono">{extCallbackURL}</span></div>}
                <div className="lkrow"><label className="lkl">{t('extCallbackSecret')}</label><input {...tx('extCallbackSecret')} type="password" className="lkin" placeholder={hasExtCallbackSecret ? t('secretConfigured') : t('extCallbackSecretPh')} />{hasExtCallbackSecret && <VButton height="36px" onClick={() => clearSecret('approval.external.callbackSecret')}>{t('secretClear')}</VButton>}</div>
                <div className="lkrow"><label className="lkl">{t('extAllowIPs')}</label><input {...tx('extAllowIPs')} className="lkin" placeholder={t('extAllowIPsPh')} /></div>
              </div>
            )}
          </section>

          {/* Security */}
          <section style={showOn('security')} className="card">
            <Head icon={Lock} title="setSec" desc="setSecSub" />
            <Row title="setTtl" desc="setTtlD"><div className="w160"><VSelect value={labelOf(f.ttl, t)} options={SESSION_TTL_KEYS.map((k) => labelOf(k, t))} onChange={(l) => patch({ ttl: keyForLabel(l, SESSION_TTL_KEYS, t, '8h') })} /></div></Row>
            <Row title="setIdle" desc="setIdleD"><VSwitch {...sw('idle')} /></Row>
            {f.idle && <Row title="setIdleMinutes" desc="setIdleMinutesDesc"><div className="w160"><NumInput {...n('idleMinutes')} min="1" max="1440" /></div></Row>}
            <Row title="setAdminIdle" desc="setAdminIdleD"><div className="w160"><NumInput {...n('adminIdleMinutes')} min="1" max="1440" data-testid="admin-idle-minutes" /></div></Row>
            <Row title="setMfa" desc="setMfaD"><VSwitch {...sw('mfa')} /></Row>
            <Row title="setMfaEnroll" desc="setMfaEnrollD">
              <div className="ipwrap">
                <span className={clsx('badge', mfaBound ? 'on' : 'off')}><KeyRound size={12} />{mfaBound ? t('mfaBound') : t('mfaUnbound')}</span>
                <VButton variant="secondary" height="34px" onClick={() => setMfaModalOpen(true)}>{mfaBound ? t('manage') : t('mfaBind')}</VButton>
              </div>
            </Row>
            <Row title="setIpAllow" desc="setIpAllowD">
              <div className="ipwrap">
                <span className={clsx('badge', f.ipAllowEnabled ? 'on' : 'off')}>{f.ipAllowEnabled ? t('enabledTag') : t('disabledTag')}</span>
                <span className="mono">{f.ipAllow}</span>
                <VButton variant="secondary" height="34px" onClick={() => setIpModalOpen(true)}>{t('manage')}</VButton>
              </div>
            </Row>
            <Row title="setAdminIpAllow" desc="setAdminIpAllowD">
              <div className="ipwrap">
                <span className={clsx('badge', adminIpEnabled ? 'on' : 'off')} data-testid="admin-ip-badge">{adminIpEnabled ? t('enabledTag') : t('disabledTag')}</span>
                <span className="mono">{adminIpList}</span>
                <VButton variant="secondary" height="34px" data-testid="admin-ip-manage" onClick={() => setAdminIpModalOpen(true)}>{t('manage')}</VButton>
              </div>
            </Row>
            {adminIpForcedOff && <div className="ssowarn" data-testid="admin-ip-forced-off">{t('setAdminIpForcedOff')}</div>}
            {/* 公司 SSO:只做认证,授权仍由角色 / 菜单 / 能力矩阵决定 */}
            <Row title="setSso" desc="setSsoD" last={!f.ssoEnabled}><VSwitch {...sw('ssoEnabled')} data-testid="sso-enabled" /></Row>
            {ssoForcedOff && <div className="ssowarn" data-testid="sso-forced-off">{t('setSsoForcedOff')}</div>}
            {f.ssoEnabled && (
              <div className="larkcfg">
                <div className="lkrow"><label className="lkl">{t('setSsoBase')}</label><input {...tx('ssoBaseUrl')} className="lkin" placeholder="https://srd.bssrvc66.com" data-testid="sso-base" /></div>
                <div className="lkrow"><label className="lkl">{t('setSsoCookie')}</label><input {...tx('ssoCookie')} className="lkin" placeholder="CS-Token" /></div>
                <div className="ssowarn">{t('setSsoBindHint')}</div>
              </div>
            )}
          </section>

          {/* Notifications */}
          <section style={showOn('notify')} className="card">
            <Head icon={Bell} title="setNotify" desc="setNotifySub" />
            <Row title="setLark" desc="setLarkD" last={!f.lark}><VSwitch {...sw('lark')} /></Row>
            {f.lark && (
              <div className="larkcfg">
                <div className="lkrow"><label className="lkl">{t('larkWebhook')}</label><input {...tx('larkWebhook')} className="lkin" placeholder="https://open.feishu.cn/open-apis/bot/v2/hook/xxxx" /></div>
                <div className="lkrow"><label className="lkl">{t('larkSecret')}</label><input {...tx('larkSecret')} type="password" className="lkin" placeholder={hasLarkSecret ? t('secretConfiguredPh') : t('larkSecretPh')} />{hasLarkSecret && <VButton height="36px" onClick={() => clearSecret('notify.larkSecret')}>{t('secretClear')}</VButton>}</div>
                <div className="lkrow"><label className="lkl">{t('larkConsole')}</label><input {...tx('consoleURL')} className="lkin" placeholder="https://gw.corp.io" /></div>
                <div className="lkfoot">
                  <span className={clsx('lkmsg', larkTest.state)}>{larkTest.state === 'sending' ? t('larkSending') : larkTest.msg}</span>
                  <VButton variant="secondary" height="34px" disabled={!f.larkWebhook?.trim()} onClick={testLark}>{t('larkSendTest')}</VButton>
                </div>
              </div>
            )}
          </section>

          {/* Webhook (audit event forwarding) */}
          <WebhookPanel style={showOn('webhook')} />

          {/* 敏感字段:规则在这里维护,脱敏在服务端做 */}
          <SensitiveColumnsPanel style={showOn('sensitive')} />

          {/* 项目的增删改:管理接口,只在管理后台做;用户后台的项目页只读 */}
          <ProjectsPanel style={showOn('projects')} />
          {activeTab === 'brand' && <BrandPanel />}

          {/* 开放接口凭据 */}
          <ApiClientsPanel style={showOn('openapi')} />

          {/* Appearance */}
          <section style={showOn('appearance')} className="card">
            <Head icon={Palette} title="setAppearance" desc="setAppearanceSub" />
            <Row title="setLangRow"><div className="seg"><div className={clsx('si', lang === 'zh' && 'active')} onClick={() => setLang('zh')}>中文</div><div className={clsx('si', lang === 'en' && 'active')} onClick={() => setLang('en')}>English</div></div></Row>
            <Row title="setThemeRow" last><div className="seg"><div className={clsx('si', theme === 'dark' && 'active')} onClick={() => setTheme('dark')}>{t('themeDark')}</div><div className={clsx('si', theme === 'light' && 'active')} onClick={() => setTheme('light')}>{t('themeLight')}</div><div className={clsx('si', theme === 'system' && 'active')} onClick={() => setTheme('system')}>{t('themeSystem')}</div></div></Row>
          </section>

          {/* 吸底保存栏:网关、审批、安全、通知、数据查询等页的改动一起保存 */}
          {showSave && (
            <div className="savebar">
              <div className="grow sbhint">{t('setSaveHint')}</div>
              {saved && <span className="savedtag">{t('saved')}</span>}
              <VButton variant="primary" onClick={save}>{t('saveSettings')}</VButton>
            </div>
          )}
        </div>
      </div>

      <MfaModal open={mfaModalOpen} enabled={mfaBound} onClose={() => setMfaModalOpen(false)} onChanged={onMfaChanged} />
      <IpAllowlistModal open={ipModalOpen} enabled={f.ipAllowEnabled} list={f.ipAllow} onClose={() => setIpModalOpen(false)} onSave={onIpSave} />
      <IpAllowlistModal open={adminIpModalOpen} enabled={adminIpEnabled} list={adminIpList} titleKey="adminIpModalTitle" subKey="adminIpModalSub"
        onClose={() => setAdminIpModalOpen(false)} onSave={onAdminIpSave} />
    </div>
  )
}

function TabGroup({ label, tabs, active, onPick }: { label: string; tabs: Tab[]; active: string; onPick: (id: string) => void }) {
  const { t } = useI18n()
  return (
    <>
      <div className="tgroup">{t(label)}</div>
      {tabs.map(({ id, icon: Icon, label: text }) => (
        <button key={id} className={clsx('tab', active === id && 'active')} onClick={() => onPick(id)}>
          <Icon size={15} /><span>{t(text)}</span>
        </button>
      ))}
    </>
  )
}
