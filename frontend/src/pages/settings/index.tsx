import { useEffect, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import clsx from 'clsx'
import {
  Bell, DatabaseZap, EyeOff, GitPullRequestArrow, KeyRound, Lock, Palette, Shield, Webhook,
} from 'lucide-react'
import {
  useApprovalChain, useSaveSettings, useSettings, useTestLark, useTestWebhook,
} from '@/hooks/useSettings'
import ApiClientsPanel from '@/components/settings/ApiClientsPanel'
import { invalidIpEntries, normalizeIpAllowlist } from '@/lib/ipAllowlist'
import { parseSetting } from '@/lib/settingOptions'
import { Card, CardHead, CardRow } from '@/components/common/Card'
import { Button } from '@/components/common/Button'
import { Switch } from '@/components/common/Switch'
import { Segmented } from '@/components/common/Segmented'
import { Loading, ErrorState } from '@/components/common/States'
import { useUIStore, type Lang, type Theme } from '@/stores/ui'
import type { SettingsResp } from '@/types'

/**
 * 子导航的八个分区。id 同时是锚点 —— 深链 `/settings#set-security` 能直接落到那一节。
 */
const SECTIONS = [
  { id: 'gateway', icon: Shield, label: 'setGw' },
  { id: 'approval', icon: GitPullRequestArrow, label: 'setAppr' },
  { id: 'security', icon: Lock, label: 'setSec' },
  { id: 'sensitive', icon: EyeOff, label: 'setSens' },
  { id: 'meta', icon: DatabaseZap, label: 'setMeta' },
  { id: 'openapi', icon: KeyRound, label: 'setOpenApi' },
  { id: 'notify', icon: Bell, label: 'setNotify' },
  { id: 'appearance', icon: Palette, label: 'setAppearance' },
] as const

/** `gateway.defaultPolicy` 认的三个值。 */
const POLICY_KEYS = ['strict', 'approve-1', 'audit-only'] as const
/** `approval.onTimeout` 认的三个值。 */
const APPROVAL_TIMEOUT_KEYS = ['auto-reject', 'auto-escalate', 'keep-waiting'] as const
/** `security.sessionTTL` 认的三个值。 */
const SESSION_TTL_KEYS = ['8h', '4h', '24h'] as const

/**
 * 下拉里存的是**键**,不是翻译过的标签。
 *
 * Vue 版一度把当前语言下的标签存进模型,保存时再拿它跟 t() 比对着还原 —— 换一次
 * 语言,模型里就留着上一门语言的文字,比对全部落空,两项设置都悄悄回到默认值:
 * 切过语言的人按一下保存,审批超时被放宽成自动升级、会话时长变成 8h,而界面上什么
 * 也没说(EF11)。这里的 value 始终是键,标签只在渲染时算出来。
 */
const LABEL_OF: Record<string, string> = {
  strict: 'setPolicyStrict', 'approve-1': 'setPolicyApprove1', 'audit-only': 'setPolicyAuditOnly',
  'auto-reject': 'autoReject', 'auto-escalate': 'autoEscalate', 'keep-waiting': 'keepWaiting',
  '4h': 'ttl4', '8h': 'ttl8', '24h': 'ttl24',
}

/** Webhook 只转发审计类事件;命令拦截与审批流转不走它。 */
const WEBHOOK_EVENTS = ['exec', 'login'] as const

/** 服务端存着界面不认识的值时,回落到默认值而不是把它显示出来。 */
function keyOf<T extends string>(value: string, allowed: readonly T[], fallback: T): T {
  return (allowed as readonly string[]).includes(value) ? (value as T) : fallback
}

function clampInt(v: unknown, min: number, max: number, def: number): number {
  const n = Math.round(Number(v))
  if (!Number.isFinite(n)) return def
  return Math.max(min, Math.min(max, n))
}

export default function SettingsPage() {
  const { t } = useTranslation()
  const { data, isLoading, error, refetch } = useSettings()

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>{t('setTitle')}</h1>
          <p>{t('setSub')}</p>
        </div>
      </header>
      {isLoading && <Loading />}
      {error && <ErrorState error={error} retry={() => refetch()} />}
      {/*
        表单在数据到齐之后才挂载,于是每个 useState 的初值就是服务端的值 —— 不需要
        一个把请求结果同步进表单的 effect,也就不会出现"刚打开时显示默认值、半秒后
        跳成真值"的那一下。
      */}
      {data && <SettingsForm data={data} />}
    </div>
  )
}

function SettingsForm({ data }: { data: SettingsResp }) {
  const { t } = useTranslation()
  const g = data.settings ?? {}
  const save = useSaveSettings()
  const notify = useUIStore((s) => s.notify)
  const testLark = useTestLark()
  const testWebhook = useTestWebhook()
  const [active, setActive] = useState<string>(SECTIONS[0].id)

  const [f, setF] = useState(() => ({
    // ---- 网关策略 ----
    policy: keyOf(parseSetting<string>(g['gateway.defaultPolicy'], 'strict'), POLICY_KEYS, 'strict'),
    execTimeout: Number(parseSetting(g['gateway.execTimeout'], 30)) || 30,
    // Vue 版没有这一项的界面控件,但后端一直在读它(service/async_exec.go)。
    // 没有控件意味着只能改库 —— 于是它实际上是个隐藏设置。补上。
    asyncExecTimeout: Number(parseSetting(g['gateway.asyncExecTimeout'], 5400)) || 5400,
    scriptPath: parseSetting<string>(g['script.savePath'], ''),
    exportPath: parseSetting<string>(g['export.savePath'], ''),
    exportMaxRows: Number(parseSetting(g['export.maxRows'], 5_000_000)),
    exportMaxBytes: Number(parseSetting(g['export.maxBytes'], 2_000_000_000)),
    exportTimeout: Number(parseSetting(g['export.execTimeout'], 1800)) || 1800,
    // `|| 3` 会把用户设的 0(永久保留)悄悄改回 3,所以这里不能用它兜底。
    exportRetention: Math.max(0, Number(parseSetting(g['export.retentionDays'], 3))),

    // ---- 审批 ----
    onTimeout: keyOf(parseSetting<string>(g['approval.onTimeout'], 'auto-escalate'), APPROVAL_TIMEOUT_KEYS, 'auto-escalate'),
    escalate: parseSetting(g['approval.escalate'], true),
    allowSelf: parseSetting(g['approval.allowSelfApprove'], false),
    extEnabled: parseSetting(g['approval.external.enabled'], false),
    extBaseURL: parseSetting<string>(g['approval.external.baseURL'], ''),
    extAiGroup: parseSetting<string>(g['approval.external.aiGroup'], ''),
    extCallbackBaseURL: parseSetting<string>(g['approval.external.callbackBaseURL'], ''),
    extAllowIPs: parseSetting<string>(g['approval.external.callbackAllowIPs'], ''),
    // 密钥从不回传。输入框留空 = 保持不变,所以初值只能是空串。
    extToken: '',
    extCallbackSecret: '',

    // ---- 会话与安全 ----
    ttl: keyOf(parseSetting<string>(g['security.sessionTTL'], '8h'), SESSION_TTL_KEYS, '8h'),
    idle: parseSetting(g['security.idleLock'], true),
    idleMinutes: Number(parseSetting(g['security.idleMinutes'], 15)) || 15,
    mfa: parseSetting(g['security.requireMFA'], true),
    // 下面两项后端在用(service 里读得到),Vue 版却没有控件。补上。
    mfaMandatory: parseSetting(g['security.mfaMandatory'], false),
    mfaGrace: Number(parseSetting(g['security.mfaGraceMinutes'], 30)) || 30,
    ipAllowEnabled: parseSetting(g['security.ipAllowEnabled'], false),
    ipAllow: parseSetting<string>(g['security.ipAllowlist'], ''),

    // ---- 元数据同步 ----
    metaEnabled: parseSetting(g['meta.sync.enabled'], false),
    metaIntervalHrs: Number(parseSetting(g['meta.sync.intervalHours'], 24)) || 24,
    metaConcurrency: Number(parseSetting(g['meta.sync.concurrency'], 2)) || 2,

    // ---- 通知 ----
    lark: parseSetting(g['notify.lark'], true),
    email: parseSetting(g['notify.email'], false),
    push: parseSetting(g['notify.push'], true),
    larkWebhook: parseSetting<string>(g['notify.larkWebhook'], ''),
    larkSecret: '',
    consoleURL: parseSetting<string>(g['notify.consoleURL'], ''),

    // ---- Webhook(审计事件转发,独立接口) ----
    whEnabled: data.webhook?.enabled ?? false,
    whEndpoint: data.webhook?.endpoint ?? '',
    whSecret: '',
    whEvents: (data.webhook?.events ?? 'exec').split(',').filter(Boolean),
    whRetryMax: data.webhook?.retryMax ?? 5,
  }))
  type Form = typeof f
  const set = (patch: Partial<Form>) => setF((p) => ({ ...p, ...patch }))

  /**
   * 滚动到哪一节,子导航就高亮哪一节。
   *
   * 只在点击时高亮的话,人手动滚下去之后导航仍指着上面那一节 —— 一个说着假话的
   * 位置指示比没有指示更糟。rootMargin 把判定线压到视口上沿偏下,避免刚露出一角
   * 的下一节就抢走高亮。
   */
  useEffect(() => {
    const seen = new Map<string, boolean>()
    const io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) seen.set(e.target.id, e.isIntersecting)
        const first = SECTIONS.find((s) => seen.get(`set-${s.id}`))
        if (first) setActive(first.id)
      },
      { rootMargin: '-72px 0px -55% 0px' },
    )
    for (const s of SECTIONS) {
      const el = document.getElementById(`set-${s.id}`)
      if (el) io.observe(el)
    }
    return () => io.disconnect()
  }, [])

  const badIps = f.ipAllowEnabled ? invalidIpEntries(f.ipAllow) : []

  function onSave() {
    // 白名单开着却有写错的条目时不提交:存下去等于让人以为它生效了。
    if (badIps.length) {
      notify(t('ipInvalidEntries', { list: badIps.join(', ') }), 'error')
      return
    }
    save.mutate({
      settings: {
        'gateway.defaultPolicy': f.policy,
        'gateway.execTimeout': clampInt(f.execTimeout, 1, 3600, 30),
        'gateway.asyncExecTimeout': clampInt(f.asyncExecTimeout, 60, 86400, 5400),
        'script.savePath': f.scriptPath.trim(),
        'export.savePath': f.exportPath.trim(),
        // 0 = 不限;负数没有意义,归零
        'export.maxRows': Math.max(0, Math.round(Number(f.exportMaxRows) || 0)),
        'export.maxBytes': Math.max(0, Math.round(Number(f.exportMaxBytes) || 0)),
        'export.execTimeout': clampInt(f.exportTimeout, 1, 86400, 1800),
        // 0 = 永久保留,与 maxRows 的 0=不限一致
        'export.retentionDays': Math.max(0, Math.round(Number(f.exportRetention) || 0)),
        'approval.onTimeout': f.onTimeout,
        'approval.escalate': f.escalate,
        'approval.allowSelfApprove': f.allowSelf,
        'approval.external.enabled': f.extEnabled,
        'approval.external.baseURL': f.extBaseURL.trim(),
        // 空 token / 空 secret 表示沿用已存的那一个,服务端据此判断
        'approval.external.token': f.extToken.trim(),
        'approval.external.aiGroup': f.extAiGroup.trim(),
        'approval.external.callbackBaseURL': f.extCallbackBaseURL.trim(),
        'approval.external.callbackSecret': f.extCallbackSecret.trim(),
        'approval.external.callbackAllowIPs': f.extAllowIPs.trim(),
        'security.sessionTTL': f.ttl,
        'security.requireMFA': f.mfa,
        'security.mfaMandatory': f.mfaMandatory,
        'security.mfaGraceMinutes': clampInt(f.mfaGrace, 1, 1440, 30),
        'security.idleLock': f.idle,
        'security.idleMinutes': clampInt(f.idleMinutes, 1, 1440, 15),
        'security.ipAllowlist': normalizeIpAllowlist(f.ipAllow),
        'security.ipAllowEnabled': f.ipAllowEnabled,
        'meta.sync.enabled': f.metaEnabled,
        'meta.sync.intervalHours': clampInt(f.metaIntervalHrs, 1, 720, 24),
        'meta.sync.concurrency': clampInt(f.metaConcurrency, 1, 8, 2),
        'notify.lark': f.lark,
        'notify.email': f.email,
        'notify.push': f.push,
        'notify.larkWebhook': f.larkWebhook.trim(),
        'notify.larkSecret': f.larkSecret.trim(),
        'notify.consoleURL': f.consoleURL.trim(),
      },
      webhook: {
        endpoint: f.whEndpoint.trim(),
        secret: f.whSecret.trim(),
        events: f.whEvents.join(','),
        retryMax: f.whRetryMax,
        enabled: f.whEnabled,
      },
    })
  }

  const callbackURL = f.extCallbackBaseURL.trim().replace(/\/+$/, '')
    ? `${f.extCallbackBaseURL.trim().replace(/\/+$/, '')}/api/v1/approvals/lark/callback`
    : ''

  return (
    <div className="set-wrap">
      <nav className="set-nav">
        {SECTIONS.map((s) => (
          <a
            key={s.id}
            href={`#set-${s.id}`}
            className={clsx('set-nav-item', active === s.id && 'on')}
            onClick={() => setActive(s.id)}
          >
            <s.icon size={15} />{t(s.label)}
          </a>
        ))}
      </nav>

      <div className="set-body">
        {/* ---------------- 网关策略 ---------------- */}
        <section id="set-gateway" className="set-sec"><Card>
          <CardHead icon={<Shield size={17} />} title={t('setGw')} sub={t('setGwSub')} />
          <CardRow title={t('setDefPolicy')} hint={t('setDefPolicyD')}>
            <select
              className="set-in w180"
              value={f.policy}
              onChange={(e) => set({ policy: e.target.value as Form['policy'] })}
            >
              {POLICY_KEYS.map((k) => <option key={k} value={k}>{t(LABEL_OF[k])}</option>)}
            </select>
          </CardRow>
          {/*
            无 WHERE 的 DELETE / UPDATE 按**分层**开关(迁移 0030),开关在环境分层页。
            这里只留一个去处,不放第二个能改同一件事的控件 —— 两个入口改同一条规则,
            迟早会有人对着其中一个说"我明明关了"。
          */}
          <CardRow title={t('setStrict')} hint={t('setStrictD')}>
            <Link className="set-link" to="/env-tiers">{t('setStrictGo')}</Link>
          </CardRow>
          <CardRow title={t('setTimeout')} hint={t('setTimeoutD')}>
            <input className="set-in w160" type="number" min={1} max={3600}
                   value={f.execTimeout} onChange={(e) => set({ execTimeout: Number(e.target.value) })} />
          </CardRow>
          <CardRow title={t('setAsyncTimeout')} hint={t('setAsyncTimeoutD')}>
            <input className="set-in w160" type="number" min={60} max={86400}
                   value={f.asyncExecTimeout} onChange={(e) => set({ asyncExecTimeout: Number(e.target.value) })} />
          </CardRow>
          <CardRow title={t('setScriptPath')} hint={t('setScriptPathD')}>
            <input className="set-in w320" value={f.scriptPath}
                   placeholder={t('setScriptPathPh')} onChange={(e) => set({ scriptPath: e.target.value })} />
          </CardRow>
          <CardRow title={t('setExportPath')} hint={t('setExportPathD')}>
            <input className="set-in w320" value={f.exportPath}
                   placeholder={t('setExportPathPh')} onChange={(e) => set({ exportPath: e.target.value })} />
          </CardRow>
          <CardRow title={t('setExportMaxRows')} hint={t('setExportMaxRowsD')}>
            <input className="set-in w160" type="number" min={0}
                   value={f.exportMaxRows} onChange={(e) => set({ exportMaxRows: Number(e.target.value) })} />
          </CardRow>
          <CardRow title={t('setExportMaxBytes')} hint={t('setExportMaxBytesD')}>
            <input className="set-in w160" type="number" min={0}
                   value={f.exportMaxBytes} onChange={(e) => set({ exportMaxBytes: Number(e.target.value) })} />
          </CardRow>
          <CardRow title={t('setExportTimeout')} hint={t('setExportTimeoutD')}>
            <input className="set-in w160" type="number" min={1}
                   value={f.exportTimeout} onChange={(e) => set({ exportTimeout: Number(e.target.value) })} />
          </CardRow>
          <CardRow title={t('setExportRetention')} hint={t('setExportRetentionD')}>
            <input className="set-in w160" type="number" min={0}
                   value={f.exportRetention} onChange={(e) => set({ exportRetention: Number(e.target.value) })} />
          </CardRow>
        </Card></section>

        {/* ---------------- 审批 ---------------- */}
        <section id="set-approval" className="set-sec"><Card>
          <CardHead icon={<GitPullRequestArrow size={17} />} title={t('setAppr')} sub={t('setApprSub')} />
          <CardRow title={t('setApprTimeout')} hint={t('setApprTimeoutD')}>
            <select className="set-in w180" value={f.onTimeout}
                    onChange={(e) => set({ onTimeout: e.target.value as Form['onTimeout'] })}>
              {APPROVAL_TIMEOUT_KEYS.map((k) => <option key={k} value={k}>{t(LABEL_OF[k])}</option>)}
            </select>
          </CardRow>
          <CardRow title={t('setDefApprovers')} hint={t('setDefApproversD')}>
            <ApprovalChain />
            <Link className="set-link" to="/permissions">{t('setApproversManage')}</Link>
          </CardRow>
          <CardRow title={t('setEscalate')} hint={t('setEscalateD')}>
            <Switch checked={f.escalate} onChange={(v) => set({ escalate: v })} />
          </CardRow>
          <CardRow title={t('setSelfApprove')} hint={t('setSelfApproveD')}>
            <Switch checked={f.allowSelf} onChange={(v) => set({ allowSelf: v })} />
          </CardRow>
          <CardRow title={t('setExtAppr')} hint={t('setExtApprD')}>
            <Switch checked={f.extEnabled} onChange={(v) => set({ extEnabled: v })} />
          </CardRow>
          {f.extEnabled && (
            <div className="set-sub">
              <Field label={t('extBaseURL')}>
                <input className="set-in" value={f.extBaseURL} placeholder="https://approval.example.com"
                       onChange={(e) => set({ extBaseURL: e.target.value })} />
              </Field>
              <Field label={t('extToken')}>
                <input className="set-in" type="password" value={f.extToken}
                       placeholder={data.secretsSet?.['approval.external.token'] ? t('secretConfigured') : t('extTokenPh')}
                       onChange={(e) => set({ extToken: e.target.value })} />
              </Field>
              <Field label={t('extAiGroup')}>
                <input className="set-in" value={f.extAiGroup} placeholder="K8S_AI"
                       onChange={(e) => set({ extAiGroup: e.target.value })} />
              </Field>
              <Field label={t('extCallbackBase')}>
                <input className="set-in" value={f.extCallbackBaseURL} placeholder="https://gw.corp.io"
                       onChange={(e) => set({ extCallbackBaseURL: e.target.value })} />
              </Field>
              {/* 回调地址本身不带密钥;对方用 callbackSecret 作 Bearer 来认证。 */}
              {callbackURL && (
                <div className="set-hint">{t('extCallbackFull')}<code>{callbackURL}</code></div>
              )}
              <Field label={t('extCallbackSecret')}>
                <input className="set-in" type="password" value={f.extCallbackSecret}
                       placeholder={data.secretsSet?.['approval.external.callbackSecret'] ? t('secretConfigured') : t('extCallbackSecretPh')}
                       onChange={(e) => set({ extCallbackSecret: e.target.value })} />
              </Field>
              <Field label={t('extAllowIPs')}>
                <input className="set-in" value={f.extAllowIPs} placeholder={t('extAllowIPsPh')}
                       onChange={(e) => set({ extAllowIPs: e.target.value })} />
              </Field>
            </div>
          )}
        </Card></section>

        {/* ---------------- 会话与安全 ---------------- */}
        <section id="set-security" className="set-sec"><Card>
          <CardHead icon={<Lock size={17} />} title={t('setSec')} sub={t('setSecSub')} />
          <CardRow title={t('setTtl')} hint={t('setTtlD')}>
            <select className="set-in w160" value={f.ttl}
                    onChange={(e) => set({ ttl: e.target.value as Form['ttl'] })}>
              {SESSION_TTL_KEYS.map((k) => <option key={k} value={k}>{t(LABEL_OF[k])}</option>)}
            </select>
          </CardRow>
          <CardRow title={t('setIdle')} hint={t('setIdleD')}>
            <Switch checked={f.idle} onChange={(v) => set({ idle: v })} />
          </CardRow>
          {f.idle && (
            <CardRow title={t('setIdleMinutes')} hint={t('setIdleMinutesD')}>
              <input className="set-in w160" type="number" min={1} max={1440}
                     value={f.idleMinutes} onChange={(e) => set({ idleMinutes: Number(e.target.value) })} />
            </CardRow>
          )}
          <CardRow title={t('setMfa')} hint={t('setMfaD')}>
            <Switch checked={f.mfa} onChange={(v) => set({ mfa: v })} />
          </CardRow>
          {/*
            强制全员绑定:打开它会把**尚未绑定**的人全部挡在生产之外。这是一次投放
            决定,不是一个默认值,所以提示里要把后果说出来,而不是只写"是否强制"。
          */}
          <CardRow title={t('setMfaMandatory')} hint={t('setMfaMandatoryD')}>
            <Switch checked={f.mfaMandatory} onChange={(v) => set({ mfaMandatory: v })} />
          </CardRow>
          <CardRow title={t('setMfaGrace')} hint={t('setMfaGraceD')}>
            <input className="set-in w160" type="number" min={1} max={1440}
                   value={f.mfaGrace} onChange={(e) => set({ mfaGrace: Number(e.target.value) })} />
          </CardRow>
          <CardRow title={t('setIpAllow')} hint={t('setIpAllowD')}>
            <Switch checked={f.ipAllowEnabled} onChange={(v) => set({ ipAllowEnabled: v })} />
          </CardRow>
          {f.ipAllowEnabled && (
            <div className="set-sub">
              <Field label={t('setIpAllowList')}>
                <textarea className={clsx('set-in set-ta', badIps.length && 'is-bad')}
                          value={f.ipAllow} placeholder={t('setIpAllowPh')}
                          onChange={(e) => set({ ipAllow: e.target.value })} />
              </Field>
              {/*
                非法条目当场标红并挡住保存。Vue 版是保存时把它们静默丢掉 —— 输入框里
                那一行还在,人由此以为白名单已经放行了那台机器,直到它被挡在门外。
              */}
              <div className={clsx('set-hint', badIps.length && 'bad')}>
                {badIps.length ? t('ipInvalidEntries', { list: badIps.join(', ') }) : t('setIpAllowHint')}
              </div>
            </div>
          )}
        </Card></section>

        {/* ---------------- 敏感字段(只放入口) ---------------- */}
        <section id="set-sensitive" className="set-sec"><Card>
          <CardHead icon={<EyeOff size={17} />} title={t('setSens')} sub={t('setSensSub')} />
          {/*
            规则维护在数据治理页,这里只给一个去处。
            同一份规则两个编辑入口的代价不是多写一遍界面,而是两处对"哪些列算敏感"
            各有一套判断 —— 而脱敏本身在服务端做,前端从头到尾看不到原值。
          */}
          <CardRow title={t('setSensRules')} hint={t('setSensRulesD')}>
            <Link className="set-link" to="/gov">{t('setSensGo')}</Link>
          </CardRow>
        </Card></section>

        {/* ---------------- 元数据同步 ---------------- */}
        <section id="set-meta" className="set-sec"><Card>
          <CardHead icon={<DatabaseZap size={17} />} title={t('setMeta')} sub={t('setMetaSub')} />
          <CardRow title={t('setMetaEnabled')} hint={t('setMetaEnabledD')}>
            <Switch checked={f.metaEnabled} onChange={(v) => set({ metaEnabled: v })} />
          </CardRow>
          {f.metaEnabled && (
            <>
              <CardRow title={t('setMetaInterval')} hint={t('setMetaIntervalD')}>
                <input className="set-in w160" type="number" min={1} max={720}
                       value={f.metaIntervalHrs} onChange={(e) => set({ metaIntervalHrs: Number(e.target.value) })} />
              </CardRow>
              <CardRow title={t('setMetaConcurrency')} hint={t('setMetaConcurrencyD')}>
                <input className="set-in w160" type="number" min={1} max={8}
                       value={f.metaConcurrency} onChange={(e) => set({ metaConcurrency: Number(e.target.value) })} />
              </CardRow>
            </>
          )}
          {/* 这三句不是客套话,是这个功能真实的三处行为。少说任何一句,人就会等在一件
              不会发生的事情上,或者以为某台实例是空的。 */}
          <CardRow
            title={t('setMetaNote')}
            hint={<>
              <div>{t('setMetaNoteFirst')}</div>
              <div>{t('setMetaNoteSkip')}</div>
              <div>{t('setMetaNoteStale')}</div>
            </>}
          >
            <Link className="set-link" to="/catalog">{t('setMetaGo')}</Link>
          </CardRow>
        </Card></section>

        {/* ---------------- 开放接口 ---------------- */}
        <section id="set-openapi" className="set-sec"><Card>
          <CardHead icon={<KeyRound size={17} />} title={t('setOpenApi')} sub={t('setOpenApiSub')} />
          <ApiClientsPanel />
        </Card></section>

        {/* ---------------- 通知 ---------------- */}
        <section id="set-notify" className="set-sec"><Card>
          <CardHead icon={<Bell size={17} />} title={t('setNotify')} sub={t('setNotifySub')} />
          <CardRow title={t('setLark')} hint={t('setLarkD')}>
            <Switch checked={f.lark} onChange={(v) => set({ lark: v })} />
          </CardRow>
          {f.lark && (
            <div className="set-sub">
              <Field label={t('larkWebhook')}>
                <input className="set-in" value={f.larkWebhook}
                       placeholder="https://open.feishu.cn/open-apis/bot/v2/hook/xxxx"
                       onChange={(e) => set({ larkWebhook: e.target.value })} />
              </Field>
              <Field label={t('larkSecret')}>
                <input className="set-in" type="password" value={f.larkSecret}
                       placeholder={data.secretsSet?.['notify.larkSecret'] ? t('secretConfigured') : t('larkSecretPh')}
                       onChange={(e) => set({ larkSecret: e.target.value })} />
              </Field>
              <Field label={t('larkConsole')}>
                <input className="set-in" value={f.consoleURL} placeholder="https://gw.corp.io"
                       onChange={(e) => set({ consoleURL: e.target.value })} />
              </Field>
              <div className="set-sub-foot">
                <Button
                  disabled={!f.larkWebhook.trim() || testLark.isPending}
                  onClick={() => testLark.mutate({
                    webhook: f.larkWebhook, secret: f.larkSecret, consoleURL: f.consoleURL,
                  })}
                >
                  {testLark.isPending ? t('larkSending') : t('larkSendTest')}
                </Button>
              </div>
            </div>
          )}
          <CardRow title={t('setEmail')} hint={t('setEmailD')}>
            <Switch checked={f.email} onChange={(v) => set({ email: v })} />
          </CardRow>
          <CardRow title={t('setPush')} hint={t('setPushD')}>
            <Switch checked={f.push} onChange={(v) => set({ push: v })} />
          </CardRow>

          {/* 审计事件转发。它有自己的接口,但在界面上跟着同一个保存按钮走。 */}
          <CardRow title={<><Webhook size={14} /> {t('setWebhook')}</>} hint={t('setWebhookD')}>
            <Switch checked={f.whEnabled} onChange={(v) => set({ whEnabled: v })} />
          </CardRow>
          {f.whEnabled && (
            <div className="set-sub">
              <Field label={t('whEndpoint')}>
                <input className="set-in" value={f.whEndpoint} placeholder="https://events.corp.io/hook"
                       onChange={(e) => set({ whEndpoint: e.target.value })} />
              </Field>
              <Field label={t('whSecret')}>
                <input className="set-in" type="password" value={f.whSecret}
                       placeholder={data.webhookHasSecret ? t('secretConfigured') : t('whSecretPh')}
                       onChange={(e) => set({ whSecret: e.target.value })} />
              </Field>
              <Field label={t('whEvents')}>
                <div className="set-chips">
                  {WEBHOOK_EVENTS.map((ev) => (
                    <button
                      key={ev}
                      type="button"
                      className={clsx('set-chip', f.whEvents.includes(ev) && 'on')}
                      onClick={() => set({
                        whEvents: f.whEvents.includes(ev)
                          ? f.whEvents.filter((x) => x !== ev)
                          : [...f.whEvents, ev],
                      })}
                    >
                      {t(ev === 'exec' ? 'whEvExec' : 'whEvLogin')}
                    </button>
                  ))}
                </div>
              </Field>
              <div className="set-sub-foot">
                <span className="set-hint">{t('whRetryUpTo', { n: f.whRetryMax })}</span>
                <Button disabled={testWebhook.isPending} onClick={() => testWebhook.mutate()}>
                  {testWebhook.isPending ? t('larkSending') : t('whSendTest')}
                </Button>
              </div>
            </div>
          )}
        </Card></section>

        {/* ---------------- 外观与语言 ---------------- */}
        <section id="set-appearance" className="set-sec"><Card>
          <CardHead icon={<Palette size={17} />} title={t('setAppearance')} sub={t('setAppearanceSub')} />
          <AppearanceRows />
        </Card></section>

        <div className="set-save">
          <span className="set-hint">{t('setSaveHint')}</span>
          <Button variant="primary" disabled={save.isPending} onClick={onSave}>
            {save.isPending ? t('setSaving') : t('setSave')}
          </Button>
        </div>
      </div>
    </div>
  )
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="set-field">
      <span>{label}</span>
      {children}
    </label>
  )
}

/**
 * 外观与语言**即改即生效**,不进下面那次提交。
 *
 * 它们是这台浏览器上的偏好,不是网关的运行时设置 —— 让人切了主题还得按一下保存,
 * 等于把一个即时反馈硬拖成一次提交。
 */
function AppearanceRows() {
  const { t, i18n } = useTranslation()
  const lang = useUIStore((s) => s.lang)
  const theme = useUIStore((s) => s.theme)
  const setLang = useUIStore((s) => s.setLang)
  const setTheme = useUIStore((s) => s.setTheme)

  return (
    <>
      <CardRow title={t('setLangRow')} hint={t('setLangRowD')}>
        <Segmented<Lang>
          value={lang}
          options={[{ value: 'zh', label: '中文' }, { value: 'en', label: 'English' }]}
          onChange={(v) => { setLang(v); i18n.changeLanguage(v) }}
        />
      </CardRow>
      <CardRow title={t('setThemeRow')} hint={t('setThemeRowD')}>
        <Segmented<Theme>
          value={theme}
          options={[
            { value: 'dark', label: t('themeDark') },
            { value: 'light', label: t('themeLight') },
            { value: 'system', label: t('themeSystem') },
          ]}
          onChange={setTheme}
        />
      </CardRow>
    </>
  )
}

/**
 * 默认审批链上的人。
 *
 * 取不到就**说取不到**,不画一个空列表:空列表会被读成"这条链上没有人",而真正的
 * 原因通常是这个角色还没配人,或者调用者看不到成员名单 —— 两件事要人去做的动作
 * 完全不同。
 */
function ApprovalChain() {
  const { t } = useTranslation()
  const { data, isLoading, error } = useApprovalChain()

  if (isLoading) return <span className="set-hint">{t('loading')}…</span>
  if (error) return <span className="set-hint bad">{t('setApproversUnavailable')}</span>

  const chain = data?.chain ?? []
  if (!chain.length) return <span className="set-hint bad">{t('setApproversEmpty')}</span>

  return (
    <div className="apv-list">
      {chain.map((m) => (
        <span key={m.id} className="apv">
          <span className="apv-ava">{m.initials}</span>{m.name}
        </span>
      ))}
    </div>
  )
}
