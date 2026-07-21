<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Tag } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSelect from '@/components/common/VSelect.vue'
import TagEditModal from '@/components/modals/TagEditModal.vue'
import api from '@/api'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import type { Connection } from '@/types'

const auth = useAuthStore()
// Instance configuration is platform-admin only (backend enforces it too).
const isAdmin = computed(() => auth.me?.roleCodes?.includes('admin') ?? (auth.me?.roleCode === 'admin'))

const { t } = useI18n()
const ui = useUIStore()

const conns = ref<Connection[]>([])
const saved = ref(false)
const allTags = ref<string[]>([])
const tagModal = ref<{ open: boolean; conn: Connection | null }>({ open: false, conn: null })
const tagArr = (s: string) => (s ? s.split(',').map((x) => x.trim()).filter(Boolean) : [])
function openTagEdit(c: Connection) { if (!isAdmin.value) return; tagModal.value = { open: true, conn: c } }
async function saveTags(tags: string[]) {
  // M14: 保存标签失败以 toast 呈现
  try {
    if (tagModal.value.conn) await api.setConnectionTags(tagModal.value.conn.id, tags.join(','))
    tagModal.value.open = false
    await load()
    allTags.value = await api.tags()
  } catch (e) {
    ui.notifyError(e, '操作失败')
  }
}

const envOpts = [
  { label: 'PROD · L1 核心', env: 'prod' },
  { label: 'STAGING · L3 预发', env: 'staging' },
  { label: 'DEV · L4 沙盒', env: 'dev' },
]
const engineOpts = ['MySQL 8.0', 'TiDB', 'GaussDB (DWS)', 'Oracle', 'PostgreSQL 15', 'ClickHouse', 'Redis 7']
const policyOpts = ['strict', 'approve-1', 'audit-only']

const draft = ref({ name: '', host: '', engine: 'MySQL 8.0', envLabel: 'PROD · L1 核心', policy: 'strict', username: '', password: '', database: '' })

const prod = computed(() => conns.value.filter((c) => c.env === 'prod'))
const staging = computed(() => conns.value.filter((c) => c.env === 'staging'))
const dev = computed(() => conns.value.filter((c) => c.env === 'dev'))

async function load() {
  // M14: 加载失败以 toast 呈现，避免静默失败
  try {
    conns.value = await api.connections()
    ui.pageSub = t('subDb', { n: conns.value.length })
  } catch (e) {
    ui.notifyError(e, '加载失败')
  }
}
onMounted(async () => { await load(); try { allTags.value = await api.tags() } catch { /* ignore */ } })

function polMeta(p: string) {
  if (p === 'strict') return { bg: 'var(--danger-subtle)', c: 'var(--danger-text)' }
  if (p === 'audit-only') return { bg: 'var(--surface-sunken)', c: 'var(--text-muted)' }
  return { bg: 'var(--warning-subtle)', c: 'var(--warning-text)' }
}
function roleColor(r: string) { return r.startsWith('dba') ? '#8facff' : 'var(--text-muted)' }
function stMeta(s: string) {
  return s === 'online'
    ? { bg: 'var(--success-subtle)', c: 'var(--success-text)' }
    : { bg: 'var(--warning-subtle)', c: 'var(--warning-text)' }
}

async function toggle(c: Connection) {
  if (!isAdmin.value) return
  // M14: 切换状态失败以 toast 呈现
  try {
    await api.toggleConnection(c.id)
    await load()
  } catch (e) {
    ui.notifyError(e, '操作失败')
  }
}

async function add() {
  if (!draft.value.name.trim() || !draft.value.host.trim()) return
  const env = envOpts.find((o) => o.label === draft.value.envLabel)?.env || 'prod'
  // M14: 创建连接失败以 toast 呈现
  try {
    // "测试连接并保存" (FR-CONN-02): create then test-attach to the gateway.
    const created = await api.createConnection({
      name: draft.value.name.trim(), engine: draft.value.engine,
      host: draft.value.host.trim(), env: env as any, policy: draft.value.policy,
      username: draft.value.username.trim(), password: draft.value.password, database: draft.value.database.trim(),
    })
    try { await api.testConnection(created.id) } catch { /* test is best-effort */ }
    draft.value = { name: '', host: '', engine: 'MySQL 8.0', envLabel: 'PROD · L1 核心', policy: 'strict', username: '', password: '', database: '' }
    saved.value = true
    setTimeout(() => (saved.value = false), 2600)
    await load()
  } catch (e) {
    ui.notifyError(e, '操作失败')
  }
}
</script>

<template>
  <div class="scy page">
    <div class="head">
      <div>
        <div class="eyebrow">CONNECTIONS</div>
        <div class="sub">{{ conns.length }} {{ $t('connSub') }}</div>
      </div>
      <div class="acts">
        <span v-if="!isAdmin" class="roflag">{{ $t('readOnlyPerms') }}</span>
        <template v-else>
          <VButton variant="secondary">{{ $t('importInst') }}</VButton>
          <VButton variant="primary">{{ $t('newConn') }}</VButton>
        </template>
      </div>
    </div>

    <div class="table">
      <div class="thead">
        <span>{{ $t('colInst') }}</span><span>{{ $t('colEngine') }}</span><span>{{ $t('colAddr') }}</span>
        <span>{{ $t('colRole') }}</span><span>{{ $t('colPolicy') }}</span><span>{{ $t('colStatus') }}</span>
      </div>

      <template v-for="(group, gi) in [{ rows: prod, cls: 'danger', label: 'prodRow' }, { rows: staging, cls: 'warn', label: 'stgRow' }, { rows: dev, cls: 'success', label: 'devRow' }]" :key="gi">
        <div class="grouprow" :class="group.cls"><span class="d" />{{ $t(group.label as any) }}</div>
        <div v-for="c in group.rows" :key="c.id" class="trow">
          <div>
            <div class="cn">{{ c.name }}</div>
            <div class="cl">{{ c.layer }}</div>
            <div class="tags">
              <span class="rbadge" :class="c.username ? 'real' : 'sim'">{{ c.username ? $t('connReal') : $t('connSim') }}</span>
              <span v-for="tg in tagArr(c.tags)" :key="tg" class="tchip">{{ tg }}</span>
              <span v-if="isAdmin" class="tedit" @click="openTagEdit(c)"><Tag :size="10" />{{ tagArr(c.tags).length ? $t('edit') : $t('tagAdd') }}</span>
            </div>
          </div>
          <div class="mono">{{ c.engine }}</div>
          <div class="mono mute">{{ c.host }}:{{ c.port }}</div>
          <div class="mono" :style="{ color: roleColor(c.defaultRole) }">{{ c.defaultRole }}</div>
          <div><span class="pill" :style="{ background: polMeta(c.policy).bg, color: polMeta(c.policy).c }">{{ c.policy }}</span></div>
          <div>
            <span class="pill" :class="{ click: isAdmin }" :style="{ background: stMeta(c.status).bg, color: stMeta(c.status).c }" :title="isAdmin ? '切换状态' : ''" @click="toggle(c)">
              <span class="dotc" />{{ c.status === 'online' ? $t('online') : $t('maint') }}
            </span>
          </div>
        </div>
      </template>
    </div>

    <div v-if="isAdmin" class="form">
      <div class="ftitle">{{ $t('formNewConn') }}<span v-if="saved" class="savetag">{{ $t('connSaved') }}</span></div>
      <div class="fsub">{{ $t('formNewConnSub') }}</div>
      <div class="fgrid">
        <div><div class="fl">{{ $t('fName') }}</div><input v-model="draft.name" placeholder="order-cluster-2" /></div>
        <div><div class="fl">{{ $t('fEngine') }}</div><VSelect v-model="draft.engine" :options="engineOpts" /></div>
        <div><div class="fl">{{ $t('fEnv') }}</div><VSelect v-model="draft.envLabel" :options="envOpts.map((o) => o.label)" /></div>
        <div><div class="fl">{{ $t('fAddr') }}</div><input v-model="draft.host" placeholder="10.20.3.12:3306" /></div>
        <div><div class="fl">{{ $t('fPolicy') }}</div><VSelect v-model="draft.policy" :options="policyOpts" /></div>
        <div><div class="fl">{{ $t('fDatabase') }}</div><input v-model="draft.database" placeholder="orders_db" /></div>
        <div><div class="fl">{{ $t('fUser') }}</div><input v-model="draft.username" placeholder="app_ro" /></div>
        <div><div class="fl">{{ $t('fPassword') }}</div><input v-model="draft.password" type="password" placeholder="••••••" /></div>
      </div>
      <div class="credhint">{{ $t('connCredHint') }}</div>
      <div class="fsaverow"><VButton variant="primary" @click="add">{{ $t('fSave') }}</VButton></div>
    </div>

    <TagEditModal
      :open="tagModal.open" :title="$t('tagConnTitle')" :subtitle="tagModal.conn ? tagModal.conn.name : ''"
      :tags="tagArr(tagModal.conn?.tags || '')" :suggestions="allTags"
      @close="tagModal.open = false" @save="saveTags"
    />
  </div>
</template>

<style scoped>
.page { flex: 1; min-height: 0; padding: 24px 28px; }
.head { display: flex; align-items: center; margin-bottom: 18px; }
.eyebrow { font: 500 11px var(--font-mono); letter-spacing: 0.12em; color: var(--text-faint); text-transform: uppercase; }
.sub { font: 500 13px var(--font-body); color: var(--text-muted); margin-top: 4px; }
.acts { margin-left: auto; display: flex; gap: 10px; align-items: center; }
.roflag { display: inline-flex; align-items: center; height: 26px; padding: 0 11px; border-radius: 999px; background: var(--surface-sunken); border: 1px solid var(--border-subtle); font: 600 11px var(--font-mono); color: var(--text-muted); }
.table { border: 1px solid var(--border-subtle); border-radius: 14px; overflow: hidden; background: var(--surface-card); }
.thead, .trow { display: grid; grid-template-columns: 1.5fr 1fr 1.7fr 1fr 1fr 0.9fr; gap: 12px; }
.thead { padding: 12px 18px; border-bottom: 1px solid var(--border-subtle); background: var(--surface-sunken); font: 600 11px var(--font-mono); letter-spacing: 0.06em; color: var(--text-faint); text-transform: uppercase; }
.trow { padding: 14px 18px; border-bottom: 1px solid var(--border-subtle); align-items: center; }
.grouprow { padding: 9px 18px; display: flex; align-items: center; gap: 8px; font: 600 11px var(--font-mono); }
.grouprow.danger { background: rgba(240, 71, 62, 0.06); color: var(--danger-text); }
.grouprow.warn { background: rgba(245, 165, 36, 0.06); color: var(--warning-text); }
.grouprow.success { background: rgba(24, 179, 104, 0.06); color: var(--success-text); }
.grouprow .d { width: 6px; height: 6px; border-radius: 50%; background: currentColor; }
.cn { font: 600 13px var(--font-body); color: var(--text-strong); }
.cl { font: 500 11px var(--font-mono); color: var(--text-faint); }
.tags { margin-top: 6px; display: flex; flex-wrap: wrap; align-items: center; gap: 5px; }
.tchip { display: inline-flex; height: 19px; align-items: center; padding: 0 8px; border-radius: 999px; background: var(--accent-subtle); color: var(--accent-text); font: 600 10px var(--font-mono); }
.rbadge { display: inline-flex; height: 19px; align-items: center; padding: 0 8px; border-radius: 999px; font: 700 10px var(--font-mono); }
.rbadge.real { background: var(--success-subtle); color: var(--success-text); }
.rbadge.sim { background: var(--surface-sunken); color: var(--text-faint); border: 1px solid var(--border-subtle); }
.tedit { display: inline-flex; align-items: center; gap: 3px; height: 19px; padding: 0 7px; border-radius: 999px; border: 1px dashed var(--border-default); color: var(--text-faint); font: 600 10px var(--font-mono); cursor: pointer; }
.tedit:hover { color: var(--accent-text); border-color: var(--accent-subtle-border); }
.mono { font: 500 12px var(--font-mono); color: var(--text-body); }
.mono.mute { color: var(--text-muted); }
.pill { display: inline-flex; align-items: center; gap: 5px; height: 22px; padding: 0 9px; border-radius: 999px; font: 600 11px var(--font-mono); }
.pill.click { cursor: pointer; }
.dotc { width: 5px; height: 5px; border-radius: 50%; background: currentColor; }
.form { margin-top: 20px; border: 1px solid var(--border-subtle); border-radius: 14px; background: var(--surface-card); padding: 20px 22px; }
.ftitle { font: 600 14px var(--font-display); color: var(--text-strong); display: flex; align-items: center; gap: 10px; }
.savetag { font: 600 12px var(--font-mono); color: var(--success-text); }
.fsub { font: 500 12px var(--font-body); color: var(--text-muted); margin: 4px 0 16px; }
.fgrid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 16px; }
.fl { font: 500 11px var(--font-body); color: var(--text-faint); margin-bottom: 6px; }
.fgrid input { width: 100%; box-sizing: border-box; height: 40px; border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-sunken); padding: 0 12px; font: 400 13px var(--font-mono); color: var(--text-body); outline: none; }
.fgrid input:focus { border-color: var(--accent-text); }
.credhint { margin-top: 14px; font: 500 11.5px var(--font-mono); color: var(--text-faint); }
.fsaverow { margin-top: 14px; display: flex; justify-content: flex-end; }
</style>
