import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { Database } from 'lucide-react'
import { connectionSchemaQueryOptions } from '@/api/modules/connections'
import { useSetDatabaseProject } from '@/hooks/useProjects'
import { Loading, Empty } from '@/components/common/States'
import type { Connection, Project } from '@/types'

/**
 * 一台实例底下的库,以及每个库归哪个项目。
 *
 * 库清单是**实时探查**出来的:对一台真连接意味着一次网络往返,所以这块只在行被
 * 展开时才挂载 —— 没人看的时候不该替他付这个钱。组件一挂载 query 就跑,卸载即停,
 * 展开态由父组件持有。
 *
 * 归属的单位是**库**而不是实例:一台实例底下的几个库分属不同团队是常态。
 */
export function DbPanel({ conn, projects }: { conn: Connection; projects: Project[] }) {
  const { t } = useTranslation()
  const { data, isLoading, error } = useQuery(connectionSchemaQueryOptions(conn.id))
  const assign = useSetDatabaseProject()

  const dbs = data?.databases ?? []
  /*
   * 四种「空」分开说。
   *
   * 连不上时服务端把原因放在 `error` 字段里而不是抛错 —— 原样说出来:空清单配一句
   * "没有库"会把一次网络故障伪装成事实。请求本身失败(超时、无权限)走 query 的
   * error 通道,同样照原样显示。
   */
  const probeError = (error as Error | null)?.message || data?.error || ''

  return (
    <div className="conn-dbs">
      {isLoading && <Loading />}
      {!isLoading && probeError && <Empty hint={`${t('connDbUnreachable')}:${probeError}`} />}
      {!isLoading && !probeError && !dbs.length && <Empty hint={t('connDbNone')} />}

      {!isLoading && !probeError && dbs.map((db) => (
        <div key={db.name} className="conn-db">
          <Database size={12} />
          <span className="conn-db-name">{db.name}</span>
          <select
            className="conn-db-sel"
            title={t('connProject')}
            disabled={assign.isPending}
            value={db.projectId || 0}
            onChange={(e) => assign.mutate({
              connectionId: conn.id,
              database: db.name,
              projectId: Number(e.target.value),
            })}
          >
            {/* 0 = 未归属。它是一个合法状态,不是"还没填好"。 */}
            <option value={0}>{t('connNoProject')}</option>
            {projects.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
          </select>
        </div>
      ))}

      {/* 项目只决定归谁跟进;谁碰得到这个库仍然由标签与角色决定。两者并排出现在
          同一行里,不说清楚迟早有人以为改了归属就改了权限。 */}
      <div className="conn-db-hint">{t('prScopeHint')}</div>
    </div>
  )
}
