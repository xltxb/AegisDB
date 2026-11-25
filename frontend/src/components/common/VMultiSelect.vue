<script setup lang="ts">
/**
 * 多选下拉。VSelect 的兄弟,不是它的一个开关 —— 两者的取值类型不同(一个 string、
 * 一个 string[]),混成一个组件的话每个调用点都要先分辨自己拿到的是哪一种。
 *
 * 选项用 { v, label } 而不是裸字符串:VSelect 认的是**显示文案**,于是每个调用点都要
 * 写一段"把标签映射回内部取值"的来回。多选再走一遍那条路,映射会写四份。
 */
import { ref, computed } from 'vue'
import { ChevronDown, Check } from 'lucide-vue-next'

const props = withDefaults(
  defineProps<{
    /** 已选中的内部取值;空数组 = 不筛 */
    modelValue: string[]
    options: { v: string; label: string }[]
    /** 一个都没选时显示的文案 —— 说"全部…",不是"请选择" */
    placeholder: string
    /** 选了 n 项时的文案模板,{n} 会被替换掉 */
    summary?: string
    height?: string
  }>(),
  { height: '36px', summary: '' },
)
const emit = defineEmits<{ 'update:modelValue': [string[]] }>()

const open = ref(false)
const root = ref<HTMLElement | null>(null)

// 一项时直接显示那一项的名字:「已选 1 项」要人再点开一次才知道是哪一项,而这一格
// 本来就放得下。
const label = computed(() => {
  const n = props.modelValue.length
  if (!n) return props.placeholder
  if (n === 1) return props.options.find((o) => o.v === props.modelValue[0])?.label || props.placeholder
  return (props.summary || '{n}').replace('{n}', String(n))
})

function toggle() { open.value = !open.value }
function close() { open.value = false }

function pick(v: string) {
  const next = props.modelValue.includes(v)
    ? props.modelValue.filter((x) => x !== v)
    : [...props.modelValue, v]
  emit('update:modelValue', next)
}
function clearAll() { emit('update:modelValue', []) }

// 与 VSelect 同一条:focusout 会冒泡并带着 relatedTarget,所以焦点真的离开这个组件
// 时才关 —— blur 不冒泡,菜单会在点中一项的那一刻自己关掉。
function onFocusOut(e: FocusEvent) {
  const next = e.relatedTarget as Node | null
  if (next && root.value?.contains(next)) return
  open.value = false
}
</script>

<template>
  <div ref="root" class="vmsel" tabindex="0" @focusout="onFocusOut" @keydown.esc.stop="close">
    <div class="control" :class="{ on: modelValue.length }" :style="{ height }" @click="toggle">
      <span class="val">{{ label }}</span>
      <ChevronDown :size="14" class="chev" />
    </div>
    <div v-if="open" class="menu">
      <div class="scy list">
        <!-- mousedown.prevent 保住焦点,免得点中一项时先触发 focusout -->
        <div
          v-for="o in options"
          :key="o.v"
          class="opt"
          :class="{ active: modelValue.includes(o.v) }"
          @mousedown.prevent
          @click="pick(o.v)"
        >
          <span class="box"><Check v-if="modelValue.includes(o.v)" :size="11" /></span>
          {{ o.label }}
        </div>
      </div>
      <!-- 清空只在选过东西时出现:一个永远亮着、点了什么也不会变的按钮是噪音 -->
      <div v-if="modelValue.length" class="clr" @mousedown.prevent @click="clearAll">
        {{ placeholder }}
      </div>
    </div>
  </div>
</template>

<style scoped>
.vmsel { position: relative; outline: none; }
.control {
  display: flex; align-items: center; justify-content: space-between; gap: 8px;
  padding: 0 12px; border: 1px solid var(--border-default); border-radius: var(--radius-md);
  background: var(--surface-card); color: var(--text-body);
  font: 500 12.5px var(--font-body); cursor: pointer;
}
/* 选过东西的控件要和没选过的一眼分得开 —— 否则筛选是开着的这件事只能靠回想 */
.control.on { border-color: var(--accent-subtle-border); background: var(--accent-subtle); color: var(--accent-text); }
.val { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.chev { flex-shrink: 0; opacity: .7; }
.menu {
  position: absolute; z-index: 30; top: calc(100% + 4px); left: 0;
  min-width: 100%; width: max-content; max-width: min(360px, 92vw);
  background: var(--surface-card); border: 1px solid var(--border-default);
  border-radius: var(--radius-md); box-shadow: var(--shadow-lg); padding: 5px;
}
.list { max-height: min(320px, 52vh); }
.opt {
  display: flex; align-items: center; gap: 9px; padding: 7px 10px; border-radius: 8px;
  font: 500 12.5px var(--font-body); color: var(--text-body); cursor: pointer; white-space: nowrap;
}
.opt:hover { background: var(--surface-sunken); }
.opt.active { color: var(--accent-text); }
.box {
  flex: none; display: grid; place-items: center; width: 15px; height: 15px;
  border: 1px solid var(--border-default); border-radius: 4px; background: var(--surface-card);
}
.opt.active .box { background: var(--accent); border-color: var(--accent); color: #fff; }
.clr {
  margin-top: 4px; padding: 7px 10px; border-top: 1px solid var(--border-subtle);
  font: 500 12px var(--font-body); color: var(--text-muted); cursor: pointer; text-align: center;
}
.clr:hover { color: var(--accent-text); }
</style>
