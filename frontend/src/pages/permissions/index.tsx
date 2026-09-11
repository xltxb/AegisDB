import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import clsx from 'clsx'
import {
  Crown, Shield, UserCog, Code, Eye, Search, X, Plus, UserPlus, Check,
  SquareTerminal, ClipboardCheck, Database, ShieldAlert, Layers, UsersRound,
  ScrollText, Settings, Rocket, Clock, Tags, Info, type LucideIcon,
} from 'lucide-react'
import {
  useRoles, useRole, useUsers, useEnvTiers, useIsAdmin,
  useSetRoleCapabilities, useSetRoleMenus, useUpdateRole,
  useAddRoleMember, useRemoveRoleMember, useSetRoleTags, useAllTags,
} from '@/hooks/usePermissions'
import { TagEditModal } from '@/components/modals/TagEditModal'
import { CapabilityMatrix } from '@/components/permission/CapabilityMatrix'
import { Card, CardHead } from '@/components/common/Card'
import { Button } from '@/components/common/Button'
import { Modal } from '@/components/common/Modal'
import { Switch } from '@/components/common/Switch'
import { Badge } from '@/components/common/Badge'
import { Loading, ErrorState, Empty } from '@/components/common/States'
import { confirmAction } from '@/lib/confirm'
import { initialsOf } from '@/lib/initials'
import type { RoleDetailFull } from '@/api/modules/permissions'
import type { RoleBrief, UserView } from '@/types'

const ROLE_ICON: Record<string, LucideIcon> = {
  crown: Crown, shield: Shield, 'user-cog': UserCog, code: Code, eye: Eye,
}

/**
 * 十个菜单键,与后端 `menu(...)` 中间件认的那些一一对应。
 *
 * 少一个键不是"界面上少一格开关",是**这一项再也开不出来**:角色菜单是整表覆盖
 * (PUT /roles/:id/menus),这里没列到的键在每次保存时都会从提交的对象里消失。
 */
const MENUS: { key: string; Icon: LucideIcon; label: string }[] = [
  { key: 'terminal', Icon: SquareTerminal, label: 'm_terminal' },
  { key: 'approve', Icon: ClipboardCheck, label: 'm_approve' },
  { key: 'db', Icon: Database, label: 'm_db' },
  { key: 'rules', Icon: ShieldAlert, label: 'm_rules' },
  { key: 'envtier', Icon: Layers, label: 'm_envtier' },
  { key: 'perms', Icon: UsersRound, label: 'm_perms' },
  { key: 'audit', Icon: ScrollText, label: 'm_audit' },
  { key: 'settings', Icon: Settings, label: 'm_settings' },
  { key: 'pipeline', Icon: Rocket, label: 'm_pipeline' },
  { key: 'execwindow', Icon: Clock, label: 'm_execwindow' },
]

/**
 * 内置角色的 code,抄自 bootstrap/seed.go 种下的那五个。
 *
 * 服务端没有 builtin 标志位,所以这是一份**复制过来的事实**:种子改了这里要跟着改。
 * 写错的代价只是少一个灰色小标,不影响任何判定 —— 它只用来打标,绝不参与推断角色
 * 有什么权限(那正是 issue #46 栽的地方)。
 */
const BUILTIN_ROLES = new Set(['admin', 'owner', 'l2', 'ro', 'audit'])

export default function PermissionsPage() {
  const { t } = useTranslation()
  const isAdmin = useIsAdmin()
  const { data: roles, isLoading, error, refetch } = useRoles()
  const { data: tiers } = useEnvTiers()
  const [picked, setPicked] = useState(0)
  const [q, setQ] = useState('')

  // 没选过就跟着列表第一个走 —— 右侧从一开始就有东西,而不是一块空白等人点。
  const roleId = picked || roles?.[0]?.id || 0
  const { data: detail } = useRole(roleId)

  const kw = q.trim().toLowerCase()
  // 角色是一次性全量拉下来的(/roles 不分页),所以在客户端筛是对的。
  const shown = (roles ?? []).filter(
    (r) => !kw || r.name.toLowerCase().includes(kw) || r.code.toLowerCase().includes(kw),
  )

  return (
    <div className="page perm-page">
      <header className="page-head">
        <div>
          <h1>{t('pmTitle')}</h1>
          <p>{t('pmSub')}</p>
        </div>
        {!isAdmin && <div className="grow"><Badge>{t('pmReadOnly')}</Badge></div>}
      </header>

      {isLoading && <Loading />}
      {error && <ErrorState error={error} retry={() => refetch()} />}

      {!isLoading && !error && (
        <div className="perm-grid">
          <aside className="perm-roles">
            <div className="perm-search">
              <Search size={14} />
              <input
                value={q}
                spellCheck={false}
                placeholder={t('pmRoleSearchPh')}
                onChange={(e) => setQ(e.target.value)}
              />
              {q && (
                <button className="perm-sclear" title={t('cancel')} onClick={() => setQ('')}>
                  <X size={13} />
                </button>
              )}
            </div>
            <div className="perm-rlist">
              {shown.map((r) => <RoleCard key={r.id} role={r} on={r.id === roleId} onPick={() => setPicked(r.id)} />)}
              {!shown.length && <Empty hint={t('pmNoRole')} />}
            </div>
            {/* 这一句是补的,不是装饰。
                角色只能改、不能新增:后端根本没有 `POST /roles`,这五个是 seed.go
                种下去的。Vue 版在这里摆着一个写「新建角色」的按钮,点下去其实打开的
                是**当前角色**的编辑表单 —— 比没有按钮更坏。这里两件事一起做:不放那
                个按钮,并且把"为什么找不到新建入口"直说,免得有人在页面上找半天,再
                去翻接口文档,最后才明白这不是他没找到。 */}
            <div className="perm-rnote">
              <Info size={13} />
              <span>{t('pmRolesFixed')}</span>
            </div>
          </aside>

          <div className="perm-detail">
            {!detail && <Empty hint={t('pmPickRole')} />}
            {detail && <RolePanes detail={detail} isAdmin={isAdmin} tiers={tiers ?? []} />}
          </div>
        </div>
      )}
    </div>
  )
}

function RoleCard({ role, on, onPick }: { role: RoleBrief; on: boolean; onPick: () => void }) {
  const { t } = useTranslation()
  const Icon = ROLE_ICON[role.icon] ?? Shield
  return (
    <button type="button" className={clsx('perm-rcard', on && 'on')} onClick={onPick}>
      <span className="perm-rname">
        <Icon size={15} />
        <span className="perm-rn">{role.name}</span>
        {BUILTIN_ROLES.has(role.code) && <span className="perm-builtin">{t('pmBuiltin')}</span>}
      </span>
      <span className="perm-rlayer">{role.layer} · {t('pmMembersN', { n: role.count })}</span>
    </button>
  )
}

function RolePanes({
  detail, isAdmin, tiers,
}: {
  detail: RoleDetailFull
  isAdmin: boolean
  tiers: { code: string; displayName: string }[]
}) {
  const { t } = useTranslation()
  const caps = useSetRoleCapabilities()
  const menus = useSetRoleMenus()
  const removeMember = useRemoveRoleMember()
  const setTags = useSetRoleTags()
  const { data: allTags } = useAllTags()
  const [editing, setEditing] = useState(false)
  const [picking, setPicking] = useState(false)
  const [tagging, setTagging] = useState(false)

  // 列头用分层自己的 displayName:它是服务端的行,管理员改了名这里就跟着改,
  // 不需要前端再维护一份 code→名字的对照。
  const cols = tiers.map((x) => ({ code: x.code, label: x.displayName || x.code.toUpperCase() }))

  return (
    <>
      <Card>
        <CardHead
          title={detail.name}
          sub={`${detail.layer} · ${t('pmMembersN', { n: detail.members.length })}`}
          actions={isAdmin && <Button onClick={() => setEditing(true)}>{t('pmEditRole')}</Button>}
        />
        <div className="perm-sec">
          <div className="perm-sechead">
            <span className="perm-eyebrow">{t('pmMatrixSection')}</span>
            <span className="perm-secsub">{t('pmMatrixHint')}</span>
          </div>
          {!cols.length ? (
            <Empty hint={t('pmNoTier')} />
          ) : (
            <CapabilityMatrix
              matrix={detail.matrix}
              tiers={cols}
              onCycle={isAdmin
                ? (cap, tier, next) => caps.mutate({
                    id: detail.id,
                    // 整表提交(PUT /capabilities 是覆盖),所以先把改动并进当前矩阵。
                    matrix: {
                      ...detail.matrix,
                      [cap]: { ...(detail.matrix?.[cap] ?? {}), [tier]: next },
                    },
                  })
                : undefined}
            />
          )}
        </div>
      </Card>

      <Card>
        <CardHead title={t('pmMenuSection')} sub={t('pmMenuSub')} />
        <div className="perm-sec">
          <div className="perm-menugrid">
            {MENUS.map(({ key, Icon, label }) => (
              <label key={key} className={clsx('perm-menucard', detail.menus?.[key] && 'on')}>
                <Icon size={16} />
                <span className="perm-ml">{t(label)}</span>
                <Switch
                  checked={!!detail.menus?.[key]}
                  disabled={!isAdmin}
                  onChange={(v) => menus.mutate({
                    id: detail.id,
                    menus: { ...(detail.menus ?? {}), [key]: v },
                  })}
                />
              </label>
            ))}
          </div>
        </div>
      </Card>

      <Card>
        <CardHead
          title={t('pmScopeSection')}
          sub={t('pmScopeSub')}
          actions={isAdmin && (
            <Button onClick={() => setTagging(true)}><Tags size={14} />{t('pmScopeEdit')}</Button>
          )}
        />
        <div className="perm-sec">
          <div className="tagpick-chips">
            {detail.tags?.length
              ? detail.tags.map((g) => <span key={g} className="tagpick-chip ro">{g}</span>)
              : (
                // 空标签**不是**"还没配":它的含义是这个角色不受标签限制,能力矩阵
                // 说什么就是什么。写成一句话,而不是留一片空白让人自己猜是哪种。
                <Badge tone="warning">{t('pmScopeAll')}</Badge>
              )}
          </div>
        </div>
      </Card>

      <Card>
        <CardHead
          title={t('pmMembersSection')}
          sub={t('pmMembersN', { n: detail.members.length })}
        />
        <div className="perm-sec">
          <div className="perm-pills">
            {detail.members.map((m) => (
              <span key={m.id} className="perm-pill">
                <span className="perm-ava">{m.initials || initialsOf(m.name)}</span>
                <span className="perm-pn">{m.name}</span>
                {isAdmin && (
                  <button
                    className="perm-px"
                    title={t('pmRemoveMember')}
                    disabled={removeMember.isPending}
                    onClick={() => {
                      if (!confirmAction(t('pmRemoveMemberConfirm', { role: detail.name, name: m.name }))) return
                      removeMember.mutate({ id: detail.id, userId: m.id })
                    }}
                  >
                    <X size={12} />
                  </button>
                )}
              </span>
            ))}
            {isAdmin && (
              <button type="button" className="perm-pill add" onClick={() => setPicking(true)}>
                <UserPlus size={13} />{t('pmAddMember')}
              </button>
            )}
            {!detail.members.length && !isAdmin && <Empty hint={t('pmNoMember')} />}
          </div>
        </div>
      </Card>

      {/* key 绑在「角色 id + 开没开」上:换角色或重开表单都重挂一次,草稿不串台。 */}
      <RoleForm key={`${detail.id}-${editing}`} open={editing} detail={detail} onClose={() => setEditing(false)} />
      <MemberPicker open={picking} detail={detail} onClose={() => setPicking(false)} />
      {/* 同样按「角色 id + 开没开」重挂:换角色不该把上一份草稿带过来。 */}
      <TagEditModal
        key={`tags-${detail.id}-${tagging}`}
        open={tagging}
        title={t('pmScopeTitle', { role: detail.name })}
        sub={t('pmScopeSub')}
        tags={detail.tags ?? []}
        suggestions={allTags ?? []}
        busy={setTags.isPending}
        onClose={() => setTagging(false)}
        onSave={(tags) => setTags.mutate({ id: detail.id, tags }, { onSuccess: () => setTagging(false) })}
      />
    </>
  )
}

interface RoleFormState {
  name: string
  defaultConnRole: string
  description: string
  /** undefined = 接口没告诉我们真值。见 RoleDetailFull 与下面的 save()。 */
  canApprove: boolean | undefined
}

/**
 * 角色表单 —— issue #46 的修复处。
 *
 * 旧版打开表单时这么填:
 *
 *   canApprove = code === 'admin' || code === 'owner'
 *   defaultConnRole = code === 'ro' ? 'readonly' : 'dba_l2'
 *
 * 两个字段都不是读来的,是**按 code 猜的**,而保存时又原样 PATCH 回去。于是给
 * 「DBA 负责人」改一个名字,顺手把某个被单独授过审批权的角色的审批权关掉 ——
 * 界面上没有任何地方提过这件事。
 *
 * 这里改成两条:值只从 detail 读(读不到就是 `undefined`,不猜);保存**只发改过
 * 的字段**。后端 UpdateRole 对空串与 nil 的 canApprove 一律跳过,所以没碰过的字段
 * 不会被这次保存动到。等服务端把这三个字段加进 RoleDetailResp,这一页不用改一行
 * 代码就会自动显示真值。
 */
function RoleForm({ open, detail, onClose }: { open: boolean; detail: RoleDetailFull; onClose: () => void }) {
  const { t } = useTranslation()
  const update = useUpdateRole()
  const base: RoleFormState = {
    name: detail.name,
    defaultConnRole: detail.defaultConnRole ?? '',
    description: detail.description ?? '',
    canApprove: detail.canApprove,
  }
  const [f, setF] = useState<RoleFormState>(base)
  const set = (patch: Partial<RoleFormState>) => setF((p) => ({ ...p, ...patch }))

  function save() {
    const body: { name?: string; description?: string; defaultConnRole?: string; canApprove?: boolean } = {}
    if (f.name.trim() && f.name.trim() !== base.name) body.name = f.name.trim()
    if (f.defaultConnRole.trim() !== base.defaultConnRole) body.defaultConnRole = f.defaultConnRole.trim()
    if (f.description.trim() !== base.description) body.description = f.description.trim()
    // 只在**人真的拨过**这个开关时才发。base 是 undefined 时,任何一次拨动都算改动。
    if (f.canApprove !== undefined && f.canApprove !== base.canApprove) body.canApprove = f.canApprove
    // 一个字段都没改就别发这一枪 —— 空 PATCH 同样会进审计链。
    if (!Object.keys(body).length) { onClose(); return }
    update.mutate({ id: detail.id, body }, { onSuccess: onClose })
  }

  return (
    <Modal
      open={open}
      title={t('pmEditRole')}
      sub={t('pmEditRoleSub')}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>{t('cancel')}</Button>
          <Button variant="primary" disabled={update.isPending} onClick={save}>{t('save')}</Button>
        </>
      }
    >
      <div className="fld-2">
        <div className="fld">
          <label>{t('pmRoleName')}</label>
          <input value={f.name} onChange={(e) => set({ name: e.target.value })} />
        </div>
        <div className="fld">
          <label>{t('pmDefConnRole')}</label>
          <input
            value={f.defaultConnRole}
            placeholder={t('pmUnknownValue')}
            onChange={(e) => set({ defaultConnRole: e.target.value })}
          />
        </div>
      </div>
      <div className="fld">
        <label>{t('pmRoleDesc')}</label>
        <textarea
          value={f.description}
          placeholder={t('pmUnknownValue')}
          onChange={(e) => set({ description: e.target.value })}
        />
      </div>
      <div className="perm-toggle">
        <div>
          <div className="perm-tt">{t('pmCanApprove')}</div>
          <div className="perm-td">{t('pmCanApproveSub')}</div>
        </div>
        <Switch checked={f.canApprove ?? false} onChange={(v) => set({ canApprove: v })} />
      </div>
      {/* 接口读不回真值时把这件事说出来,而不是让那个默认关着的开关冒充当前状态。 */}
      {f.canApprove === undefined && <div className="notice warn">{t('pmApproveUnknown')}</div>}
    </Modal>
  )
}

function MemberPicker({ open, detail, onClose }: { open: boolean; detail: RoleDetailFull; onClose: () => void }) {
  const { t } = useTranslation()
  const { data: users } = useUsers()
  const add = useAddRoleMember()
  const [q, setQ] = useState('')

  const kw = q.trim().toLowerCase()
  const joined = new Set(detail.memberIds ?? [])
  const list = (users ?? []).filter(
    (u: UserView) => !kw
      || u.name.toLowerCase().includes(kw)
      || u.email.toLowerCase().includes(kw)
      || (u.dept || '').toLowerCase().includes(kw),
  )

  return (
    <Modal
      open={open}
      title={t('pmAddMemberTo', { role: detail.name })}
      sub={t('pmAddMemberSub')}
      onClose={onClose}
      footer={<Button variant="primary" onClick={onClose}>{t('done')}</Button>}
    >
      <div className="perm-search inmodal">
        <Search size={14} />
        <input value={q} placeholder={t('pmMemberSearchPh')} onChange={(e) => setQ(e.target.value)} />
      </div>
      <div className="perm-picklist">
        {list.map((u) => (
          <button
            key={u.id}
            type="button"
            className="perm-pickrow"
            disabled={joined.has(u.id) || add.isPending}
            onClick={() => add.mutate({ id: detail.id, userId: u.id })}
          >
            <span className="perm-ava">{u.initials || initialsOf(u.name)}</span>
            <span className="perm-pickname">
              <span className="cell-strong">{u.name}</span>
              <span className="cell-sub">{u.email}</span>
            </span>
            {joined.has(u.id)
              ? <span className="perm-joined"><Check size={13} />{t('pmJoined')}</span>
              : <span className="perm-addmark"><Plus size={13} />{t('pmAdd')}</span>}
          </button>
        ))}
        {!list.length && <Empty hint={t('pmNoUserMatch')} />}
      </div>
    </Modal>
  )
}
