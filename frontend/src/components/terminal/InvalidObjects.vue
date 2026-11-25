<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { TriangleAlert, Hammer, X, CircleCheck, CircleX, Layers } from 'lucide-vue-next'
import api from '@/api'
import { confirmAction } from '@/lib/confirm'
import { CODE_MFA_REQUIRED } from '@/api/http'
import { useUIStore } from '@/stores/ui'
import type { InvalidObject, RecompileReport } from '@/types'

// 一个 Oracle schema 下的 INVALID 对象,以及"一键全编"。
//
// 只在**有**无效对象时出现。一行"0 个无效对象"挂在每个 schema 下面,在一棵已经很密的
// 树里是纯噪音;而这一行是红的、有数字、能点 —— 它出现本身就是那条信息。
//
// 编译是一批 DDL,所以点下去之前要确认:后端会把整批一起判定,判不过整批都不执行,
// 但"我以为只是刷新一下清单"这种误触,得在前端就挡住。
const props = defineProps<{ cid: number; scope: string; database?: string; oracle: boolean }>()
const { t } = useI18n()
const ui = useUIStore()

const items = ref<InvalidObject[] | null>(null) // null = 还没查 / 正在查
const err = ref('')
const busy = ref(false)
const report = ref<RecompileReport | null>(null)
// 生产实例上批量重编译同样要二次验证 —— 它一次改的对象比单个编译更多,没有理由更宽松。
// 后端答 42800 时亮出输码框重试,而不是把它当成一条普通错误。
const needMfa = ref(false)
const mfaCode = ref('')

// 勾选:全量重编译是变更窗口里的动作,勾几个是定点修复 —— 后端早就分这两种
// (RecompileReq.names),界面上原来只给了"全部"。勾了就编勾的,一个没勾就是全部。
const picked = ref<Set<string>>(new Set())
const keyOf = (o: InvalidObject) => o.kind + '|' + o.name
function toggle(o: InvalidObject) {
  const k = keyOf(o)
  const next = new Set(picked.value)
  if (next.has(k)) next.delete(k)
  else next.add(k)
  picked.value = next
}
const selected = computed(() => (items.value || []).filter((o) => picked.value.has(keyOf(o))))

async function load() {
  if (!props.oracle) return
  err.value = ''
  try {
    const r = await api.invalidObjects(props.cid, props.scope, props.database || '')
    items.value = r.items || []
    // 清单变了,勾选只保留还在清单里的:编好的那几个不该还"勾着"。
    const alive = new Set(items.value.map(keyOf))
    picked.value = new Set([...picked.value].filter((k) => alive.has(k)))
  } catch (e: any) {
    // 清点失败要说出来,而不是静默当成"没有" —— 静默的表现是这一行不出现,
    // 读起来正好是"这个 schema 很干净"。
    items.value = []
    err.value = e?.message || t('invLoadFail')
  }
}
watch(() => [props.cid, props.scope, props.database, props.oracle], load, { immediate: true })

async function recompile() {
  if (busy.value || !items.value?.length) return
  const targets = selected.value.length ? selected.value : items.value
  const schema = items.value[0].owner
  const msg = selected.value.length
    ? t('invConfirmSel', { n: targets.length, total: items.value.length, schema })
    : t('invConfirm', { n: targets.length, schema })
  if (!(await confirmAction(msg))) return
  busy.value = true
  try {
    const code = mfaCode.value.trim()
    const r = await api.recompileInvalid(props.cid, {
      scope: props.scope, database: props.database || '',
      names: selected.value.length ? targets.map((o) => o.name) : undefined,
      mfaCode: code,
    })
    if (r.code === CODE_MFA_REQUIRED) {
      // 输过一次还回 42800 就是码不对;第一次则只是亮出输入框,不该报成错误。
      if (code) ui.notify(t('mfaBadCode'), 'error', 4000)
      needMfa.value = true
      mfaCode.value = ''
      return
    }
    if (r.code !== 0) {
      ui.notify(r.msg || t('actionFailed'), 'error', 6000)
      return
    }
    needMfa.value = false
    mfaCode.value = ''
    report.value = r.data.report
    await load() // 清单按编译后的真实状态重取,而不是按"我以为都修好了"更新
  } catch (e: any) {
    ui.notifyError(e, t('actionFailed'))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div v-if="oracle && err" class="inv-hint err">{{ err }}</div>
  <div v-else-if="oracle && items && items.length" class="inv">
    <div class="inv-row">
      <TriangleAlert :size="12" />
      <span class="inv-t">{{ $t('invTitle') }}</span>
      <span class="inv-n">{{ items.length }}</span>
      <button class="inv-btn" :disabled="busy" data-testid="inv-recompile" @click.stop="recompile">
        <Hammer :size="11" />{{ busy ? $t('invRunning') : selected.length ? $t('invRecompileSel', { n: selected.length }) : $t('invRecompile') }}
      </button>
    </div>
    <!-- 生产实例上要二次验证:窄而安静的一条,它是流程里的一步,不是一次报错。 -->
    <div v-if="needMfa" class="inv-mfa">
      <span class="inv-mfa-t">{{ $t('mfaStepTitle') }}</span>
      <input
        v-model="mfaCode" class="inv-mfa-i" inputmode="numeric" maxlength="6"
        :placeholder="$t('mfaCodeLabel')" @click.stop @keyup.enter="recompile" />
      <button class="inv-mfa-b" :disabled="busy || mfaCode.trim().length !== 6" @click.stop="recompile">
        {{ $t('mfaVerify') }}
      </button>
    </div>
    <label v-for="o in items" :key="o.kind + o.name" class="inv-obj" :class="{ on: picked.has(keyOf(o)) }" :title="o.units.join(' · ')" @click.stop>
      <input type="checkbox" class="io-ck" :checked="picked.has(keyOf(o))" @change="toggle(o)" />
      <span class="io-kind">{{ o.kind }}</span>{{ o.name }}
    </label>
  </div>

  <!-- 结果。逐个列,而不是只给一句"完成" —— 编不过的那几个才是要看的东西。 -->
  <div v-if="report" class="rc-overlay" @click.self="report = null">
    <div class="rc-mask" />
    <div class="rc-modal">
      <div class="rc-hdr">
        <div class="rc-title">{{ $t('invReportTitle', { schema: report.owner }) }}</div>
        <div class="rc-x" @click="report = null"><X :size="16" /></div>
      </div>
      <div class="rc-sum">
        <span class="rc-chip ok"><CircleCheck :size="12" />{{ $t('invFixed', { n: report.fixed }) }}</span>
        <span class="rc-chip" :class="report.failed ? 'bad' : ''"><CircleX :size="12" />{{ $t('invFailed', { n: report.failed }) }}</span>
        <span v-if="report.skipped" class="rc-chip warn">{{ $t('invSkipped', { n: report.skipped }) }}</span>
        <span v-if="report.cyclic" class="rc-chip warn" :title="$t('invCyclicTip')">{{ $t('invCyclic', { n: report.cyclic }) }}</span>
        <span class="rc-meta"><Layers :size="11" />{{ $t('invLevels', { n: report.levels }) }} · {{ $t('invPasses', { n: report.passes }) }} · {{ report.ms }}ms</span>
      </div>
      <!-- 依赖图没读到:这次没有分层,只靠多轮。人看到"3 轮还剩很多"时要知道这一点。 -->
      <div v-if="report.depsNote" class="rc-note">{{ $t('invDepsNote') }}<span class="rc-note-d">{{ report.depsNote }}</span></div>
      <div class="rc-body scy">
        <div v-for="it in report.items" :key="it.kind + it.name" class="rc-item" :class="it.status.toLowerCase()">
          <div class="ri-head">
            <span class="ri-status" :class="it.status.toLowerCase()">{{ it.status }}</span>
            <span class="ri-kind">{{ it.kind }}</span>
            <span class="ri-name">{{ it.name }}</span>
            <span class="ri-lv" :class="{ cyc: it.cyclic }" :title="it.cyclic ? $t('invCyclicTip') : ''">{{ $t('invLevel', { n: it.level }) }}<template v-if="it.passes > 1"> · {{ $t('invPassN', { n: it.passes }) }}</template></span>
          </div>
          <div v-if="it.err" class="ri-err">{{ it.err }}</div>
          <div v-for="(d, i) in (it.errors || [])" :key="i" class="ri-diag">
            <span class="ri-loc">{{ d.type }} {{ d.line }}:{{ d.position }}</span>{{ d.text }}
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.inv-hint { padding: 4px 8px; font: 400 11.5px var(--font-body); color: var(--text-faint); }
.inv-hint.err { color: var(--danger-text); }
.inv { margin: 2px 0 4px; }
.inv-row { display: flex; align-items: center; gap: 6px; padding: 4px 8px; border-radius: 7px; background: var(--danger-subtle); color: var(--danger-text); font: 600 11.5px var(--font-body); }
.inv-n { font: 700 10px var(--font-mono); }
.inv-btn { margin-left: auto; display: inline-flex; align-items: center; gap: 4px; height: 20px; padding: 0 8px; border: 1px solid var(--danger-text); border-radius: 6px; background: transparent; color: var(--danger-text); font: 600 10.5px var(--font-body); cursor: pointer; }
.inv-btn:hover:not(:disabled) { background: var(--danger-text); color: var(--surface-card); }
.inv-btn:disabled { opacity: 0.6; cursor: default; }
.inv-obj { display: flex; align-items: baseline; gap: 6px; padding: 3px 8px 3px 10px; font: 400 11.5px var(--font-mono); color: var(--text-muted); cursor: pointer; border-radius: 5px; }
.inv-obj:hover { background: var(--surface-sunken); }
.inv-obj.on { color: var(--text-body); }
.io-ck { margin: 0; width: 12px; height: 12px; align-self: center; accent-color: var(--danger-text); cursor: pointer; }
.io-kind { font: 600 9.5px var(--font-mono); color: var(--text-faint); text-transform: uppercase; }

.rc-overlay { position: fixed; inset: 0; z-index: 55; display: flex; align-items: center; justify-content: center; }
.rc-mask { position: absolute; inset: 0; background: rgba(0, 0, 0, 0.45); }
.rc-modal { position: relative; width: min(720px, 92vw); max-height: 80vh; display: flex; flex-direction: column; background: var(--surface-card); border: 1px solid var(--border-subtle); border-radius: 12px; overflow: hidden; }
.rc-hdr { display: flex; align-items: center; padding: 12px 16px; border-bottom: 1px solid var(--border-subtle); }
.rc-title { font: 600 13px var(--font-body); color: var(--text-strong); }
.rc-x { margin-left: auto; color: var(--text-faint); cursor: pointer; display: flex; }
.rc-x:hover { color: var(--text-strong); }
.rc-sum { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; padding: 10px 16px; border-bottom: 1px solid var(--border-subtle); }
.rc-chip { display: inline-flex; align-items: center; gap: 4px; padding: 2px 8px; border-radius: 999px; background: var(--surface-sunken); color: var(--text-muted); font: 600 11px var(--font-body); }
.rc-chip.ok { background: var(--success-subtle); color: var(--success-text); }
.rc-chip.bad { background: var(--danger-subtle); color: var(--danger-text); }
.rc-chip.warn { background: var(--warning-subtle); color: var(--warning-text); }
.rc-meta { margin-left: auto; display: inline-flex; align-items: center; gap: 4px; font: 500 10.5px var(--font-mono); color: var(--text-faint); }
.rc-note { padding: 8px 16px; border-bottom: 1px solid var(--border-subtle); background: var(--warning-subtle); color: var(--warning-text); font: 500 11.5px var(--font-body); }
.rc-note-d { display: block; margin-top: 2px; font: 400 11px var(--font-mono); opacity: 0.85; }
.ri-lv { margin-left: auto; padding: 1px 6px; border-radius: 5px; font: 600 9.5px var(--font-mono); background: var(--surface-sunken); color: var(--text-muted); white-space: nowrap; }
.ri-lv.cyc { background: var(--warning-subtle); color: var(--warning-text); }
.rc-body { flex: 1; min-height: 0; overflow: auto; padding: 8px 16px 14px; }
.rc-item { padding: 8px 0; border-bottom: 1px solid var(--border-subtle); }
.rc-item:last-child { border-bottom: none; }
.ri-head { display: flex; align-items: baseline; gap: 8px; }
.ri-status { padding: 1px 6px; border-radius: 5px; font: 700 9.5px var(--font-mono); background: var(--surface-sunken); color: var(--text-muted); }
.ri-status.valid { background: var(--success-subtle); color: var(--success-text); }
.ri-status.invalid, .ri-status.error { background: var(--danger-subtle); color: var(--danger-text); }
.ri-kind { font: 600 9.5px var(--font-mono); color: var(--text-faint); text-transform: uppercase; }
.ri-name { font: 500 12px var(--font-mono); color: var(--text-body); }
.ri-err { margin-top: 4px; font: 500 11.5px var(--font-mono); color: var(--danger-text); }
.ri-diag { margin-top: 4px; font: 500 11.5px var(--font-mono); color: var(--danger-text); white-space: pre-wrap; }
.ri-loc { display: inline-block; min-width: 132px; color: var(--text-muted); }

.inv-mfa { display: flex; align-items: center; gap: 6px; flex-wrap: wrap;
  padding: 6px 8px; margin: 4px 0; border-radius: 6px;
  background: var(--warning-soft, rgba(234,179,8,.10));
  border: 1px solid var(--warning-border, rgba(234,179,8,.35)); }
.inv-mfa-t { font-size: 11px; font-weight: 600; color: var(--text); }
.inv-mfa-i { width: 84px; padding: 3px 6px; font: inherit; font-size: 12px;
  letter-spacing: 2px; text-align: center; border-radius: 5px;
  border: 1px solid var(--border); background: var(--surface); color: var(--text); }
.inv-mfa-b { padding: 3px 10px; font-size: 11px; border-radius: 5px; cursor: pointer;
  border: 1px solid var(--accent); background: var(--accent); color: #fff; }
.inv-mfa-b:disabled { opacity: .5; cursor: not-allowed; }
</style>
