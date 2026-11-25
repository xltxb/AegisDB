import { useEffect, useState } from 'react'
import clsx from 'clsx'
import { Bot, Copy, X } from 'lucide-react'
import VButton from '@/components/common/VButton'
import api from '@/api'
import { CODE_MFA_REQUIRED } from '@/api/codes'
import { copyText } from '@/lib/clipboard'
import { confirmAction } from '@/lib/confirm'
import { absTime } from '@/lib/relTime'
import { useAuthStore } from '@/stores/auth'
import { useUIStore } from '@/stores/ui'
import { useEvent } from '@/hooks/useEvent'
import { useI18n } from '@/hooks/useI18n'
import type { ChatBinding } from '@/types'
import './ChatBindModal.scoped.css'

interface Props {
  open: boolean
  bindings: ChatBinding[]
  reload: () => Promise<void>
  onUnbound: (channel: string) => void
  onClose: () => void
}

// 把自己的聊天账号绑到网关账号上:生成一次性绑定码,私聊发给机器人。绑上的人不登录网关也能批单,
// 所以开了 MFA 的账号生成码之前要过一次。
export default function ChatBindModal({ open, ...rest }: Props) {
  // 每次打开都是一个新的内容组件:关掉时码、验证码随它一起没了。
  return open ? <Body {...rest} /> : null
}

function Body({ bindings, reload, onUnbound, onClose }: Omit<Props, 'open'>) {
  const { t } = useI18n()
  return (
    <div className="chatbind-overlay" data-testid="chat-bind-modal">
      <div className="chatbind-mask" onClick={onClose} />
      <div className="modal" role="dialog" aria-modal="true" aria-label={t('chatBindTitle')}>
        <div className="topbar" />
        <div className="pad">
          <div className="hdr">
            <div className="ic"><Bot size={20} color="var(--accent-text)" /></div>
            <div className="grow">
              <div className="title">{t('chatBindTitle')}</div>
              <div className="desc">{t('chatBindDesc')}</div>
            </div>
            <div className="x" onClick={onClose}><X size={18} /></div>
          </div>
          {bindings.map((b) => <ChannelRow key={b.channel} b={b} reload={reload} onUnbound={() => onUnbound(b.channel)} />)}
          {!bindings.length && <div className="none">{t('chatBindNone')}</div>}
        </div>
        <div className="footer">
          <VButton variant="secondary" data-testid="chat-close" onClick={onClose}>{t('btnClose')}</VButton>
        </div>
      </div>
    </div>
  )
}

// 与后端 chatCodeTTL 一致。按自己收到码的那一刻计,不拿 expiresAt 和本机时钟比:时钟偏了的电脑上,
// 刚生成的码会当场算过期。
const CODE_TTL_MS = 10 * 60 * 1000
const POLL_MS = 3000

function ChannelRow({ b, reload, onUnbound }: { b: ChatBinding; reload: () => Promise<void>; onUnbound: () => void }) {
  const { t } = useI18n()
  const { notify, notifyError } = useUIStore.getState()
  const name = t('chatCh_' + b.channel)
  const [code, setCode] = useState('')
  const [askMfa, setAskMfa] = useState(false)
  const [mfa, setMfa] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  // 绑上了,码就没用了(它也确实作废了)。
  if (b.bound && (code || askMfa)) { setCode(''); setAskMfa(false); setMfa('') }

  // 显示着码的时候才问绑上了没有;到点把码收起来。
  const expire = useEvent(() => { setCode(''); setErr(t('chatCodeExpired')) })
  useEffect(() => {
    if (!code) return
    const poll = setInterval(() => { if (!document.hidden) reload().catch(() => { /* 下一轮再问 */ }) }, POLL_MS)
    const ttl = setTimeout(expire, CODE_TTL_MS)
    return () => { clearInterval(poll); clearTimeout(ttl) }
  }, [code, reload, expire])

  async function generate(mfaCode = '') {
    if (busy) return
    setBusy(true)
    setErr('')
    try {
      const r = await api.chatBindCode(b.channel, mfaCode)
      setAskMfa(false)
      setCode(r.code)
    } catch (e: any) {
      if (e?.code === CODE_MFA_REQUIRED) setAskMfa(true) // 页面打开之后才开的 MFA
      else setErr(e?.message || t('actionFailed'))
    } finally {
      setMfa('') // 验证码只能用一次
      setBusy(false)
    }
  }
  function start() {
    setErr('')
    if (useAuthStore.getState().me?.mfaEnabled) setAskMfa(true)
    else void generate()
  }
  function submitMfa() {
    if (mfa.length !== 6) { setErr(t('mfaNeedCurrent6')); return }
    void generate(mfa)
  }
  function cancelMfa() { setAskMfa(false); setMfa(''); setErr('') }
  async function copy() {
    const done = await copyText(code)
    notify(t(done ? 'copied' : 'chatCopyFailed'), done ? 'success' : 'error')
  }
  async function unbind() {
    if (!(await confirmAction(t('chatUnbindConfirm', { channel: name }), { danger: true }))) return
    setBusy(true)
    try {
      await api.chatUnbind(b.channel)
      onUnbound()
      reload().catch(() => { /* 已经解绑了,重读不成不算失败 */ })
    } catch (e) {
      notifyError(e, t('actionFailed'))
    } finally { setBusy(false) }
  }

  return (
    <div className="ch">
      <div className="chhead">
        <div className="grow">
          <div className="chname">{name}{b.botName && <span className="bot">{b.botName}</span>}</div>
          {b.bound && b.boundAt && <div className="desc">{t('chatBoundAt', { at: absTime(b.boundAt) })}</div>}
        </div>
        <span className={clsx('tag', b.bound && 'on')} data-testid={`chat-state-${b.channel}`}>{b.bound ? t('otpBound') : t('otpUnboundState')}</span>
        {b.bound
          ? <VButton variant="danger" height="34px" disabled={busy} data-testid={`chat-unbind-${b.channel}`} onClick={unbind}>{t('chatUnbind')}</VButton>
          : !askMfa && <VButton variant="secondary" height="34px" disabled={busy} data-testid={`chat-gen-${b.channel}`} onClick={start}>{code ? t('chatRegen') : t('chatGen')}</VButton>}
      </div>

      {code && (
        <div className="codebox">
          <div className="coderow">
            <code className="code" data-testid="chat-code">{code}</code>
            <VButton variant="secondary" height="34px" data-testid="chat-code-copy" onClick={copy}><Copy size={13} />{t('chatCopy')}</VButton>
          </div>
          <div className="valid">{t('chatCodeValid')}</div>
          <div className="how">{t('chatHow_' + b.channel)}</div>
        </div>
      )}

      {askMfa && (
        <>
          <div className="cl">{t('mfaCurrentCode')}</div>
          <div className="mfarow">
            <input
              autoFocus className="codein" inputMode="numeric" autoComplete="one-time-code" placeholder="000000" data-testid="chat-mfa"
              value={mfa} onChange={(e) => setMfa(e.target.value.replace(/\D/g, '').slice(0, 6))}
              onKeyUp={(e) => { if (e.key === 'Enter') submitMfa() }}
            />
            <VButton variant="primary" height="44px" disabled={busy} data-testid="chat-mfa-submit" onClick={submitMfa}>{t('chatGen')}</VButton>
            <VButton variant="secondary" height="44px" data-testid="chat-mfa-cancel" onClick={cancelMfa}>{t('btnCancel')}</VButton>
          </div>
        </>
      )}
      {err && <div className="err" data-testid="chat-err">{err}</div>}
    </div>
  )
}
