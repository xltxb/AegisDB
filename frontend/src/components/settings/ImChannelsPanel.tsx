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
}
type ChannelDef = { notify?: FieldDef[]; approval?: FieldDef[] }

/**
 * 每个渠道自己的配置项。表单初值、保存载荷、渲染都从这张表派生 —— 接一个新渠道只在
 * 这里加一项,不必再去动设置页。第 1 期只有飞书群通知。
 */
export const IM_CHANNELS: Record<string, ChannelDef> = {
  lark: {
    notify: [
      { key: 'im.lark.notifyWebhook', label: 'imLarkWebhook', placeholder: 'https://open.feishu.cn/open-apis/bot/v2/hook/xxxx' },
      { key: 'im.lark.notifySecret', label: 'imLarkSecret', placeholderKey: 'imLarkSecretPh', secret: true },
    ],
  },
}

export type ImValues = Record<string, string | boolean>

export function imInitialValues(g: Record<string, string>): ImValues {
  const v: ImValues = {}
  for (const [kind, def] of Object.entries(IM_CHANNELS)) {
    if (def.notify) v[`im.${kind}.notify`] = parseSetting(g[`im.${kind}.notify`], false)
    if (def.approval) v[`im.${kind}.approval`] = parseSetting(g[`im.${kind}.approval`], false)
    for (const f of [...(def.notify ?? []), ...(def.approval ?? [])]) {
      // 密钥从不回传。输入框留空 = 保持不变,所以初值只能是空串。
      v[f.key] = f.secret ? '' : parseSetting<string>(g[f.key], '')
    }
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
  const { data: status } = useImStatus()
  const testNotify = useTestImNotify()
  const testCard = useTestImCard()
  const settings = () => imSettingsPayload(values)

  // 以服务端登记过的渠道为准:前端认得、服务端却没登记的渠道不显示 —— 显示出来也配不了。
  const rows = (status ?? []).filter((s) => IM_CHANNELS[s.kind])
  return (
    <div className="im-list">
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
              />
            )}
          </div>
        )
      })}
    </div>
  )
}

function Capability({
  title, hint, switchKey, fields, values, onChange, secretsSet, testLabel, busy, onTest,
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
          {fields.map((f) => (
            <label key={f.key} className="set-field">
              <span>{t(f.label)}</span>
              <input
                className="set-in"
                type={f.secret ? 'password' : 'text'}
                value={String(values[f.key] ?? '')}
                placeholder={
                  f.secret && secretsSet?.[f.key] ? t('secretConfigured')
                    : f.placeholderKey ? t(f.placeholderKey) : f.placeholder
                }
                onChange={(e) => onChange(f.key, e.target.value)}
              />
            </label>
          ))}
          <div className="set-sub-foot">
            <Button disabled={busy} onClick={onTest}>{busy ? t('imSending') : t(testLabel)}</Button>
          </div>
        </div>
      )}
    </>
  )
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
