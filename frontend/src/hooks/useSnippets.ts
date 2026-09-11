import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { snippetLimitsQueryOptions, snippetsQueryOptions, terminalApi } from '@/api/modules/terminal'
import { CODE_OK } from '@/api/codes'
import { useUIStore } from '@/stores/ui'
import type { SnippetLimits, TerminalSnippet } from '@/types'

/**
 * 快捷脚本(Alt+1…9)。
 *
 * 存在服务端而不是浏览器里:换一台机器、换一个浏览器,这些脚本还在;而且上限
 * (条数、正文字节、名称长度、槽位范围)由服务端说了算 —— 前端把同一套数字再写
 * 一遍,只会在某一次改配置之后和它分叉,表现为"界面说能存,保存回来说不能"。
 */
export function useSnippets() {
  return useQuery(snippetsQueryOptions())
}

export function useSnippetLimits() {
  return useQuery(snippetLimitsQueryOptions())
}

/** 槽位 → 脚本。0 表示存了但没绑键,不进这张表。 */
export function snippetsBySlot(list: TerminalSnippet[] | undefined): Record<number, TerminalSnippet> {
  const m: Record<number, TerminalSnippet> = {}
  for (const s of list ?? []) if (s.slot >= 1 && s.slot <= 9) m[s.slot] = s
  return m
}

/** 兜底上限:服务端还没答上来时先给一份保守值,不拿它去覆盖服务端的裁决。 */
export const FALLBACK_LIMITS: SnippetLimits = { maxBytes: 8192, maxName: 64, maxSlot: 9, max: 50 }

/**
 * 保存(新建或改)。
 *
 * 拿的是**原始信封**:服务端拒绝时那句话才是真正有用的信息(名称太长 / 正文超出
 * 字节上限 / 槽位越界),而把它压成一句通用的"保存失败",人就只能自己猜是哪一条。
 */
export function useSaveSnippet() {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return useMutation({
    mutationFn: (v: { id: number; name: string; body: string; slot: number }) =>
      terminalApi.snippetSave(v.id, { name: v.name, body: v.body, slot: v.slot }),
    onSuccess: (env) => {
      if (env.code !== CODE_OK) {
        notify(env.msg || t('snipSaveFail'), 'error')
        return
      }
      qc.invalidateQueries({ queryKey: ['snippets'] })
      notify(t('saved'), 'ok')
    },
    onError: (e: Error) => notify(e.message || t('snipSaveFail'), 'error'),
  })
}

export function useDeleteSnippet() {
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const { t } = useTranslation()
  return useMutation({
    mutationFn: (id: number) => terminalApi.snippetDelete(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['snippets'] })
      notify(t('saved'), 'ok')
    },
    onError: (e: Error) => notify(e.message || t('snipDelFail'), 'error'),
  })
}
