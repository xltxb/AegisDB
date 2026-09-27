import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import clsx from 'clsx'
import { MessagesSquare } from 'lucide-react'
import { Modal } from '@/components/common/Modal'
import { Button } from '@/components/common/Button'
import { Loading, ErrorState } from '@/components/common/States'
import { confirmAction } from '@/lib/confirm'
import { useIssueImCode, useMyImIdentities, useUnbindMyIm } from '@/hooks/useIm'
import type { ImIdentity } from '@/api/modules/im'

/**
 * IM 账号自助绑定。
 *
 * 流程:点「绑定」→ 服务端生成一次性码,只在这里显示一次 → 用户用**自己的** IM 账号
 * 私聊机器人发这串码 → 平台保证发送者是谁,服务端据此写绑定。这边轮询,绑上了自动更新。
 *
 * 渠道没启用时不给绑:机器人不在跑,码发出去没人收,表现就是「按了没反应」。
 *
 * 由调用方在打开时才挂载,上一次的码不会留到下一次。
 */
export default function ImBindModal({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation()
  const [pending, setPending] = useState<{
    channel: string; code: string; botName?: string; before?: string
  } | null>(null)
  const { data, isLoading, error, refetch } = useMyImIdentities(pending !== null)
  const issue = useIssueImCode()
  const unbind = useUnbindMyIm()

  // 轮询到这个渠道绑上了**一个新的账号**,就把码收起来 —— 那串码已经被用掉了。
  // 只看 bound 不够:重新绑定时旧绑定本来就在。用同一个平台账号重新绑定时 ID 不变,
  // 码会一直显示到弹窗关闭,可以接受。
  const pendingRow = pending && data?.find((r) => r.channel === pending.channel)
  const used = !!pendingRow?.bound && pendingRow.platformUserId !== pending?.before
  const shownCode = pending && !used ? pending : null

  function bind(row: ImIdentity) {
    issue.mutate(row.channel, {
      onSuccess: (r) => setPending({
        channel: row.channel, code: r.code, botName: r.botName || row.botName, before: row.platformUserId,
      }),
    })
  }

  function doUnbind(row: ImIdentity) {
    if (!confirmAction(t('imUnbindConfirm', { channel: row.label }))) return
    unbind.mutate(row.channel)
  }

  return (
    <Modal open title={<><MessagesSquare size={16} /> {t('imBindTitle')}</>} sub={t('imBindSub')} onClose={onClose}>
      {isLoading && <Loading />}
      {error && <ErrorState error={error} retry={() => refetch()} />}
      {data && data.length === 0 && <div className="im-empty">{t('imBindEmpty')}</div>}
      {data && data.length > 0 && (
        <div className="im-bind-list">
          {data.map((row) => (
            <div key={row.channel} className="im-bind-row">
              <span className="im-ch-name">{row.label}</span>
              <span className={clsx('um-tag', row.bound && 'on')}>
                {!row.enabled ? t('imChannelOff') : row.bound ? t('imBound') : t('imUnbound')}
              </span>
              {row.bound && <code className="im-pid">{row.platformUserId}</code>}
              <div className="im-bind-act">
                <Button disabled={!row.enabled || issue.isPending} onClick={() => bind(row)}
                        title={!row.enabled ? t('imChannelOffHint') : undefined}>
                  {row.bound ? t('imRebind') : t('imBind')}
                </Button>
                {row.bound && (
                  <Button variant="danger" disabled={unbind.isPending} onClick={() => doUnbind(row)}>
                    {t('imUnbind')}
                  </Button>
                )}
              </div>
            </div>
          ))}
        </div>
      )}
      {shownCode && (
        <div className="im-codebox">
          <div className="cell-strong">
            {t('imCodeTitle', {
              channel: data?.find((r) => r.channel === shownCode.channel)?.label ?? shownCode.channel,
              bot: shownCode.botName ? ` @${shownCode.botName}` : '',
            })}
          </div>
          {/* 码是数据,不进 i18n;等宽字体是为了让人能一位一位对着敲。 */}
          <code className="im-code">{shownCode.code}</code>
          <div className="cell-sub">{t('imCodeExpire')}</div>
        </div>
      )}
    </Modal>
  )
}
