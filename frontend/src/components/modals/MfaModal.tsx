import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toDataURL } from 'qrcode'
import { ShieldCheck } from 'lucide-react'
import { Modal } from '@/components/common/Modal'
import { Button } from '@/components/common/Button'
import { Loading } from '@/components/common/States'
import { useDisableMfa, useEnableMfa, useMfaSetup } from '@/hooks/useMfa'
import { useUIStore } from '@/stores/ui'

/**
 * 二次验证(TOTP)自助绑定 / 解绑。
 *
 * 三样东西一起给:二维码、明文密钥、一个 6 位码输入框。二维码扫不了(远程桌面、
 * 摄像头被公司策略关掉)的人照样能把密钥手打进验证器 —— 只给二维码等于把一部分
 * 人挡在门外,而这道门后面是生产库。
 */
export default function MfaModal({
  open, bound, onClose,
}: { open: boolean; bound: boolean; onClose: () => void }) {
  const { t } = useTranslation()
  const notify = useUIStore((s) => s.notify)
  const setup = useMfaSetup(open && !bound)
  const enable = useEnableMfa()
  const disable = useDisableMfa()

  const [code, setCode] = useState('')
  const [qr, setQr] = useState('')
  const [err, setErr] = useState('')

  // 每次开合都从头开始:上一次输错的码和上一次的报错不该留到下一次。
  useEffect(() => {
    setCode('')
    setErr('')
  }, [open])

  /** otpauth URI → 二维码图。渲染是异步的,组件先关掉就把结果丢掉。 */
  useEffect(() => {
    const uri = setup.data?.otpauthUri
    if (!uri) { setQr(''); return }
    let alive = true
    toDataURL(uri, { margin: 1, width: 176 })
      .then((d) => { if (alive) setQr(d) })
      .catch(() => { if (alive) setQr('') })
    return () => { alive = false }
  }, [setup.data?.otpauthUri])

  const busy = enable.isPending || disable.isPending

  function submit() {
    const c = code.trim()
    if (c.length !== 6) {
      setErr(bound ? t('mfaNeedCurrent6') : t('mfaNeed6'))
      return
    }
    setErr('')
    const m = bound ? disable : enable
    m.mutate(c, {
      onSuccess: () => {
        notify(bound ? t('mfaDisabled') : t('mfaEnabled'), 'ok')
        onClose()
      },
      onError: (e: Error) => setErr(e.message || t('mfaBadCodeRetry')),
    })
  }

  return (
    <Modal
      open={open}
      width={420}
      title={bound ? t('mfaEnabledTitle') : t('mfaModalTitle')}
      sub={bound ? t('mfaEnabledDesc') : t('mfaScan')}
      onClose={onClose}
      footer={<>
        <Button onClick={onClose}>{t('cancel')}</Button>
        <Button variant={bound ? 'danger' : 'primary'} disabled={busy} onClick={submit}>
          {bound ? t('mfaDisable') : t('mfaEnable')}
        </Button>
      </>}
    >
      {!bound && (
        <div className="mfa-enroll">
          <div className="mfa-qr">
            {setup.isLoading && <Loading />}
            {qr && <img src={qr} alt="" />}
          </div>
          <div className="mfa-key">
            <div className="mfa-key-l">{t('mfaSecretLabel')}</div>
            <code className="mfa-key-v">{setup.data?.secret ?? '—'}</code>
          </div>
        </div>
      )}
      {!bound && setup.error && (
        <div className="notice danger">{(setup.error as Error).message || t('mfaInitFailed')}</div>
      )}
      {bound && (
        <div className="notice ok">
          <ShieldCheck size={15} />{t('mfaEnabledActive')}
        </div>
      )}

      <div className="mfa-cl">{bound ? t('mfaCurrentCode') : t('mfaCodeLabel')}</div>
      <input
        className="mfa-code"
        // 打开这个弹窗的人下一步一定是照着验证器敲六位数字,光标就该已经在这里 ——
        // 让他先去点一下输入框,是在一个只有一个输入项的弹窗里多要一次点击。
        autoFocus
        inputMode="numeric"
        autoComplete="one-time-code"
        placeholder="000000"
        value={code}
        onChange={(e) => setCode(e.target.value.replace(/\D/g, '').slice(0, 6))}
        onKeyDown={(e) => { if (e.key === 'Enter') submit() }}
      />
      {err && <div className="mfa-err">{err}</div>}
    </Modal>
  )
}
