<script setup lang="ts">
// 管理后台「在线用户」:此刻谁登录着网关、从哪来、最近一次操作、在哪些实例上开着终端。
//
// 数据来自服务端的在线记录(见 backend/internal/presence):3 分钟内有请求、或者还开着终端的会话
// 算在线;退出登录 / 停用 / 重置口令后立即消失。只记在内存里 —— 网关重启后清空,几十秒内开着的
// 页面再次请求就又回来了。
//
// 「最近操作」与「在线」分开:页面开着就会定时轮询,所以一个挂了一整天没人动的标签页一直"在线";
// 最近操作不算轮询,说的是人还在不在用。超过 10 分钟没操作的标成"空闲"。
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Search, TerminalSquare, RotateCw } from 'lucide-vue-next'
import api from '@/api'
import type { OnlineSession } from '@/types'
import { relTime, absTime, useNow } from '@/lib/relTime'

const { t } = useI18n()
const tt = (k: string, n?: Record<string, unknown>) => t(k, n || {})
const now = useNow(15_000)

const sessions = ref<OnlineSession[]>([])
const users = ref(0)
const terminals = ref(0)
const loaded = ref(false)
const failed = ref('')
const q = ref('')

const IDLE_MS = 10 * 60_000
const idle = (s: OnlineSession) => !s.lastAction || now.value - new Date(s.lastAction).getTime() > IDLE_MS

const shown = computed(() => {
  const k = q.value.trim().toLowerCase()
  if (!k) return sessions.value
  return sessions.value.filter((s) =>
    [s.name, s.email, s.dept, s.ip, ...s.terminals.map((x) => x.instance)].some((v) => (v || '').toLowerCase().includes(k)))
})

async function load() {
  try {
    const r = await api.onlineUsers()
    sessions.value = r.sessions
    users.value = r.users
    terminals.value = r.terminals
    failed.value = ''
  } catch (e: any) {
    failed.value = e?.message || t('loadFailed')
  } finally {
    loaded.value = true
  }
}

// 15 秒刷新一次:在线列表本来就是"此刻",停着不动的列表比没有更误导
let timer: ReturnType<typeof setInterval> | undefined
onMounted(() => { void load(); timer = setInterval(load, 15_000) })
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <div class="olp" data-testid="online-panel">
    <div class="olbar">
      <div class="olsum" data-testid="online-summary">
        <span class="dot" />{{ $t('olSummary', { users, sessions: sessions.length, terminals }) }}
      </div>
      <div class="olsearch"><Search :size="13" /><input v-model="q" :placeholder="$t('olSearchPh')" /></div>
      <button class="olref" :title="$t('olRefresh')" @click="load"><RotateCw :size="13" /></button>
    </div>
    <div class="olnote">{{ $t('olNote') }}</div>
    <div v-if="failed" class="olerr">{{ failed }}</div>
    <div class="olt">
      <div class="olh">
        <span>{{ $t('olColUser') }}</span><span>{{ $t('olColFrom') }}</span><span>{{ $t('olColLogin') }}</span>
        <span>{{ $t('olColAction') }}</span><span>{{ $t('olColTerm') }}</span>
      </div>
      <div v-if="loaded && !shown.length" class="olempty">{{ q ? $t('olEmptyFiltered') : $t('olEmpty') }}</div>
      <div v-for="s in shown" :key="s.userId + '-' + s.loginAt + '-' + s.ip" class="olr" data-testid="online-row">
        <div class="cell user">
          <div class="ava">{{ s.initials || s.name.slice(0, 1) }}</div>
          <div class="nm"><div class="b nm">{{ s.name }}</div><div class="s nm">{{ s.email }}<template v-if="s.dept && s.dept !== '—'"> · {{ s.dept }}</template></div></div>
        </div>
        <div class="cell"><div class="b nm mono">{{ s.ip || '—' }}</div><div class="s nm">{{ s.browser || '—' }}</div></div>
        <div class="cell s" :title="absTime(s.loginAt)">{{ s.loginAt ? relTime(tt, s.loginAt, now) : '—' }}</div>
        <div class="cell" :title="absTime(s.lastAction)">
          <span class="pill" :class="idle(s) ? 'idle' : 'active'" data-testid="online-state">{{ idle(s) ? $t('olIdle') : $t('olActive') }}</span>
          <span class="s">{{ relTime(tt, s.lastAction, now) }}</span>
        </div>
        <div class="cell terms">
          <span v-if="!s.terminals.length" class="s">—</span>
          <span v-for="tm in s.terminals" :key="tm.openedAt + tm.instance" class="term" :class="{ run: tm.running }"
                :title="tm.running ? $t('olTermRunning') : absTime(tm.lastExecAt || tm.openedAt)" data-testid="online-term">
            <TerminalSquare :size="12" />{{ tm.instance || $t('olTermIdle') }}<span v-if="tm.running" class="rdot" />
          </span>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.olp { display: flex; flex-direction: column; gap: 10px; }
.olbar { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.olsum { display: inline-flex; align-items: center; gap: 8px; font: 600 13px var(--font-body); color: var(--text-strong); margin-right: auto; }
.olsum .dot { width: 8px; height: 8px; border-radius: 50%; background: var(--success-text); box-shadow: 0 0 0 3px var(--success-subtle); }
.olsearch { display: flex; align-items: center; gap: 6px; height: 32px; padding: 0 10px; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-card); color: var(--text-faint); }
.olsearch input { border: 0; outline: none; background: transparent; color: var(--text-body); font: 500 12.5px var(--font-body); width: 200px; }
.olref { display: inline-flex; align-items: center; justify-content: center; width: 32px; height: 32px; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-card); color: var(--text-muted); cursor: pointer; }
.olref:hover { color: var(--accent-text); border-color: var(--accent-text); }
.olnote { font: 500 11.5px var(--font-body); color: var(--text-faint); }
.olerr { font: 500 12px var(--font-body); color: var(--danger-text); }
.olt { border: 1px solid var(--border-subtle); border-radius: 12px; overflow: hidden; background: var(--surface-card); }
.olh, .olr { display: grid; grid-template-columns: minmax(200px, 1.6fr) minmax(150px, 1.1fr) 110px 150px minmax(200px, 1.8fr); gap: 12px; align-items: center; padding: 10px 14px; }
.olh { background: var(--surface-sunken); font: 600 11px var(--font-body); color: var(--text-faint); border-bottom: 1px solid var(--border-subtle); }
.olr { border-bottom: 1px solid var(--border-subtle); }
.olr:last-child { border-bottom: 0; }
.cell { min-width: 0; }
.user { display: flex; align-items: center; gap: 10px; }
.ava { flex-shrink: 0; width: 30px; height: 30px; border-radius: 50%; display: flex; align-items: center; justify-content: center; background: var(--accent-subtle); color: var(--accent-text); font: 700 11px var(--font-body); }
.b { font: 600 12.5px var(--font-body); color: var(--text-strong); }
.s { font: 500 11.5px var(--font-body); color: var(--text-muted); }
.mono { font-family: var(--font-mono); }
.nm { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pill { display: inline-block; margin-right: 6px; padding: 1px 8px; border-radius: 999px; font: 600 11px var(--font-body); }
.pill.active { background: var(--success-subtle); color: var(--success-text); }
.pill.idle { background: var(--surface-sunken); color: var(--text-muted); }
.terms { display: flex; flex-wrap: wrap; gap: 6px; }
.term { display: inline-flex; align-items: center; gap: 5px; height: 22px; padding: 0 8px; border-radius: 6px; background: var(--surface-sunken); color: var(--text-body); font: 500 11.5px var(--font-mono); }
.term.run { background: var(--accent-subtle); color: var(--accent-text); }
.rdot { width: 6px; height: 6px; border-radius: 50%; background: currentColor; animation: olpulse 1.2s ease-in-out infinite; }
@keyframes olpulse { 50% { opacity: .25; } }
.olempty { padding: 22px; text-align: center; font: 500 12.5px var(--font-body); color: var(--text-faint); }
@media (max-width: 900px) { .olh { display: none; } .olh, .olr { grid-template-columns: 1fr 1fr; } }
</style>
