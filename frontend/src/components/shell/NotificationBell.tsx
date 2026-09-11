import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import clsx from 'clsx'
import { Bell, CircleCheck, CircleX, Clock } from 'lucide-react'
import { meQueryOptions } from '@/api/modules/auth'
import {
  toneOfNotification, useMarkNotificationsRead, useNotifications, useNotificationToasts,
} from '@/hooks/useNotifications'

const ICON_OF = { ok: CircleCheck, bad: CircleX, warn: Clock }

/**
 * 通知铃铛。
 *
 * 两件事分开:**弹窗**告诉人"刚刚发生了什么"(见 useNotificationToasts),**面板**
 * 是回头翻的收件箱。红点只是个提示,不替代前者 —— 它要人先看见、再点开。
 */
export default function NotificationBell() {
  const { t } = useTranslation()
  const nav = useNavigate()
  const { data: me } = useQuery(meQueryOptions())
  const { data } = useNotifications()
  const markRead = useMarkNotificationsRead()
  const [open, setOpen] = useState(false)

  useNotificationToasts(data?.items, me?.id)

  const items = data?.items ?? []
  const unread = data?.unread ?? 0

  function toggle() {
    const next = !open
    setOpen(next)
    // 打开即全部标已读 —— 面板一屏就把当前这些都摆出来了,再让人逐条点一次
    // "已读"只是把同一个动作做两遍。
    if (next && unread > 0) markRead.mutate([])
  }

  return (
    <div className="notif">
      <button
        className={clsx('iconbtn bell', open && 'on')}
        title={t('notifTitle')}
        onClick={toggle}
      >
        <Bell size={17} />
        {/* 99+ 封顶:三位数以上的红点只会撑破图标,而且"多少条"在这里不重要。 */}
        {unread > 0 && <span className="notif-badge">{unread > 99 ? '99+' : unread}</span>}
      </button>

      {open && (
        <>
          <div className="notif-backdrop" onClick={() => setOpen(false)} />
          <div className="notif-panel">
            <div className="notif-head">
              <span>{t('notifTitle')}</span>
              {/* 通知里最常见的一类就是审批结果,给它一个直达入口。 */}
              {me?.menus?.approve && (
                <button
                  className="notif-link"
                  onClick={() => { setOpen(false); nav('/approvals') }}
                >
                  {t('notifViewAppr')}
                </button>
              )}
            </div>
            <div className="notif-list">
              {!items.length && <div className="notif-empty">{t('notifEmpty')}</div>}
              {items.map((n) => {
                const tone = toneOfNotification(n.type)
                const Icon = ICON_OF[tone]
                return (
                  <div key={n.id} className={clsx('notif-item', !n.read && 'unread')}>
                    <Icon size={15} className={`notif-ic t-${tone}`} />
                    <div className="notif-body">
                      <div className="notif-t">
                        {n.title}
                        {n.refNo && <span className="notif-ref">{n.refNo}</span>}
                      </div>
                      {n.body && <div className="notif-d">{n.body}</div>}
                    </div>
                  </div>
                )
              })}
            </div>
          </div>
        </>
      )}
    </div>
  )
}
