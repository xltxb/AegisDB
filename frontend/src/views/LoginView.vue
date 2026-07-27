<script setup lang="ts">
import { ref, nextTick } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Sailboat, Mail, Lock, Languages, ShieldCheck } from 'lucide-vue-next'
import { useAuthStore } from '@/stores/auth'
import { useUIStore } from '@/stores/ui'
import { CODE_MFA_REQUIRED } from '@/api/http'
import VButton from '@/components/common/VButton.vue'

const router = useRouter()
const route = useRoute()
const auth = useAuthStore()
const ui = useUIStore()
const { t } = useI18n()

// Prefill AND advertise the demo credentials only in a dev build — never ship
// them in a production bundle (L5).
const isDev = import.meta.env.DEV
const email = ref(isDev ? 'linwei@vela.io' : '')
const password = ref(isDev ? 'vela123' : '')
const loading = ref(false)
// Two-step login: once the password is accepted for an MFA-enrolled user, the
// backend answers CODE_MFA_REQUIRED and we reveal the TOTP field.
const mfaRequired = ref(false)
const mfaCode = ref('')
const mfaInput = ref<HTMLInputElement | null>(null)
// Surface the idle-lock notice when redirected here by the auto-lock.
const error = ref(route.query.locked ? t('mfaLocked') : '')

async function submit() {
  error.value = ''
  loading.value = true
  try {
    await auth.login(email.value.trim(), password.value, mfaCode.value.trim())
    const dest = auth.firstVisibleRoute
    if (!dest) {
      // Logged in but the role has no visible menu — don't push('') (which would
      // silently stay on the login page), tell the user (V5).
      error.value = '登录成功,但当前角色没有可访问的菜单,请联系管理员'
      return
    }
    router.push(dest)
  } catch (e: any) {
    if (e?.code === CODE_MFA_REQUIRED) {
      // A submitted-but-wrong code carries a message; the first prompt does not.
      mfaRequired.value = true
      error.value = mfaCode.value ? (e.message || t('loginMfaError')) : ''
      mfaCode.value = ''
      // Move the cursor straight to the code field so the user can type at once.
      nextTick(() => mfaInput.value?.focus())
      return
    }
    error.value = t('loginError')
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="login">
    <div class="lang" @click="ui.toggleLang()"><Languages :size="14" />{{ $t('langLabel') }}</div>
    <div class="card">
      <div class="brand">
        <div class="logo"><Sailboat :size="22" color="#fff" /></div>
        <div>
          <div class="bn">DP DB GATEWAY</div>
          <div class="bs">数据库管理网关</div>
        </div>
      </div>
      <div class="title">{{ $t('loginTitle') }}</div>
      <div class="sub">{{ $t('loginSub') }}</div>

      <label class="lbl">{{ $t('loginEmail') }}</label>
      <div class="field"><Mail :size="15" color="var(--text-faint)" /><input v-model="email" type="email" autocomplete="username" @keyup.enter="submit" /></div>

      <label class="lbl">{{ $t('loginPassword') }}</label>
      <div class="field"><Lock :size="15" color="var(--text-faint)" /><input v-model="password" type="password" autocomplete="current-password" @keyup.enter="submit" /></div>

      <template v-if="mfaRequired">
        <label class="lbl">{{ $t('loginMfaLabel') }}</label>
        <div class="field"><ShieldCheck :size="15" color="var(--text-faint)" /><input ref="mfaInput" v-model="mfaCode" inputmode="numeric" autocomplete="one-time-code" maxlength="6" :placeholder="$t('loginMfaPh')" @keyup.enter="submit" /></div>
      </template>

      <div v-if="error" class="err">{{ error }}</div>

      <VButton variant="primary" :full-width="true" :disabled="loading" @click="submit">
        {{ loading ? '…' : $t('loginBtn') }}
      </VButton>
      <div v-if="isDev" class="hint">{{ $t('loginHint') }}</div>
    </div>
  </div>
</template>

<style scoped>
.login {
  height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  background:
    radial-gradient(900px 500px at 50% -10%, rgba(59, 110, 246, 0.18), transparent 60%),
    var(--surface-page);
}
.lang {
  position: absolute;
  top: 22px;
  right: 26px;
  display: inline-flex;
  align-items: center;
  gap: 5px;
  height: 30px;
  padding: 0 12px;
  border: 1px solid var(--border-default);
  border-radius: 999px;
  cursor: pointer;
  font: 600 12px var(--font-mono);
  color: var(--text-body);
}
.card {
  width: 380px;
  background: var(--surface-card);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-xl);
  padding: 30px 32px 26px;
  box-shadow: var(--shadow-xl);
}
.brand { display: flex; align-items: center; gap: 11px; margin-bottom: 22px; }
.logo {
  width: 38px;
  height: 38px;
  border-radius: 10px;
  background: linear-gradient(135deg, #3b6ef6, #2dcde6);
  display: flex;
  align-items: center;
  justify-content: center;
}
.bn { font: 700 16px var(--font-display); color: var(--text-strong); }
.bs { font: 500 11px var(--font-mono); color: var(--text-faint); margin-top: 2px; }
.title { font: 700 22px var(--font-display); color: var(--text-strong); letter-spacing: -0.02em; }
.sub { font: 400 13px var(--font-body); color: var(--text-muted); margin: 4px 0 20px; }
.lbl { display: block; font: 500 11px var(--font-body); color: var(--text-faint); margin: 14px 0 6px; }
.field {
  display: flex;
  align-items: center;
  gap: 8px;
  height: 42px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-md);
  background: var(--surface-sunken);
  padding: 0 12px;
}
.field input {
  flex: 1;
  background: transparent;
  border: none;
  outline: none;
  color: var(--text-strong);
  font: 400 13px var(--font-mono);
}
.err { color: var(--danger-text); font: 500 12px var(--font-body); margin: 12px 0 0; }
.hint { text-align: center; font: 500 11px var(--font-mono); color: var(--text-faint); margin-top: 14px; }
:deep(.vbtn) { margin-top: 20px; height: 44px; }
</style>
