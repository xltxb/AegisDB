import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import clsx from 'clsx'
import { TriangleAlert, Hammer, CircleCheck, CircleX } from 'lucide-react'
import { connectionsApi } from '@/api/modules/connections'
import { confirmAction } from '@/lib/confirm'
import { CODE_OK } from '@/api/codes'
import { useUIStore } from '@/stores/ui'
import { Modal } from '@/components/common/Modal'
import type { RecompileReport } from '@/types'

/**
 * 一个 Oracle owner 下的 INVALID 对象,以及「全部重编译」。
 *
 * 只在**真有**无效对象时出现。一行"0 个无效对象"挂在每个 schema 下面,在一棵已经
 * 很密的树里是纯噪音;而这一行是红的、有数字、能点 —— 它出现本身就是那条信息。
 *
 * 清点失败要说出来,不能静默当成"没有":静默的表现是这一行不出现,读起来正好是
 * "这个 schema 很干净"。
 */
export function InvalidObjects({
  cid, scope, database = '', oracle,
}: { cid: number; scope: string; database?: string; oracle: boolean }) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const notify = useUIStore((s) => s.notify)
  const [report, setReport] = useState<RecompileReport | null>(null)

  const key = ['invalid-objects', cid, scope, database] as const
  const q = useQuery({
    queryKey: key,
    queryFn: () => connectionsApi.invalidObjects(cid, scope, database),
    enabled: oracle && !!scope,
    retry: false,
    staleTime: 60_000,
  })

  const recompile = useMutation({
    mutationFn: () => connectionsApi.recompileInvalid(cid, { scope, database }),
    onSuccess: (env) => {
      if (env.code !== CODE_OK) { notify(env.msg || t('actionFailed'), 'error'); return }
      setReport(env.data.report)
      // 清单按编译后的**真实状态**重取,而不是按"我以为都修好了"就地清空。
      qc.invalidateQueries({ queryKey: key })
    },
    onError: (e: Error) => notify(e.message || t('actionFailed'), 'error'),
  })

  if (!oracle) return null
  if (q.error) return <div className="tv-hint err">{(q.error as Error).message || t('invLoadFail')}</div>
  const items = q.data?.items ?? []
  if (!items.length) return null

  function onRecompile() {
    // 后端会把整批一起判定、判不过整批都不执行;但"我以为只是刷新一下清单"这种
    // 误触,得在这里就挡住。
    if (!confirmAction(t('invConfirm', { n: items.length, schema: items[0].owner }))) return
    recompile.mutate()
  }

  return (
    <>
      <div className="tv-inv">
        <div className="tv-inv-row">
          <TriangleAlert size={12} />
          <span>{t('invTitle')}</span>
          <span className="tv-inv-n">{items.length}</span>
          <button className="tv-inv-btn" disabled={recompile.isPending} onClick={onRecompile}>
            <Hammer size={11} />{recompile.isPending ? t('invRunning') : t('invRecompile')}
          </button>
        </div>
        {items.map((o) => (
          <div key={o.kind + o.name} className="tv-inv-obj" title={o.units.join(' · ')}>
            <span className="tv-inv-kind">{o.kind}</span>{o.name}
          </div>
        ))}
      </div>

      {/* 结果逐个列,而不是只给一句"完成" —— 编不过的那几个才是要看的东西。 */}
      <Modal
        open={!!report}
        width={720}
        title={t('invReportTitle', { schema: report?.owner ?? '' })}
        onClose={() => setReport(null)}
      >
        {report && (
          <>
            <div className="rc-sum">
              <span className="rc-chip ok"><CircleCheck size={12} />{t('invFixed', { n: report.fixed })}</span>
              <span className={clsx('rc-chip', report.failed && 'bad')}><CircleX size={12} />{t('invFailed', { n: report.failed })}</span>
              {!!report.skipped && <span className="rc-chip warn">{t('invSkipped', { n: report.skipped })}</span>}
              <span className="rc-meta">{t('invPasses', { n: report.passes })} · {report.ms}ms</span>
            </div>
            <div className="rc-body">
              {report.items.map((it) => (
                <div key={it.kind + it.name} className="rc-item">
                  <div className="rc-item-head">
                    <span className={clsx('rc-status', it.status.toLowerCase())}>{it.status}</span>
                    <span className="rc-kind">{it.kind}</span>
                    <span className="rc-name">{it.name}</span>
                  </div>
                  {it.err && <div className="rc-diag">{it.err}</div>}
                  {(it.errors ?? []).map((d, i) => (
                    <div key={i} className="rc-diag">
                      <span className="rc-loc">{d.type} {d.line}:{d.position}</span>{d.text}
                    </div>
                  ))}
                </div>
              ))}
            </div>
          </>
        )}
      </Modal>
    </>
  )
}
