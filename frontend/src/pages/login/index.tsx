import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { ShieldCheck } from 'lucide-react'
import clsx from 'clsx'
import { useAuthStore, type Portal } from '@/stores/auth'
import { queryClient } from '@/api/queryClient'
import { CODE_MFA_REQUIRED, CODE_LOGIN_THROTTLED } from '@/api/http'
import { firstVisibleRoute } from '@/router/guards'

/**
 * 双入口登录(前端文档 v2.0)。入口不是装饰:选运维前台时,后台路由由守卫直接
 * 拒绝,而不只是把侧栏入口藏起来。
 *
 * 动态码输入框只在服务端回 42800 之后才出现 —— 一上来就摆着它,没绑过 MFA 的人
 * 会以为自己少填了东西。
 */
export default function LoginPage() {
  const { t } = useTranslation()
  const nav = useNavigate()
  const login = useAuthStore((s) => s.login)
  const portal = useAuthStore((s) => s.portal)
  const setPortal = useAuthStore((s) => s.setPortal)

  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [mfaCode, setMfaCode] = useState('')
  const [needMfa, setNeedMfa] = useState(false)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setErr('')
    setBusy(true)
    try {
      const me = await login(email, password, mfaCode, portal)
      queryClient.setQueryData(['me'], me)
      nav(firstVisibleRoute(me, portal) ? '/dashboard' : '/', { replace: true })
    } catch (e: unknown) {
      const code = (e as { code?: number }).code
      if (code === CODE_MFA_REQUIRED) {
        setNeedMfa(true)
        setErr('')
      } else if (code === CODE_LOGIN_THROTTLED) {
        setErr(t('loginThrottled'))
      } else {
        setErr((e as Error).message || t('loginFailed'))
      }
    } finally {
      setBusy(false)
    }
  }

  const portals: { id: Portal; label: string; hint: string }[] = [
    { id: 'frontend', label: t('portalFrontend'), hint: t('portalFrontendHint') },
    { id: 'backend', label: t('portalBackend'), hint: t('portalBackendHint') },
  ]

  return (
    <div className="login">
      <form className="login-card" onSubmit={submit}>
        <div className="login-brand">
          <span className="login-mark"><ShieldCheck size={18} /></span>
          <span className="login-name">{t('appName')}</span>
        </div>
        <h1>{t('loginTitle')}</h1>
        <p className="login-sub">{t('loginSub')}</p>

        <span className="fld-label">{t('loginPortal')}</span>
        <div className="portal-pick">
          {portals.map((p) => (
            <button
              key={p.id}
              type="button"
              className={clsx('portal-opt', portal === p.id && 'on')}
              onClick={() => setPortal(p.id)}
            >
              <span className="portal-label">{p.label}</span>
              <span className="portal-hint">{p.hint}</span>
            </button>
          ))}
        </div>

        <label className="fld-label" htmlFor="email">{t('loginEmail')}</label>
        <input id="email" type="email" autoComplete="username" value={email}
               onChange={(e) => setEmail(e.target.value)} required />

        <label className="fld-label" htmlFor="pw">{t('loginPassword')}</label>
        <input id="pw" type="password" autoComplete="current-password" value={password}
               onChange={(e) => setPassword(e.target.value)} required />

        {needMfa && (
          <>
            <label className="fld-label" htmlFor="mfa">{t('loginMfa')}</label>
            <input id="mfa" inputMode="numeric" maxLength={6} autoFocus value={mfaCode}
                   onChange={(e) => setMfaCode(e.target.value)} />
          </>
        )}

        {err && <div className="login-err">{err}</div>}
        <button className="login-submit" type="submit" disabled={busy}>{t('loginSubmit')}</button>
      </form>
    </div>
  )
}
