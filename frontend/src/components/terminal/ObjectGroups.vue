<script setup lang="ts">
import { ref } from 'vue'
import { ChevronDown, ChevronRight, FunctionSquare, Cog, Package, Zap, FileCode2 } from 'lucide-vue-next'
import type { DbObjects } from '@/types'

// Programmable-object groups (functions / procedures / packages / triggers)
// under one database or schema node of the terminal tree. Purely presentational:
// the parent owns loading and the source viewer; this renders whatever it got
// and reports which object was clicked.
//
// props.objects: undefined → the parent never requested this scope (render
// nothing); null → request in flight; loaded → groups (empty kinds are hidden).
const props = defineProps<{ objects: DbObjects | null | undefined }>()
const emit = defineEmits<{ open: [type: string, name: string] }>()

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
          <div v-for="o in list(cat.key)" :key="o" class="obj" :title="$t('objViewSource')"
               @click.stop="emit('open', cat.type, o)">
            <FileCode2 :size="12" color="var(--text-faint)" />{{ o }}
          </div>
        </div>
      </template>
    </template>
    <div v-if="objects.error" class="hint err">{{ objects.error }}</div>
  </template>
</template>

<style scoped>
.cat { display: flex; align-items: center; gap: 6px; padding: 4px 8px; border-radius: 7px; font: 400 12px var(--font-mono); color: var(--text-muted); cursor: pointer; }
.cat:hover { color: var(--text-body); background: rgba(255, 255, 255, 0.03); }
.cnt { margin-left: auto; font: 600 10px var(--font-mono); color: var(--text-faint); }
.objs { padding-left: 16px; }
.obj { display: flex; align-items: center; gap: 7px; padding: 4px 8px; border-radius: 6px; font: 400 12px var(--font-mono); color: var(--text-muted); cursor: pointer; }
.obj:hover { color: var(--accent-text); background: rgba(255, 255, 255, 0.04); }
.hint { padding: 4px 8px; font: 500 11px var(--font-mono); color: var(--text-faint); }
.hint.err { color: var(--danger-text); white-space: normal; word-break: break-word; }
</style>
