import type { ReactNode } from 'react'
import { Loader2, Inbox, TriangleAlert } from 'lucide-react'
import { useTranslation } from 'react-i18next'

export function Loading() {
  const { t } = useTranslation()
  return <div className="c-state"><Loader2 size={18} className="spin" />{t('loading')}…</div>
}

export function ErrorState({ error, retry }: { error: unknown; retry?: () => void }) {
  const { t } = useTranslation()
  return (
    <div className="c-state err">
      <TriangleAlert size={18} />
      <span>{(error as Error)?.message || t('errTitle')}</span>
      {retry && <button className="c-btn v-ghost" onClick={retry}>{t('retry')}</button>}
    </div>
  )
}

/**
 * 空状态。
 *
 * 原型的约定:四种「空」要分开呈现 —— 尚未同步 / 连不上 / 无权限 / 真的空。
 * 它们在界面上长得一样,但只有最后一种是正常的,所以 hint 这个参数不是可选的
 * 修饰,是让调用方必须说清楚是哪一种。
 */
export function Empty({ hint }: { hint?: ReactNode }) {
  const { t } = useTranslation()
  return <div className="c-state"><Inbox size={18} />{hint || t('empty')}</div>
}
