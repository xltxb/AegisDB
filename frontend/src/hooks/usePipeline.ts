import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import {
  pipelineApi, pipelineQueryKeys, pipelinesQueryOptions, releaseQueryOptions, releasesQueryOptions,
} from '@/api/modules/pipeline'
import { CODE_MFA_REQUIRED, CODE_OK } from '@/api/http'
import { useUIStore } from '@/stores/ui'
import type { Pipeline } from '@/types'

export type ReleaseScope = 'mine' | 'all'

export function useReleases(scope: ReleaseScope) {
  return useQuery(releasesQueryOptions(scope))
}

/**
 * 打开的那一张单单独取一次。
 *
 * 列表接口只带够列表用的字段,阶段是详情才有的 —— 而阶段正是这一页的全部内容,
 * 所以选中哪张就跟哪张,轮询也各自按自己的终态停。
 */
export function useRelease(id: number) {
  return useQuery(releaseQueryOptions(id))
}

export function usePipelines() {
  return useQuery(pipelinesQueryOptions())
}

/** 工单的任何改动都会同时影响列表与详情,两处一起失效。 */
function useRunFeedback(okText: string) {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  return {
    onSuccess: () => {
      notify(okText, 'ok')
      qc.invalidateQueries({ queryKey: pipelineQueryKeys.releases })
      qc.invalidateQueries({ queryKey: pipelineQueryKeys.release })
    },
    onError: (e: Error) => notify(e.message, 'error'),
  }
}

export interface CreateReleaseBody {
  title: string
  pipelineId: number
  connectionId: number
  database?: string
  sql?: string
  changeType?: string
  reason?: string
  mfaCode?: string
}

/**
 * 建单。信封原样交回给调用方,因为 42800 不是失败。
 *
 * 「该实例要二次验证」带着的意思是"再输一次动态码就能提交",抛成 Error 会把它
 * 碾平成一句红字,表单也就不知道该把验证码那一栏亮出来。
 */
export function useCreateRelease() {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()

  return useMutation({
    mutationFn: async (body: CreateReleaseBody) => {
      const env = await pipelineApi.createRelease(body)
      if (env.code !== CODE_OK && env.code !== CODE_MFA_REQUIRED) throw new Error(env.msg)
      return env
    },
    onSuccess: (env) => {
      if (env.code === CODE_MFA_REQUIRED) {
        notify(t('chgNeedMfa'), 'error')
        return
      }
      notify(t('chgSubmitted'), 'ok')
      qc.invalidateQueries({ queryKey: pipelineQueryKeys.releases })
    },
    onError: (e: Error) => notify(e.message, 'error'),
  })
}

export function useAbortRelease() {
  const { t } = useTranslation()
  const fb = useRunFeedback(t('chgAborted'))
  return useMutation({
    mutationFn: async (id: number) => {
      const env = await pipelineApi.abortRelease(id)
      if (env.code !== CODE_OK) throw new Error(env.msg)
      return env
    },
    ...fb,
  })
}

/**
 * 推进一道人工闸。
 *
 * 服务端按阶段类型分流:`manual` 是流程里配置的确认点,`execute` 是内置的执行闸
 * —— 执行阶段**一律先停在这里**,有人点过才真的下发。前端这边只有一个入口,
 * 语义差别交给按钮文案。
 */
export function useContinueStage() {
  const { t } = useTranslation()
  const fb = useRunFeedback(t('chgAdvanced'))
  return useMutation({
    mutationFn: async ({ releaseId, stageId }: { releaseId: number; stageId: number }) => {
      const env = await pipelineApi.continueStage(releaseId, stageId)
      if (env.code !== CODE_OK) throw new Error(env.msg)
      return env
    },
    ...fb,
  })
}

function usePipelineFeedback() {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return {
    onSuccess: () => {
      notify(t('saved'), 'ok')
      qc.invalidateQueries({ queryKey: pipelineQueryKeys.pipelines })
    },
    onError: (e: Error) => notify(e.message, 'error'),
  }
}

export function useSavePipeline() {
  const fb = usePipelineFeedback()
  return useMutation({
    mutationFn: async (p: Pipeline) => {
      const env = await pipelineApi.savePipeline(p.id, {
        name: p.name.trim(), description: p.description, tierCode: p.tierCode,
        enabled: p.enabled, isDefault: p.isDefault, stages: p.stages,
      })
      if (env.code !== CODE_OK) throw new Error(env.msg)
      return env.data
    },
    ...fb,
  })
}

export function useDeletePipeline() {
  const fb = usePipelineFeedback()
  return useMutation({
    mutationFn: async (id: number) => {
      const env = await pipelineApi.deletePipeline(id)
      if (env.code !== CODE_OK) throw new Error(env.msg)
      return env
    },
    ...fb,
  })
}
