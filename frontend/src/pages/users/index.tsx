import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import clsx from 'clsx'
import QRCode from 'qrcode'
import {
  MailPlus, UserPlus, ShieldOff, ShieldCheck, Check, ChevronDown, Tag,
  KeyRound, Smartphone, Unlink,
} from 'lucide-react'
import {
  useUsers, useRoles, useEnvTiers, useIsAdmin, useEffectiveMatrix,
  useSetUserRoles, useToggleUserStatus, useInviteUser, useCreateUser,
  useUserTags, useSetUserTags, useAllTags,
  useSetUserPassword, useBindUserMfa, useResetUserMfa,
} from '@/hooks/usePermissions'
import { TagEditModal } from '@/components/modals/TagEditModal'
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
  const isAdmin = useIsAdmin()
  const { data: tiers } = useEnvTiers()
  const { data: tags, isError: tagsFailed } = useUserTags(user.id)
  const { data: allTags } = useAllTags()
  const setTags = useSetUserTags()
  const { matrix, isLoading } = useEffectiveMatrix(user.roleIds ?? [])
  const [tagging, setTagging] = useState(false)

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
        {/* 读失败时不给编辑入口:那时手上这份「当前值」是假的,保存等于拿一份空草稿
            去覆盖人家真正的授权。 */}
        {isAdmin && !tagsFailed && (
          <button type="button" className="us-tagedit" onClick={() => setTagging(true)}>
            {t('usTagsEdit')}
          </button>
        )}
      </div>

      {isAdmin && <CredentialPane user={user} />}

      <TagEditModal
        key={`utags-${user.id}-${tagging}`}
        open={tagging}
        title={t('usTagsTitle')}
        sub={user.email}
        tags={tags ?? []}
        suggestions={allTags ?? []}
        busy={setTags.isPending}
        onClose={() => setTagging(false)}
        onSave={(next) => setTags.mutate({ id: user.id, tags: next }, { onSuccess: () => setTagging(false) })}
      />

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

/**
 * 管理员代为处置一个账户的凭据:重置口令、代绑 OTP、解绑 OTP。
 *
 * 三件事放在同一小节里,是因为它们有同一个性质:**做的人不是被做的人**。所以这里
 * 一条也不走"改完弹个已保存"的路子 ——
 *
 *  - 结果就地写在按钮旁边(`us-cmsg`),而不是一闪而过的 toast:管理员多半是在
 *    电话/工位旁边替人操作,需要一句能停在屏幕上、可以念给对方听的话。
 *  - 代绑会**当场把旧的验证器作废**(服务端生成新密钥并直接置为已启用),解绑会
 *    让那个人下次登录不再需要动态码 —— 两件都要先二次确认。
 *  - 新密钥只在那一次响应里出现,离开这个弹窗就再也拿不到。所以二维码画出来就留在
 *    那儿,旁边附明文密钥给手输的人,而不是弹一下就收。
 *
 * 重置口令没有二次确认:它要先键入一串新口令(且 ≥8 位),这个动作本身已经足够
 * 说明来意了,再拦一道只是多一次点击。
 */
function CredentialPane({ user }: { user: UserView }) {
  const { t } = useTranslation()
  const setPw = useSetUserPassword()
  const bind = useBindUserMfa()
  const reset = useResetUserMfa()

  const [pw, setPw2] = useState('')
  const [pwMsg, setPwMsg] = useState('')
  const [otp, setOtp] = useState<{ secret: string; otpauthUri: string } | null>(null)
  const [qr, setQr] = useState('')
  const [otpMsg, setOtpMsg] = useState('')

  function savePw() {
    // 长度后端也判(≥8)。前端先判一次是为了省一次白跑的往返,不是为了代替它。
    if (pw.length < 8) { setPwMsg(t('usPwTooShort')); return }
    setPw.mutate({ id: user.id, password: pw }, {
      onSuccess: () => { setPwMsg(t('usPwSaved')); setPw2('') },
      onError: () => setPwMsg(t('usPwFailed')),
    })
  }

  async function bindOtp() {
    // 已经绑过的时候,这一下是**换掉**他现在用的那个 —— 旧验证器立刻失效。
    if (user.mfaEnabled && !confirmAction(t('usOtpRebindConfirm', { name: user.name }))) return
    try {
      const r = await bind.mutateAsync(user.id)
      setOtp(r)
      setOtpMsg('')
      // 二维码只是把同一个 otpauth URI 画出来,画不出来不影响绑定本身 ——
      // 下面那行明文密钥仍然可用,所以这里只是少一张图,不是一次失败。
      setQr(await QRCode.toDataURL(r.otpauthUri, { margin: 1, width: 160 }).catch(() => ''))
    } catch {
      setOtpMsg(t('usOtpFailed'))
    }
  }

  function resetOtp() {
    if (!confirmAction(t('usOtpResetConfirm', { name: user.name }))) return
    reset.mutate(user.id, {
      onSuccess: () => { setOtp(null); setQr(''); setOtpMsg(t('usOtpUnbound')) },
      onError: () => setOtpMsg(t('usOtpFailed')),
    })
  }

  return (
    <Card className="us-dcard">
      <CardHead title={t('usCred')} sub={t('usCredSub')} />
      <div className="perm-sec us-cred">
        <div className="us-credrow">
          <div className="us-credl"><KeyRound size={14} />{t('usPwReset')}</div>
          <input
            type="password"
            value={pw}
            autoComplete="new-password"
            placeholder={t('usPwPh')}
            onChange={(e) => { setPw2(e.target.value); setPwMsg('') }}
          />
          <Button disabled={setPw.isPending || !pw} onClick={savePw}>{t('usPwSubmit')}</Button>
          {pwMsg && <span className="us-cmsg">{pwMsg}</span>}
        </div>

        <div className="us-credrow">
          <div className="us-credl"><Smartphone size={14} />{t('usOtp')}</div>
          <span className="cell-sub">{t(user.mfaEnabled ? 'usOtpOn' : 'usOtpOff')}</span>
          <Button disabled={bind.isPending} onClick={bindOtp}>
            {t(user.mfaEnabled ? 'usOtpRebind' : 'usOtpBind')}
          </Button>
          {user.mfaEnabled && (
            <Button variant="danger" disabled={reset.isPending} onClick={resetOtp}>
              <Unlink size={14} />{t('usOtpUnbind')}
            </Button>
          )}
          {otpMsg && <span className="us-cmsg">{otpMsg}</span>}
        </div>

        {otp && (
          <div className="us-otpbox">
            {qr && <img className="us-otpqr" src={qr} alt="OTP QR" />}
            <div className="us-otptext">
              <div className="cell-strong">{t('usOtpScan')}</div>
              {/* 密钥是数据,不进 i18n;等宽字体是为了让人能一位一位念出来。 */}
              <code className="us-otpsecret">{otp.secret}</code>
              <div className="cell-sub">{t('usOtpOnce')}</div>
            </div>
          </div>
        )}
      </div>
    </Card>
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
