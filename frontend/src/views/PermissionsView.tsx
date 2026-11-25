import { useState } from 'react'
import clsx from 'clsx'
import {
  Crown, Shield, UserCog, Code, Eye, SquareTerminal, ClipboardCheck, Database,
  ShieldAlert, Layers, UsersRound, ScrollText, Settings, UserPlus, X, Search, Check, MailPlus, ShieldCheck, Tag, KeyRound, Rocket,
  CircleCheck, Lock, Minus, Plus, Clock, GitCompare, Stethoscope, type LucideIcon,
} from 'lucide-react'
import QRCode from 'qrcode'
import VButton from '@/components/common/VButton'
import VSwitch from '@/components/common/VSwitch'
import VSelect from '@/components/common/VSelect'
import TagEditModal from '@/components/modals/TagEditModal'
import api from '@/api'
import { confirmAction } from '@/lib/confirm'
import { sameForm } from '@/lib/unsavedChanges'
import { BUILTIN_ROLE_NAME, BUILTIN_ROLE_LAYER, builtinLabel } from '@/lib/builtinNames'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import { useEnvTierStore } from '@/stores/envtier'
import QueryGrantsPanel from '@/components/perms/QueryGrantsPanel'
import { relTime, absTime } from '@/lib/relTime'
import { useNow } from '@/hooks/useNow'
import { useI18n } from '@/hooks/useI18n'
import { useUnsavedFlag } from '@/hooks/useUnsavedFlag'
import { useStateRef } from '@/hooks/useStateRef'
import { useMountEffect } from '@/hooks/effects'
import type { RoleBrief, RoleDetail, UserView } from '@/types'
import './PermissionsView.scoped.css'

/**
 * 内置角色的 code,镜像自 bootstrap/seed.go 里种下的那五个。
 *
 * 服务端没有 builtin 标志位,所以这份清单是**复制过来的事实** —— 和
 * lib/envTierLabels.ts 里的 BUILTIN_TIER_LABEL 是同一种做法,也同样要求:种子改了
 * 这里得跟着改。写错的代价只是少一个灰色小标,不影响任何判定;但写错过一次
 * (admin/dba_owner/dba_l2/dev/auditor,五个里只有一个对得上),所以把出处写在这里。
 */
const BUILTIN_ROLES = ['admin', 'owner', 'l2', 'ro', 'audit']
const isBuiltin = (code: string) => BUILTIN_ROLES.includes(code)

const roleIcon: Record<string, LucideIcon> = { crown: Crown, shield: Shield, 'user-cog': UserCog, code: Code, eye: Eye }
function RoleIcon({ icon, size }: { icon: string; size: number }) {
  const Icon = roleIcon[icon] || Shield
  return <Icon size={size} />
}
// One row per capability dimension, in the backend's order (model.Capabilities).
// 'release' is the newest: it gates RAISING a release on a tier, which is a
// different question from whether the statement inside it may run — the pipeline
// still asks write/ddl again at execute time.
//
// 'release' 曾漏在这份清单外(只有文案在 capLabels 里):这一行从没渲染出来。内置角色
// 有种子值不受影响,但新建角色全 deny,发布单这一格在界面上永远打不开 —— 挂上它的
// 服务账号经开放接口建单一律 403(2026-10-31)。
const capKeys = ['select', 'write', 'ddl', 'grant', 'conn', 'approve', 'explain', 'release']
const capLabels = ['capSelect', 'capWrite', 'capDdl', 'capGrant', 'capConn', 'capApprove', 'capExplain', 'capRelease']
const menuDefs = [
  { key: 'terminal', icon: SquareTerminal, label: 'm_terminal' },
  { key: 'approve', icon: ClipboardCheck, label: 'm_approve' },
  { key: 'db', icon: Database, label: 'm_db' },
  { key: 'rules', icon: ShieldAlert, label: 'm_rules' },
  { key: 'envtier', icon: Layers, label: 'm_envtier' },
  { key: 'perms', icon: UsersRound, label: 'm_perms' },
  { key: 'audit', icon: ScrollText, label: 'm_audit' },
  { key: 'settings', icon: Settings, label: 'm_settings' },
  // 发布流程 (CI/CD). The releases and flows pages share this key; the SQL-review
  // page rides 'terminal' on purpose — self-checking your own change is part of
  // writing it, not a separate privilege.
  { key: 'pipeline', icon: Rocket, label: 'm_pipeline' },
  // 执行窗口(班车)。它是**申请**权,不是管理权 —— 批不批由审批链决定,所以它
  // 跟着 terminal 发,而不是跟着 envtier。
  { key: 'execwindow', icon: Clock, label: 'm_execwindow' },
  // 业务方数据查询页。查哪些库由查询授权决定,这里只管能不能进这一页。
  { key: 'query', icon: Search, label: 'm_query' },
  // 跨环境表结构对比。能进这一页之外,还要对左右两个实例都有访问权限。
  { key: 'schemadiff', icon: GitCompare, label: 'm_schemadiff' },
  // AI 巡检报告。后端另有角色校验(平台管理员、DBA 负责人、DBA L2),勾给别的角色也看不到。
  { key: 'inspection', icon: Stethoscope, label: 'm_inspection' },
]
// 三种判定各自一个图标 + 一块语义底色。原先是三个裸字符(✓ ⚑ —),同一个字号、
// 同一种粗细,扫一列下去要逐格辨认;而这张表最常做的动作恰恰是"扫一列"。
const sym: Record<string, { icon: LucideIcon; cls: string; tip: string }> = {
  allow: { icon: CircleCheck, cls: 'allow', tip: 'capTipAllow' },
  approve: { icon: Lock, cls: 'approve', tip: 'capTipApprove' },
  deny: { icon: Minus, cls: 'deny', tip: 'capTipDeny' },
}
const cycleOrder = ['allow', 'approve', 'deny']
// 管理类菜单只给平台管理员角色(管理后台拆分,工单 03;后端 model.AdminOnlyMenus 同一份清单)。
// 别的角色上这几个开关灰掉 —— 勾上去后端也会拒绝;存量的勾选在网关启动时已收回。
const ADMIN_ONLY_MENUS = ['db', 'rules', 'envtier', 'perms', 'settings']

const sortedIds = (xs: number[]) => [...xs].sort((a, b) => a - b)
const toggleId = (xs: number[], id: number) => (xs.includes(id) ? xs.filter((x) => x !== id) : [...xs, id])

type RoleForm = { name: string; layer: string; description: string; defaultConnRole: string; canApprove: boolean }

export default function PermissionsView() {
  const { t } = useI18n()
  // 出厂角色的名字与层级按读者语言显示;管理员改过名的原样显示(lib/builtinNames)。
  const roleName = (s: string) => builtinLabel(BUILTIN_ROLE_NAME, s, t)
  const roleLayer = (s: string) => builtinLabel(BUILTIN_ROLE_LAYER, s, t)
  const envtier = useEnvTierStore()
  const notifyError = (e: unknown, fallback: string) => useUIStore.getState().notifyError(e, fallback)
  // Role/permission + user-credential edits are platform-admin only (backend
  // enforces this too); non-admins see the page read-only.
  // Admin if ANY held role is admin (union), falling back to the primary role code.
  const isAdmin = useAuthStore((s) => s.me?.roleCodes?.includes('admin') ?? (s.me?.roleCode === 'admin'))
  // 角色与用户都到了才报数:GET /users 比角色慢,先到的那一半配上一个 0 读起来像"没有用户"
  // (黑盒 2026-10 工单 05)。没到之前副标题与页签提示都写"加载中"。
  const [rolesLoaded, setRolesLoaded, rolesLoadedRef] = useStateRef(false)
  const [usersLoaded, setUsersLoaded, usersLoadedRef] = useStateRef(false)
  function publishSub() {
    useUIStore.getState().setPageSub(rolesLoadedRef.current && usersLoadedRef.current
      ? { key: 'subPerms', params: { r: rolesRef.current.length, u: usersRef.current.length } }
      : { key: 'pmLoading' })
  }
  const [view, setView] = useState<'roles' | 'users' | 'grants'>('roles')
  // 「最近活跃」现在是真实时间(RFC3339);老数据里是建号时写的"—"/"刚刚",认不出就原样显示
  const nowTick = useNow()
  const lastActiveText = (s: string) =>
    /^\d{4}-\d\d-\d\dT/.test(s || '') ? relTime((k, n) => t(k, n || {}), s, nowTick) : (s || '—')
  const [roles, setRoles, rolesRef] = useStateRef<RoleBrief[]>([])
  const [detail, setDetail, detailRef] = useStateRef<RoleDetail | null>(null)
  const [users, setUsers, usersRef] = useStateRef<UserView[]>([])
  const [memberForm, setMemberForm] = useState(false)
  const [inviteForm, setInviteForm] = useState(false)
  const [roleForm, setRoleForm] = useState(false)
  // 同一个弹窗两种用法:新建(create)与编辑当前选中的角色(edit)。从前「新建角色」打开的也是编辑表单,
  // 保存会改掉当前选中的那个角色(2026-10-22)。
  const [roleMode, setRoleMode] = useState<'create' | 'edit'>('edit')
  const [edit, setEdit] = useState<RoleForm>({ name: '', layer: '', description: '', defaultConnRole: 'dba_l2', canApprove: true })
  const [roleSnap, setRoleSnap] = useState<RoleForm | null>(null)
  const patchEdit = (p: Partial<RoleForm>) => setEdit((v) => ({ ...v, ...p }))
  const [inviteEmail, setInviteEmail] = useState('')
  const [inviteRoleName, setInviteRoleName] = useState('')
  const [memberSearch, setMemberSearch] = useState('')

  // 角色搜索。角色是一次性全量拉下来的(api.roles 不分页),所以在客户端筛是对的 ——
  // 和审批页那个必须走服务端的搜索框不是一回事。
  const [roleQ, setRoleQ] = useState('')
  const roleKw = roleQ.trim().toLowerCase()
  const filteredRoles = !roleKw ? roles
    : roles.filter((r) => r.name.toLowerCase().includes(roleKw) || roleName(r.name).toLowerCase().includes(roleKw) || r.code.toLowerCase().includes(roleKw))

  // 成员的邮箱来自本页已经加载的用户列表(挂载时 loadUsers 已经跑过),按 id
  // 关联即可 —— 不为此再发一次请求。
  //
  // **加入时间没有**:tbl_role_member 只有 role_id + user_id 两列,库里根本没记
  // 这个时刻。宁可不显示,也不摆一个看着像真的的假时间。
  const memberEmail = (id: number) => users.find((u) => u.id === id)?.email || ''

  const memberKw = memberSearch.trim().toLowerCase()
  const filteredUsers = !memberKw ? users : users.filter(
    (u) => u.name.toLowerCase().includes(memberKw) || (u.dept || '').toLowerCase().includes(memberKw) || u.email.toLowerCase().includes(memberKw),
  )

  function showRoleForm(mode: 'create' | 'edit', form: RoleForm) {
    setRoleMode(mode)
    setEdit(form)
    setRoleSnap(form)
    setRoleForm(true)
  }
  function openRoleForm() {
    const d = detail
    if (!d) return
    // 回显角色的真实值。从前描述清空、「可审批」按 code 猜(只有 admin / owner 是开),保存时会把
    // 一个本来可审批的自定义角色悄悄改成不可审批。
    showRoleForm('edit', {
      name: d.name, layer: d.layer, description: d.description || '',
      defaultConnRole: d.defaultConnRole || (d.code === 'ro' ? 'readonly' : 'dba_l2'),
      canApprove: d.canApprove ?? (d.code === 'admin' || d.code === 'owner'),
    })
  }
  // 删除自定义角色。有成员、被发布流程引用时服务端会拒,原因原样提示(要人先移走成员 / 换掉流程里的角色)
  async function deleteRole() {
    const d = detail
    if (!d || !d.deletable) return
    if (!(await confirmAction(t('drConfirm', { name: roleName(d.name) })))) return
    try {
      await api.deleteRole(d.id)
      useUIStore.getState().notify(t('drDone', { name: roleName(d.name) }), 'success')
      setDetail(null)
      await loadRoles() // 列表为空的选中项会落到第一个角色上
    } catch (e) {
      notifyError(e, t('actionFailed'))
    }
  }
  // 新建角色:什么权限都不带,建好后选中它,在右边的矩阵 / 菜单里逐项打开
  function openNewRole() {
    showRoleForm('create', { name: '', layer: '', description: '', defaultConnRole: 'readonly', canApprove: false })
  }
  async function saveRole() {
    // M14: 保存角色失败以 toast 呈现
    try {
      if (roleMode === 'create') {
        if (!edit.name.trim()) return
        const created = await api.createRole({
          name: edit.name.trim(), layer: edit.layer.trim(), description: edit.description.trim(),
          defaultConnRole: edit.defaultConnRole.trim(), canApprove: edit.canApprove,
        })
        await loadRoles()
        setDetail(created)
        setRoleForm(false)
        useUIStore.getState().notify(t('nrCreated', { name: created.name }), 'success')
        return
      }
      if (!detailRef.current) return
      // 层级说明只在新建时填,编辑接口不收它
      const body = { name: edit.name, description: edit.description, defaultConnRole: edit.defaultConnRole, canApprove: edit.canApprove }
      setDetail(await api.updateRole(detailRef.current.id, body))
      await loadRoles()
      setRoleForm(false)
    } catch (e) {
      notifyError(e, t('actionFailed'))
    }
  }
  function openInvite() {
    setInviteEmail('')
    setInviteRoleName(roleName(roles.find((r) => r.code === 'ro')?.name || roles[0]?.name || ''))
    setInviteForm(true)
  }
  async function doInvite() {
    const roleId = roles.find((r) => roleName(r.name) === inviteRoleName)?.id
    if (!inviteEmail.trim() || !roleId) return
    // M14: 邀请失败以 toast 呈现
    try {
      await api.invite(inviteEmail.trim(), roleId)
      setInviteForm(false)
      await loadUsers()
    } catch (e) {
      notifyError(e, t('actionFailed'))
    }
  }

  // One column per control tier, in the tier list's own order. This was the four
  // built-in strings; the matrix is keyed by tier and tiers are rows now, so a
  // hardcoded list would silently omit any tier added later — and an omitted
  // column is an ungoverned cell, since a capability with no row reads as allow.
  const envCols = envtier.tierCodes
  // The grid was four fixed columns. Each tier now needs a minimum width so a long
  // list scrolls rather than squeezing every cell into an unreadable sliver.
  const matrixCols = {
    gridTemplateColumns: `minmax(150px, 2fr) repeat(${envCols.length}, minmax(88px, 1fr))`,
  }

  async function loadRoles() {
    // M14: 加载失败以 toast 呈现，避免静默失败
    try {
      setRoles((await api.roles()) ?? [])
      setRolesLoaded(true)
      if (rolesRef.current.length && !detailRef.current) await selectRole(rolesRef.current[0].id)
      publishSub()
    } catch (e) {
      notifyError(e, t('loadFailed'))
    }
  }
  async function selectRole(id: number) {
    // M14: 加载失败以 toast 呈现
    try { setDetail(await api.role(id)) } catch (e) { notifyError(e, t('loadFailed')) }
  }

  // ---- DB access tags (assign databases to this group by tag) ----
  const [allTags, setAllTags] = useState<string[]>([])
  const [tagModalOpen, setTagModalOpen] = useState(false)
  async function saveRoleTags(tags: string[]) {
    if (!detailRef.current) return
    // M14: 保存标签失败以 toast 呈现
    try {
      setDetail(await api.setRoleTags(detailRef.current.id, tags))
      setTagModalOpen(false)
    } catch (e) {
      notifyError(e, t('actionFailed'))
    }
  }
  // ---------------------------------------------------------------- SSO 绑定
  // 绑定发生在这个人第一次经 SRD 授权登录、邮箱对上的时候(服务端完成)。这里只展示,并让
  // 管理员解绑:解绑后恢复口令登录,下次再经 SRD 登录会重新绑定。
  const [ssoEnabled, setSsoEnabled] = useState(false)

  async function unbindSso(u: UserView) {
    if (!isAdmin || !u.ssoBound || !(await confirmAction(t('ssoUnbindConfirm', { name: u.name })))) return
    try { await api.ssoUnbind(u.id); useUIStore.getState().notify(t('ssoUnbindDone'), 'success'); await loadUsers() } catch (e) { notifyError(e, t('actionFailed')) }
  }

  async function loadUsers() {
    // M14: 加载失败以 toast 呈现
    try { setUsers((await api.users()) ?? []); setUsersLoaded(true); publishSub() } catch (e) { notifyError(e, t('loadFailed')) }
  }

  async function init() {
    publishSub()
    // Tiers decide the matrix columns, so load them before anything renders cells.
    await useEnvTierStore.getState().load().catch(() => {})
    await Promise.all([loadRoles(), loadUsers()])
    try { setSsoEnabled((await api.ssoConfig()).enabled) } catch { setSsoEnabled(false) }
    try { setAllTags((await api.tags()) ?? []) } catch { /* ignore */ }
  }
  useMountEffect(() => { void init() })

  const cellLevel = (d: RoleDetail, cap: string, env: string): string => d.matrix?.[cap]?.[env] || 'allow'
  async function cycleCell(cap: string, env: string) {
    const d = detailRef.current
    if (!d || !isAdmin) return
    const cur = cellLevel(d, cap, env)
    const next = cycleOrder[(cycleOrder.indexOf(cur) + 1) % 3]
    const matrix = { ...d.matrix, [cap]: { ...d.matrix?.[cap], [env]: next } }
    setDetail({ ...d, matrix })
    // M14: 保存能力矩阵失败以 toast 呈现
    try {
      await api.setCapabilities(d.id, matrix)
    } catch (e) {
      notifyError(e, t('actionFailed'))
    }
  }
  const menuLocked = (key: string) => !!detail && detail.code !== 'admin' && ADMIN_ONLY_MENUS.includes(key)
  async function toggleMenu(key: string) {
    const d = detailRef.current
    if (!d || !isAdmin || menuLocked(key)) return
    const menus = { ...d.menus, [key]: !d.menus?.[key] }
    setDetail({ ...d, menus })
    // M14: 保存菜单权限失败以 toast 呈现
    try {
      await api.setMenus(d.id, menus)
    } catch (e) {
      notifyError(e, t('actionFailed'))
    }
  }
  async function removeMember(userId: number) {
    if (!detail) return
    // M15: 移除角色成员前二次确认
    const mb = members.find((m) => m.id === userId)
    if (!(await confirmAction(t('pmRemoveMemberConfirm', { role: roleName(detail.name), name: mb?.name || userId })))) return
    // M14: 移除失败以 toast 呈现
    try {
      setDetail(await api.removeMember(detail.id, userId))
      await loadRoles()
    } catch (e) {
      notifyError(e, t('actionFailed'))
    }
  }
  async function addMember(userId: number) {
    if (!detailRef.current) return
    // M14: 添加成员失败以 toast 呈现
    try {
      setDetail(await api.addMember(detailRef.current.id, userId))
      await loadRoles()
    } catch (e) {
      notifyError(e, t('actionFailed'))
    }
  }
  async function toggleUser(u: UserView) {
    if (!isAdmin) return
    // 停不了的先说原因,别让人白确认一次 —— "确认停用林伟?"→点确定→"不能停用自己"
    // 是很难受的一段。canDisable 由服务端算(自己/最后一位管理员),前端只照着显示。
    if (u.status !== 'disabled' && u.canDisable === false) {
      useUIStore.getState().notify(u.disableBlock || t('actionFailed'), 'error', 5000)
      return
    }
    // M15: 禁用用户前二次确认（启用无需确认）
    if (u.status !== 'disabled' && !(await confirmAction(t('pmDisableUserConfirm', { name: u.name })))) return
    // M14: 启停失败以 toast 呈现
    try {
      await api.patchUser(u.id, { status: u.status === 'disabled' ? 'active' : 'disabled' })
      await loadUsers()
    } catch (e) {
      notifyError(e, t('actionFailed'))
    }
  }

  // ---- admin user management: password reset + OTP binding ----
  // ---- per-user data-access scope ----
  //
  // Tags are otherwise granted to ROLES, where "no tags" means unrestricted. That
  // cannot express "this particular person", so a grant made here is the more
  // specific statement and REPLACES the role scope; clearing it falls back to the
  // role. See backend model.UserTag.
  const [userTags, setUserTags] = useState<string[]>([])
  const [userTagsOpen, setUserTagsOpen] = useState(false)
  const [tagMsg, setTagMsg] = useState('')

  async function loadUserTags(id: number) {
    try { setUserTags((await api.userTags(id)) ?? []) } catch { setUserTags([]) }
  }
  async function saveUserTags(tags: string[]) {
    if (!userModalRef.current) return
    try {
      await api.setUserTags(userModalRef.current.id, tags)
      setUserTags(tags)
      setUserTagsOpen(false)
      setTagMsg(tags.length ? t('utScoped', { n: tags.length }) : t('utRoleDefault'))
    } catch (e) {
      notifyError(e, t('actionFailed'))
    }
  }

  const [userModal, setUserModal, userModalRef] = useStateRef<UserView | null>(null)
  const [newPw, setNewPw] = useState('')
  const [pwMsg, setPwMsg] = useState('')
  const [otpMsg, setOtpMsg] = useState('')
  const [otpBind, setOtpBind] = useState<{ secret: string; otpauthUri: string } | null>(null)
  const [otpQr, setOtpQr] = useState('')

  function openUser(u: UserView) {
    if (!isAdmin) return
    setUserModal(u)
    setNewPw(''); setPwMsg(''); setOtpMsg(''); setOtpBind(null); setOtpQr('')
    setRoleSel([...(u.roleIds || [])]); setRoleMsg('')
    setTagMsg(''); setUserTags([])
    void loadUserTags(u.id)
  }

  // ---- per-user role assignment (a user may hold several roles; permissions
  // compose as the union across them). The first selected role is the primary. ----
  const [roleSel, setRoleSel] = useState<number[]>([])
  const [roleMsg, setRoleMsg] = useState('')
  async function saveRoles() {
    if (!userModalRef.current || !roleSel.length) { setRoleMsg(t('urNeedOne')); return }
    try {
      await api.setUserRoles(userModalRef.current.id, roleSel)
      setRoleMsg(t('urSaved'))
      await loadUsers(); syncModal()
      if (userModalRef.current) setRoleSel([...(userModalRef.current.roleIds || [])])
    } catch { setRoleMsg(t('urFailed')) }
  }

  // ---- admin: create an account directly (initial password + roles), an
  // alternative to invite-only onboarding. The account is active immediately. ----
  const [createForm, setCreateForm] = useState(false)
  const [createEmail, setCreateEmail] = useState('')
  const [createName, setCreateName] = useState('')
  const [createPw, setCreatePw] = useState('')
  const [createRoleIds, setCreateRoleIds] = useState<number[]>([])
  const [createMsg, setCreateMsg] = useState('')
  function openCreate() {
    setCreateEmail(''); setCreateName(''); setCreatePw(''); setCreateMsg('')
    const def = roles.find((r) => r.code === 'ro') || roles[0]
    setCreateRoleIds(def ? [def.id] : [])
    setCreateForm(true)
  }
  async function doCreate() {
    if (!createEmail.trim() || createPw.length < 8 || !createRoleIds.length) {
      setCreateMsg(t('cuInvalid')); return
    }
    try {
      await api.createUser({
        email: createEmail.trim(), name: createName.trim(),
        password: createPw, roleIds: createRoleIds,
      })
      setCreateForm(false)
      await loadUsers()
    } catch (e) {
      // 服务端会说清是哪一项不对(邮箱格式、重复、角色、长度……),写在表单里,表单不关,改了再提交
      setCreateMsg((e as any)?.message || t('cuFailed'))
    }
  }
  // 有没保存的改动(管理后台空闲锁屏,2026-10-26 回归排查 10):角色表单改过、建号 / 邀请表单填了东西、
  // 用户弹窗里填了新密码或改了角色勾选。能力矩阵与菜单开关是即点即存的,不算。
  useUnsavedFlag((roleForm && !sameForm(edit, roleSnap))
    || (createForm && !!(createEmail || createName || createPw))
    || (inviteForm && !!inviteEmail.trim())
    || (!!userModal && (!!newPw || !sameForm(sortedIds(roleSel), sortedIds(userModal.roleIds || [])))))
  function syncModal() {
    const m = userModalRef.current
    if (m) setUserModal(usersRef.current.find((x) => x.id === m.id) || m)
  }
  async function savePw() {
    if (!userModalRef.current) return
    if (newPw.length < 8) { setPwMsg(t('pwTooShort')); return }
    try { await api.setUserPassword(userModalRef.current.id, newPw); setPwMsg(t('pwSaved')); setNewPw('') }
    catch (e) { setPwMsg((e as any)?.message || t('pwFailed')) }
  }
  async function bindOtp() {
    if (!userModalRef.current) return
    try {
      const b = await api.bindUserMfa(userModalRef.current.id)
      setOtpBind(b)
      setOtpQr(await QRCode.toDataURL(b.otpauthUri, { margin: 1, width: 160 }))
      setOtpMsg('')
      await loadUsers(); syncModal()
    } catch { setOtpMsg(t('otpFailed')) }
  }
  async function resetOtp() {
    const m = userModalRef.current
    if (!m) return
    // M15: 解绑他人 MFA 前二次确认
    if (!(await confirmAction(t('pmResetMfaConfirm', { name: m.name })))) return
    try { await api.resetUserMfa(m.id); setOtpBind(null); setOtpQr(''); setOtpMsg(t('otpUnbound')); await loadUsers(); syncModal() }
    catch { setOtpMsg(t('otpFailed')) }
  }

  const selName = roleName(detail?.name || '')
  const memberIds = new Set(detail?.memberIds || [])
  // 角色清单(RoleBrief)里的 members 有时是个人数,不是名单
  const members = Array.isArray(detail?.members) ? detail.members : []
  const tags = detail?.tags ?? []

  return (
    <div className="wrap">
      {/* 这一页是全出血的分栏布局,.wrap 自己没有内边距 —— 页头得自带,不然会贴到左缘。 */}
      <div className="v-head pm-head">
        <div className="v-head-l">
          <div className="v-eyebrow">ROLES &amp; PERMISSIONS</div>
        </div>
      </div>
      <div className="vtabs">
        <div className={clsx('vt', view === 'roles' && 'active')} onClick={() => setView('roles')}>{t('tabRoleView')}</div>
        <div className={clsx('vt', view === 'users' && 'active')} onClick={() => setView('users')}>{t('tabUserView')}</div>
        <div className={clsx('vt', view === 'grants' && 'active')} data-testid="tab-grants" onClick={() => setView('grants')}>{t('tabGrantView')}</div>
        {/* 在线用户移到了管理总览(管理后台拆分,工单 06) */}
        <div className="vhint" data-testid="perms-vhint">
          {view === 'roles' ? (rolesLoaded ? t('pmRolesHint', { n: roles.length }) : t('pmLoading'))
            : view === 'users' ? (usersLoaded ? t('pmUsersHint', { n: users.length }) : t('pmLoading')) : t('pmGrantsHint')}
        </div>
      </div>

      {view === 'grants' ? (
        <div className="scy userpage"><QueryGrantsPanel isAdmin={isAdmin} /></div>
      ) : view === 'roles' ? (
        // ROLE VIEW
        <div className="rolegrid">
          {/* 左:角色导航 */}
          <div className="rolepanel">
            <div className="rphead">
              <div className="eyebrow">{t('rolesTier')}</div>
              {isAdmin && <VButton variant="primary" height="30px" data-testid="pm-new-role" onClick={openNewRole}><Plus size={14} />{t('pmNewRole')}</VButton>}
            </div>
            <div className="rsearch">
              <Search size={14} color="var(--text-faint)" />
              <input value={roleQ} onChange={(e) => setRoleQ(e.target.value)} placeholder={t('pmRoleSearchPh')} spellCheck={false} />
              {roleQ && <button className="sclear" title={t('apSearchClear')} onClick={() => setRoleQ('')}><X size={13} /></button>}
            </div>
            <div className="scy rl">
              {filteredRoles.map((r) => (
                <div key={r.id} className={clsx('ritem', detail?.id === r.id && 'active')} onClick={() => selectRole(r.id)}>
                  <div className="rname">
                    <RoleIcon icon={r.icon} size={15} />
                    <span className="rn">{roleName(r.name)}</span>
                    {isBuiltin(r.code) && <span className="builtin">{t('pmBuiltin')}</span>}
                  </div>
                  <div className="rlayer">{roleLayer(r.layer)}<span className="sep">·</span>{t('pmMembersN', { n: r.count })}</div>
                </div>
              ))}
              {!filteredRoles.length && <div className="rempty">{t('pmNoRole')}</div>}
            </div>
          </div>

          {/* 右:权限详情。外层负责滚动,内层卡片限宽 —— 宽屏上一张横向铺满的表格,
              眼睛要从最左的动作名一路扫到最右的开关,中间全是空白。 */}
          {detail && (
            <div className="scy detailwrap">
              <div className="detail">
                <div className="mhead">
                  <div className="grow">
                    <div className="mtitle">{selName}<span className="mtsub">{t('matrixTitleSuffix')}</span></div>
                    <div className="mhint">{roleLayer(detail.layer)}<span className="sep">·</span>{t('pmMembersN', { n: members.length })}<span className="sep">·</span>{t('matrixHint')}</div>
                  </div>
                  {!isAdmin && <span className="roflag">{t('readOnlyPerms')}</span>}
                  {isAdmin && <VButton variant="secondary" height="34px" onClick={openRoleForm}>{t('editRole')}</VButton>}
                  {isAdmin && detail.deletable && <VButton variant="danger" height="34px" data-testid="pm-delete-role" onClick={deleteRole}>{t('drBtn')}</VButton>}
                </div>

                {/* 模块一:操作权限矩阵 */}
                <div className="sec">
                  <div className="sechead"><span className="eyebrow2">{t('matrixSection')}</span></div>
                  {/* Scrolls inside its own box once the tier count outgrows the width;
                      the page body must never scroll sideways. */}
                  <div className="mtable scx">
                    {/* The template goes on each ROW: they are the grids. Binding it to the
                        wrapper instead left the rows with no columns at all, so every cell
                        stacked vertically in one implicit column. */}
                    <div className="mgrid">
                      <div className="mth" style={matrixCols}>
                        <span>{t('colCap')}</span>
                        {/* 环境列头带分层色。颜色来自 envtier.dotFor —— 和树、审批列表、
                            分层页是同一个来源,不在这一页另配一份。 */}
                        {envCols.map((env) => (
                          <span key={env} className="ctr">
                            <span className={clsx('envhd', envtier.dotFor(env))}>{env.toUpperCase()}</span>
                          </span>
                        ))}
                      </div>
                      {capKeys.map((cap, i) => (
                        <div key={cap} className="mtr" style={matrixCols}>
                          <span className="cap">{t(capLabels[i])}</span>
                          {envCols.map((env) => {
                            const s = sym[cellLevel(detail, cap, env)] ?? sym.allow
                            return (
                              <div key={env} className="cellwrap">
                                <button className={clsx('cell', s.cls, !isAdmin && 'ro')} title={t(s.tip)} onClick={() => cycleCell(cap, env)}>
                                  <s.icon size={14} />
                                </button>
                              </div>
                            )
                          })}
                        </div>
                      ))}
                    </div>
                  </div>
                  <div className="legend">
                    <span className="lg"><span className="cell allow sm"><CircleCheck size={12} /></span>{t('capTipAllow')}</span>
                    <span className="lg"><span className="cell approve sm"><Lock size={12} /></span>{t('capTipApprove')}</span>
                    <span className="lg"><span className="cell deny sm"><Minus size={12} /></span>{t('capTipDeny')}</span>
                  </div>
                </div>

                {/* 模块二:功能菜单。网格卡片,开关紧跟在名字后面 —— 原先是一行一项,
                    名字在最左、开关在屏幕最右,一千多像素的空白把两者拉断了。 */}
                <div className="sec">
                  <div className="sechead"><span className="eyebrow2">{t('menuTitle')}</span><span className="secsub">{t('menuSub')}</span></div>
                  <div className="menugrid">
                    {menuDefs.map((m) => {
                      const locked = menuLocked(m.key)
                      const on = !!detail.menus?.[m.key]
                      return (
                        <label
                          key={m.key} className={clsx('menucard', on && 'on', locked && 'locked')}
                          title={locked ? t('pmAdminOnlyMenu') : undefined} data-testid={'menu-' + m.key}
                        >
                          <m.icon size={16} className="mi" />
                          <span className="ml">{t(m.label)}</span>
                          {locked && <span className="mlock">{t('pmAdminOnlyTag')}</span>}
                          <VSwitch value={on} disabled={!isAdmin || locked} onChange={() => toggleMenu(m.key)} />
                        </label>
                      )
                    })}
                  </div>
                </div>

                {/* 模块三:授权范围 + 成员,分两块 */}
                <div className="sec">
                  <div className="sechead"><span className="eyebrow2">{t('tagAccessTitle')}</span><span className="secsub">{tags.length ? t('tagAccessSub') : detail.noInstances ? t('tagAccessNone') : t('tagAccessAll')}</span></div>
                  <div className="rtags">
                    {tags.map((tg) => <span key={tg} className="rtag">{tg}</span>)}
                    {!tags.length && <span className="rtagall">{detail.noInstances ? t('tagNoneBadge') : t('tagAllBadge')}</span>}
                    {isAdmin && <button className="rtedit" onClick={() => setTagModalOpen(true)}><Tag size={12} />{t('tagAssign')}</button>}
                  </div>
                </div>

                <div className="sec last">
                  <div className="sechead">
                    <span className="eyebrow2">{t('membersTitle')}</span>
                    <span className="secsub">{t('pmMembersN', { n: members.length })}</span>
                    {isAdmin && (
                      <VButton className="secbtn" variant="secondary" height="30px" onClick={() => setMemberForm(true)}>
                        <UserPlus size={14} />{t('addMember')}
                      </VButton>
                    )}
                  </div>
                  <div className="memgrid">
                    {/* 邮箱来自本页已加载的用户列表。**加入时间没有** —— tbl_role_member
                        只有 role_id + user_id,库里就没记这个时刻,不摆一个假的。 */}
                    {members.map((mb, i) => (
                      <div key={mb.id} className="memcard">
                        <div className={clsx('mava', i === 0 && 'first')}>{mb.initials}</div>
                        <div className="mbody">
                          <div className="mn">{mb.name}</div>
                          <div className="me">{memberEmail(mb.id) || mb.dept || '—'}</div>
                        </div>
                        {isAdmin && <button className="mx" title={t('pmRemoveMember')} onClick={() => removeMember(mb.id)}><X size={13} /></button>}
                      </div>
                    ))}
                    {!members.length && <div className="memempty">{t('pmNoMember')}</div>}
                  </div>
                </div>
              </div>
            </div>
          )}
        </div>
      ) : (
        // USER VIEW
        <div className="scy userpage">
          <div className="uhead">
            <div>
              <div className="utitle">{t('usersTitle')} · {users.length} {t('people')}</div>
              <div className="usub">{t('usersSub')}</div>
            </div>
            {isAdmin && (
              <div className="uacts">
                <VButton variant="secondary" onClick={openInvite}>{t('invite')}</VButton>
                <VButton variant="primary" onClick={openCreate}><UserPlus size={15} />{t('addAccount')}</VButton>
              </div>
            )}
          </div>
          <div className="utable">
            <div className="uth"><span>{t('colUser')}</span><span>{t('colAcct')}</span><span>{t('colRoles')}</span><span>{t('colActive')}</span><span className="r">{t('colStatus')}</span></div>
            {users.map((u, i) => {
              const enabled = u.status !== 'disabled'
              return (
                <div key={u.id} className="utr" style={{ opacity: enabled ? 1 : 0.5 }}>
                  <div className="ucell"><div className={clsx('uava', i === 0 && 'first')}>{u.initials}</div><div><div className="un">{u.name}{u.kind === 'service' && <span className="svcbadge">{t('saMark')}</span>}</div><div className="ud">{u.dept}</div></div></div>
                  <span className="mono mute">{u.email}</span>
                  <div className="uroles">
                    {(u.roles ?? []).map((rn) => <span key={rn} className="urole">{roleName(rn)}</span>)}
                    {!u.roles?.length && <span className="mono mute">{t('unassigned')}</span>}
                  </div>
                  <span className="mono mute" title={absTime(u.lastActive)}>{lastActiveText(u.lastActive)}</span>
                  <div className="ustatus">
                    <span className={clsx('otptag', u.mfaEnabled && 'on')} title={u.mfaEnabled ? t('otpBound') : t('otpUnboundState')}><ShieldCheck size={12} />OTP</span>
                    {ssoEnabled && u.kind !== 'service' && (
                      <span
                        className={clsx('otptag ssotag', u.ssoBound && 'on', isAdmin && u.ssoBound && 'act')}
                        title={u.ssoBound ? t('ssoBoundTip', { sso: u.ssoUserName || u.email }) : t('ssoUnboundTip')}
                        data-testid={'sso-tag-' + u.id} onClick={() => unbindSso(u)}
                      >SSO</span>
                    )}
                    <div
                      className="stbadge"
                      style={{ background: enabled ? 'var(--success-subtle)' : 'var(--surface-sunken)', color: enabled ? 'var(--success-text)' : 'var(--text-muted)', cursor: isAdmin ? 'pointer' : 'default' }}
                      onClick={() => toggleUser(u)}
                    >
                      <span className="dotc" />{enabled ? t('enabled') : t('disabled')}
                    </div>
                    {isAdmin && <button className="manage" title={t('manage')} onClick={() => openUser(u)}><UserCog size={15} /></button>}
                  </div>
                </div>
              )
            })}
          </div>
          <div className="ufoot">{t('userFootHint')}</div>
        </div>
      )}

      {/* member picker */}
      {memberForm && (
        <div className="overlay">
          <div className="mask" onClick={() => setMemberForm(false)} />
          <div className="pmodal">
            <div className="phead"><div className="pic"><UserPlus size={19} color="var(--accent-text)" /></div><div><div className="pt">{t('mpTitle')}{selName}</div><div className="ps">{t('mpSub')}</div></div><X size={18} className="px" onClick={() => setMemberForm(false)} /></div>
            <div className="psearch"><Search size={14} color="var(--text-faint)" /><input value={memberSearch} onChange={(e) => setMemberSearch(e.target.value)} placeholder={t('mpSearch')} /></div>
            <div className="scy plist">
              {filteredUsers.map((u) => (
                <div key={u.id} className="prow" onClick={() => { if (!memberIds.has(u.id)) void addMember(u.id) }}>
                  <div className="pava">{u.initials}</div>
                  <div className="pgrow"><div className="pn">{u.name}</div><div className="pd">{u.dept}</div></div>
                  {memberIds.has(u.id)
                    ? <span className="joined"><Check size={14} />{t('mpJoined')}</span>
                    : <span className="addbtn"><span className="ic">+</span>{t('mpAdd')}</span>}
                </div>
              ))}
            </div>
            <div className="pfoot"><div className="pfh">{t('mpFoot')}</div><VButton variant="primary" height="38px" onClick={() => setMemberForm(false)}>{t('done')}</VButton></div>
          </div>
        </div>
      )}

      <TagEditModal
        open={userTagsOpen} title={t('utTitle')} subtitle={userModal ? userModal.email : ''}
        tags={userTags} suggestions={allTags}
        onClose={() => setUserTagsOpen(false)} onSave={saveUserTags}
      />

      {/* user management: password + OTP */}
      {userModal && (
        <div className="overlay">
          <div className="mask" onClick={() => setUserModal(null)} />
          <div className="pmodal">
            <div className="phead">
              <div className="pic"><UserCog size={19} color="var(--accent-text)" /></div>
              <div><div className="pt">{userModal.name}</div><div className="ps mono">{userModal.email}</div></div>
              <X size={18} className="px" onClick={() => setUserModal(null)} />
            </div>
            <div className="usec">
              <div className="uslbl"><KeyRound size={14} />{t('resetPw')}</div>
              <div className="pwrow">
                <input value={newPw} onChange={(e) => setNewPw(e.target.value)} type="password" placeholder={t('newPwPh')} onKeyUp={(e) => { if (e.key === 'Enter') void savePw() }} />
                <VButton variant="secondary" height="40px" onClick={savePw}>{t('save')}</VButton>
              </div>
              {pwMsg && <div className="umsg">{pwMsg}</div>}
            </div>
            <div className="usec">
              <div className="uslbl"><Tag size={14} />{t('utTitle')}</div>
              <div className="uthint">{t('utHint')}</div>
              <div className="utrow">
                {userTags.length
                  ? userTags.map((tg) => <span key={tg} className="uttag">{tg}</span>)
                  : <span className="utnone">{t('utRoleDefault')}</span>}
                <VButton variant="secondary" height="34px" onClick={() => setUserTagsOpen(true)}>{t('utEdit')}</VButton>
              </div>
              {tagMsg && <div className="umsg">{tagMsg}</div>}
            </div>
            <div className="usec">
              <div className="uslbl"><UsersRound size={14} />{t('urTitle')}{' '}
                <span className="urhint">{t('urHint')}</span>
              </div>
              <div className="rolepick">
                {roles.map((r) => (
                  <button key={r.id} type="button" className={clsx('rchip', roleSel.includes(r.id) && 'on')} onClick={() => setRoleSel((xs) => toggleId(xs, r.id))}>
                    {roleSel.includes(r.id) ? <Check size={13} /> : <RoleIcon icon={r.icon} size={13} />}{roleName(r.name)}
                  </button>
                ))}
              </div>
              <div className="urrow">
                <VButton variant="secondary" height="38px" disabled={!roleSel.length} onClick={saveRoles}>{t('urSave')}</VButton>
                {roleMsg && <span className="umsg inline">{roleMsg}</span>}
              </div>
            </div>
            <div className="usec">
              <div className="uslbl"><ShieldCheck size={14} />{t('otpBinding')}{' '}
                <span className={clsx('otpstate', userModal.mfaEnabled && 'on')}>{userModal.mfaEnabled ? t('otpBound') : t('otpUnboundState')}</span>
              </div>
              <div className="otprow">
                <VButton variant="secondary" height="38px" onClick={bindOtp}>{userModal.mfaEnabled ? t('otpRebind') : t('otpBind')}</VButton>
                {userModal.mfaEnabled && <VButton variant="danger" height="38px" onClick={resetOtp}>{t('otpReset')}</VButton>}
              </div>
              {otpBind && (
                <div className="otpresult">
                  <div className="qrbox">{otpQr && <img src={otpQr} alt="OTP QR" />}</div>
                  <div className="otpinfo">
                    <div className="oih">{t('otpScan')}</div>
                    <div className="oisecret">{otpBind.secret}</div>
                  </div>
                </div>
              )}
              {otpMsg && <div className="umsg">{otpMsg}</div>}
            </div>
          </div>
        </div>
      )}

      {/* invite */}
      {inviteForm && (
        <div className="overlay">
          <div className="mask" onClick={() => setInviteForm(false)} />
          <div className="imodal">
            <div className="ihead"><div className="iic"><MailPlus size={19} color="var(--accent-text)" /></div><div><div className="it">{t('ivTitle')}</div><div className="is">{t('ivSub')}</div></div><X size={18} className="ix" onClick={() => setInviteForm(false)} /></div>
            <div className="ibody">
              <div><div className="fl">{t('ivEmail')}</div><input value={inviteEmail} onChange={(e) => setInviteEmail(e.target.value)} placeholder="name@aegisdb.io" type="email" /></div>
              <div><div className="fl">{t('ivRole')}</div><VSelect value={inviteRoleName} onChange={setInviteRoleName} options={roles.map((r) => roleName(r.name))} /></div>
              <div className="inote"><ShieldCheck size={15} color="#2dcde6" /><div>{t('ivNote')}</div></div>
            </div>
            <div className="ifoot"><VButton variant="secondary" onClick={() => setInviteForm(false)}>{t('cancel')}</VButton><VButton variant="primary" disabled={!inviteEmail.trim()} onClick={doInvite}>{t('ivSend')}</VButton></div>
          </div>
        </div>
      )}

      {/* create account (admin: initial password + one or more roles) */}
      {createForm && (
        <div className="overlay">
          <div className="mask" onClick={() => setCreateForm(false)} />
          <div className="imodal">
            <div className="ihead"><div className="iic"><UserPlus size={19} color="var(--accent-text)" /></div><div><div className="it">{t('cuTitle')}</div><div className="is">{t('cuSub')}</div></div><X size={18} className="ix" onClick={() => setCreateForm(false)} /></div>
            <div className="ibody">
              <div><div className="fl">{t('ivEmail')}</div><input value={createEmail} onChange={(e) => setCreateEmail(e.target.value)} placeholder="name@aegisdb.io" type="email" /></div>
              <div><div className="fl">{t('cuName')}</div><input value={createName} onChange={(e) => setCreateName(e.target.value)} placeholder={t('cuNamePh')} type="text" /></div>
              <div><div className="fl">{t('cuPw')}</div><input value={createPw} onChange={(e) => setCreatePw(e.target.value)} placeholder={t('cuPwPh')} type="password" /></div>
              <div>
                <div className="fl">{t('cuRoles')}</div>
                <div className="rolepick">
                  {roles.map((r) => (
                    <button key={r.id} type="button" className={clsx('rchip', createRoleIds.includes(r.id) && 'on')} onClick={() => setCreateRoleIds((xs) => toggleId(xs, r.id))}>
                      {createRoleIds.includes(r.id) ? <Check size={13} /> : <RoleIcon icon={r.icon} size={13} />}{roleName(r.name)}
                    </button>
                  ))}
                </div>
              </div>
              <div className="inote"><ShieldCheck size={15} color="#2dcde6" /><div>{t('cuNote')}</div></div>
              {createMsg && <div className="umsg">{createMsg}</div>}
            </div>
            <div className="ifoot"><VButton variant="secondary" onClick={() => setCreateForm(false)}>{t('cancel')}</VButton><VButton variant="primary" disabled={!createEmail.trim() || createPw.length < 8 || !createRoleIds.length} onClick={doCreate}>{t('cuCreate')}</VButton></div>
          </div>
        </div>
      )}

      {/* edit role */}
      {roleForm && (
        <div className="overlay">
          <div className="mask" onClick={() => setRoleForm(false)} />
          <div className="imodal">
            <div className="ihead">
              <div className="iic"><UserCog size={19} color="var(--accent-text)" /></div>
              <div>
                <div className="it" data-testid="role-form-title">{roleMode === 'create' ? t('nrTitle') : t('erTitle') + selName}</div>
                <div className="is">{roleMode === 'create' ? t('nrSub') : t('erSub')}</div>
              </div>
              <X size={18} className="ix" onClick={() => setRoleForm(false)} />
            </div>
            <div className="ibody">
              <div className="er2">
                <div><div className="fl">{t('erName')}</div><input value={roleMode === 'create' ? edit.name : roleName(edit.name)} data-testid="role-form-name" onChange={(e) => patchEdit({ name: e.target.value })} /></div>
                <div><div className="fl">{t('erDefRole')}</div><input value={edit.defaultConnRole} onChange={(e) => patchEdit({ defaultConnRole: e.target.value })} /></div>
              </div>
              {roleMode === 'create' && <div><div className="fl">{t('nrLayer')}</div><input value={edit.layer} onChange={(e) => patchEdit({ layer: e.target.value })} placeholder={t('nrLayerPh')} maxLength={32} /></div>}
              <div><div className="fl">{t('erDesc')}</div><textarea value={edit.description} onChange={(e) => patchEdit({ description: e.target.value })} placeholder={t('erDescVal')} /></div>
              {roleMode === 'create' && <div className="nrnote">{t('nrNote')}</div>}
              <div className="ertoggle">
                <div><div className="rt">{t('erApprAllow')}</div><div className="rd">{t('erApprAllowSub')}</div></div>
                <VSwitch value={edit.canApprove} onChange={(canApprove) => patchEdit({ canApprove })} />
              </div>
            </div>
            <div className="ifoot"><VButton variant="secondary" onClick={() => setRoleForm(false)}>{t('cancel')}</VButton><VButton variant="primary" disabled={roleMode === 'create' && !edit.name.trim()} data-testid="role-form-save" onClick={saveRole}>{roleMode === 'create' ? t('nrCreate') : t('save')}</VButton></div>
          </div>
        </div>
      )}

      {detail && (
        <TagEditModal
          open={tagModalOpen} title={t('tagRoleTitle')} subtitle={detail.name}
          tags={tags} suggestions={allTags}
          onClose={() => setTagModalOpen(false)} onSave={saveRoleTags}
        />
      )}
    </div>
  )
}
