import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import clsx from 'clsx'
import { LogOut, ShieldCheck } from 'lucide-react'
import { meQueryOptions } from '@/api/modules/auth'
import { useAuthStore } from '@/stores/auth'
import MfaModal from '@/components/modals/MfaModal'

/**
 * 账户菜单 —— 头像点开。
 *
 * 它回答"我现在是谁、以什么身份在操作":姓名、邮箱、角色。这三样在一个会同时挂着
 * 多个角色、还分两个门户的控制台里不是装饰 —— 有人用同事的机器改了一行生产配置,
 * 事后才发现登录的是别人的账号。
 *
 * 二次验证的自助绑定也在这里,而不是设置页:设置页整页挂着 `menu("settings")` 闸,
 * 而 MFA 是**每个账号自己的事**,非管理员同样要能绑。
 */
export default function AccountMenu() {
  const { t } = useTranslation()
  const nav = useNavigate()
  const { data: me } = useQuery(meQueryOptions())
  const logout = useAuthStore((s) => s.logout)
  const [open, setOpen] = useState(false)
  const [mfaOpen, setMfaOpen] = useState(false)

  const bound = !!me?.mfaEnabled

  return (
    <>
      <button
        className="rail-avatar"
        title={t('accountMenu')}
        onClick={() => setOpen((v) => !v)}
      >
        {me?.initials || '··'}
      </button>

      {open && (
        <>
          <div className="um-mask" onClick={() => setOpen(false)} />
          <div className="usermenu">
            <div className="um-head">
              <span className="um-ava">{me?.initials || '··'}</span>
              <div className="um-id">
                <div className="um-name">{me?.name || '—'}</div>
                <div className="um-mail">{me?.email}</div>
              </div>
            </div>
            <div className="um-role">
              {me?.roleName}{me?.layer ? ` · ${me.layer}` : ''}
            </div>
            <button className="um-row" onClick={() => { setOpen(false); setMfaOpen(true) }}>
              <ShieldCheck size={15} />{t('mfaSelfTitle')}
              <span className={clsx('um-tag', bound && 'on')}>
                {bound ? t('otpBound') : t('otpUnboundState')}
              </span>
            </button>
            <button
              className="um-row um-logout"
              onClick={() => { setOpen(false); logout(); nav('/login', { replace: true }) }}
            >
              <LogOut size={15} />{t('logout')}
            </button>
          </div>
        </>
      )}

      <MfaModal open={mfaOpen} bound={bound} onClose={() => setMfaOpen(false)} />
    </>
  )
}
