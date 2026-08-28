<script setup lang="ts">
// 敏感字段 —— 按 表名 + 字段名 定义,命中的列在**结果离开网关之前**就被打码。
//
// 这个面板只维护规则。脱敏本身在服务端做,前端拿到的数据已经是打码后的:
// 让前端去打码等于把明文发到浏览器再请它别显示 —— 抓个包、开个 DevTools 就绕过了,
// 而且它已经躺在浏览器缓存和沿途任何代理的日志里。这条不是实现细节,是这个功能
// 全部的意义,所以面板上也把它写出来。
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { EyeOff, Plus, Pencil, Trash2, X, Check } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSwitch from '@/components/common/VSwitch.vue'
import api from '@/api'
import { confirmAction } from '@/lib/confirm'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import type { SensitiveColumn } from '@/types'

const { t } = useI18n()
const ui = useUIStore()
const auth = useAuthStore()
const isAdmin = computed(() => auth.me?.roleCode === 'admin' || (auth.me?.roleCodes || []).includes('admin'))

const rows = ref<SensitiveColumn[]>([])
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
    if (env.code !== 0) { ui.notifyError(new Error(env.msg), t('actionFailed')); return }
    editing.value = null
    await load()
  } catch (e) { ui.notifyError(e, t('actionFailed')) }
}

// 停用/删除都会让该字段**从此明文回传**,所以两个动作都要先确认一次。
async function toggle(r: SensitiveColumn) {
  if (!isAdmin.value) return
  if (r.enabled && !confirmAction(t('scDisableConfirm', { col: r.tableName + '.' + r.columnName }))) return
  try {
    const env = await api.saveSensitiveColumn(r.id, { ...r, enabled: !r.enabled })
    if (env.code !== 0) { ui.notifyError(new Error(env.msg), t('actionFailed')); return }
    await load()
  } catch (e) { ui.notifyError(e, t('actionFailed')) }
}

async function remove(r: SensitiveColumn) {
  if (!isAdmin.value) return
  if (!confirmAction(t('scDelConfirm', { col: r.tableName + '.' + r.columnName }))) return
  try {
    const env = await api.deleteSensitiveColumn(r.id)
    if (env.code !== 0) { ui.notifyError(new Error(env.msg), t('actionFailed')); return }
    await load()
  } catch (e) { ui.notifyError(e, t('actionFailed')) }
}
</script>

<template>
  <div class="sc">
    <div class="schead">
      <div class="scic"><EyeOff :size="18" color="var(--accent-text)" /></div>
      <div class="grow">
        <div class="sct">{{ $t('scTitle') }}</div>
        <div class="scs">{{ $t('scSub') }}</div>
      </div>
      <VButton v-if="isAdmin" variant="primary" height="34px" @click="openNew"><Plus :size="14" />{{ $t('scNew') }}</VButton>
    </div>

    <div class="list">
      <div v-for="r in rows" :key="r.id" class="item" :class="{ off: !r.enabled }">
        <div class="grow">
          <div class="itop">
            <span class="icol"><span class="dim">{{ r.tableName }}</span>.{{ r.columnName }}</span>
            <span class="style">{{ $t('scStyle_' + r.maskStyle) }}</span>
          </div>
          <div v-if="r.note" class="inote">{{ r.note }}</div>
        </div>
        <VSwitch :model-value="r.enabled" :disabled="!isAdmin" @update:model-value="toggle(r)" />
        <template v-if="isAdmin">
          <button class="act" :title="$t('edit')" @click="openEdit(r)"><Pencil :size="14" /></button>
          <button class="act del" :title="$t('delete')" @click="remove(r)"><Trash2 :size="14" /></button>
        </template>
      </div>
      <div v-if="!rows.length" class="empty">{{ $t('scEmpty') }}</div>
    </div>

    <div v-if="editing" class="overlay">
      <div class="mask" @click="editing = null" />
      <div class="modal">
        <div class="mhead">
          <div class="mic"><EyeOff :size="17" color="var(--accent-text)" /></div>
          <div><div class="mt">{{ editing.id ? $t('scEdit') : $t('scNew') }}</div><div class="ms">{{ $t('scSub') }}</div></div>
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
.list { padding: 14px 20px; display: flex; flex-direction: column; gap: 8px; }
.item { display: flex; align-items: center; gap: 12px; padding: 10px 12px; border: 1px solid var(--border-default); border-radius: 11px; background: var(--surface-sunken); }
.item.off { opacity: 0.55; }
.itop { display: flex; align-items: center; gap: 9px; }
.icol { font: 600 12.5px var(--font-mono); color: var(--text-strong); }
.dim { color: var(--text-faint); }
.style { padding: 1px 7px; border-radius: 999px; background: var(--accent-subtle); color: var(--accent-text); font: 600 10.5px var(--font-body); }
.inote { margin-top: 3px; font: 500 11.5px var(--font-body); color: var(--text-muted); }
.act { width: 30px; height: 28px; flex-shrink: 0; border: 1px solid var(--border-default); border-radius: 7px; background: var(--surface-card); color: var(--text-body); cursor: pointer; display: flex; align-items: center; justify-content: center; }
.act:hover { border-color: var(--accent-text); color: var(--accent-text); }
.act.del:hover { border-color: var(--danger-text); color: var(--danger-text); }
.empty { padding: 22px 0; text-align: center; font: 500 12px var(--font-body); color: var(--text-faint); }
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
</style>
