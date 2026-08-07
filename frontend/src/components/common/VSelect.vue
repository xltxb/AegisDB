<script setup lang="ts">
import { ref, computed, nextTick } from 'vue'
import { ChevronDown, Search } from 'lucide-vue-next'

const props = withDefaults(
  defineProps<{
    modelValue: string
    options: string[]
    // searchable turns the menu into filter-then-pick. Off by default: the short
    // dropdowns (engine, env, policy, retention) are faster to scan than to type
    // into, and a search box on four options is just noise. Turn it on where the
    // list grows with the deployment — the database instances, above all.
    searchable?: boolean
    // height matches the control to the form around it; pages mixing VSelect with
    // native inputs need the two to line up.
    height?: string
    searchPlaceholder?: string
  }>(),
  { searchable: false, height: '40px', searchPlaceholder: '' },
)
const emit = defineEmits<{ 'update:modelValue': [string] }>()

const open = ref(false)
const kw = ref('')
const root = ref<HTMLElement | null>(null)
const box = ref<HTMLInputElement | null>(null)

const shown = computed(() => {
  const q = kw.value.trim().toLowerCase()
  if (!props.searchable || !q) return props.options
  return props.options.filter((o) => o.toLowerCase().includes(q))
})

function toggle() {
  open.value = !open.value
  if (!open.value) return
  kw.value = ''
  if (props.searchable) nextTick(() => box.value?.focus())
}

function pick(o: string) {
  emit('update:modelValue', o)
  open.value = false
  kw.value = ''
}

// focusout, not blur: blur does not bubble, so once the search box takes focus the
// root's own blur would fire and close the menu the instant it opened. focusout
// bubbles and carries relatedTarget, so the menu closes only when focus genuinely
// leaves the component.
function onFocusOut(e: FocusEvent) {
  const next = e.relatedTarget as Node | null
  if (next && root.value?.contains(next)) return
  open.value = false
  kw.value = ''
}

// Enter takes the first remaining match — typing enough to narrow the list to one
// and pressing Enter is the whole point of a searchable select.
function onEnter() {
  if (shown.value.length) pick(shown.value[0])
}

function close() {
  open.value = false
  kw.value = ''
}
</script>

<template>
  <div ref="root" class="vsel" tabindex="0" @focusout="onFocusOut" @keydown.esc.stop="close">
    <div class="control" :style="{ height }" @click="toggle">
      <span class="val">{{ modelValue }}</span>
      <ChevronDown :size="14" class="chev" />
    </div>
    <div v-if="open" class="menu">
      <div v-if="searchable" class="sbox">
        <Search :size="13" color="var(--text-faint)" />
        <input
          ref="box"
          v-model="kw"
          :placeholder="searchPlaceholder || $t('selSearchPh')"
          spellcheck="false"
          @keydown.enter.prevent="onEnter"
        />
      </div>
      <div class="scy list">
        <!-- mousedown.prevent keeps focus where it is, so picking an option never
             trips focusout before the click lands. -->
        <div
          v-for="o in shown"
          :key="o"
          class="opt"
          :class="{ active: o === modelValue }"
          @mousedown.prevent
          @click="pick(o)"
        >
          {{ o }}
        </div>
        <div v-if="!shown.length" class="nores">{{ $t('selNoMatch') }}</div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.vsel { position: relative; outline: none; }
.control {
  border: 1px solid var(--border-default);
  border-radius: var(--radius-md);
  background: var(--surface-sunken);
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 0 12px;
  font: 400 13px var(--font-mono);
  color: var(--text-body);
  cursor: pointer;
}
.val { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.chev { color: var(--text-faint); flex-shrink: 0; }
.menu {
  position: absolute;
  z-index: 30;
  top: calc(100% + 4px);
  left: 0;
  right: 0;
  background: var(--surface-card);
  border: 1px solid var(--border-default);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-lg);
  padding: 5px;
}
.sbox {
  display: flex;
  align-items: center;
  gap: 7px;
  padding: 6px 9px;
  margin-bottom: 4px;
  border: 1px solid var(--border-default);
  border-radius: 8px;
  background: var(--surface-sunken);
}
.sbox input {
  flex: 1;
  min-width: 0;
  border: none;
  outline: none;
  background: transparent;
  color: var(--text-strong);
  font: 400 12.5px var(--font-mono);
}
.list { max-height: 220px; }
.opt {
  padding: 8px 11px;
  border-radius: 8px;
  font: 400 13px var(--font-mono);
  color: var(--text-body);
  cursor: pointer;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.opt:hover { background: var(--surface-sunken); }
.opt.active { background: var(--accent-subtle); color: var(--accent-text); }
.nores { padding: 14px 11px; text-align: center; font: 500 12px var(--font-mono); color: var(--text-faint); }
</style>
