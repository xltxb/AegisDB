<script setup lang="ts">
// 系统设置 → 品牌:系统名称(中 / 英)与 LOGO。
//
// 自己保存(只发 brand.* 三项),存成了立刻换掉当前页面上的名称、LOGO、favicon;其他人下次打开
// 页面(包括未登录的登录页)读 GET /auth/branding 拿到新值。名称留空 = 保持默认字样。
// LOGO 以 data URI 存进数据库(多活各节点共享,不落本机磁盘),服务端会再校验类型、大小、SVG 脚本。
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { ImageUp, Trash2 } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import api from '@/api'
import { useUIStore } from '@/stores/ui'
import { DEFAULT_BRAND, LOGO_MAX_BYTES, LOGO_TYPES, brand, checkLogoFile, setBrand } from '@/lib/brand'
import BrandMark from '@/components/BrandMark.vue'

const { t } = useI18n()
const ui = useUIStore()

const name = ref('')
const nameEn = ref('')
const logo = ref('')
const saving = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)

function fill() {
  name.value = brand.name
  nameEn.value = brand.nameEn
  logo.value = brand.logo
}

onMounted(async () => {
  try { setBrand(await api.branding()) } catch { /* 用启动时拉到的 */ }
  fill()
})

function pick() { fileInput.value?.click() }

function onFile(e: Event) {
  const input = e.target as HTMLInputElement
  const f = input.files?.[0]
  input.value = '' // 同一个文件再选一次也要触发
  if (!f) return
  const bad = checkLogoFile(f)
  if (bad === 'type') { ui.notify(t('brandLogoBadType'), 'error'); return }
  if (bad === 'size') { ui.notify(t('brandLogoTooBig', { n: LOGO_MAX_BYTES / 1024 }), 'error'); return }
  const rd = new FileReader()
  rd.onload = () => { logo.value = String(rd.result || '') }
  rd.onerror = () => ui.notify(t('brandLogoReadFail'), 'error')
  rd.readAsDataURL(f)
}

async function save() {
  saving.value = true
  const next = { name: name.value.trim(), nameEn: nameEn.value.trim(), logo: logo.value }
  try {
    await api.saveSettings({ 'brand.name': next.name, 'brand.nameEn': next.nameEn, 'brand.logo': next.logo })
  } catch (e) {
    ui.notifyError(e, t('saveFailed'))
    return
  } finally {
    saving.value = false
  }
  setBrand(next)
  fill()
  ui.notify(t('settingsSaved'), 'success')
}
</script>

<template>
  <section class="bp" data-testid="brand-panel">
    <div class="bphead">
      <div class="bpic"><ImageUp :size="17" color="var(--accent-text)" /></div>
      <div class="grow"><div class="bpt">{{ $t('setBrand') }}</div><div class="bps">{{ $t('setBrandSub') }}</div></div>
    </div>

    <div class="row">
      <div class="grow"><div class="rt">{{ $t('brandName') }}</div><div class="rd">{{ $t('brandNameD') }}</div></div>
      <input v-model="name" class="in" data-testid="brand-name" maxlength="40" :placeholder="DEFAULT_BRAND" />
    </div>
    <div class="row">
      <div class="grow"><div class="rt">{{ $t('brandNameEn') }}</div><div class="rd">{{ $t('brandNameEnD') }}</div></div>
      <input v-model="nameEn" class="in" data-testid="brand-name-en" maxlength="40" :placeholder="DEFAULT_BRAND" />
    </div>
    <div class="row">
      <div class="grow"><div class="rt">{{ $t('brandLogo') }}</div><div class="rd">{{ $t('brandLogoD', { n: LOGO_MAX_BYTES / 1024 }) }}</div></div>
      <div class="logoctl">
        <div class="prev" :class="{ custom: !!logo }" data-testid="brand-logo-preview">
          <img v-if="logo" :src="logo" alt="" />
          <BrandMark v-else :size="20" />
        </div>
        <input ref="fileInput" type="file" class="hidden" data-testid="brand-logo-file" :accept="LOGO_TYPES.join(',')" @change="onFile" />
        <VButton height="34px" @click="pick"><ImageUp :size="14" />{{ $t('brandLogoUpload') }}</VButton>
        <VButton v-if="logo" size="sm" data-testid="brand-logo-clear" @click="logo = ''"><Trash2 :size="14" />{{ $t('brandLogoClear') }}</VButton>
      </div>
    </div>

    <div class="foot">
      <div class="grow hint">{{ $t('brandHint') }}</div>
      <VButton variant="primary" :disabled="saving" data-testid="brand-save" @click="save">{{ $t('saveSettings') }}</VButton>
    </div>
  </section>
</template>

<style scoped>
.bp { border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-xs); overflow: hidden; }
.bphead { display: flex; align-items: center; gap: 12px; padding: 18px 22px; border-bottom: 1px solid var(--border-subtle); background: linear-gradient(180deg, var(--accent-subtle), transparent 140%); }
.bpic { width: 36px; height: 36px; flex-shrink: 0; border-radius: 10px; background: var(--surface-card); border: 1px solid var(--accent-subtle-border); display: flex; align-items: center; justify-content: center; }
.bpt { font: 650 15px var(--font-display); color: var(--text-strong); }
.bps { font: 500 12px var(--font-body); color: var(--text-muted); margin-top: 2px; }
.grow { flex: 1; min-width: 0; }
.row { display: flex; align-items: center; gap: 24px; padding: 16px 22px; border-bottom: 1px solid var(--border-subtle); flex-wrap: wrap; }
.rt { font: 600 13px var(--font-body); color: var(--text-strong); }
.rd { font: 400 12px/1.55 var(--font-body); color: var(--text-muted); margin-top: 3px; }
.in { width: min(300px, 100%); height: 36px; box-sizing: border-box; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-sunken); padding: 0 12px; font: 500 13px var(--font-body); color: var(--text-strong); outline: none; }
.in:focus { border-color: var(--accent-text); }
.logoctl { display: flex; align-items: center; gap: 10px; }
.prev { width: 38px; height: 38px; border-radius: 10px; background: linear-gradient(135deg, #3b6ef6, #2dcde6); display: flex; align-items: center; justify-content: center; overflow: hidden; flex-shrink: 0; }
.prev.custom { background: var(--surface-sunken); border: 1px solid var(--border-subtle); }
.prev img { width: 100%; height: 100%; object-fit: contain; }
.hidden { display: none; }
.foot { display: flex; align-items: center; gap: 14px; padding: 14px 22px; }
.hint { font: 500 12px var(--font-body); color: var(--text-muted); }
</style>
