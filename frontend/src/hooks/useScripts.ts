import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import {
  scriptScanQueryOptions, scriptUploadContentQueryOptions, scriptUploadsQueryOptions, terminalApi,
} from '@/api/modules/terminal'
import { CODE_OK, CODE_SCRIPT_PATH_UNSET } from '@/api/http'
import { useUIStore } from '@/stores/ui'
import type { ScriptScanResp } from '@/types'

export function useScriptUploads() {
  return useQuery(scriptUploadsQueryOptions())
}

export function useScriptContent(id: number) {
  return useQuery(scriptUploadContentQueryOptions(id))
}

/**
 * 已扫描过的脚本的结果,按 id 取。
 *
 * 刻意**不**在列表加载时把每份脚本都扫一遍:扫描是按语句数线性的,一份 6MB 的迁移
 * 脚本服务端要跑十几秒,十份就是一次两分钟的列表加载。所以只扫人真的打开过的那些,
 * 其余的卡片老老实实显示"未扫描",而不是填一个看起来像真的零。
 */
export function useScriptScans(ids: number[], nameOf: (id: number) => string) {
  const results = useQueries({
    queries: ids.map((id) => scriptScanQueryOptions(id, nameOf(id))),
  })
  const byId = new Map<number, ScriptScanResp>()
  ids.forEach((id, i) => {
    const d = results[i]?.data
    if (d) byId.set(id, d)
  })
  return byId
}

export function useScriptScan(id: number, filename: string) {
  return useQuery(scriptScanQueryOptions(id, filename))
}

/** 上传一份脚本。42600 = 服务端还没配脚本目录,这不是"上传失败",是"没地方放"。 */
export function useUploadScript() {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return useMutation({
    mutationFn: async (file: File) => terminalApi.scriptUpload(await file.text(), file.name),
    onSuccess: (env) => {
      qc.invalidateQueries({ queryKey: ['script-uploads'] })
      if (env.code === CODE_OK) notify(t('scriptsUploaded'), 'ok')
      else if (env.code === CODE_SCRIPT_PATH_UNSET) notify(t('scriptsNoPath'), 'error')
      else notify(env.msg || t('scriptsUploadFailed'), 'error')
    },
    onError: () => notify(t('scriptsUploadFailed'), 'error'),
  })
}

export function useDeleteScriptUpload() {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return useMutation({
    mutationFn: (id: number) => terminalApi.scriptUploadDelete(id),
    onSuccess: () => {
      notify(t('scriptsDeleted'), 'ok')
      qc.invalidateQueries({ queryKey: ['script-uploads'] })
    },
    onError: (e: Error) => notify(e.message || t('scriptsDeleteFailed'), 'error'),
  })
}
