<script setup lang="ts">
// 项目 —— 数据库与升级单的归属。
//
// 它和标签(tags)是两回事,面板文案也刻意这么说:标签决定"谁能碰这个库"(访问
// 范围,判定层真的会看),项目决定"这个库归谁跟进"(组织维度,判定层不看)。两者
// 长得像,一旦被当成同一个东西用,总有一天会有人以为改了项目就改了权限。
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { FolderKanban, Plus, Pencil, Trash2, X, Check } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import api from '@/api'
import { confirmAction } from '@/lib/confirm'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import type { Project } from '@/types'

const { t } = useI18n()
const ui = useUIStore()
const auth = useAuthStore()
const isAdmin = computed(() => auth.me?.roleCode === 'admin' || (auth.me?.roleCodes || []).includes('admin'))

// 列表变了就喊一声 —— 别处(数据源行的归属下拉)拿的是同一份数据,
// 不同步的话新建的项目要刷新页面才选得到。
const emit = defineEmits<{ (e: 'changed', projects: Project[]): void }>()
const projects = ref<Project[]>([])
const editing = ref<{ id: number; name: string; owner: string; description: string } | null>(null)

async function load() {
  try {
    projects.value = await api.projects()
    emit('changed', projects.value)
  } catch (e) { ui.notifyError(e, t('loadFailed')) }
}
onMounted(load)
defineExpose({ load })

function openNew() { editing.value = { id: 0, name: '', owner: '', description: '' } }
function openEdit(p: Project) {
  editing.value = { id: p.id, name: p.name, owner: p.owner || '', description: p.description || '' }
}

async function save() {
  const f = editing.value
  if (!f) return
  if (!f.name.trim()) { ui.notify(t('prNameRequired'), 'error'); return }
  try {
    const env = f.id
      ? await api.updateProject(f.id, { name: f.name.trim(), owner: f.owner, description: f.description })
      : await api.createProject({ name: f.name.trim(), owner: f.owner, description: f.description })
    if (env.code !== 0) { ui.notifyError(new Error(env.msg), t('actionFailed')); return }
    editing.value = null
    await load()
  } catch (e) { ui.notifyError(e, t('actionFailed')) }
}

async function remove(p: Project) {
  // 名下还有库时后端会拒并说明剩几个 —— 这里不预判,让服务端的理由原样呈现。
  if (!confirmAction(t('prDelConfirm', { name: p.name }))) return
  try {
    const env = await api.deleteProject(p.id)
    if (env.code !== 0) { ui.notifyError(new Error(env.msg), t('actionFailed')); return }
    await load()
  } catch (e) { ui.notifyError(e, t('actionFailed')) }
}
</script>

<template>
  <div class="pr">
    <div class="prhead">
      <div class="pric"><FolderKanban :size="18" color="var(--accent-text)" /></div>
      <div class="grow">
        <div class="prt">{{ $t('prTitle') }}</div>
        <div class="prs">{{ $t('prSub') }}</div>
      </div>
      <VButton v-if="isAdmin" variant="primary" height="34px" @click="openNew"><Plus :size="14" />{{ $t('prNew') }}</VButton>
    </div>

    <div class="list">
      <div v-for="p in projects" :key="p.id" class="item">
        <div class="grow">
          <div class="itop">
            <span class="iname">{{ p.name }}</span>
            <span v-if="p.owner" class="iowner">{{ p.owner }}</span>
          </div>
          <div class="imeta">
            <span>{{ $t('prDbs', { n: p.databases }) }}</span>
            <span class="sep">·</span>
            <span>{{ $t('prReleases', { n: p.releases }) }}</span>
            <template v-if="p.description"><span class="sep">·</span><span class="dim">{{ p.description }}</span></template>
          </div>
        </div>
        <template v-if="isAdmin">
          <button class="act" :title="$t('edit')" @click="openEdit(p)"><Pencil :size="14" /></button>
          <button class="act del" :title="$t('delete')" @click="remove(p)"><Trash2 :size="14" /></button>
        </template>
      </div>
      <div v-if="!projects.length" class="empty">{{ $t('prEmpty') }}</div>
    </div>

    <div v-if="editing" class="overlay">
      <div class="mask" @click="editing = null" />
      <div class="modal">
        <div class="mhead">
          <div class="mic"><FolderKanban :size="17" color="var(--accent-text)" /></div>
          <div><div class="mt">{{ editing.id ? $t('prEdit') : $t('prNew') }}</div><div class="ms">{{ $t('prSub') }}</div></div>
          <X :size="17" class="mx" @click="editing = null" />
        </div>
        <div class="mbody">
          <div><div class="fl">{{ $t('prName') }}</div><input v-model="editing.name" class="in" :placeholder="$t('prNamePh')" /></div>
          <div><div class="fl">{{ $t('prOwner') }}</div><input v-model="editing.owner" class="in" :placeholder="$t('prOwnerPh')" /></div>
          <div><div class="fl">{{ $t('prDesc') }}</div><input v-model="editing.description" class="in" :placeholder="$t('prDescPh')" /></div>
          <div class="hint">{{ $t('prScopeHint') }}</div>
        </div>
        <div class="mfoot">
          <VButton @click="editing = null">{{ $t('btnCancel') }}</VButton>
          <VButton variant="primary" @click="save"><Check :size="14" />{{ $t('btnSave') }}</VButton>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.pr { border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-xs); overflow: hidden; }
.prhead { display: flex; align-items: center; gap: 12px; padding: 16px 20px; border-bottom: 1px solid var(--border-subtle); }
.pric { width: 34px; height: 34px; border-radius: 10px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; }
.grow { flex: 1; min-width: 0; }
.prt { font: 600 14px var(--font-display); color: var(--text-strong); }
.prs { font: 500 12px var(--font-body); color: var(--text-muted); margin-top: 2px; }
.list { padding: 14px 20px; display: flex; flex-direction: column; gap: 8px; }
.item { display: flex; align-items: center; gap: 12px; padding: 10px 12px; border: 1px solid var(--border-default); border-radius: 11px; background: var(--surface-sunken); }
.itop { display: flex; align-items: center; gap: 9px; }
.iname { font: 600 13px var(--font-body); color: var(--text-strong); }
.iowner { padding: 1px 7px; border-radius: 999px; background: var(--accent-subtle); color: var(--accent-text); font: 600 10.5px var(--font-body); }
.imeta { margin-top: 3px; display: flex; align-items: center; gap: 7px; font: 500 11.5px var(--font-body); color: var(--text-muted); }
.sep { color: var(--text-faint); }
.dim { color: var(--text-faint); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.act { width: 30px; height: 28px; flex-shrink: 0; border: 1px solid var(--border-default); border-radius: 7px; background: var(--surface-card); color: var(--text-body); cursor: pointer; display: flex; align-items: center; justify-content: center; }
.act:hover { border-color: var(--accent-text); color: var(--accent-text); }
.act.del:hover { border-color: var(--danger-text); color: var(--danger-text); }
.empty { padding: 22px 0; text-align: center; font: 500 12px var(--font-body); color: var(--text-faint); }
.overlay { position: fixed; inset: 0; z-index: 80; display: flex; align-items: center; justify-content: center; padding: 24px; }
.mask { position: absolute; inset: 0; background: var(--surface-overlay); backdrop-filter: blur(3px); }
.modal { position: relative; width: 100%; max-width: 520px; border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-lg); overflow: hidden; }
.mhead { display: flex; align-items: center; gap: 11px; padding: 15px 18px; border-bottom: 1px solid var(--border-subtle); }
.mic { width: 32px; height: 32px; border-radius: 9px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; }
.mt { font: 600 14px var(--font-display); color: var(--text-strong); }
.ms { font: 500 11.5px var(--font-body); color: var(--text-muted); }
.mx { margin-left: auto; cursor: pointer; color: var(--text-faint); }
.mbody { padding: 16px 18px; display: flex; flex-direction: column; gap: 12px; }
.fl { font: 600 11px var(--font-mono); letter-spacing: 0.06em; color: var(--text-faint); text-transform: uppercase; margin-bottom: 5px; }
.in { width: 100%; box-sizing: border-box; height: 36px; padding: 0 12px; border: 1px solid var(--border-default); border-radius: 9px; background: var(--surface-sunken); font: 500 12.5px var(--font-body); color: var(--text-strong); outline: none; }
.in:focus { border-color: var(--accent-text); box-shadow: 0 0 0 3px var(--accent-subtle); }
.hint { font: 500 11.5px var(--font-body); color: var(--text-muted); line-height: 1.6; }
.mfoot { display: flex; justify-content: flex-end; gap: 10px; padding: 13px 18px; border-top: 1px solid var(--border-subtle); }
</style>
