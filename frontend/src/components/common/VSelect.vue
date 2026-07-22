<script setup lang="ts">
import { ref } from 'vue'
import { ChevronDown } from 'lucide-vue-next'

const props = defineProps<{ modelValue: string; options: string[] }>()
const emit = defineEmits<{ 'update:modelValue': [string] }>()
const open = ref(false)

function pick(o: string) {
  emit('update:modelValue', o)
  open.value = false
}
function onBlur() {
  // delay so click registers
  setTimeout(() => (open.value = false), 120)
}
</script>

<template>
  <div class="vsel" tabindex="0" @blur="onBlur">
    <div class="control" @click="open = !open">
      <span>{{ modelValue }}</span>
      <ChevronDown :size="14" class="chev" />
    </div>
    <div v-if="open" class="menu scy">
      <div
        v-for="o in options"
        :key="o"
        class="opt"
        :class="{ active: o === modelValue }"
        @click="pick(o)"
      >
        {{ o }}
      </div>
    </div>
  </div>
</template>

<style scoped>
.vsel { position: relative; outline: none; }
.control {
  height: 40px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-md);
  background: var(--surface-sunken);
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 12px;
  font: 400 13px var(--font-mono);
  color: var(--text-body);
  cursor: pointer;
}
.chev { color: var(--text-faint); }
.menu {
  position: absolute;
  z-index: 30;
  top: 44px;
  left: 0;
  right: 0;
  max-height: 220px;
  background: var(--surface-card);
  border: 1px solid var(--border-default);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-lg);
  padding: 5px;
}
.opt {
  padding: 8px 11px;
  border-radius: 8px;
  font: 400 13px var(--font-mono);
  color: var(--text-body);
  cursor: pointer;
}
.opt:hover { background: var(--surface-sunken); }
.opt.active { background: var(--accent-subtle); color: var(--accent-text); }
</style>
