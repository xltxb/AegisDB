import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import clsx from 'clsx'
import {
  MailPlus, UserPlus, ShieldOff, ShieldCheck, Check, ChevronDown, Tag,
} from 'lucide-react'
import {
  useUsers, useRoles, useEnvTiers, useIsAdmin, useEffectiveMatrix,
  useSetUserRoles, useToggleUserStatus, useInviteUser, useCreateUser,
} from '@/hooks/usePermissions'
import { userTagsQueryOptions } from '@/api/modules/permissions'
import { CapabilityMatrix } from '@/components/permission/CapabilityMatrix'
import { Card, CardHead } from '@/components/common/Card'
import { Button } from '@/components/common/Button'
import { Modal } from '@/components/common/Modal'
import { Badge } from '@/components/common/Badge'
import { Loading, ErrorState, Empty } from '@/components/common/States'
import { confirmAction } from '@/lib/confirm'
import { initialsOf } from '@/lib/initials'
import { useUIStore } from '@/stores/ui'
import type { RoleBrief, UserView } from '@/types'

/** 主表列宽,照原型:用户 / 账号 / 角色 / 最近活跃 / 状态。 */
const COLS = '1.4fr 1.6fr 2.2fr 0.9fr 0.9fr'

export default function UsersPage() {
  const { t } = useTranslation()
  const isAdmin = useIsAdmin()
  const { data: users, isLoading, error, refetch } = useUsers()
  const { data: roles } = useRoles()
  const [invite, setInvite] = useState(false)
  const [create, setCreate] = useState(false)
  const [detail, setDetail] = useState<UserView | null>(null)

  const rows = users ?? []
  const noRole = rows.filter((u) => !u.roleIds?.length).length
  const disabled = rows.filter((u) => u.status === 'disabled').length

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>{t('usTitle')}</h1>
          <p>{t('usSub')}</p>
        </div>
        {isAdmin && (
          <div className="grow">
            <Button onClick={() => setInvite(true)}><MailPlus size={15} />{t('usInvite')}</Button>
            <Button variant="primary" onClick={() => setCreate(true)}>
              <UserPlus size={15} />{t('usCreate')}
            </Button>
          </div>
        )}
      </header>

      <div className="us-stats">
        <Stat label={t('usStatTotal')} value={rows.length} />
        {/* 「未分配角色」单独立一格:一个没有角色的账户能登录、但处处被拒,
            那是重新启用之后的合法中间态,而它看起来和"权限出问题了"一模一样。 */}
        <Stat label={t('usStatNoRole')} value={noRole} tone={noRole ? 'warning' : undefined} />
        <Stat label={t('usStatDisabled')} value={disabled} />
      </div>

      {isLoading && <Loading />}
      {error && <ErrorState error={error} retry={() => refetch()} />}

      {!isLoading && !error && (
        <div className="c-table us-table">
          <div className="c-thead" style={{ gridTemplateColumns: COLS }}>
            <div>{t('usColUser')}</div>
            <div>{t('usColAccount')}</div>
            <div>{t('usColRoles')}</div>
            <div>{t('usColActive')}</div>
            <div>{t('usColStatus')}</div>
          </div>
          {!rows.length && <Empty hint={t('usEmpty')} />}
          {rows.map((u) => (
            <UserRow
              key={u.id}
              user={u}
              roles={roles ?? []}
              isAdmin={isAdmin}
              onOpen={() => setDetail(u)}
            />
          ))}
        </div>
      )}

      <InviteModal open={invite} roles={roles ?? []} onClose={() => setInvite(false)} />
      <CreateModal open={create} roles={roles ?? []} onClose={() => setCreate(false)} />
      {detail && (
        <UserDetail
          user={rows.find((x) => x.id === detail.id) ?? detail}
          onClose={() => setDetail(null)}
        />
      )}
    </div>
  )
}

function Stat({ label, value, tone }: { label: string; value: number; tone?: 'warning' }) {
  return (
    <div className={clsx('us-stat', tone)}>
      <div className="us-statv">{value}</div>
      <div className="us-statl">{label}</div>
    </div>
  )
}

function UserRow({
  user, roles, isAdmin, onOpen,
}: {
  user: UserView
  roles: RoleBrief[]
  isAdmin: boolean
  onOpen: () => void
}) {
  const { t } = useTranslation()
  const [expand, setExpand] = useState(false)
  const noRole = !user.roleIds?.length

  return (
    <>
      <div
        className={clsx('c-trow clickable', user.status === 'disabled' && 'us-off')}
        style={{ gridTemplateColumns: COLS }}
        onClick={onOpen}
      >
        <div className="c-td us-cell">
          <span className="us-ava">{user.initials || initialsOf(user.name)}</span>
          <span>
            <span className="cell-strong">
              {user.name}
              {user.kind === 'service' && <span className="us-svc">{t('usService')}</span>}
            </span>
            <span className="cell-sub">{user.dept || '—'}</span>
          </span>
          {/* 没有角色的人在列表里必须一眼看得出来 —— 否则他会一直"登录正常、
              什么都干不了",而没人知道要去哪里改。 */}
          {noRole && <ShieldOff size={15} className="us-warnicon" aria-label={t('usNoRole')} />}
        </div>

        <div className="c-td mono">{user.email}</div>

        <div className="c-td us-rolecell">
          {user.roles?.length
            ? user.roles.map((r) => <span key={r} className="us-rolechip">{r}</span>)
            : <span className="us-norole">{t('usNoRole')}</span>}
          {isAdmin && (
            <button
              type="button"
              className={clsx('us-assign', expand && 'on')}
              title={t('usAssign')}
              onClick={(e) => { e.stopPropagation(); setExpand((v) => !v) }}
            >
              <ChevronDown size={13} />
            </button>
          )}
        </div>

        <div className="c-td mono">{user.lastActive || '—'}</div>

        <div className="c-td us-statuscell" onClick={(e) => e.stopPropagation()}>
          <span
            className={clsx('us-otp', user.mfaEnabled && 'on')}
            title={t(user.mfaEnabled ? 'usOtpOn' : 'usOtpOff')}
          >
            <ShieldCheck size={12} />OTP
          </span>
          <StatusToggle user={user} isAdmin={isAdmin} />
        </div>
      </div>

      {expand && (
        <div className="us-expand" onClick={(e) => e.stopPropagation()}>
          <RoleAssigner user={user} roles={roles} onDone={() => setExpand(false)} />
        </div>
      )}
    </>
  )
}

function StatusToggle({ user, isAdmin }: { user: UserView; isAdmin: boolean }) {
  const { t } = useTranslation()
  const toggle = useToggleUserStatus()
  const notify = useUIStore((s) => s.notify)
  const on = user.status !== 'disabled'

  function click() {
    if (!isAdmin || toggle.isPending) return
    /*
     * 停不了的先说原因,别让人白确认一次。
     *
     * canDisable / disableBlock 由服务端算(不能停自己、不能停最后一个管理员),
     * 前端照着显示而不是自己判一遍 —— 和执行按钮读 canExecute 是同一条规矩。
     */
    if (on && user.canDisable === false) {
      notify(user.disableBlock || t('usCannotDisable'), 'error')
      return
    }
    if (on && !confirmAction(t('usDisableConfirm', { name: user.name }))) return
    toggle.mutate({ id: user.id, status: on ? 'disabled' : 'active' })
  }

  return (
    <button type="button" className="us-stbtn" disabled={!isAdmin} onClick={click}>
      <Badge tone={on ? 'success' : 'neutral'}>{t(on ? 'usEnabled' : 'usDisabled')}</Badge>
    </button>
  )
}

/**
 * 行内角色分配器。
 *
 * 多选:一个人可以同时挂几个角色,权限取并集。存下去的第一个是主角色(侧栏显示的
 * 那个名字),所以顺序保留点击顺序,不排序。保存即同步角色成员 —— 角色页的成员墙
 * 和成员数读的是同一批数据,mutation 成功后一并失效。
 */
function RoleAssigner({ user, roles, onDone }: { user: UserView; roles: RoleBrief[]; onDone: () => void }) {
  const { t } = useTranslation()
  const save = useSetUserRoles()
  const [sel, setSel] = useState<number[]>(user.roleIds ?? [])

  const toggle = (id: number) =>
    setSel((p) => (p.includes(id) ? p.filter((x) => x !== id) : [...p, id]))

  return (
    <div className="us-assigner">
      <div className="us-chips">
        {roles.map((r) => (
          <button
            key={r.id}
            type="button"
            className={clsx('us-chip', sel.includes(r.id) && 'on')}
            onClick={() => toggle(r.id)}
          >
            {sel.includes(r.id) && <Check size={13} />}{r.name}
          </button>
        ))}
      </div>
      <div className="us-assignfoot">
        {/* 一个角色都不选等于把人变成"能登录但处处被拒",后端也会拒这次提交。 */}
        <span className="cell-sub">{sel.length ? t('usPrimaryHint') : t('usNeedOneRole')}</span>
        <Button variant="ghost" onClick={onDone}>{t('cancel')}</Button>
        <Button
          variant="primary"
          disabled={!sel.length || save.isPending}
          onClick={() => save.mutate({ id: user.id, roleIds: sel }, { onSuccess: onDone })}
        >
          {t('save')}
        </Button>
      </div>
    </div>
  )
}

function UserDetail({ user, onClose }: { user: UserView; onClose: () => void }) {
  const { t } = useTranslation()
  const { data: tiers } = useEnvTiers()
  const { data: tags, isError: tagsFailed } = useQuery(userTagsQueryOptions(user.id))
  const { matrix, isLoading } = useEffectiveMatrix(user.roleIds ?? [])

  const cols = (tiers ?? []).map((x) => ({ code: x.code, label: x.displayName || x.code.toUpperCase() }))

  return (
    <Modal open title={user.name} sub={user.email} width={760} onClose={onClose}>
      <div className="us-dmeta">
        <span><b>{t('usColRoles')}</b>{user.roles?.length ? user.roles.join(' · ') : t('usNoRole')}</span>
        <span><b>{t('usColActive')}</b>{user.lastActive || '—'}</span>
        <span><b>{t('usOtp')}</b>{t(user.mfaEnabled ? 'usOtpOn' : 'usOtpOff')}</span>
      </div>

      <div className="us-dtags">
        <Tag size={13} />
        {/* 「没单独授权」和「这一下没读到」必须分开说 —— 两者都是一片空白,
            但前者是正常状态,后者是这一格现在什么都不该断言。 */}
        {tagsFailed
          ? <span className="cell-sub">{t('usTagsFailed')}</span>
          : tags?.length
            ? tags.map((g) => <span key={g} className="us-rolechip">{g}</span>)
            : <span className="cell-sub">{t('usTagsFromRole')}</span>}
      </div>

      <Card className="us-dcard">
        <CardHead title={t('usEffective')} sub={t('usEffectiveSub')} />
        <div className="perm-sec">
          {!user.roleIds?.length ? (
            <div className="notice warn"><ShieldOff size={15} />{t('usNoRoleHint')}</div>
          ) : isLoading ? (
            <Loading />
          ) : !cols.length ? (
            <Empty hint={t('pmNoTier')} />
          ) : (
            // 原型的 1.7fr + 5 列 1fr —— 后面那 5 是出厂分层的个数,不是写死的列数。
            <CapabilityMatrix matrix={matrix} tiers={cols} headWidth="1.7fr" colWidth="1fr" />
          )}
        </div>
      </Card>
    </Modal>
  )
}

function InviteModal({ open, roles, onClose }: { open: boolean; roles: RoleBrief[]; onClose: () => void }) {
  const { t } = useTranslation()
  const invite = useInviteUser()
  const [email, setEmail] = useState('')
  const [roleId, setRoleId] = useState(0)
  const pick = roleId || roles.find((r) => r.code === 'ro')?.id || roles[0]?.id || 0

  return (
    <Modal
      open={open}
      title={t('usInvite')}
      sub={t('usInviteSub')}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>{t('cancel')}</Button>
          <Button
            variant="primary"
            disabled={!email.trim() || !pick || invite.isPending}
            onClick={() => invite.mutate(
              { email: email.trim(), roleId: pick },
              { onSuccess: () => { setEmail(''); onClose() } },
            )}
          >
            {t('usSendInvite')}
          </Button>
        </>
      }
    >
      <div className="fld">
        <label>{t('usEmail')}</label>
        <input type="email" value={email} placeholder="name@vela.io" onChange={(e) => setEmail(e.target.value)} />
      </div>
      <div className="fld">
        <label>{t('usInitialRole')}</label>
        <select value={pick} onChange={(e) => setRoleId(Number(e.target.value))}>
          {roles.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}
        </select>
      </div>
    </Modal>
  )
}

function CreateModal({ open, roles, onClose }: { open: boolean; roles: RoleBrief[]; onClose: () => void }) {
  const { t } = useTranslation()
  const create = useCreateUser()
  const [f, setF] = useState({ email: '', name: '', password: '', roleIds: [] as number[] })
  const set = (patch: Partial<typeof f>) => setF((p) => ({ ...p, ...patch }))
  const toggle = (id: number) =>
    set({ roleIds: f.roleIds.includes(id) ? f.roleIds.filter((x) => x !== id) : [...f.roleIds, id] })
  // 8 位是服务端的下限,这里先挡一道,免得人填完整张表才被退回来。
  const okToSend = !!f.email.trim() && f.password.length >= 8 && f.roleIds.length > 0

  return (
    <Modal
      open={open}
      title={t('usCreate')}
      sub={t('usCreateSub')}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>{t('cancel')}</Button>
          <Button
            variant="primary"
            disabled={!okToSend || create.isPending}
            onClick={() => create.mutate(
              { email: f.email.trim(), name: f.name.trim(), password: f.password, roleIds: f.roleIds },
              { onSuccess: () => { setF({ email: '', name: '', password: '', roleIds: [] }); onClose() } },
            )}
          >
            {t('usCreateSubmit')}
          </Button>
        </>
      }
    >
      <div className="fld-2">
        <div className="fld">
          <label>{t('usEmail')}</label>
          <input type="email" value={f.email} placeholder="name@vela.io" onChange={(e) => set({ email: e.target.value })} />
        </div>
        <div className="fld">
          <label>{t('usName')}</label>
          <input value={f.name} onChange={(e) => set({ name: e.target.value })} />
        </div>
      </div>
      <div className="fld">
        <label>{t('usInitialPw')}</label>
        <input type="password" value={f.password} placeholder={t('usPwHint')} onChange={(e) => set({ password: e.target.value })} />
      </div>
      <div className="fld">
        <label>{t('usColRoles')}</label>
        <div className="us-chips">
          {roles.map((r) => (
            <button
              key={r.id}
              type="button"
              className={clsx('us-chip', f.roleIds.includes(r.id) && 'on')}
              onClick={() => toggle(r.id)}
            >
              {f.roleIds.includes(r.id) && <Check size={13} />}{r.name}
            </button>
          ))}
        </div>
      </div>
      <div className="notice">{t('usCreateNote')}</div>
    </Modal>
  )
}
