import { useRouteError } from 'react-router'
import { useI18n } from '@/hooks/useI18n'

// 某一页渲染时抛错的兜底:只替换这一页的内容区。
export default function RouteError() {
  const { t } = useI18n()
  const err = useRouteError()
  return (
    <div role="alert" style={{ padding: 32, font: '500 13px var(--font-body)', color: 'var(--danger-text)' }}>
      {t('actionFailed')}: {err instanceof Error ? err.message : String(err)}
    </div>
  )
}
