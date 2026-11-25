import { useEffect, useState, type ComponentType, type CSSProperties, type ReactNode } from 'react'
import clsx from 'clsx'
import { Bot } from 'lucide-react'
import VButton from '@/components/common/VButton'
import VSwitch from '@/components/common/VSwitch'
import VSelect from '@/components/common/VSelect'
import api from '@/api'
import { localizeText } from '@/api/http'
import { CHAT_LANG_KEYS, CHAT_REGION_KEYS, CHAT_SECRETS, keyForLabel, labelOf, type ChatSecretKey } from '@/lib/settingOptions'
import { useI18n } from '@/hooks/useI18n'
import { useStateRef } from '@/hooks/useStateRef'
import type { ChatChannel, ChatStatus } from '@/types'
import type { SettingsForm } from './SettingsView'
import './SettingsView.scoped.css'

// 设置 › 审批里的「审批机器人」。表单是设置页那一份,走底部的「保存设置」;状态与测试卡片是即时的,
// 用的是**已保存**的配置。

const POLL_MS = 5000
// 保存之后后端是异步换连接的:马上取一次,再补一次,不等下一轮。
const SETTLE_MS = 1500

interface FieldsProps {
  f: SettingsForm
  patch: (p: Partial<SettingsForm>) => void
  /** 哪些机密已经配置过 */
  secretsSet: Record<string, boolean>
  onClearSecret: (key: ChatSecretKey) => void
}

interface Props extends FieldsProps {
  style?: CSSProperties
  /** 这一页签正开着:只有这时才问状态 */
  active: boolean
  /** 存成过几次,变了就马上重取状态 */
  saves: number
}

/** 各渠道:开关对应的表单字段,与展开后它自己的那几项。其余(说明、状态、测试卡片)每个渠道一样,见 Channel。 */
const CHANNELS: { channel: ChatChannel; on: 'chatLarkEnabled' | 'chatTelegramEnabled'; Fields: ComponentType<FieldsProps> }[] = [
  { channel: 'lark', on: 'chatLarkEnabled', Fields: LarkFields },
  { channel: 'telegram', on: 'chatTelegramEnabled', Fields: TelegramFields },
]

export default function ChatChannelsPanel({ style, active, saves, ...fields }: Props) {
  const { t } = useI18n()
  const { f, patch } = fields
  const status = useChatStatus(active, saves)
  const langLabel = (k: string) => labelOf(k, t)

  return (
    <section style={style} className="card" data-testid="chat-panel">
      <div className="shead"><div className="sic"><Bot size={17} color="var(--accent-text)" /></div><div><div className="st">{t('chatBindTitle')}</div><div className="ss">{t('chatPanelSub')}</div></div></div>

      {/* 对每个渠道都算数的,排在各渠道的开关前面,只说一遍。两句提醒不等打开就要看得到(PRD §5.1、§5.7) */}
      <div className="srow"><div className="grow">
        <div className="rt">{t('chatCommon')}</div>
        <div className="rd">{t('chatCommonD')}</div>
        <div className="ssowarn">{t('chatNoteIp')}</div>
        <div className="ssowarn">{t('chatNoteLeave')}</div>
      </div></div>
      <div className="srow">
        <div className="grow"><div className="rt">{t('chatCardLang')}</div><div className="rd">{t('chatCardLangD')}</div></div>
        <div className="w180"><VSelect
          data-testid="chat-card-lang" value={langLabel(f.chatCardLang)} options={CHAT_LANG_KEYS.map(langLabel)}
          onChange={(l) => patch({ chatCardLang: keyForLabel(l, CHAT_LANG_KEYS, t, 'zh') })}
        /></div>
      </div>

      {CHANNELS.map(({ channel, on, Fields }, i) => (
        <Channel
          key={channel} channel={channel} last={i === CHANNELS.length - 1} status={status?.[channel]}
          on={f[on]} onToggle={(v) => patch({ [on]: v })}
        >
          <Fields {...fields} />
        </Channel>
      ))}
    </section>
  )
}

function LarkFields(p: FieldsProps) {
  const { t } = useI18n()
  const { f, patch } = p
  const label = (k: string) => labelOf(k, t)
  return (
    <>
      <div className="lkrow"><label className="lkl">{t('chatRegion')}</label><div className="chsel">
        <VSelect
          data-testid="chat-lark-region" height="36px" value={label(f.chatLarkRegion)} options={CHAT_REGION_KEYS.map(label)}
          onChange={(l) => patch({ chatLarkRegion: keyForLabel(l, CHAT_REGION_KEYS, t, 'feishu') })}
        />
      </div></div>
      {f.chatLarkRegion === 'lark' && <div className="ssowarn" data-testid="chat-lark-unverified">{t('chatRegionUnverified')}</div>}
      <div className="lkrow"><label className="lkl">App ID</label><input className="lkin" data-testid="chat-lark-appid" autoComplete="off" spellCheck={false} placeholder="cli_xxxxxxxxxxxx" value={f.chatLarkAppId} onChange={(e) => patch({ chatLarkAppId: e.target.value })} /></div>
      <SecretRow {...p} setting="chat.lark.appSecret" id="chat-lark-secret" label="App Secret" ph={t('chatSecretPh')} />
    </>
  )
}

function TelegramFields(p: FieldsProps) {
  const { t } = useI18n()
  const { f, patch } = p
  return (
    <>
      <SecretRow {...p} setting="chat.telegram.botToken" id="chat-telegram-token" label="Bot Token" ph={t('chatTokenPh')} />
      <SecretRow {...p} setting="chat.telegram.proxyToken" id="chat-telegram-proxy" label={t('chatProxy')} ph={t('chatProxyPh')} />
      <div className="lkrow">
        <div className="grow"><div className="rt">{t('chatSql')}</div><div className="ssowarn">{t('chatSqlD')}</div></div>
        <VSwitch value={f.chatTelegramIncludeSql} onChange={(v) => patch({ chatTelegramIncludeSql: v })} data-testid="chat-telegram-sql" aria-label={t('chatSql')} />
      </div>
    </>
  )
}

/** 一项机密:不回显,留空 = 不改;配置过才有「清除」。 */
function SecretRow({ f, patch, secretsSet, onClearSecret, setting, id, label, ph }: FieldsProps & { setting: ChatSecretKey; id: string; label: string; ph: string }) {
  const { t } = useI18n()
  const field = CHAT_SECRETS[setting]
  const configured = !!secretsSet[setting]
  return (
    <div className="lkrow">
      <label className="lkl">{label}</label>
      <input
        className="lkin" data-testid={id} type="password" autoComplete="new-password"
        placeholder={configured ? t('secretConfiguredPh') : ph}
        value={f[field]} onChange={(e) => patch({ [field]: e.target.value })}
      />
      {configured && <VButton height="36px" data-testid={`${id}-clear`} onClick={() => onClearSecret(setting)}>{t('secretClear')}</VButton>}
    </div>
  )
}

/** 各渠道的状态。只在页签开着、页面在前台时轮询。 */
function useChatStatus(active: boolean, saves: number) {
  const [status, setStatus] = useState<Record<string, ChatStatus> | null>(null)
  useEffect(() => {
    if (!active) return
    let gone = false
    let seq = 0
    async function load() {
      if (document.hidden) return
      const mine = ++seq
      let next: Record<string, ChatStatus> | null = null
      // 读不到就不显示状态:留着上一次的,会让人以为它还连着
      try { next = await api.chatStatus() } catch { /* ignore */ }
      if (!gone && mine === seq) setStatus(next)
    }
    const onVisible = () => { if (!document.hidden) void load() }
    void load()
    const poll = setInterval(load, POLL_MS)
    const settle = setTimeout(load, SETTLE_MS)
    document.addEventListener('visibilitychange', onVisible)
    return () => {
      gone = true
      clearInterval(poll)
      clearTimeout(settle)
      document.removeEventListener('visibilitychange', onVisible)
    }
  }, [active, saves])
  return status
}

/** 一个渠道:开关 + 说明,展开后是它自己的字段,加上每个渠道都一样的状态与测试卡片。 */
function Channel({ channel, last, on, onToggle, status, children }: {
  channel: ChatChannel; last: boolean; on: boolean; onToggle: (v: boolean) => void; status?: ChatStatus; children: ReactNode
}) {
  const { t } = useI18n()
  const name = t('chatCh_' + channel)
  const [test, setTest, testRef] = useStateRef<{ state: 'idle' | 'sending' | 'ok' | 'err'; msg: string }>({ state: 'idle', msg: '' })
  async function sendTest() {
    if (testRef.current.state === 'sending') return
    setTest({ state: 'sending', msg: t('larkSending') })
    try {
      await api.chatTest(channel)
      setTest({ state: 'ok', msg: t('chatTestSent', { channel: name }) })
    } catch (e: any) {
      setTest({ state: 'err', msg: e?.message || t('larkTestFail') })
    }
  }
  const reason = status?.detail ? localizeText(status.detail, status.detailRef) : ''

  return (
    <>
      <div className={clsx('srow', last && !on && 'last')}>
        <div className="grow">
          <div className="rt">{t('chatEnable', { channel: name })}</div>
          <div className="rd">{t('chatDesc_' + channel)}</div>
        </div>
        <VSwitch value={on} onChange={onToggle} data-testid={`chat-${channel}-enabled`} aria-label={t('chatEnable', { channel: name })} />
      </div>
      {on && (
        <div className="larkcfg">
          {children}
          <div className="lkrow">
            <span className="lkl">{t('chatStatus')}</span>
            <span className={clsx('chstate', status?.state)} data-testid={`chat-${channel}-status`}>
              {status ? t('chatSt_' + status.state) : '—'}{reason && <span className="chwhy">{reason}</span>}
            </span>
          </div>
          <div className="rd">{t('chatSaveFirst')}</div>
          <div className="lkfoot">
            <span className={clsx('lkmsg', test.state)} data-testid={`chat-${channel}-msg`}>{test.msg}</span>
            <VButton variant="secondary" height="34px" disabled={test.state === 'sending'} data-testid={`chat-${channel}-test`} onClick={sendTest}>{t('chatTest')}</VButton>
          </div>
        </div>
      )}
    </>
  )
}
