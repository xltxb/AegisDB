import { useState, type HTMLAttributes } from 'react'
import clsx from 'clsx'
import { Tags, X, Plus } from 'lucide-react'
import VButton from '@/components/common/VButton'
import { useI18n } from '@/hooks/useI18n'
import './TagEditModal.scoped.css'

interface Props extends Omit<HTMLAttributes<HTMLDivElement>, 'title'> {
  open: boolean
  title: string
  subtitle?: string
  tags: string[]
  suggestions?: string[]
  onClose: () => void
  onSave: (tags: string[]) => void
}

export default function TagEditModal({ open, ...rest }: Props) {
  // 每次打开都是一个新的内容组件:已选标签从 tags 抄一份,输入框清空。
  return open ? <Body {...rest} /> : null
}

const norm = (s: string) => s.trim().toLowerCase().replace(/[,\s]+/g, '')

function Body({ title, subtitle, tags, suggestions, onClose, onSave, className, ...rest }: Omit<Props, 'open'>) {
  const { t } = useI18n()
  const [list, setList] = useState<string[]>(() => [...tags])
  const [input, setInput] = useState('')

  function add(raw: string) {
    const v = norm(raw)
    if (v) setList((l) => (l.includes(v) ? l : [...l, v]))
    setInput('')
  }
  const remove = (v: string) => setList((l) => l.filter((x) => x !== v))
  const freeSuggest = (suggestions || []).filter((s) => !list.includes(s))

  return (
    <div {...rest} className={clsx('tagpick-overlay', className)}>
      <div className="tagpick-mask" onClick={() => onClose()} />
      <div className="tagpick-modal">
        <div className="topbar" />
        <div className="pad">
          <div className="hdr">
            <div className="ic"><Tags size={20} color="var(--accent-text)" /></div>
            <div className="grow"><div className="title">{title}</div>{subtitle && <div className="desc">{subtitle}</div>}</div>
            <div className="x" onClick={() => onClose()}><X size={18} /></div>
          </div>

          <div className="chips">
            {list.map((v) => <span key={v} className="chip">{v}<span className="cx" onClick={() => remove(v)}><X size={11} /></span></span>)}
            {!list.length && <span className="empty">{t('tagNone')}</span>}
          </div>

          <div className="inrow">
            <input
              value={input} onChange={(e) => setInput(e.target.value)} className="tin" placeholder={t('tagAddPh')}
              onKeyUp={(e) => { if (e.key === 'Enter') add(input) }}
            />
            <button className="addb" disabled={!norm(input)} onClick={() => add(input)}><Plus size={15} /></button>
          </div>

          {freeSuggest.length > 0 && (
            <>
              <div className="sl">{t('tagSuggest')}</div>
              <div className="sugs">{freeSuggest.map((s) => <span key={s} className="sug" onClick={() => add(s)}><Plus size={11} />{s}</span>)}</div>
            </>
          )}
        </div>

        <div className="footer">
          <VButton variant="secondary" onClick={() => onClose()}>{t('btnCancel')}</VButton>
          <VButton variant="primary" onClick={() => onSave(list)}>{t('btnSave')}</VButton>
        </div>
      </div>
    </div>
  )
}
