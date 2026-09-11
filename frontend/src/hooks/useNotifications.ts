import { useEffect } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { notificationsApi, notificationsQueryOptions } from '@/api/modules/notifications'
import { useUIStore, type Toast } from '@/stores/ui'
import type { Notification } from '@/types'

/** 好消息:审批过了、导出跑完了、发布完成了。 */
const OK_TYPES = ['approval-approved', 'export-done', 'release-done']
/** 坏消息:被驳回、导出失败、发布失败。 */
const BAD_TYPES = ['approval-rejected', 'export-failed', 'release-failed']

/** 通知类型 → 色调。其余(超时、等待处理)都是"需要你回去看一眼",算中性。 */
export function toneOfNotification(type: string): 'ok' | 'bad' | 'warn' {
  if (OK_TYPES.includes(type)) return 'ok'
  if (BAD_TYPES.includes(type)) return 'bad'
  return 'warn'
}

function toastKindOf(type: string): Toast['kind'] {
  const tone = toneOfNotification(type)
  return tone === 'ok' ? 'ok' : tone === 'bad' ? 'error' : 'info'
}

export function useNotifications() {
  return useQuery(notificationsQueryOptions())
}

/**
 * 打开面板即全部标已读。
 *
 * 不做乐观更新:未读数是服务端的账,先把徽标清零再失败,人会以为自己已经看过了。
 */
export function useMarkNotificationsRead() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (ids: number[] = []) => notificationsApi.markRead(ids),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['notifications'] }),
  })
}

/**
 * 已经弹过的通知 id,以及"这一轮之前的都不算新"的水位线。
 *
 * 放在模块作用域而不是组件里:外壳会因为路由、主题、语言反复重渲染,挂在组件状态
 * 上的账本会跟着组件的生命周期一起没,于是同一条通知被弹第二次。
 */
let toastedIds = new Set<number>()
/** 已经完成首轮记账的那个人的 id;null = 还没有基线。 */
let primedFor: number | null = null

/**
 * 新到的通知弹一条 toast。
 *
 * 审批出结果、异步导出跑完,人早就离开那个页面了 —— 不主动说一声,他只能自己想起来
 * 回去刷一下;铃铛上的红点不算"说一声",它要人先看见、再点开。
 *
 * **首轮只记账、不弹窗**:否则每次刷新页面都会把历史通知重演一遍 —— 三天前那次导出
 * 完成的提示,今天早上再弹一次,人很快就学会无视所有弹窗。这里要的是"刚刚完成了",
 * 不是"曾经完成过"。
 */
export function useNotificationToasts(items: Notification[] | undefined, userId: number | undefined) {
  const notify = useUIStore((s) => s.notify)
  useEffect(() => {
    if (!items || !userId) return
    // 换了人就重来。单页应用里退出再登录不会重新加载模块,沿用上一个人的水位线会
    // 把新账号的历史通知当成"刚到的"整批弹出来。
    if (primedFor !== null && primedFor !== userId) {
      toastedIds = new Set()
      primedFor = null
    }
    const primed = primedFor !== null
    for (const n of items) {
      if (toastedIds.has(n.id)) continue
      toastedIds.add(n.id)
      if (!primed || n.read) continue
      notify(n.body ? `${n.title} — ${n.body}` : n.title, toastKindOf(n.type))
    }
    primedFor = userId
  }, [items, userId, notify])
}
