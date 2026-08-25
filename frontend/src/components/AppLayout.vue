<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
  Sailboat, SquareTerminal, ClipboardCheck, Database, ShieldAlert, Layers, UsersRound, Rocket, GitBranch, SpellCheck,
  ScrollText, Settings, Activity, Hourglass, Languages, Bell,
  CircleCheck, CircleX, Clock, DatabaseZap, Upload, LogOut, Sun, Moon, ShieldCheck,
} from 'lucide-vue-next'
import { useAuthStore } from '@/stores/auth'
import { useUIStore } from '@/stores/ui'
import api from '@/api'
import MfaModal from '@/components/modals/MfaModal.vue'
import type { Notification } from '@/types'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const ui = useUIStore()
const { t } = useI18n()

const navItems = [
  { key: 'terminal', path: '/terminal', icon: SquareTerminal, label: 'navTerm' },
  // export & uploads reuse the terminal permission (`gate`) rather than own menu keys.
  { key: 'export', gate: 'terminal', path: '/export', icon: DatabaseZap, label: 'navExport' },
  { key: 'uploads', gate: 'terminal', path: '/uploads', icon: Upload, label: 'navUploads' },
  { key: 'asyncjobs', gate: 'terminal', path: '/async-jobs', icon: Clock, label: 'navAsync' },
  // 发布 (CI/CD) — the release list and the flow editor share the pipeline menu.
  { key: 'releases', gate: 'pipeline', path: '/releases', icon: Rocket, label: 'navReleases' },
  { key: 'pipelines', gate: 'pipeline', path: '/pipelines', icon: GitBranch, label: 'navPipelines' },
  // 规范审查 rides the terminal permission: it is a self-check tool first, and a
  // rule library second (editing it is admin-only server-side).
  { key: 'sqlreview', gate: 'terminal', path: '/sql-review', icon: SpellCheck, label: 'navSqlReview' },
  { key: 'approve', path: '/approvals', icon: ClipboardCheck, label: 'navAppr', badge: true },
  { key: 'db', path: '/connections', icon: Database, label: 'navDb' },
  { key: 'rules', path: '/risk-rules', icon: ShieldAlert, label: 'navRules' },
  { key: 'envtier', path: '/env-tiers', icon: Layers, label: 'navEnvTier' },
  { key: 'perms', path: '/permissions', icon: UsersRound, label: 'navPerms' },
  { key: 'audit', path: '/audit', icon: ScrollText, label: 'navAudit' },
  // Settings is a menu item too; the seeded menu grants it to admins only.
  { key: 'settings', path: '/settings', icon: Settings, label: 'navSettings' },
]

const visibleNav = computed(() => navItems.filter((n) => auth.menus[(n as any).gate ?? n.key]))
const activeKey = computed(() => (route.meta.menuKey as string) || '')
// Titles are keyed off the MENU key, not the route name. The two differ for
// several routes (connections→db, approvals→approve, risk-rules→rules,
// permissions→perms), and vue-i18n renders a missing key as the key itself — so
// those four pages showed a raw "t_connections" where their title belongs.
const titleKey = computed(() => (route.meta.menuKey as string) || (route.name as string) || '')
const pageTitle = computed(() => t(`t_${titleKey.value}` as any))
// Views may publish a data-driven subtitle via ui.pageSub; otherwise fall back to i18n default.
const pageSub = computed(() => ui.pageSub || t(`t_${titleKey.value}Sub` as any))
const isDark = computed(() => ui.resolvedTheme() === 'dark')

// Reset the override on navigation so each view starts from its default.
watch(() => route.name, () => { ui.pageSub = '' })

function go(path: string) { router.push(path) }

// ---- notifications ----
const notifs = ref<Notification[]>([])
const notifUnread = ref(0)

// ---- live gateway status (real latency, not hard-coded) ----
const gwOnline = ref(true)
const gwP50 = ref<number | null>(null)
let gwTimer: ReturnType<typeof setInterval> | null = null
async function fetchGwStats() {
  try {
    const s = await api.gatewayStats()
    gwOnline.value = s.online
    gwP50.value = s.p50Ms
  } catch { gwOnline.value = false }
}
const notifOpen = ref(false)
let notifTimer: ReturnType<typeof setInterval> | null = null

const notifIcon = (t: string) =>
  t === 'approval-approved' ? CircleCheck : t === 'approval-rejected' ? CircleX : Clock
const notifCls = (t: string) =>
  t === 'approval-approved' ? 'ok' : t === 'approval-rejected' ? 'bad' : 'warn'

async function fetchNotifs() {
  try {
    const r = await api.notifications()
    notifs.value = r.items
    notifUnread.value = r.unread
  } catch { /* ignore */ }
}

async function toggleNotif() {
  notifOpen.value = !notifOpen.value
  if (notifOpen.value) {
    await fetchNotifs()
    if (notifUnread.value > 0) {
      try { await api.markNotificationsRead() } catch { /* ignore */ }
      notifUnread.value = 0
      notifs.value = notifs.value.map((n) => ({ ...n, read: true }))
    }
  }
}

// ---- second-factor enrolment (available to every account) ----
const mfaOpen = ref(false)
const mfaBound = computed(() => !!auth.me?.mfaEnabled)
function openMfa() {
  userMenuOpen.value = false
  mfaOpen.value = true
}
async function onMfaChanged() {
  mfaOpen.value = false
  await auth.fetchMe() // refresh the bound state shown in the menu
}

function openApprovals() {
  notifOpen.value = false
  if (auth.menus.approve) router.push('/approvals')
}

onMounted(async () => {
  if (auth.menus.approve) {
    try {
      // The listing is paged, so the badge uses the server-side count rather than
      // counting a page — otherwise it would silently cap at the page size.
      const res = await api.approvals('all', 1, 1)
      auth.pendingCount = res.pending
    } catch { /* ignore */ }
  }
  await fetchNotifs()
  notifTimer = setInterval(fetchNotifs, 20000)
  await fetchGwStats()
  gwTimer = setInterval(fetchGwStats, 5000)

  // idle auto-lock: read the policy (admins only can GET /settings; default on)
  try {
    const s = await api.settings()
    idleEnabled = JSON.parse(s.settings?.['security.idleLock'] ?? 'true')
    // Idle timeout is configurable (security.idleMinutes) and capped at the
    // session TTL so the lock never outlives the token itself (L6).
    const mins = Number(JSON.parse(s.settings?.['security.idleMinutes'] ?? '15')) || 15
    const ttlHours = parseInt(String(JSON.parse(s.settings?.['security.sessionTTL'] ?? '"8h"')), 10) || 8
    idleMs = Math.min(mins, ttlHours * 60) * 60 * 1000
  } catch { idleEnabled = true }
  IDLE_EVENTS.forEach((e) => window.addEventListener(e, kickIdle, { passive: true }))
  kickIdle()
})

// ---- idle auto-lock (Session & Security · security.idleLock) ----
// Effective idle timeout (ms); overwritten from settings on mount (L6).
let idleMs = 15 * 60 * 1000
const IDLE_EVENTS = ['mousemove', 'mousedown', 'keydown', 'scroll', 'touchstart'] as const
let idleEnabled = true
let idleTimer: ReturnType<typeof setTimeout> | null = null
let lastKick = 0

function lockSession() {
  teardownIdle()
  auth.logout()
  router.push({ path: '/login', query: { locked: '1' } })
}

// ---- user menu / manual logout ----
const userMenuOpen = ref(false)
function logout() {
  userMenuOpen.value = false
  teardownIdle()
  auth.logout()
  router.push({ name: 'login' })
}
function kickIdle() {
  const now = Date.now()
  if (now - lastKick < 5000) return // throttle: reset at most every 5s
  lastKick = now
  if (idleTimer) clearTimeout(idleTimer)
  if (idleEnabled) idleTimer = setTimeout(lockSession, idleMs)
}
function teardownIdle() {
  if (idleTimer) clearTimeout(idleTimer)
  idleTimer = null
  IDLE_EVENTS.forEach((e) => window.removeEventListener(e, kickIdle))
}

onUnmounted(() => {
  if (notifTimer) clearInterval(notifTimer)
  if (gwTimer) clearInterval(gwTimer)
  teardownIdle()
})
</script>

<template>
  <div class="shell">
    <!-- LEFT RAIL -->
    <aside class="rail">
      <div class="logo"><Sailboat :size="20" color="#fff" /></div>
      <nav class="rail-nav">
        <div
          v-for="n in visibleNav"
          :key="n.key"
          class="rail-item"
          :class="{ active: activeKey === n.key }"
          :title="$t(n.label as any)"
          @click="go(n.path)"
        >
          <component :is="n.icon" :size="21" />
          <span class="rl">{{ $t(n.label as any) }}</span>
          <span v-if="n.badge && auth.pendingCount" class="dot">{{ auth.pendingCount }}</span>
        </div>
      </nav>
      <div class="rail-bottom">
        <div class="avatar" :title="$t('accountMenu')" @click="userMenuOpen = !userMenuOpen">{{ auth.me?.initials || 'LW' }}</div>
        <template v-if="userMenuOpen">
          <div class="um-mask" @click="userMenuOpen = false" />
          <div class="usermenu">
            <div class="um-head">
              <div class="um-ava">{{ auth.me?.initials || 'LW' }}</div>
              <div class="um-id">
                <div class="um-name">{{ auth.me?.name || '—' }}</div>
                <div class="um-mail">{{ auth.me?.email }}</div>
              </div>
            </div>
            <div class="um-role">{{ auth.me?.roleName }}<span v-if="auth.me?.layer"> · {{ auth.me?.layer }}</span></div>
            <div class="um-mfa" @click="openMfa">
              <ShieldCheck :size="15" />{{ $t('mfaSelfTitle') }}
              <span class="um-tag" :class="{ on: mfaBound }">{{ mfaBound ? $t('otpBound') : $t('otpUnboundState') }}</span>
            </div>
            <div class="um-logout" @click="logout"><LogOut :size="15" />{{ $t('logout') }}</div>
          </div>
        </template>
      </div>
    </aside>

    <!-- MAIN -->
    <div class="main">
      <header class="topbar">
        <div class="tb-title">{{ pageTitle }}</div>
        <div class="tb-sub">{{ pageSub }}</div>
        <div class="tb-right">
          <div class="pill" :class="{ off: !gwOnline }">
            <Activity :size="14" :color="gwOnline ? '#2dcde6' : 'var(--danger-text)'" />
            {{ gwOnline ? $t('gwOnline') : $t('gwOffline') }}<span v-if="gwOnline && gwP50 !== null"> · p50 {{ gwP50 }}ms</span>
          </div>
          <div class="pill warn"><Hourglass :size="13" />{{ $t('pending') }} {{ auth.pendingCount }}</div>
          <div class="pill click" :title="$t('langSwitch')" @click="ui.toggleLang()">
            <Languages :size="14" />{{ $t('langLabel') }}
          </div>
          <div class="pill click" :title="$t('themeToggle')" @click="ui.toggleTheme()">
            <component :is="isDark ? Sun : Moon" :size="14" />{{ isDark ? $t('themeLight') : $t('themeDark') }}
          </div>
          <div class="notif">
            <button class="bell" :class="{ active: notifOpen }" :title="$t('notifTitle')" @click="toggleNotif">
              <Bell :size="18" />
              <span v-if="notifUnread" class="nbadge">{{ notifUnread > 99 ? '99+' : notifUnread }}</span>
            </button>
            <template v-if="notifOpen">
              <div class="backdrop" @click="notifOpen = false" />
              <div class="panel">
                <div class="phead">
                  <span>{{ $t('notifTitle') }}</span>
                  <span class="link" @click="openApprovals">{{ $t('notifViewAppr') }}</span>
                </div>
                <div class="plist">
                  <div v-if="!notifs.length" class="pempty">{{ $t('notifEmpty') }}</div>
                  <div v-for="n in notifs" :key="n.id" class="pitem" :class="{ unread: !n.read }">
                    <component :is="notifIcon(n.type)" :size="16" class="pic" :class="notifCls(n.type)" />
                    <div class="pbody">
                      <div class="pt">{{ n.title }}<span v-if="n.refNo" class="pref">{{ n.refNo }}</span></div>
                      <div class="pd">{{ n.body }}</div>
                    </div>
                  </div>
                </div>
              </div>
            </template>
          </div>
        </div>
      </header>
      <main class="content">
        <!-- keep the terminal (its tabs, sessions, sockets) alive across menu switches -->
        <router-view v-slot="{ Component }">
          <keep-alive include="TerminalView">
            <component :is="Component" />
          </keep-alive>
        </router-view>
      </main>
    </div>
  </div>
  <MfaModal :open="mfaOpen" :enabled="mfaBound" @close="mfaOpen = false" @changed="onMfaChanged" />
</template>

<style scoped>
.shell {
  height: 100vh;
  display: flex;
  background: var(--surface-page);
  color: var(--text-body);
  overflow: hidden;
}
/* rail
   84px, not 74: the labels were 9px mono, which is below the size anything is
   readable at — they existed to satisfy the layout, not to be read. At 10.5px
   in the body face they are legible, and the extra 10px is what pays for it.
   The rail also sits on the CARD surface now, so the page's content area is
   the recessed plane and the chrome is the raised one, rather than the other
   way round. */
.rail {
  width: 84px;
  flex-shrink: 0;
  background: var(--surface-card);
  box-shadow: 1px 0 0 var(--border-subtle);
  display: flex;
  flex-direction: column;
  align-items: center;
  padding: 16px 0 14px;
  overflow: hidden;
  transition: width var(--dur-base) var(--ease-out), padding var(--dur-base) var(--ease-out);
}
.logo {
  width: 38px;
  height: 38px;
  border-radius: 11px;
  background: linear-gradient(135deg, var(--azure-500), var(--cyan-400));
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: 0 6px 16px -6px rgba(59, 110, 246, 0.7);
}
/* The nav scrolls, the logo and the account button do not.
   With 14 destinations the rail is taller than a 900px window: it used to just
   run past the bottom edge, taking the ACCOUNT BUTTON with it — the one control
   that holds logout and MFA enrolment. Scrolling the middle keeps both ends
   reachable at any window height. The scrollbar is hidden because the rail is
   84px wide and a 9px gutter would eat a tenth of it; the list still scrolls by
   wheel, trackpad and keyboard. */
.rail-nav {
  margin-top: 20px;
  display: flex;
  flex: 1 1 auto;
  min-height: 0;
  overflow-y: auto;
  flex-direction: column;
  gap: 2px;
  width: 100%;
  align-items: center;
  scrollbar-width: none;
}
.rail-nav::-webkit-scrollbar { display: none; }
.rail-item {
  position: relative;
  width: 68px;
  height: 54px;
  border-radius: var(--radius-md);
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 5px;
  cursor: pointer;
  transition: var(--transition-colors);
  color: var(--text-muted);
  border: none;
}
.rail-item:hover { background: var(--surface-sunken); color: var(--text-body); }
.rail-item:active { transform: scale(0.97); transition: transform var(--dur-instant) var(--ease-out); }
.rail-item.active {
  background: var(--accent-subtle);
  color: var(--accent-text);
}
/* The brand gradient finally does a job: it marks WHERE YOU ARE. Until now the
   azure→cyan pair appeared once, on the logo, and never again. */
.rail-item.active::before {
  content: "";
  position: absolute;
  left: -8px;
  top: 13px;
  bottom: 13px;
  width: 3px;
  border-radius: 0 3px 3px 0;
  background: linear-gradient(180deg, var(--accent), var(--glow-accent));
}
.rl { font: 600 10.5px var(--font-body); letter-spacing: 0.01em; }
.dot {
  position: absolute;
  top: 6px;
  right: 12px;
  min-width: 16px;
  height: 16px;
  padding: 0 4px;
  border-radius: 999px;
  background: var(--danger);
  color: #fff;
  font: 700 9.5px var(--font-mono);
  display: flex;
  align-items: center;
  justify-content: center;
  /* a ring in the rail surface so the badge reads as ON the icon, not beside it */
  box-shadow: 0 0 0 2px var(--surface-card);
}
.rail-bottom {
  margin-top: auto;
  flex-shrink: 0;
  padding-top: 12px;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 14px;
}
.avatar {
  width: 34px;
  height: 34px;
  border-radius: 50%;
  background: linear-gradient(135deg, #5e83fb, #2dcde6);
  display: flex;
  align-items: center;
  justify-content: center;
  font: 600 12px var(--font-body);
  color: #fff;
  cursor: pointer;
  transition: box-shadow var(--dur-fast) var(--ease-out);
}
.avatar:hover { box-shadow: 0 0 0 3px var(--accent-subtle); }
.um-mask { position: fixed; inset: 0; z-index: 400; }
.um-mfa { display: flex; align-items: center; gap: 8px; padding: 10px 14px; cursor: pointer;
  font: 500 13px var(--font-body); color: var(--text-body); border-top: 1px solid var(--border-subtle); }
.um-mfa:hover { background: var(--surface-page); }
.um-tag { margin-left: auto; font: 600 10px var(--font-mono); color: var(--text-faint); }
.um-tag.on { color: var(--success-text); }
.usermenu {
  position: fixed; bottom: 18px; left: 84px; z-index: 401; width: 240px;
  background: var(--surface-overlay, var(--surface-card)); border: 1px solid var(--border-default);
  border-radius: 12px; box-shadow: var(--shadow-xl); padding: 12px;
}
.um-head { display: flex; align-items: center; gap: 10px; }
.um-ava { width: 34px; height: 34px; border-radius: 50%; flex-shrink: 0; background: linear-gradient(135deg, #5e83fb, #2dcde6); display: flex; align-items: center; justify-content: center; font: 600 12px var(--font-body); color: #fff; }
.um-id { min-width: 0; }
.um-name { font: 600 13px var(--font-display); color: var(--text-strong); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.um-mail { font: 500 11px var(--font-mono); color: var(--text-faint); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.um-role { margin: 10px 0; padding: 5px 9px; border-radius: 7px; background: var(--accent-subtle); color: var(--accent-text); font: 600 11px var(--font-mono); display: inline-block; }
.um-logout { display: flex; align-items: center; gap: 8px; padding: 9px 10px; border-radius: 8px; border: 1px solid var(--border-default); color: var(--danger-text); font: 600 12.5px var(--font-body); cursor: pointer; }
.um-logout:hover { background: var(--danger-subtle); border-color: rgba(240, 71, 62, 0.4); }
/* main */
.main { flex: 1; min-width: 0; display: flex; flex-direction: column; }
.topbar {
  display: flex;
  align-items: center;
  gap: 12px;
  height: 56px;
  flex-shrink: 0;
  padding: 0 var(--space-6);
  background: var(--topbar-bg);
  box-shadow: 0 1px 0 var(--border-subtle);
}
.tb-title { font: 700 17px var(--font-display); color: var(--text-strong); letter-spacing: var(--tracking-tight); }
/* The subtitle is PROSE ("一次变更走完一条流水线"), not data — it was set in
   JetBrains Mono, which is why every page header read like a log line. Numbers
   inside it keep the mono face via .v-num. */
.tb-sub { font: 500 12.5px var(--font-body); color: var(--text-muted); }
.tb-right { margin-left: auto; display: flex; align-items: center; gap: 10px; }
/* Status pills are filled, not outlined: an outline on a light page competes
   with the card edges around it, and these are meant to be read at a glance. */
.pill {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  height: 30px;
  padding: 0 12px;
  border: none;
  border-radius: var(--radius-full);
  background: var(--surface-sunken);
  font: 600 11.5px var(--font-body);
  color: var(--text-muted);
  transition: var(--transition-colors);
}
.pill .v-num, .pill .num { font-family: var(--font-mono); font-variant-numeric: tabular-nums; }
.pill.warn { background: var(--warning-subtle); color: var(--warning-text); gap: 6px; padding: 0 11px; font-weight: 600; }
.pill.off { background: var(--danger-subtle); color: var(--danger-text); font-weight: 600; }
.pill.click { color: var(--text-body); cursor: pointer; font-weight: 600; gap: 5px; }
.pill.click:hover { background: var(--border-subtle); }
.content { flex: 1; min-height: 0; display: flex; flex-direction: column; }

/* notifications */
.notif { position: relative; display: flex; }
.bell {
  position: relative; display: flex; align-items: center; justify-content: center;
  width: 32px; height: 32px; border-radius: 9px; border: 1px solid transparent;
  background: transparent; color: var(--text-muted); cursor: pointer; transition: all 0.15s ease;
}
.bell:hover { color: var(--text-body); background: var(--surface-sunken); }
.bell.active { color: var(--accent-text); background: var(--accent-subtle); border-color: var(--accent-subtle-border); }
.nbadge {
  position: absolute; top: -3px; right: -3px; min-width: 16px; height: 16px; padding: 0 4px;
  border-radius: 999px; background: var(--danger); color: #fff; font: 700 9px var(--font-mono);
  display: flex; align-items: center; justify-content: center; border: 2px solid var(--surface-page);
}
.backdrop { position: fixed; inset: 0; z-index: 40; }
.panel {
  position: absolute; top: 42px; right: 0; z-index: 41; width: 340px; max-height: 440px;
  display: flex; flex-direction: column; overflow: hidden;
  background: var(--surface-card); border: 1px solid var(--border-default); border-radius: 12px;
  box-shadow: 0 16px 40px -12px rgba(0, 0, 0, 0.55);
}
.phead {
  display: flex; align-items: center; justify-content: space-between; padding: 12px 14px;
  border-bottom: 1px solid var(--border-subtle); font: 600 13px var(--font-body); color: var(--text-strong);
}
.phead .link { font: 600 11px var(--font-mono); color: var(--accent-text); cursor: pointer; }
.phead .link:hover { text-decoration: underline; }
.plist { overflow-y: auto; }
.pempty { padding: 28px 14px; text-align: center; font: 500 12px var(--font-mono); color: var(--text-faint); }
.pitem { display: flex; gap: 10px; padding: 11px 14px; border-bottom: 1px solid var(--border-subtle); }
.pitem.unread { background: var(--accent-subtle); }
.pic { flex-shrink: 0; margin-top: 1px; }
.pic.ok { color: var(--success); }
.pic.bad { color: var(--danger); }
.pic.warn { color: var(--warning); }
.pbody { min-width: 0; }
.pt { font: 600 12px var(--font-body); color: var(--text-strong); display: flex; align-items: center; gap: 7px; }
.pref { font: 600 10px var(--font-mono); color: #8facff; }
.pd { margin-top: 3px; font: 400 12px/1.5 var(--font-body); color: var(--text-muted); word-break: break-word; }
</style>
