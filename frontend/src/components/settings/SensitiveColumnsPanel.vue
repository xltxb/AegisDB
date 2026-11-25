<script setup lang="ts">
// 敏感字段 —— 按 表名 + 字段名 定义,命中的列在**结果离开网关之前**就被打码。
//
// 这个面板只维护规则。脱敏本身在服务端做,前端拿到的数据已经是打码后的:
// 让前端去打码等于把明文发到浏览器再请它别显示 —— 抓个包、开个 DevTools 就绕过了,
// 而且它已经躺在浏览器缓存和沿途任何代理的日志里。这条不是实现细节,是这个功能
// 全部的意义,所以面板上也把它写出来。
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { EyeOff, Plus, Pencil, Trash2, X, Check, Search } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSwitch from '@/components/common/VSwitch.vue'
import api from '@/api'
import { envError } from '@/api/http'
import { confirmAction } from '@/lib/confirm'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import type { SensitiveColumn } from '@/types'

const { t } = useI18n()
const ui = useUIStore()
const auth = useAuthStore()
const isAdmin = computed(() => auth.me?.roleCode === 'admin' || (auth.me?.roleCodes || []).includes('admin'))

const rows = ref<SensitiveColumn[]>([])

// ---- 找得到,才谈得上维护 ----
//
// 这一块从卡片列表改成紧凑表格(2026-09):二十条规则在原来的版式里要滚两屏,而每行右边
// 还空着一大片。真正的痛点不是好不好看,是**找不到** —— 想确认"phone 打没打码"只能拿眼
// 睛顺着扫。所以先给搜索与两个筛选,再谈密度。
const kw = ref('')
const styleFilter = ref<'' | SensitiveColumn['maskStyle']>('')
const stateFilter = ref<'' | 'on' | 'off'>('')
const filtering = computed(() => !!(kw.value.trim() || styleFilter.value || stateFilter.value))
function clearFilters() { kw.value = ''; styleFilter.value = ''; stateFilter.value = '' }

const offCount = computed(() => rows.value.filter((r) => !r.enabled).length)

const shown = computed(() => {
  const q = kw.value.trim().toLowerCase()
  return rows.value.filter((r) => {
    if (styleFilter.value && (r.maskStyle || 'partial') !== styleFilter.value) return false
    if (stateFilter.value === 'on' && !r.enabled) return false
    if (stateFilter.value === 'off' && r.enabled) return false
    if (!q) return true
    // 表名一起进搜索:规则多半写成 * ,但一旦有按表限定的,"这张表上都定了什么"是要问的。
    return `${r.tableName}.${r.columnName} ${r.note || ''}`.toLowerCase().includes(q)
  })
})
const editing = ref<{ id: number; tableName: string; columnName: string; maskStyle: SensitiveColumn['maskStyle']; note: string; enabled: boolean } | null>(null)

const styles: SensitiveColumn['maskStyle'][] = ['partial', 'full', 'hash']

async function load() {
  try { rows.value = await api.sensitiveColumns() } catch (e) { ui.notifyError(e, t('loadFailed')) }
}
onMounted(load)
defineExpose({ load })

function openNew() {
  editing.value = { id: 0, tableName: '', columnName: '', maskStyle: 'partial' as const, note: '', enabled: true }
}
function openEdit(r: SensitiveColumn) {
  editing.value = {
    id: r.id, tableName: r.tableName, columnName: r.columnName,
    maskStyle: r.maskStyle || 'partial', note: r.note || '', enabled: r.enabled,
  }
}

async function save() {
  const f = editing.value
  if (!f) return
  if (!f.columnName.trim()) { ui.notify(t('scNeedColumn'), 'error'); return }
  try {
    const env = await api.saveSensitiveColumn(f.id, {
      tableName: f.tableName.trim(), columnName: f.columnName.trim(),
      maskStyle: f.maskStyle, note: f.note.trim(), enabled: f.enabled,
    })
    if (env.code !== 0) { ui.notifyError(envError(env), t('actionFailed')); return }
    editing.value = null
    await load()
  } catch (e) { ui.notifyError(e, t('actionFailed')) }
}

// 停用/删除都会让该字段**从此明文回传**,所以两个动作都要先确认一次。
async function toggle(r: SensitiveColumn) {
  if (!isAdmin.value) return
  if (r.enabled && !(await confirmAction(t('scDisableConfirm', { col: r.tableName + '.' + r.columnName })))) return
  try {
    const env = await api.saveSensitiveColumn(r.id, { ...r, enabled: !r.enabled })
    if (env.code !== 0) { ui.notifyError(envError(env), t('actionFailed')); return }
    await load()
  } catch (e) { ui.notifyError(e, t('actionFailed')) }
}

async function remove(r: SensitiveColumn) {
  if (!isAdmin.value) return
  if (!(await confirmAction(t('scDelConfirm', { col: r.tableName + '.' + r.columnName })))) return
  try {
    const env = await api.deleteSensitiveColumn(r.id)
    if (env.code !== 0) { ui.notifyError(envError(env), t('actionFailed')); return }
    await load()
  } catch (e) { ui.notifyError(e, t('actionFailed')) }
}
</script>

<template>
  <div class="sc">
    <div class="schead">
      <div class="scic"><EyeOff :size="18" color="var(--accent-text)" /></div>
      <div class="grow">
        <div class="sct">{{ $t('sensTitle') }}</div>
        <div class="scs">{{ $t('sensSub') }}</div>
      </div>
      <VButton v-if="isAdmin" variant="primary" height="34px" @click="openNew"><Plus :size="14" />{{ $t('scNew') }}</VButton>
    </div>

    <!-- 工具栏。条数、搜索、两个筛选 —— 二十条以上时这一行才是入口 -->
    <div v-if="rows.length" class="tools">
      <span class="cnt">
        {{ $t('scCount', { n: rows.length }) }}
        <span v-if="offCount" class="cntoff">{{ $t('scCountOff', { n: offCount }) }}</span>
      </span>
      <div class="sbox">
        <Search :size="13" color="var(--text-faint)" />
        <input v-model="kw" :placeholder="$t('scSearchPh')" spellcheck="false">
        <button v-if="kw" class="sx" @click="kw = ''"><X :size="12" /></button>
      </div>
      <select v-model="styleFilter" class="fsel">
        <option value="">{{ $t('scFilterAllStyle') }}</option>
        <option v-for="s2 in styles" :key="s2" :value="s2">{{ $t('scStyle_' + s2) }}</option>
      </select>
      <select v-model="stateFilter" class="fsel">
        <option value="">{{ $t('scFilterAllState') }}</option>
        <option value="on">{{ $t('scOnlyOn') }}</option>
        <option value="off">{{ $t('scOnlyOff') }}</option>
      </select>
    </div>

    <div v-if="rows.length" class="tbl">
      <div class="th">
        <span>{{ $t('scColHead') }}</span>
        <span>{{ $t('scStyleHead') }}</span>
        <span>{{ $t('scNoteHead') }}</span>
        <span class="c">{{ $t('scStateHead') }}</span>
        <span />
      </div>
      <div v-for="r in shown" :key="r.id" class="tr" :class="{ off: !r.enabled }">
        <span class="colcell">
          <!-- 表名是 * 时说"全部表":一个裸星号读起来像没填完,而它恰恰是最要紧的一档 -->
          <span class="tb" :class="{ any: !r.tableName || r.tableName === '*' }">
            {{ !r.tableName || r.tableName === '*' ? $t('scAllTables') : r.tableName }}
          </span>
          <span class="col">{{ r.columnName }}</span>
          <span v-if="!r.enabled" class="offtag">{{ $t('scDisabledTag') }}</span>
        </span>
        <!-- 三种方式三种颜色。它们的后果差别很大(抹光 / 留头尾 / 同值同码),
             共用一个蓝胶囊等于把最该一眼分清的事藏起来 -->
        <span class="stcell">
          <span class="style" :class="'s-' + (r.maskStyle || 'partial')" :title="$t('scStyleHint_' + (r.maskStyle || 'partial'))">
            {{ $t('scStyle_' + (r.maskStyle || 'partial')) }}
          </span>
        </span>
        <span class="notecell" :title="r.note">{{ r.note || '—' }}</span>
        <span class="c"><VSwitch :model-value="r.enabled" :disabled="!isAdmin" @update:model-value="toggle(r)" /></span>
        <span class="acts">
          <template v-if="isAdmin">
            <button class="act" :title="$t('edit')" @click="openEdit(r)"><Pencil :size="13" /></button>
            <button class="act del" :title="$t('delete')" @click="remove(r)"><Trash2 :size="13" /></button>
          </template>
        </span>
      </div>
      <!-- 筛没了和本来就没有,出口不同:前者该给一个清空条件的按钮 -->
      <div v-if="!shown.length" class="empty">
        {{ $t('scNoMatch') }}
        <button class="eclear" @click="clearFilters">{{ $t('scClearFilters') }}</button>
      </div>
    </div>
    <div v-else class="empty">{{ $t('scEmpty') }}</div>

    <div v-if="editing" class="overlay">
      <div class="mask" @click="editing = null" />
      <div class="modal">
        <div class="mhead">
          <div class="mic"><EyeOff :size="17" color="var(--accent-text)" /></div>
          <div><div class="mt">{{ editing.id ? $t('scEdit') : $t('scNew') }}</div><div class="ms">{{ $t('sensSub') }}</div></div>
          <X :size="17" class="mx" @click="editing = null" />
        </div>
        <div class="mbody">
          <div class="row2">
            <div><div class="fl">{{ $t('scTable') }}</div><input v-model="editing.tableName" class="in" :placeholder="$t('scTablePh')" /></div>
            <div><div class="fl">{{ $t('scColumn') }}</div><input v-model="editing.columnName" class="in" :placeholder="$t('scColumnPh')" /></div>
          </div>
          <div>
            <div class="fl">{{ $t('scStyle') }}</div>
            <select v-model="editing.maskStyle" class="in">
              <option v-for="s in styles" :key="s" :value="s">{{ $t('scStyle_' + s) }} — {{ $t('scStyleHint_' + s) }}</option>
            </select>
          </div>
          <div><div class="fl">{{ $t('scNote') }}</div><input v-model="editing.note" class="in" :placeholder="$t('scNotePh')" /></div>
          <div class="hint">{{ $t('scServerSideHint') }}</div>
        </div>
        <div class="mfoot">
          <VButton @click="editing = null">{{ $t('btnCancel') }}</VButton>
          <VButton variant="primary" @click="save"><Check :size="14" />{{ $t('btnSave') }}</VButton>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.sc { border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-xs); overflow: hidden; }
.schead { display: flex; align-items: center; gap: 12px; padding: 16px 20px; border-bottom: 1px solid var(--border-subtle); }
.scic { width: 34px; height: 34px; border-radius: 10px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; }
.grow { flex: 1; min-width: 0; }
.sct { font: 600 14px var(--font-display); color: var(--text-strong); }
.scs { font: 500 12px var(--font-body); color: var(--text-muted); margin-top: 2px; }
/* ---- 工具栏 ---- */
.tools { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; padding: 12px 20px; border-bottom: 1px solid var(--border-subtle); }
.cnt { font: 600 12px var(--font-body); color: var(--text-muted); }
.cntoff { color: var(--warning-text); margin-left: 4px; }
.sbox { margin-left: auto; display: flex; align-items: center; gap: 7px; height: 30px; padding: 0 10px; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-card); }
.sbox input { width: 190px; border: none; outline: none; background: transparent; color: var(--text-strong); font: 500 12px var(--font-body); }
.sx { display: grid; place-items: center; width: 16px; height: 16px; border: none; border-radius: 4px; background: transparent; color: var(--text-faint); cursor: pointer; }
.sx:hover { color: var(--text-body); }
.fsel { height: 30px; padding: 0 8px; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-card); color: var(--text-body); font: 500 12px var(--font-body); cursor: pointer; outline: none; }

/* ---- 表格 ----
   .th 与每个 .tr 各自是一个 grid,所以列宽必须是与内容无关的定值 —— 写 auto 的话每行
   按自己那行的内容各算一套,表头和行就对不齐了。说明那一列吃掉剩下的宽度:原先每行
   右边空着一大片,而说明恰恰是最该展开的一格。 */
.th, .tr { display: grid; grid-template-columns: minmax(220px, 1.1fr) 96px minmax(160px, 1.6fr) 64px 74px; gap: 12px; align-items: center; padding: 0 20px; }
.th { height: 34px; background: var(--surface-sunken); border-bottom: 1px solid var(--border-subtle); font: 600 10.5px var(--font-body); color: var(--text-muted); text-transform: uppercase; letter-spacing: .05em; }
.th .c, .tr .c { text-align: center; justify-items: center; }
.tr { min-height: 40px; border-bottom: 1px solid var(--border-subtle); font: 500 12px var(--font-body); color: var(--text-body); }
.tr:last-of-type { border-bottom: none; }
.tr:hover { background: var(--surface-page); }
/* 停用的行不是"淡一点"就完了:整行降透明度会让它的开关也看不清,而那正是要点回去的
   那个控件。只灰掉字,并单独挂一个标记。 */
.tr.off .col, .tr.off .notecell { color: var(--text-faint); }

.colcell { display: flex; align-items: center; gap: 7px; min-width: 0; }
.tb { flex: none; padding: 1px 6px; border-radius: 5px; background: var(--surface-sunken); color: var(--text-muted); font: 600 10px var(--font-mono); border: 1px solid var(--border-subtle); }
.tb.any { background: transparent; color: var(--text-faint); border-style: dashed; }
.col { font: 600 12.5px var(--font-mono); color: var(--text-strong); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.offtag { flex: none; padding: 1px 6px; border-radius: 5px; background: var(--surface-sunken); color: var(--text-faint); font: 600 10px var(--font-body); }

.stcell { min-width: 0; }
.style { display: inline-block; padding: 2px 8px; border-radius: 999px; font: 600 10.5px var(--font-body); white-space: nowrap; cursor: default; }
/* 抹光最重 → 危险色;留头留尾 → 警示;同值同码是可核对的,最轻 → 强调色 */
.style.s-full { background: var(--danger-subtle); color: var(--danger-text); }
.style.s-partial { background: var(--warning-subtle); color: var(--warning-text); }
.style.s-hash { background: var(--accent-subtle); color: var(--accent-text); }

.notecell { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-muted); }
.acts { display: flex; justify-content: flex-end; gap: 6px; }
.act { width: 26px; height: 26px; flex-shrink: 0; border: 1px solid transparent; border-radius: 7px; background: transparent; color: var(--text-faint); cursor: pointer; display: flex; align-items: center; justify-content: center; }
.tr:hover .act { border-color: var(--border-subtle); color: var(--text-muted); }
.act:hover { border-color: var(--accent-text) !important; color: var(--accent-text) !important; }
.act.del:hover { border-color: var(--danger-text) !important; color: var(--danger-text) !important; }
.empty { padding: 26px 0; text-align: center; font: 500 12px var(--font-body); color: var(--text-faint); }
.eclear { margin-left: 10px; padding: 4px 10px; border: 1px solid var(--border-default); border-radius: 7px; background: var(--surface-card); color: var(--accent-text); font: 600 11.5px var(--font-body); cursor: pointer; }
.overlay { position: fixed; inset: 0; z-index: 80; display: flex; align-items: center; justify-content: center; padding: 24px; }
.mask { position: absolute; inset: 0; background: var(--surface-overlay); backdrop-filter: blur(3px); }
.modal { position: relative; width: 100%; max-width: 560px; border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-lg); overflow: hidden; }
.mhead { display: flex; align-items: center; gap: 11px; padding: 15px 18px; border-bottom: 1px solid var(--border-subtle); }
.mic { width: 32px; height: 32px; border-radius: 9px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; }
.mt { font: 600 14px var(--font-display); color: var(--text-strong); }
.ms { font: 500 11.5px var(--font-body); color: var(--text-muted); }
.mx { margin-left: auto; cursor: pointer; color: var(--text-faint); }
.mbody { padding: 16px 18px; display: flex; flex-direction: column; gap: 12px; }
.row2 { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
.fl { font: 600 11px var(--font-mono); letter-spacing: 0.06em; color: var(--text-faint); text-transform: uppercase; margin-bottom: 5px; }
.in { width: 100%; box-sizing: border-box; height: 36px; padding: 0 12px; border: 1px solid var(--border-default); border-radius: 9px; background: var(--surface-sunken); font: 500 12.5px var(--font-body); color: var(--text-strong); outline: none; }
.in:focus { border-color: var(--accent-text); box-shadow: 0 0 0 3px var(--accent-subtle); }
.hint { font: 500 11.5px var(--font-body); color: var(--text-muted); line-height: 1.6; }
.mfoot { display: flex; justify-content: flex-end; gap: 10px; padding: 13px 18px; border-top: 1px solid var(--border-subtle); }

/* 弹层封顶(见 main.css 的 .v-modal 三件套):内容再长,底下的按钮永远点得到。 */
.modal { display: flex; flex-direction: column; max-height: calc(100vh - 48px); }
.modal .mbody { flex: 1; min-height: 0; overflow-y: auto; }
.modal .mfoot { flex-shrink: 0; }
</style>
