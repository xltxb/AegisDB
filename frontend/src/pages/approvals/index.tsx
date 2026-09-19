import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronLeft, ChevronRight, Check, Clock, TimerOff, Undo2, X } from 'lucide-react'
import { Badge, toneOfStatus } from '@/components/common/Badge'
import { Button } from '@/components/common/Button'
import { Segmented } from '@/components/common/Segmented'
import { Empty, ErrorState, Loading } from '@/components/common/States'
import { useApprovals, useExecuteApproval } from '@/hooks/useApprovals'
import { APPROVAL_STATUS_TABS } from '@/lib/approvals'
import type { ApprovalScope, ApprovalStatus } from '@/api/modules/approvals'
import type { Approval } from '@/types'

/**
 * 目标库的显示名 —— 长了只留末段。
 *
 * sqlite 的"库名"就是一条绝对路径。整条摆进去会挤掉同一行的发起人和原因,而交给
 * CSS 截又只能截尾:留下的恰好是每条单子都一样的 /private/tmp/... 头部,唯一有
 * 区分度的文件名被截掉了。完整路径挂在 title 上。
 */
const shortDb = (db: string) =>
  db.length > 32 && db.includes('/') ? db.slice(db.lastIndexOf('/') + 1) : db

const STATUS_KEY: Record<string, string> = {
  pending: 'apPending', approved: 'apApproved', rejected: 'apRejected',
  expired: 'apExpired', cancelled: 'apCancelled',
}

const STATUS_ICON: Record<string, typeof Check> = {
  pending: Clock, approved: Check, rejected: X, expired: TimerOff, cancelled: Undo2,
}

export default function ApprovalsPage() {
  const { t } = useTranslation()
  const [scope, setScope] = useState<ApprovalScope>('mine')
  const [status, setStatus] = useState<ApprovalStatus>('')
  const [page, setPage] = useState(1)
  const { data, isLoading, error, refetch } = useApprovals(scope, page, status)
  const exec = useExecuteApproval()

  const rows: Approval[] = data?.items ?? []
  const total = data?.total ?? 0
  const pages = Math.max(1, Math.ceil(total / 20))

  // 换 scope 或换状态都要回到第 1 页。停在第 3 页换一次筛选,新结果可能只有一页,
  // 于是页面空着而分页器显示「3 / 1」—— 看起来像"没有数据"。
  const reset = <T,>(set: (v: T) => void) => (v: T) => { set(v); setPage(1) }

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>{t('apTitle')}</h1>
          <p>{t('apSubtitle')}</p>
        </div>
        {/*
          用通用的 Segmented。这里原来写的是裸 `.seg` / `.seg-item` —— 样式表里
          没有这两个类(实际的是 `.c-seg` / `.c-seg-item`),所以选中的那一段一直
          看不出被选中。整页都是这个毛病:改版前 15 个类名里有 12 个在 theme.css
          里一条规则都没有,页面渲染出来是裸 HTML。现在能用通用组件的都用通用
          组件(Badge / Button / States / Segmented),剩下的卡片布局才自己写
          `.ap-*`,并且都在 theme.css 里有对应规则。
        */}
        <div className="grow">
          <Segmented
            value={scope}
            options={[
              { value: 'mine', label: t('apScopeMine') },
              { value: 'all', label: t('apScopeAll') },
            ]}
            onChange={reset(setScope)}
          />
        </div>
      </header>

      {/*
        状态筛选走服务端(handler.ListApprovals 的 status 参数)。列表是分页的,
        拿一页回来自己筛,会对一张躺在第三页的单子回答"没有"。
        「已通过待执行」的单尤其如此:通过了的工单不会过期(超时清扫只作废
        pending),所以它可以停很久,早被新工单挤出了任何一页。
      */}
      <div className="ap-bar">
        <Segmented
          value={status}
          options={APPROVAL_STATUS_TABS.map((x) => ({ value: x.value, label: t(x.key) }))}
          onChange={reset(setStatus)}
        />
      </div>

      {isLoading && <Loading />}
      {error && <ErrorState error={error} retry={() => refetch()} />}

      {!isLoading && !error && (
        rows.length ? (
          <>
            <div className="ap-list">
              {rows.map((a) => (
                <article key={a.id} className="ap-card">
                  <div className="ap-top">
                    <span className="ap-no">{a.apNo}</span>
                    <Badge
                      tone={toneOfStatus(a.status)}
                      icon={(() => {
                        const Icon = STATUS_ICON[a.status] ?? Clock
                        return <Icon size={11} />
                      })()}
                    >
                      {t(STATUS_KEY[a.status] ?? a.status)}
                    </Badge>
                  </div>

                  <pre className="cmd">{a.command}</pre>

                  <dl className="ap-kv">
                    <div><dt>{t('apInitiator')}</dt><dd>{a.initiator}</dd></div>
                    <div>
                      <dt>{t('apTarget')}</dt>
                      {/* 库名可能是一条绝对路径(sqlite 的"库名"就是),任其换行会让这一条
                          独占整行。单行省略,全文挂在 title 上。 */}
                      <dd className="ap-target" title={`${a.instance}${a.database ? ` · ${a.database}` : ''}`}>
                        {a.instance}{a.database ? ` · ${shortDb(a.database)}` : ''}
                      </dd>
                    </div>
                    {a.reason && <div><dt>{t('apReason')}</dt><dd>{a.reason}</dd></div>}
                  </dl>

                  {/*
                    通过 ≠ 执行(ADR 0010)。按钮亮不亮**读服务端的 canExecute**,不在这里
                    用 status / executedAt / 是不是发起人自己拼一套 —— 两份判断迟早不一致,
                    而不一致的样子就是一个亮着却点不动的按钮。
                  */}
                  {a.canExecute && (
                    <div className="ap-act">
                      <span className="ap-wait">{t('apWaitingExec')}</span>
                      <Button
                        variant="primary"
                        disabled={exec.isPending}
                        onClick={() => exec.mutate({ id: a.id })}
                      >
                        {exec.isPending ? t('apExecuting') : t('apExecute')}
                      </Button>
                    </div>
                  )}
                </article>
              ))}
            </div>

            <div className="page-foot">
              {/* 总数写出来。分页器只说"第几页",而"一共多少张"是另一个问题 ——
                  改版前这一页两个都不说,翻不到第 21 张,页面上也没有任何迹象
                  说明还有第 21 张。 */}
              <span className="total">{t('apTotal', { n: total })}</span>
              {pages > 1 && (
                <div className="pager">
                  <button
                    className="pg"
                    disabled={page <= 1}
                    onClick={() => setPage((p) => Math.max(1, p - 1))}
                  >
                    <ChevronLeft size={15} />
                  </button>
                  <span className="pgn">{t('audPageOf', { p: page, n: pages })}</span>
                  <button
                    className="pg"
                    disabled={page >= pages}
                    onClick={() => setPage((p) => Math.min(pages, p + 1))}
                  >
                    <ChevronRight size={15} />
                  </button>
                </div>
              )}
            </div>
          </>
        ) : (
          // 「这个筛选下没有」和「一张申请都没有」要人做的事不同:前者改筛选,
          // 后者是真的还没提过单。
          <Empty hint={status ? t('apEmptyFiltered') : t('apEmpty')} />
        )
      )}
    </div>
  )
}
