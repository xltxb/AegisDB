<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Crown, Shield, UserCog, Code, Eye, SquareTerminal, ClipboardCheck, Database,
  ShieldAlert, Layers, UsersRound, ScrollText, Settings, UserPlus, X, Search, Check, MailPlus, ShieldCheck, Tag, KeyRound, Rocket,
} from 'lucide-vue-next'
import QRCode from 'qrcode'
import VButton from '@/components/common/VButton.vue'
import VSwitch from '@/components/common/VSwitch.vue'
import VSelect from '@/components/common/VSelect.vue'
import TagEditModal from '@/components/modals/TagEditModal.vue'
import api from '@/api'
import { confirmAction } from '@/lib/confirm'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import { useEnvTierStore } from '@/stores/envtier'
import type { RoleBrief, RoleDetail, UserView } from '@/types'

const { t } = useI18n()
const ui = useUIStore()
const auth = useAuthStore()
const envtier = useEnvTierStore()
// Role/permission + user-credential edits are platform-admin only (backend
// enforces this too); non-admins see the page read-only.
// Admin if ANY held role is admin (union), falling back to the primary role code.
const isAdmin = computed(() => auth.me?.roleCodes?.includes('admin') ?? (auth.me?.roleCode === 'admin'))
function publishSub() { ui.pageSub = t('subPerms', { r: roles.value.length, u: users.value.length }) }
const view = ref<'roles' | 'users'>('roles')
const roles = ref<RoleBrief[]>([])
const detail = ref<RoleDetail | null>(null)
const users = ref<UserView[]>([])
const memberForm = ref(false)
const inviteForm = ref(false)
const roleForm = ref(false)
const edit = ref({ name: '', description: '', defaultConnRole: 'dba_l2', canApprove: true })
const inviteEmail = ref('')
const inviteRoleName = ref('')
const memberSearch = ref('')

const filteredUsers = computed(() => {
  const q = memberSearch.value.trim().toLowerCase()
  if (!q) return users.value
  return users.value.filter(
    (u) => u.name.toLowerCase().includes(q) || (u.dept || '').toLowerCase().includes(q) || u.email.toLowerCase().includes(q),
  )
})

function openRoleForm() {
  if (!detail.value) return
  edit.value = { name: detail.value.name, description: '', defaultConnRole: detail.value.code === 'ro' ? 'readonly' : 'dba_l2', canApprove: detail.value.code === 'admin' || detail.value.code === 'owner' }
  roleForm.value = true
}
async function saveRole() {
  if (!detail.value) return
  // M14: 保存角色失败以 toast 呈现
  try {
    detail.value = await api.updateRole(detail.value.id, edit.value)
    await loadRoles()
    roleForm.value = false
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  }
}
function openInvite() {
  inviteEmail.value = ''
  inviteRoleName.value = roles.value.find((r) => r.code === 'ro')?.name || roles.value[0]?.name || ''
  inviteForm.value = true
}
async function doInvite() {
  const roleId = roles.value.find((r) => r.name === inviteRoleName.value)?.id
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
const capKeys = ['select', 'write', 'ddl', 'grant', 'conn', 'approve', 'explain']
// One row per capability dimension, in the backend's order (model.Capabilities).
// 'release' is the newest: it gates RAISING a release on a tier, which is a
// different question from whether the statement inside it may run — the pipeline
// still asks write/ddl again at execute time.
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
]
const sym: Record<string, { s: string; c: string }> = {
  allow: { s: '✓', c: 'var(--success-text)' },
  approve: { s: '⚑', c: 'var(--warning-text)' },
  deny: { s: '—', c: 'var(--text-faint)' },
}
const cycleOrder = ['allow', 'approve', 'deny']

async function loadRoles() {
  // M14: 加载失败以 toast 呈现，避免静默失败
  try {
    roles.value = await api.roles()
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
async function loadUsers() {
  // M14: 加载失败以 toast 呈现
  try { users.value = await api.users(); publishSub() } catch (e) { ui.notifyError(e, t('loadFailed')) }
}

onMounted(async () => {
  // Tiers decide the matrix columns, so load them before anything renders cells.
  await envtier.load().catch(() => {})
  await loadRoles(); await loadUsers()
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
async function toggleMenu(key: string) {
  if (!detail.value || !isAdmin.value) return
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
  if (!confirmAction(t('pmRemoveMemberConfirm', { role: detail.value.name, name: mb?.name || userId }))) return
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
  // M15: 禁用用户前二次确认（启用无需确认）
  if (u.status !== 'disabled' && !confirmAction(t('pmDisableUserConfirm', { name: u.name }))) return
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
  } catch (e) { ui.notifyError(e, t('cuFailed')) }
}
function syncModal() {
  if (userModal.value) userModal.value = users.value.find((x) => x.id === userModal.value!.id) || userModal.value
}
async function savePw() {
  if (!userModal.value) return
  if (newPw.value.length < 8) { pwMsg.value = t('pwTooShort'); return }
  try { await api.setUserPassword(userModal.value.id, newPw.value); pwMsg.value = t('pwSaved'); newPw.value = '' }
  catch { pwMsg.value = t('pwFailed') }
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
  if (!confirmAction(t('pmResetMfaConfirm', { name: userModal.value.name }))) return
  try { await api.resetUserMfa(userModal.value.id); otpBind.value = null; otpQr.value = ''; otpMsg.value = t('otpUnbound'); await loadUsers(); syncModal() }
  catch { otpMsg.value = t('otpFailed') }
}

const selName = computed(() => detail.value?.name || '')
const memberIds = computed(() => new Set(detail.value?.memberIds || []))
</script>

<template>
  <div class="wrap">
    <div class="vtabs">
      <div class="vt" :class="{ active: view === 'roles' }" @click="view = 'roles'">{{ $t('tabRoleView') }}</div>
      <div class="vt" :class="{ active: view === 'users' }" @click="view = 'users'">{{ $t('tabUserView') }}</div>
      <div class="vhint">{{ view === 'roles' ? $t('pmRolesHint', { n: roles.length }) : $t('pmUsersHint', { n: users.length }) }}</div>
    </div>

    <!-- ROLE VIEW -->
    <div v-if="view === 'roles'" class="rolegrid">
      <div class="scy rolelist">
        <div class="eyebrow">{{ $t('rolesTier') }}</div>
        <div class="rl">
          <div v-for="r in roles" :key="r.id" class="ritem" :class="{ active: detail?.id === r.id }" @click="selectRole(r.id)">
            <div class="rname"><component :is="roleIcon[r.icon] || Shield" :size="15" />{{ r.name }}</div>
            <div class="rlayer">{{ r.layer }} · {{ $t('pmMembersN', { n: r.count }) }}</div>
          </div>
        </div>
      </div>

      <div v-if="detail" class="scy matrix">
        <div class="mhead">
          <div class="mtitle">{{ $t('matrixTitle') }}{{ selName }}</div>
          <span v-if="!isAdmin" class="roflag">{{ $t('readOnlyPerms') }}</span>
          <VButton v-if="isAdmin" variant="secondary" @click="openRoleForm">{{ $t('editRole') }}</VButton>
        </div>
        <div class="mhint">{{ detail.layer }} · {{ detail.members.length }} {{ $t('membersTitle') }} · {{ $t('matrixHint') }}</div>

        <!-- Scrolls inside its own box once the tier count outgrows the width;
             the page body must never scroll sideways. -->
        <div class="mtable scx">
          <!-- The template goes on each ROW: they are the grids. Binding it to the
               wrapper instead left the rows with no columns at all, so every cell
               stacked vertically in one implicit column. -->
          <div class="mgrid">
            <div class="mth" :style="matrixCols"><span>{{ $t('colCap') }}</span><span v-for="env in envCols" :key="env" class="ctr">{{ env }}</span></div>
            <div v-for="(cap, i) in capKeys" :key="cap" class="mtr" :style="matrixCols">
              <span class="cap">{{ $t(capLabels[i] as any) }}</span>
              <div v-for="env in envCols" :key="env" class="cellwrap">
                <div class="cell" :style="{ color: sym[cellLevel(cap, env)].c }" @click="cycleCell(cap, env)">{{ sym[cellLevel(cap, env)].s }}</div>
              </div>
            </div>
          </div>
        </div>

        <div class="eyebrow2">{{ $t('menuTitle') }}</div>
        <div class="menusub">{{ $t('menuSub') }}</div>
        <div class="menus">
          <div v-for="m in menuDefs" :key="m.key" class="menurow">
            <component :is="m.icon" :size="17" color="var(--text-muted)" />
            <span class="ml">{{ $t(m.label as any) }}</span>
            <VSwitch :model-value="!!detail.menus[m.key]" :disabled="!isAdmin" @update:model-value="toggleMenu(m.key)" />
          </div>
        </div>

        <div class="eyebrow2">{{ $t('tagAccessTitle') }}</div>
        <div class="menusub">{{ detail.tags.length ? $t('tagAccessSub') : $t('tagAccessAll') }}</div>
        <div class="rtags">
          <span v-for="tg in detail.tags" :key="tg" class="rtag">{{ tg }}</span>
          <span v-if="!detail.tags.length" class="rtagall">{{ $t('tagAllBadge') }}</span>
          <span v-if="isAdmin" class="rtedit" @click="tagModalOpen = true"><Tag :size="12" />{{ $t('tagAssign') }}</span>
        </div>

        <div class="eyebrow2">{{ $t('membersTitle') }} · {{ detail.members.length }} {{ $t('people') }}</div>
        <div class="members">
          <div v-for="(mb, i) in detail.members" :key="mb.id" class="mchip">
            <div class="mava" :class="{ first: i === 0 }">{{ mb.initials }}</div>
            <span>{{ mb.name }}</span>
            <span v-if="isAdmin" class="mx" :title="$t('pmRemoveMember')" @click="removeMember(mb.id)"><X :size="12" /></span>
          </div>
          <div v-if="isAdmin" class="addm" @click="memberForm = true"><UserPlus :size="15" />{{ $t('addMember') }}</div>
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
            <span v-for="rn in u.roles" :key="rn" class="urole">{{ rn }}</span>
            <span v-if="!u.roles.length" class="mono mute">{{ $t('unassigned') }}</span>
          </div>
          <span class="mono mute">{{ u.lastActive }}</span>
          <div class="ustatus">
            <span class="otptag" :class="{ on: u.mfaEnabled }" :title="u.mfaEnabled ? $t('otpBound') : $t('otpUnboundState')"><ShieldCheck :size="12" />OTP</span>
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
              <Check v-if="roleSel.includes(r.id)" :size="13" /><component v-else :is="roleIcon[r.icon] || Shield" :size="13" />{{ r.name }}
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
          <div><div class="fl">{{ $t('ivEmail') }}</div><input v-model="inviteEmail" placeholder="name@vela.io" type="email" /></div>
          <div><div class="fl">{{ $t('ivRole') }}</div><VSelect v-model="inviteRoleName" :options="roles.map((r) => r.name)" /></div>
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
          <div><div class="fl">{{ $t('ivEmail') }}</div><input v-model="createEmail" placeholder="name@vela.io" type="email" /></div>
          <div><div class="fl">{{ $t('cuName') }}</div><input v-model="createName" :placeholder="$t('cuNamePh')" type="text" /></div>
          <div><div class="fl">{{ $t('cuPw') }}</div><input v-model="createPw" :placeholder="$t('cuPwPh')" type="password" /></div>
          <div>
            <div class="fl">{{ $t('cuRoles') }}</div>
            <div class="rolepick">
              <button v-for="r in roles" :key="r.id" type="button" class="rchip" :class="{ on: createRoleIds.includes(r.id) }" @click="toggleCreateRole(r.id)">
                <Check v-if="createRoleIds.includes(r.id)" :size="13" /><component v-else :is="roleIcon[r.icon] || Shield" :size="13" />{{ r.name }}
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
        <div class="ihead"><div class="iic"><UserCog :size="19" color="var(--accent-text)" /></div><div><div class="it">{{ $t('erTitle') }}{{ selName }}</div><div class="is">{{ $t('erSub') }}</div></div><X :size="18" class="ix" @click="roleForm = false" /></div>
        <div class="ibody">
          <div class="er2">
            <div><div class="fl">{{ $t('erName') }}</div><input v-model="edit.name" /></div>
            <div><div class="fl">{{ $t('erDefRole') }}</div><input v-model="edit.defaultConnRole" /></div>
          </div>
          <div><div class="fl">{{ $t('erDesc') }}</div><textarea v-model="edit.description" :placeholder="$t('erDescVal')" /></div>
          <div class="ertoggle">
            <div><div class="rt">{{ $t('erApprAllow') }}</div><div class="rd">{{ $t('erApprAllowSub') }}</div></div>
            <VSwitch v-model="edit.canApprove" />
          </div>
        </div>
        <div class="ifoot"><VButton variant="secondary" @click="roleForm = false">{{ $t('cancel') }}</VButton><VButton variant="primary" @click="saveRole">{{ $t('save') }}</VButton></div>
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
.vt { height: 32px; line-height: 32px; padding: 0 16px; border-radius: 9px; cursor: pointer; font: 600 12px var(--font-body); color: var(--text-muted); }
.vt.active { background: var(--accent-subtle); color: var(--accent-text); }
.vhint { margin-left: auto; font: 500 11px var(--font-mono); color: var(--text-faint); }
.rolegrid { flex: 1; min-height: 0; display: grid; grid-template-columns: 280px 1fr; }
.rolelist { border-right: 1px solid var(--border-subtle); background: var(--surface-sunken); padding: 18px 14px; }
.eyebrow { font: 600 11px var(--font-mono); letter-spacing: 0.14em; color: var(--text-faint); text-transform: uppercase; margin-bottom: 12px; }
.rl { display: flex; flex-direction: column; gap: 8px; }
.ritem { padding: 12px 14px; border-radius: 10px; cursor: pointer; transition: all 0.15s ease; border: 1px solid var(--border-subtle); }
.ritem.active { background: var(--accent-subtle); border-color: var(--accent-subtle-border); }
.rname { display: flex; align-items: center; gap: 8px; font: 600 13px var(--font-body); color: var(--text-body); }
.ritem.active .rname { color: var(--accent-text); }
.rlayer { font: 500 11px var(--font-mono); color: var(--text-faint); margin-top: 4px; }
.matrix { padding: 24px 28px; }
.mhead { display: flex; align-items: center; margin-bottom: 6px; }
.mtitle { font: 700 16px var(--font-display); color: var(--text-strong); }
.mhead :deep(.vbtn) { margin-left: auto; }
.roflag { margin-left: auto; display: inline-flex; align-items: center; height: 24px; padding: 0 10px; border-radius: 999px; background: var(--surface-sunken); border: 1px solid var(--border-subtle); font: 600 11px var(--font-mono); color: var(--text-muted); }
.mhint { font: 500 12px var(--font-body); color: var(--text-muted); margin-bottom: 18px; }
/* overflow-y:hidden keeps the rounded corners clipping (the old `overflow:hidden`
   did that) while .scx supplies the horizontal scroll. */
/* 这页四个区块全是白底叠白底,--border-subtle 在浅色下几乎不可见 —— 区块边界
   一律用 --border-default,矩阵格子给出可点击的"形状"而不是漂浮的符号。 */
.mtable { border: 1px solid var(--border-default); border-radius: 14px; overflow-y: hidden; background: var(--surface-card); box-shadow: var(--shadow-xs); }
/* min-width:max-content keeps the rows at their natural width so .mtable's
   overflow-x is what scrolls; without it the grid compresses instead. */
.mgrid { min-width: max-content; }
/* Column template is set inline from the tier count — see matrixCols — and it is
   set on the ROWS, which are the grids. It sat on .mgrid for a while, which is a
   plain block: the declaration was simply ignored there, and the rows, left with
   no template, collapsed to one implicit column and stacked every cell. Nothing
   errors when grid-template-columns lands on a non-grid, which is why that looked
   fine in review. */
.mth, .mtr { display: grid; gap: 10px; }
.mth { padding: 12px 18px; border-bottom: 1px solid var(--border-default); background: var(--surface-sunken); font: 600 11px var(--font-mono); letter-spacing: 0.06em; color: var(--text-muted); text-transform: uppercase; }
.ctr { text-align: center; }
.mtr { padding: 9px 18px; border-bottom: 1px solid var(--border-subtle); align-items: center; }
.mtr:nth-child(even) { background: color-mix(in oklch, var(--surface-sunken) 55%, var(--surface-card)); }
.mtr:last-child { border-bottom: none; }
.mtr:hover { background: var(--accent-subtle); }
/* 分层列之间的竖向分隔:没有它,五列 ✓/旗 在一大片留白里对不上列头 */
.cellwrap, .mth .ctr { border-left: 1px solid var(--border-subtle); }
.cap { font: 500 13px var(--font-body); color: var(--text-body); }
.cellwrap { display: flex; justify-content: center; }
.cell { width: 40px; height: 30px; border-radius: 8px; display: flex; align-items: center; justify-content: center;
  font-weight: 700; font-size: 15px; cursor: pointer;
  border: 1px solid var(--border-subtle); background: var(--surface-card); transition: border-color var(--dur-fast) var(--ease-out), box-shadow var(--dur-fast) var(--ease-out); }
.cell:hover { border-color: var(--accent-text); box-shadow: 0 0 0 3px var(--accent-subtle); }
.cell:hover { background: var(--surface-sunken); }
.eyebrow2 { margin-top: 28px; font: 600 11px var(--font-mono); letter-spacing: 0.1em; color: var(--text-muted); text-transform: uppercase; margin-bottom: 12px; }
.menusub { font: 500 12px var(--font-body); color: var(--text-muted); margin-top: -6px; margin-bottom: 12px; }
.menus { border: 1px solid var(--border-default); border-radius: 14px; overflow: hidden; background: var(--surface-card); box-shadow: var(--shadow-xs); display: grid; grid-template-columns: 1fr 1fr; }
.menurow { display: flex; align-items: center; gap: 11px; padding: 13px 18px; border-bottom: 1px solid var(--border-subtle); }
.menurow:nth-child(odd) { border-right: 1px solid var(--border-subtle); }
.menurow:hover { background: color-mix(in oklch, var(--surface-sunken) 60%, var(--surface-card)); }
.menurow:last-child, .menurow:nth-last-child(2):nth-child(odd) { border-bottom: none; }
.menurow .ml { flex: 1; font: 600 13px var(--font-body); color: var(--text-body); }
.rtags { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin-bottom: 4px; padding: 14px 16px; border: 1px solid var(--border-default); border-radius: 14px; background: color-mix(in oklch, var(--surface-sunken) 45%, var(--surface-card)); box-shadow: var(--shadow-xs); }
.rtag { display: inline-flex; height: 26px; align-items: center; padding: 0 12px; border-radius: 999px; background: var(--accent-subtle); color: var(--accent-text); font: 600 12px var(--font-mono); }
.rtagall { display: inline-flex; height: 26px; align-items: center; padding: 0 12px; border-radius: 999px; background: var(--success-subtle); color: var(--success-text); font: 600 12px var(--font-mono); }
.rtedit { display: inline-flex; align-items: center; gap: 6px; height: 26px; padding: 0 12px; border: 1px dashed var(--border-default); border-radius: 999px; color: var(--text-muted); font: 600 12px var(--font-body); cursor: pointer; }
.rtedit:hover { color: var(--accent-text); border-color: var(--accent-subtle-border); }
.members { display: flex; gap: 10px; flex-wrap: wrap; padding: 14px 16px; border: 1px solid var(--border-default); border-radius: 14px; background: color-mix(in oklch, var(--surface-sunken) 45%, var(--surface-card)); box-shadow: var(--shadow-xs); }
.mchip { display: flex; align-items: center; gap: 9px; padding: 7px 8px 7px 7px; border: 1px solid var(--border-default); border-radius: 999px; background: var(--surface-card); }
.mava { width: 28px; height: 28px; border-radius: 50%; background: #232838; display: flex; align-items: center; justify-content: center; font: 600 11px var(--font-body); color: var(--text-muted); }
.mava.first { background: linear-gradient(135deg, #5e83fb, #2dcde6); color: #fff; }
.mchip span { font: 600 12px var(--font-body); color: var(--text-body); }
.mx { width: 18px; height: 18px; display: flex; align-items: center; justify-content: center; border-radius: 50%; cursor: pointer; color: var(--text-faint); }
.mx:hover { background: var(--surface-sunken); color: var(--danger-text); }
.addm { display: flex; align-items: center; gap: 7px; padding: 7px 16px; border: 1px dashed var(--border-default); border-radius: 999px; color: var(--text-muted); font: 600 12px var(--font-body); cursor: pointer; }
.addm:hover { border-color: var(--accent); color: var(--accent-text); }
/* user view */
.userpage { flex: 1; min-height: 0; padding: 24px 28px; }
.uhead { display: flex; align-items: center; margin-bottom: 18px; }
.utitle { font: 700 16px var(--font-display); color: var(--text-strong); }
.usub { font: 500 12px var(--font-body); color: var(--text-muted); margin-top: 3px; }
.uhead :deep(.vbtn) { margin-left: auto; }
.uacts { margin-left: auto; display: flex; gap: 10px; }
.uacts :deep(.vbtn) { margin-left: 0; }
.utable { border: 1px solid var(--border-subtle); border-radius: 14px; overflow: hidden; background: var(--surface-card); }
.uth, .utr { display: grid; grid-template-columns: 1.5fr 1.6fr 2fr 1fr 1fr; gap: 12px; }
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
.stbadge { display: inline-flex; align-items: center; gap: 6px; height: 26px; padding: 0 11px; border-radius: 999px; cursor: pointer; font: 600 11px var(--font-mono); }
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
.ertoggle { display: flex; align-items: center; justify-content: space-between; padding: 12px 14px; border: 1px solid var(--border-subtle); border-radius: 10px; background: var(--surface-sunken); }
.ertoggle .rt { font: 600 13px var(--font-body); color: var(--text-strong); }
.ertoggle .rd { font: 500 11px var(--font-mono); color: var(--text-muted); margin-top: 2px; }
</style>
