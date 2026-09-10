<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { TriangleAlert, Hammer, X, CircleCheck, CircleX } from 'lucide-vue-next'
import api from '@/api'
import { confirmAction } from '@/lib/confirm'
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

async function load() {
  if (!props.oracle) return
  err.value = ''
  try {
    const r = await api.invalidObjects(props.cid, props.scope, props.database || '')
    items.value = r.items || []
  } catch (e: any) {
    // 清点失败要说出来,而不是静默当成"没有" —— 静默的表现是这一行不出现,
    // 读起来正好是"这个 schema 很干净"。
    items.value = []
    err.value = e?.message || t('invLoadFail')
  }
}
watch(() => [props.cid, props.scope, props.database, props.oracle], load, { immediate: true })

async function recompileAll() {
  if (busy.value || !items.value?.length) return
  if (!confirmAction(t('invConfirm', { n: items.value.length, schema: items.value[0].owner }))) return
  busy.value = true
  try {
    const r = await api.recompileInvalid(props.cid, { scope: props.scope, database: props.database || '' })
    if (r.code !== 0) {
      ui.notify(r.msg || t('actionFailed'), 'error', 6000)
      return
    }
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
      <button class="inv-btn" :disabled="busy" @click.stop="recompileAll">
        <Hammer :size="11" />{{ busy ? $t('invRunning') : $t('invRecompile') }}
      </button>
    </div>
    <div v-for="o in items" :key="o.kind + o.name" class="inv-obj" :title="o.units.join(' · ')">
      <span class="io-kind">{{ o.kind }}</span>{{ o.name }}
    </div>
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
        <span class="rc-meta">{{ $t('invPasses', { n: report.passes }) }} · {{ report.ms }}ms</span>
      </div>
      <div class="rc-body scy">
        <div v-for="it in report.items" :key="it.kind + it.name" class="rc-item" :class="it.status.toLowerCase()">
          <div class="ri-head">
            <span class="ri-status" :class="it.status.toLowerCase()">{{ it.status }}</span>
            <span class="ri-kind">{{ it.kind }}</span>
            <span class="ri-name">{{ it.name }}</span>
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
.inv-obj { display: flex; align-items: baseline; gap: 6px; padding: 3px 8px 3px 22px; font: 400 11.5px var(--font-mono); color: var(--text-muted); }
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
.rc-meta { margin-left: auto; font: 500 10.5px var(--font-mono); color: var(--text-faint); }
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
</style>
