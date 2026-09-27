import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { MessagesSquare, Unlink } from 'lucide-react'
import { Card, CardHead } from '@/components/common/Card'
import { Button } from '@/components/common/Button'
import { confirmAction } from '@/lib/confirm'
import { useBindUserIm, useUnbindUserIm, useUserImIdentities } from '@/hooks/useIm'
import type { UserView } from '@/types'

/**
 * 管理员代绑 / 解绑某人的 IM 账号。用于换号、离职回收、绑错 —— 与代绑 MFA 同一个
 * 信任假设:管理员声明「这个平台账号是这个人的」。两个动作都进审计链。
 */
export default function UserImPane({ user }: { user: UserView }) {
  const { t } = useTranslation()
  const { data } = useUserImIdentities(user.id)
  const bind = useBindUserIm()
  const unbind = useUnbindUserIm()
  const [draft, setDraft] = useState<Record<string, string>>({})

  if (!data || data.length === 0) return null
  return (
    <Card className="us-dcard">
      <CardHead title={t('usIm')} sub={t('usImSub')} />
      <div className="perm-sec us-cred">
        {data.map((row) => (
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
            <Button
              disabled={bind.isPending || !(draft[row.channel] ?? '').trim()}
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
                disabled={unbind.isPending}
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
