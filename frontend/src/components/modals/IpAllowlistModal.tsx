import { useState } from 'react'
import { Network, X } from 'lucide-react'
import VButton from '@/components/common/VButton'
import VSwitch from '@/components/common/VSwitch'
import { useI18n } from '@/hooks/useI18n'
import './IpAllowlistModal.scoped.css'

// titleKey / subKey:同一个弹窗也用来配管理后台 IP 白名单(工单 08),标题与说明可换
interface Props {
  open: boolean
  enabled: boolean
  list: string
  titleKey?: string
  subKey?: string
  onClose: () => void
  onSave: (payload: { enabled: boolean; list: string }) => void
}

export default function IpAllowlistModal({ open, ...rest }: Props) {
  // 每次打开都是一个新的内容组件:从当前已存的值重新开始填。
  return open ? <Body {...rest} /> : null
}

function Body({ enabled, list, titleKey, subKey, onClose, onSave }: Omit<Props, 'open'>) {
  const { t } = useI18n()
  const [on, setOn] = useState(enabled)
  const [text, setText] = useState(list)

  function save() {
    // normalize: entries split by newline/comma → single comma-separated string.
    onSave({ enabled: on, list: text.split(/[\n,]/).map((s) => s.trim()).filter(Boolean).join(', ') })
  }

  return (
    <div className="ipallow-overlay">
      <div className="ipallow-mask" onClick={onClose} />
      <div className="modal">
        <div className="topbar" />
        <div className="pad">
          <div className="hdr">
            <div className="ic"><Network size={20} color="var(--accent-text)" /></div>
            <div className="grow">
              <div className="title">{t(titleKey || 'ipModalTitle')}</div>
              <div className="desc">{t(subKey || 'ipModalSub')}</div>
            </div>
            <div className="x" onClick={onClose}><X size={18} /></div>
          </div>

          <div className="enrow">
            <div><div className="rt">{t('ipEnable')}</div><div className="rd">{t('ipEnableD')}</div></div>
            <VSwitch value={on} onChange={setOn} />
          </div>

          <div className="cl">{t('ipListLabel')}</div>
          <textarea value={text} onChange={(e) => setText(e.target.value)} className="area" rows={5} placeholder={'10.20.0.0/16\n203.0.113.5'} />
          <div className="hint">{t('ipHint')}</div>
        </div>

        <div className="footer">
          <VButton variant="secondary" onClick={onClose}>{t('btnCancel')}</VButton>
          <VButton variant="primary" onClick={save}>{t('btnSave')}</VButton>
        </div>
      </div>
    </div>
  )
}
