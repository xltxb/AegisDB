<script setup lang="ts">
import { ref, watch } from 'vue'
import QRCode from 'qrcode'
import { KeyRound, X, ShieldCheck } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import api from '@/api'

const props = defineProps<{ open: boolean; enabled: boolean }>()
const emit = defineEmits<{ close: []; changed: [] }>()

const secret = ref('')
const qr = ref('')
const code = ref('')
const err = ref('')
const busy = ref(false)

// When opening for a not-yet-enrolled user, fetch a fresh secret + render its QR.
watch(() => props.open, async (open) => {
  err.value = ''
  code.value = ''
  if (!open) return
  if (props.enabled) return
  try {
    const s = await api.mfaSetup()
    secret.value = s.secret
    qr.value = await QRCode.toDataURL(s.otpauthUri, { margin: 1, width: 176 })
  } catch { err.value = '初始化失败' }
})

async function enable() {
  if (code.value.trim().length !== 6) { err.value = '请输入 6 位验证码'; return }
  busy.value = true
  err.value = ''
  try {
    await api.mfaEnable(code.value.trim())
    emit('changed')
    emit('close')
  } catch (e: any) {
    err.value = e?.message || '验证码错误,请重试'
  } finally { busy.value = false }
}

async function disable() {
  if (code.value.trim().length !== 6) { err.value = '请输入当前 6 位验证码'; return }
  busy.value = true
  err.value = ''
  try {
    await api.mfaDisable(code.value.trim())
    emit('changed')
    emit('close')
  } catch (e: any) {
    err.value = e?.message || '验证码错误,请重试'
  } finally { busy.value = false }
}

const onlyDigits = (e: Event) => {
  const el = e.target as HTMLInputElement
  el.value = el.value.replace(/\D/g, '').slice(0, 6)
  code.value = el.value
}
</script>

<template>
  <div v-if="open" class="overlay">
    <div class="mask" @click="emit('close')" />
    <div class="modal">
      <div class="topbar" />
      <div class="pad">
        <div class="hdr">
          <div class="ic"><KeyRound :size="20" color="var(--accent-text)" /></div>
          <div class="grow">
            <div class="title">{{ enabled ? $t('mfaEnabledTitle') : $t('mfaModalTitle') }}</div>
            <div class="desc">{{ enabled ? $t('mfaEnabledDesc') : $t('mfaScan') }}</div>
          </div>
          <div class="x" @click="emit('close')"><X :size="18" /></div>
        </div>

        <!-- enrollment -->
        <template v-if="!enabled">
          <div class="enroll">
            <div class="qrbox"><img v-if="qr" :src="qr" alt="QR" /></div>
            <div class="skey">
              <div class="kl">{{ $t('mfaSecretLabel') }}</div>
              <code class="kv">{{ secret }}</code>
            </div>
          </div>
        </template>
        <div v-else class="okrow"><ShieldCheck :size="16" color="var(--success-text)" /><span>{{ $t('mfaEnabledActive') }}</span></div>

        <div class="cl">{{ enabled ? $t('mfaCurrentCode') : $t('mfaCodeLabel') }}</div>
        <input class="codein" inputmode="numeric" autocomplete="one-time-code" placeholder="000000"
               :value="code" @input="onlyDigits" @keyup.enter="enabled ? disable() : enable()" />
        <div v-if="err" class="err">{{ err }}</div>
      </div>

      <div class="footer">
        <VButton variant="secondary" @click="emit('close')">{{ $t('btnCancel') }}</VButton>
        <VButton v-if="enabled" variant="danger" :disabled="busy" @click="disable">{{ $t('mfaDisable') }}</VButton>
        <VButton v-else variant="primary" :disabled="busy" @click="enable">{{ $t('mfaEnable') }}</VButton>
      </div>
    </div>
  </div>
</template>

<style scoped>
.overlay { position: fixed; inset: 0; z-index: 60; }
.mask { position: absolute; inset: 0; background: rgba(4, 6, 12, 0.7); backdrop-filter: blur(3px); }
.modal {
  position: absolute; left: 50%; top: 50%; transform: translate(-50%, -50%);
  width: 420px; max-width: 94vw; display: flex; flex-direction: column;
  background: var(--surface-card); border: 1px solid var(--border-default); border-radius: 18px;
  box-shadow: 0 30px 80px -20px rgba(0, 0, 0, 0.7); overflow: hidden;
  animation: modalIn 0.24s cubic-bezier(0.16, 1, 0.3, 1);
}
.topbar { height: 4px; background: linear-gradient(90deg, #3b6ef6, #2dcde6); }
.pad { padding: 20px 24px 4px; }
.hdr { display: flex; align-items: flex-start; gap: 13px; }
.ic { width: 40px; height: 40px; border-radius: 11px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; flex-shrink: 0; }
.grow { flex: 1; }
.title { font: 700 16px var(--font-display); color: var(--text-strong); letter-spacing: -0.01em; }
.desc { margin-top: 3px; font: 400 12px/1.5 var(--font-body); color: var(--text-muted); }
.x { width: 30px; height: 30px; border-radius: 8px; display: flex; align-items: center; justify-content: center; cursor: pointer; color: var(--text-faint); }
.x:hover { background: var(--surface-sunken); }
.enroll { margin-top: 16px; display: flex; gap: 16px; align-items: center; }
.qrbox { width: 130px; height: 130px; border-radius: 10px; background: #fff; padding: 6px; display: flex; align-items: center; justify-content: center; flex-shrink: 0; }
.qrbox img { width: 100%; height: 100%; }
.skey { min-width: 0; }
.kl { font: 600 11px var(--font-mono); color: var(--text-faint); text-transform: uppercase; letter-spacing: 0.08em; }
.kv { display: block; margin-top: 6px; font: 600 12px var(--font-mono); color: var(--accent-text); word-break: break-all; line-height: 1.5; }
.okrow { margin-top: 16px; display: flex; align-items: center; gap: 8px; padding: 10px 12px; border-radius: 10px; background: var(--success-subtle); font: 600 12px var(--font-body); color: var(--success-text); }
.cl { margin-top: 18px; font: 600 11px var(--font-mono); color: var(--text-muted); }
.codein {
  margin-top: 8px; width: 100%; height: 44px; border: 1px solid var(--border-default); border-radius: 10px;
  background: var(--surface-sunken); color: var(--text-strong); text-align: center;
  font: 700 22px var(--font-mono); letter-spacing: 10px; outline: none;
}
.codein:focus { border-color: var(--accent-text); }
.err { margin-top: 8px; font: 600 12px var(--font-body); color: var(--danger-text); }
.footer { margin-top: 16px; display: flex; justify-content: flex-end; gap: 10px; padding: 14px 24px; border-top: 1px solid var(--border-subtle); background: var(--surface-raised); }
</style>
