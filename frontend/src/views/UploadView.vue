<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { Upload, FileCode2, Download, Trash2, FolderCog } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import api from '@/api'
import { confirmAction } from '@/lib/confirm'
import { CODE_OK, CODE_SCRIPT_PATH_UNSET } from '@/api/http'
import type { ScriptUpload } from '@/types'

const router = useRouter()
const { t } = useI18n()
const files = ref<ScriptUpload[]>([])
const busy = ref(false)
const err = ref('')
const needPath = ref(false)
const dragOver = ref(false)
const fileRef = ref<HTMLInputElement>()

async function loadList() {
  try { files.value = await api.scriptUploads() } catch { /* ignore */ }
}
onMounted(loadList)

async function uploadFile(file: File) {
  busy.value = true
  err.value = ''
  needPath.value = false
  try {
    const text = await file.text()
    const env = await api.scriptUpload(text, file.name)
    if (env.code === CODE_SCRIPT_PATH_UNSET) { needPath.value = true; return }
    if (env.code === CODE_OK) { await loadList(); return }
    err.value = env.msg || t('uploadFailed')
  } catch { err.value = t('uploadFailed') }
  finally { busy.value = false }
}

function onPick(e: Event) {
  const f = (e.target as HTMLInputElement).files?.[0]
  if (f) uploadFile(f)
  ;(e.target as HTMLInputElement).value = ''
}
function onDrop(e: DragEvent) {
  dragOver.value = false
  const f = e.dataTransfer?.files?.[0]
  if (f) uploadFile(f)
}

async function download(u: ScriptUpload) {
  try {
    const blob = await api.scriptUploadDownload(u.id)
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = u.filename
    a.click()
    URL.revokeObjectURL(url)
  } catch { err.value = t('downloadFailed') }
}
async function remove(u: ScriptUpload) {
  // M15: 删除脚本文件前二次确认
  if (!confirmAction(t('upDelConfirm', { name: u.filename }))) return
  try { await api.scriptUploadDelete(u.id); await loadList() } catch { err.value = t('deleteFailed') }
}
function goSettings() { router.push('/settings') }
const kb = (n: number) => (n < 1024 ? n + ' B' : n < 1048576 ? (n / 1024).toFixed(1) + ' KB' : (n / 1048576).toFixed(2) + ' MB')
</script>

<template>
  <div class="scy page">
    <div class="col">
      <section class="card">
        <div class="shead">
          <div class="sic"><Upload :size="18" color="var(--accent-text)" /></div>
          <div><div class="st">{{ $t('uploadTitle') }}</div><div class="ss">{{ $t('uploadSub') }}</div></div>
        </div>
        <div class="body">
          <div v-if="needPath" class="notice">
            <FolderCog :size="15" /><span>{{ $t('scPathTitle') }}</span>
            <VButton variant="secondary" height="30px" @click="goSettings">{{ $t('scPathGo') }}</VButton>
          </div>

          <div
            class="drop" :class="{ over: dragOver, busy }"
            @click="fileRef?.click()"
            @dragover.prevent="dragOver = true" @dragleave.prevent="dragOver = false" @drop.prevent="onDrop"
          >
            <Upload :size="24" />
            <div class="dt">{{ busy ? $t('uploadRunning') : $t('uploadDrop') }}</div>
            <div class="dh">{{ $t('uploadHint') }}</div>
          </div>
          <input ref="fileRef" type="file" accept=".sql,text/plain" class="hidden" @change="onPick" />
          <div v-if="err" class="err">{{ err }}</div>
        </div>
      </section>

      <section class="card">
        <div class="shead">
          <div class="sic"><FileCode2 :size="18" color="var(--accent-text)" /></div>
          <div><div class="st">{{ $t('uploadList') }}</div><div class="ss">{{ $t('uploadListSub') }}</div></div>
        </div>
        <div class="list">
          <div v-if="!files.length" class="empty">{{ $t('uploadNone') }}</div>
          <div v-for="u in files" :key="u.id" class="row">
            <FileCode2 :size="16" class="ic" />
            <div class="grow">
              <div class="fn">{{ u.filename }}<span class="src" :class="u.source">{{ u.source === 'terminal' ? $t('uploadFromTerm') : $t('uploadFromPage') }}</span></div>
              <div class="fm">{{ kb(u.size) }} · {{ u.createdAt?.slice(5, 16) }}</div>
              <div class="fp" :title="u.path"><span class="fpk">{{ $t('uploadPath') }}</span>{{ u.path }}</div>
            </div>
            <button class="act" :title="$t('exportDownload')" @click="download(u)"><Download :size="15" /></button>
            <button class="act del" :title="$t('delete')" @click="remove(u)"><Trash2 :size="15" /></button>
          </div>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.page { flex: 1; min-height: 0; padding: var(--page-pad); max-width: var(--page-max); margin-inline: auto; width: 100%; }
.col { max-width: 720px; display: flex; flex-direction: column; gap: 18px; }
.card { border: none; border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-xs); overflow: hidden; }
.shead { display: flex; align-items: center; gap: 11px; padding: 16px 20px; border-bottom: 1px solid var(--border-subtle); }
.sic { width: 32px; height: 32px; border-radius: 9px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; flex-shrink: 0; }
.st { font: 600 14px var(--font-display); color: var(--text-strong); }
.ss { font: 500 12px var(--font-body); color: var(--text-muted); margin-top: 1px; }
.body { padding: 16px 20px 20px; }
.notice { display: flex; align-items: center; gap: 9px; padding: 11px 14px; margin-bottom: 12px; border-radius: 10px; background: var(--warning-subtle); color: var(--warning-text); font: 600 12px var(--font-body); }
.notice span { flex: 1; }
.drop {
  display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 8px;
  padding: 34px; border: 1.5px dashed var(--border-default); border-radius: 12px; background: var(--surface-sunken);
  color: var(--text-muted); cursor: pointer; transition: all 0.15s ease;
}
.drop:hover, .drop.over { border-color: var(--accent-text); color: var(--accent-text); background: var(--accent-subtle); }
.drop.busy { opacity: 0.6; pointer-events: none; }
.dt { font: 600 13px var(--font-body); color: var(--text-strong); }
.dh { font: 500 11px var(--font-mono); color: var(--text-faint); }
.hidden { display: none; }
.err { margin-top: 10px; font: 600 12px var(--font-body); color: var(--danger-text); }
.list { display: flex; flex-direction: column; }
.empty { padding: 30px; text-align: center; font: 500 12.5px var(--font-mono); color: var(--text-faint); }
.row { display: flex; align-items: center; gap: 12px; padding: 13px 20px; border-bottom: 1px solid var(--border-subtle); }
.row:last-child { border-bottom: none; }
.ic { color: var(--text-muted); flex-shrink: 0; }
.grow { flex: 1; min-width: 0; }
.fn { font: 600 13px var(--font-mono); color: var(--text-strong); display: flex; align-items: center; gap: 8px; }
.src { font: 600 9px var(--font-mono); padding: 1px 6px; border-radius: 999px; background: var(--surface-sunken); color: var(--text-faint); }
.src.terminal { background: var(--accent-subtle); color: var(--accent-text); }
.fm { margin-top: 3px; font: 500 11px var(--font-mono); color: var(--text-faint); }
.fp { margin-top: 4px; font: 500 11px var(--font-mono); color: var(--text-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.fpk { color: var(--text-faint); margin-right: 6px; }
.act { width: 32px; height: 32px; flex-shrink: 0; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-sunken); color: var(--text-body); cursor: pointer; display: flex; align-items: center; justify-content: center; }
.act:hover { color: var(--accent-text); border-color: var(--accent-subtle-border); }
.act.del:hover { color: var(--danger-text); border-color: rgba(240, 71, 62, 0.4); background: var(--danger-subtle); }
</style>
