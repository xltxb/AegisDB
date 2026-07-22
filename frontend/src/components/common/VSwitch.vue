<script setup lang="ts">
const props = defineProps<{ modelValue: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [boolean] }>()
function toggle() { emit('update:modelValue', !props.modelValue) }
</script>

<template>
  <button class="vsw" :class="{ on: modelValue }" type="button" @click="toggle">
    <span class="knob" />
  </button>
</template>

<style scoped>
.vsw {
  width: 44px;
  height: 24px;
  border-radius: 999px;
  border: none;
  cursor: pointer;
  background: var(--surface-sunken);
  border: 1px solid var(--border-default);
  position: relative;
  transition: background 0.18s ease, border-color 0.18s ease;
  padding: 0;
}
.vsw.on { background: var(--accent); border-color: var(--accent); }
.knob {
  position: absolute;
  top: 2px;
  left: 2px;
  width: 18px;
  height: 18px;
  border-radius: 50%;
  background: #fff;
  transition: transform 0.18s ease;
}
.vsw.on .knob { transform: translateX(20px); }
</style>
