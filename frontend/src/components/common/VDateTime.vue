<script setup lang="ts">
/**
 * 日期时间选择器 —— 自己画的,不用 <input type="datetime-local">。
 *
 * 换掉原生控件的唯一原因是**语言**:Chromium 的日期面板(月份名、星期表头、"清除/今天")
 * 跟的是**浏览器的界面语言**,不是页面的 `lang`,也不归 vue-i18n 管。所以在一个已经
 * 切成英文的表单里,点开那个框弹出来的仍然是"2026年09月 一 二 三 …"。这不是漏翻,是
 * 一块页面根本改不动的浏览器 chrome —— 想让它跟着界面走,就只能不用它。
 *
 * 月份标题故意写成 `2026-09` 这种数字形式,而不是月份名:这样它对中英文读者都一样准确,
 * 也就不需要再维护十二个月份名的两份译文(以及将来第三份)。星期表头复用已有的
 * dow1..dow7 词条,与「班车」页上的发车日勾选是同一套字。
 *
 * 值的格式与原生控件保持一致(`YYYY-MM-DDTHH:mm`,本地时间),所以调用方那些
 * `new Date(v).toISOString()` 一行都不用改。
 */
import { ref, computed, watch, nextTick, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { CalendarDays, ChevronLeft, ChevronRight, X } from 'lucide-vue-next'

const props = defineProps<{ modelValue: string; placeholder?: string }>()
const emit = defineEmits<{ 'update:modelValue': [string] }>()
const { t } = useI18n()

const open = ref(false)
const root = ref<HTMLElement>()

const pad = (n: number) => String(n).padStart(2, '0')
const ymd = (d: Date) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`

/** 拆出日期与时间两半;值为空时时间给一个 00:00 的起点,而日期留空。 */
const datePart = computed(() => (props.modelValue || '').slice(0, 10))
const timePart = computed(() => (props.modelValue || '').slice(11, 16) || '00:00')

// 面板当前显示的月份。打开时对齐到已选日期,没选过就对齐到今天。
const cursor = ref(new Date())
watch(open, (v) => {
  if (!v) return
  const d = datePart.value ? new Date(`${datePart.value}T00:00:00`) : new Date()
  cursor.value = new Date(d.getFullYear(), d.getMonth(), 1)
})

const title = computed(() => `${cursor.value.getFullYear()}-${pad(cursor.value.getMonth() + 1)}`)
const dowLabels = computed(() => [1, 2, 3, 4, 5, 6, 7].map((n) => t(`dow${n}` as any)))

/**
 * 六行 × 七列的格子,周一起头。
 *
 * 固定六行是故意的:行数随月份变化时,面板的高度会跟着跳,而人正在里面点日期。
 */
const cells = computed(() => {
  const first = new Date(cursor.value.getFullYear(), cursor.value.getMonth(), 1)
  // getDay() 里周日是 0;这里按 ISO 把周一当第一天。
  const lead = (first.getDay() + 6) % 7
  const start = new Date(first)
  start.setDate(first.getDate() - lead)
  return Array.from({ length: 42 }, (_, i) => {
    const d = new Date(start)
    d.setDate(start.getDate() + i)
    return { key: ymd(d), day: d.getDate(), outside: d.getMonth() !== cursor.value.getMonth() }
  })
})

const todayKey = ymd(new Date())

function shiftMonth(n: number) {
  cursor.value = new Date(cursor.value.getFullYear(), cursor.value.getMonth() + n, 1)
}
function pickDay(key: string) {
  emit('update:modelValue', `${key}T${timePart.value}`)
}
function setTime(e: Event) {
  const v = (e.target as HTMLInputElement).value || '00:00'
  // 还没选日期就先调了时间:补上今天,而不是把这次输入丢掉。
  emit('update:modelValue', `${datePart.value || todayKey}T${v}`)
}
function pickToday() { emit('update:modelValue', `${todayKey}T${timePart.value}`) }
function clear() { emit('update:modelValue', ''); open.value = false }

/**
 * 面板挂到 body 上,用 fixed 定位。
 *
 * 因为这个控件常常长在一个**会裁剪**的容器里 —— 弹窗的表单区是 overflow:auto,
 * 卡片是 overflow:hidden。用绝对定位跟着字段走的话,面板一伸出容器就被切掉,或者
 * 逼出一条本不该有的滚动条(这正是它被提出来的样子)。挂到 body 就没有祖先能裁它。
 *
 * 位置每次打开时算:下方放得下就朝下,放不下就朝上翻;左右夹在视口里,免得贴着右
 * 边缘的字段把面板顶出屏幕。窗口尺寸变化或页面滚动时重算一次 —— fixed 不会自己
 * 跟着走。
 */
const popEl = ref<HTMLElement>()
const popStyle = ref<Record<string, string>>({})
const fieldEl = ref<HTMLElement>()

async function place() {
  const f = fieldEl.value
  if (!f) return
  await nextTick()
  const r = f.getBoundingClientRect()
  const h = popEl.value?.offsetHeight || 320
  const w = popEl.value?.offsetWidth || 252
  const GAP = 6
  const below = window.innerHeight - r.bottom - GAP
  const top = below >= h ? r.bottom + GAP : Math.max(8, r.top - h - GAP)
  const left = Math.min(Math.max(8, r.left), Math.max(8, window.innerWidth - w - 8))
  popStyle.value = { top: `${top}px`, left: `${left}px` }
}

// 点面板外面关掉。面板已经不在 root 里了(Teleport 走了),所以两处都要问。
function onDocDown(e: MouseEvent) {
  const t = e.target as Node
  if (root.value?.contains(t) || popEl.value?.contains(t)) return
  open.value = false
}
const reposition = () => { if (open.value) place() }
watch(open, (v) => {
  if (v) {
    place()
    document.addEventListener('mousedown', onDocDown)
    window.addEventListener('resize', reposition)
    // capture:内层滚动容器的滚动不冒泡,不捕获就跟不上。
    window.addEventListener('scroll', reposition, true)
  } else {
    document.removeEventListener('mousedown', onDocDown)
    window.removeEventListener('resize', reposition)
    window.removeEventListener('scroll', reposition, true)
  }
})
// 月份翻页会改变面板高度(六行固定,其实不会),但朝上翻时顶边要跟着重算。
watch(cursor, reposition)
onBeforeUnmount(() => {
  document.removeEventListener('mousedown', onDocDown)
  window.removeEventListener('resize', reposition)
  window.removeEventListener('scroll', reposition, true)
})

const display = computed(() => (props.modelValue ? props.modelValue.replace('T', ' ').slice(0, 16) : ''))
</script>

<template>
  <div ref="root" class="vdt">
    <button ref="fieldEl" type="button" class="field" :class="{ on: open }" @click="open = !open">
      <CalendarDays :size="13" />
      <span :class="{ ph: !display }">{{ display || (placeholder || $t('dtPick')) }}</span>
      <X v-if="display" class="clr" :size="12" @click.stop="clear" />
    </button>

    <Teleport to="body">
    <div v-if="open" ref="popEl" class="pop" :style="popStyle">
      <div class="phead">
        <button type="button" class="nav" @click="shiftMonth(-1)"><ChevronLeft :size="14" /></button>
        <span class="ym">{{ title }}</span>
        <button type="button" class="nav" @click="shiftMonth(1)"><ChevronRight :size="14" /></button>
      </div>
      <div class="dows"><span v-for="d in dowLabels" :key="d">{{ d }}</span></div>
      <div class="grid">
        <button
          v-for="c in cells" :key="c.key" type="button"
          class="cell" :class="{ out: c.outside, sel: c.key === datePart, today: c.key === todayKey }"
          @click="pickDay(c.key)"
        >{{ c.day }}</button>
      </div>
      <div class="pfoot">
        <!-- 时间仍用原生 time 控件:它弹出来的是一列数字,没有需要翻译的词。 -->
        <input class="tin" type="time" :value="timePart" @change="setTime" />
        <button type="button" class="lnk" @click="pickToday">{{ $t('dtToday') }}</button>
        <button type="button" class="lnk" @click="clear">{{ $t('dtClear') }}</button>
        <button type="button" class="lnk primary" @click="open = false">{{ $t('dtDone') }}</button>
      </div>
    </div>
    </Teleport>
  </div>
</template>

<!-- scoped 保留:Teleport 出去的节点照样带着组件的 data-v- 标记(scope id 是按
     渲染它的组件加的,与它落在 DOM 的哪里无关),所以 .pop 的样式跟得过去。
     不 scoped 的话,.field / .cell / .grid 这些通用名会变成全局样式砸到别的页面上。 -->
<style scoped>
.vdt { position: relative; }
.field {
  width: 100%; box-sizing: border-box; display: flex; align-items: center; gap: 7px;
  height: 36px; padding: 0 11px; cursor: pointer; text-align: left;
  border: 1px solid var(--border-default); border-radius: var(--radius-md);
  background: var(--surface-sunken); color: var(--text-body); font: 500 12.5px var(--font-mono);
}
.field.on, .field:hover { border-color: var(--accent-text); }
.field .ph { color: var(--text-faint); }
.field span { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.clr { flex-shrink: 0; color: var(--text-faint); }
.clr:hover { color: var(--danger-text); }
/* fixed + 挂在 body 上:没有祖先能裁它。z-index 要压过弹窗(80)。 */
.pop {
  position: fixed; z-index: 200; width: 252px;
  padding: 10px; border-radius: var(--radius-md);
  background: var(--surface-card); border: 1px solid var(--border-default); box-shadow: var(--shadow-lg);
}
.phead { display: flex; align-items: center; justify-content: space-between; margin-bottom: 8px; }
.ym { font: 700 12.5px var(--font-mono); color: var(--text-strong); }
.nav { width: 24px; height: 24px; display: grid; place-items: center; border: none; border-radius: var(--radius-sm); background: transparent; color: var(--text-muted); cursor: pointer; }
.nav:hover { background: var(--surface-sunken); color: var(--accent-text); }
.dows, .grid { display: grid; grid-template-columns: repeat(7, 1fr); gap: 2px; }
.dows span { text-align: center; padding: 4px 0; font: 600 10px var(--font-mono); color: var(--text-faint); }
.cell {
  height: 28px; border: none; border-radius: var(--radius-sm); background: transparent;
  color: var(--text-body); font: 500 11.5px var(--font-mono); cursor: pointer;
}
.cell:hover { background: var(--surface-sunken); }
/* 相邻月份的日子留在格子里但压暗:抹掉它们会让月初月末那一行看着缺一块。 */
.cell.out { color: var(--text-faint); }
.cell.today { box-shadow: inset 0 0 0 1px var(--accent-subtle-border); }
.cell.sel { background: var(--accent-text); color: var(--text-on-accent); }
.pfoot { display: flex; align-items: center; gap: 6px; margin-top: 9px; padding-top: 9px; border-top: 1px solid var(--border-subtle); }
.tin {
  width: 92px; height: 28px; padding: 0 7px; border: 1px solid var(--border-default);
  border-radius: var(--radius-sm); background: var(--surface-sunken); color: var(--text-body);
  font: 500 11.5px var(--font-mono); outline: none;
}
.lnk { border: none; background: transparent; color: var(--text-muted); font: 600 11px var(--font-body); cursor: pointer; padding: 0 4px; }
.lnk:hover { color: var(--accent-text); }
.lnk.primary { margin-left: auto; color: var(--accent-text); }
</style>
