<script setup lang="ts">
import { ref, nextTick, onMounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Mail, Lock, Languages, ShieldCheck, KeyRound, ShieldX } from 'lucide-vue-next'
import { useAuthStore, SSO_SKIP_KEY } from '@/stores/auth'
import { useUIStore } from '@/stores/ui'
import { CODE_MFA_REQUIRED } from '@/api/http'
import { CODE_SSO_NO_SESSION, CODE_SSO_NO_ACCOUNT } from '@/api/codes'
import { srdLoginHref, justReturnedFromSrd, SRD_REDIRECTED_KEY } from '@/lib/srdLogin'
import api from '@/api'
import type { SsoConfig } from '@/types'
import VButton from '@/components/common/VButton.vue'
import BrandMark from '@/components/BrandMark.vue'
import { brand, brandText } from '@/lib/brand'

const router = useRouter()
const route = useRoute()
const auth = useAuthStore()
const ui = useUIStore()
const { t } = useI18n()

// Prefill AND advertise the demo credentials only in a dev build — never ship
// them in a production bundle (L5).
const isDev = import.meta.env.DEV
const email = ref(isDev ? 'linwei@aegisdb.io' : '')
const password = ref(isDev ? 'aegisdb123' : '')
const loading = ref(false)
// Two-step login: once the password is accepted for an MFA-enrolled user, the
// backend answers CODE_MFA_REQUIRED and we reveal the TOTP field.
const mfaRequired = ref(false)
const mfaCode = ref('')
const mfaInput = ref<HTMLInputElement | null>(null)
// Surface the idle-lock notice when redirected here by the auto-lock.
const error = ref(route.query.locked ? t('mfaLocked') : '')

// ---------------------------------------------------------------- SRD 授权登录(公司 SSO)
// SSO 只管"你是谁"。网关里没有这个邮箱(管理员没开通)时服务端拒绝(40302),这里弹窗
// 拦下并说清"无权限访问、找管理员开通";有账号但还没分配角色的,提示"等管理员分配"。
const sso = ref<SsoConfig | null>(null)
const ssoBusy = ref(false)
const noAccount = ref(false)
// 本地表单:未启用 SSO 时照常展开;启用后默认收起,点一下才出来 —— 还没绑定 SSO 的人和
// 管理员(应急)仍要用它,但它不该是一进来就看到的那扇门。
const showLocal = ref(true)
// 已绑定 TOTP 的管理员走 SSO 也要输码(服务端答 42800):在 SSO 按钮下亮出输码框,带码再换一次会话。
const ssoMfa = ref(false)
const ssoMfaCode = ref('')
const ssoMfaInput = ref<HTMLInputElement | null>(null)

function afterLogin(viaSSO: boolean) {
  // firstVisibleRoute 在这里只用来回答一个问题:这个人有没有任何一处能去。
  // 有,就送去总览 —— 落地页不该是 Web 命令行,那是这个产品里唯一能改动真实数据
  // 的地方。总览本身不设菜单闸,所以送得过去。
  if (!auth.firstVisibleRoute) {
    // Logged in but the role has no visible menu — don't push('') (which would
    // silently stay on the login page), tell the user (V5).
    error.value = viaSSO ? t('ssoNoRole') : t('loginNoMenu')
    return
  }
  // 只挂业务查询角色的人(业务方)只有「数据查询」一页,送去那里而不是总览。
  router.push(auth.onlyQuery ? '/query' : '/dashboard')
}

// 跳去 SRD 授权。回来时落在本页,onMounted 里的静默尝试会用 SRD 写下的 CS-Token 换会话。
function goSRD() {
  if (!sso.value?.loginUrl) return
  try {
    // 人主动要走 SSO:登出时设的"别自动登录"就该撤掉,否则授权回来什么都不发生。
    sessionStorage.removeItem(SSO_SKIP_KEY)
    sessionStorage.setItem(SRD_REDIRECTED_KEY, String(Date.now()))
  } catch { /* 隐私模式 */ }
  window.location.href = srdLoginHref(sso.value.loginUrl, window.location)
}

// auto:进登录页时静默试一次。没有登录态(40101)时什么都不说,露出按钮就够了。
// 手动点按钮:浏览器里已有 SRD 登录态就直接进来;没有、或者已过期被 SRD 拒绝,就去 SRD 授权。
async function trySSO(auto: boolean, mfaCode = '') {
  if (!sso.value?.enabled || ssoBusy.value) return
  error.value = ''
  ssoBusy.value = true
  let returned = false
  try {
    returned = justReturnedFromSrd(sessionStorage.getItem(SRD_REDIRECTED_KEY), Date.now())
    sessionStorage.removeItem(SRD_REDIRECTED_KEY)
  } catch { /* 隐私模式 */ }
  try {
    await auth.loginSSO(mfaCode)
    afterLogin(true)
  } catch (e: any) {
    if (e?.code === CODE_MFA_REQUIRED) {
      // 第一次只是在要码,不带说明;带了码还被拒才显示原因
      ssoMfa.value = true
      error.value = mfaCode ? (e.message || t('loginMfaError')) : ''
      ssoMfaCode.value = ''
      nextTick(() => ssoMfaInput.value?.focus())
      return
    }
    if (e?.code === CODE_SSO_NO_ACCOUNT) {
      noAccount.value = true
      return
    }
    const noSession = e?.code === CODE_SSO_NO_SESSION
    if (auto && noSession && returned) {
      // 刚在 SRD 授权完,网关却没收到 CS-Token:再点一次只会来回跳。把原因说出来。
      error.value = t('ssoNoCookie')
      return
    }
    if (!auto && (noSession || e?.code === 40100)) {
      goSRD()
      return
    }
    if (!noSession) error.value = e?.message || t('loginError')
  } finally {
    ssoBusy.value = false
  }
}

onMounted(async () => {
  try { sso.value = await api.ssoConfig() } catch { sso.value = null }
  if (!sso.value?.enabled) return
  showLocal.value = false
  // 主动登出后回到这里不自动登录(否则"登出"按下去一眨眼又登进来了);
  // 空闲锁定跳回来的也不自动 —— 锁定的意义就是要人重新确认一次。
  let skip = false
  try { skip = sessionStorage.getItem(SSO_SKIP_KEY) === '1' } catch { /* 隐私模式 */ }
  if (!skip && !route.query.locked) trySSO(true)
})

async function submit() {
  error.value = ''
  loading.value = true
  try {
    await auth.login(email.value.trim(), password.value, mfaCode.value.trim())
    afterLogin(false)
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
    // 已启用 SSO、非管理员用本地口令时,服务端给的是一句明确的"请使用 SSO 登录" ——
    // 换成通用的"邮箱或密码错误"会让人去反复试口令。
    error.value = sso.value?.enabled && e?.message ? e.message : t('loginError')
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
        <div class="logo" :class="{ custom: !!brand.logo }"><BrandMark :size="22" /></div>
        <div>
          <div class="bn" data-testid="login-brand-name">{{ brandText(ui.lang) }}</div>
          <div class="bs">{{ $t('loginTitle') }}</div>
        </div>
      </div>
      <div class="title">{{ $t('loginTitle') }}</div>
      <div class="sub">{{ $t('loginSub') }}</div>

      <!-- SRD 授权登录(公司 SSO):启用时它是主入口 -->
      <template v-if="sso?.enabled">
        <VButton variant="primary" :full-width="true" :disabled="ssoBusy" class="ssobtn" data-testid="sso-login" @click="trySSO(false)">
          <KeyRound :size="15" />{{ ssoBusy ? $t('ssoTrying') : $t('ssoBtn') }}
        </VButton>
        <template v-if="ssoMfa">
          <label class="lbl">{{ $t('loginMfaLabel') }}</label>
          <div class="field"><ShieldCheck :size="15" color="var(--text-faint)" /><input ref="ssoMfaInput" v-model="ssoMfaCode" data-testid="sso-mfa-code" inputmode="numeric" autocomplete="one-time-code" maxlength="6" :placeholder="$t('loginMfaPh')" @keyup.enter="trySSO(false, ssoMfaCode.trim())" /></div>
          <VButton variant="primary" :full-width="true" :disabled="ssoBusy || !ssoMfaCode.trim()" class="ssobtn" data-testid="sso-mfa-submit" @click="trySSO(false, ssoMfaCode.trim())">
            {{ $t('ssoMfaSubmit') }}
          </VButton>
        </template>
        <div v-if="!showLocal" class="ssoalt">
          <span class="ssolink" data-testid="sso-admin-toggle" @click="showLocal = true">{{ $t('ssoAdminToggle') }}</span>
        </div>
        <div v-else class="ssosep"><span>{{ $t('ssoOr') }}</span></div>
        <div v-if="showLocal" class="ssohint">{{ $t('ssoAdminHint') }}</div>
      </template>

      <template v-if="showLocal">
      <label class="lbl">{{ $t('loginEmail') }}</label>
      <div class="field"><Mail :size="15" color="var(--text-faint)" /><input v-model="email" type="email" autocomplete="username" @keyup.enter="submit" /></div>

      <label class="lbl">{{ $t('loginPassword') }}</label>
      <div class="field"><Lock :size="15" color="var(--text-faint)" /><input v-model="password" type="password" autocomplete="current-password" @keyup.enter="submit" /></div>

      <template v-if="mfaRequired">
        <label class="lbl">{{ $t('loginMfaLabel') }}</label>
        <div class="field"><ShieldCheck :size="15" color="var(--text-faint)" /><input ref="mfaInput" v-model="mfaCode" inputmode="numeric" autocomplete="one-time-code" maxlength="6" :placeholder="$t('loginMfaPh')" @keyup.enter="submit" /></div>
      </template>

      </template>

      <div v-if="error" class="err" data-testid="login-error">{{ error }}</div>

      <VButton v-if="showLocal" :variant="sso?.enabled ? 'secondary' : 'primary'" :full-width="true" :disabled="loading" @click="submit">
        {{ loading ? '…' : $t('loginBtn') }}
      </VButton>
      <div v-if="isDev" class="hint">{{ $t('loginHint') }}</div>
    </div>

    <Teleport to="body">
      <div v-if="noAccount" class="ovl" data-testid="sso-no-account" @click.self="noAccount = false">
        <div class="dlg" role="alertdialog" aria-modal="true" :aria-label="$t('ssoNoAccountTitle')">
          <div class="dlgic"><ShieldX :size="22" color="var(--danger-text)" /></div>
          <div class="dlgt">{{ $t('ssoNoAccountTitle') }}</div>
          <div class="dlgb">{{ $t('ssoNoAccountBody') }}</div>
          <VButton variant="primary" :full-width="true" data-testid="sso-no-account-ok" @click="noAccount = false">
            {{ $t('ssoNoAccountOk') }}
          </VButton>
        </div>
      </div>
    </Teleport>
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
  flex-shrink: 0;
}
.logo.custom { background: none; }
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

/* 未开通账号的拦截弹窗 */
.ovl { position: fixed; inset: 0; z-index: 80; display: flex; align-items: center; justify-content: center; padding: 16px; background: var(--surface-overlay); backdrop-filter: blur(2px); }
.dlg { width: min(400px, 100%); box-sizing: border-box; padding: 26px 24px 20px; text-align: center; background: var(--surface-card); border: 1px solid var(--border-default); border-radius: var(--radius-lg); box-shadow: var(--shadow-xl); }
.dlgic { width: 44px; height: 44px; margin: 0 auto 14px; border-radius: 50%; background: var(--danger-subtle); display: grid; place-items: center; }
.dlgt { font: 700 15px var(--font-display); color: var(--text-strong); }
.dlgb { margin: 8px 0 20px; font: 500 12.5px/1.7 var(--font-body); color: var(--text-muted); }
/* SSO 入口 */
.ssobtn { margin-top: 18px; gap: 8px; }
.ssoalt { text-align: center; margin-top: 14px; }
.ssolink { font: 500 12px var(--font-body); color: var(--text-muted); cursor: pointer; text-decoration: underline; text-underline-offset: 3px; }
.ssolink:hover { color: var(--accent-text); }
.ssosep { display: flex; align-items: center; gap: 10px; margin: 16px 0 2px; font: 500 11px var(--font-body); color: var(--text-faint); }
.ssosep::before, .ssosep::after { content: ''; flex: 1; height: 1px; background: var(--border-subtle); }
.ssohint { margin-top: 8px; font: 500 11px var(--font-body); color: var(--warning-text); text-align: center; }
</style>
