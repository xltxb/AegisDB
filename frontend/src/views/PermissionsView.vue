<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Crown, Shield, UserCog, Code, Eye, SquareTerminal, ClipboardCheck, Database,
  ShieldAlert, Layers, UsersRound, ScrollText, Settings, UserPlus, X, Search, Check, MailPlus, ShieldCheck, Tag, KeyRound, Rocket,
  CircleCheck, Lock, Minus, Plus, Clock, GitCompare, Stethoscope,
} from 'lucide-vue-next'
import QRCode from 'qrcode'
import VButton from '@/components/common/VButton.vue'
import VSwitch from '@/components/common/VSwitch.vue'
import VSelect from '@/components/common/VSelect.vue'
import TagEditModal from '@/components/modals/TagEditModal.vue'
import api from '@/api'
import { confirmAction } from '@/lib/confirm'
import { useUnsavedFlag, sameForm } from '@/lib/unsavedChanges'
import { BUILTIN_ROLE_NAME, BUILTIN_ROLE_LAYER, builtinLabel } from '@/lib/builtinNames'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import { useEnvTierStore } from '@/stores/envtier'
import QueryGrantsPanel from '@/components/perms/QueryGrantsPanel.vue'
import { relTime, absTime, useNow } from '@/lib/relTime'
import type { RoleBrief, RoleDetail, UserView } from '@/types'

const { t } = useI18n()
// 出厂角色的名字与层级按读者语言显示;管理员改过名的原样显示(lib/builtinNames)。
const roleName = (s: string) => builtinLabel(BUILTIN_ROLE_NAME, s, t)
const roleLayer = (s: string) => builtinLabel(BUILTIN_ROLE_LAYER, s, t)
const ui = useUIStore()
const auth = useAuthStore()
const envtier = useEnvTierStore()
// Role/permission + user-credential edits are platform-admin only (backend
// enforces this too); non-admins see the page read-only.
// Admin if ANY held role is admin (union), falling back to the primary role code.
const isAdmin = computed(() => auth.me?.roleCodes?.includes('admin') ?? (auth.me?.roleCode === 'admin'))
// 角色与用户都到了才报数:GET /users 比角色慢,先到的那一半配上一个 0 读起来像"没有用户"
// (黑盒 2026-10 工单 05)。没到之前副标题与页签提示都写"加载中"。
const rolesLoaded = ref(false)
const usersLoaded = ref(false)
function publishSub() {
  ui.pageSub = rolesLoaded.value && usersLoaded.value
    ? { key: 'subPerms', params: { r: roles.value.length, u: users.value.length } }
    : { key: 'pmLoading' }
}
const view = ref<'roles' | 'users' | 'grants'>('roles')
// 「最近活跃」现在是真实时间(RFC3339);老数据里是建号时写的"—"/"刚刚",认不出就原样显示
const nowTick = useNow()
const lastActiveText = (s: string) =>
  /^\d{4}-\d\d-\d\dT/.test(s || '') ? relTime((k, n) => t(k, n || {}), s, nowTick.value) : (s || '—')
const roles = ref<RoleBrief[]>([])
const detail = ref<RoleDetail | null>(null)
const users = ref<UserView[]>([])
const memberForm = ref(false)
const inviteForm = ref(false)
const roleForm = ref(false)
// 同一个弹窗两种用法:新建(create)与编辑当前选中的角色(edit)。从前「新建角色」打开的也是编辑表单,
// 保存会改掉当前选中的那个角色(2026-10-22)。
const roleMode = ref<'create' | 'edit'>('edit')
const edit = ref({ name: '', layer: '', description: '', defaultConnRole: 'dba_l2', canApprove: true })
const inviteEmail = ref('')
const inviteRoleName = ref('')
const memberSearch = ref('')

// 角色搜索。角色是一次性全量拉下来的(api.roles 不分页),所以在客户端筛是对的 ——
// 和审批页那个必须走服务端的搜索框不是一回事。
const roleQ = ref('')
const filteredRoles = computed(() => {
  const kw = roleQ.value.trim().toLowerCase()
  if (!kw) return roles.value
  return roles.value.filter((r) => r.name.toLowerCase().includes(kw) || roleName(r.name).toLowerCase().includes(kw) || r.code.toLowerCase().includes(kw))
})
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

// 成员的邮箱来自本页已经加载的用户列表(onMounted 里 loadUsers 已经跑过),按 id
// 关联即可 —— 不为此再发一次请求。
//
// **加入时间没有**:tbl_role_member 只有 role_id + user_id 两列,库里根本没记
// 这个时刻。宁可不显示,也不摆一个看着像真的的假时间。
const memberEmail = (id: number) => users.value.find((u) => u.id === id)?.email || ''

const filteredUsers = computed(() => {
  const q = memberSearch.value.trim().toLowerCase()
  if (!q) return users.value
  return users.value.filter(
    (u) => u.name.toLowerCase().includes(q) || (u.dept || '').toLowerCase().includes(q) || u.email.toLowerCase().includes(q),
  )
})

function openRoleForm() {
  if (!detail.value) return
  roleMode.value = 'edit'
  // 回显角色的真实值。从前描述清空、「可审批」按 code 猜(只有 admin / owner 是开),保存时会把
  // 一个本来可审批的自定义角色悄悄改成不可审批。
  const d = detail.value
  edit.value = {
    name: d.name, layer: d.layer, description: d.description || '',
    defaultConnRole: d.defaultConnRole || (d.code === 'ro' ? 'readonly' : 'dba_l2'),
    canApprove: d.canApprove ?? (d.code === 'admin' || d.code === 'owner'),
  }
  roleSnap.value = { ...edit.value }
  roleForm.value = true
}
const roleSnap = ref<unknown>(null)
// 删除自定义角色。有成员、被发布流程引用时服务端会拒,原因原样提示(要人先移走成员 / 换掉流程里的角色)
async function deleteRole() {
  const d = detail.value
  if (!d || !d.deletable) return
  if (!(await confirmAction(t('drConfirm', { name: roleName(d.name) })))) return
  try {
    await api.deleteRole(d.id)
    ui.notify(t('drDone', { name: roleName(d.name) }), 'success')
    detail.value = null
    await loadRoles() // 列表为空的选中项会落到第一个角色上
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  }
}
// 新建角色:什么权限都不带,建好后选中它,在右边的矩阵 / 菜单里逐项打开
function openNewRole() {
  roleMode.value = 'create'
  edit.value = { name: '', layer: '', description: '', defaultConnRole: 'readonly', canApprove: false }
  roleSnap.value = { ...edit.value }
  roleForm.value = true
}
async function saveRole() {
  // M14: 保存角色失败以 toast 呈现
  try {
    if (roleMode.value === 'create') {
      if (!edit.value.name.trim()) return
      const created = await api.createRole({
        name: edit.value.name.trim(), layer: edit.value.layer.trim(), description: edit.value.description.trim(),
        defaultConnRole: edit.value.defaultConnRole.trim(), canApprove: edit.value.canApprove,
      })
      await loadRoles()
      detail.value = created
      roleForm.value = false
      ui.notify(t('nrCreated', { name: created.name }), 'success')
      return
    }
    if (!detail.value) return
    // 层级说明只在新建时填,编辑接口不收它
    const body = { name: edit.value.name, description: edit.value.description, defaultConnRole: edit.value.defaultConnRole, canApprove: edit.value.canApprove }
    detail.value = await api.updateRole(detail.value.id, body)
    await loadRoles()
    roleForm.value = false
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  }
}
function openInvite() {
  inviteEmail.value = ''
  inviteRoleName.value = roleName(roles.value.find((r) => r.code === 'ro')?.name || roles.value[0]?.name || '')
  inviteForm.value = true
}
async function doInvite() {
  const roleId = roles.value.find((r) => roleName(r.name) === inviteRoleName.value)?.id
  if (!inviteEmail.value.trim() || !roleId) return
  // M14: 邀请失败以 toast 呈现
  try {
    await api.invite(inviteEmail.value.trim(), roleId)
    inviteForm.value = false
    await loadUsers()
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  }
}

const roleIcon: Record<string, any> = { crown: Crown, shield: Shield, 'user-cog': UserCog, code: Code, eye: Eye }
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
// One column per control tier, in the tier list's own order. This was the four
// built-in strings; the matrix is keyed by tier and tiers are rows now, so a
// hardcoded list would silently omit any tier added later — and an omitted
// column is an ungoverned cell, since a capability with no row reads as allow.
const envCols = computed(() => envtier.tierCodes)
// The grid was four fixed columns. Each tier now needs a minimum width so a long
// list scrolls rather than squeezing every cell into an unreadable sliver.
const matrixCols = computed(() => ({
  gridTemplateColumns: `minmax(150px, 2fr) repeat(${envCols.value.length}, minmax(88px, 1fr))`,
}))
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
const sym: Record<string, { icon: any; cls: string; tip: string }> = {
  allow: { icon: CircleCheck, cls: 'allow', tip: 'capTipAllow' },
  approve: { icon: Lock, cls: 'approve', tip: 'capTipApprove' },
  deny: { icon: Minus, cls: 'deny', tip: 'capTipDeny' },
}
const cycleOrder = ['allow', 'approve', 'deny']

async function loadRoles() {
  // M14: 加载失败以 toast 呈现，避免静默失败
  try {
    roles.value = await api.roles()
    rolesLoaded.value = true
    if (roles.value.length && !detail.value) await selectRole(roles.value[0].id)
    publishSub()
  } catch (e) {
    ui.notifyError(e, t('loadFailed'))
  }
}
async function selectRole(id: number) {
  // M14: 加载失败以 toast 呈现
  try { detail.value = await api.role(id) } catch (e) { ui.notifyError(e, t('loadFailed')) }
}

// ---- DB access tags (assign databases to this group by tag) ----
const allTags = ref<string[]>([])
const tagModalOpen = ref(false)
async function saveRoleTags(tags: string[]) {
  if (!detail.value) return
  // M14: 保存标签失败以 toast 呈现
  try {
    detail.value = await api.setRoleTags(detail.value.id, tags)
    tagModalOpen.value = false
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  }
}
// ---------------------------------------------------------------- SSO 绑定
// 绑定发生在这个人第一次经 SRD 授权登录、邮箱对上的时候(服务端完成)。这里只展示,并让
// 管理员解绑:解绑后恢复口令登录,下次再经 SRD 登录会重新绑定。
const ssoEnabled = ref(false)

async function unbindSso(u: UserView) {
  if (!isAdmin.value || !u.ssoBound || !(await confirmAction(t('ssoUnbindConfirm', { name: u.name })))) return
  try { await api.ssoUnbind(u.id); ui.notify(t('ssoUnbindDone'), 'success'); await loadUsers() } catch (e) { ui.notifyError(e, t('actionFailed')) }
}

async function loadUsers() {
  // M14: 加载失败以 toast 呈现
  try { users.value = await api.users(); usersLoaded.value = true; publishSub() } catch (e) { ui.notifyError(e, t('loadFailed')) }
}

onMounted(async () => {
  publishSub()
  // Tiers decide the matrix columns, so load them before anything renders cells.
  await envtier.load().catch(() => {})
  await Promise.all([loadRoles(), loadUsers()])
  try { ssoEnabled.value = (await api.ssoConfig()).enabled } catch { ssoEnabled.value = false }
  try { allTags.value = await api.tags() } catch { /* ignore */ }
})

function cellLevel(cap: string, env: string): string {
  return detail.value?.matrix?.[cap]?.[env] || 'allow'
}
async function cycleCell(cap: string, env: string) {
  if (!detail.value || !isAdmin.value) return
  const cur = cellLevel(cap, env)
  const next = cycleOrder[(cycleOrder.indexOf(cur) + 1) % 3]
  if (!detail.value.matrix[cap]) detail.value.matrix[cap] = {}
  detail.value.matrix[cap][env] = next
  // M14: 保存能力矩阵失败以 toast 呈现
  try {
    await api.setCapabilities(detail.value.id, detail.value.matrix)
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  }
}
// 管理类菜单只给平台管理员角色(管理后台拆分,工单 03;后端 model.AdminOnlyMenus 同一份清单)。
// 别的角色上这几个开关灰掉 —— 勾上去后端也会拒绝;存量的勾选在网关启动时已收回。
const ADMIN_ONLY_MENUS = ['db', 'rules', 'envtier', 'perms', 'settings']
const menuLocked = (key: string) => !!detail.value && detail.value.code !== 'admin' && ADMIN_ONLY_MENUS.includes(key)
async function toggleMenu(key: string) {
  if (!detail.value || !isAdmin.value || menuLocked(key)) return
  detail.value.menus[key] = !detail.value.menus[key]
  // M14: 保存菜单权限失败以 toast 呈现
  try {
    await api.setMenus(detail.value.id, detail.value.menus)
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  }
}
async function removeMember(userId: number) {
  if (!detail.value) return
  // M15: 移除角色成员前二次确认
  const mb = detail.value.members.find((m) => m.id === userId)
  if (!(await confirmAction(t('pmRemoveMemberConfirm', { role: roleName(detail.value.name), name: mb?.name || userId })))) return
  // M14: 移除失败以 toast 呈现
  try {
    detail.value = await api.removeMember(detail.value.id, userId)
    await loadRoles()
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  }
}
async function addMember(userId: number) {
  if (!detail.value) return
  // M14: 添加成员失败以 toast 呈现
  try {
    detail.value = await api.addMember(detail.value.id, userId)
    await loadRoles()
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  }
}
async function toggleUser(u: UserView) {
  if (!isAdmin.value) return
  // 停不了的先说原因,别让人白确认一次 —— "确认停用林伟?"→点确定→"不能停用自己"
  // 是很难受的一段。canDisable 由服务端算(自己/最后一位管理员),前端只照着显示。
  if (u.status !== 'disabled' && u.canDisable === false) {
    ui.notify(u.disableBlock || t('actionFailed'), 'error', 5000)
    return
  }
  // M15: 禁用用户前二次确认（启用无需确认）
  if (u.status !== 'disabled' && !(await confirmAction(t('pmDisableUserConfirm', { name: u.name })))) return
  // M14: 启停失败以 toast 呈现
  try {
    await api.patchUser(u.id, { status: u.status === 'disabled' ? 'active' : 'disabled' })
    await loadUsers()
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  }
}

// ---- admin user management: password reset + OTP binding ----
// ---- per-user data-access scope ----
//
// Tags are otherwise granted to ROLES, where "no tags" means unrestricted. That
// cannot express "this particular person", so a grant made here is the more
// specific statement and REPLACES the role scope; clearing it falls back to the
// role. See backend model.UserTag.
const userTags = ref<string[]>([])
const userTagsOpen = ref(false)
const tagMsg = ref('')

async function loadUserTags(id: number) {
  try { userTags.value = await api.userTags(id) } catch { userTags.value = [] }
}
async function saveUserTags(tags: string[]) {
  if (!userModal.value) return
  try {
    await api.setUserTags(userModal.value.id, tags)
    userTags.value = tags
    userTagsOpen.value = false
    tagMsg.value = tags.length ? t('utScoped', { n: tags.length }) : t('utRoleDefault')
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  }
}

const userModal = ref<UserView | null>(null)
const newPw = ref('')
const pwMsg = ref('')
const otpMsg = ref('')
const otpBind = ref<{ secret: string; otpauthUri: string } | null>(null)
const otpQr = ref('')

function openUser(u: UserView) {
  if (!isAdmin.value) return
  userModal.value = u
  newPw.value = ''; pwMsg.value = ''; otpMsg.value = ''; otpBind.value = null; otpQr.value = ''
  roleSel.value = [...(u.roleIds || [])]; roleMsg.value = ''
  tagMsg.value = ''; userTags.value = []
  loadUserTags(u.id)
}

// ---- per-user role assignment (a user may hold several roles; permissions
// compose as the union across them). The first selected role is the primary. ----
const roleSel = ref<number[]>([])
const roleMsg = ref('')
function toggleUserRole(id: number) {
  const i = roleSel.value.indexOf(id)
  if (i >= 0) roleSel.value.splice(i, 1)
  else roleSel.value.push(id)
}
async function saveRoles() {
  if (!userModal.value || !roleSel.value.length) { roleMsg.value = t('urNeedOne'); return }
  try {
    await api.setUserRoles(userModal.value.id, roleSel.value)
    roleMsg.value = t('urSaved')
    await loadUsers(); syncModal()
    if (userModal.value) roleSel.value = [...(userModal.value.roleIds || [])]
  } catch { roleMsg.value = t('urFailed') }
}

// ---- admin: create an account directly (initial password + roles), an
// alternative to invite-only onboarding. The account is active immediately. ----
const createForm = ref(false)
const createEmail = ref('')
const createName = ref('')
const createPw = ref('')
const createRoleIds = ref<number[]>([])
const createMsg = ref('')
function openCreate() {
  createEmail.value = ''; createName.value = ''; createPw.value = ''; createMsg.value = ''
  const def = roles.value.find((r) => r.code === 'ro') || roles.value[0]
  createRoleIds.value = def ? [def.id] : []
  createForm.value = true
}
function toggleCreateRole(id: number) {
  const i = createRoleIds.value.indexOf(id)
  if (i >= 0) createRoleIds.value.splice(i, 1)
  else createRoleIds.value.push(id)
}
async function doCreate() {
  if (!createEmail.value.trim() || createPw.value.length < 8 || !createRoleIds.value.length) {
    createMsg.value = t('cuInvalid'); return
  }
  try {
    await api.createUser({
      email: createEmail.value.trim(), name: createName.value.trim(),
      password: createPw.value, roleIds: createRoleIds.value,
    })
    createForm.value = false
    await loadUsers()
  } catch (e) {
    // 服务端会说清是哪一项不对(邮箱格式、重复、角色、长度……),写在表单里,表单不关,改了再提交
    createMsg.value = (e as any)?.message || t('cuFailed')
  }
}
// 有没保存的改动(管理后台空闲锁屏,2026-10-26 回归排查 10):角色表单改过、建号 / 邀请表单填了东西、
// 用户弹窗里填了新密码或改了角色勾选。能力矩阵与菜单开关是即点即存的,不算。
const sortedIds = (xs: number[]) => [...xs].sort((a, b) => a - b)
useUnsavedFlag(() => (roleForm.value && !sameForm(edit.value, roleSnap.value))
  || (createForm.value && !!(createEmail.value || createName.value || createPw.value))
  || (inviteForm.value && !!inviteEmail.value.trim())
  || (!!userModal.value && (!!newPw.value || !sameForm(sortedIds(roleSel.value), sortedIds(userModal.value.roleIds || [])))))
function syncModal() {
  if (userModal.value) userModal.value = users.value.find((x) => x.id === userModal.value!.id) || userModal.value
}
async function savePw() {
  if (!userModal.value) return
  if (newPw.value.length < 8) { pwMsg.value = t('pwTooShort'); return }
  try { await api.setUserPassword(userModal.value.id, newPw.value); pwMsg.value = t('pwSaved'); newPw.value = '' }
  catch (e) { pwMsg.value = (e as any)?.message || t('pwFailed') }
}
async function bindOtp() {
  if (!userModal.value) return
  try {
    otpBind.value = await api.bindUserMfa(userModal.value.id)
    otpQr.value = await QRCode.toDataURL(otpBind.value.otpauthUri, { margin: 1, width: 160 })
    otpMsg.value = ''
    await loadUsers(); syncModal()
  } catch { otpMsg.value = t('otpFailed') }
}
async function resetOtp() {
  if (!userModal.value) return
  // M15: 解绑他人 MFA 前二次确认
  if (!(await confirmAction(t('pmResetMfaConfirm', { name: userModal.value.name })))) return
  try { await api.resetUserMfa(userModal.value.id); otpBind.value = null; otpQr.value = ''; otpMsg.value = t('otpUnbound'); await loadUsers(); syncModal() }
  catch { otpMsg.value = t('otpFailed') }
}

const selName = computed(() => roleName(detail.value?.name || ''))
const memberIds = computed(() => new Set(detail.value?.memberIds || []))
</script>

<template>
  <div class="wrap">
    <!-- 这一页是全出血的分栏布局,.wrap 自己没有内边距 —— 页头得自带,不然会贴到左缘。 -->
    <div class="v-head pm-head">
      <div class="v-head-l">
        <div class="v-eyebrow">ROLES &amp; PERMISSIONS</div>
      </div>
    </div>
    <div class="vtabs">
      <div class="vt" :class="{ active: view === 'roles' }" @click="view = 'roles'">{{ $t('tabRoleView') }}</div>
      <div class="vt" :class="{ active: view === 'users' }" @click="view = 'users'">{{ $t('tabUserView') }}</div>
      <div class="vt" :class="{ active: view === 'grants' }" data-testid="tab-grants" @click="view = 'grants'">{{ $t('tabGrantView') }}</div>
      <!-- 在线用户移到了管理总览(管理后台拆分,工单 06) -->
      <div class="vhint" data-testid="perms-vhint">{{ view === 'roles' ? (rolesLoaded ? $t('pmRolesHint', { n: roles.length }) : $t('pmLoading')) : view === 'users' ? (usersLoaded ? $t('pmUsersHint', { n: users.length }) : $t('pmLoading')) : $t('pmGrantsHint') }}</div>
    </div>

    <!-- ROLE VIEW -->
    <div v-if="view === 'grants'" class="scy userpage"><QueryGrantsPanel :is-admin="isAdmin" /></div>
    <div v-else-if="view === 'roles'" class="rolegrid">
      <!-- 左:角色导航 -->
      <div class="rolepanel">
        <div class="rphead">
          <div class="eyebrow">{{ $t('rolesTier') }}</div>
          <VButton v-if="isAdmin" variant="primary" height="30px" data-testid="pm-new-role" @click="openNewRole"><Plus :size="14" />{{ $t('pmNewRole') }}</VButton>
        </div>
        <div class="rsearch">
          <Search :size="14" color="var(--text-faint)" />
          <input v-model="roleQ" :placeholder="$t('pmRoleSearchPh')" spellcheck="false">
          <button v-if="roleQ" class="sclear" :title="$t('apSearchClear')" @click="roleQ = ''"><X :size="13" /></button>
        </div>
        <div class="scy rl">
          <div v-for="r in filteredRoles" :key="r.id" class="ritem" :class="{ active: detail?.id === r.id }" @click="selectRole(r.id)">
            <div class="rname">
              <component :is="roleIcon[r.icon] || Shield" :size="15" />
              <span class="rn">{{ roleName(r.name) }}</span>
              <span v-if="isBuiltin(r.code)" class="builtin">{{ $t('pmBuiltin') }}</span>
            </div>
            <div class="rlayer">{{ roleLayer(r.layer) }}<span class="sep">·</span>{{ $t('pmMembersN', { n: r.count }) }}</div>
          </div>
          <div v-if="!filteredRoles.length" class="rempty">{{ $t('pmNoRole') }}</div>
        </div>
      </div>

      <!-- 右:权限详情。外层负责滚动,内层卡片限宽 —— 宽屏上一张横向铺满的表格,
           眼睛要从最左的动作名一路扫到最右的开关,中间全是空白。 -->
      <div v-if="detail" class="scy detailwrap">
        <div class="detail">
          <div class="mhead">
            <div class="grow">
              <div class="mtitle">{{ selName }}<span class="mtsub">{{ $t('matrixTitleSuffix') }}</span></div>
              <div class="mhint">{{ roleLayer(detail.layer) }}<span class="sep">·</span>{{ $t('pmMembersN', { n: detail.members.length }) }}<span class="sep">·</span>{{ $t('matrixHint') }}</div>
            </div>
            <span v-if="!isAdmin" class="roflag">{{ $t('readOnlyPerms') }}</span>
            <VButton v-if="isAdmin" variant="secondary" height="34px" @click="openRoleForm">{{ $t('editRole') }}</VButton>
            <VButton v-if="isAdmin && detail.deletable" variant="danger" height="34px" data-testid="pm-delete-role" @click="deleteRole">{{ $t('drBtn') }}</VButton>
          </div>

          <!-- 模块一:操作权限矩阵 -->
          <div class="sec">
            <div class="sechead"><span class="eyebrow2">{{ $t('matrixSection') }}</span></div>
            <!-- Scrolls inside its own box once the tier count outgrows the width;
                 the page body must never scroll sideways. -->
            <div class="mtable scx">
              <!-- The template goes on each ROW: they are the grids. Binding it to the
                   wrapper instead left the rows with no columns at all, so every cell
                   stacked vertically in one implicit column. -->
              <div class="mgrid">
                <div class="mth" :style="matrixCols">
                  <span>{{ $t('colCap') }}</span>
                  <!-- 环境列头带分层色。颜色来自 envtier.dotFor —— 和树、审批列表、
                       分层页是同一个来源,不在这一页另配一份。 -->
                  <span v-for="env in envCols" :key="env" class="ctr">
                    <span class="envhd" :class="envtier.dotFor(env)">{{ env.toUpperCase() }}</span>
                  </span>
                </div>
                <div v-for="(cap, i) in capKeys" :key="cap" class="mtr" :style="matrixCols">
                  <span class="cap">{{ $t(capLabels[i] as any) }}</span>
                  <div v-for="env in envCols" :key="env" class="cellwrap">
                    <button
                      class="cell" :class="[sym[cellLevel(cap, env)].cls, { ro: !isAdmin }]"
                      :title="$t(sym[cellLevel(cap, env)].tip)" @click="cycleCell(cap, env)"
                    >
                      <component :is="sym[cellLevel(cap, env)].icon" :size="14" />
                    </button>
                  </div>
                </div>
              </div>
            </div>
            <div class="legend">
              <span class="lg"><span class="cell allow sm"><CircleCheck :size="12" /></span>{{ $t('capTipAllow') }}</span>
              <span class="lg"><span class="cell approve sm"><Lock :size="12" /></span>{{ $t('capTipApprove') }}</span>
              <span class="lg"><span class="cell deny sm"><Minus :size="12" /></span>{{ $t('capTipDeny') }}</span>
            </div>
          </div>

          <!-- 模块二:功能菜单。网格卡片,开关紧跟在名字后面 —— 原先是一行一项,
               名字在最左、开关在屏幕最右,一千多像素的空白把两者拉断了。 -->
          <div class="sec">
            <div class="sechead"><span class="eyebrow2">{{ $t('menuTitle') }}</span><span class="secsub">{{ $t('menuSub') }}</span></div>
            <div class="menugrid">
              <label v-for="m in menuDefs" :key="m.key" class="menucard" :class="{ on: !!detail.menus[m.key], locked: menuLocked(m.key) }"
                :title="menuLocked(m.key) ? $t('pmAdminOnlyMenu') : undefined" :data-testid="'menu-' + m.key">
                <component :is="m.icon" :size="16" class="mi" />
                <span class="ml">{{ $t(m.label as any) }}</span>
                <span v-if="menuLocked(m.key)" class="mlock">{{ $t('pmAdminOnlyTag') }}</span>
                <VSwitch :model-value="!!detail.menus[m.key]" :disabled="!isAdmin || menuLocked(m.key)" @update:model-value="toggleMenu(m.key)" />
              </label>
            </div>
          </div>

          <!-- 模块三:授权范围 + 成员,分两块 -->
          <div class="sec">
            <div class="sechead"><span class="eyebrow2">{{ $t('tagAccessTitle') }}</span><span class="secsub">{{ detail.tags.length ? $t('tagAccessSub') : detail.noInstances ? $t('tagAccessNone') : $t('tagAccessAll') }}</span></div>
            <div class="rtags">
              <span v-for="tg in detail.tags" :key="tg" class="rtag">{{ tg }}</span>
              <span v-if="!detail.tags.length" class="rtagall">{{ detail.noInstances ? $t('tagNoneBadge') : $t('tagAllBadge') }}</span>
              <button v-if="isAdmin" class="rtedit" @click="tagModalOpen = true"><Tag :size="12" />{{ $t('tagAssign') }}</button>
            </div>
          </div>

          <div class="sec last">
            <div class="sechead">
              <span class="eyebrow2">{{ $t('membersTitle') }}</span>
              <span class="secsub">{{ $t('pmMembersN', { n: detail.members.length }) }}</span>
              <VButton v-if="isAdmin" class="secbtn" variant="secondary" height="30px" @click="memberForm = true">
                <UserPlus :size="14" />{{ $t('addMember') }}
              </VButton>
            </div>
            <div class="memgrid">
              <!-- 邮箱来自本页已加载的用户列表。**加入时间没有** —— tbl_role_member
                   只有 role_id + user_id,库里就没记这个时刻,不摆一个假的。 -->
              <div v-for="(mb, i) in detail.members" :key="mb.id" class="memcard">
                <div class="mava" :class="{ first: i === 0 }">{{ mb.initials }}</div>
                <div class="mbody">
                  <div class="mn">{{ mb.name }}</div>
                  <div class="me">{{ memberEmail(mb.id) || mb.dept || '—' }}</div>
                </div>
                <button v-if="isAdmin" class="mx" :title="$t('pmRemoveMember')" @click="removeMember(mb.id)"><X :size="13" /></button>
              </div>
              <div v-if="!detail.members.length" class="memempty">{{ $t('pmNoMember') }}</div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- USER VIEW -->
    <div v-else class="scy userpage">
      <div class="uhead">
        <div>
          <div class="utitle">{{ $t('usersTitle') }} · {{ users.length }} {{ $t('people') }}</div>
          <div class="usub">{{ $t('usersSub') }}</div>
        </div>
        <div v-if="isAdmin" class="uacts">
          <VButton variant="secondary" @click="openInvite">{{ $t('invite') }}</VButton>
          <VButton variant="primary" @click="openCreate"><UserPlus :size="15" />{{ $t('addAccount') }}</VButton>
        </div>
      </div>
      <div class="utable">
        <div class="uth"><span>{{ $t('colUser') }}</span><span>{{ $t('colAcct') }}</span><span>{{ $t('colRoles') }}</span><span>{{ $t('colActive') }}</span><span class="r">{{ $t('colStatus') }}</span></div>
        <div v-for="(u, i) in users" :key="u.id" class="utr" :style="{ opacity: u.status === 'disabled' ? 0.5 : 1 }">
          <div class="ucell"><div class="uava" :class="{ first: i === 0 }">{{ u.initials }}</div><div><div class="un">{{ u.name }}<span v-if="u.kind === 'service'" class="svcbadge">{{ $t('saMark') }}</span></div><div class="ud">{{ u.dept }}</div></div></div>
          <span class="mono mute">{{ u.email }}</span>
          <div class="uroles">
            <span v-for="rn in u.roles" :key="rn" class="urole">{{ roleName(rn) }}</span>
            <span v-if="!u.roles.length" class="mono mute">{{ $t('unassigned') }}</span>
          </div>
          <span class="mono mute" :title="absTime(u.lastActive)">{{ lastActiveText(u.lastActive) }}</span>
          <div class="ustatus">
            <span class="otptag" :class="{ on: u.mfaEnabled }" :title="u.mfaEnabled ? $t('otpBound') : $t('otpUnboundState')"><ShieldCheck :size="12" />OTP</span>
            <span v-if="ssoEnabled && u.kind !== 'service'" class="otptag ssotag" :class="{ on: u.ssoBound, act: isAdmin && u.ssoBound }"
              :title="u.ssoBound ? $t('ssoBoundTip', { sso: u.ssoUserName || u.email }) : $t('ssoUnboundTip')"
              :data-testid="'sso-tag-' + u.id" @click="unbindSso(u)">SSO</span>
            <div class="stbadge" :style="{ background: u.status !== 'disabled' ? 'var(--success-subtle)' : 'var(--surface-sunken)', color: u.status !== 'disabled' ? 'var(--success-text)' : 'var(--text-muted)', cursor: isAdmin ? 'pointer' : 'default' }" @click="toggleUser(u)">
              <span class="dotc" />{{ u.status !== 'disabled' ? $t('enabled') : $t('disabled') }}
            </div>
            <button v-if="isAdmin" class="manage" :title="$t('manage')" @click="openUser(u)"><UserCog :size="15" /></button>
          </div>
        </div>
      </div>
      <div class="ufoot">{{ $t('userFootHint') }}</div>
    </div>

    <!-- member picker -->
    <div v-if="memberForm" class="overlay">
      <div class="mask" @click="memberForm = false" />
      <div class="pmodal">
        <div class="phead"><div class="pic"><UserPlus :size="19" color="var(--accent-text)" /></div><div><div class="pt">{{ $t('mpTitle') }}{{ selName }}</div><div class="ps">{{ $t('mpSub') }}</div></div><X :size="18" class="px" @click="memberForm = false" /></div>
        <div class="psearch"><Search :size="14" color="var(--text-faint)" /><input v-model="memberSearch" :placeholder="$t('mpSearch')" /></div>
        <div class="scy plist">
          <div v-for="u in filteredUsers" :key="u.id" class="prow" @click="!memberIds.has(u.id) && addMember(u.id)">
            <div class="pava">{{ u.initials }}</div>
            <div class="pgrow"><div class="pn">{{ u.name }}</div><div class="pd">{{ u.dept }}</div></div>
            <span v-if="memberIds.has(u.id)" class="joined"><Check :size="14" />{{ $t('mpJoined') }}</span>
            <span v-else class="addbtn"><span class="ic">+</span>{{ $t('mpAdd') }}</span>
          </div>
        </div>
        <div class="pfoot"><div class="pfh">{{ $t('mpFoot') }}</div><VButton variant="primary" height="38px" @click="memberForm = false">{{ $t('done') }}</VButton></div>
      </div>
    </div>

    <TagEditModal
      :open="userTagsOpen" :title="$t('utTitle')" :subtitle="userModal ? userModal.email : ''"
      :tags="userTags" :suggestions="allTags"
      @close="userTagsOpen = false" @save="saveUserTags"
    />

    <!-- user management: password + OTP -->
    <div v-if="userModal" class="overlay">
      <div class="mask" @click="userModal = null" />
      <div class="pmodal">
        <div class="phead">
          <div class="pic"><UserCog :size="19" color="var(--accent-text)" /></div>
          <div><div class="pt">{{ userModal.name }}</div><div class="ps mono">{{ userModal.email }}</div></div>
          <X :size="18" class="px" @click="userModal = null" />
        </div>
        <div class="usec">
          <div class="uslbl"><KeyRound :size="14" />{{ $t('resetPw') }}</div>
          <div class="pwrow">
            <input v-model="newPw" type="password" :placeholder="$t('newPwPh')" @keyup.enter="savePw" />
            <VButton variant="secondary" height="40px" @click="savePw">{{ $t('save') }}</VButton>
          </div>
          <div v-if="pwMsg" class="umsg">{{ pwMsg }}</div>
        </div>
        <div class="usec">
          <div class="uslbl"><Tag :size="14" />{{ $t('utTitle') }}</div>
          <div class="uthint">{{ $t('utHint') }}</div>
          <div class="utrow">
            <template v-if="userTags.length">
              <span v-for="tg in userTags" :key="tg" class="uttag">{{ tg }}</span>
            </template>
            <span v-else class="utnone">{{ $t('utRoleDefault') }}</span>
            <VButton variant="secondary" height="34px" @click="userTagsOpen = true">{{ $t('utEdit') }}</VButton>
          </div>
          <div v-if="tagMsg" class="umsg">{{ tagMsg }}</div>
        </div>
        <div class="usec">
          <div class="uslbl"><UsersRound :size="14" />{{ $t('urTitle') }}
            <span class="urhint">{{ $t('urHint') }}</span>
          </div>
          <div class="rolepick">
            <button v-for="r in roles" :key="r.id" type="button" class="rchip" :class="{ on: roleSel.includes(r.id) }" @click="toggleUserRole(r.id)">
              <Check v-if="roleSel.includes(r.id)" :size="13" /><component v-else :is="roleIcon[r.icon] || Shield" :size="13" />{{ roleName(r.name) }}
            </button>
          </div>
          <div class="urrow">
            <VButton variant="secondary" height="38px" :disabled="!roleSel.length" @click="saveRoles">{{ $t('urSave') }}</VButton>
            <span v-if="roleMsg" class="umsg inline">{{ roleMsg }}</span>
          </div>
        </div>
        <div class="usec">
          <div class="uslbl"><ShieldCheck :size="14" />{{ $t('otpBinding') }}
            <span class="otpstate" :class="{ on: userModal.mfaEnabled }">{{ userModal.mfaEnabled ? $t('otpBound') : $t('otpUnboundState') }}</span>
          </div>
          <div class="otprow">
            <VButton variant="secondary" height="38px" @click="bindOtp">{{ userModal.mfaEnabled ? $t('otpRebind') : $t('otpBind') }}</VButton>
            <VButton v-if="userModal.mfaEnabled" variant="danger" height="38px" @click="resetOtp">{{ $t('otpReset') }}</VButton>
          </div>
          <div v-if="otpBind" class="otpresult">
            <div class="qrbox"><img v-if="otpQr" :src="otpQr" alt="OTP QR" /></div>
            <div class="otpinfo">
              <div class="oih">{{ $t('otpScan') }}</div>
              <div class="oisecret">{{ otpBind.secret }}</div>
            </div>
          </div>
          <div v-if="otpMsg" class="umsg">{{ otpMsg }}</div>
        </div>
      </div>
    </div>

    <!-- invite -->
    <div v-if="inviteForm" class="overlay">
      <div class="mask" @click="inviteForm = false" />
      <div class="imodal">
        <div class="ihead"><div class="iic"><MailPlus :size="19" color="var(--accent-text)" /></div><div><div class="it">{{ $t('ivTitle') }}</div><div class="is">{{ $t('ivSub') }}</div></div><X :size="18" class="ix" @click="inviteForm = false" /></div>
        <div class="ibody">
          <div><div class="fl">{{ $t('ivEmail') }}</div><input v-model="inviteEmail" placeholder="name@aegisdb.io" type="email" /></div>
          <div><div class="fl">{{ $t('ivRole') }}</div><VSelect v-model="inviteRoleName" :options="roles.map((r) => roleName(r.name))" /></div>
          <div class="inote"><ShieldCheck :size="15" color="#2dcde6" /><div>{{ $t('ivNote') }}</div></div>
        </div>
        <div class="ifoot"><VButton variant="secondary" @click="inviteForm = false">{{ $t('cancel') }}</VButton><VButton variant="primary" :disabled="!inviteEmail.trim()" @click="doInvite">{{ $t('ivSend') }}</VButton></div>
      </div>
    </div>

    <!-- create account (admin: initial password + one or more roles) -->
    <div v-if="createForm" class="overlay">
      <div class="mask" @click="createForm = false" />
      <div class="imodal">
        <div class="ihead"><div class="iic"><UserPlus :size="19" color="var(--accent-text)" /></div><div><div class="it">{{ $t('cuTitle') }}</div><div class="is">{{ $t('cuSub') }}</div></div><X :size="18" class="ix" @click="createForm = false" /></div>
        <div class="ibody">
          <div><div class="fl">{{ $t('ivEmail') }}</div><input v-model="createEmail" placeholder="name@aegisdb.io" type="email" /></div>
          <div><div class="fl">{{ $t('cuName') }}</div><input v-model="createName" :placeholder="$t('cuNamePh')" type="text" /></div>
          <div><div class="fl">{{ $t('cuPw') }}</div><input v-model="createPw" :placeholder="$t('cuPwPh')" type="password" /></div>
          <div>
            <div class="fl">{{ $t('cuRoles') }}</div>
            <div class="rolepick">
              <button v-for="r in roles" :key="r.id" type="button" class="rchip" :class="{ on: createRoleIds.includes(r.id) }" @click="toggleCreateRole(r.id)">
                <Check v-if="createRoleIds.includes(r.id)" :size="13" /><component v-else :is="roleIcon[r.icon] || Shield" :size="13" />{{ roleName(r.name) }}
              </button>
            </div>
          </div>
          <div class="inote"><ShieldCheck :size="15" color="#2dcde6" /><div>{{ $t('cuNote') }}</div></div>
          <div v-if="createMsg" class="umsg">{{ createMsg }}</div>
        </div>
        <div class="ifoot"><VButton variant="secondary" @click="createForm = false">{{ $t('cancel') }}</VButton><VButton variant="primary" :disabled="!createEmail.trim() || createPw.length < 8 || !createRoleIds.length" @click="doCreate">{{ $t('cuCreate') }}</VButton></div>
      </div>
    </div>

    <!-- edit role -->
    <div v-if="roleForm" class="overlay">
      <div class="mask" @click="roleForm = false" />
      <div class="imodal">
        <div class="ihead"><div class="iic"><UserCog :size="19" color="var(--accent-text)" /></div><div>
          <div class="it" data-testid="role-form-title">{{ roleMode === 'create' ? $t('nrTitle') : $t('erTitle') + selName }}</div>
          <div class="is">{{ roleMode === 'create' ? $t('nrSub') : $t('erSub') }}</div></div><X :size="18" class="ix" @click="roleForm = false" /></div>
        <div class="ibody">
          <div class="er2">
            <div><div class="fl">{{ $t('erName') }}</div><input :value="roleMode === 'create' ? edit.name : roleName(edit.name)" data-testid="role-form-name" @input="edit.name = ($event.target as HTMLInputElement).value" /></div>
            <div><div class="fl">{{ $t('erDefRole') }}</div><input v-model="edit.defaultConnRole" /></div>
          </div>
          <div v-if="roleMode === 'create'"><div class="fl">{{ $t('nrLayer') }}</div><input v-model="edit.layer" :placeholder="$t('nrLayerPh')" maxlength="32" /></div>
          <div><div class="fl">{{ $t('erDesc') }}</div><textarea v-model="edit.description" :placeholder="$t('erDescVal')" /></div>
          <div v-if="roleMode === 'create'" class="nrnote">{{ $t('nrNote') }}</div>
          <div class="ertoggle">
            <div><div class="rt">{{ $t('erApprAllow') }}</div><div class="rd">{{ $t('erApprAllowSub') }}</div></div>
            <VSwitch v-model="edit.canApprove" />
          </div>
        </div>
        <div class="ifoot"><VButton variant="secondary" @click="roleForm = false">{{ $t('cancel') }}</VButton><VButton variant="primary" :disabled="roleMode === 'create' && !edit.name.trim()" data-testid="role-form-save" @click="saveRole">{{ roleMode === 'create' ? $t('nrCreate') : $t('save') }}</VButton></div>
      </div>
    </div>

    <TagEditModal
      v-if="detail" :open="tagModalOpen" :title="$t('tagRoleTitle')" :subtitle="detail.name"
      :tags="detail.tags" :suggestions="allTags"
      @close="tagModalOpen = false" @save="saveRoleTags"
    />
  </div>
</template>

<style scoped>
.wrap { flex: 1; min-height: 0; display: flex; flex-direction: column; }
.vtabs { display: flex; align-items: center; gap: 8px; height: 52px; flex-shrink: 0; padding: 0 24px; border-bottom: 1px solid var(--border-subtle); }
/* 用 flex 居中,不用 line-height —— `font:` 简写会把 line-height 一并重置成
   normal,而这里原先正是 `line-height: 32px; ... font: 600 ...`,后者把前者吹掉了,
   于是文字在盒子里贴着上边。改成 flex 之后,字号怎么调都不会再把居中弄丢。 */
.vt { display: flex; align-items: center; height: 32px; padding: 0 16px; border-radius: 9px; cursor: pointer; font: 600 12px var(--font-body); color: var(--text-muted); }
.vt.active { background: var(--accent-subtle); color: var(--accent-text); }
.vhint { margin-left: auto; font: 500 11px var(--font-mono); color: var(--text-faint); }
.rolegrid { flex: 1; min-height: 0; display: grid; grid-template-columns: 300px minmax(0, 1fr); background: var(--surface-page); }
@media (max-width: 1080px) { .rolegrid { grid-template-columns: 260px minmax(0, 1fr); } }

/* ---------------- 左:角色导航 ---------------- */
.rolepanel { display: flex; flex-direction: column; min-height: 0; border-right: 1px solid var(--border-subtle); background: var(--surface-card); }
.rphead { display: flex; align-items: center; gap: 10px; padding: 16px 14px 10px; }
.rphead .eyebrow { flex: 1; font: 600 11px var(--font-mono); letter-spacing: 0.12em; color: var(--text-faint); text-transform: uppercase; }
.rsearch { display: flex; align-items: center; gap: 7px; margin: 0 14px 10px; height: 32px; padding: 0 10px; border: 1px solid var(--border-default); border-radius: 9px; background: var(--surface-sunken); }
.rsearch input { width: 100%; min-width: 0; border: none; outline: none; background: transparent; color: var(--text-strong); font: 500 12px var(--font-body); }
.sclear { display: grid; place-items: center; width: 18px; height: 18px; border: none; border-radius: 5px; background: transparent; color: var(--text-faint); cursor: pointer; }
.sclear:hover { color: var(--text-body); }
.rl { flex: 1; min-height: 0; display: flex; flex-direction: column; gap: 6px; padding: 0 14px 18px; }
/* 选中态用左侧重点色边条,而不是整块换底:一整块蓝底在一列卡片里太重,而这一列
   要一眼看出"现在编的是哪一个",边条比底色更快。 */
.ritem { position: relative; padding: 11px 13px 11px 15px; border-radius: 10px; cursor: pointer; border: 1px solid var(--border-subtle); background: var(--surface-card); transition: background .15s ease, border-color .15s ease; }
.ritem:hover { border-color: var(--border-default); background: var(--surface-sunken); }
.ritem.active { background: var(--accent-subtle); border-color: transparent; }
.ritem.active::before { content: ''; position: absolute; left: 0; top: 8px; bottom: 8px; width: 3px; border-radius: 0 3px 3px 0; background: var(--accent); }
.rname { display: flex; align-items: center; gap: 8px; min-width: 0; font: 600 13px var(--font-body); color: var(--text-body); }
.rn { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ritem.active .rname { color: var(--accent-text); }
.builtin { flex-shrink: 0; padding: 1px 6px; border-radius: 5px; background: var(--surface-sunken); border: 1px solid var(--border-subtle); font: 600 9.5px var(--font-mono); color: var(--text-faint); }
.ritem.active .builtin { background: var(--surface-card); border-color: transparent; }
.rlayer { margin-top: 4px; font: 500 11px var(--font-mono); color: var(--text-faint); }
.sep { margin: 0 5px; opacity: .6; }
.rempty { padding: 24px 4px; text-align: center; font: 500 12px var(--font-body); color: var(--text-faint); }

/* ---------------- 右:权限详情 ---------------- */
.detailwrap { min-height: 0; padding: 22px 26px 32px; }
/* 限宽:宽屏上一张横向铺满的表格,眼睛要从最左的动作名一路扫到最右的开关,中间
   全是空白;而这张表的行本来就短。 */
.detail { max-width: 1120px; border: 1px solid var(--border-subtle); border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-sm); padding: 22px 24px 24px; }
.mhead { display: flex; align-items: flex-start; gap: 12px; padding-bottom: 16px; border-bottom: 1px solid var(--border-subtle); }
.mhead .grow { flex: 1; min-width: 0; }
.mtitle { font: 700 17px var(--font-display); color: var(--text-strong); }
.mtsub { margin-left: 8px; font: 500 12px var(--font-body); color: var(--text-faint); }
.mhint { margin-top: 4px; font: 500 12px var(--font-body); color: var(--text-muted); }
.roflag { display: inline-flex; align-items: center; height: 26px; padding: 0 11px; border-radius: 999px; background: var(--surface-sunken); border: 1px solid var(--border-subtle); font: 600 11px var(--font-mono); color: var(--text-muted); }

.sec { margin-top: 22px; }
.sec.last { margin-bottom: 2px; }
.sechead { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; margin-bottom: 11px; }
.eyebrow2 { font: 600 11px var(--font-mono); letter-spacing: 0.1em; color: var(--text-muted); text-transform: uppercase; }
.secsub { font: 500 12px var(--font-body); color: var(--text-faint); }
.secbtn { margin-left: auto; }

/* ---------------- 权限矩阵 ---------------- */
.mtable { border: 1px solid var(--border-subtle); border-radius: 12px; overflow-y: hidden; background: var(--surface-card); }
.mgrid { min-width: max-content; }
.mth, .mtr { display: grid; gap: 10px; }
.mth { padding: 10px 16px; border-bottom: 1px solid var(--border-default); background: var(--surface-page); font: 600 11px var(--font-mono); letter-spacing: 0.06em; color: var(--text-muted); text-transform: uppercase; align-items: center; }
.mth .ctr { display: flex; justify-content: center; }
/* 环境列头的颜色来自 envtier.dotFor —— 和树、审批列表、分层页同一个来源。 */
.envhd { padding: 2px 9px; border-radius: 999px; background: var(--surface-sunken); border: 1px solid var(--border-subtle); font: 700 10px var(--font-mono); color: var(--text-muted); }
.envhd.danger { background: var(--danger-subtle); color: var(--danger-text); border-color: transparent; }
.envhd.warning { background: var(--warning-subtle); color: var(--warning-text); border-color: transparent; }
.envhd.success { background: var(--success-subtle); color: var(--success-text); border-color: transparent; }
.envhd.info { background: var(--accent-subtle); color: var(--accent-text); border-color: transparent; }
.mtr { padding: 7px 16px; border-bottom: 1px solid var(--border-subtle); align-items: center; }
.mtr:last-child { border-bottom: none; }
/* 斑马纹弱到几乎看不见,整行 hover 才是主要的定位手段 —— 这张表最常做的动作是
   横着看一行、竖着扫一列,而强斑马纹会跟竖向的列边框打架。 */
.mtr:nth-child(even) { background: color-mix(in oklch, var(--surface-sunken) 30%, var(--surface-card)); }
.mtr:hover { background: var(--surface-sunken); }
.cellwrap, .mth .ctr { border-left: 1px solid var(--border-subtle); }
.cap { font: 500 13px var(--font-body); color: var(--text-body); }
.cellwrap { display: flex; justify-content: center; }
/* 三种判定各自一块语义底色 + 一个图标。原先是三个裸字符,同字号同粗细,扫一列
   下去要逐格辨认。 */
.cell { width: 34px; height: 28px; border: 1px solid transparent; border-radius: 8px; display: grid; place-items: center; cursor: pointer; transition: box-shadow .12s ease, border-color .12s ease; }
.cell.allow { background: var(--success-subtle); color: var(--success-text); }
.cell.approve { background: var(--danger-subtle); color: var(--danger-text); }
.cell.deny { background: transparent; color: var(--text-faint); }
.cell:hover:not(.ro) { border-color: var(--accent-text); box-shadow: 0 0 0 3px var(--accent-subtle); }
.cell.ro { cursor: default; }
.cell.sm { width: 20px; height: 20px; border-radius: 6px; }
.legend { margin-top: 10px; display: flex; align-items: center; gap: 16px; flex-wrap: wrap; font: 500 11.5px var(--font-body); color: var(--text-muted); }
.lg { display: inline-flex; align-items: center; gap: 6px; }

/* ---------------- 菜单权限 ---------------- */
/* 网格卡片:开关紧跟在名字后面。原先是一行一项,名字在最左、开关在屏幕最右,
   一千多像素的空白把两者拉断了 —— 要对准哪个开关属于哪一项得用手指比。 */
.menugrid { display: grid; grid-template-columns: repeat(auto-fit, minmax(240px, 1fr)); gap: 10px; }
.menucard { display: flex; align-items: center; gap: 10px; padding: 10px 12px; border: 1px solid var(--border-subtle); border-radius: 10px; background: var(--surface-card); cursor: pointer; transition: border-color .12s ease, background .12s ease; }
.menucard:hover { border-color: var(--border-default); background: var(--surface-sunken); }
.menucard.on { border-color: var(--accent-subtle-border); background: var(--accent-subtle); }
.menucard .mi { flex-shrink: 0; color: var(--text-faint); }
.menucard.on .mi { color: var(--accent-text); }
.menucard .ml { flex: 1; min-width: 0; font: 600 12.5px var(--font-body); color: var(--text-body); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.menucard.on .ml { color: var(--accent-text); }
.menucard.locked { cursor: not-allowed; opacity: 0.6; }
.menucard .mlock { flex-shrink: 0; padding: 1px 6px; border-radius: 4px; background: var(--surface-sunken); color: var(--text-faint); font: 600 10.5px var(--font-body); }

/* ---------------- 授权范围 ---------------- */
.rtags { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; padding: 12px 14px; border: 1px solid var(--border-subtle); border-radius: 12px; background: var(--surface-sunken); }
.rtag { display: inline-flex; height: 24px; align-items: center; padding: 0 11px; border-radius: 999px; background: var(--accent-subtle); color: var(--accent-text); font: 600 11.5px var(--font-mono); }
.rtagall { display: inline-flex; height: 24px; align-items: center; padding: 0 11px; border-radius: 999px; background: var(--success-subtle); color: var(--success-text); font: 600 11.5px var(--font-mono); }
.rtedit { display: inline-flex; align-items: center; gap: 6px; height: 24px; padding: 0 11px; border: 1px dashed var(--border-default); border-radius: 999px; background: transparent; color: var(--text-muted); font: 600 11.5px var(--font-body); cursor: pointer; }
.rtedit:hover { border-color: var(--accent); color: var(--accent-text); }

/* ---------------- 成员 ---------------- */
.memgrid { display: grid; grid-template-columns: repeat(auto-fill, minmax(230px, 1fr)); gap: 10px; }
.memcard { position: relative; display: flex; align-items: center; gap: 10px; padding: 9px 11px; border: 1px solid var(--border-subtle); border-radius: 10px; background: var(--surface-card); }
.memcard:hover { border-color: var(--border-default); }
.mava { flex-shrink: 0; width: 30px; height: 30px; border-radius: 50%; background: var(--surface-raised); border: 1px solid var(--border-default); display: grid; place-items: center; font: 600 11px var(--font-body); color: var(--text-muted); }
.mava.first { background: linear-gradient(135deg, #5e83fb, #2dcde6); color: #fff; border: none; }
.mbody { flex: 1; min-width: 0; }
.mn { font: 600 12.5px var(--font-body); color: var(--text-strong); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.me { font: 500 11px var(--font-mono); color: var(--text-faint); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* 移除按钮只在 hover 时露出来:它是一次不可逆的操作,常驻在每张卡片上等于把
   最危险的那个按钮摆得到处都是。 */
.mx { flex-shrink: 0; width: 22px; height: 22px; display: grid; place-items: center; border: none; border-radius: 6px; background: transparent; cursor: pointer; color: var(--text-faint); opacity: 0; transition: opacity .12s ease; }
.memcard:hover .mx, .mx:focus-visible { opacity: 1; }
.mx:hover { background: var(--danger-subtle); color: var(--danger-text); }
.memempty { padding: 18px 4px; font: 500 12px var(--font-body); color: var(--text-faint); }

/* user view */
.userpage { flex: 1; min-height: 0; padding: 24px 28px; }
.uhead { display: flex; align-items: center; margin-bottom: 18px; }
.utitle { font: 700 16px var(--font-display); color: var(--text-strong); }
.usub { font: 500 12px var(--font-body); color: var(--text-muted); margin-top: 3px; }
.uhead :deep(.vbtn) { margin-left: auto; }
.uacts { margin-left: auto; display: flex; gap: 10px; }
.uacts :deep(.vbtn) { margin-left: 0; }
.utable { border: 1px solid var(--border-subtle); border-radius: 14px; overflow: hidden; background: var(--surface-card); }
/* 状态列按内容给最小宽度:OTP、SSO 两枚标签 + 启停徽章 + 管理按钮。给 1fr 时 SSO 标签一加上,
   "启用"就被挤成两行。 */
.uth, .utr { display: grid; grid-template-columns: 1.5fr 1.6fr 2fr 0.8fr minmax(250px, 1fr); gap: 12px; }
.uth { padding: 12px 18px; border-bottom: 1px solid var(--border-subtle); background: var(--surface-sunken); font: 600 11px var(--font-mono); letter-spacing: 0.05em; color: var(--text-faint); text-transform: uppercase; }
.uth .r { text-align: right; }
.utr { padding: 12px 18px; border-bottom: 1px solid var(--border-subtle); align-items: center; }
.ucell { display: flex; align-items: center; gap: 10px; }
.uava { width: 30px; height: 30px; border-radius: 50%; background: #232838; display: flex; align-items: center; justify-content: center; font: 600 11px var(--font-body); color: var(--text-muted); }
.uava.first { background: linear-gradient(135deg, #5e83fb, #2dcde6); color: #fff; }
.svcbadge { margin-left: 6px; padding: 1px 6px; border-radius: 999px; background: var(--accent-subtle); color: var(--accent-text); font: 600 10px var(--font-mono); vertical-align: 1px; }
.un { font: 600 13px var(--font-body); color: var(--text-strong); }
.ud { font: 500 10px var(--font-mono); color: var(--text-faint); }
.mono { font: 500 12px var(--font-mono); }
.mute { color: var(--text-muted); }
.uroles { display: flex; flex-wrap: wrap; gap: 5px; }
.urole { display: inline-flex; align-items: center; height: 20px; padding: 0 8px; border-radius: 999px; background: var(--accent-subtle); color: var(--accent-text); font: 600 10px var(--font-mono); }
.ustatus { display: flex; justify-content: flex-end; align-items: center; gap: 9px; }
.stbadge { display: inline-flex; align-items: center; gap: 6px; height: 26px; padding: 0 11px; border-radius: 999px; cursor: pointer; font: 600 11px var(--font-mono); white-space: nowrap; flex-shrink: 0; }
.dotc { width: 6px; height: 6px; border-radius: 50%; background: currentColor; }
.otptag { display: inline-flex; align-items: center; gap: 4px; height: 22px; padding: 0 8px; border-radius: 999px; font: 700 10px var(--font-mono); background: var(--surface-sunken); color: var(--text-faint); border: 1px solid var(--border-subtle); }
.otptag.on { background: var(--success-subtle); color: var(--success-text); border-color: transparent; }
.manage { width: 30px; height: 30px; display: flex; align-items: center; justify-content: center; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-sunken); color: var(--text-muted); cursor: pointer; }
.manage:hover { color: var(--accent-text); border-color: var(--accent-subtle-border); }
/* user management modal */
.usec { padding: 16px 22px; border-top: 1px solid var(--border-subtle); }
.uslbl { display: flex; align-items: center; gap: 7px; font: 600 12px var(--font-display); color: var(--text-strong); }
.pwrow { margin-top: 10px; display: flex; gap: 9px; }
.pwrow input { flex: 1; height: 40px; box-sizing: border-box; border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-sunken); padding: 0 12px; font: 400 13px var(--font-mono); color: var(--text-strong); outline: none; }
.pwrow input:focus { border-color: var(--accent-text); }
.umsg { margin-top: 8px; font: 600 11.5px var(--font-mono); color: var(--accent-text); }
.umsg.inline { margin-top: 0; }
/* multi-role picker (user modal + create modal) */
.urhint { margin-left: auto; font: 500 10.5px var(--font-mono); color: var(--text-faint); }
.rolepick { margin-top: 10px; display: flex; flex-wrap: wrap; gap: 8px; }
.rchip { display: inline-flex; align-items: center; gap: 6px; height: 32px; padding: 0 12px; border: 1px solid var(--border-default); border-radius: 999px; background: var(--surface-sunken); color: var(--text-muted); font: 600 11.5px var(--font-body); cursor: pointer; transition: color .12s, border-color .12s, background .12s; }
.rchip:hover { color: var(--text-body); border-color: var(--border-strong); }
.rchip.on { border-color: var(--accent-subtle-border); background: var(--accent-subtle); color: var(--accent-text); }
.urrow { margin-top: 11px; display: flex; align-items: center; gap: 12px; }
.otprow { margin-top: 10px; display: flex; gap: 9px; }
.otpstate { margin-left: auto; font: 700 10px var(--font-mono); padding: 2px 8px; border-radius: 999px; background: var(--surface-sunken); color: var(--text-faint); }
.otpstate.on { background: var(--success-subtle); color: var(--success-text); }
.otpresult { margin-top: 14px; display: flex; gap: 14px; align-items: center; padding: 12px; border: 1px solid var(--border-subtle); border-radius: 12px; background: var(--surface-sunken); }
.qrbox { width: 108px; height: 108px; flex-shrink: 0; border-radius: 10px; background: #fff; padding: 6px; box-sizing: border-box; }
.qrbox img { width: 100%; height: 100%; display: block; }
.otpinfo { min-width: 0; }
.oih { font: 500 12px var(--font-body); color: var(--text-muted); }
.oisecret { margin-top: 8px; font: 700 14px var(--font-mono); letter-spacing: 0.08em; color: var(--text-strong); word-break: break-all; }
.ufoot { margin-top: 12px; font: 500 11px var(--font-mono); color: var(--text-faint); }
/* modals */
.overlay { position: fixed; inset: 0; z-index: 50; }
.mask { position: absolute; inset: 0; background: rgba(4, 6, 12, 0.7); backdrop-filter: blur(3px); }
.pmodal, .imodal { position: absolute; left: 50%; top: 50%; transform: translate(-50%, -50%); width: 480px; max-width: 92vw; background: var(--surface-card); border: 1px solid var(--border-default); border-radius: 18px; overflow: hidden; box-shadow: var(--shadow-xl); animation: modalIn 0.26s cubic-bezier(0.16, 1, 0.3, 1); }
.phead, .ihead { display: flex; align-items: center; gap: 12px; padding: 18px 22px; border-bottom: 1px solid var(--border-subtle); }
.pic, .iic { width: 36px; height: 36px; border-radius: 10px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; }
.pt, .it { font: 700 16px var(--font-display); color: var(--text-strong); }
.ps, .is { font: 500 12px var(--font-body); color: var(--text-muted); margin-top: 1px; }
.px, .ix { color: var(--text-muted); margin-left: auto; cursor: pointer; }
.psearch { display: flex; align-items: center; gap: 8px; margin: 14px 22px 6px; height: 38px; padding: 0 12px; border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-sunken); font: 400 12px var(--font-body); color: var(--text-faint); }
.plist { max-height: 300px; padding: 6px 14px 14px; }
.prow { display: flex; align-items: center; gap: 11px; padding: 9px 10px; border-radius: 10px; cursor: pointer; }
.prow:hover { background: var(--surface-sunken); }
.pava { width: 32px; height: 32px; border-radius: 50%; background: #232838; border: 1px solid var(--border-default); display: flex; align-items: center; justify-content: center; font: 600 11px var(--font-body); color: var(--text-muted); }
.pgrow { flex: 1; min-width: 0; }
.pn { font: 600 13px var(--font-body); color: var(--text-body); }
.pd { font: 500 11px var(--font-mono); color: var(--text-faint); }
.joined { display: inline-flex; align-items: center; gap: 5px; font: 600 11px var(--font-mono); color: var(--success-text); }
.addbtn { display: inline-flex; align-items: center; gap: 5px; height: 26px; padding: 0 11px; border-radius: 8px; background: var(--accent-subtle); color: var(--accent-text); font: 600 11px var(--font-mono); }
.addbtn .ic { font-weight: 700; }
.pfoot, .ifoot { display: flex; align-items: center; padding: 13px 22px; border-top: 1px solid var(--border-subtle); background: var(--surface-raised); }
.pfh { font: 500 11px var(--font-mono); color: var(--text-faint); }
.pfoot :deep(.vbtn) { margin-left: auto; }
.ifoot { gap: 10px; justify-content: flex-end; }
.ibody { padding: 20px 22px; display: flex; flex-direction: column; gap: 16px; }
.fl { font: 500 11px var(--font-body); color: var(--text-faint); margin-bottom: 6px; }
.ibody input { width: 100%; box-sizing: border-box; height: 40px; border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-sunken); padding: 0 12px; font: 400 13px var(--font-mono); color: var(--text-body); outline: none; }
.ifield { height: 40px; border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-sunken); display: flex; align-items: center; padding: 0 12px; font: 500 13px var(--font-body); color: var(--text-body); }
.inote { display: flex; align-items: flex-start; gap: 8px; padding: 11px 13px; border: 1px solid var(--border-subtle); border-radius: 10px; background: var(--surface-sunken); font: 400 11.5px/1.5 var(--font-body); color: var(--text-muted); }
.psearch input { flex: 1; background: transparent; border: none; outline: none; font: 400 12px var(--font-body); color: var(--text-body); }
.er2 { display: grid; grid-template-columns: 1.4fr 1fr; gap: 16px; }
.ibody textarea { width: 100%; box-sizing: border-box; min-height: 56px; resize: none; border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-sunken); padding: 11px 13px; font: 400 13px var(--font-body); color: var(--text-body); outline: none; }
.nrnote { padding: 10px 12px; border-radius: 9px; background: var(--warning-subtle); color: var(--warning-text); font: 500 12px/1.6 var(--font-body); }
.ertoggle { display: flex; align-items: center; justify-content: space-between; padding: 12px 14px; border: 1px solid var(--border-subtle); border-radius: 10px; background: var(--surface-sunken); }
.ertoggle .rt { font: 600 13px var(--font-body); color: var(--text-strong); }
.ertoggle .rd { font: 500 11px var(--font-mono); color: var(--text-muted); margin-top: 2px; }
.pm-head { padding: 18px 24px 0; margin-bottom: 8px; }

/* SSO 绑定 */
.ssotag.act { cursor: pointer; }
.ssotag.act:hover { text-decoration: line-through; }
</style>
