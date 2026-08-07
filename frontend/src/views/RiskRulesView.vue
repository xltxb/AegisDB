<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { ListX, X, Plus } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSwitch from '@/components/common/VSwitch.vue'
import api from '@/api'
import { confirmAction } from '@/lib/confirm'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import { useEnvTierStore } from '@/stores/envtier'
import type { RiskCommandView } from '@/types'

const { t } = useI18n()
const ui = useUIStore()
const auth = useAuthStore()
const envtier = useEnvTierStore()
// Rule configuration is platform-admin only (backend enforces it too).
const isAdmin = computed(() => auth.me?.roleCode === 'admin')
const cmds = ref<RiskCommandView[]>([])
// The dictionary is keyed by control tier, and tiers are rows an administrator
// creates — so the visible tab set comes from the store, not from a union type.
// `env` here (and in the API) is a TIER code; the column name is historical.
const env = ref<string>('prod')
const draft = ref('')
const ruleForm = ref(false)
const strictMode = ref(false)

// new-rule form state (functional: creates dictionary entries)
const rfName = ref('')
const rfTriggers = ref([
  { name: 'DROP', on: true }, { name: 'TRUNCATE', on: true }, { name: 'ALTER', on: false },
  { name: 'DELETE', on: false }, { name: 'GRANT', on: false },
])
const rfTrigDraft = ref('')
const rfEnv = ref<string>('prod')
const rfAction = ref<'block' | 'approve' | 'alert'>('block')

/** Tier codes in display order — the tabs, the segmented control and the cards. */
const tierCodes = computed(() => envtier.tierCodes)

function openRule() {
  rfName.value = ''
  rfTriggers.value = [
    { name: 'DROP', on: true }, { name: 'TRUNCATE', on: true }, { name: 'ALTER', on: false },
    { name: 'DELETE', on: false }, { name: 'GRANT', on: false },
  ]
  rfTrigDraft.value = ''
  rfEnv.value = tierCodes.value[0] ?? 'prod'
  rfAction.value = 'block'
  ruleForm.value = true
}
function addTrigger() {
  const n = rfTrigDraft.value.trim().toUpperCase().replace(/[^A-Z_ ]/g, '')
  if (!n || rfTriggers.value.some((t) => t.name === n)) { rfTrigDraft.value = ''; return }
  rfTriggers.value.push({ name: n, on: true })
  rfTrigDraft.value = ''
}
async function createRule() {
  // block→high (拦截), approve/alert→mid (需审批). Never map to 'off', which would
  // DISABLE the rule and silently allow the command (R11).
  const level = rfAction.value === 'block' ? 'high' : 'mid'
  const selected = rfTriggers.value.filter((t) => t.on).map((t) => t.name)
  // M14: 创建规则失败以 toast 呈现
  try {
    for (const name of selected) {
      // Only touch the chosen environment — sending off-defaults for the others
      // would wipe their existing levels (e.g. clear a PROD=high interception).
      cmds.value = await api.upsertRiskCommand(name, { [rfEnv.value]: level })
    }
    env.value = rfEnv.value
    ruleForm.value = false
  } catch (e) {
    ui.notifyError(e, '操作失败')
  }
}

async function load() {
  // Tiers first: they are the tab set, and `env` must point at one that exists.
  try {
    await envtier.load()
    if (!envtier.tierCodes.includes(env.value)) env.value = envtier.tierCodes[0] ?? env.value
  } catch { /* tabs fall back to the current selection */ }
  // M14: 加载失败以 toast 呈现，避免静默失败
  try {
    cmds.value = await api.riskCommands()
    ui.pageSub = t('subRules', { n: cmds.value.length })
  } catch (e) {
    ui.notifyError(e, '加载失败')
  }
  try {
    strictMode.value = (await api.settings()).strictMode
  } catch { /* keep default */ }
  try {
    hits.value = (await api.gatewayStats()).intercepts || 0
  } catch { /* keep 0 */ }
}
onMounted(load)

// Real rule-hit count (PROD interceptions), from the gateway — no demo number.
const hits = ref(0)
// PROD policy coverage: share of dictionary commands actively gated (not "off").
const coverage = computed(() => {
  const total = cmds.value.length
  if (!total) return 0
  const gated = cmds.value.filter((c) => c.env.prod !== 'off').length
  return Math.round((gated / total) * 1000) / 10
})

async function toggleStrict() {
  if (!isAdmin.value) return
  // M14: 保存严格模式失败以 toast 呈现
  try {
    strictMode.value = !strictMode.value
    await api.saveSettings({ strictMode: strictMode.value })
  } catch (e) {
    ui.notifyError(e, '操作失败')
  }
}

function lvlMeta(l: string) {
  if (l === 'high') return { bg: 'var(--danger-subtle)', c: 'var(--danger-text)', tag: t('block') }
  if (l === 'mid') return { bg: 'var(--warning-subtle)', c: 'var(--warning-text)', tag: t('approve') }
  return { bg: 'var(--surface-sunken)', c: 'var(--text-muted)', tag: t('allow') }
}
const nextLevel: Record<string, string> = { high: 'mid', mid: 'off', off: 'high' }

async function cycle(c: RiskCommandView) {
  if (!isAdmin.value) return
  const cur = c.env[env.value] || 'off'
  // M14: 切换等级失败以 toast 呈现
  try {
    cmds.value = await api.patchRiskCommand(c.command, env.value, nextLevel[cur])
  } catch (e) {
    ui.notifyError(e, '操作失败')
  }
}
async function addCmd() {
  if (!isAdmin.value) return
  const name = draft.value.trim().toUpperCase().replace(/[^A-Z_ ]/g, '')
  if (!name) return
  if (cmds.value.some((c) => c.command === name)) { draft.value = ''; return }
  // Send NO levels. This used to be a literal { prod, staging, dev } map, which
  // omitted gli — and the server wrote only the keys it was given, so every
  // command added here was missing its gli row, and a missing row reads as `off`.
  // The dictionary looked complete on this page while grey-release instances
  // ignored it entirely.
  //
  // The server now expands across every tier it knows about and picks a default
  // per tier, which is the only version that cannot fall behind a tier this page
  // has not heard of.
  // M14: 新增命令失败以 toast 呈现
  try {
    cmds.value = await api.upsertRiskCommand(name, {})
    draft.value = ''
  } catch (e) {
    ui.notifyError(e, '操作失败')
  }
}
async function removeCmd(c: RiskCommandView) {
  if (!isAdmin.value) return
  // M15: 删除风控规则前二次确认
  if (!confirmAction(`确定删除风控规则「${c.command}」吗？此操作不可撤销。`)) return
  // M14: 删除失败以 toast 呈现
  try {
    cmds.value = await api.deleteRiskCommand(c.command)
  } catch (e) {
    ui.notifyError(e, '操作失败')
  }
}

// Policy summary cards derived from the live dictionary + strict mode — no
// fabricated rules. Each reflects real backend state.
const policies = computed(() => {
  const cards: {
    key: string; label: string; tag: string; border: string
    tagBg: string; tagC: string; expr: string; toggle?: boolean; on?: boolean
  }[] = []

  const highProd = cmds.value.filter((c) => c.env.prod === 'high').map((c) => c.command)
  if (highProd.length) {
    cards.push({
      key: 'pHigh', label: t('pHigh'), tag: t('actBlockAppr'), border: 'var(--danger)',
      tagBg: 'var(--danger-subtle)', tagC: 'var(--danger-text)',
      expr: `env=PROD AND cmd IN (${highProd.join(', ')}) → ${t('block')} → ${t('approve')}`,
    })
  }

  // Strict mode is a real global toggle; the card reflects and controls it.
  cards.push({
    key: 'pStrict', label: t('pStrict'), tag: t('actBlockAppr'), border: 'var(--danger)',
    tagBg: 'var(--danger-subtle)', tagC: 'var(--danger-text)',
    expr: `cmd IN (DELETE, UPDATE) AND NOT contains(WHERE) → ${strictMode.value ? t('block') : t('allow')}`,
    toggle: true, on: strictMode.value,
  })

  const midAny = cmds.value
    .filter((c) => tierCodes.value.some((e) => c.env[e] === 'mid'))
    .map((c) => c.command)
  if (midAny.length) {
    cards.push({
      key: 'pAppr', label: t('pAppr'), tag: t('actAppr'), border: 'var(--warning)',
      tagBg: 'var(--warning-subtle)', tagC: 'var(--warning-text)',
      expr: `cmd IN (${midAny.join(', ')}) → ${t('approve')}`,
    })
  }
  return cards
})
</script>

<template>
  <div class="scy page">
    <div class="head">
      <div>
        <div class="eyebrow">RISK POLICIES</div>
        <div class="sub">{{ $t('riskSub') }}</div>
      </div>
      <span v-if="!isAdmin" class="roflag">{{ $t('readOnlyPerms') }}</span>
      <VButton v-else variant="primary" @click="openRule">{{ $t('newRule') }}</VButton>
    </div>

    <!-- dictionary -->
    <div class="dict">
      <div class="dhead">
        <div class="dic"><ListX :size="17" color="var(--danger)" /></div>
        <div class="grow"><div class="dt">{{ $t('dictTitle') }}</div><div class="ds">{{ $t('dictSub') }}</div></div>
        <!-- One tab per tier; scrolls sideways once they outgrow the header. -->
        <div class="envtabs scx">
          <div v-for="e in tierCodes" :key="e" class="et" :class="{ active: env === e }" :title="envtier.tierLabel(e, t)" @click="env = e">{{ e.toUpperCase() }}</div>
        </div>
      </div>
      <div class="chips">
        <div v-for="c in cmds" :key="c.command" class="chip" :style="{ background: lvlMeta(c.env[env]).bg, color: lvlMeta(c.env[env]).c, opacity: c.env[env] === 'off' ? 0.5 : 1 }">
          <span class="cname">{{ c.command }}</span>
          <span class="ctag" :title="isAdmin ? '切换等级' : ''" :style="{ cursor: isAdmin ? 'pointer' : 'default' }" @click="cycle(c)">{{ lvlMeta(c.env[env]).tag }}</span>
          <span v-if="isAdmin" class="cx" title="移除" @click="removeCmd(c)"><X :size="12" /></span>
        </div>
        <div v-if="isAdmin" class="addchip">
          <input v-model="draft" :placeholder="$t('addCmdPh')" @keyup.enter="addCmd" />
          <span class="addbtn" @click="addCmd"><Plus :size="14" /></span>
        </div>
      </div>
    </div>

    <!-- stats -->
    <div class="stats">
      <div class="stat"><div class="n danger">{{ cmds.filter((c) => c.env.prod === 'high').length }}</div><div class="l">{{ $t('statBlock') }}</div></div>
      <div class="stat"><div class="n warn">{{ cmds.filter((c) => c.env.prod === 'mid').length }}</div><div class="l">{{ $t('statAlert') }}</div></div>
      <div class="stat"><div class="n grad">{{ hits }}</div><div class="l">{{ $t('statHits') }}</div></div>
      <div class="stat"><div class="n">{{ coverage }}%</div><div class="l">{{ $t('statCover') }}</div></div>
    </div>

    <!-- policies (derived from the live dictionary + strict mode) -->
    <div class="rules">
      <div v-for="r in policies" :key="r.key" class="rule" :style="{ borderLeftColor: r.border, opacity: r.toggle && !r.on ? 0.5 : 1 }">
        <div class="rgrow">
          <div class="rtop">
            <span class="rn">{{ r.label }}</span>
            <span class="rtag" :style="{ background: r.tagBg, color: r.tagC }">{{ r.tag }}</span>
          </div>
          <div class="rexpr">{{ r.expr }}</div>
        </div>
        <VSwitch v-if="r.toggle" :model-value="!!r.on" :disabled="!isAdmin" @update:model-value="toggleStrict" />
      </div>
    </div>

    <!-- new rule modal (visual) -->
    <div v-if="ruleForm" class="overlay">
      <div class="mask" @click="ruleForm = false" />
      <div class="rfmodal">
        <div class="rfhead">
          <div class="rfic"><Plus :size="19" color="var(--accent-text)" /></div>
          <div><div class="rft">{{ $t('rfTitle') }}</div><div class="rfs">{{ $t('rfSub') }}</div></div>
          <X :size="18" class="rfx" @click="ruleForm = false" />
        </div>
        <div class="rfbody">
          <div><div class="fl">{{ $t('rfName') }}</div><input v-model="rfName" :placeholder="$t('rfNamePh')" /></div>
          <div>
            <div class="fl">{{ $t('rfTriggers') }}</div>
            <div class="trigs">
              <span
                v-for="tg in rfTriggers" :key="tg.name" class="trig" :class="{ off: !tg.on }"
                @click="tg.on = !tg.on"
              >{{ tg.name }}</span>
              <span class="trig add">
                <input v-model="rfTrigDraft" :placeholder="$t('rfCustomPh')" @keyup.enter="addTrigger" />
                <Plus :size="12" @click="addTrigger" />
              </span>
            </div>
          </div>
          <div class="rf2">
            <div>
              <div class="fl">{{ $t('rfEnv') }}</div>
              <div class="seg scx">
                <div v-for="e in tierCodes" :key="e" class="si" :class="{ active: rfEnv === e }" :title="envtier.tierLabel(e, t)" @click="rfEnv = e">{{ e.toUpperCase() }}</div>
              </div>
            </div>
            <div>
              <div class="fl">{{ $t('rfAction') }}</div>
              <div class="seg">
                <div class="si" :class="{ active: rfAction === 'block', danger: rfAction === 'block' }" @click="rfAction = 'block'">{{ $t('rfActBlock') }}</div>
                <div class="si" :class="{ active: rfAction === 'approve' }" @click="rfAction = 'approve'">{{ $t('rfActApprove') }}</div>
                <div class="si" :class="{ active: rfAction === 'alert' }" @click="rfAction = 'alert'">{{ $t('rfActAlert') }}</div>
              </div>
            </div>
          </div>
        </div>
        <div class="rffoot">
          <div class="rfhint">{{ $t('rfFootHint') }}</div>
          <div class="rfacts"><VButton variant="secondary" @click="ruleForm = false">{{ $t('cancel') }}</VButton><VButton variant="primary" @click="createRule">{{ $t('rfCreate') }}</VButton></div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.page { flex: 1; min-height: 0; padding: 24px 28px; }
.head { display: flex; align-items: center; margin-bottom: 18px; }
.head .roflag { margin-left: auto; }
.roflag { display: inline-flex; align-items: center; height: 26px; padding: 0 11px; border-radius: 999px; background: var(--surface-sunken); border: 1px solid var(--border-subtle); font: 600 11px var(--font-mono); color: var(--text-muted); }
.eyebrow { font: 500 11px var(--font-mono); letter-spacing: 0.12em; color: var(--text-faint); text-transform: uppercase; }
.sub { font: 500 13px var(--font-body); color: var(--text-muted); margin-top: 4px; }
.head :deep(.vbtn) { margin-left: auto; }
.dict { border: 1px solid var(--border-subtle); border-radius: 14px; background: var(--surface-card); padding: 18px 20px; margin-bottom: 18px; }
.dhead { display: flex; align-items: center; gap: 10px; margin-bottom: 14px; }
.dic { width: 32px; height: 32px; border-radius: 9px; background: var(--danger-subtle); display: flex; align-items: center; justify-content: center; }
.grow { flex: 1; }
.dt { font: 600 14px var(--font-display); color: var(--text-strong); }
.ds { font: 500 12px var(--font-body); color: var(--text-muted); margin-top: 1px; }
.envtabs { display: flex; border: 1px solid var(--border-default); border-radius: 10px; overflow: hidden; }
.et { height: 32px; line-height: 32px; padding: 0 14px; cursor: pointer; font: 600 11px var(--font-mono); color: var(--text-muted); border-left: 1px solid var(--border-subtle); }
.et:first-child { border-left: none; }
.et.active { background: var(--accent-subtle); color: var(--accent-text); }
.chips { display: flex; flex-wrap: wrap; gap: 9px; align-items: center; }
.chip { display: inline-flex; align-items: center; gap: 8px; height: 32px; padding: 0 6px 0 12px; border-radius: 999px; transition: opacity 0.15s ease; }
.cname { font: 600 12px var(--font-mono); }
.ctag { font: 700 9px var(--font-mono); padding: 2px 6px; border-radius: 999px; background: rgba(255, 255, 255, 0.1); cursor: pointer; }
.cx { width: 18px; height: 18px; display: flex; align-items: center; justify-content: center; border-radius: 50%; cursor: pointer; background: rgba(255, 255, 255, 0.08); }
.addchip { display: inline-flex; align-items: center; gap: 6px; height: 32px; padding: 0 6px 0 12px; border: 1px dashed var(--border-default); border-radius: 999px; }
.addchip input { width: 104px; background: transparent; border: none; outline: none; font: 600 12px var(--font-mono); color: var(--text-body); text-transform: uppercase; }
.addbtn { width: 22px; height: 22px; display: flex; align-items: center; justify-content: center; border-radius: 50%; background: var(--accent); color: #fff; cursor: pointer; }
.stats { display: flex; gap: 14px; margin-bottom: 18px; }
.stat { flex: 1; border: 1px solid var(--border-subtle); border-radius: 12px; background: var(--surface-card); padding: 16px 18px; }
.stat .n { font: 700 26px var(--font-display); letter-spacing: -0.02em; color: var(--text-strong); }
.stat .n.danger { color: var(--danger-text); }
.stat .n.warn { color: var(--warning-text); }
.stat .n.grad { background: linear-gradient(135deg, #5e83fb, #2dcde6); -webkit-background-clip: text; background-clip: text; -webkit-text-fill-color: transparent; }
.stat .l { font: 500 12px var(--font-mono); color: var(--text-muted); margin-top: 2px; }
.rules { display: flex; flex-direction: column; gap: 12px; }
.rule { border: 1px solid var(--border-subtle); border-left: 3px solid; border-radius: 12px; background: var(--surface-card); padding: 16px 18px; display: flex; align-items: center; gap: 18px; transition: opacity 0.2s ease; }
.rgrow { flex: 1; min-width: 0; }
.rtop { display: flex; align-items: center; gap: 10px; }
.rn { font: 600 14px var(--font-body); color: var(--text-strong); }
.rtag { display: inline-flex; align-items: center; height: 20px; padding: 0 8px; border-radius: 999px; font: 600 10px var(--font-mono); }
.rexpr { margin-top: 8px; font: 500 12px var(--font-mono); color: var(--text-muted); background: var(--surface-sunken); border: 1px solid var(--border-subtle); border-radius: 8px; padding: 8px 11px; display: inline-block; }
/* rule modal */
.overlay { position: fixed; inset: 0; z-index: 50; }
.mask { position: absolute; inset: 0; background: rgba(4, 6, 12, 0.7); backdrop-filter: blur(3px); }
.rfmodal { position: absolute; left: 50%; top: 50%; transform: translate(-50%, -50%); width: 600px; max-width: 92vw; background: var(--surface-card); border: 1px solid var(--border-default); border-radius: 18px; overflow: hidden; box-shadow: var(--shadow-xl); animation: modalIn 0.26s cubic-bezier(0.16, 1, 0.3, 1); }
.rfhead { display: flex; align-items: center; gap: 12px; padding: 18px 24px; border-bottom: 1px solid var(--border-subtle); }
.rfic { width: 36px; height: 36px; border-radius: 10px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; }
.rft { font: 700 16px var(--font-display); color: var(--text-strong); }
.rfs { font: 500 12px var(--font-body); color: var(--text-muted); margin-top: 1px; }
.rfx { color: var(--text-muted); margin-left: auto; cursor: pointer; }
.rfbody { padding: 20px 24px; display: flex; flex-direction: column; gap: 16px; }
.fl { font: 500 11px var(--font-body); color: var(--text-faint); margin-bottom: 6px; }
.rfbody input { width: 100%; box-sizing: border-box; height: 40px; border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-sunken); padding: 0 12px; font: 400 13px var(--font-body); color: var(--text-body); outline: none; }
.trigs { display: flex; flex-wrap: wrap; gap: 7px; }
.trig { display: inline-flex; align-items: center; height: 28px; padding: 0 11px; border-radius: 8px; font: 600 11px var(--font-mono); background: var(--danger-subtle); color: var(--danger-text); cursor: pointer; }
.trig.off { background: transparent; color: var(--text-faint); border: 1px dashed var(--border-default); }
.trig.add { background: transparent; border: 1px dashed var(--border-default); color: var(--text-body); cursor: default; gap: 6px; padding: 0 8px 0 11px; }
.trig.add input { width: 78px; background: transparent; border: none; outline: none; font: 600 11px var(--font-mono); color: var(--text-body); text-transform: uppercase; }
.trig.add svg { cursor: pointer; color: var(--accent-text); }
.si { cursor: pointer; }
.rf2 { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; }
.seg { display: flex; border: 1px solid var(--border-default); border-radius: 10px; overflow: hidden; }
.si { flex: 1; text-align: center; height: 38px; line-height: 38px; font: 600 12px var(--font-mono); color: var(--text-muted); border-left: 1px solid var(--border-subtle); }
.si:first-child { border-left: none; }
.si.active { background: var(--accent-subtle); color: var(--accent-text); }
.si.active.danger { background: var(--danger-subtle); color: var(--danger-text); }
.rffoot { display: flex; align-items: center; gap: 10px; padding: 14px 24px; border-top: 1px solid var(--border-subtle); background: var(--surface-raised); }
.rfhint { font: 500 11px var(--font-mono); color: var(--text-faint); }
.rfacts { margin-left: auto; display: flex; gap: 10px; }
</style>
