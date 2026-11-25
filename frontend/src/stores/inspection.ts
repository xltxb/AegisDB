import { defineStore } from 'pinia'
import { ref } from 'vue'
import api from '@/api'
import { useAuthStore } from '@/stores/auth'
import type { InspectionLatestRow, InspectionReport } from '@/types'

// 每个实例最近一份巡检报告的摘要,给终端实例树、实例管理页的巡检标记共用(.scratch/inspection-report 工单 05)。
// 缓存 5 分钟:标记只是提醒,不必每次打开页面都去拉。看不了巡检的角色不请求,也就没有标记。
const TTL = 5 * 60 * 1000

export const useInspectionStore = defineStore('inspection', () => {
  const byConn = ref<Record<number, InspectionReport>>({})
  const rows = ref<InspectionLatestRow[]>([])
  const today = ref('')
  let loadedAt = 0
  let inflight: Promise<void> | null = null

  async function load(force = false) {
    const auth = useAuthStore()
    if (!auth.canViewInspection) return
    if (!force && Date.now() - loadedAt < TTL) return
    if (inflight) return inflight
    inflight = (async () => {
      try {
        const r = await api.inspectionLatest()
        rows.value = r.items
        today.value = r.today
        const m: Record<number, InspectionReport> = {}
        for (const it of r.items) if (it.report) m[it.connectionId] = it.report
        byConn.value = m
        loadedAt = Date.now()
      } catch { /* 拿不到就不显示标记,不打扰 */ } finally { inflight = null }
    })()
    return inflight
  }
  return { byConn, rows, today, load }
})
