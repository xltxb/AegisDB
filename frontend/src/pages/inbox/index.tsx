import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import clsx from 'clsx'
import {
  Check, ChevronLeft, ChevronRight, Clock, Inbox, Info, ShieldAlert, TimerOff, Undo2, UserX, X,
} from 'lucide-react'
import { useApprovals, useDecideApproval } from '@/hooks/useApprovals'
import { approvalsApi, type ApprovalStatus } from '@/api/modules/approvals'
import { meQueryOptions } from '@/api/modules/auth'
import { useUIStore } from '@/stores/ui'
import { Badge, toneOfStatus, type BadgeTone } from '@/components/common/Badge'
import { Button } from '@/components/common/Button'
import { Segmented } from '@/components/common/Segmented'
import { Empty, ErrorState, Loading } from '@/components/common/States'
import {
  batchableOf, highPendingCount, initialOf, keywordAt, stepDone, waitingOn,
} from '@/lib/inbox'
import type { Approval } from '@/types'
import { confirmAction } from '@/lib/confirm'

const STATUS_KEY: Record<string, string> = {
  pending: 'apPending', approved: 'apApproved', rejected: 'apRejected',
  expired: 'apExpired', cancelled: 'apCancelled',
}

const STATUS_ICON: Record<string, typeof Check> = {
  pending: Clock, approved: Check, rejected: X, expired: TimerOff, cancelled: Undo2,
}

/**
 * 审批链一级的状态文案。与工单状态(STATUS_KEY)是两套词 —— 后端给步骤用的是
 * waiting / active / approved / rejected,拿工单那套去查,链上就会印出原始的
 * `active`。
 */
const STEP_KEY: Record<string, string> = {
  waiting: 'ibStepWaiting', active: 'ibStepActive',
  approved: 'apApproved', rejected: 'apRejected',
}

const RISK_TONE: Record<string, BadgeTone> = { high: 'danger', mid: 'warning', low: 'success' }
const RISK_KEY: Record<string, string> = { high: 'scanHigh', mid: 'scanMid', low: 'scanSafe' }

const at = (s: string) => (s ? s.slice(5, 16).replace('T', ' ') : '—')

export default function InboxPage() {
  const { t } = useTranslation()
  const notify = useUIStore((s) => s.notify)
  const { data: me } = useQuery(meQueryOptions())
  const [tab, setTab] = useState<'pending' | 'all'>('pending')
  const [page, setPage] = useState(1)
  const [selId, setSelId] = useState(0)
  const [busy, setBusy] = useState(false)

  const status: ApprovalStatus = tab === 'pending' ? 'pending' : ''
  // scope 固定 mine —— 收件箱是「轮到我签字的」,后端的 mine 正是"我在审批链上"
  // (repository.approvalScope)。用 all 会把我自己发起的、我能执行的一起拖进来,
  // 那是「我的申请」页的范围。
  const { data, isLoading, error, refetch } = useApprovals('mine', page, status)
  const decide = useDecideApproval()

  /**
   * 「全部」那一格的计数。
   *
   * 单独一个 pageSize=1 的查询,而不是等人切过去才有数 —— 站在「待审」上时,
   * 手头这一页的 total 数的是待审那一批,拿它当全部会少报。两个格子的数字都要
   * 在同一眼里是真的。
   */
  const allCount = useQuery({
    queryKey: ['approvals', 'mine', 'count-all'] as const,
    queryFn: () => approvalsApi.list('mine', 1, 1, '').then((r) => r.total),
  })

  const rows: Approval[] = data?.items ?? []
  const total = data?.total ?? 0
  const pendingN = data?.pending ?? 0
  const pages = Math.max(1, Math.ceil(total / 20))
  const sel = rows.find((a) => a.id === selId)

  const highN = highPendingCount(rows)
  const batchable = batchableOf(rows)

  async function batchApprove() {
    if (!batchable.length) {
      notify(t('ibBatchNone'), 'info')
      return
    }
    if (!confirmAction(t('ibBatchConfirm', { n: batchable.length }))) return
    setBusy(true)
    let done = 0
    const failed: string[] = []
    // 一张一张发,不并发:每张都是一次独立的判定与审计写入,而后端在这一步还会
    // 再判一次可决定性。串行让失败停在那一张上,也让失败清单说得出是哪几张。
    for (const a of batchable) {
      try {
        await approvalsApi.approve(a.id)
        done += 1
      } catch (e) {
        failed.push(`${a.apNo}: ${(e as Error).message}`)
      }
    }
    setBusy(false)
    notify(
      failed.length
        ? t('ibBatchPartial', { n: done, f: failed.length })
        : t('ibBatchDone', { n: done }),
      failed.length ? 'error' : 'ok',
    )
    refetch()
  }

  return (
    <div className="page ib-page">
      <header className="page-head">
        <div>
          <h1>{t('ibTitle')}</h1>
          <p>{t('ibSub')}</p>
        </div>
      </header>

      <div className="ib-grid">
        <div className="ib-main">
          <div className="ib-bar">
            <Segmented
              value={tab}
              options={[
                { value: 'pending', label: `${t('ibPending')} · ${pendingN}` },
                { value: 'all', label: `${t('ibAll')} · ${allCount.data ?? total}` },
              ]}
              onChange={(v) => { setTab(v); setPage(1); setSelId(0) }}
            />
            <div className="grow">
              <Button
                variant="secondary"
                disabled={busy || !batchable.length}
                onClick={batchApprove}
              >
                {busy ? t('ibBatching') : t('ibBatch')}
              </Button>
            </div>
          </div>

          {highN > 0 && (
            <div className="ib-warn">
              <ShieldAlert size={15} />
              <span><strong>{highN}</strong> {t('ibHighNote')}</span>
            </div>
          )}

          {isLoading && <Loading />}
          {error && <ErrorState error={error} retry={() => refetch()} />}

          {!isLoading && !error && (
            rows.length ? (
              <>
                <div className="ib-list">
                  {rows.map((a) => (
                    <Row
                      key={a.id}
                      a={a}
                      mine={!!me && a.initiatorId === me.id}
                      on={a.id === selId}
                      onPick={() => setSelId(a.id)}
                    />
                  ))}
                </div>
                {pages > 1 && (
                  <div className="page-foot">
                    <div className="pager">
                      {/* 翻页会把选中清掉:详情栏里那张单已经不在左边的清单上了,
                          留着它会让「通过」作用在一张看不见的行上。 */}
                      <button
                        className="pg"
                        disabled={page <= 1}
                        onClick={() => { setPage((p) => Math.max(1, p - 1)); setSelId(0) }}
                      >
                        <ChevronLeft size={15} />
                      </button>
                      <span className="pgn">{t('audPageOf', { p: page, n: pages })}</span>
                      <button
                        className="pg"
                        disabled={page >= pages}
                        onClick={() => { setPage((p) => Math.min(pages, p + 1)); setSelId(0) }}
                      >
                        <ChevronRight size={15} />
                      </button>
                    </div>
                  </div>
                )}
              </>
            ) : (
              <div className="ib-empty">
                <Inbox size={26} />
                <span>{t('ibEmpty')}</span>
              </div>
            )
          )}
        </div>

        <aside className="ib-side">
          {sel ? (
            <Detail
              a={sel}
              busy={decide.isPending}
              onDecide={(approve) => {
                if (!approve && !confirmAction(t('ibRejectConfirm', { no: sel.apNo }))) return
                decide.mutate({ id: sel.id, approve })
              }}
            />
          ) : (
            <Empty hint={t('ibPickHint')} />
          )}
        </aside>
      </div>
    </div>
  )
}

/** 命令里把命中的关键字标红。keyword 为空(或没出现在命令里)就原样显示。 */
function Command({ command, keyword }: { command: string; keyword: string }) {
  const i = keywordAt(command, keyword)
  if (i < 0) return <>{command}</>
  return (
    <>
      {command.slice(0, i)}
      <em className="ib-kw">{command.slice(i, i + keyword.length)}</em>
      {command.slice(i + keyword.length)}
    </>
  )
}

function StatusBadge({ status }: { status: string }) {
  const { t } = useTranslation()
  const Icon = STATUS_ICON[status] ?? Clock
  return (
    <Badge tone={toneOfStatus(status)} icon={<Icon size={11} />}>
      {t(STATUS_KEY[status] ?? status)}
    </Badge>
  )
}

function Row({
  a, mine, on, onPick,
}: { a: Approval; mine: boolean; on: boolean; onPick: () => void }) {
  const { t } = useTranslation()
  const wait = waitingOn(a.steps)

  return (
    <button type="button" className={clsx('ib-row', on && 'on')} onClick={onPick}>
      <div className="ib-row-top">
        <span className="ib-no">{a.apNo}</span>
        <Badge tone={RISK_TONE[a.riskLevel] ?? 'neutral'}>
          {t(RISK_KEY[a.riskLevel] ?? a.riskLevel)}
        </Badge>
        <StatusBadge status={a.status} />
        {/*
          「本人发起」是身份标注,不是按钮的闸。按钮亮不亮读服务端的 canDecide ——
          管理员开了自审批开关时,自己发起的单子是可以自己批的,这个徽标仍然该在,
          因为"这张是我提的"本身值得一眼看见。
        */}
        {mine && (
          <Badge tone="warning" icon={<UserX size={10} />}>{t('ibMine')}</Badge>
        )}
        <span className="ib-when">{at(a.createdAt)}</span>
      </div>

      <div className="ib-cmd"><Command command={a.command} keyword={a.keyword} /></div>

      <div className="ib-row-foot">
        <span className="ib-ini">{initialOf(a.initiator)}</span>
        <span className="ib-who">{a.initiator} · {a.env} · {a.instance}</span>
        <span className="ib-wait">{t('ibWaitOn')}: {wait || '—'}</span>
      </div>
    </button>
  )
}

function Detail({
  a, busy, onDecide,
}: { a: Approval; busy: boolean; onDecide: (approve: boolean) => void }) {
  const { t } = useTranslation()
  const steps = [...(a.steps ?? [])].sort((x, y) => x.stepOrder - y.stepOrder)

  return (
    <>
      <div className="ib-sec">{t('ibDetail')}</div>

      <div className="ib-dhead">
        <span className="ib-dno">{a.apNo}</span>
        <Badge tone={RISK_TONE[a.riskLevel] ?? 'neutral'}>
          {t(RISK_KEY[a.riskLevel] ?? a.riskLevel)}
        </Badge>
        <StatusBadge status={a.status} />
      </div>

      <div className="ib-dcmd"><Command command={a.command} keyword={a.keyword} /></div>

      <div className="ib-kv">
        <div><span>{t('apInitiator')}</span><b>{a.initiator}</b></div>
        <div><span>{t('targetInst')}</span><b>{a.instance}{a.database ? ` · ${a.database}` : ''}</b></div>
        <div><span>{t('apTarget')}</span><b className="warn">{a.env}</b></div>
      </div>

      {a.reason && (
        <>
          <div className="ib-sec sp">{t('apReason')}</div>
          <div className="ib-reason">{a.reason}</div>
        </>
      )}

      <div className="ib-sec sp">{t('chainTitle')}</div>
      {steps.length ? (
        <div className="ib-chain">
          {steps.map((s) => {
            const Icon = stepDone(s.status) ? (s.status === 'rejected' ? X : Check) : Clock
            return (
              <div key={s.id} className="ib-step">
                <span className={clsx('ib-dot', `st-${s.status}`)}><Icon size={12} /></span>
                <div className="ib-step-t">
                  <b>{t('ibStepN', { n: s.stepOrder })}</b>
                  <i>{s.approver}</i>
                </div>
                <span className="ib-step-s">{t(STEP_KEY[s.status] ?? s.status)}</span>
              </div>
            )
          })}
        </div>
      ) : (
        <div className="ib-reason muted">{t('ibNoChain')}</div>
      )}

      {/*
        按钮只在服务端说可以决定时出现。不可决定时把理由摆在原位 —— 「不能审批
        自己发起的工单」和「仅审批链成员可处理」要人做的事完全不同,一个灰按钮
        说不出是哪一条。
      */}
      {a.status === 'pending' && (
        a.canDecide ? (
          <>
            <div className="ib-acts">
              <Button variant="danger" disabled={busy} onClick={() => onDecide(false)}>
                {t('ibReject')}
              </Button>
              <Button variant="primary" disabled={busy} onClick={() => onDecide(true)}>
                {busy ? t('ibDeciding') : t('ibApprove')}
              </Button>
            </div>
            <div className="ib-after">
              <Info size={13} />
              <span>{t('ibAfter')}</span>
            </div>
          </>
        ) : (
          <div className="ib-blocked">
            <ShieldAlert size={13} />
            <span>{a.blockReason || t('ibCannotDecide')}</span>
          </div>
        )
      )}
    </>
  )
}
