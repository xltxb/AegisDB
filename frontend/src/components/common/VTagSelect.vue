<script setup lang="ts">
// 标签式多选:控件里是已选的标签(可单个去掉),菜单里搜索、勾选,可设上限。用在"选一组名字"的
// 表单里(执行窗口的库)。与 VMultiSelect 不同:那个是列表页的筛选器(只显示汇总,选项带内部取值)。
// 外观与交互沿用 VSelect(同一套控件高度、菜单宽度、搜索框、focusout 关闭)。
//
// 已选的值即便不在 options 里也照样显示、可以去掉:编辑一条老记录时,它选过的项可能已经
// 不在候选里了(库改过名、实例暂时连不上),悄悄丢掉等于替人改了数据。
import { ref, computed, nextTick } from 'vue'
import { ChevronDown, Search, Check, X } from 'lucide-vue-next'

const props = withDefaults(
  defineProps<{
    modelValue: string[]
    options: string[]
    searchable?: boolean
    height?: string
    searchPlaceholder?: string
    placeholder?: string
    /** 最多能选几个;0 = 不限。到上限后没选的项置灰 */
    max?: number
  }>(),
  { searchable: false, height: '40px', searchPlaceholder: '', placeholder: '', max: 0 },
)
const emit = defineEmits<{ 'update:modelValue': [string[]] }>()

const open = ref(false)
const kw = ref('')
const root = ref<HTMLElement | null>(null)
const box = ref<HTMLInputElement | null>(null)

const shown = computed(() => {
  const q = kw.value.trim().toLowerCase()
  if (!props.searchable || !q) return props.options
  return props.options.filter((o) => o.toLowerCase().includes(q))
})
const picked = (o: string) => props.modelValue.includes(o)
const full = computed(() => props.max > 0 && props.modelValue.length >= props.max)

function toggleMenu() {
  open.value = !open.value
  if (!open.value) return
  kw.value = ''
  if (props.searchable) nextTick(() => box.value?.focus())
}

// 勾选顺序就是保存顺序:第一个勾的是"主库"(老界面里只显示一个库的地方显示它)
function toggle(o: string) {
  if (picked(o)) emit('update:modelValue', props.modelValue.filter((x) => x !== o))
  else if (!full.value) emit('update:modelValue', [...props.modelValue, o])
}
function remove(o: string) {
  emit('update:modelValue', props.modelValue.filter((x) => x !== o))
}

function onFocusOut(e: FocusEvent) {
  const next = e.relatedTarget as Node | null
  if (next && root.value?.contains(next)) return
  open.value = false
  kw.value = ''
}
function onEnter() {
  if (shown.value.length) toggle(shown.value[0])
}
function close() {
  open.value = false
  kw.value = ''
}
</script>

<template>
  <div ref="root" class="vtag" tabindex="0" @focusout="onFocusOut" @keydown.esc.stop="close">
    <div class="control" :style="{ minHeight: height }" @click="toggleMenu">
      <div class="chips">
        <span v-for="v in modelValue" :key="v" class="chip" data-testid="vtag-chip">
          <span class="ct">{{ v }}</span>
          <button type="button" class="cx" :aria-label="$t('selRemove', { v })" @click.stop="remove(v)"><X :size="11" /></button>
        </span>
        <span v-if="!modelValue.length" class="ph">{{ placeholder }}</span>
      </div>
      <ChevronDown :size="14" class="chev" />
    </div>
    <div v-if="open" class="menu">
      <div v-if="searchable" class="sbox">
        <Search :size="13" color="var(--text-faint)" />
        <input ref="box" v-model="kw" :placeholder="searchPlaceholder || $t('selSearchPh')" spellcheck="false" @keydown.enter.prevent="onEnter" />
      </div>
      <div class="scy list">
        <div v-for="o in shown" :key="o" class="opt" :class="{ active: picked(o), off: full && !picked(o) }"
             role="option" :aria-selected="picked(o)" @mousedown.prevent @click="toggle(o)">
          <span class="box"><Check v-if="picked(o)" :size="11" /></span>
          <span class="ot">{{ o }}</span>
        </div>
        <div v-if="!shown.length" class="nores">{{ $t('selNoMatch') }}</div>
      </div>
      <div v-if="max > 0" class="foot">{{ $t('selPickedOf', { n: modelValue.length, max }) }}</div>
    </div>
  </div>
</template>

<style scoped>
.vtag { position: relative; outline: none; }
.control {
  border: 1px solid var(--border-default); border-radius: var(--radius-md); background: var(--surface-sunken);
  display: flex; align-items: center; justify-content: space-between; gap: 8px; padding: 4px 10px 4px 6px; box-sizing: border-box;
  font: 400 13px var(--font-mono); color: var(--text-body); cursor: pointer;
}
.chips { display: flex; flex-wrap: wrap; gap: 4px; min-width: 0; flex: 1; }
.chip { display: inline-flex; align-items: center; gap: 2px; max-width: 100%; height: 24px; padding: 0 2px 0 8px; border-radius: 6px;
  background: var(--accent-subtle); color: var(--accent-text); font: 500 12px var(--font-mono); }
.ct { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.cx { display: flex; align-items: center; justify-content: center; width: 18px; height: 18px; border: none; border-radius: 4px;
  background: transparent; color: inherit; cursor: pointer; opacity: .7; }
.cx:hover { opacity: 1; background: color-mix(in oklch, var(--accent) 18%, transparent); }
.ph { padding-left: 6px; color: var(--text-faint); }
.chev { color: var(--text-faint); flex-shrink: 0; }
.menu {
  position: absolute; z-index: 30; top: calc(100% + 4px); left: 0; min-width: 100%; width: max-content; max-width: min(460px, 92vw);
  background: var(--surface-card); border: 1px solid var(--border-default); border-radius: var(--radius-md); box-shadow: var(--shadow-lg); padding: 5px;
}
.sbox { display: flex; align-items: center; gap: 7px; padding: 6px 9px; margin-bottom: 4px; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-sunken); }
.sbox input { flex: 1; min-width: 0; border: none; outline: none; background: transparent; color: var(--text-strong); font: 400 12.5px var(--font-mono); }
.list { max-height: min(360px, 52vh); }
.opt { display: flex; align-items: center; gap: 9px; padding: 8px 11px; border-radius: 8px; font: 400 13px var(--font-mono); color: var(--text-body); cursor: pointer; }
.opt:hover { background: var(--surface-sunken); }
.opt.active { color: var(--accent-text); }
.opt.off { opacity: .4; cursor: not-allowed; }
.box { display: flex; align-items: center; justify-content: center; width: 15px; height: 15px; flex-shrink: 0; border: 1.5px solid var(--border-strong); border-radius: 4px; }
.opt.active .box { background: var(--accent); border-color: var(--accent); color: #fff; }
.ot { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.nores { padding: 14px 11px; text-align: center; font: 500 12px var(--font-mono); color: var(--text-faint); }
.foot { padding: 6px 11px 2px; border-top: 1px solid var(--border-subtle); margin-top: 4px; font: 500 11px var(--font-body); color: var(--text-faint); }
</style>
