import { useState } from 'react'
import { LockKeyhole, X } from 'lucide-react'
import VButton from '@/components/common/VButton'
import api from '@/api'
import { useAuthStore } from '@/stores/auth'
import { useUIStore } from '@/stores/ui'
import { useI18n } from '@/hooks/useI18n'
import './PasswordModal.scoped.css'

interface Props { open: boolean; onClose: () => void }

// 自助修改密码(黑盒 2026-10 工单 05)。成功后服务端作废这个人的其它会话,并为当前会话
// 重签一枚 token —— 这里换上它,人不必重新登录。
export default function PasswordModal({ open, onClose }: Props) {
  return open ? <Body onClose={onClose} /> : null
}

function Body({ onClose }: { onClose: () => void }) {
  const { t } = useI18n()
  const [oldPw, setOldPw] = useState('')
  const [newPw, setNewPw] = useState('')
  const [confirmPw, setConfirmPw] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  // 与服务端同一条规则(至少 8 位);服务端仍会再判一次,这里只是让人少等一个来回。
  function check(): string {
    if (!oldPw) return t('pwNeedOld')
    if (newPw.length < 8) return t('pwTooShort')
    if (newPw === oldPw) return t('pwSameAsOld')
    if (newPw !== confirmPw) return t('pwMismatch')
    return ''
  }

  async function submit() {
    if (busy) return
    const bad = check()
    setErr(bad)
    if (bad) return
    setBusy(true)
    try {
      const r = await api.changePassword(oldPw, newPw)
      useAuthStore.getState().replaceToken(r.session)
      useUIStore.getState().notify(t('pwChanged'), 'success', 6000)
      onClose()
    } catch (e: any) {
      setErr(e?.message || t('pwChangeFailed'))
    } finally { setBusy(false) }
  }

  return (
    <div className="pw-overlay" data-testid="password-modal">
      <div className="pw-mask" onClick={onClose} />
      <div className="modal">
        <div className="topbar" />
        <div className="pad">
          <div className="hdr">
            <div className="ic"><LockKeyhole size={20} color="var(--accent-text)" /></div>
            <div className="grow">
              <div className="title">{t('pwTitle')}</div>
              <div className="desc">{t('pwDesc')}</div>
            </div>
            <div className="x" onClick={onClose}><X size={18} /></div>
          </div>
          <label className="fl">{t('pwOld')}</label>
          <input autoFocus className="pin" type="password" autoComplete="current-password" data-testid="pw-old" value={oldPw} onChange={(e) => setOldPw(e.target.value)} />
          <label className="fl">{t('pwNew')}</label>
          <input className="pin" type="password" autoComplete="new-password" placeholder={t('pwNewPh')} data-testid="pw-new" value={newPw} onChange={(e) => setNewPw(e.target.value)} />
          <label className="fl">{t('pwConfirm')}</label>
          <input
            className="pin" type="password" autoComplete="new-password" data-testid="pw-confirm" value={confirmPw} onChange={(e) => setConfirmPw(e.target.value)}
            onKeyUp={(e) => { if (e.key === 'Enter') void submit() }}
          />
          {err && <div className="err" data-testid="pw-err">{err}</div>}
        </div>
        <div className="footer">
          <VButton variant="secondary" onClick={onClose}>{t('btnCancel')}</VButton>
          <VButton variant="primary" disabled={busy} data-testid="pw-submit" onClick={submit}>{busy ? t('pwSaving') : t('pwSubmit')}</VButton>
        </div>
      </div>
    </div>
  )
}
