import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import clsx from 'clsx'
import { Segmented } from '@/components/common/Segmented'
import { useApprovals, useExecuteApproval } from '@/hooks/useApprovals'
import type { ApprovalScope } from '@/api/modules/approvals'
import type { Approval } from '@/types'

const STATUS_KEY: Record<string, string> = {
  pending: 'apPending', approved: 'apApproved', rejected: 'apRejected',
  expired: 'apExpired', cancelled: 'apCancelled',
}

export default function ApprovalsPage() {
  const { t } = useTranslation()
  const [scope, setScope] = useState<ApprovalScope>('mine')
  const { data, isLoading, error } = useApprovals(scope)
  const exec = useExecuteApproval(scope)

  const rows: Approval[] = data?.items ?? []

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>{t('apTitle')}</h1>
          <p>{t('apSubtitle')}</p>
        </div>
        {/* 用通用的 Segmented。这里原来写的是裸 `.seg` / `.seg-item` —— 样式表里
            没有这两个类(实际的是 `.c-seg` / `.c-seg-item`),所以选中的那一段一直
            看不出被选中。 */}
        <Segmented
          value={scope}
          options={[
            { value: 'mine', label: t('apScopeMine') },
            { value: 'all', label: t('apScopeAll') },
          ]}
          onChange={setScope}
        />
      </header>

      {isLoading && <div className="hint">{t('loading')}…</div>}
      {error && <div className="hint err">{(error as Error).message}</div>}
      {!isLoading && !error && !rows.length && <div className="hint">{t('empty')}</div>}

      <div className="cards">
        {rows.map((a) => (
          <article key={a.id} className="card">
            <div className="card-top">
              <span className="apno">{a.apNo}</span>
              <span className={clsx('badge', `st-${a.status}`)}>{t(STATUS_KEY[a.status] ?? a.status)}</span>
            </div>
            <pre className="cmd">{a.command}</pre>
            <dl className="meta">
              <div><dt>{t('apInitiator')}</dt><dd>{a.initiator}</dd></div>
              <div><dt>{t('apTarget')}</dt><dd>{a.instance}{a.database ? ` · ${a.database}` : ''}</dd></div>
              {a.reason && <div><dt>{t('apReason')}</dt><dd>{a.reason}</dd></div>}
            </dl>

            {/*
              通过 ≠ 执行(ADR 0010)。按钮亮不亮**读服务端的 canExecute**,不在这里
              用 status / executedAt / 是不是发起人自己拼一套 —— 两份判断迟早不一致,
              而不一致的样子就是一个亮着却点不动的按钮。
            */}
            {a.canExecute && (
              <div className="card-act">
                <span className="waiting">{t('apWaitingExec')}</span>
                <button
                  className="btn-primary"
                  disabled={exec.isPending}
                  onClick={() => exec.mutate({ id: a.id })}
                >
                  {exec.isPending ? t('apExecuting') : t('apExecute')}
                </button>
              </div>
            )}
          </article>
        ))}
      </div>
    </div>
  )
}
