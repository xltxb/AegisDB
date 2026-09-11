import { useRouteError, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'

/**
 * 路由级兜底。一页取数失败不该把整个控制台变成白屏 —— 外壳留着,人还能换一页。
 */
export default function RouteError() {
  const err = useRouteError() as Error | undefined
  const { t } = useTranslation()
  const nav = useNavigate()
  return (
    <div className="routeerr">
      <h2>{t('errTitle')}</h2>
      <p>{err?.message || ''}</p>
      <button onClick={() => nav(0)}>{t('errRetry')}</button>
    </div>
  )
}
