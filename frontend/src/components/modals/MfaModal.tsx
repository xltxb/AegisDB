import { useEffect, useState } from 'react'
import QRCode from 'qrcode'
import { KeyRound, X, ShieldCheck } from 'lucide-react'
import VButton from '@/components/common/VButton'
import api from '@/api'
import { CODE_PASSWORD_REQUIRED } from '@/api/codes'
import { useI18n } from '@/hooks/useI18n'
import './MfaModal.scoped.css'

interface Props { open: boolean; enabled: boolean; onClose: () => void; onChanged: () => void }

export default function MfaModal({ open, ...rest }: Props) {
  // 每次打开都是一个新的内容组件:状态自然从头来。
  return open ? <Body {...rest} /> : null
}

function Body({ enabled, onClose, onChanged }: Omit<Props, 'open'>) {
  const { t } = useI18n()
  const [secret, setSecret] = useState('')
  const [qr, setQr] = useState('')
  const [code, setCode] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  // 有本地口令的账号:绑定前先出示当前登录密码(服务端答 42801),再领密钥
  const [needPw, setNeedPw] = useState(false)
  const [pw, setPw] = useState('')

  async function loadSetup(password = '') {
    setBusy(true)
    try {
      const s = await api.mfaSetup(password)
      setNeedPw(false)
      setPw('')
      setErr('')
      setSecret(s.secret)
      setQr(await QRCode.toDataURL(s.otpauthUri, { margin: 1, width: 176 }))
    } catch (e: any) {
      if (e?.code === CODE_PASSWORD_REQUIRED) {
        setNeedPw(true)
        // SSO 会话已超时(服务端带 ssoReauth):第一次就把"请重新 SSO 登录"说出来,
        // 否则只经 SSO 登录的人面对一个不知道的口令框无从下手
        setErr(password ? (e.message || t('mfaPwWrong')) : (e?.data?.ssoReauth ? e.message : ''))
        return
      }
      setErr(e?.message || t('mfaInitFailed'))
    } finally { setBusy(false) }
  }

  // When opening for a not-yet-enrolled user, fetch a fresh secret + render its QR.
  useEffect(() => {
    if (!enabled) void loadSetup()
    // 只在打开的那一刻领一次密钥
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  async function submit(call: (c: string) => Promise<unknown>, lenMsg: string) {
    if (code.trim().length !== 6) { setErr(t(lenMsg)); return }
    setBusy(true)
    setErr('')
    try {
      await call(code.trim())
      onChanged()
      onClose()
    } catch (e: any) {
      setErr(e?.message || t('mfaBadCodeRetry'))
    } finally { setBusy(false) }
  }
  const enable = () => submit((c) => api.mfaEnable(c), 'mfaNeed6')
  const disable = () => submit((c) => api.mfaDisable(c), 'mfaNeedCurrent6')

  return (
    <div className="mfa-overlay">
      <div className="mfa-mask" onClick={onClose} />
      <div className="modal">
        <div className="topbar" />
        <div className="pad">
          <div className="hdr">
            <div className="ic"><KeyRound size={20} color="var(--accent-text)" /></div>
            <div className="grow">
              <div className="title">{enabled ? t('mfaEnabledTitle') : t('mfaModalTitle')}</div>
              <div className="desc">{enabled ? t('mfaEnabledDesc') : t('mfaScan')}</div>
            </div>
            <div className="x" onClick={onClose}><X size={18} /></div>
          </div>

          {/* enrollment */}
          {!enabled && needPw ? (
            <>
              <div className="cl">{t('mfaPwLabel')}</div>
              <input
                autoFocus className="pwin" type="password" autoComplete="current-password" data-testid="mfa-current-pw"
                value={pw} onChange={(e) => setPw(e.target.value)}
                onKeyUp={(e) => { if (e.key === 'Enter' && pw) void loadSetup(pw) }}
              />
            </>
          ) : !enabled ? (
            <div className="enroll">
              <div className="qrbox">{qr && <img src={qr} alt="QR" />}</div>
              <div className="skey">
                <div className="kl">{t('mfaSecretLabel')}</div>
                <code className="kv">{secret}</code>
              </div>
            </div>
          ) : (
            <div className="okrow"><ShieldCheck size={16} color="var(--success-text)" /><span>{t('mfaEnabledActive')}</span></div>
          )}

          {(enabled || !needPw) && (
            <>
              <div className="cl">{enabled ? t('mfaCurrentCode') : t('mfaCodeLabel')}</div>
              <input
                autoFocus className="codein" inputMode="numeric" autoComplete="one-time-code" placeholder="000000"
                value={code} onChange={(e) => setCode(e.target.value.replace(/\D/g, '').slice(0, 6))}
                onKeyUp={(e) => { if (e.key === 'Enter') void (enabled ? disable() : enable()) }}
              />
            </>
          )}
          {err && <div className="err">{err}</div>}
        </div>

        <div className="footer">
          <VButton variant="secondary" onClick={onClose}>{t('btnCancel')}</VButton>
          {enabled ? <VButton variant="danger" disabled={busy} onClick={disable}>{t('mfaDisable')}</VButton>
            : needPw ? <VButton variant="primary" disabled={busy || !pw} data-testid="mfa-pw-next" onClick={() => loadSetup(pw)}>{t('mfaPwNext')}</VButton>
              : <VButton variant="primary" disabled={busy} onClick={enable}>{t('mfaEnable')}</VButton>}
        </div>
      </div>
    </div>
  )
}
