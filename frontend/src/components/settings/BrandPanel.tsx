// 系统设置 → 品牌:系统名称(中 / 英)与 LOGO。
//
// 自己保存(只发 brand.* 三项),存成了立刻换掉当前页面上的名称、LOGO、favicon;其他人下次打开
// 页面(包括未登录的登录页)读 GET /auth/branding 拿到新值。名称留空 = 保持默认字样。
// LOGO 以 data URI 存进数据库(多活各节点共享,不落本机磁盘),服务端会再校验类型、大小、SVG 脚本。
import { useRef, useState, type ChangeEvent, type HTMLAttributes } from 'react'
import clsx from 'clsx'
import { ImageUp, Trash2 } from 'lucide-react'
import VButton from '@/components/common/VButton'
import api from '@/api'
import { useUIStore } from '@/stores/ui'
import { DEFAULT_BRAND, LOGO_MAX_BYTES, LOGO_TYPES, brand, checkLogoFile, setBrand } from '@/lib/brand'
import BrandMark from '@/components/BrandMark'
import { useI18n } from '@/hooks/useI18n'
import { useMountEffect } from '@/hooks/effects'
import './BrandPanel.scoped.css'

export default function BrandPanel({ className, ...rest }: HTMLAttributes<HTMLElement>) {
  const { t } = useI18n()
  const { notify, notifyError } = useUIStore.getState()

  const [name, setName] = useState('')
  const [nameEn, setNameEn] = useState('')
  const [logo, setLogo] = useState('')
  const [saving, setSaving] = useState(false)
  const fileInput = useRef<HTMLInputElement>(null)

  function fill() {
    setName(brand.name)
    setNameEn(brand.nameEn)
    setLogo(brand.logo)
  }

  useMountEffect(() => {
    void (async () => {
      try { setBrand(await api.branding()) } catch { /* 用启动时拉到的 */ }
      fill()
    })()
  })

  function onFile(e: ChangeEvent<HTMLInputElement>) {
    const input = e.target
    const f = input.files?.[0]
    input.value = '' // 同一个文件再选一次也要触发
    if (!f) return
    const bad = checkLogoFile(f)
    if (bad === 'type') { notify(t('brandLogoBadType'), 'error'); return }
    if (bad === 'size') { notify(t('brandLogoTooBig', { n: LOGO_MAX_BYTES / 1024 }), 'error'); return }
    const rd = new FileReader()
    rd.onload = () => setLogo(String(rd.result || ''))
    rd.onerror = () => notify(t('brandLogoReadFail'), 'error')
    rd.readAsDataURL(f)
  }

  async function save() {
    setSaving(true)
    const next = { name: name.trim(), nameEn: nameEn.trim(), logo }
    try {
      await api.saveSettings({ 'brand.name': next.name, 'brand.nameEn': next.nameEn, 'brand.logo': next.logo })
    } catch (e) {
      notifyError(e, t('saveFailed'))
      return
    } finally {
      setSaving(false)
    }
    setBrand(next)
    fill()
    notify(t('settingsSaved'), 'success')
  }

  return (
    <section {...rest} className={clsx('bp', className)} data-testid="brand-panel">
      <div className="bphead">
        <div className="bpic"><ImageUp size={17} color="var(--accent-text)" /></div>
        <div className="grow"><div className="bpt">{t('setBrand')}</div><div className="bps">{t('setBrandSub')}</div></div>
      </div>

      <div className="row">
        <div className="grow"><div className="rt">{t('brandName')}</div><div className="rd">{t('brandNameD')}</div></div>
        <input value={name} onChange={(e) => setName(e.target.value)} className="in" data-testid="brand-name" maxLength={40} placeholder={DEFAULT_BRAND} />
      </div>
      <div className="row">
        <div className="grow"><div className="rt">{t('brandNameEn')}</div><div className="rd">{t('brandNameEnD')}</div></div>
        <input value={nameEn} onChange={(e) => setNameEn(e.target.value)} className="in" data-testid="brand-name-en" maxLength={40} placeholder={DEFAULT_BRAND} />
      </div>
      <div className="row">
        <div className="grow"><div className="rt">{t('brandLogo')}</div><div className="rd">{t('brandLogoD', { n: LOGO_MAX_BYTES / 1024 })}</div></div>
        <div className="logoctl">
          <div className={clsx('prev', !!logo && 'custom')} data-testid="brand-logo-preview">
            {logo ? <img src={logo} alt="" /> : <BrandMark size={20} />}
          </div>
          <input ref={fileInput} type="file" className="hidden" data-testid="brand-logo-file" accept={LOGO_TYPES.join(',')} onChange={onFile} />
          <VButton height="34px" onClick={() => fileInput.current?.click()}><ImageUp size={14} />{t('brandLogoUpload')}</VButton>
          {logo && <VButton data-testid="brand-logo-clear" onClick={() => setLogo('')}><Trash2 size={14} />{t('brandLogoClear')}</VButton>}
        </div>
      </div>

      <div className="foot">
        <div className="grow hint">{t('brandHint')}</div>
        <VButton variant="primary" disabled={saving} data-testid="brand-save" onClick={save}>{t('saveSettings')}</VButton>
      </div>
    </section>
  )
}
