<script setup lang="ts">
import { ref, computed, nextTick, onUnmounted } from 'vue'
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
    // creatable(配合 searchable):选项之外还能直接用输入的值 —— 候选是现场读出来的、可能不全
    // 的地方用(结构对比的库名)。输入不在选项里时,菜单末尾多一项「使用 xxx」。
    creatable?: boolean
    // modelValue 为空时控件里显示的淡色提示
    placeholder?: string
    // floating:菜单按视口定位(position: fixed),不受外层 overflow 裁切,高度按视口剩余空间收、下面放不下
    // 就往上开。默认关:只在放在会裁切的卡片里的下拉上打开(数据查询页,2026-10-26 —— 库一多,菜单底部被
    // 下半屏的结果区盖住,滑到底也看不到最后几个)。
    floating?: boolean
  }>(),
  { searchable: false, height: '40px', searchPlaceholder: '', creatable: false, placeholder: '', floating: false },
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

// 可以直接使用的输入值:开了 creatable、输入非空、且不与某个选项完全相同
const custom = computed(() => {
  const q = kw.value.trim()
  return props.creatable && props.searchable && q && !props.options.includes(q) ? q : ''
})

// ---- floating 定位 ----
const menuEl = ref<HTMLElement | null>(null)
const menuStyle = ref<Record<string, string>>({})
const listStyle = ref<Record<string, string>>({})
function place() {
  if (!props.floating || !open.value || !root.value) return
  const r = root.value.getBoundingClientRect()
  const vh = window.innerHeight
  const chrome = (props.searchable ? 46 : 0) + 16 // 搜索框 + 内边距
  const below = vh - r.bottom - 8
  const above = r.top - 8
  const up = below < 220 && above > below
  const room = (up ? above : below) - chrome
  listStyle.value = { maxHeight: `${Math.max(120, Math.min(360, room))}px` }
  menuStyle.value = {
    position: 'fixed', left: `${Math.round(r.left)}px`, minWidth: `${Math.round(r.width)}px`, zIndex: '60', right: 'auto',
    ...(up ? { top: 'auto', bottom: `${Math.round(vh - r.top + 4)}px` } : { top: `${Math.round(r.bottom + 4)}px`, bottom: 'auto' }),
  }
}
// 外面滚动 / 窗口变化时跟着控件走;菜单自己的列表滚动不算
function onViewportChange(e?: Event) {
  if (e && e.target instanceof Node && menuEl.value?.contains(e.target)) return
  place()
}
function watchViewport(on: boolean) {
  if (!props.floating) return
  if (on) {
    window.addEventListener('resize', onViewportChange)
    window.addEventListener('scroll', onViewportChange, true)
  } else {
    window.removeEventListener('resize', onViewportChange)
    window.removeEventListener('scroll', onViewportChange, true)
  }
}
onUnmounted(() => watchViewport(false))

function toggle() {
  open.value = !open.value
  watchViewport(open.value)
  if (!open.value) return
  kw.value = ''
  place()
  if (props.searchable) nextTick(() => box.value?.focus())
}

function pick(o: string) {
  emit('update:modelValue', o)
  open.value = false
  watchViewport(false)
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
  watchViewport(false)
  kw.value = ''
}

// Enter takes the first remaining match — typing enough to narrow the list to one
// and pressing Enter is the whole point of a searchable select.
// creatable 时,与输入完全相同的选项优先,再是第一个匹配项,没有匹配才用输入值本身。
function onEnter() {
  const q = kw.value.trim()
  if (props.creatable && q && props.options.includes(q)) pick(q)
  else if (shown.value.length) pick(shown.value[0])
  else if (custom.value) pick(custom.value)
}

function close() {
  open.value = false
  watchViewport(false)
  kw.value = ''
}
</script>

<template>
  <div ref="root" class="vsel" tabindex="0" @focusout="onFocusOut" @keydown.esc.stop="close">
    <div class="control" :style="{ height }" @click="toggle">
      <span class="val" :class="{ ph: !modelValue }">{{ modelValue || placeholder }}</span>
      <ChevronDown :size="14" class="chev" />
    </div>
    <div v-if="open" ref="menuEl" class="menu" :style="floating ? menuStyle : undefined">
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
      <div class="scy list" :style="floating ? listStyle : undefined">
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
        <div v-if="custom" class="opt custom" data-testid="vsel-custom" @mousedown.prevent @click="pick(custom)">
          {{ $t('selUse', { v: custom }) }}
        </div>
        <div v-if="!shown.length && !custom" class="nores">{{ $t('selNoMatch') }}</div>
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
.val.ph { color: var(--text-faint); }
.opt.custom { color: var(--accent-text); }
.chev { color: var(--text-faint); flex-shrink: 0; }
/* 菜单宽度原先被 left:0 + right:0 钉死成控件的宽度。而控件宽度是各个页面按版面
   排的(导出页上只有 225px),菜单里装的却是实例名 —— 一串长得很像的标识符,截断
   之后正是最不该截的那一段。改成至少与控件同宽、按内容伸长、再给一个上限。 */
.menu {
  position: absolute;
  z-index: 30;
  top: calc(100% + 4px);
  left: 0;
  right: auto;
  min-width: 100%;
  width: max-content;
  max-width: min(460px, 92vw);
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
/* 220px 配 33px 的行高只露得出 6.67 行 —— 十来个实例要在一个切掉半行的小窗口里
   翻,而那半行看着像渲染坏了。放到 360px(约 10 行),同时用 vh 兜住:矮屏幕上不能
   让菜单长过视口。 */
.list { max-height: min(360px, 52vh); }
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
