<script setup lang="ts">
// 全局确认弹窗(lib/confirm.ts 的 confirmAction 驱动),替代浏览器原生 confirm。
// Enter 确认、Esc / 点遮罩取消;打开时焦点落在确认按钮上。
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { AlertTriangle, X } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import { confirmSegments, confirmState, settleConfirm } from '@/lib/confirm'

const okBox = ref<HTMLElement | null>(null)
const segments = computed(() => confirmSegments(confirmState.message))

watch(() => confirmState.open, async (open) => {
  if (!open) return
  await nextTick()
  okBox.value?.querySelector('button')?.focus()
})

function onKey(e: KeyboardEvent) {
  if (!confirmState.open) return
  if (e.key === 'Escape') { e.preventDefault(); settleConfirm(false) }
  else if (e.key === 'Enter' && !e.isComposing) { e.preventDefault(); settleConfirm(true) }
}
onMounted(() => window.addEventListener('keydown', onKey, true))
onBeforeUnmount(() => window.removeEventListener('keydown', onKey, true))
</script>

<template>
  <div v-if="confirmState.open" class="cf-overlay" data-testid="confirm-dialog">
    <div class="cf-mask" @click="settleConfirm(false)" />
    <div class="cf-modal" role="alertdialog" aria-modal="true" :class="{ danger: confirmState.danger }">
      <div class="cf-top" />
      <div class="cf-pad">
        <div class="cf-hdr">
          <div class="cf-ic"><AlertTriangle :size="20" /></div>
          <div class="cf-grow">
            <div class="cf-title">{{ confirmState.title || $t('confirmTitle') }}</div>
            <div class="cf-msg" data-testid="confirm-message">
              <template v-for="(s, i) in segments" :key="i">
                <code v-if="s.code" class="cf-code">{{ s.text }}</code>
                <template v-else>{{ s.text }}</template>
              </template>
            </div>
          </div>
          <div class="cf-x" @click="settleConfirm(false)"><X :size="18" /></div>
        </div>
      </div>
      <div class="cf-footer">
        <VButton variant="secondary" data-testid="confirm-cancel" @click="settleConfirm(false)">{{ $t('btnCancel') }}</VButton>
        <span ref="okBox">
          <VButton :variant="confirmState.danger ? 'danger' : 'primary'" data-testid="confirm-ok" @click="settleConfirm(true)">
            {{ confirmState.okText || $t('confirmOk') }}
          </VButton>
        </span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.cf-overlay { position: fixed; inset: 0; z-index: 90; }
.cf-mask { position: absolute; inset: 0; background: rgba(4, 6, 12, 0.6); backdrop-filter: blur(3px); }
.cf-modal {
  position: absolute; left: 50%; top: 50%; transform: translate(-50%, -50%);
  width: 460px; max-width: 94vw; max-height: calc(100vh - 56px); display: flex; flex-direction: column;
  background: var(--surface-card); border: 1px solid var(--border-default); border-radius: 18px;
  box-shadow: 0 30px 80px -20px rgba(0, 0, 0, 0.7); overflow: hidden;
  animation: modalIn 0.24s cubic-bezier(0.16, 1, 0.3, 1);
}
.cf-top { height: 4px; flex-shrink: 0; background: linear-gradient(90deg, #f5a524, #f7c948); }
.danger .cf-top { background: linear-gradient(90deg, #e5484d, #f17b7f); }
.cf-pad { padding: 20px 24px 18px; flex: 1; min-height: 0; overflow-y: auto; }
.cf-hdr { display: flex; align-items: flex-start; gap: 13px; }
.cf-ic {
  width: 40px; height: 40px; border-radius: 11px; flex-shrink: 0; display: flex; align-items: center; justify-content: center;
  background: var(--warning-subtle); color: var(--warning-text);
}
.danger .cf-ic { background: var(--danger-subtle); color: var(--danger-text); }
.cf-grow { flex: 1; min-width: 0; }
.cf-title { font: 700 16px var(--font-display); color: var(--text-strong); letter-spacing: -0.01em; line-height: 40px; }
.cf-msg { margin-top: 2px; font: 400 13px/1.75 var(--font-body); color: var(--text-body); white-space: pre-wrap; word-break: break-word; }
.cf-code {
  display: inline; padding: 2px 6px; margin: 0 2px; border-radius: 6px;
  background: var(--surface-sunken); border: 1px solid var(--border-subtle);
  font: 600 12px/1.9 var(--font-mono); color: var(--accent-text);
  word-break: break-all; box-decoration-break: clone; -webkit-box-decoration-break: clone;
}
.cf-x { width: 30px; height: 30px; border-radius: 8px; display: flex; align-items: center; justify-content: center; cursor: pointer; color: var(--text-faint); flex-shrink: 0; }
.cf-x:hover { background: var(--surface-sunken); }
.cf-footer { flex-shrink: 0; display: flex; justify-content: flex-end; gap: 10px; padding: 14px 24px; border-top: 1px solid var(--border-subtle); background: var(--surface-raised); }
</style>
