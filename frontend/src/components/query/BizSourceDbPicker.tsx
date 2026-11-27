// 选业务查询源与库:用户申请权限(BizApplyModal)与管理员直接开通(QueryGrantCreateModal)
// 共用(issue #184)。打开时加载实例列表,选了实例再拉它的库;加载失败的原因交给 onError。
import { useState } from 'react'
import api from '@/api'
import { useI18n } from '@/hooks/useI18n'
import { useMountEffect } from '@/hooks/effects'
import type { BizApplySource } from '@/types'

export interface BizSourceDb {
  /** null 表示还在加载 */
  sources: BizApplySource[] | null
  sourceId: number
  /** null 表示还没选实例 */
  dbs: string[] | null
  dbLoading: boolean
  database: string
  setDatabase: (db: string) => void
  pickSource: (v: string) => void
}

export function useBizSourceDb(onError: (msg: string) => void): BizSourceDb {
  const { t } = useI18n()
  const [sources, setSources] = useState<BizApplySource[] | null>(null)
  const [sourceId, setSourceId] = useState(0)
  const [dbs, setDbs] = useState<string[] | null>(null)
  const [dbLoading, setDbLoading] = useState(false)
  const [database, setDatabase] = useState('')

  useMountEffect(() => {
    api.bizApplySources().then((xs) => setSources(xs ?? [])).catch((e: any) => { setSources([]); onError(e?.message || t('loadFailed')) })
  })

  async function pickSource(v: string) {
    const id = Number(v) || 0
    setSourceId(id)
    setDatabase('')
    setDbs(null)
    if (!id) return
    setDbLoading(true)
    try {
      setDbs((await api.bizApplyDatabases(id)) ?? [])
    } catch (e: any) {
      setDbs([])
      onError(e?.message || t('loadFailed'))
    } finally { setDbLoading(false) }
  }

  return { sources, sourceId, dbs, dbLoading, database, setDatabase, pickSource }
}

/** 实例、库两个下拉框;testid 是两个框 data-testid 的前缀(`<testid>-source` / `<testid>-db`)。 */
export function BizSourceDbFields({ picker, testid }: { picker: BizSourceDb; testid: string }) {
  const { t } = useI18n()
  const { sources, sourceId, dbs, dbLoading, database, setDatabase, pickSource } = picker
  return (
    <>
      <label className="fl">{t('qaSource')}</label>
      <select className="pin" value={sourceId || ''} disabled={sources === null} data-testid={`${testid}-source`} onChange={(e) => pickSource(e.target.value)}>
        <option value="">{sources === null ? t('treeLoading') : sources.length ? t('qaSourcePh') : t('qaNoSources')}</option>
        {(sources ?? []).map((s) => <option key={s.id} value={s.id}>{s.name}{s.env ? ` · ${s.env}` : ''}</option>)}
      </select>
      <label className="fl">{t('qaDb')}</label>
      <select className="pin" value={database} disabled={!sourceId || dbLoading} data-testid={`${testid}-db`} onChange={(e) => setDatabase(e.target.value)}>
        <option value="">{dbLoading ? t('treeLoading') : dbs && !dbs.length ? t('qaDbNone') : t('qaDbPh')}</option>
        {(dbs ?? []).map((d) => <option key={d} value={d}>{d}</option>)}
      </select>
    </>
  )
}
