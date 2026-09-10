<script setup lang="ts">
// 开放接口凭据 —— 外部系统(DevOps 平台 / CI)提交 SQL 升级单用的 Key/Secret。
//
// The panel is built around one fact: the secret exists exactly once, in the
// create response. So the flow is create → copy → gone, and everything else on
// this screen is about the credential's identity (which service account it acts
// as, what it may do, where it may call from) rather than about the secret.
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { KeyRound, Plus, Copy, Check, Trash2, X, TriangleAlert, Bot } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSwitch from '@/components/common/VSwitch.vue'
import VSelect from '@/components/common/VSelect.vue'
import api from '@/api'
import { confirmAction } from '@/lib/confirm'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import type { APIClient, Pipeline, RoleBrief, ServiceAccount, UserView } from '@/types'

const { t } = useI18n()
const ui = useUIStore()
const auth = useAuthStore()
const isAdmin = computed(() => auth.me?.roleCode === 'admin' || (auth.me?.roleCodes || []).includes('admin'))

const clients = ref<APIClient[]>([])
const users = ref<UserView[]>([])
// 服务账号 —— 凭据背后的机器主体。凭据应绑定它,而不是某个人的账号:人会离职、
// 会换角色,绑在人身上的集成会跟着一起断。
const svcAccounts = ref<ServiceAccount[]>([])
const roles = ref<RoleBrief[]>([])
const saOpen = ref(false)
const sf = ref({ name: '', roleIds: [] as number[], tags: '' })
const formOpen = ref(false)
const nf = ref({ name: '', userId: 0, allowIps: '', scopes: ['release:create', 'release:read', 'review:check'], pipelineId: 0 })
// 发布流程绑定在凭据上(网关侧策略),外部请求不可指定 —— 0 = 走目标分层的默认流程。
const pipelines = ref<Pipeline[]>([])
// The one plaintext copy of a freshly-issued credential. Held in memory only,
// shown until dismissed, never fetched again — there is nothing to fetch.
const issued = ref<{ name: string; token: string } | null>(null)
const copied = ref(false)

const SCOPES = ['release:create', 'release:read', 'review:check']

// 服务账号排最前并带标记;人类账号仍可绑(兼容既有凭据),但不是推荐姿势。
const principalLabel = (u: UserView) =>
  u.kind === 'service' ? `🤖 ${u.name} · ${t('saMark')}` : `${u.name} · ${u.email}`
const principals = computed(() =>
  [...users.value].sort((a, b) => (a.kind === 'service' ? 0 : 1) - (b.kind === 'service' ? 0 : 1)))
const userLabels = computed(() => principals.value.map(principalLabel))
const pipeLabel = computed({
  get: () => {
    const p = pipelines.value.find(x => x.id === nf.value.pipelineId)
    return p ? p.name : t('acPipeDefault')
  },
  set: (l: string) => {
    const p = pipelines.value.find(x => x.name === l)
    nf.value.pipelineId = p ? p.id : 0
  },
})
const pipeOptions = computed(() => [t('acPipeDefault'), ...pipelines.value.map(p => p.name)])
const pipeNameOf = (id: number) => pipelines.value.find(p => p.id === id)?.name || ''
const userLabel = computed({
  get: () => {
    const u = users.value.find(x => x.id === nf.value.userId)
    return u ? principalLabel(u) : ''
  },
  set: (l: string) => {
    const u = users.value.find(x => principalLabel(x) === l)
    nf.value.userId = u ? u.id : 0
  },
})

async function load() {
  try { clients.value = await api.apiClients() } catch (e) { ui.notifyError(e, t('loadFailed')) }
  try { users.value = await api.users() } catch { /* the picker degrades to empty */ }
  try { svcAccounts.value = await api.serviceAccounts() } catch { /* section degrades to empty */ }
  try { roles.value = await api.roles() } catch { /* role picker degrades */ }
  try { pipelines.value = (await api.pipelines()).filter(p => p.enabled) } catch { /* picker degrades */ }
}

function toggleRole(id: number) {
  const i = sf.value.roleIds.indexOf(id)
  if (i >= 0) sf.value.roleIds.splice(i, 1)
  else sf.value.roleIds.push(id)
}

async function createSA() {
  if (!sf.value.name.trim() || !sf.value.roleIds.length) { ui.notify(t('saNeedFields'), 'error'); return }
  try {
    const env = await api.createServiceAccount({
      name: sf.value.name.trim(), roleIds: sf.value.roleIds,
      tags: sf.value.tags.split(',').map(x => x.trim()).filter(Boolean),
    })
    if (env.code !== 0) { ui.notifyError(new Error(env.msg), t('actionFailed')); return }
    saOpen.value = false
    sf.value = { name: '', roleIds: [], tags: '' }
    await load()
    // 建完主体顺手打开发凭据的表单 —— 这两步几乎总是连着做的。
    const created = users.value.find(u => u.kind === 'service' && env.data && u.id === env.data.id)
    openForm()
    if (created) nf.value.userId = created.id
  } catch (e) { ui.notifyError(e, t('actionFailed')) }
}
onMounted(load)

function openForm() {
  nf.value = { name: '', userId: 0, allowIps: '', scopes: [...SCOPES], pipelineId: 0 }
  issued.value = null
  formOpen.value = true
}

function toggleScope(s: string) {
  const i = nf.value.scopes.indexOf(s)
  if (i >= 0) nf.value.scopes.splice(i, 1)
  else nf.value.scopes.push(s)
}

async function create() {
  if (!nf.value.name.trim() || !nf.value.userId) { ui.notify(t('acNeedFields'), 'error'); return }
  try {
    const env = await api.createApiClient({
      name: nf.value.name.trim(), userId: nf.value.userId,
      allowIps: nf.value.allowIps.trim(), scopes: nf.value.scopes, pipelineId: nf.value.pipelineId, enabled: true,
    })
    if (env.code !== 0) { ui.notifyError(new Error(env.msg), t('actionFailed')); return }
    issued.value = { name: env.data.client.name, token: env.data.token }
    formOpen.value = false
    copied.value = false
    await load()
  } catch (e) { ui.notifyError(e, t('actionFailed')) }
}

async function copyToken() {
  if (!issued.value) return
  try {
    await navigator.clipboard.writeText(issued.value.token)
    copied.value = true
  } catch { ui.notify(t('acCopyFailed'), 'error') }
}

async function toggle(c: APIClient) {
  if (!isAdmin.value) return
  try {
    const env = await api.updateApiClient(c.id, { enabled: !c.enabled })
    if (env.code !== 0) { ui.notifyError(new Error(env.msg), t('actionFailed')); return }
    c.enabled = !c.enabled
  } catch (e) { ui.notifyError(e, t('actionFailed')) }
}

async function remove(c: APIClient) {
  if (!isAdmin.value) return
  if (!confirmAction(t('acDelConfirm', { name: c.name }))) return
  try {
    const env = await api.deleteApiClient(c.id)
    if (env.code !== 0) { ui.notifyError(new Error(env.msg), t('actionFailed')); return }
    clients.value = clients.value.filter(x => x.id !== c.id)
  } catch (e) { ui.notifyError(e, t('actionFailed')) }
}

function fmt(s: string | null) { return s ? new Date(s).toLocaleString('en-GB') : '—' }

// A copy-paste starting point. The base URL is this console's own origin, which
// is the one an integrator is looking at right now.
const sample = computed(() => {
  const base = window.location.origin
  return `curl -X POST ${base}/api/v1/open/releases \\
  -H "Authorization: Bearer <key>.<secret>" \\
  -H "Content-Type: application/json" \\
  -d '{"title":"订单表加字段","externalRef":"CHG-1001","instance":"order-cluster","database":"orders","sql":"ALTER TABLE ...;","reason":"需求 #123"}'`
})
</script>

<template>
  <div class="ac">
    <div class="achead">
      <div class="acic"><KeyRound :size="18" color="var(--accent-text)" /></div>
      <div class="grow">
        <div class="act">{{ $t('acTitle') }}</div>
        <div class="acs">{{ $t('acSub') }}</div>
      </div>
      <VButton v-if="isAdmin" variant="primary" height="34px" @click="openForm"><Plus :size="14" />{{ $t('acNew') }}</VButton>
    </div>

    <!-- one-time secret -->
    <div v-if="issued" class="issued">
      <div class="ihead">
        <TriangleAlert :size="15" />
        <span class="it">{{ $t('acIssued', { name: issued.name }) }}</span>
        <X :size="15" class="ix" @click="issued = null" />
      </div>
      <div class="ibody">
        <code class="tok">{{ issued.token }}</code>
        <VButton height="32px" @click="copyToken">
          <component :is="copied ? Check : Copy" :size="13" />{{ copied ? $t('acCopied') : $t('acCopy') }}
        </VButton>
      </div>
      <div class="ihint">{{ $t('acIssuedHint') }}</div>
    </div>

    <!-- 服务账号:凭据背后的机器主体 -->
    <div class="sablk">
      <div class="sahead">
        <Bot :size="14" /><span class="sat">{{ $t('saTitle') }}</span>
        <span class="sasub">{{ $t('saSub') }}</span>
        <VButton v-if="isAdmin" height="28px" @click="saOpen = !saOpen"><Plus :size="12" />{{ $t('saNew') }}</VButton>
      </div>
      <div v-if="saOpen" class="saform">
        <input v-model="sf.name" class="in" :placeholder="$t('saNamePh')" />
        <div class="scopes">
          <span v-for="ro in roles" :key="ro.id" class="sc" :class="{ on: sf.roleIds.includes(ro.id) }" @click="toggleRole(ro.id)">
            <Check v-if="sf.roleIds.includes(ro.id)" :size="11" />{{ ro.name }}
          </span>
        </div>
        <input v-model="sf.tags" class="in" :placeholder="$t('saTagsPh')" />
        <div class="sarow">
          <span class="hint">{{ $t('saFormHint') }}</span>
          <VButton variant="primary" height="30px" @click="createSA">{{ $t('saCreate') }}</VButton>
        </div>
      </div>
      <div v-for="sa in svcAccounts" :key="sa.id" class="sarowi" :class="{ off: sa.status !== 'active' }">
        <Bot :size="13" class="saic" />
        <span class="san">{{ sa.name }}</span>
        <code class="ikey">{{ sa.email }}</code>
        <span class="sam">{{ sa.roles.join(' / ') }}<template v-if="sa.tags.length"> · {{ sa.tags.join(',') }}</template></span>
        <span class="sam right">{{ $t('saClients', { n: sa.clients }) }}</span>
      </div>
      <div v-if="!svcAccounts.length" class="saempty">{{ $t('saEmpty') }}</div>
    </div>

    <div class="list">
      <div v-for="c in clients" :key="c.id" class="item" :class="{ off: !c.enabled }">
        <div class="grow">
          <div class="itop">
            <span class="iname">{{ c.name }}</span>
            <code class="ikey">{{ c.key }}</code>
          </div>
          <div class="imeta">
            <span>{{ $t('acAccount') }}: {{ c.userName }}</span>
            <span class="sep">·</span>
            <span>{{ c.scopes }}</span>
            <span class="sep">·</span>
            <span>{{ c.pipelineId ? pipeNameOf(c.pipelineId) : $t('acPipeDefault') }}</span>
            <span class="sep">·</span>
            <span>{{ c.allowIps ? c.allowIps : $t('acAnyIP') }}</span>
            <span class="sep">·</span>
            <span>{{ $t('acLastUsed') }}: {{ fmt(c.lastUsedAt) }}</span>
          </div>
        </div>
        <VSwitch :model-value="c.enabled" :disabled="!isAdmin" @update:model-value="toggle(c)" />
        <span v-if="isAdmin" class="del" @click="remove(c)"><Trash2 :size="13" /></span>
      </div>
      <div v-if="!clients.length" class="empty">{{ $t('acEmpty') }}</div>
    </div>

    <div class="sample">
      <div class="fl">{{ $t('acSample') }}</div>
      <pre class="code">{{ sample }}</pre>
      <div class="ihint">{{ $t('acSampleHint') }}</div>
    </div>

    <!-- create form -->
    <div v-if="formOpen" class="overlay">
      <div class="mask" @click="formOpen = false" />
      <div class="modal">
        <div class="mhead">
          <div class="mic"><KeyRound :size="17" color="var(--accent-text)" /></div>
          <div><div class="mt">{{ $t('acNew') }}</div><div class="ms">{{ $t('acNewSub') }}</div></div>
          <X :size="17" class="mx" @click="formOpen = false" />
        </div>
        <div class="mbody">
          <div><div class="fl">{{ $t('acName') }}</div><input v-model="nf.name" class="in" :placeholder="$t('acNamePh')" /></div>
          <div>
            <div class="fl">{{ $t('acAccount') }}</div>
            <VSelect v-model="userLabel" :options="userLabels" searchable />
            <div class="hint">{{ $t('acAccountHint') }}</div>
          </div>
          <div>
            <div class="fl">{{ $t('acScopes') }}</div>
            <div class="scopes">
              <span v-for="s in SCOPES" :key="s" class="sc" :class="{ on: nf.scopes.includes(s) }" @click="toggleScope(s)">
                <Check v-if="nf.scopes.includes(s)" :size="11" />{{ s }}
              </span>
            </div>
          </div>
          <div>
            <div class="fl">{{ $t('acPipeline') }}</div>
            <VSelect v-model="pipeLabel" :options="pipeOptions" />
            <div class="hint">{{ $t('acPipelineHint') }}</div>
          </div>
          <div>
            <div class="fl">{{ $t('acAllowIPs') }}</div>
            <input v-model="nf.allowIps" class="in" placeholder="10.0.0.0/8, 203.0.113.7" />
            <div class="hint">{{ $t('acAllowIPsHint') }}</div>
          </div>
        </div>
        <div class="mfoot">
          <VButton @click="formOpen = false">{{ $t('btnCancel') }}</VButton>
          <VButton variant="primary" @click="create">{{ $t('acCreate') }}</VButton>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.ac { border: none; border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-xs); overflow: hidden; }
.achead { display: flex; align-items: center; gap: 12px; padding: 16px 20px; border-bottom: 1px solid var(--border-subtle); }
.acic { width: 34px; height: 34px; border-radius: 10px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; }
.grow { flex: 1; min-width: 0; }
.act { font: 600 14px var(--font-display); color: var(--text-strong); }
.acs { font: 500 12px var(--font-body); color: var(--text-muted); margin-top: 2px; }
.issued { margin: 14px 20px 0; border: 1px solid var(--warning-text); border-radius: 12px; background: var(--warning-subtle); padding: 12px 14px; }
.ihead { display: flex; align-items: center; gap: 8px; color: var(--warning-text); }
.it { font: 600 12.5px var(--font-body); flex: 1; }
.ix { cursor: pointer; }
.ibody { display: flex; align-items: center; gap: 10px; margin-top: 9px; }
.tok { flex: 1; min-width: 0; overflow-x: auto; white-space: nowrap; padding: 8px 10px; border-radius: 8px; background: var(--surface-card); border: 1px solid var(--border-default); font: 500 11.5px var(--font-mono); color: var(--text-strong); }
.ihint { margin-top: 7px; font: 500 11px var(--font-body); color: var(--text-muted); }
.sablk { margin: 14px 20px 0; border: 1px solid var(--border-subtle); border-radius: 12px; background: var(--surface-sunken); padding: 10px 12px; }
.sahead { display: flex; align-items: center; gap: 8px; color: var(--text-body); }
.sat { font: 600 12.5px var(--font-body); color: var(--text-strong); }
.sasub { flex: 1; font: 500 11px var(--font-body); color: var(--text-faint); }
.saform { margin-top: 9px; display: flex; flex-direction: column; gap: 8px; padding: 10px; border: 1px dashed var(--border-default); border-radius: 10px; background: var(--surface-card); }
.sarow { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.sarowi { display: flex; align-items: center; gap: 8px; margin-top: 8px; min-width: 0; }
.sarowi.off { opacity: 0.55; }
.saic { color: var(--accent-text); flex-shrink: 0; }
.san { font: 600 12.5px var(--font-body); color: var(--text-strong); white-space: nowrap; }
.sam { font: 500 11px var(--font-body); color: var(--text-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.sam.right { margin-left: auto; flex-shrink: 0; }
.saempty { margin-top: 8px; font: 500 11.5px var(--font-body); color: var(--text-faint); }
.list { padding: 14px 20px 6px; display: flex; flex-direction: column; gap: 8px; }
.item { display: flex; align-items: center; gap: 12px; padding: 10px 12px; border: 1px solid var(--border-subtle); border-radius: 11px; background: var(--surface-sunken); }
.item.off { opacity: 0.55; }
.itop { display: flex; align-items: center; gap: 9px; }
.iname { font: 600 13px var(--font-body); color: var(--text-strong); }
.ikey { font: 500 11px var(--font-mono); color: var(--text-muted); background: var(--surface-card); border: 1px solid var(--border-subtle); border-radius: 6px; padding: 1px 6px; }
.imeta { margin-top: 4px; font: 500 11px var(--font-mono); color: var(--text-muted); display: flex; flex-wrap: wrap; gap: 6px; }
.sep { color: var(--text-faint); }
.del { width: 26px; height: 26px; display: inline-flex; align-items: center; justify-content: center; border-radius: 7px; border: 1px solid var(--border-subtle); color: var(--danger-text); cursor: pointer; }
.empty { padding: 16px; text-align: center; font: 500 12px var(--font-body); color: var(--text-faint); }
.sample { padding: 8px 20px 18px; }
.fl { font: 500 11px var(--font-body); color: var(--text-faint); margin-bottom: 6px; }
.code { margin: 0; padding: 12px 14px; border-radius: 10px; background: var(--surface-sunken); border: 1px solid var(--border-subtle); font: 500 11px var(--font-mono); color: var(--text-body); white-space: pre-wrap; word-break: break-all; overflow-x: auto; }
.overlay { position: fixed; inset: 0; z-index: 50; }
.mask { position: absolute; inset: 0; background: rgba(4, 6, 12, 0.7); backdrop-filter: blur(3px); }
.modal { position: absolute; left: 50%; top: 50%; transform: translate(-50%, -50%); width: 560px; max-width: 92vw; max-height: 88vh; overflow-y: auto; background: var(--surface-card); border: 1px solid var(--border-default); border-radius: 18px; box-shadow: var(--shadow-xl); }
.mhead { display: flex; align-items: center; gap: 12px; padding: 15px 20px; border-bottom: 1px solid var(--border-subtle); }
.mic { width: 32px; height: 32px; border-radius: 10px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; }
.mt { font: 700 14.5px var(--font-display); color: var(--text-strong); }
.ms { font: 500 11.5px var(--font-body); color: var(--text-muted); }
.mx { color: var(--text-muted); margin-left: auto; cursor: pointer; }
.mbody { padding: 6px 20px 16px; display: flex; flex-direction: column; gap: 12px; }
.in { width: 100%; box-sizing: border-box; height: 38px; border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-sunken); padding: 0 12px; font: 400 13px var(--font-body); color: var(--text-body); outline: none; }
.hint { margin-top: 5px; font: 500 11px var(--font-body); color: var(--text-faint); }
.scopes { display: flex; flex-wrap: wrap; gap: 7px; }
.sc { display: inline-flex; align-items: center; gap: 5px; padding: 5px 10px; border-radius: 8px; border: 1px dashed var(--border-default); font: 600 11px var(--font-mono); color: var(--text-faint); cursor: pointer; }
.sc.on { background: var(--accent-subtle); color: var(--accent-text); border-style: solid; border-color: transparent; }
.mfoot { display: flex; justify-content: flex-end; gap: 10px; padding: 12px 20px; border-top: 1px solid var(--border-subtle); background: var(--surface-raised); }
</style>
