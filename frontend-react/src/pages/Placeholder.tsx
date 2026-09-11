import { useTranslation } from 'react-i18next'

/**
 * 尚未迁移的页面。**刻意不画假界面** —— 一个填着假数据的壳子会让人以为功能已经
 * 好了,等真接上去才发现全不对。这里只说明这一页还没迁完。
 */
export default function Placeholder({ titleKey }: { titleKey: string }) {
  const { t } = useTranslation()
  return (
    <div className="placeholder">
      <h2>{t(titleKey)}</h2>
      <p>这一页尚未迁移到 React 版本。当前可用实现见 Vue 版前端。</p>
    </div>
  )
}
