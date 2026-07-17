<script setup lang="ts">
import { ref, watch, computed } from 'vue'
import { Tags, X, Plus } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'

const props = defineProps<{ open: boolean; title: string; subtitle?: string; tags: string[]; suggestions?: string[] }>()
const emit = defineEmits<{ close: []; save: [string[]] }>()

const list = ref<string[]>([])
const input = ref('')

watch(() => props.open, (o) => { if (o) { list.value = [...props.tags]; input.value = '' } })

const norm = (s: string) => s.trim().toLowerCase().replace(/[,\s]+/g, '')
function add(raw: string) {
  const v = norm(raw)
  if (v && !list.value.includes(v)) list.value.push(v)
  input.value = ''
}
function remove(t: string) { list.value = list.value.filter((x) => x !== t) }
const freeSuggest = computed(() => (props.suggestions || []).filter((s) => !list.value.includes(s)))
</script>

<template>
  <div v-if="open" class="overlay">
    <div class="mask" @click="emit('close')" />
    <div class="modal">
      <div class="topbar" />
      <div class="pad">
        <div class="hdr">
          <div class="ic"><Tags :size="20" color="var(--accent-text)" /></div>
          <div class="grow"><div class="title">{{ title }}</div><div v-if="subtitle" class="desc">{{ subtitle }}</div></div>
          <div class="x" @click="emit('close')"><X :size="18" /></div>
        </div>

        <div class="chips">
          <span v-for="t in list" :key="t" class="chip">{{ t }}<span class="cx" @click="remove(t)"><X :size="11" /></span></span>
          <span v-if="!list.length" class="empty">{{ $t('tagNone') }}</span>
        </div>

        <div class="inrow">
          <input v-model="input" class="tin" :placeholder="$t('tagAddPh')" @keyup.enter="add(input)" />
          <button class="addb" :disabled="!norm(input)" @click="add(input)"><Plus :size="15" /></button>
        </div>

        <template v-if="freeSuggest.length">
          <div class="sl">{{ $t('tagSuggest') }}</div>
          <div class="sugs"><span v-for="s in freeSuggest" :key="s" class="sug" @click="add(s)"><Plus :size="11" />{{ s }}</span></div>
        </template>
      </div>

      <div class="footer">
        <VButton variant="secondary" @click="emit('close')">{{ $t('btnCancel') }}</VButton>
        <VButton variant="primary" @click="emit('save', list)">{{ $t('btnSave') }}</VButton>
      </div>
    </div>
  </div>
</template>

<style scoped>
.overlay { position: fixed; inset: 0; z-index: 70; }
.mask { position: absolute; inset: 0; background: rgba(4, 6, 12, 0.7); backdrop-filter: blur(3px); }
.modal {
  position: absolute; left: 50%; top: 50%; transform: translate(-50%, -50%);
  width: 440px; max-width: 94vw; display: flex; flex-direction: column;
  background: var(--surface-card); border: 1px solid var(--border-default); border-radius: 18px;
  box-shadow: 0 30px 80px -20px rgba(0, 0, 0, 0.7); overflow: hidden;
  animation: modalIn 0.24s cubic-bezier(0.16, 1, 0.3, 1);
}
.topbar { height: 4px; background: linear-gradient(90deg, #3b6ef6, #2dcde6); }
.pad { padding: 20px 24px 6px; }
.hdr { display: flex; align-items: flex-start; gap: 13px; }
.ic { width: 40px; height: 40px; border-radius: 11px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; flex-shrink: 0; }
.grow { flex: 1; min-width: 0; }
.title { font: 700 16px var(--font-display); color: var(--text-strong); }
.desc { margin-top: 3px; font: 400 12px/1.5 var(--font-body); color: var(--text-muted); }
.x { width: 30px; height: 30px; border-radius: 8px; display: flex; align-items: center; justify-content: center; cursor: pointer; color: var(--text-faint); }
.x:hover { background: var(--surface-sunken); }
.chips { margin-top: 16px; min-height: 40px; display: flex; flex-wrap: wrap; gap: 7px; padding: 10px 12px; border: 1px solid var(--border-subtle); border-radius: 10px; background: var(--surface-sunken); }
.chip { display: inline-flex; align-items: center; gap: 5px; height: 24px; padding: 0 6px 0 10px; border-radius: 999px; background: var(--accent-subtle); color: var(--accent-text); font: 600 12px var(--font-mono); }
.cx { display: flex; align-items: center; justify-content: center; width: 16px; height: 16px; border-radius: 50%; cursor: pointer; color: var(--accent-text); }
.cx:hover { background: rgba(255, 255, 255, 0.12); }
.empty { font: 500 12px var(--font-mono); color: var(--text-faint); align-self: center; }
.inrow { margin-top: 10px; display: flex; gap: 8px; }
.tin { flex: 1; height: 38px; padding: 0 12px; border: 1px solid var(--border-default); border-radius: 9px; background: var(--surface-sunken); color: var(--text-strong); font: 500 12.5px var(--font-mono); outline: none; }
.tin:focus { border-color: var(--accent-text); }
.addb { width: 40px; height: 38px; border: 1px solid var(--border-default); border-radius: 9px; background: var(--surface-sunken); color: var(--accent-text); cursor: pointer; display: flex; align-items: center; justify-content: center; }
.addb:disabled { color: var(--text-faint); cursor: default; }
.sl { margin-top: 15px; font: 600 11px var(--font-mono); color: var(--text-muted); }
.sugs { margin-top: 7px; display: flex; flex-wrap: wrap; gap: 7px; }
.sug { display: inline-flex; align-items: center; gap: 4px; height: 24px; padding: 0 9px 0 7px; border-radius: 999px; border: 1px solid var(--border-default); color: var(--text-muted); font: 600 11px var(--font-mono); cursor: pointer; }
.sug:hover { color: var(--accent-text); border-color: var(--accent-subtle-border); }
.footer { margin-top: 16px; display: flex; justify-content: flex-end; gap: 10px; padding: 14px 24px; border-top: 1px solid var(--border-subtle); background: var(--surface-raised); }
</style>
