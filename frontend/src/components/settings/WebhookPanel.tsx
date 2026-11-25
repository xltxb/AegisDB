import { useEffect, useRef, useState, type HTMLAttributes } from 'react'
import clsx from 'clsx'
import { Webhook, KeyRound, Check, CircleCheck, Loader, CircleX, type LucideIcon } from 'lucide-react'
import VButton from '@/components/common/VButton'
import VSwitch from '@/components/common/VSwitch'
import api from '@/api'
import { useUIStore } from '@/stores/ui'
import { useI18n } from '@/hooks/useI18n'
import { useEvent } from '@/hooks/useEvent'
import { useMountEffect } from '@/hooks/effects'
import { useStateRef } from '@/hooks/useStateRef'
import type { WebhookConfig, WebhookDelivery, Environment } from '@/types'
import './WebhookPanel.scoped.css'

const EVENT_CLS: Record<string, string> = {
  create: 'accent', delete: 'danger', update: 'warn', read: 'accent', other: 'off',
}

function fmtTime(s: string) { return new Date(s).toLocaleTimeString('en-GB') }

const flip = (list: string[], k: string) => (list.includes(k) ? list.filter((x) => x !== k) : [...list, k])

export default function WebhookPanel({ className, ...rest }: HTMLAttributes<HTMLDivElement>) {
  const { t } = useI18n()
  const { notifyError } = useUIStore.getState()
  const [webhook, setWebhook] = useState<WebhookConfig | null>(null)
  const [whState, setWhState, whStateRef] = useStateRef<'idle' | 'sending' | 'ok'>('idle')
  const [whEnabled, setWhEnabled, whEnabledRef] = useStateRef(true)
  const [whEndpoint, setWhEndpoint, whEndpointRef] = useStateRef('')
  const [whSecret, setWhSecret, whSecretRef] = useStateRef('')
  const [whHasSecret, setWhHasSecret] = useState(false)
  const [whEvents, setWhEvents, whEventsRef] = useStateRef<string[]>(['exec'])
  // 推送的环境:空 = 全部环境。按审计行上那一刻的环境匹配;登录这类不属于任何实例的事件不受限。
  const [whEnvs, setWhEnvs, whEnvsRef] = useStateRef<string[]>([])
  const [environments, setEnvironments] = useState<Environment[]>([])
  // 已选、但环境列表里已经没有的代码(环境改了名 / 被删)也要画出来,否则它在界面上看不见、却一直在过滤
  const known = environments.map((e) => ({ code: e.code as string, label: e.displayName || e.code, tier: e.tierCode as string }))
  const envChips = [...known, ...whEnvs.filter((c) => !known.some((k) => k.code === c)).map((c) => ({ code: c, label: c, tier: '' }))]
  const [whMessage, setWhMessage] = useState('')
  const [showLog, setShowLog, showLogRef] = useStateRef(false)
  const [deliveries, setDeliveries] = useState<WebhookDelivery[]>([])
  const lastDelivery = deliveries[0] || null

  // 能订阅的 = 审计行上**「增删改查」那一列**的取值,由服务端下发(GetSettings.webhookActions)。
  //
  // 按这一维而不是按事件类型:接 SIEM 的人想说的通常是"所有删除都推给我",而被拦下来的 DROP
  // 和执行掉的 DROP 是两个事件类型、同一件事 —— 按类型订阅答不了那句话。
  //
  // 文案与审计页「增删改查」列共用同一组词条(auditAct_*),好让这里勾的那一枚和那一列里显示
  // 的那一枚一眼对得上。颜色也一致:删是危险色,查是主色。
  const [eventKeys, setEventKeys] = useState<string[]>(['create', 'delete', 'update', 'read', 'other'])
  const eventDefs = eventKeys.map((k) => ({ key: k, label: 'auditAct_' + k, cls: EVENT_CLS[k] || 'off' }))

  const retryText = t('whRetryUpTo', { n: webhook?.retryMax ?? 5 })

  // persistWebhook saves and reports success so optimistic toggles can roll back
  // on failure instead of leaving the UI out of sync with the server (R28).
  const persistWebhook = useEvent(async (): Promise<boolean> => {
    try {
      const saved = await api.saveWebhook({
        endpoint: whEndpointRef.current,
        secret: whSecretRef.current.trim(),
        events: whEventsRef.current.join(','),
        envs: whEnvsRef.current.join(','),
        enabled: whEnabledRef.current,
      })
      setWebhook(saved)
      // 保存响应与 GET 一致:不回 secret 明文,只回 hasSecret(工单 02 第 3 条)
      setWhHasSecret(!!(saved as WebhookConfig & { hasSecret?: boolean }).hasSecret)
      setWhSecret('')
      return true
    } catch (e) {
      notifyError(e, t('whSaveFailed'))
      return false
    }
  })
  // 两个输入框改完(失焦 / 回车)才存。React 的 onChange 是逐键的,这里要的是原生 change。
  const endpointEl = useRef<HTMLInputElement>(null)
  const secretEl = useRef<HTMLInputElement>(null)
  useEffect(() => {
    const els = [endpointEl.current, secretEl.current]
    els.forEach((el) => el?.addEventListener('change', persistWebhook))
    return () => els.forEach((el) => el?.removeEventListener('change', persistWebhook))
  }, [persistWebhook])

  async function toggleWh() {
    const next = !whEnabledRef.current
    setWhEnabled(next)
    if (!(await persistWebhook())) setWhEnabled(!next)
  }
  async function toggleEvent(key: string) {
    const before = whEventsRef.current
    setWhEvents(flip(before, key))
    if (!(await persistWebhook())) setWhEvents(before)
  }
  async function toggleEnv(code: string) {
    const before = whEnvsRef.current
    setWhEnvs(flip(before, code))
    if (!(await persistWebhook())) setWhEnvs(before)
  }
  async function allEnvs() {
    const before = whEnvsRef.current
    if (!before.length) return
    setWhEnvs([])
    if (!(await persistWebhook())) setWhEnvs(before)
  }
  async function testWebhook() {
    if (whStateRef.current === 'sending') return
    setWhState('sending')
    try {
      const res = await api.testWebhook()
      setWhMessage(res?.message || '')
      setWhState('ok')
      await loadDeliveries()
    } catch { setWhState('idle') }
  }
  async function toggleLog() {
    setShowLog(!showLogRef.current)
    if (showLogRef.current) await loadDeliveries()
  }
  async function loadDeliveries() {
    const r = await api.webhookDeliveries()
    setDeliveries(Array.isArray(r) ? r : [])
  }

  useMountEffect(() => {
    void (async () => {
      try {
        const s = await api.settings()
        setWebhook(s.webhook)
        // 可订阅的事件清单由服务端下发。拿不到就退回一个保守的默认,而不是留空 —— 留空的话
        // 这一栏一个格子都没有,看起来像"这个功能没了"。
        if (Array.isArray(s.webhookActions) && s.webhookActions.length) {
          setEventKeys(s.webhookActions)
        }
        if (s.webhook) {
          setWhEnabled(s.webhook.enabled)
          setWhEndpoint(s.webhook.endpoint)
          setWhSecret('') // secret is never returned; blank means "keep unchanged" on save
          setWhHasSecret(!!s.webhookHasSecret)
          // 存着什么就显示什么,**不做过滤**。过滤掉一个界面暂时不认得的键,等于下一次保存
          // 就把它从订阅里抹掉 —— 而那是一份别人配过的东西(原先 intercept / approve 正是
          // 这么丢的)。服务端保存时会按白名单归一(handler.normalizeEvents),真正的无效值
          // 在那里就落不下来。
          setWhEvents((s.webhook.events || '').split(',').filter(Boolean))
          setWhEnvs((s.webhook.envs || '').split(',').filter(Boolean))
        }
        try {
          setEnvironments([...(await api.environments())].sort((a, b) => a.sortOrder - b.sortOrder || a.code.localeCompare(b.code)))
        } catch { /* 拿不到环境列表:已选的仍按代码画出来 */ }
        await loadDeliveries()
      } catch { /* ignore */ }
    })()
  })

  function viewState(): { status: string; icon: LucideIcon; color: string; spin: boolean } {
    if (whState === 'sending')
      return { status: t('whSending'), icon: Loader, color: 'var(--accent-text)', spin: true }
    if (whState === 'ok')
      return { status: whMessage || t('whDelivered'), icon: CircleCheck, color: 'var(--success-text)', spin: false }
    const d = lastDelivery
    if (!d)
      return { status: t('whNoDelivery'), icon: Webhook, color: 'var(--text-muted)', spin: false }
    const prefix = t('whLastDelivery')
    return {
      status: `${prefix} ${fmtTime(d.createdAt)} · ${d.status} · ${d.attempts}×`,
      icon: d.success ? CircleCheck : CircleX,
      color: d.success ? 'var(--success-text)' : 'var(--danger-text)',
      spin: false,
    }
  }
  const st = viewState()
  const StIcon = st.icon

  return (
    <div {...rest} className={clsx('wh', className)}>
      <div className="whhead">
        <div className="whic"><Webhook size={18} color="var(--accent-text)" /></div>
        <div className="grow"><div className="wht">{t('whTitle')}</div><div className="whs">{t('whSub')}</div></div>
        <span className="whbadge" style={{ background: whEnabled ? 'var(--success-subtle)' : 'var(--surface-sunken)', color: whEnabled ? 'var(--success-text)' : 'var(--text-muted)' }}><span className="dotc" />{whEnabled ? t('whEnabled') : t('disabled')}</span>
        <VSwitch value={whEnabled} onChange={toggleWh} />
      </div>
      <div className="whgrid">
        <div>
          <div className="fl">{t('whEndpoint')}</div>
          <div className="whbox"><span className="method">POST</span><input ref={endpointEl} className="ep" value={whEndpoint ?? ''} onChange={(e) => setWhEndpoint(e.target.value)} /></div>
        </div>
        <div>
          <div className="fl">{t('whSecret')}</div>
          <div className="whbox"><KeyRound size={14} color="var(--text-faint)" /><input ref={secretEl} className="ep" type="password" value={whSecret} onChange={(e) => setWhSecret(e.target.value)} placeholder={whHasSecret ? t('whSecretConfigured') : t('whSecretPh')} /></div>
        </div>
        <div>
          <div className="fl">{t('whEvents')}<span className="hint">{t('whEventsHint')}</span></div>
          <div className="evs" data-testid="wh-events">
            {eventDefs.map((e) => (
              <span
                key={e.key}
                className={clsx('ev', whEvents.includes(e.key) ? e.cls : 'off')}
                style={{ cursor: 'pointer' }} onClick={() => toggleEvent(e.key)}
              >
                {whEvents.includes(e.key) && <Check size={12} />}{t(e.label)}
              </span>
            ))}
          </div>
        </div>
        <div>
          <div className="fl">{t('whRetry')}</div>
          <div className="retry"><span>{retryText}</span><span className="sep">·</span><span>{t('whRetryDesc2')}</span><span className="sep">·</span><span className="green">{t('whRetryDesc3')}</span></div>
        </div>
        <div className="span2">
          <div className="fl">{t('whEnvs')}<span className="hint">{t('whEnvsHint')}</span></div>
          <div className="evs" data-testid="wh-envs">
            <span className={clsx('ev', whEnvs.length ? 'off' : 'accent')} style={{ cursor: 'pointer' }} data-testid="wh-env-all" onClick={allEnvs}>
              {!whEnvs.length && <Check size={12} />}{t('whEnvsAll')}
            </span>
            {envChips.map((e) => (
              <span
                key={e.code}
                className={clsx('ev', whEnvs.includes(e.code) ? (e.tier === 'prod' ? 'danger' : 'accent') : 'off')}
                style={{ cursor: 'pointer' }} title={e.code} data-env={e.code} onClick={() => toggleEnv(e.code)}
              >
                {whEnvs.includes(e.code) && <Check size={12} />}{e.label}
              </span>
            ))}
          </div>
        </div>
      </div>
      <div className="whfoot">
        <div className="whstatus" style={{ color: st.color }}><StIcon size={14} className={clsx(st.spin && 'spin')} />{st.status}</div>
        <div className="whacts"><VButton variant="secondary" height="36px" onClick={toggleLog}>{t('whViewLog')}</VButton><VButton variant="primary" height="36px" onClick={testWebhook}>{t('whSendTest')}</VButton></div>
      </div>
      {showLog && (
        <div className="whlog">
          {!deliveries.length && <div className="whlog-empty">{t('whLogEmpty')}</div>}
          {deliveries.map((d) => {
            const Icon = d.success ? CircleCheck : CircleX
            return (
              <div key={d.id} className="whlog-row">
                <Icon size={14} color={d.success ? 'var(--success-text)' : 'var(--danger-text)'} />
                <span className="whlog-ev">{d.event}</span>
                <span className="mono mute">{d.status}</span>
                <span className="whlog-att">{t('whLogAttempts', { n: d.attempts })}</span>
                <span className="mono mute whlog-time">{d.createdAt}</span>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
