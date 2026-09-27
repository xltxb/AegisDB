import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { MessagesSquare, Unlink } from 'lucide-react'
import { Card, CardHead } from '@/components/common/Card'
import { Button } from '@/components/common/Button'
import { ErrorState } from '@/components/common/States'
import { confirmAction } from '@/lib/confirm'
import { useBindUserIm, useUnbindUserIm, useUserImIdentities } from '@/hooks/useIm'
import type { UserView } from '@/types'

/**
 * 管理员代绑 / 解绑某人的 IM 账号。用于换号、离职回收、绑错 —— 与代绑 MFA 同一个
 * 信任假设:管理员声明「这个平台账号是这个人的」。两个动作都进审计链。
 *
 * 「没有交互渠道」和「这一下没读到」必须分开说:前者是正常状态(返回 null,卡片不出现),
 * 后者是这一格现在什么都不该断言 —— 管理员看到的一片空白和"没渠道"长得一样,
 * 但只有前者该被当作事实,所以读失败时改为把卡片显出来、露出错误与重试。
 */
export default function UserImPane({ user }: { user: UserView }) {
  const { t } = useTranslation()
  const { data, isLoading, error, refetch } = useUserImIdentities(user.id)
  const bind = useBindUserIm()
  const unbind = useUnbindUserIm()
  const [draft, setDraft] = useState<Record<string, string>>({})

  if (isLoading) return null
  if (!error && (!data || data.length === 0)) return null
  return (
    <Card className="us-dcard">
      <CardHead title={t('usIm')} sub={t('usImSub')} />
      <div className="perm-sec us-cred">
        {error && <ErrorState error={error} retry={() => refetch()} />}
        {!error && data!.map((row) => (
          <div key={row.channel} className="us-credrow">
            <div className="us-credl"><MessagesSquare size={14} />{row.label}</div>
            <span className="cell-sub">
              {row.bound ? `${row.platformUserId} · ${t(row.boundBy === 'admin' ? 'imBoundAdmin' : 'imBoundSelf')}` : t('imUnbound')}
            </span>
            <input
              value={draft[row.channel] ?? ''}
              placeholder={t('usImPh')}
              onChange={(e) => setDraft((d) => ({ ...d, [row.channel]: e.target.value }))}
            />
            {/* bind/unbind 是每个面板共一份 mutation,不是每行一份 —— 光看 isPending
                会把正在等结果的那一行之外的所有行也锁住。用 variables.channel 认行。 */}
            <Button
              disabled={(bind.isPending && bind.variables?.channel === row.channel) || !(draft[row.channel] ?? '').trim()}
              onClick={() => bind.mutate(
                { id: user.id, channel: row.channel, platformUserId: (draft[row.channel] ?? '').trim() },
                { onSuccess: () => setDraft((d) => ({ ...d, [row.channel]: '' })) },
              )}
            >
              {t('usImBind')}
            </Button>
            {row.bound && (
              <Button
                variant="danger"
                disabled={unbind.isPending && unbind.variables?.channel === row.channel}
                onClick={() => {
                  if (confirmAction(t('usImUnbindConfirm', { name: user.name, channel: row.label }))) {
                    unbind.mutate({ id: user.id, channel: row.channel })
                  }
                }}
              >
                <Unlink size={14} />{t('imUnbind')}
              </Button>
            )}
          </div>
        ))}
      </div>
    </Card>
  )
}
