<script setup lang="ts">
import { ref, watch } from 'vue'
import { Network, X } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSwitch from '@/components/common/VSwitch.vue'

const props = defineProps<{ open: boolean; enabled: boolean; list: string }>()
const emit = defineEmits<{ close: []; save: [{ enabled: boolean; list: string }] }>()

const on = ref(props.enabled)
const text = ref(props.list)

watch(() => props.open, (o) => {
  if (o) { on.value = props.enabled; text.value = props.list }
})

function save() {
  // normalize: entries split by newline/comma → single comma-separated string.
  const list = text.value.split(/[\n,]/).map((s) => s.trim()).filter(Boolean).join(', ')
  emit('save', { enabled: on.value, list })
}
</script>

<template>
  <div v-if="open" class="ipallow-overlay">
    <div class="ipallow-mask" @click="emit('close')" />
    <div class="modal">
      <div class="topbar" />
      <div class="pad">
        <div class="hdr">
          <div class="ic"><Network :size="20" color="var(--accent-text)" /></div>
          <div class="grow">
            <div class="title">{{ $t('ipModalTitle') }}</div>
            <div class="desc">{{ $t('ipModalSub') }}</div>
          </div>
          <div class="x" @click="emit('close')"><X :size="18" /></div>
        </div>

        <div class="enrow">
          <div><div class="rt">{{ $t('ipEnable') }}</div><div class="rd">{{ $t('ipEnableD') }}</div></div>
          <VSwitch v-model="on" />
        </div>

        <div class="cl">{{ $t('ipListLabel') }}</div>
        <textarea v-model="text" class="area" rows="5" placeholder="10.20.0.0/16&#10;203.0.113.5" />
        <div class="hint">{{ $t('ipHint') }}</div>
      </div>

      <div class="footer">
        <VButton variant="secondary" @click="emit('close')">{{ $t('btnCancel') }}</VButton>
        <VButton variant="primary" @click="save">{{ $t('btnSave') }}</VButton>
      </div>
    </div>
  </div>
</template>

<style scoped>
.ipallow-overlay { position: fixed; inset: 0; z-index: 60; }
.ipallow-mask { position: absolute; inset: 0; background: rgba(4, 6, 12, 0.7); backdrop-filter: blur(3px); }
.modal {
  position: absolute; left: 50%; top: 50%; transform: translate(-50%, -50%);
  width: 460px; max-width: 94vw; display: flex; flex-direction: column;
  background: var(--surface-card); border: 1px solid var(--border-default); border-radius: 18px;
  box-shadow: 0 30px 80px -20px rgba(0, 0, 0, 0.7); overflow: hidden;
  animation: modalIn 0.24s cubic-bezier(0.16, 1, 0.3, 1);
}
.topbar { height: 4px; background: linear-gradient(90deg, #3b6ef6, #2dcde6); }
.pad { padding: 20px 24px 4px; }
.hdr { display: flex; align-items: flex-start; gap: 13px; }
.ic { width: 40px; height: 40px; border-radius: 11px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; flex-shrink: 0; }
.grow { flex: 1; }
.title { font: 700 16px var(--font-display); color: var(--text-strong); }
.desc { margin-top: 3px; font: 400 12px/1.5 var(--font-body); color: var(--text-muted); }
.x { width: 30px; height: 30px; border-radius: 8px; display: flex; align-items: center; justify-content: center; cursor: pointer; color: var(--text-faint); }
.x:hover { background: var(--surface-sunken); }
.enrow { margin-top: 16px; display: flex; align-items: center; gap: 16px; padding: 12px 14px; border: 1px solid var(--border-subtle); border-radius: 11px; background: var(--surface-sunken); }
.enrow .rt { font: 600 13px var(--font-body); color: var(--text-strong); }
.enrow .rd { font: 500 11px var(--font-mono); color: var(--text-muted); margin-top: 2px; }
.enrow > div:first-child { flex: 1; }
.cl { margin-top: 16px; font: 600 11px var(--font-mono); color: var(--text-muted); }
.area {
  margin-top: 8px; width: 100%; border: 1px solid var(--border-default); border-radius: 10px;
  background: var(--surface-sunken); color: var(--text-strong); padding: 10px 12px; resize: vertical;
  font: 500 12.5px var(--font-mono); line-height: 1.7; outline: none;
}
.area:focus { border-color: var(--accent-text); }
.hint { margin-top: 8px; font: 500 11px var(--font-mono); color: var(--text-faint); }
.footer { margin-top: 16px; display: flex; justify-content: flex-end; gap: 10px; padding: 14px 24px; border-top: 1px solid var(--border-subtle); background: var(--surface-raised); }
</style>
