import { useMutation, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { auditApi, auditQueryOptions } from '@/api/modules/audit'
import { useUIStore } from '@/stores/ui'
import type { AuditQuery } from '@/types'
import { downloadBlob } from '@/lib/download'

export function useAudit(q: AuditQuery) {
  return useQuery(auditQueryOptions(q))
}

/**
 * 导出当前过滤条件下的全部记录。
 *
 * 不是 query 而是 mutation:它不是这一页的状态,而是一个按下去才发生的动作,重进
 * 页面不该自己再导一次。失败会经 onError 变成一条 toast —— 导出静默失败的样子是
 * "点了没反应",而人会一直点。
 */
export function useAuditExport() {
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return useMutation({
    mutationFn: async (q: AuditQuery) => {
      const blob = await auditApi.exportCsv(q)
      /*
        后端一律以 HTTP 200 回话,业务结果在信封里(§06) —— 令牌过期、菜单没开,
        回来的都是一段 JSON,而不是 CSV。responseType 是 blob 时 axios 不会替我们
        发现这件事,照单全收地存下去,结果就是一个名叫 audit_export.csv、内容是
        `{"code":40100}` 的文件,而界面上什么都没说。
        Content-Type 是这里唯一能分辨两者的东西,所以在落盘之前先看它一眼。
      */
      if (blob.type.includes('json')) {
        const env = JSON.parse(await blob.text()) as { msg?: string }
        throw new Error(env.msg || t('exportFailed'))
      }
      downloadBlob('audit_export.csv', blob)
    },
    onSuccess: () => notify(t('auditExported'), 'ok'),
    onError: (e: Error) => notify(e.message || t('exportFailed'), 'error'),
  })
}
