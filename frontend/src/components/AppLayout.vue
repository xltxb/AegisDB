<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
  Sailboat, SquareTerminal, ClipboardCheck, Database, ShieldAlert, UsersRound,
  ScrollText, Settings, Activity, Hourglass, Languages, Bell,
  CircleCheck, CircleX, Clock, DatabaseZap, Upload, LogOut, Sun, Moon,
} from 'lucide-vue-next'
import { useAuthStore } from '@/stores/auth'
import { useUIStore } from '@/stores/ui'
import api from '@/api'
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
  { key: 'approve', path: '/approvals', icon: ClipboardCheck, label: 'navAppr', badge: true },
  { key: 'db', path: '/connections', icon: Database, label: 'navDb' },
  { key: 'rules', path: '/risk-rules', icon: ShieldAlert, label: 'navRules' },
  { key: 'perms', path: '/permissions', icon: UsersRound, label: 'navPerms' },
  { key: 'audit', path: '/audit', icon: ScrollText, label: 'navAudit' },
  // Settings is a menu item too; the seeded menu grants it to admins only.
  { key: 'settings', path: '/settings', icon: Settings, label: 'navSettings' },
]

const visibleNav = computed(() => navItems.filter((n) => auth.menus[(n as any).gate ?? n.key]))
const activeKey = computed(() => (route.meta.menuKey as string) || '')
const pageTitle = computed(() => t(`t_${route.name as string}` as any))
// Views may publish a data-driven subtitle via ui.pageSub; otherwise fall back to i18n default.
const pageSub = computed(() => ui.pageSub || t(`t_${route.name as string}Sub` as any))
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

function openApprovals() {
  notifOpen.value = false
  if (auth.menus.approve) router.push('/approvals')
}

onMounted(async () => {
  if (auth.menus.approve) {
    try {
      const list = await api.approvals('all')
      auth.pendingCount = list.filter((a) => a.status === 'pending').length
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
          <div class="pill click" :title="'中 / EN'" @click="ui.toggleLang()">
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
</template>

<style scoped>
.shell {
  height: 100vh;
  display: flex;
  background: var(--surface-page);
  color: var(--text-body);
  overflow: hidden;
}
/* rail */
.rail {
  width: 74px;
  flex-shrink: 0;
  border-right: 1px solid var(--border-subtle);
  background: var(--surface-sunken);
  display: flex;
  flex-direction: column;
  align-items: center;
  padding: 14px 0;
  overflow: hidden;
  transition: width var(--dur-med, 0.2s) var(--ease-out, ease), padding var(--dur-med, 0.2s) var(--ease-out, ease);
}
.logo {
  width: 36px;
  height: 36px;
  border-radius: 10px;
  background: linear-gradient(135deg, #3b6ef6, #2dcde6);
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: 0 0 18px -4px rgba(45, 205, 230, 0.6);
}
.rail-nav {
  margin-top: 22px;
  display: flex;
  flex-direction: column;
  gap: 6px;
  width: 100%;
  align-items: center;
}
.rail-item {
  position: relative;
  width: 58px;
  height: 52px;
  border-radius: 12px;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 4px;
  cursor: pointer;
  transition: all 0.15s ease;
  color: var(--text-muted);
  border: 1px solid transparent;
}
.rail-item:hover { color: var(--text-body); }
.rail-item.active {
  background: var(--accent-subtle);
  color: var(--accent-text);
  border-color: var(--accent-subtle-border);
}
.rl { font: 600 9px var(--font-mono); }
.dot {
  position: absolute;
  top: 5px;
  right: 9px;
  min-width: 15px;
  height: 15px;
  padding: 0 3px;
  border-radius: 999px;
  background: var(--danger);
  color: #fff;
  font: 700 9px var(--font-mono);
  display: flex;
  align-items: center;
  justify-content: center;
}
.rail-bottom {
  margin-top: auto;
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
.usermenu {
  position: fixed; bottom: 18px; left: 74px; z-index: 401; width: 240px;
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
  gap: 14px;
  height: 54px;
  flex-shrink: 0;
  padding: 0 22px;
  border-bottom: 1px solid var(--border-subtle);
  background: var(--topbar-bg);
  backdrop-filter: blur(8px);
}
.tb-title { font: 700 16px var(--font-display); color: var(--text-strong); letter-spacing: -0.01em; }
.tb-sub { font: 500 12px var(--font-mono); color: var(--text-muted); }
.tb-right { margin-left: auto; display: flex; align-items: center; gap: 14px; }
.pill {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  height: 30px;
  padding: 0 12px;
  border: 1px solid var(--border-subtle);
  border-radius: 999px;
  font: 500 12px var(--font-mono);
  color: var(--text-muted);
}
.pill.warn { background: var(--warning-subtle); color: var(--warning-text); border: none; gap: 6px; padding: 0 11px; font-weight: 600; }
.pill.off { background: var(--danger-subtle); color: var(--danger-text); border: none; font-weight: 600; }
.pill.click { border-color: var(--border-default); color: var(--text-body); cursor: pointer; font-weight: 600; gap: 5px; }
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
