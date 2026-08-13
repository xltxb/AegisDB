<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Command, X, Plus, Pencil, Trash2, Keyboard } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import api from '@/api'
import { useSnippetStore } from '@/stores/snippets'
import { useUIStore } from '@/stores/ui'
import { SNIPPET_SLOTS, slotLabel, snippetPreview } from '@/lib/snippet'
import type { TerminalSnippet } from '@/types'

// Manage the operator's saved scripts and their hotkeys. Nothing here executes
// anything: the modal writes text, and pressing the key later submits that text
// through the terminal's normal path, where it is judged against whatever
// instance the session is on.

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ close: [] }>()

const { t } = useI18n()
const store = useSnippetStore()
const ui = useUIStore()

const editing = ref<TerminalSnippet | null>(null)
const isNew = ref(false)
const form = ref({ name: '', body: '', slot: 0 })
const saving = ref(false)
const err = ref('')

// The body cap is in bytes because the column is; a Chinese comment costs three
// bytes a character, so a character counter would read "well under the limit"
// on a body the server refuses.
const bodyBytes = computed(() => new TextEncoder().encode(form.value.body).length)
const overCap = computed(() => bodyBytes.value > store.limits.maxBytes)

watch(() => props.open, (o) => { if (o) { store.load().catch(() => {}); closeEditor() } })

function closeEditor() {
  editing.value = null
  isNew.value = false
  err.value = ''
}

function startNew() {
  isNew.value = true
  editing.value = null
  err.value = ''
  form.value = { name: '', body: '', slot: firstFreeSlot() }
}

function startEdit(s: TerminalSnippet) {
  isNew.value = false
  editing.value = s
  err.value = ''
  form.value = { name: s.name, body: s.body, slot: s.slot }
}

/** Suggest the lowest unused hotkey, or none when all nine are taken. */
function firstFreeSlot(): number {
  return SNIPPET_SLOTS.find((n) => !store.bySlot[n]) ?? 0
}

// Which snippet currently holds the slot the form is about to claim. Saying so
// up front matters: claiming a taken key releases the other one, and finding
// that out afterwards means finding out that a key you rely on stopped working.
const stealing = computed(() => {
  const s = form.value.slot
  if (!s) return null
  const holder = store.bySlot[s]
  if (!holder || holder.id === editing.value?.id) return null
  return holder
})

async function save() {
  if (saving.value) return
  err.value = ''
  saving.value = true
  try {
    const env = await api.snippetSave(editing.value?.id ?? 0, { ...form.value })
    if (env.code !== 0) { err.value = env.msg || t('snipSaveFail'); return }
    await store.refresh()
    closeEditor()
  } catch (e: any) {
    err.value = e?.msg || t('snipSaveFail')
  } finally {
    saving.value = false
  }
}

const confirmDel = ref(0)
async function remove(s: TerminalSnippet) {
  if (confirmDel.value !== s.id) { confirmDel.value = s.id; return }
  confirmDel.value = 0
  try {
    await api.snippetDelete(s.id)
    await store.refresh()
    if (editing.value?.id === s.id) closeEditor()
  } catch (e) {
    ui.notifyError(e, t('snipDelFail'))
  }
}
</script>

<template>
  <div v-if="open" class="snip-overlay">
    <div class="snip-mask" @click="emit('close')" />
    <div class="modal">
      <div class="topbar" />
      <div class="pad">
        <div class="hdr">
          <div class="ic"><Command :size="21" color="var(--accent-text)" /></div>
          <div class="grow">
            <div class="title">{{ $t('snipTitle') }}</div>
            <div class="desc">{{ $t('snipSub') }}</div>
          </div>
          <div class="x" @click="emit('close')"><X :size="18" /></div>
        </div>

        <!-- The one thing about this feature that is not obvious from using it. -->
        <div class="note"><Keyboard :size="14" /><span>{{ $t('snipJudgeNote') }}</span></div>
      </div>

      <div class="body">
        <div class="listcol">
          <div class="colhead">
            <span>{{ $t('snipSaved', { n: store.items.length, max: store.limits.max }) }}</span>
            <span class="new" :class="{ off: store.items.length >= store.limits.max }"
              @click="store.items.length < store.limits.max && startNew()"><Plus :size="12" />{{ $t('snipNew') }}</span>
          </div>
          <div v-if="!store.items.length" class="empty">{{ $t('snipEmpty') }}</div>
          <div v-for="s in store.items" :key="s.id" class="row" :class="{ sel: editing?.id === s.id }" @click="startEdit(s)">
            <span class="key" :class="{ none: !s.slot }">{{ s.slot ? slotLabel(s.slot) : '—' }}</span>
            <div class="rgrow">
              <div class="rname">{{ s.name }}</div>
              <div class="rprev">{{ snippetPreview(s.body) }}</div>
            </div>
            <span class="act" :title="$t('snipEdit')" @click.stop="startEdit(s)"><Pencil :size="13" /></span>
            <span class="act del" :class="{ armed: confirmDel === s.id }"
              :title="confirmDel === s.id ? $t('snipDelConfirm') : $t('snipDel')"
              @click.stop="remove(s)"><Trash2 :size="13" /></span>
          </div>
        </div>

        <div class="editcol">
          <template v-if="isNew || editing">
            <label class="lbl">{{ $t('snipName') }}</label>
            <input v-model="form.name" class="inp" :maxlength="store.limits.maxName" :placeholder="$t('snipNamePh')" />

            <label class="lbl">{{ $t('snipHotkey') }}</label>
            <div class="slots">
              <span class="slot" :class="{ on: form.slot === 0 }" @click="form.slot = 0">{{ $t('snipNoKey') }}</span>
              <span v-for="n in SNIPPET_SLOTS" :key="n" class="slot" :class="{ on: form.slot === n, taken: !!store.bySlot[n] && store.bySlot[n].id !== editing?.id }"
                @click="form.slot = n">{{ n }}</span>
            </div>
            <div v-if="stealing" class="steal">{{ $t('snipSteal', { name: stealing.name, key: slotLabel(form.slot) }) }}</div>

            <label class="lbl">
              {{ $t('snipBody') }}
              <span class="bytes" :class="{ over: overCap }">{{ bodyBytes }} / {{ store.limits.maxBytes }} B</span>
            </label>
            <textarea v-model="form.body" class="ta" spellcheck="false" :placeholder="$t('snipBodyPh')" />

            <div v-if="err" class="err">{{ err }}</div>
            <div class="acts">
              <VButton variant="secondary" @click="closeEditor">{{ $t('btnCancel') }}</VButton>
              <VButton variant="primary" :disabled="saving || overCap || !form.name.trim() || !form.body.trim()" @click="save">
                {{ $t('snipSave') }}
              </VButton>
            </div>
          </template>
          <div v-else class="placeholder">
            <Command :size="26" />
            <div>{{ $t('snipPickOne') }}</div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.snip-overlay { position: fixed; inset: 0; z-index: 55; }
.snip-mask { position: absolute; inset: 0; background: rgba(4, 6, 12, 0.7); backdrop-filter: blur(3px); }
.modal {
  position: absolute; left: 50%; top: 50%; transform: translate(-50%, -50%);
  width: 860px; max-width: 95vw; max-height: 88vh; display: flex; flex-direction: column;
  background: var(--surface-card); border: 1px solid var(--border-default); border-radius: 18px;
  box-shadow: 0 30px 80px -20px rgba(0, 0, 0, 0.7); overflow: hidden;
  animation: modalIn 0.26s cubic-bezier(0.16, 1, 0.3, 1);
}
.topbar { height: 4px; background: linear-gradient(90deg, #3b6ef6, #2dcde6); }
.pad { padding: 20px 24px 0; }
.hdr { display: flex; align-items: flex-start; gap: 13px; }
.ic { width: 40px; height: 40px; border-radius: 11px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; flex-shrink: 0; }
.grow { flex: 1; }
.title { font: 700 17px var(--font-display); color: var(--text-strong); letter-spacing: -0.02em; }
.desc { margin-top: 3px; font: 400 12.5px/1.5 var(--font-body); color: var(--text-muted); }
.x { width: 30px; height: 30px; border-radius: 8px; display: flex; align-items: center; justify-content: center; cursor: pointer; color: var(--text-faint); }
.x:hover { background: var(--surface-sunken); }
.note { margin-top: 14px; display: flex; align-items: flex-start; gap: 9px; padding: 10px 13px; border-radius: 10px; background: var(--accent-subtle); color: var(--accent-text); }
.note span { font: 500 12px/1.5 var(--font-body); }
.note svg { flex-shrink: 0; margin-top: 1px; }

.body { flex: 1; min-height: 0; display: grid; grid-template-columns: 320px 1fr; gap: 0; margin-top: 16px; border-top: 1px solid var(--border-subtle); }
.listcol { border-right: 1px solid var(--border-subtle); overflow-y: auto; padding: 10px; }
.colhead { display: flex; align-items: center; justify-content: space-between; padding: 4px 6px 8px; font: 600 10.5px var(--font-mono); color: var(--text-faint); text-transform: uppercase; letter-spacing: 0.05em; }
.new { display: inline-flex; align-items: center; gap: 4px; color: var(--accent-text); cursor: pointer; text-transform: none; letter-spacing: 0; }
.new.off { opacity: 0.4; cursor: not-allowed; }
.empty { padding: 26px 10px; text-align: center; font: 500 12px var(--font-mono); color: var(--text-faint); }
.row { display: flex; align-items: center; gap: 9px; padding: 9px 10px; border-radius: 9px; cursor: pointer; }
.row:hover { background: var(--surface-sunken); }
.row.sel { background: var(--accent-subtle); }
.key { flex-shrink: 0; min-width: 46px; text-align: center; padding: 3px 6px; border: 1px solid var(--accent-subtle-border); border-radius: 6px; background: var(--surface-card); font: 700 10.5px var(--font-mono); color: var(--accent-text); }
.key.none { border-color: var(--border-subtle); color: var(--text-faint); }
.rgrow { flex: 1; min-width: 0; }
.rname { font: 600 12.5px var(--font-body); color: var(--text-strong); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.rprev { margin-top: 2px; font: 500 11px var(--font-mono); color: var(--text-faint); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.act { flex-shrink: 0; width: 24px; height: 24px; border-radius: 6px; display: flex; align-items: center; justify-content: center; color: var(--text-faint); }
.act:hover { background: var(--surface-card); color: var(--accent-text); }
.act.del:hover { color: var(--danger-text); }
.act.del.armed { background: var(--danger-subtle); color: var(--danger-text); }

.editcol { display: flex; flex-direction: column; padding: 14px 20px 18px; overflow-y: auto; }
.lbl { display: flex; align-items: baseline; justify-content: space-between; margin: 10px 0 6px; font: 600 11px var(--font-mono); color: var(--text-faint); text-transform: uppercase; letter-spacing: 0.05em; }
.lbl:first-child { margin-top: 0; }
.bytes { font: 500 10.5px var(--font-mono); color: var(--text-faint); text-transform: none; letter-spacing: 0; }
.bytes.over { color: var(--danger-text); }
.inp { height: 34px; padding: 0 11px; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-sunken); color: var(--text-strong); font: 500 13px var(--font-body); outline: none; }
.inp:focus { border-color: var(--accent-text); }
.slots { display: flex; flex-wrap: wrap; gap: 6px; }
.slot { min-width: 30px; height: 28px; padding: 0 9px; display: inline-flex; align-items: center; justify-content: center; border: 1px solid var(--border-default); border-radius: 7px; background: var(--surface-sunken); font: 600 11.5px var(--font-mono); color: var(--text-body); cursor: pointer; }
.slot:hover { border-color: var(--accent-subtle-border); }
.slot.taken { color: var(--warning-text); border-color: rgba(245, 165, 36, 0.35); }
.slot.on { background: var(--accent); border-color: transparent; color: #fff; }
.steal { margin-top: 7px; font: 500 11.5px/1.5 var(--font-body); color: var(--warning-text); }
.ta { flex: 1; min-height: 190px; resize: vertical; padding: 10px 12px; border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-sunken); color: var(--text-strong); font: 500 12.5px/1.6 var(--font-mono); outline: none; white-space: pre; overflow-wrap: normal; overflow-x: auto; }
.ta:focus { border-color: var(--accent-text); }
.err { margin-top: 9px; font: 600 12px var(--font-body); color: var(--danger-text); }
.acts { margin-top: 14px; display: flex; justify-content: flex-end; gap: 10px; }
.placeholder { flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 10px; color: var(--text-faint); font: 500 12.5px var(--font-body); }
</style>
