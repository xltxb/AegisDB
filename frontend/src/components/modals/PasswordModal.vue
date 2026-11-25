<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { LockKeyhole, X } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import api from '@/api'
import { useAuthStore } from '@/stores/auth'
import { useUIStore } from '@/stores/ui'

// 自助修改密码(黑盒 2026-10 工单 05)。成功后服务端作废这个人的其它会话,并为当前会话
// 重签一枚 token —— 这里换上它,人不必重新登录。
const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ close: [] }>()

const { t } = useI18n()
const auth = useAuthStore()
const ui = useUIStore()

const oldPw = ref('')
const newPw = ref('')
const confirmPw = ref('')
const err = ref('')
const busy = ref(false)

watch(() => props.open, (open) => {
  if (!open) return
  oldPw.value = ''
  newPw.value = ''
  confirmPw.value = ''
  err.value = ''
})

// 与服务端同一条规则(至少 8 位);服务端仍会再判一次,这里只是让人少等一个来回。
function check(): string {
  if (!oldPw.value) return t('pwNeedOld')
  if (newPw.value.length < 8) return t('pwTooShort')
  if (newPw.value === oldPw.value) return t('pwSameAsOld')
  if (newPw.value !== confirmPw.value) return t('pwMismatch')
  return ''
}

async function submit() {
  if (busy.value) return
  err.value = check()
  if (err.value) return
  busy.value = true
  try {
    const r = await api.changePassword(oldPw.value, newPw.value)
    auth.replaceToken(r.session)
    ui.notify(t('pwChanged'), 'success', 6000)
    emit('close')
  } catch (e: any) {
    err.value = e?.message || t('pwChangeFailed')
  } finally { busy.value = false }
}
</script>

<template>
  <div v-if="open" class="pw-overlay" data-testid="password-modal">
    <div class="pw-mask" @click="emit('close')" />
    <div class="modal">
      <div class="topbar" />
      <div class="pad">
        <div class="hdr">
          <div class="ic"><LockKeyhole :size="20" color="var(--accent-text)" /></div>
          <div class="grow">
            <div class="title">{{ $t('pwTitle') }}</div>
            <div class="desc">{{ $t('pwDesc') }}</div>
          </div>
          <div class="x" @click="emit('close')"><X :size="18" /></div>
        </div>
        <label class="fl">{{ $t('pwOld') }}</label>
        <input v-model="oldPw" v-autofocus class="pin" type="password" autocomplete="current-password" data-testid="pw-old" />
        <label class="fl">{{ $t('pwNew') }}</label>
        <input v-model="newPw" class="pin" type="password" autocomplete="new-password" :placeholder="$t('pwNewPh')" data-testid="pw-new" />
        <label class="fl">{{ $t('pwConfirm') }}</label>
        <input v-model="confirmPw" class="pin" type="password" autocomplete="new-password" data-testid="pw-confirm" @keyup.enter="submit" />
        <div v-if="err" class="err" data-testid="pw-err">{{ err }}</div>
      </div>
      <div class="footer">
        <VButton variant="secondary" @click="emit('close')">{{ $t('btnCancel') }}</VButton>
        <VButton variant="primary" :disabled="busy" data-testid="pw-submit" @click="submit">{{ busy ? $t('pwSaving') : $t('pwSubmit') }}</VButton>
      </div>
    </div>
  </div>
</template>

<style scoped>
.pw-overlay { position: fixed; inset: 0; z-index: 60; }
.pw-mask { position: absolute; inset: 0; background: rgba(4, 6, 12, 0.7); backdrop-filter: blur(3px); }
.modal {
  position: absolute; left: 50%; top: 50%; transform: translate(-50%, -50%);
  width: 420px; max-width: 94vw; max-height: calc(100vh - 56px); display: flex; flex-direction: column;
  background: var(--surface-card); border: 1px solid var(--border-default); border-radius: 18px;
  box-shadow: 0 30px 80px -20px rgba(0, 0, 0, 0.7); overflow: hidden;
  animation: modalIn 0.24s cubic-bezier(0.16, 1, 0.3, 1);
}
.topbar { height: 4px; background: linear-gradient(90deg, #3b6ef6, #2dcde6); flex-shrink: 0; }
.pad { padding: 20px 24px 4px; flex: 1; min-height: 0; overflow-y: auto; }
.hdr { display: flex; align-items: flex-start; gap: 13px; margin-bottom: 6px; }
.ic { width: 40px; height: 40px; border-radius: 11px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; flex-shrink: 0; }
.grow { flex: 1; }
.title { font: 700 16px var(--font-display); color: var(--text-strong); letter-spacing: -0.01em; }
.desc { margin-top: 3px; font: 400 12px/1.5 var(--font-body); color: var(--text-muted); }
.x { width: 30px; height: 30px; border-radius: 8px; display: flex; align-items: center; justify-content: center; cursor: pointer; color: var(--text-faint); }
.x:hover { background: var(--surface-sunken); }
.fl { display: block; margin-top: 14px; font: 600 11px var(--font-mono); color: var(--text-muted); }
.pin {
  margin-top: 6px; width: 100%; height: 38px; padding: 0 12px; border: 1px solid var(--border-default); border-radius: 10px;
  background: var(--surface-sunken); color: var(--text-strong); font: 500 13px var(--font-body); outline: none;
}
.pin:focus { border-color: var(--accent-text); }
.err { margin-top: 10px; font: 600 12px var(--font-body); color: var(--danger-text); }
.footer { margin-top: 16px; flex-shrink: 0; display: flex; justify-content: flex-end; gap: 10px; padding: 14px 24px; border-top: 1px solid var(--border-subtle); background: var(--surface-raised); }
</style>
