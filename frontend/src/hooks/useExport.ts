import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { exportConfigQueryOptions, exportJobsQueryOptions, terminalApi } from '@/api/modules/terminal'
import { CODE_EXPORT_PATH_UNSET, CODE_OK } from '@/api/http'
import { useUIStore } from '@/stores/ui'
import type { ExportJob } from '@/types'
import { downloadBlob } from '@/lib/download'

export function useExportJobs() {
  return useQuery(exportJobsQueryOptions())
}

export function useExportConfig() {
  return useQuery(exportConfigQueryOptions())
}

export interface ExportVars {
  connectionId: number
  sql: string
  name: string
  database: string
  includeSensitive: boolean
}

/**
 * 建一个导出任务。
 *
 * **没有乐观更新**:任务能不能建起来,取决于服务端有没有配导出目录(42601)、勾了
 * 原值之后要不要先转审批。先在列表里塞一条"执行中"再回滚,等于把这两种结果都先
 * 说成第三种。
 */
export function useCreateExport() {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return useMutation({
    mutationFn: (v: ExportVars) =>
      terminalApi.exportData(v.connectionId, v.sql, v.name, v.database, v.includeSensitive),
    onSuccess: (env) => {
      qc.invalidateQueries({ queryKey: ['export-jobs'] })
      if (env.code === CODE_OK) notify(t('exportSubmitted'), 'ok')
      else if (env.code === CODE_EXPORT_PATH_UNSET) notify(t('exportNoPath'), 'error')
      else notify(env.msg || t('exportSubmitFailed'), 'error')
    },
    onError: (e: Error) => notify(e.message, 'error'),
  })
}

/** 一个任务的分卷清单。files 是换行拼起来的服务器路径。 */
export function partsOf(j: ExportJob): string[] {
  return j.files ? j.files.split('\n').map((f) => f.trim()).filter(Boolean) : []
}

/**
 * 下载一个任务的全部分卷。
 *
 * 分卷**串行**取,中间隔一下:一次性触发五个下载会被浏览器当成弹窗轰炸拦掉,
 * 而被拦掉的那几卷不会有任何提示 —— 人只会以为导出少了半份数据。
 */
export function useDownloadExport() {
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return useMutation({
    mutationFn: async (j: ExportJob) => {
      for (const file of partsOf(j)) {
        const blob = await terminalApi.exportDownload(file)
        downloadBlob(file.split(/[\\/]/).pop() || 'export.zip', blob)
        await new Promise((r) => setTimeout(r, 250))
      }
    },
    onError: () => notify(t('downloadFailed'), 'error'),
  })
}
