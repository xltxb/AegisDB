<script setup lang="ts">
// Global toast host — renders transient success/error/info messages pushed via
// the ui store's notify()/notifyError(). Mounted once at the app root.
import { useUIStore } from '@/stores/ui'

const ui = useUIStore()
</script>

<template>
  <div class="toast-host" aria-live="polite">
    <div
      v-for="t in ui.toasts"
      :key="t.id"
      class="toast"
      :class="`toast--${t.kind}`"
      role="status"
      @click="ui.dismissToast(t.id)"
    >
      {{ t.msg }}
    </div>
  </div>
</template>

<style scoped>
.toast-host {
  position: fixed;
  top: 16px;
  right: 16px;
  z-index: 9999;
  display: flex;
  flex-direction: column;
  gap: 8px;
  pointer-events: none;
}
.toast {
  pointer-events: auto;
  cursor: pointer;
  min-width: 220px;
  max-width: 380px;
  padding: 10px 14px;
  border-radius: 8px;
  font-size: 13px;
  line-height: 1.4;
  color: #fff;
  box-shadow: 0 6px 20px rgba(0, 0, 0, 0.25);
  background: #334155;
}
.toast--success {
  background: #16a34a;
}
.toast--error {
  background: #dc2626;
}
.toast--info {
  background: #334155;
}
</style>
