import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { pipelineApi, projectsQueryOptions } from '@/api/modules/pipeline'
import { CODE_OK } from '@/api/http'
import { useUIStore } from '@/stores/ui'
import type { Envelope } from '@/api/shared'
import i18n from '@/locales'

/**
 * 项目 —— 库与升级单的归属。
 *
 * 它和标签(tags)是两件事,界面上也刻意分开说:**标签决定谁碰得到这个库**(判定层
 * 真的会读),**项目决定这个库归谁跟进**(判定层不看)。两者长得像,一旦被当成同一
 * 个东西用,迟早有人以为改了项目就改了权限。
 *
 * 归属的单位是**库**而不是实例:一台实例底下的几个库分属不同团队是常态。
 */
export function useProjects() {
  return useQuery(projectsQueryOptions())
}

/** 项目接口回的是原始信封(没走 ok),这里统一拆:非 0 就是失败,原样抛服务端那句话。 */
function unwrap<T>(env: Envelope<T>): T {
  if (env.code !== CODE_OK) throw new Error(env.msg || i18n.t('reqFailed'))
  return env.data
}

function useProjectFeedback() {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return {
    onSuccess: () => {
      notify(t('saved'), 'ok')
      qc.invalidateQueries({ queryKey: ['projects'] })
    },
    onError: (e: Error) => notify(e.message, 'error'),
    qc,
    notify,
    t,
  }
}

export interface ProjectDraft {
  /** 0 = 新建 */
  id: number
  name: string
  owner: string
  description: string
}

export function useSaveProject() {
  const { onSuccess, onError } = useProjectFeedback()
  return useMutation({
    mutationFn: async (d: ProjectDraft) => {
      const body = { name: d.name.trim(), owner: d.owner.trim(), description: d.description.trim() }
      return unwrap(d.id ? await pipelineApi.updateProject(d.id, body) : await pipelineApi.createProject(body))
    },
    onSuccess,
    onError,
  })
}

/**
 * 删除项目。
 *
 * 名下还有库时**服务端会拒**,并在 msg 里说明还剩几个。这里不预判、也不按本地
 * `databases` 计数抢先拦下:那个数是上一次拉列表时的快照,而拒绝的理由要由真正
 * 做决定的一方给出,才不会出现"界面说能删、点下去失败"的错位。
 */
export function useDeleteProject() {
  const { onSuccess, onError } = useProjectFeedback()
  return useMutation({
    mutationFn: async (id: number) => unwrap(await pipelineApi.deleteProject(id)),
    onSuccess,
    onError,
  })
}

/**
 * 把一台实例底下的一个库挂到项目上(projectId 0 = 取消归属)。
 *
 * 成功后要失效的是**那台实例的库清单**(归属跟着库走,列表是实时探查出来的),
 * 以及项目列表(每个项目名下的库数会变)。
 */
export function useSetDatabaseProject() {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return useMutation({
    mutationFn: async (v: { connectionId: number; database: string; projectId: number }) =>
      unwrap(await pipelineApi.setDatabaseProject(v.connectionId, v.database, v.projectId)),
    onSuccess: (_d, v) => {
      notify(t('saved'), 'ok')
      qc.invalidateQueries({ queryKey: ['connection-schema', v.connectionId] })
      qc.invalidateQueries({ queryKey: ['projects'] })
    },
    onError: (e: Error) => notify(e.message, 'error'),
  })
}
