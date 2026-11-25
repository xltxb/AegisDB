<script setup lang="ts">
// 数据查询页的 SQL 编辑器:行号、语法着色、当前行高亮。
//
// 做法是一个透明文字的 <textarea> 叠在着色的 <pre> 上 —— 输入、光标、选区、撤销、输入法都是
// 浏览器原生的,着色只是背后那一层画出来的同一段文字。不引编辑器库:这一页只写单条 SELECT,
// 用不上它们的大部分能力,却要多背几百 KB。着色复用 lib/sqlHighlight(与库树里看 DDL 同一个
// 分词器,两处对"什么是关键字"不会有分歧)。
//
// 两层必须逐像素对齐:同一套字体、字号、行高、内边距,都不折行(wrap="off"),滚动时着色层
// 跟着 textarea 走。
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { highlightSqlHtml } from '@/lib/sqlHighlight'

defineOptions({ inheritAttrs: false }) // data-testid 等落到 textarea 上

// completer:自动补全的候选来源(2026-10-26)。不传就没有补全,编辑器与之前完全一样。
// 返回光标前要替换掉的字符数和候选(候选就是要插入的文本,已按需加好引号)。
export type SqlCompleter = (text: string, cursor: number) => Promise<{ replace: number; items: string[] } | null>
const props = defineProps<{ modelValue: string; placeholder?: string; completer?: SqlCompleter }>()
const emit = defineEmits<{
  'update:modelValue': [string]
  run: []
  format: []
  cursor: [{ line: number; col: number }]
}>()

const ta = ref<HTMLTextAreaElement | null>(null)
const hl = ref<HTMLElement | null>(null)
const gutter = ref<HTMLElement | null>(null)
const line = ref(1)
const scrollTop = ref(0)

const lineCount = computed(() => Math.max(1, props.modelValue.split('\n').length))
// 行号只画看得见的那一段(上下用占位撑出总高度):每行一个元素时,几千行的 SQL 每敲一个键
// 都要比对几千个节点(2026-10-26 回归排查)。LH 与样式里的 --se-lh 一致。
const LH = 21
const viewH = ref(1200)
const gutterRange = computed(() => {
  const start = Math.max(0, Math.floor(scrollTop.value / LH) - 5)
  const end = Math.min(lineCount.value, start + Math.ceil(viewH.value / LH) + 10)
  return { start, end, lines: Array.from({ length: end - start }, (_, k) => start + k + 1) }
})
// 末尾补一个换行:最后一行是空行时 <pre> 不给它高度,两层就差一行
// 超大文本退回素文本:着色层几十万个节点光排版就要几百毫秒,每敲一个键都卡。这时 textarea 的
// 文字直接显示(与改版前的普通输入框一样快),着色层不画。
const PLAIN_LINES = 3000
const PLAIN_CHARS = 200_000
const plain = computed(() => props.modelValue.length > PLAIN_CHARS || lineCount.value > PLAIN_LINES)
const html = computed(() => (plain.value ? '' : highlightSqlHtml(props.modelValue) + '\n'))

function syncScroll() {
  const el = ta.value
  if (!el) return
  if (ac.value) closeAc() // 弹框跟着光标位置算,滚动后就不准了
  scrollTop.value = el.scrollTop
  viewH.value = el.clientHeight || viewH.value
  if (hl.value) {
    hl.value.scrollTop = el.scrollTop
    hl.value.scrollLeft = el.scrollLeft
  }
  if (gutter.value) gutter.value.scrollTop = el.scrollTop
}

// 拖大分割条 / 放大窗口时编辑器变高:行号可见段跟着变,不必等滚动或按键
let ro: ResizeObserver | null = null
onMounted(() => {
  if (ta.value?.clientHeight) viewH.value = ta.value.clientHeight
  if (ta.value && typeof ResizeObserver !== 'undefined') {
    ro = new ResizeObserver(() => { if (ta.value?.clientHeight) viewH.value = ta.value.clientHeight })
    ro.observe(ta.value)
  }
})
onBeforeUnmount(() => ro?.disconnect())

function updateCursor() {
  const el = ta.value
  if (!el) return
  viewH.value = el.clientHeight || viewH.value
  const before = el.value.slice(0, el.selectionStart)
  const l = before.split('\n').length
  const col = before.length - before.lastIndexOf('\n')
  line.value = l
  emit('cursor', { line: l, col })
}

function onInput(e: Event) {
  emit('update:modelValue', (e.target as HTMLTextAreaElement).value)
  updateCursor()
  // 敲的是标识符字符或点:稍等一下再取候选(连续打字时只取最后一次);删字时弹框开着就跟着更新
  const ie = e as InputEvent
  if (!props.completer) return
  if (ie.inputType === 'insertText' && ie.data && /[\p{L}\p{N}_$.]$/u.test(ie.data)) scheduleComplete()
  else if (ac.value && ie.inputType?.startsWith('delete')) scheduleComplete()
  else closeAc()
}

// ---- 自动补全弹框 ----
//
// 只在弹框开着的时候接管 ↑ ↓ Tab Enter Esc;关着时按键与之前完全一样(Tab 缩进、Esc→Tab 移走焦点、
// Ctrl+Enter 执行、Shift+Alt+F 格式化)。敲完的词已经和唯一候选一模一样时不弹 —— 否则敲完 SELECT
// 按回车想换行,会被补全吃掉。Ctrl+Space 手动唤出。
interface AcState { items: string[]; idx: number; replace: number; left: number; top: number; up: boolean }
const ac = ref<AcState | null>(null)
const acList = ref<HTMLElement | null>(null)
const AC_MAX = 100
const AC_ROW = 26
let acTimer: ReturnType<typeof setTimeout> | undefined
let acSeq = 0
let charW = 0
function closeAc() { ac.value = null; clearTimeout(acTimer); acSeq++ }
function scheduleComplete() {
  clearTimeout(acTimer)
  acTimer = setTimeout(() => requestComplete(false), 120)
}
function measureCharW(el: HTMLTextAreaElement): number {
  if (charW) return charW
  const c = document.createElement('canvas').getContext('2d')
  if (!c) return 7.8
  const cs = getComputedStyle(el)
  c.font = `${cs.fontWeight} ${cs.fontSize} ${cs.fontFamily}`
  charW = c.measureText('MMMMMMMMMM').width / 10 || 7.8
  return charW
}
async function requestComplete(manual: boolean) {
  const el = ta.value
  const fn = props.completer
  if (!el || !fn || el.selectionStart !== el.selectionEnd) { closeAc(); return }
  const seq = ++acSeq
  const text = el.value
  const pos = el.selectionStart
  let res: Awaited<ReturnType<SqlCompleter>> = null
  try { res = await fn(text, pos) } catch { res = null }
  // 等候选的时候人又动了:作废
  if (seq !== acSeq || el.value !== text || el.selectionStart !== pos) return
  const items = (res?.items || []).slice(0, AC_MAX)
  const prefix = text.slice(pos - (res?.replace || 0), pos)
  if (!res || !items.length || (!manual && items.length === 1 && items[0].toLowerCase() === prefix.toLowerCase())) {
    ac.value = null
    return
  }
  // 弹框放在光标下一行;下面放不下就放到上面
  const before = text.slice(0, pos)
  const lineIdx = before.split('\n').length - 1
  const col = before.length - before.lastIndexOf('\n') - 1
  const cs = getComputedStyle(el)
  const padL = parseFloat(cs.paddingLeft) || 14
  const padT = parseFloat(cs.paddingTop) || 12
  const left = Math.max(4, padL + col * measureCharW(el) - el.scrollLeft)
  const lineTop = padT + lineIdx * LH - el.scrollTop
  const h = Math.min(items.length, 8) * AC_ROW + 8
  const up = lineTop + LH + h > el.clientHeight && lineTop - h > 0
  ac.value = { items, idx: 0, replace: res.replace, left, top: up ? lineTop - h : lineTop + LH, up }
}
function acAccept(i = ac.value?.idx ?? 0) {
  const el = ta.value
  const st = ac.value
  if (!el || !st) return
  const item = st.items[i]
  const pos = el.selectionStart
  const from = Math.max(0, pos - st.replace)
  const v = el.value.slice(0, from) + item + el.value.slice(pos)
  closeAc()
  emit('update:modelValue', v)
  requestAnimationFrame(() => { el.focus(); el.setSelectionRange(from + item.length, from + item.length); updateCursor() })
}
function acMove(d: number) {
  const st = ac.value
  if (!st) return
  st.idx = (st.idx + d + st.items.length) % st.items.length
  requestAnimationFrame(() => acList.value?.children[st.idx]?.scrollIntoView({ block: 'nearest' }))
}
// 弹框开着时的按键;接管了返回 true
function acKeydown(e: KeyboardEvent): boolean {
  if (!ac.value) return false
  if (e.ctrlKey || e.metaKey || e.altKey) { closeAc(); return false } // Ctrl+Enter 等:关掉弹框,照常处理
  switch (e.key) {
    case 'ArrowDown': e.preventDefault(); acMove(1); return true
    case 'ArrowUp': e.preventDefault(); acMove(-1); return true
    case 'Enter':
    case 'Tab':
      if (e.shiftKey) { closeAc(); return false }
      e.preventDefault(); acAccept(); return true
    case 'Escape': e.preventDefault(); e.stopPropagation(); closeAc(); return true
    case 'ArrowLeft': case 'ArrowRight': case 'Home': case 'End': case 'PageUp': case 'PageDown':
      closeAc(); return false
  }
  return false
}
onBeforeUnmount(() => clearTimeout(acTimer))

// 按过 Esc 之后的那一下 Tab 照常移走焦点(与 VS Code / CodeMirror 同一个约定):Tab 被拿来缩进后,
// 纯键盘用户原本没法从编辑器 Tab 到「执行」按钮(2026-10-26 回归排查)。
let tabEscapes = false

function onKeydown(e: KeyboardEvent) {
  if (acKeydown(e)) return
  if (props.completer && e.ctrlKey && (e.key === ' ' || e.code === 'Space')) {
    e.preventDefault()
    requestComplete(true)
    return
  }
  if (e.key === 'Escape') { tabEscapes = true; return }
  const escaped = tabEscapes
  tabEscapes = false
  if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
    e.preventDefault()
    emit('run')
    return
  }
  // Shift + Alt + F:格式化(与 VS Code 同一个键)
  if (e.shiftKey && e.altKey && (e.key === 'F' || e.key === 'f')) {
    e.preventDefault()
    emit('format')
    return
  }
  // Tab 插两个空格,而不是把焦点移走 —— 写 SQL 时缩进比跳到下一个按钮常用得多
  if (e.key === 'Tab' && !escaped && !e.shiftKey && !e.ctrlKey && !e.altKey && !e.metaKey) {
    const el = ta.value
    if (!el) return
    e.preventDefault()
    const s = el.selectionStart
    const end = el.selectionEnd
    const v = el.value.slice(0, s) + '  ' + el.value.slice(end)
    emit('update:modelValue', v)
    requestAnimationFrame(() => { el.setSelectionRange(s + 2, s + 2); updateCursor() })
  }
}

defineExpose({
  focus: () => ta.value?.focus(),
  el: ta,
})
</script>

<template>
  <div class="se" :class="{ plain }">
    <div ref="gutter" class="se-gutter" aria-hidden="true">
      <div :style="{ height: `${gutterRange.start * LH}px` }" />
      <div v-for="n in gutterRange.lines" :key="n" class="se-ln" :class="{ on: n === line }">{{ n }}</div>
      <div :style="{ height: `${(lineCount - gutterRange.end) * LH}px` }" />
    </div>
    <div class="se-main">
      <!-- 当前行高亮:跟着光标所在行,随滚动平移 -->
      <div class="se-curline" :style="{ transform: `translateY(${(line - 1) * LH - scrollTop}px)` }" />
      <pre ref="hl" class="se-hl" aria-hidden="true" v-html="html" />
      <textarea
        ref="ta"
        v-bind="$attrs"
        class="se-input"
        :value="modelValue"
        :placeholder="placeholder"
        spellcheck="false"
        autocomplete="off"
        autocapitalize="off"
        wrap="off"
        @input="onInput"
        @keydown="onKeydown"
        @keyup="updateCursor"
        @click="updateCursor(); closeAc()"
        @select="updateCursor"
        @focus="updateCursor"
        @blur="closeAc"
        @scroll="syncScroll"
      />
      <!-- 自动补全弹框:mousedown.prevent 让焦点留在 textarea 上,点选不会先触发 blur 关掉它 -->
      <div v-if="ac" ref="acList" class="se-ac" role="listbox" data-testid="sql-ac"
           :style="{ left: `${ac.left}px`, top: `${ac.top}px`, maxHeight: `${8 * AC_ROW + 8}px` }">
        <div v-for="(it, i) in ac.items" :key="it + i" class="se-ac-item" :class="{ on: i === ac.idx }" role="option"
             :aria-selected="i === ac.idx" @mousedown.prevent @click="acAccept(i)" @mouseenter="ac.idx = i">{{ it }}</div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* 行高写死 21px:当前行高亮的位移按它算(见模板) */
.se { --se-lh: 21px; --se-pad: 12px; display: flex; flex: 1; min-height: 0; background: var(--surface-card); font: 400 13px/var(--se-lh) var(--font-mono);
  /* 关掉连字:>= 画成 ≥ 会让人以为写的是另一个字符 */
  font-variant-ligatures: none; font-feature-settings: "liga" 0, "calt" 0; }
.se-gutter { flex-shrink: 0; min-width: 44px; padding: var(--se-pad) 0; overflow: hidden; text-align: right; user-select: none;
  background: color-mix(in oklch, var(--surface-sunken) 60%, var(--surface-card)); border-right: 1px solid var(--border-subtle); color: var(--text-faint); }
.se-ln { padding: 0 10px 0 8px; height: var(--se-lh); font-size: 11.5px; font-variant-numeric: tabular-nums; }
.se-ln.on { color: var(--text-strong); font-weight: 600; }
.se-main { position: relative; flex: 1; min-width: 0; overflow: hidden; }
.se-curline { position: absolute; left: 0; right: 0; top: var(--se-pad); height: var(--se-lh); pointer-events: none;
  background: color-mix(in oklch, var(--accent) 7%, transparent); }
.se-hl, .se-input {
  position: absolute; inset: 0; margin: 0; padding: var(--se-pad) 14px; box-sizing: border-box;
  font: inherit; letter-spacing: 0; tab-size: 2; white-space: pre; overflow: auto; border: none;
}
.se-hl { color: var(--text-body); pointer-events: none; overflow: hidden; }
.se-input { resize: none; outline: none; background: transparent; color: transparent; caret-color: var(--text-strong); }
.se-input::placeholder { color: var(--text-faint); }
.se-ac { position: absolute; z-index: 5; min-width: 180px; max-width: 420px; overflow-y: auto; padding: 4px;
  background: var(--surface-card); border: 1px solid var(--border-default); border-radius: 8px; box-shadow: var(--shadow-lg); }
.se-ac-item { height: 26px; line-height: 26px; padding: 0 10px; border-radius: 6px; white-space: nowrap; overflow: hidden;
  text-overflow: ellipsis; font: 400 12.5px var(--font-mono); color: var(--text-body); cursor: pointer; }
.se-ac-item.on { background: var(--accent-subtle); color: var(--accent-text); }
.se.plain .se-input { color: var(--text-body); }
.se.plain .se-hl { display: none; }
.se-input::selection { background: color-mix(in oklch, var(--accent) 28%, transparent); }
.se-hl :deep(.sqlh-kw) { color: var(--accent-text); font-weight: 600; }
.se-hl :deep(.sqlh-str) { color: var(--warning-text); }
.se-hl :deep(.sqlh-num) { color: #a855f7; }
.se-hl :deep(.sqlh-cmt) { color: var(--text-faint); font-style: italic; }
.se-hl :deep(.sqlh-meta) { color: var(--success-text, var(--success)); }
</style>
