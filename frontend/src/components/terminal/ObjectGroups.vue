<script setup lang="ts">
import { ref, onBeforeUnmount, nextTick } from 'vue'
import { ChevronDown, ChevronRight, FunctionSquare, Cog, Package, Zap, FileCode2, Download, ListChecks } from 'lucide-vue-next'
import type { DbObjects } from '@/types'

// Programmable-object groups (functions / procedures / packages / triggers)
// under one database or schema node of the terminal tree. Purely presentational:
// the parent owns loading, the source viewer and the download; this renders
// whatever it got and reports which object was clicked.
//
// props.objects: undefined → the parent never requested this scope (render
// nothing); null → request in flight; loaded → groups (empty kinds are hidden).
const props = defineProps<{ objects: DbObjects | null | undefined }>()
const emit = defineEmits<{ open: [type: string, name: string]; download: [names: string[]] }>()

const CATS = [
  { key: 'functions', label: 'treeFunctions', type: 'function', icon: FunctionSquare },
  { key: 'procedures', label: 'treeProcedures', type: 'procedure', icon: Cog },
  { key: 'packages', label: 'treePackages', type: 'package', icon: Package },
  { key: 'triggers', label: 'treeTriggers', type: 'trigger', icon: Zap },
] as const

function list(key: string): string[] {
  return props.objects ? ((props.objects as unknown as Record<string, string[]>)[key] ?? []) : []
}

// Per-node collapse state — one component instance per tree node, so plain
// per-category keys are already scoped correctly.
const open = ref<Record<string, boolean>>({})
function toggle(k: string) { open.value[k] = !open.value[k] }

// ---- 程序包:多选 + 右键下载 ----
//
// 只有程序包有这套交互:下载 .pck 是按"包头 + 包体"成对拼的,别的对象没有这个形状。
// 点击的习惯与文件管理器一致 —— 单击看源码(原有行为不变),Ctrl/⌘ 单击加减选,
// Shift 单击连选;右键在已选中的行上作用于整组,在未选中的行上只作用于这一行。
const sel = ref<Set<string>>(new Set())
let anchor = ''
const pkgs = () => list('packages')

function clickPkg(e: MouseEvent, name: string) {
  if (e.ctrlKey || e.metaKey) {
    const s = new Set(sel.value)
    s.has(name) ? s.delete(name) : s.add(name)
    sel.value = s
    anchor = name
    return
  }
  if (e.shiftKey && anchor) {
    const all = pkgs()
    const a = all.indexOf(anchor)
    const b = all.indexOf(name)
    if (a >= 0 && b >= 0) {
      sel.value = new Set(all.slice(Math.min(a, b), Math.max(a, b) + 1))
      return
    }
  }
  sel.value = new Set([name])
  anchor = name
  emit('open', 'package', name)
}

const menu = ref<{ x: number; y: number } | null>(null)
const menuEl = ref<HTMLElement | null>(null)

async function ctxPkg(e: MouseEvent, name: string) {
  if (!sel.value.has(name)) {
    sel.value = new Set([name])
    anchor = name
  }
  menu.value = { x: e.clientX, y: e.clientY }
  window.addEventListener('mousedown', onOutside, true)
  window.addEventListener('keydown', onKey, true)
  window.addEventListener('resize', closeMenu)
  window.addEventListener('scroll', closeMenu, true)
  // 贴着鼠标弹出,但不许越出视口 —— 树在屏幕底部时菜单要往上翻。
  await nextTick()
  const el = menuEl.value
  if (el && menu.value) {
    const r = el.getBoundingClientRect()
    menu.value = {
      x: Math.max(4, Math.min(menu.value.x, window.innerWidth - r.width - 4)),
      y: Math.max(4, Math.min(menu.value.y, window.innerHeight - r.height - 4)),
    }
  }
}
function closeMenu() {
  menu.value = null
  window.removeEventListener('mousedown', onOutside, true)
  window.removeEventListener('keydown', onKey, true)
  window.removeEventListener('resize', closeMenu)
  window.removeEventListener('scroll', closeMenu, true)
}
function onOutside(e: MouseEvent) {
  if (menuEl.value && !menuEl.value.contains(e.target as Node)) closeMenu()
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') { e.stopPropagation(); closeMenu() }
}
onBeforeUnmount(closeMenu)

// 按树上的顺序交出去,文件在 zip 里的顺序与人看到的一致。
const selected = () => pkgs().filter((n) => sel.value.has(n))
function doDownload() {
  const names = selected()
  closeMenu()
  if (names.length) emit('download', names)
}
function doView() {
  const [name] = selected()
  closeMenu()
  if (name) emit('open', 'package', name)
}
function doSelectAll() {
  sel.value = new Set(pkgs())
  closeMenu()
}
</script>

<template>
  <div v-if="objects === null" class="hint">{{ $t('treeLoading') }}</div>
  <template v-else-if="objects">
    <template v-for="cat in CATS" :key="cat.key">
      <template v-if="list(cat.key).length">
        <div class="cat" @click.stop="toggle(cat.key)">
          <component :is="open[cat.key] ? ChevronDown : ChevronRight" :size="11" color="var(--text-faint)" />
          <component :is="cat.icon" :size="12" color="var(--text-faint)" />
          {{ $t(cat.label as any) }}<span class="cnt">{{ list(cat.key).length }}</span>
        </div>
        <div v-if="open[cat.key]" class="objs">
          <template v-if="cat.key === 'packages'">
            <div v-for="o in list(cat.key)" :key="o" class="obj" :class="{ on: sel.has(o) }"
                 :title="o + ' · ' + $t('pkgRowHint')" data-testid="pkg-row"
                 @click.stop="clickPkg($event, o)" @contextmenu.prevent.stop="ctxPkg($event, o)">
              <FileCode2 :size="12" color="var(--text-faint)" /><span class="nm">{{ o }}</span>
            </div>
          </template>
          <template v-else>
            <div v-for="o in list(cat.key)" :key="o" class="obj" :title="o + ' · ' + $t('objViewSource')"
                 @click.stop="emit('open', cat.type, o)">
              <FileCode2 :size="12" color="var(--text-faint)" /><span class="nm">{{ o }}</span>
            </div>
          </template>
        </div>
      </template>
    </template>
    <div v-if="objects.error" class="hint err">{{ objects.error }}</div>
  </template>

  <Teleport to="body">
    <div v-if="menu" ref="menuEl" class="ctx" role="menu" data-testid="pkg-menu"
         :style="{ left: menu.x + 'px', top: menu.y + 'px' }" @contextmenu.prevent>
      <button v-if="sel.size === 1" class="ctx-i" role="menuitem" @click="doView">
        <FileCode2 :size="13" />{{ $t('objViewSource') }}
      </button>
      <button class="ctx-i" role="menuitem" data-testid="pkg-download" @click="doDownload">
        <Download :size="13" />{{ sel.size > 1 ? $t('pkgDownloadMany', { n: sel.size }) : $t('pkgDownloadOne') }}
      </button>
      <div class="ctx-sep" />
      <button class="ctx-i" role="menuitem" data-testid="pkg-select-all" @click="doSelectAll">
        <ListChecks :size="13" />{{ $t('pkgSelectAll', { n: pkgs().length }) }}
      </button>
    </div>
  </Teleport>
</template>

<style scoped>
.cat { display: flex; align-items: center; gap: 6px; padding: 4px 8px; border-radius: 7px; font: 400 12px var(--font-mono); color: var(--text-muted); cursor: pointer; }
.cat:hover { color: var(--text-body); background: rgba(255, 255, 255, 0.03); }
.cnt { margin-left: auto; font: 600 10px var(--font-mono); color: var(--text-faint); }
.objs { padding-left: 16px; }
/* 同 DbTree:图标不许被长名字压扁,名字自己截断 */
.cat svg, .obj svg { flex-shrink: 0; }
.nm { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.obj { display: flex; align-items: center; gap: 7px; padding: 4px 8px; border-radius: 6px; font: 400 12px var(--font-mono); color: var(--text-muted); cursor: pointer; user-select: none; }
.obj:hover { color: var(--accent-text); background: rgba(255, 255, 255, 0.04); }
.obj.on { color: var(--accent-text); background: var(--accent-subtle); box-shadow: inset 0 0 0 1px var(--accent-subtle-border); }
.hint { padding: 4px 8px; font: 500 11px var(--font-mono); color: var(--text-faint); }
.hint.err { color: var(--danger-text); white-space: normal; word-break: break-word; }

.ctx { position: fixed; z-index: 1000; min-width: 200px; padding: 4px; border-radius: 9px; background: var(--surface-raised); border: 1px solid var(--border-default); box-shadow: var(--shadow-lg); }
.ctx-i { display: flex; align-items: center; gap: 8px; width: 100%; padding: 7px 10px; border: 0; border-radius: 6px; background: none; color: var(--text-body); font: 500 12px var(--font-body); text-align: left; cursor: pointer; white-space: nowrap; }
.ctx-i:hover, .ctx-i:focus-visible { background: rgba(255, 255, 255, 0.06); color: var(--accent-text); outline: none; }
.ctx-sep { height: 1px; margin: 4px 2px; background: var(--border-subtle); }
</style>
