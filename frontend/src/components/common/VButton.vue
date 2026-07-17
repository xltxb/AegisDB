<script setup lang="ts">
withDefaults(defineProps<{
  variant?: 'primary' | 'secondary' | 'danger'
  fullWidth?: boolean
  height?: string
  disabled?: boolean
}>(), { variant: 'secondary', fullWidth: false, height: '40px', disabled: false })
defineEmits<{ click: [MouseEvent] }>()
</script>

<template>
  <button
    class="vbtn"
    :class="[`v-${variant}`, { full: fullWidth }]"
    :style="{ height }"
    :disabled="disabled"
    @click="$emit('click', $event)"
  >
    <slot />
  </button>
</template>

<style scoped>
.vbtn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 7px;
  padding: 0 16px;
  border-radius: var(--radius-md);
  font: 600 13px var(--font-body);
  cursor: pointer;
  border: 1px solid transparent;
  white-space: nowrap;
  transition: all 0.15s ease;
}
.vbtn.full { width: 100%; }
.vbtn:disabled { opacity: 0.5; cursor: not-allowed; }
.v-primary {
  background: var(--accent);
  color: #fff;
  box-shadow: 0 6px 18px -6px rgba(59, 110, 246, 0.5);
}
.v-primary:hover:not(:disabled) { background: var(--accent-hover); }
.v-secondary {
  background: transparent;
  color: var(--text-body);
  border-color: var(--border-default);
}
.v-secondary:hover:not(:disabled) { background: var(--surface-sunken); }
.v-danger {
  background: var(--danger-subtle);
  color: var(--danger-text);
  border-color: rgba(240, 71, 62, 0.4);
}
.v-danger:hover:not(:disabled) { background: rgba(240, 71, 62, 0.26); }
</style>
