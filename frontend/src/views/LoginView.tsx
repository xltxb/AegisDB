import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import clsx from 'clsx'
import { Mail, Lock, Languages, ShieldCheck, KeyRound, ShieldX } from 'lucide-react'
import { useAuthStore, SSO_SKIP_KEY } from '@/stores/auth'
import { useUIStore } from '@/stores/ui'
import { CODE_MFA_REQUIRED } from '@/api/http'
import { CODE_SSO_NO_SESSION, CODE_SSO_NO_ACCOUNT } from '@/api/codes'
import { srdLoginHref, justReturnedFromSrd, SRD_REDIRECTED_KEY } from '@/lib/srdLogin'
import api from '@/api'
import type { SsoConfig } from '@/types'
import VButton from '@/components/common/VButton'
import BrandMark from '@/components/BrandMark'
import { brandText } from '@/lib/brand'
import { useBrand } from '@/hooks/useBrand'
import { useI18n } from '@/hooks/useI18n'
import { nav, useRoute } from '@/hooks/useNav'
import './LoginView.scoped.css'

// Prefill AND advertise the demo credentials only in a dev build — never ship
// them in a production bundle (L5).
const isDev = import.meta.env.DEV

export default function LoginView() {
  const { t } = useI18n()
  const route = useRoute()
  const lang = useUIStore((s) => s.lang)
  const toggleLang = useUIStore((s) => s.toggleLang)
  const brand = useBrand()

  const [email, setEmail] = useState(isDev ? 'linwei@aegisdb.io' : '')
  const [password, setPassword] = useState(isDev ? 'aegisdb123' : '')
  const [loading, setLoading] = useState(false)
  // Two-step login: once the password is accepted for an MFA-enrolled user, the
  // backend answers CODE_MFA_REQUIRED and we reveal the TOTP field.
  const [mfaRequired, setMfaRequired] = useState(false)
  const [mfaCode, setMfaCode] = useState('')
  const mfaInput = useRef<HTMLInputElement>(null)
  // Surface the idle-lock notice when redirected here by the auto-lock.
  const [error, setError] = useState(() => (route.query.locked ? t('mfaLocked') : ''))

  // ---------------------------------------------------------------- SRD 授权登录(公司 SSO)
  // SSO 只管"你是谁"。网关里没有这个邮箱(管理员没开通)时服务端拒绝(40302),这里弹窗
  // 拦下并说清"无权限访问、找管理员开通";有账号但还没分配角色的,提示"等管理员分配"。
  const [sso, setSso] = useState<SsoConfig | null>(null)
  const [ssoBusy, setSsoBusy] = useState(false)
  const ssoBusyRef = useRef(false)
  const [noAccount, setNoAccount] = useState(false)
  // 本地表单:未启用 SSO 时照常展开;启用后默认收起,点一下才出来 —— 还没绑定 SSO 的人和
  // 管理员(应急)仍要用它,但它不该是一进来就看到的那扇门。
  const [showLocal, setShowLocal] = useState(true)
  // 已绑定 TOTP 的管理员走 SSO 也要输码(服务端答 42800):在 SSO 按钮下亮出输码框,带码再换一次会话。
  const [ssoMfa, setSsoMfa] = useState(false)
  const [ssoMfaCode, setSsoMfaCode] = useState('')
  const ssoMfaInput = useRef<HTMLInputElement>(null)

  function afterLogin(viaSSO: boolean) {
    const auth = useAuthStore.getState()
    // firstVisibleRoute 在这里只用来回答一个问题:这个人有没有任何一处能去。
    // 有,就送去总览 —— 落地页不该是 Web 命令行,那是这个产品里唯一能改动真实数据
    // 的地方。总览本身不设菜单闸,所以送得过去。
    if (!auth.firstVisibleRoute) {
      // Logged in but the role has no visible menu — don't push('') (which would
      // silently stay on the login page), tell the user (V5).
      setError(viaSSO ? t('ssoNoRole') : t('loginNoMenu'))
      return
    }
    // 只挂业务查询角色的人(业务方)只有「数据查询」一页,送去那里而不是总览。
    void nav.push(auth.onlyQuery ? '/query' : '/dashboard')
  }

  // 跳去 SRD 授权。回来时落在本页,挂载时的静默尝试会用 SRD 写下的 CS-Token 换会话。
  function goSRD(cfg: SsoConfig) {
    if (!cfg.loginUrl) return
    try {
      // 人主动要走 SSO:登出时设的"别自动登录"就该撤掉,否则授权回来什么都不发生。
      sessionStorage.removeItem(SSO_SKIP_KEY)
      sessionStorage.setItem(SRD_REDIRECTED_KEY, String(Date.now()))
    } catch { /* 隐私模式 */ }
    window.location.href = srdLoginHref(cfg.loginUrl, window.location)
  }

  // auto:进登录页时静默试一次。没有登录态(40101)时什么都不说,露出按钮就够了。
  // 手动点按钮:浏览器里已有 SRD 登录态就直接进来;没有、或者已过期被 SRD 拒绝,就去 SRD 授权。
  async function trySSO(cfg: SsoConfig | null, auto: boolean, code = '') {
    if (!cfg?.enabled || ssoBusyRef.current) return
    setError('')
    ssoBusyRef.current = true
    setSsoBusy(true)
    let returned = false
    try {
      returned = justReturnedFromSrd(sessionStorage.getItem(SRD_REDIRECTED_KEY), Date.now())
      sessionStorage.removeItem(SRD_REDIRECTED_KEY)
    } catch { /* 隐私模式 */ }
    try {
      await useAuthStore.getState().loginSSO(code)
      afterLogin(true)
    } catch (e: any) {
      if (e?.code === CODE_MFA_REQUIRED) {
        // 第一次只是在要码,不带说明;带了码还被拒才显示原因
        setSsoMfa(true)
        setError(code ? (e.message || t('loginMfaError')) : '')
        setSsoMfaCode('')
        setTimeout(() => ssoMfaInput.current?.focus())
        return
      }
      if (e?.code === CODE_SSO_NO_ACCOUNT) {
        setNoAccount(true)
        return
      }
      const noSession = e?.code === CODE_SSO_NO_SESSION
      if (auto && noSession && returned) {
        // 刚在 SRD 授权完,网关却没收到 CS-Token:再点一次只会来回跳。把原因说出来。
        setError(t('ssoNoCookie'))
        return
      }
      if (!auto && (noSession || e?.code === 40100)) {
        goSRD(cfg)
        return
      }
      if (!noSession) setError(e?.message || t('loginError'))
    } finally {
      ssoBusyRef.current = false
      setSsoBusy(false)
    }
  }

  useEffect(() => {
    let gone = false
    void (async () => {
      let cfg: SsoConfig | null = null
      try { cfg = await api.ssoConfig() } catch { /* 拿不到就当没启用 */ }
      if (gone) return
      setSso(cfg)
      if (!cfg?.enabled) return
      setShowLocal(false)
      // 主动登出后回到这里不自动登录(否则"登出"按下去一眨眼又登进来了);
      // 空闲锁定跳回来的也不自动 —— 锁定的意义就是要人重新确认一次。
      let skip = false
      try { skip = sessionStorage.getItem(SSO_SKIP_KEY) === '1' } catch { /* 隐私模式 */ }
      if (!skip && !route.query.locked) void trySSO(cfg, true)
    })()
    return () => { gone = true }
    // 只在进登录页时跑一次
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  async function submit() {
    setError('')
    setLoading(true)
    try {
      await useAuthStore.getState().login(email.trim(), password, mfaCode.trim())
      afterLogin(false)
    } catch (e: any) {
      if (e?.code === CODE_MFA_REQUIRED) {
        // A submitted-but-wrong code carries a message; the first prompt does not.
        setMfaRequired(true)
        setError(mfaCode ? (e.message || t('loginMfaError')) : '')
        setMfaCode('')
        // Move the cursor straight to the code field so the user can type at once.
        setTimeout(() => mfaInput.current?.focus())
        return
      }
      // 已启用 SSO、非管理员用本地口令时,服务端给的是一句明确的"请使用 SSO 登录" ——
      // 换成通用的"邮箱或密码错误"会让人去反复试口令。
      setError(sso?.enabled && e?.message ? e.message : t('loginError'))
    } finally {
      setLoading(false)
    }
  }
  const onEnter = (fn: () => void) => (e: React.KeyboardEvent) => { if (e.key === 'Enter') fn() }
  const submitSsoMfa = () => trySSO(sso, false, ssoMfaCode.trim())

  return (
    <div className="login">
      <div className="lang" onClick={toggleLang}><Languages size={14} />{t('langLabel')}</div>
      <div className="card">
        <div className="brand">
          <div className={clsx('logo', brand.logo && 'custom')}><BrandMark size={22} /></div>
          <div>
            <div className="bn" data-testid="login-brand-name">{brandText(lang, undefined, brand)}</div>
            <div className="bs">{t('loginTitle')}</div>
          </div>
        </div>
        <div className="title">{t('loginTitle')}</div>
        <div className="sub">{t('loginSub')}</div>

        {/* SRD 授权登录(公司 SSO):启用时它是主入口 */}
        {sso?.enabled && (
          <>
            <VButton variant="primary" fullWidth disabled={ssoBusy} className="ssobtn" data-testid="sso-login" onClick={() => trySSO(sso, false)}>
              <KeyRound size={15} />{ssoBusy ? t('ssoTrying') : t('ssoBtn')}
            </VButton>
            {ssoMfa && (
              <>
                <label className="lbl">{t('loginMfaLabel')}</label>
                <div className="field">
                  <ShieldCheck size={15} color="var(--text-faint)" />
                  <input
                    ref={ssoMfaInput} data-testid="sso-mfa-code" inputMode="numeric" autoComplete="one-time-code" maxLength={6} placeholder={t('loginMfaPh')}
                    value={ssoMfaCode} onChange={(e) => setSsoMfaCode(e.target.value)} onKeyUp={onEnter(submitSsoMfa)}
                  />
                </div>
                <VButton variant="primary" fullWidth disabled={ssoBusy || !ssoMfaCode.trim()} className="ssobtn" data-testid="sso-mfa-submit" onClick={submitSsoMfa}>
                  {t('ssoMfaSubmit')}
                </VButton>
              </>
            )}
            {!showLocal
              ? <div className="ssoalt"><span className="ssolink" data-testid="sso-admin-toggle" onClick={() => setShowLocal(true)}>{t('ssoAdminToggle')}</span></div>
              : <div className="ssosep"><span>{t('ssoOr')}</span></div>}
            {showLocal && <div className="ssohint">{t('ssoAdminHint')}</div>}
          </>
        )}

        {showLocal && (
          <>
            <label className="lbl">{t('loginEmail')}</label>
            <div className="field"><Mail size={15} color="var(--text-faint)" /><input type="email" autoComplete="username" value={email} onChange={(e) => setEmail(e.target.value)} onKeyUp={onEnter(submit)} /></div>

            <label className="lbl">{t('loginPassword')}</label>
            <div className="field"><Lock size={15} color="var(--text-faint)" /><input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} onKeyUp={onEnter(submit)} /></div>

            {mfaRequired && (
              <>
                <label className="lbl">{t('loginMfaLabel')}</label>
                <div className="field">
                  <ShieldCheck size={15} color="var(--text-faint)" />
                  <input
                    ref={mfaInput} inputMode="numeric" autoComplete="one-time-code" maxLength={6} placeholder={t('loginMfaPh')}
                    value={mfaCode} onChange={(e) => setMfaCode(e.target.value)} onKeyUp={onEnter(submit)}
                  />
                </div>
              </>
            )}
          </>
        )}

        {error && <div className="err" data-testid="login-error">{error}</div>}

        {showLocal && (
          <VButton variant={sso?.enabled ? 'secondary' : 'primary'} fullWidth disabled={loading} onClick={submit}>
            {loading ? '…' : t('loginBtn')}
          </VButton>
        )}
        {isDev && <div className="hint">{t('loginHint')}</div>}
      </div>

      {noAccount && createPortal(
        <div className="ovl" data-testid="sso-no-account" onClick={(e) => { if (e.target === e.currentTarget) setNoAccount(false) }}>
          <div className="dlg" role="alertdialog" aria-modal="true" aria-label={t('ssoNoAccountTitle')}>
            <div className="dlgic"><ShieldX size={22} color="var(--danger-text)" /></div>
            <div className="dlgt">{t('ssoNoAccountTitle')}</div>
            <div className="dlgb">{t('ssoNoAccountBody')}</div>
            <VButton variant="primary" fullWidth data-testid="sso-no-account-ok" onClick={() => setNoAccount(false)}>
              {t('ssoNoAccountOk')}
            </VButton>
          </div>
        </div>,
        document.body,
      )}
    </div>
  )
}
