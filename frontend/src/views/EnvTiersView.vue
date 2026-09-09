<script setup lang="ts">
/**
 * Control tiers and environments.
 *
 * A tier owns the rules; an environment groups instances and binds to one tier.
 * Adding a second production cluster is a new ENVIRONMENT on the existing prod
 * tier — nothing is copied and it is governed from its first second.
 *
 * Almost every control on this page is a guard rather than a convenience. Both
 * rule lookups treat a missing row as permission granted, so a tier without its
 * rules, or an instance whose environment no longer resolves, is an unregulated
 * production database that looks exactly like a regulated one. The server
 * enforces each rule; the UI states the reason up front so an operator does not
 * discover it from a rejected request.
 */
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Layers, Boxes, Plus, X, ShieldCheck, Clock, Pencil, Trash2, Check, Database } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSwitch from '@/components/common/VSwitch.vue'
import VSelect from '@/components/common/VSelect.vue'
import api from '@/api'
import { confirmAction } from '@/lib/confirm'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import { useEnvTierStore } from '@/stores/envtier'
import type { Connection, EnvTier, ExecWindow } from '@/types'

const { t } = useI18n()
const ui = useUIStore()
const auth = useAuthStore()
const envtier = useEnvTierStore()
// Every mutation is admin-only server-side; non-admins get the page read-only.
const isAdmin = computed(() => auth.me?.roleCodes?.includes('admin') ?? (auth.me?.roleCode === 'admin'))

// 三个子领域分成页签,而不是一路铺下去。它们是三张各自完整的表,叠在一页里要滚
// 三屏才看得完,而每次进来其实只处理其中一件事。
const tab = ref<'tiers' | 'envs'>('tiers')

const usage = ref<Record<string, number>>({})
const busy = ref(false)

async function reload() {
  await envtier.load(true)
  try { usage.value = await api.environmentUsage() } catch { /* counts are advisory */ }
  ui.pageSub = { key: 'etSub', params: { tiers: envtier.tiers.length, envs: envtier.environments.length } }
}

onMounted(async () => {
  try { await reload() } catch (e) { ui.notifyError(e, t('actionFailed')) }
})

const envCount = (code: string) => usage.value[code] ?? 0

// 已绑定环境:列表里最多摊开三个,其余折成 +N。一个分层底下挂七八个环境是常事,
// 全铺出来这一列会把整行撑成两行高,而它回答的问题只是"大概挂了哪些"。
const MAX_ENV_CHIPS = 3
const envChips = (code: string) => boundEnvs(code).slice(0, MAX_ENV_CHIPS)
const envChipsMore = (code: string) => Math.max(0, boundEnvs(code).length - MAX_ENV_CHIPS)

// 环境行的分层绑定:默认只显示一个标签,点"编辑"才换成下拉。整列都摆着下拉框时,
// 一张只是想看一眼归属的表会长得像一张待填的表单。
const editEnv = ref('')

/** Environments bound to a tier — what blocks its deletion. */
const boundEnvs = (code: string) => envtier.environments.filter((e) => e.tierCode === code)

// ---------------------------------------------------------------- tier form

const tierForm = ref(false)
const tf = ref({
  code: '', displayName: '', templateCode: '', sortOrder: 0,
  requireMfa: false, dangerBanner: false, countsInPending: false, strictNoWhere: true,
  connLayer: '', defaultRole: 'dba_l2',
})
function openTierForm() {
  if (!isAdmin.value) return
  tf.value = {
    code: '', displayName: '', templateCode: envtier.tierCodes[0] ?? '', sortOrder: envtier.tiers.length,
    requireMfa: false, dangerBanner: false, countsInPending: false, strictNoWhere: true,
    connLayer: '', defaultRole: 'dba_l2',
  }
  tierForm.value = true
}
async function createTier() {
  if (!tf.value.code.trim() || !tf.value.displayName.trim() || !tf.value.templateCode) return
  busy.value = true
  try {
    // The template is required by the API, not just prefilled here: a tier
    // created empty would have no capability and no dictionary rows, and every
    // lookup against it falls through to allowed.
    await api.createEnvTier({ ...tf.value, scanBaseline: false })
    tierForm.value = false
    await reload()
    ui.notify(t('etTierCreated', { code: tf.value.code, from: tf.value.templateCode }), 'success')
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  } finally { busy.value = false }
}

async function saveTier(tier: EnvTier, patch: Partial<EnvTier>) {
  if (!isAdmin.value) return
  busy.value = true
  try {
    await api.updateEnvTier(tier.code, { ...tier, ...patch })
    await reload()
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
    await reload() // the switch showed the attempted state; put it back
  } finally { busy.value = false }
}

/**
 * Moving the scan baseline. It is single-valued: setting it here clears it
 * elsewhere in the same transaction, and it can never be switched off outright —
 * with no baseline, script scanning matches nothing and reports every uploaded
 * script as clean, without raising an error anywhere.
 */
async function makeBaseline(tier: EnvTier) {
  if (!isAdmin.value || tier.scanBaseline) return
  if (!confirmAction(t('etBaselineConfirm', { code: tier.code }))) return
  await saveTier(tier, { scanBaseline: true })
}

async function removeTier(tier: EnvTier) {
  if (!isAdmin.value || !tierDeletable(tier)) return
  if (!confirmAction(t('etTierDeleteConfirm', { code: tier.code }))) return
  busy.value = true
  try {
    await api.deleteEnvTier(tier.code)
    await reload()
    ui.notify(t('etTierDeleted', { code: tier.code }), 'success')
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  } finally { busy.value = false }
}

const tierDeletable = (tier: EnvTier) =>
  !tier.scanBaseline && boundEnvs(tier.code).length === 0 && envtier.tiers.length > 1
/** Why the delete is unavailable — shown instead of an unexplained dead button. */
function tierBlockReason(tier: EnvTier): string {
  if (tier.scanBaseline) return t('etBlockedBaseline')
  const n = boundEnvs(tier.code).length
  if (n > 0) return t('etBlockedBound', { n })
  if (envtier.tiers.length <= 1) return t('etBlockedLast')
  return ''
}

// ------------------------------------------------------------ environment form

const envForm = ref(false)
const ef = ref({ code: '', displayName: '', tierCode: '', sortOrder: 0 })
function openEnvForm() {
  if (!isAdmin.value) return
  ef.value = {
    code: '', displayName: '', tierCode: envtier.tierCodes[0] ?? '',
    sortOrder: envtier.environments.length,
  }
  envForm.value = true
}
async function createEnvironment() {
  if (!ef.value.code.trim() || !ef.value.displayName.trim() || !ef.value.tierCode) return
  busy.value = true
  try {
    await api.createEnvironment(ef.value)
    envForm.value = false
    await reload()
    ui.notify(t('etEnvCreated', { code: ef.value.code, tier: ef.value.tierCode }), 'success')
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  } finally { busy.value = false }
}

/**
 * Rebinding an environment to another tier changes how every instance in it is
 * governed from that moment on. It does NOT rewrite history: approvals and audit
 * rows keep the tier they were judged under.
 */
async function rebind(code: string, tierLabel: string) {
  const tierCode = envtier.tierCodes.find((c) => tierOptionLabel(c) === tierLabel)
  const env = envtier.envByCode[code]
  if (!isAdmin.value || !env || !tierCode || tierCode === env.tierCode) return
  if (!confirmAction(t('etRebindConfirm', { code, from: env.tierCode, to: tierCode, n: envCount(code) }))) {
    await reload() // revert the select
    return
  }
  busy.value = true
  try {
    await api.updateEnvironment(code, { ...env, tierCode })
    await reload()
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
    await reload()
  } finally { busy.value = false }
}

/**
 * Deleting an environment moves its instances rather than orphaning them: an
 * instance pointing at an environment that no longer exists resolves to no tier,
 * and therefore to no rules at all.
 */
const delEnv = ref<{ open: boolean; code: string; moveTo: string }>({ open: false, code: '', moveTo: '' })
function openDeleteEnv(code: string) {
  if (!isAdmin.value || envtier.environments.length <= 1) return
  const other = envtier.environments.find((e) => e.code !== code)
  delEnv.value = { open: true, code, moveTo: other ? envOptionLabel(other.code) : '' }
}
async function confirmDeleteEnv() {
  const moveTo = envtier.environments.find((e) => envOptionLabel(e.code) === delEnv.value.moveTo)?.code
  if (!moveTo || moveTo === delEnv.value.code) return
  busy.value = true
  try {
    await api.deleteEnvironment(delEnv.value.code, moveTo)
    ui.notify(t('etEnvDeleted', { code: delEnv.value.code, n: envCount(delEnv.value.code), to: moveTo }), 'success')
    delEnv.value = { open: false, code: '', moveTo: '' }
    await reload()
  } catch (e) {
    ui.notifyError(e, t('actionFailed'))
  } finally { busy.value = false }
}

// Selects bind on label strings, so labels must be unique — codes are.
const tierOptionLabel = (code: string) => `${code} · ${envtier.tierLabel(code, t)}`
const envOptionLabel = (code: string) => `${code} · ${envtier.envLabel(code)}`
const tierOptions = computed(() => envtier.tierCodes.map(tierOptionLabel))
const moveTargets = computed(() =>
  envtier.environments.filter((e) => e.code !== delEnv.value.code).map((e) => envOptionLabel(e.code)))
</script>

<template>
  <div class="scy page">
    <div class="head">
      <div>
        <div class="eyebrow">TIERS &amp; ENVIRONMENTS</div>
        <div class="sub">{{ $t('etIntro') }}</div>
      </div>
      <span v-if="!isAdmin" class="roflag">{{ $t('readOnlyPerms') }}</span>
    </div>

    <!-- 三个子领域分页签。它们是三张各自完整的表 —— 一路铺下去要滚三屏才看得完,
         而每次进来其实只处理其中一件事。 -->
    <div class="segtabs">
      <button class="segtab" :class="{ on: tab === 'tiers' }" @click="tab = 'tiers'">
        <Layers :size="14" />{{ $t('etTierTitle') }}<span class="segn">{{ envtier.tiers.length }}</span>
      </button>
      <button class="segtab" :class="{ on: tab === 'envs' }" @click="tab = 'envs'">
        <Boxes :size="14" />{{ $t('etEnvTitle') }}<span class="segn">{{ envtier.environments.length }}</span>
      </button>
    </div>

    <!-- ------------------------------------------------------------ tiers -->
    <div v-show="tab === 'tiers'" class="card">
      <div class="chead">
        <div class="cic"><Layers :size="17" color="var(--danger)" /></div>
        <div class="grow">
          <div class="ct">{{ $t('etTierTitle') }}</div>
          <div class="cs">{{ $t('etTierSub') }}</div>
        </div>
        <VButton v-if="isAdmin" variant="secondary" height="34px" @click="openTierForm">
          <Plus :size="14" />{{ $t('etNewTier') }}
        </VButton>
      </div>

      <div class="scx">
        <div class="tgrid">
          <!-- 四个管控开关合并到一个大表头下面。它们回答的是同一个问题("这一层
               按什么规矩办"),分散成四个平级列头时,读的人得逐列去猜彼此的关系。 -->
          <div class="thgroup">
            <span />
            <span class="grouphd">{{ $t('etGroupPolicy') }}</span>
            <span /><span /><span />
          </div>
          <div class="th">
            <span>{{ $t('etColTier') }}</span><span class="ctr">{{ $t('etColMfa') }}</span>
            <span class="ctr">{{ $t('etColBanner') }}</span><span class="ctr">{{ $t('etColPending') }}</span>
            <span class="ctr" :title="$t('etColStrictHint')">{{ $t('etColStrict') }}</span>
            <span class="ctr">{{ $t('etColBaseline') }}</span><span>{{ $t('etColBound') }}</span><span />
          </div>
          <div v-for="tier in envtier.tiers" :key="tier.code" class="tr">
            <div class="tiercell">
              <span class="tierbadge" :class="envtier.dotFor(tier.code)">{{ tier.code }}</span>
              <span class="tiertxt">
                <span class="cl">{{ envtier.tierLabel(tier.code, t) }}</span>
                <span class="cl2">{{ tier.connLayer }} · {{ tier.defaultRole }}</span>
              </span>
            </div>
            <div class="ctr" :class="{ ro: !isAdmin }"><VSwitch :model-value="tier.requireMfa" @update:model-value="(v: boolean) => isAdmin && saveTier(tier, { requireMfa: v })" /></div>
            <div class="ctr" :class="{ ro: !isAdmin }"><VSwitch :model-value="tier.dangerBanner" @update:model-value="(v: boolean) => isAdmin && saveTier(tier, { dangerBanner: v })" /></div>
            <div class="ctr" :class="{ ro: !isAdmin }"><VSwitch :model-value="tier.countsInPending" @update:model-value="(v: boolean) => isAdmin && saveTier(tier, { countsInPending: v })" /></div>
            <!-- 判定的第三层。前两层(能力矩阵、高危命令字典)本来就按分层存,
                 这一层过去是个全局开关,于是它是唯一一道瞄不准的闸。 -->
            <div class="ctr" :class="{ ro: !isAdmin }" :title="$t('etColStrictHint')"><VSwitch :model-value="tier.strictNoWhere" @update:model-value="(v: boolean) => isAdmin && saveTier(tier, { strictNoWhere: v })" /></div>
            <!-- Radio semantics, not a switch: exactly one tier holds it, and
                 turning it off is never an option — only moving it elsewhere. -->
            <div class="ctr">
              <span v-if="tier.scanBaseline" class="basebadge" :title="$t('etBaselineHeld')"><ShieldCheck :size="13" />{{ $t('etBaselineOn') }}</span>
              <button v-else-if="isAdmin" class="baselink" :disabled="busy" @click="makeBaseline(tier)">{{ $t('etBaselineSet') }}</button>
              <span v-else class="dash">—</span>
            </div>
            <div class="chips">
              <template v-if="boundEnvs(tier.code).length">
                <span v-for="e in envChips(tier.code)" :key="e.code" class="chip">{{ e.code }}</span>
                <span v-if="envChipsMore(tier.code)" class="chip more" :title="boundEnvs(tier.code).map((e) => e.code).join(', ')">+{{ envChipsMore(tier.code) }}</span>
              </template>
              <span v-else class="dash">{{ $t('etNoEnv') }}</span>
            </div>
            <div class="acts">
              <button v-if="isAdmin" class="iconbtn danger" :disabled="!tierDeletable(tier) || busy" :title="tierBlockReason(tier) || $t('etDelete')" @click="removeTier(tier)">
                <Trash2 :size="13" />
              </button>
            </div>
          </div>
        </div>
      </div>

      <div v-if="tierForm" class="form">
        <div class="fhead">{{ $t('etNewTier') }}<span class="x" @click="tierForm = false"><X :size="16" /></span></div>
        <div class="frow">
          <div><div class="fl">{{ $t('etFCode') }}</div><input v-model="tf.code" placeholder="prod-hk" /></div>
          <div><div class="fl">{{ $t('etFName') }}</div><input v-model="tf.displayName" :placeholder="$t('etFNamePh')" /></div>
        </div>
        <div class="frow">
          <div><div class="fl">{{ $t('etFTemplate') }}</div><VSelect v-model="tf.templateCode" :options="envtier.tierCodes" /></div>
          <div><div class="fl">{{ $t('etFLayer') }}</div><input v-model="tf.connLayer" :placeholder="$t('etLayerPh')" /></div>
        </div>
        <!-- Stating what the clone does is the whole point of the mandatory
             template: the operator is copying a rule set, not naming a label. -->
        <div class="warn">{{ $t('etCloneNote', { from: tf.templateCode || '—' }) }}</div>
        <div class="frow3">
          <label class="chk"><VSwitch v-model="tf.requireMfa" />{{ $t('etColMfa') }}</label>
          <label class="chk"><VSwitch v-model="tf.dangerBanner" />{{ $t('etColBanner') }}</label>
          <label class="chk"><VSwitch v-model="tf.countsInPending" />{{ $t('etColPending') }}</label>
          <label class="chk" :title="$t('etColStrictHint')"><VSwitch v-model="tf.strictNoWhere" />{{ $t('etColStrict') }}</label>
        </div>
        <div class="ffoot">
          <VButton variant="secondary" height="34px" @click="tierForm = false">{{ $t('btnCancel') }}</VButton>
          <VButton variant="primary" height="34px" :disabled="busy || !tf.code || !tf.displayName || !tf.templateCode" @click="createTier">{{ $t('btnSave') }}</VButton>
        </div>
      </div>
    </div>

    <!-- ----------------------------------------------------- environments -->
    <div v-show="tab === 'envs'" class="card">
      <div class="chead">
        <div class="cic"><Boxes :size="17" color="var(--accent-text)" /></div>
        <div class="grow">
          <div class="ct">{{ $t('etEnvTitle') }}</div>
          <div class="cs">{{ $t('etEnvSub') }}</div>
        </div>
        <VButton v-if="isAdmin" variant="secondary" height="34px" @click="openEnvForm">
          <Plus :size="14" />{{ $t('etNewEnv') }}
        </VButton>
      </div>

      <div class="scx">
        <div class="egrid">
          <div class="th"><span>{{ $t('etColEnv') }}</span><span>{{ $t('etColTierBind') }}</span><span class="ctr">{{ $t('etColInsts') }}</span><span /></div>
          <div v-for="e in envtier.environments" :key="e.code" class="tr">
            <div class="tiercell">
              <span class="d" :class="envtier.dotForEnv(e.code)" />
              <span class="tiertxt">
                <span class="cn">{{ e.code }}</span>
                <span class="cl">{{ e.displayName }}</span>
              </span>
            </div>
            <!-- 归属默认是一个标签,点铅笔才换成下拉。整列都摆着下拉框时,一张
                 只是想看一眼归属的表会长得像一张待填的表单。 -->
            <div class="bindcell">
              <template v-if="isAdmin && editEnv === e.code">
                <VSelect
                  :model-value="tierOptionLabel(e.tierCode)" :options="tierOptions" height="32px"
                  @update:model-value="(v: string) => { rebind(e.code, v); editEnv = '' }"
                />
                <button class="iconbtn" :title="$t('btnCancel')" @click="editEnv = ''"><X :size="13" /></button>
              </template>
              <template v-else>
                <span class="tierbadge sm" :class="envtier.dotFor(e.tierCode)">{{ e.tierCode }}</span>
                <span class="bindname">{{ envtier.tierLabel(e.tierCode, t) }}</span>
                <button v-if="isAdmin" class="iconbtn ghost" :title="$t('etRebind')" @click="editEnv = e.code"><Pencil :size="12" /></button>
              </template>
            </div>
            <div class="ctr">
              <span class="countpill" :class="{ zero: !envCount(e.code) }">
                <!-- 带上计数走复数分支:英文里 "1 instances" 是错的,而这一列
                     大多数行的值恰恰是 1。中文只有一种形式,同一个 key 照常工作。 -->
                <Database :size="11" />{{ $t('etInstN', { n: envCount(e.code) }, envCount(e.code)) }}
              </span>
            </div>
            <div class="acts">
              <button v-if="isAdmin" class="iconbtn danger" :disabled="envtier.environments.length <= 1 || busy" :title="envtier.environments.length <= 1 ? $t('etBlockedLastEnv') : $t('etDelete')" @click="openDeleteEnv(e.code)">
                <Trash2 :size="13" />
              </button>
            </div>
          </div>
        </div>
      </div>

      <div v-if="envForm" class="form">
        <div class="fhead">{{ $t('etNewEnv') }}<span class="x" @click="envForm = false"><X :size="16" /></span></div>
        <div class="frow">
          <div><div class="fl">{{ $t('etFCode') }}</div><input v-model="ef.code" placeholder="prod-hk" /></div>
          <div><div class="fl">{{ $t('etFName') }}</div><input v-model="ef.displayName" :placeholder="$t('etFEnvNamePh')" /></div>
        </div>
        <div class="frow">
          <div><div class="fl">{{ $t('etColTierBind') }}</div><VSelect v-model="ef.tierCode" :options="envtier.tierCodes" /></div>
          <div />
        </div>
        <div class="note">{{ $t('etEnvCreateNote', { tier: ef.tierCode || '—' }) }}</div>
        <div class="ffoot">
          <VButton variant="secondary" height="34px" @click="envForm = false">{{ $t('btnCancel') }}</VButton>
          <VButton variant="primary" height="34px" :disabled="busy || !ef.code || !ef.displayName || !ef.tierCode" @click="createEnvironment">{{ $t('btnSave') }}</VButton>
        </div>
      </div>
    </div>

    <!-- Deleting an environment always relocates its instances. -->
    <div v-if="delEnv.open" class="ovl">
      <div class="mask" @click="delEnv.open = false" />
      <div class="modal">
        <div class="mhead">{{ $t('etDeleteEnvTitle', { code: delEnv.code }) }}</div>
        <div class="mbody">
          <div class="mwarn">{{ $t('etDeleteEnvWarn', { n: envCount(delEnv.code) }) }}</div>
          <div class="fl">{{ $t('etMoveTo') }}</div>
          <VSelect v-model="delEnv.moveTo" :options="moveTargets" />
        </div>
        <div class="mfoot">
          <VButton variant="secondary" height="34px" @click="delEnv.open = false">{{ $t('btnCancel') }}</VButton>
          <VButton variant="danger" height="34px" :disabled="busy || !delEnv.moveTo" @click="confirmDeleteEnv">{{ $t('etDelete') }}</VButton>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.page { padding: var(--page-pad); padding-bottom: 40px; max-width: var(--page-max); margin-inline: auto; width: 100%; }
.head { display: flex; align-items: flex-start; gap: 16px; margin-bottom: 20px; }
.eyebrow { font: 600 11px var(--font-mono); letter-spacing: 0.14em; color: var(--text-faint); }
.sub { margin-top: 6px; font: 400 12.5px/1.6 var(--font-body); color: var(--text-muted); max-width: 720px; }
.roflag { margin-left: auto; font: 600 11px var(--font-mono); color: var(--warning-text); }

.card { margin-bottom: 22px; border: 1px solid var(--border-subtle); border-radius: 16px; background: var(--surface-card); overflow: hidden; }
.chead { display: flex; align-items: center; gap: 12px; padding: 15px 18px; border-bottom: 1px solid var(--border-subtle); }
.cic { width: 34px; height: 34px; border-radius: 10px; background: var(--surface-sunken); display: flex; align-items: center; justify-content: center; }
.grow { flex: 1; }
.ct { font: 700 14px var(--font-display); color: var(--text-strong); }
.cs { margin-top: 3px; font: 400 11.5px/1.5 var(--font-body); color: var(--text-muted); }

/* Both tables scroll inside their own card; the page body never moves sideways. */
.tgrid, .egrid { min-width: max-content; }
/* 开关四列收窄并固定:它们是同一组东西,列宽一致才有一条能顺着往下看的竖线。
   合并表头 .thgroup 用同一套列宽,所以"管控策略配置"正好压在那四列上面。 */
.tgrid .th, .tgrid .tr, .tgrid .thgroup { display: grid; grid-template-columns: minmax(200px, 1.6fr) 76px 76px 76px 76px 116px minmax(170px, 1.2fr) 64px; gap: 10px; align-items: center; }
.thgroup { padding: 8px 14px 0; }
.thgroup .grouphd { grid-column: 2 / 6; text-align: center; padding-bottom: 6px; border-bottom: 1px solid var(--border-default); font: 600 10.5px var(--font-body); color: var(--text-faint); text-transform: uppercase; letter-spacing: .06em; }
/* 执行窗口列表 */
.wgrid { min-width: max-content; }
.wgrid .th, .wgrid .tr { display: grid; grid-template-columns: minmax(180px, 1.4fr) minmax(160px, 1.1fr) minmax(190px, 1.2fr) 210px 96px 72px; gap: 10px; align-items: center; }
.tr.empty { padding: 18px; color: var(--text-faint); font: 500 12px var(--font-body); }
.cl2 { margin-top: 3px; font: 400 11.5px var(--font-body); color: var(--text-faint); }
/* 三种状态要一眼分得开:开着的是当下真的免审批,值得显眼。 */
.wopen { display: inline-flex; align-items: center; height: 22px; padding: 0 9px; border-radius: 999px; background: var(--danger-subtle); color: var(--danger-text); font: 600 11px var(--font-mono); }
.wclosed { color: var(--text-faint); font: 500 11px var(--font-mono); }
.woff { color: var(--text-faint); font: 500 11px var(--font-mono); text-decoration: line-through; }
.days { display: flex; flex-wrap: wrap; gap: 8px; margin: 6px 0 2px; }
/* 胶囊本身就是开关(.day.on 换色),旁边再放一个原生方框等于同一件事说两遍,而且
   那个方框还是这一排里唯一没被设计过的东西。把它藏起来但保留在 DOM 里 —— 键盘
   和读屏靠它,焦点由 :focus-within 画在胶囊上。 */
.day { position: relative; display: inline-flex; align-items: center; justify-content: center; min-width: 38px; height: 34px; padding: 0 12px; border: 1px solid var(--border-default); border-radius: 999px; font: 600 12px var(--font-body); color: var(--text-muted); cursor: pointer; user-select: none; transition: color .12s, border-color .12s, background .12s; }
.day input { position: absolute; width: 1px; height: 1px; opacity: 0; pointer-events: none; }
/* 选中态实心填充,而不是淡淡的一层底色。原生方框藏起来之后,开关状态全靠这一处
   表达 —— 而"淡底色 + accent 字"和"hover 到一个未选中的胶囊"几乎分不出来,等于
   把仅有的那点区别又交给了鼠标位置。hover 只动边框,不碰字色和填充。 */
.day:hover { border-color: var(--accent-text); }
.day:focus-within { outline: 2px solid var(--accent); outline-offset: 2px; }
.day.on { border-color: var(--accent); background: var(--accent); color: #fff; }
/* 绑定分层那一列给上限,不再按 1.6fr 分走富余:里面是一个下拉框,而"prod · PROD ·
   生产环境"只要两百多像素。之前它被拉到六百多宽,一个带底色的控件横在行中间,看着
   像一条选中的色带而不是一个控件。多出来的宽度让给环境名那一列。 */
.egrid .th, .egrid .tr { display: grid; grid-template-columns: minmax(190px, 2fr) minmax(220px, 340px) 96px 90px; gap: 10px; align-items: center; }
.th { padding: 11px 18px; background: var(--surface-sunken); border-bottom: 1px solid var(--border-subtle); font: 600 11px var(--font-mono); letter-spacing: 0.05em; color: var(--text-faint); text-transform: uppercase; }
.tr { padding: 11px 18px; border-bottom: 1px solid var(--border-subtle); }
.tr:last-child { border-bottom: none; }
.ctr { display: flex; justify-content: center; }
.cn { display: flex; align-items: center; gap: 7px; font: 600 13px var(--font-mono); color: var(--text-strong); }
.cl { margin-top: 3px; font: 500 11.5px var(--font-body); color: var(--text-muted); }
.cl2 { margin-top: 2px; font: 500 10.5px var(--font-mono); color: var(--text-faint); }
.mono { font: 500 12px var(--font-mono); }
.mute { color: var(--text-muted); }
.dash { color: var(--text-faint); }
.d { width: 7px; height: 7px; border-radius: 50%; }
.d.danger { background: var(--danger); }
.d.info { background: #3b82f6; }
.d.warning { background: var(--warning); }
.d.success { background: var(--success); }
.d.muted { background: var(--text-faint); }

.basebadge { display: inline-flex; align-items: center; gap: 5px; padding: 3px 9px; border-radius: 999px; background: var(--success-subtle, rgba(24, 179, 104, 0.12)); color: var(--success-text); font: 600 10.5px var(--font-mono); }
.baselink { border: 1px solid var(--border-subtle); border-radius: 8px; background: transparent; color: var(--text-muted); font: 500 11px var(--font-body); padding: 4px 10px; cursor: pointer; }
.baselink:hover:not(:disabled) { color: var(--accent-text); border-color: var(--accent-text); }
.baselink:disabled { opacity: 0.5; cursor: not-allowed; }

.acts { display: flex; justify-content: flex-end; }
.del { border: 1px solid var(--border-subtle); border-radius: 8px; background: transparent; color: var(--danger-text); font: 500 11px var(--font-body); padding: 4px 10px; cursor: pointer; }
.del:hover:not(:disabled) { background: var(--danger-subtle); }
/* Disabled deletes keep a tooltip explaining which guard is holding — a dead
   button with no reason reads as a bug. */
.del:disabled { opacity: 0.4; cursor: not-allowed; }

/* 全站的约定是"凹陷的字段 + 抬起的面板":输入框和 VSelect 都用 --surface-sunken,
   放在 --surface-card 的面板上(见 ConnectionsView 的 .ce-card/.fgrid)。这张表单
   原先把两者反了过来 —— 面板 sunken、输入框 card —— 于是遵守约定的 VSelect 底色
   和面板一模一样,读起来是一条色带而不是一个控件;旁边的白底输入框又像是另一类
   东西。乱的不是下拉框,是这张表单没跟着约定走。分隔靠 border-top 和标题,不靠底色。 */
.form { padding: 16px 18px 18px; border-top: 1px solid var(--border-subtle); background: var(--surface-card); }
.fhead { display: flex; align-items: center; font: 700 13px var(--font-display); color: var(--text-strong); margin-bottom: 12px; }
.fhead .x { margin-left: auto; cursor: pointer; color: var(--text-faint); display: flex; }
.frow { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; margin-bottom: 12px; }
.frow3 { display: flex; flex-wrap: wrap; gap: 18px; margin: 12px 0; }
.chk { display: flex; align-items: center; gap: 8px; font: 500 12px var(--font-body); color: var(--text-body); }
.fl { font: 600 11px var(--font-mono); color: var(--text-muted); margin-bottom: 6px; }
/* :not([type="checkbox"]) —— 这一条原先是无差别的 `.form input`,于是星期那七个
   复选框也拿到了文本框的样式:一个 13px 宽的复选框被拉成 38px 高,把外面的胶囊
   撑到 56px,比同一张表单里的任何一个控件都高。选择器写宽了,不是样式配错了。

   高度/字号/圆角与 VSelect 对齐(40px · 13px mono · 10px),它们在同一行里并排,
   差一档就看得出来。 */
.form input:not([type="checkbox"]) { width: 100%; height: 40px; padding: 0 12px; border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-sunken); color: var(--text-strong); font: 400 13px var(--font-mono); outline: none; }
.form input:not([type="checkbox"]):focus { border-color: var(--accent-text); }
.warn { padding: 9px 12px; border-radius: 9px; background: var(--warning-subtle, rgba(245, 165, 36, 0.1)); color: var(--warning-text); font: 500 11.5px/1.6 var(--font-body); }
/* 提示原先用的是 --surface-card 的底色加 9px 圆角,而同一张表单里的输入框是
   --surface-card 加 10px 圆角 —— 两者在屏幕上一模一样,于是这段说明看着像一个
   填不进字的空输入框。改成一段带左侧竖线的说明文字,一眼能看出它不是控件。 */
.note { padding: 2px 0 2px 11px; border-left: 2px solid var(--border-default); color: var(--text-muted); font: 500 11.5px/1.6 var(--font-body); }
.ffoot { margin-top: 14px; display: flex; justify-content: flex-end; gap: 10px; }

.ovl { position: fixed; inset: 0; z-index: 60; }
.mask { position: absolute; inset: 0; background: rgba(4, 6, 12, 0.7); backdrop-filter: blur(3px); }
.modal { position: absolute; left: 50%; top: 50%; transform: translate(-50%, -50%); width: 440px; max-width: 94vw; background: var(--surface-card); border: 1px solid var(--border-default); border-radius: 16px; overflow: hidden; }
.mhead { padding: 16px 20px; font: 700 15px var(--font-display); color: var(--text-strong); border-bottom: 1px solid var(--border-subtle); }
.mbody { padding: 16px 20px; }
.mwarn { margin-bottom: 14px; padding: 10px 12px; border-radius: 9px; background: var(--danger-subtle); color: var(--danger-text); font: 500 12px/1.6 var(--font-body); }
.mfoot { display: flex; justify-content: flex-end; gap: 10px; padding: 14px 20px; border-top: 1px solid var(--border-subtle); background: var(--surface-raised); }

/* ---------------- 页签 ---------------- */
.segtabs { display: flex; gap: 6px; margin-bottom: 18px; padding: 4px; border: 1px solid var(--border-subtle); border-radius: 12px; background: var(--surface-card); width: fit-content; max-width: 100%; flex-wrap: wrap; box-shadow: var(--shadow-xs); }
.segtab { display: inline-flex; align-items: center; gap: 7px; height: 34px; padding: 0 14px; border: none; border-radius: 9px; background: transparent; color: var(--text-muted); font: 600 12.5px var(--font-body); cursor: pointer; white-space: nowrap; transition: color .12s, background .12s; }
.segtab:hover { color: var(--text-strong); }
.segtab.on { background: var(--accent-subtle); color: var(--accent-text); }
.segn { padding: 1px 7px; border-radius: 999px; background: var(--surface-sunken); font: 700 10px var(--font-mono); }
.segtab.on .segn { background: var(--surface-card); }

/* ---------------- 分层标识 ---------------- */
.tiercell { display: flex; align-items: center; gap: 10px; min-width: 0; }
.tiertxt { display: flex; flex-direction: column; min-width: 0; }
/* 分层代码用语义色胶囊。颜色来自 envtier.dotFor —— 和树、审批列表上的那套是
   同一个来源,而不是在这一页另配一份。 */
.tierbadge { flex-shrink: 0; display: inline-flex; align-items: center; height: 22px; padding: 0 10px; border-radius: 999px; font: 700 11px var(--font-mono); text-transform: uppercase; background: var(--surface-sunken); color: var(--text-muted); border: 1px solid var(--border-subtle); }
.tierbadge.sm { height: 20px; padding: 0 8px; font-size: 10px; }
.tierbadge.danger { background: var(--danger-subtle); color: var(--danger-text); border-color: transparent; }
.tierbadge.warning { background: var(--warning-subtle); color: var(--warning-text); border-color: transparent; }
.tierbadge.success { background: var(--success-subtle); color: var(--success-text); border-color: transparent; }
.tierbadge.info { background: var(--accent-subtle); color: var(--accent-text); border-color: transparent; }

/* 不能操作的开关弱化,但**不隐藏** —— 只读的人也要看得见这一层现在是怎么配的 */
.ctr.ro { opacity: .45; pointer-events: none; }

/* 已绑定环境:标签组,超出折成 +N */
.chips { display: flex; align-items: center; gap: 5px; flex-wrap: nowrap; min-width: 0; overflow: hidden; }
.chip { flex-shrink: 0; padding: 2px 8px; border-radius: 6px; background: var(--surface-sunken); border: 1px solid var(--border-subtle); font: 500 10.5px var(--font-mono); color: var(--text-muted); }
.chip.more { background: var(--accent-subtle); border-color: transparent; color: var(--accent-text); cursor: default; }

/* ---------------- 环境行 ---------------- */
.bindcell { display: flex; align-items: center; gap: 8px; min-width: 0; }
.bindcell :deep(.vsel) { flex: 1; min-width: 0; }
.bindname { font: 500 12px var(--font-body); color: var(--text-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.countpill { display: inline-flex; align-items: center; gap: 5px; height: 22px; padding: 0 9px; border-radius: 999px; background: var(--accent-subtle); color: var(--accent-text); font: 600 11px var(--font-mono); }
.countpill.zero { background: var(--surface-sunken); color: var(--text-faint); }

/* ---------------- 图标操作按钮 ---------------- */
.iconbtn { width: 26px; height: 26px; display: grid; place-items: center; border: 1px solid var(--border-subtle); border-radius: 8px; background: var(--surface-card); color: var(--text-muted); cursor: pointer; }
.iconbtn:hover:not(:disabled) { color: var(--accent-text); border-color: var(--accent-text); }
.iconbtn.danger:hover:not(:disabled) { color: var(--danger-text); border-color: var(--danger); }
.iconbtn.ghost { border-color: transparent; background: transparent; }
.iconbtn:disabled { opacity: .35; cursor: default; }
.acts { display: flex; align-items: center; justify-content: flex-end; gap: 6px; }

/* ---------------- 发车日圆点 ---------------- */
.daycell { display: flex; align-items: center; gap: 4px; }
.daydot { width: 22px; height: 22px; display: grid; place-items: center; border-radius: 50%; border: 1px solid var(--border-default); font: 600 10px var(--font-body); color: var(--text-faint); }
.daydot.on { background: var(--accent); border-color: var(--accent); color: #fff; }

/* ---------------- 右侧抽屉 ---------------- */
.drawer-ovl { position: fixed; inset: 0; z-index: var(--z-modal, 1100); }
.drawer-mask { position: absolute; inset: 0; background: rgba(4, 6, 12, .45); backdrop-filter: blur(2px); }
.drawer { position: absolute; top: 0; right: 0; bottom: 0; width: min(520px, 100vw); display: flex; flex-direction: column; background: var(--surface-card); border-left: 1px solid var(--border-subtle); box-shadow: -12px 0 32px rgba(16, 24, 48, .18); }
.dhead { display: flex; align-items: center; gap: 11px; padding: 16px 20px; border-bottom: 1px solid var(--border-subtle); }
.dic { width: 32px; height: 32px; border-radius: 9px; background: var(--accent-subtle); display: grid; place-items: center; flex-shrink: 0; }
.dt { font: 600 14px var(--font-display); color: var(--text-strong); }
.ds { font: 500 12px var(--font-body); color: var(--text-muted); margin-top: 1px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.dhead .x { cursor: pointer; color: var(--text-faint); display: flex; }
/* 表单区自己滚,底部操作栏钉在抽屉底 —— 字段一多就把保存按钮滚出视野的表单,
   人会以为还没填完。 */
.dbody { flex: 1; min-height: 0; overflow-y: auto; padding: 16px 20px; }
.dfoot { flex-shrink: 0; display: flex; justify-content: flex-end; gap: 10px; padding: 14px 20px; border-top: 1px solid var(--border-subtle); background: var(--surface-card); }
.drawer .frow { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; margin-bottom: 14px; }
.drawer .fl { margin-bottom: 7px; font: 600 11px var(--font-mono); color: var(--text-muted); }
.drawer input:not([type="checkbox"]) { width: 100%; box-sizing: border-box; height: 40px; padding: 0 12px; border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-sunken); color: var(--text-strong); font: 400 13px var(--font-mono); outline: none; }
.drawer input:not([type="checkbox"]):focus { border-color: var(--accent-text); box-shadow: 0 0 0 3px var(--accent-subtle); }
</style>
