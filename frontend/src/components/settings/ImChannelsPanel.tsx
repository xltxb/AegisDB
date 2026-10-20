import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { CardRow } from '@/components/common/Card'
import { Switch } from '@/components/common/Switch'
import { Button } from '@/components/common/Button'
import { useImStatus, useTestImCard, useTestImNotify } from '@/hooks/useIm'
import { parseSetting } from '@/lib/settingOptions'
import type { ImStatus } from '@/api/modules/im'

type FieldDef = {
  key: string
  label: string
  /** 字面的占位符(URL 这类不翻译的) */
  placeholder?: string
  /** 需要翻译的占位符 */
  placeholderKey?: string
  secret?: boolean
  /** 有这项就渲染成下拉:value 存进设置,labelKey 走翻译 */
  options?: { value: string; labelKey: string }[]
  /** 设置里还没有值时的初值(下拉必须有一个合法的初值) */
  defaultValue?: string
}
type ChannelDef = { notify?: FieldDef[]; approval?: FieldDef[] }

/**
 * 所有渠道共用的回调配置。只有 webhook 模式用得到 —— polling / 长连接由网关主动外连,
 * 不需要公网入口。
 */
export const IM_COMMON: FieldDef[] = [
  { key: 'im.callbackBaseURL', label: 'imCallbackBase', placeholder: 'https://gw.corp.io' },
  { key: 'im.callbackAllowIPs', label: 'imCallbackAllowIPs', placeholderKey: 'imCallbackAllowIPsPh' },
]

/**
 * 每个渠道自己的配置项。表单初值、保存载荷、渲染都从这张表派生 —— 接一个新渠道只在
 * 这里加一项,不必再去动设置页。飞书群通知（第 1 期）与交互审批（第 3 期）、Telegram 交互审批（第 2 期）、
 * 钉钉交互审批（第 4 期）、Slack 交互审批（第 5 期）。
 */
export const IM_CHANNELS: Record<string, ChannelDef> = {
  lark: {
    notify: [
      { key: 'im.lark.notifyWebhook', label: 'imLarkWebhook', placeholder: 'https://open.feishu.cn/open-apis/bot/v2/hook/xxxx' },
      { key: 'im.lark.notifySecret', label: 'imLarkSecret', placeholderKey: 'imLarkSecretPh', secret: true },
    ],
    approval: [
      { key: 'im.lark.appID', label: 'imLarkAppID', placeholder: 'cli_xxxxxxxxxxxxxxxx' },
      { key: 'im.lark.appSecret', label: 'imLarkAppSecret', placeholderKey: 'imLarkAppSecretPh', secret: true },
      {
        key: 'im.lark.domain', label: 'imLarkDomain', defaultValue: 'feishu',
        options: [{ value: 'feishu', labelKey: 'imLarkDomainFeishu' }, { value: 'lark', labelKey: 'imLarkDomainLark' }],
      },
      {
        key: 'im.lark.mode', label: 'imLarkMode', defaultValue: 'ws',
        options: [{ value: 'ws', labelKey: 'imLarkModeWs' }, { value: 'webhook', labelKey: 'imLarkModeWebhook' }],
      },
      { key: 'im.lark.encryptKey', label: 'imLarkEncryptKey', placeholderKey: 'imLarkEncryptKeyPh', secret: true },
      { key: 'im.lark.verificationToken', label: 'imLarkVerifyToken', placeholderKey: 'imLarkVerifyTokenPh', secret: true },
      { key: 'im.lark.apiBase', label: 'imLarkApiBase', placeholderKey: 'imLarkApiBasePh' },
    ],
  },
  telegram: {
    approval: [
      { key: 'im.telegram.botToken', label: 'imTgToken', placeholderKey: 'imTgTokenPh', secret: true },
      {
        key: 'im.telegram.mode', label: 'imTgMode', defaultValue: 'polling',
        options: [{ value: 'polling', labelKey: 'imTgModePolling' }, { value: 'webhook', labelKey: 'imTgModeWebhook' }],
      },
      { key: 'im.telegram.webhookSecret', label: 'imTgSecret', placeholderKey: 'imTgSecretPh', secret: true },
      { key: 'im.telegram.apiBase', label: 'imTgApiBase', placeholder: 'https://api.telegram.org' },
    ],
  },
  dingtalk: {
    approval: [
      { key: 'im.dingtalk.clientID', label: 'imDtClientID', placeholderKey: 'imDtClientIDPh' },
      { key: 'im.dingtalk.clientSecret', label: 'imDtClientSecret', placeholderKey: 'imDtClientSecretPh', secret: true },
      { key: 'im.dingtalk.robotCode', label: 'imDtRobotCode', placeholderKey: 'imDtRobotCodePh' },
      { key: 'im.dingtalk.cardTemplateId', label: 'imDtTemplate', placeholderKey: 'imDtTemplatePh' },
      { key: 'im.dingtalk.apiBase', label: 'imDtApiBase', placeholder: 'https://api.dingtalk.com' },
    ],
  },
  slack: {
    approval: [
      { key: 'im.slack.botToken', label: 'imSlBotToken', placeholderKey: 'imSlBotTokenPh', secret: true },
      {
        key: 'im.slack.mode', label: 'imSlMode', defaultValue: 'socket',
        options: [{ value: 'socket', labelKey: 'imSlModeSocket' }, { value: 'webhook', labelKey: 'imSlModeWebhook' }],
      },
      { key: 'im.slack.appToken', label: 'imSlAppToken', placeholderKey: 'imSlAppTokenPh', secret: true },
      { key: 'im.slack.signingSecret', label: 'imSlSigningSecret', placeholderKey: 'imSlSigningSecretPh', secret: true },
      { key: 'im.slack.apiBase', label: 'imSlApiBase', placeholder: 'https://slack.com/api' },
    ],
  },
}

export type ImValues = Record<string, string | boolean>

export function imInitialValues(g: Record<string, string>): ImValues {
  // 密钥从不回传。输入框留空 = 保持不变,所以初值只能是空串。
  const init = (f: FieldDef) => (f.secret ? '' : parseSetting<string>(g[f.key], f.defaultValue ?? ''))
  const v: ImValues = {}
  for (const f of IM_COMMON) v[f.key] = init(f)
  for (const [kind, def] of Object.entries(IM_CHANNELS)) {
    if (def.notify) v[`im.${kind}.notify`] = parseSetting(g[`im.${kind}.notify`], false)
    if (def.approval) v[`im.${kind}.approval`] = parseSetting(g[`im.${kind}.approval`], false)
    for (const f of [...(def.notify ?? []), ...(def.approval ?? [])]) v[f.key] = init(f)
  }
  return v
}

export function imSettingsPayload(v: ImValues): Record<string, string | boolean> {
  const out: Record<string, string | boolean> = {}
  for (const [k, val] of Object.entries(v)) out[k] = typeof val === 'string' ? val.trim() : val
  return out
}

export default function ImChannelsPanel({
  values, onChange, secretsSet,
}: {
  values: ImValues
  onChange: (key: string, v: string | boolean) => void
  secretsSet?: Record<string, boolean>
}) {
  const { t } = useTranslation()
  const { data: status } = useImStatus()
  const testNotify = useTestImNotify()
  const testCard = useTestImCard()
  const settings = () => imSettingsPayload(values)

  // 以服务端登记过的渠道为准:前端认得、服务端却没登记的渠道不显示 —— 显示出来也配不了。
  const rows = (status ?? []).filter((s) => IM_CHANNELS[s.kind])
  return (
    <div className="im-list">
      <div className="im-common">
        {IM_COMMON.map((f) => (
          <FieldInput key={f.key} f={f} values={values} onChange={onChange} secretsSet={secretsSet} />
        ))}
        <div className="set-hint">{t('imCallbackD')}</div>
      </div>
      {rows.map((s) => {
        const def = IM_CHANNELS[s.kind]
        return (
          <div key={s.kind} className="im-ch">
            <div className="im-ch-head">
              <span className="im-ch-name">{s.label}</span>
              <StateTag s={s} />
            </div>
            {s.canNotify && def.notify && (
              <Capability
                title="imNotify" hint="imNotifyD" switchKey={`im.${s.kind}.notify`} fields={def.notify}
                values={values} onChange={onChange} secretsSet={secretsSet}
                testLabel="imTestNotify" busy={testNotify.isPending}
                onTest={() => testNotify.mutate({ kind: s.kind, settings: settings() })}
              />
            )}
            {s.canApprove && def.approval && (
              <Capability
                title="imApproval" hint="imApprovalD" switchKey={`im.${s.kind}.approval`} fields={def.approval}
                values={values} onChange={onChange} secretsSet={secretsSet}
                testLabel="imTestCard" busy={testCard.isPending}
                onTest={() => testCard.mutate({ kind: s.kind, settings: settings() })}
                extra={
                  s.kind === 'telegram' ? <TelegramHint values={values} />
                    : s.kind === 'lark' ? <LarkHint values={values} />
                      : s.kind === 'dingtalk' ? <DingTalkHint />
                        : s.kind === 'slack' ? <SlackHint values={values} />
                          : undefined
                }
              />
            )}
          </div>
        )
      })}
    </div>
  )
}

function Capability({
  title, hint, switchKey, fields, values, onChange, secretsSet, testLabel, busy, onTest, extra,
}: {
  title: string
  hint: string
  switchKey: string
  fields: FieldDef[]
  values: ImValues
  onChange: (key: string, v: string | boolean) => void
  secretsSet?: Record<string, boolean>
  testLabel: string
  busy: boolean
  onTest: () => void
  extra?: ReactNode
}) {
  const { t } = useTranslation()
  const on = values[switchKey] === true
  return (
    <>
      <CardRow title={t(title)} hint={t(hint)}>
        <Switch checked={on} onChange={(v) => onChange(switchKey, v)} />
      </CardRow>
      {on && (
        <div className="set-sub">
          {fields.map((f) => <FieldInput key={f.key} f={f} values={values} onChange={onChange} secretsSet={secretsSet} />)}
          {extra}
          <div className="set-sub-foot">
            <Button disabled={busy} onClick={onTest}>{busy ? t('imSending') : t(testLabel)}</Button>
          </div>
        </div>
      )}
    </>
  )
}

function FieldInput({
  f, values, onChange, secretsSet,
}: {
  f: FieldDef
  values: ImValues
  onChange: (key: string, v: string | boolean) => void
  secretsSet?: Record<string, boolean>
}) {
  const { t } = useTranslation()
  const value = String(values[f.key] ?? '')
  return (
    <label className="set-field">
      <span>{t(f.label)}</span>
      {f.options ? (
        <select className="set-in" value={value} onChange={(e) => onChange(f.key, e.target.value)}>
          {f.options.map((o) => <option key={o.value} value={o.value}>{t(o.labelKey)}</option>)}
        </select>
      ) : (
        <input
          className="set-in"
          type={f.secret ? 'password' : 'text'}
          value={value}
          placeholder={
            f.secret && secretsSet?.[f.key] ? t('secretConfigured')
              : f.placeholderKey ? t(f.placeholderKey) : f.placeholder
          }
          onChange={(e) => onChange(f.key, e.target.value)}
        />
      )}
    </label>
  )
}

/**
 * Telegram 两种模式各有一件必须让人知道的事:polling 只能单实例跑(第二个实例会与它
 * 互相踢掉),webhook 要把这个地址填进 setWebhook —— 网关会自己登记,但管理员要确认
 * 平台能从公网访问到它。
 */
function TelegramHint({ values }: { values: ImValues }) {
  const { t } = useTranslation()
  if (values['im.telegram.mode'] !== 'webhook') {
    return <div className="set-hint">{t('imTgPollingNote')}</div>
  }
  const base = String(values['im.callbackBaseURL'] ?? '').trim().replace(/\/+$/, '')
  if (!base) return <div className="set-hint warn">{t('imTgWebhookNeedsBase')}</div>
  return <div className="set-hint">{t('imTgWebhookURL')}<code>{`${base}/api/v1/im/telegram/webhook`}</code></div>
}

/**
 * 飞书两种接收方式各要管理员在开发者后台做一件事:长连接要把订阅方式切成「使用长连接接收」,
 * webhook 要把这个地址同时填进「事件配置」与「回调配置」的请求地址 —— 飞书不像 Telegram 那样
 * 由网关自己登记。
 */
function LarkHint({ values }: { values: ImValues }) {
  const { t } = useTranslation()
  if (values['im.lark.mode'] !== 'webhook') {
    return <div className="set-hint">{t('imLarkWsNote')}</div>
  }
  const base = String(values['im.callbackBaseURL'] ?? '').trim().replace(/\/+$/, '')
  if (!base) return <div className="set-hint warn">{t('imLarkWebhookNeedsBase')}</div>
  return <div className="set-hint">{t('imLarkWebhookURL')}<code>{`${base}/api/v1/im/lark/webhook`}</code></div>
}

/**
 * 钉钉只有 Stream 一种接收方式,不需要公网入口;要让管理员知道的是:机器人得设成 Stream 模式,
 * 卡片模板得按手册搭(变量名与网关一一对应,搭错了卡片发不出去或按钮点了没反应)。
 */
function DingTalkHint() {
  const { t } = useTranslation()
  return <div className="set-hint">{t('imDtStreamNote')}</div>
}

/**
 * Slack 两种接收方式各要管理员在应用后台做一件事:Socket Mode 要打开 Socket Mode 并建 App-Level Token,
 * webhook 要把这个地址同时填进「Interactivity & Shortcuts」与「Event Subscriptions」的 Request URL ——
 * Slack 不像 Telegram 那样由网关自己登记。
 */
function SlackHint({ values }: { values: ImValues }) {
  const { t } = useTranslation()
  if (values['im.slack.mode'] !== 'webhook') {
    return <div className="set-hint">{t('imSlSocketNote')}</div>
  }
  const base = String(values['im.callbackBaseURL'] ?? '').trim().replace(/\/+$/, '')
  if (!base) return <div className="set-hint warn">{t('imSlWebhookNeedsBase')}</div>
  return <div className="set-hint">{t('imSlWebhookURL')}<code>{`${base}/api/v1/im/slack/webhook`}</code></div>
}

function StateTag({ s }: { s: ImStatus }) {
  const { t } = useTranslation()
  if (s.lastError) return <span className="im-state err" title={s.lastError}>{t('imStateError', { msg: s.lastError })}</span>
  if (s.lastNotifyError) {
    return <span className="im-state err" title={s.lastNotifyError}>{t('imStateNotifyError', { msg: s.lastNotifyError })}</span>
  }
  if (s.running) return <span className="im-state on">{t('imStateRunning')}</span>
  if (!s.configured) return <span className="im-state">{t('imStateUnconfigured')}</span>
  return <span className="im-state">{s.notify || s.approval ? t('imStateReady') : t('imStateIdle')}</span>
}
