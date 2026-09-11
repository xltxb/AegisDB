import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import api from '@/api'
import type { Connection } from '@/types'

/**
 * 数据源领域 store(前端开发文档 §04)。
 *
 * 它存在的直接理由是「选哪个库」这条规则曾经被抄了三遍:导出页、后台执行页、
 * 发布页各写一份 `loadDbs`,三份的代码一样、注释各不相同。抄第三遍的时候没人
 * 会去想它对不对,而它恰恰是个**领域规则**:实例自己填了库就用那个,没填才退回
 * 探查到的第一个 —— 漏掉它,提交上去的导出没有 schema,目标库回一句
 * "No database selected"。
 */
export const useConnectionsStore = defineStore('connections', () => {
  const list = ref<Connection[]>([])
  const loaded = ref(false)

  /** 按环境分组,给左侧树与分段筛选用。 */
  const byEnv = computed<Record<string, Connection[]>>(() => {
    const out: Record<string, Connection[]> = {}
    for (const c of list.value) (out[c.env] ||= []).push(c)
    return out
  })

  function byId(id: number): Connection | undefined {
    return list.value.find((c) => c.id === id)
  }

  /**
   * fetch 拉连接列表。默认只拉一次 —— 多个页面共用这一份,谁先挂载谁触发。
   * 传 force 可在增删改之后刷新。
   */
  async function fetch(force = false): Promise<Connection[]> {
    if (loaded.value && !force) return list.value
    list.value = await api.connections()
    loaded.value = true
    return list.value
  }

  /**
   * databasesOf 探查一台实例的库清单,并按上面那条规则给出应当预选的库。
   *
   * **不抛异常**:探查失败(实例不可达、无凭据)时返回空清单加一句原因。三个调用方
   * 原本各写一个"出错就保持默认"的空 catch —— 把这个约定写进签名,比指望每处都记得写可靠。
   */
  async function databasesOf(id: number): Promise<{ options: string[]; preferred: string; error: string }> {
    if (!id) return { options: [], preferred: '', error: '' }
    try {
      const sc = await api.connectionSchema(id)
      const options = (sc.databases || []).map((d) => d.name)
      const own = byId(id)?.database
      const preferred = own && options.includes(own) ? own : options[0] || ''
      return { options, preferred, error: sc.error || '' }
    } catch (e: any) {
      return { options: [], preferred: '', error: e?.message || String(e) }
    }
  }

  async function create(body: Partial<Connection>) {
    const r = await api.createConnection(body)
    await fetch(true)
    return r
  }

  async function update(id: number, body: Partial<Connection>) {
    const r = await api.updateConnection(id, body)
    await fetch(true)
    return r
  }

  async function toggleStatus(id: number, status?: string) {
    const r = await api.toggleConnection(id, status)
    await fetch(true)
    return r
  }

  const test = (id: number) => api.testConnection(id)

  return { list, loaded, byEnv, byId, fetch, databasesOf, create, update, toggleStatus, test }
})
