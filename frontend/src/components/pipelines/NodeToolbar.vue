<script setup lang="ts">
// 步骤列表底部的「+ 添加步骤」。
//
// 以前是一排硬邦邦的文字按钮贴在表单底下。这里改成一条分割线 + 居中的一个入口,点开
// 才出现按类型排开的图标卡片 —— 七种类型一字排开常驻在页面上,读的人每次都要把它们
// 全扫一遍;而加步骤是偶发动作,不该一直占着版面。
import { ref } from 'vue'
import {
  Plus, ShieldCheck, ClipboardCheck, DatabaseBackup, Terminal, Zap, Search, Hourglass, Bell,
} from 'lucide-vue-next'
import type { StageType } from '@/types'

// disabled:暂时不能加的类型 → 原因(悬停提示)。「自动执行」只在允许它的分层上能加,
// 卡片照样摆出来、置灰并说明为什么 —— 藏起来的话,没人知道还有这一种步骤。
defineProps<{ types: StageType[]; disabled?: Partial<Record<StageType, string>> }>()
const emit = defineEmits<{ add: [type: StageType] }>()

const open = ref(false)
const root = ref<HTMLElement | null>(null)

const icon: Record<StageType, any> = {
  review: ShieldCheck, approve: ClipboardCheck, backup: DatabaseBackup,
  execute: Terminal, auto_execute: Zap, verify: Search, manual: Hourglass, notify: Bell,
}

function pick(t: StageType) {
  emit('add', t)
  open.value = false
}
// 焦点离开整个组件就收起 —— 与 VSelect 同一种关法,不另装全局点击监听。
function onFocusOut(e: FocusEvent) {
  if (!root.value?.contains(e.relatedTarget as Node | null)) open.value = false
}
</script>

<template>
  <div ref="root" class="toolbar" tabindex="-1" @focusout="onFocusOut" @keydown.esc="open = false">
    <div class="rule" />
    <button type="button" class="add" :class="{ open }" @click="open = !open">
      <Plus :size="14" />{{ $t('plAddStep') }}
    </button>
    <div class="rule" />

    <div v-if="open" class="pop">
      <div class="pt">{{ $t('plPickType') }}</div>
      <div class="cards">
        <button v-for="ty in types" :key="ty" type="button" class="card" :data-testid="'add-step-' + ty"
                :disabled="!!disabled?.[ty]" :title="disabled?.[ty] || undefined" @click="pick(ty)">
          <span class="ci"><component :is="icon[ty]" :size="15" /></span>
          <span class="cn">{{ $t('plType_' + ty) }}</span>
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.toolbar { position: relative; display: flex; align-items: center; gap: 12px; padding: 4px 0; outline: none; }
.rule { flex: 1; height: 1px; background: var(--border-subtle); }
.add {
  display: inline-flex; align-items: center; gap: 6px; height: 32px; padding: 0 14px;
  border: 1px dashed var(--border-default); border-radius: var(--radius-full);
  background: color-mix(in srgb, var(--surface-card) 60%, transparent);
  color: var(--text-muted); font: 500 12.5px var(--font-body); cursor: pointer;
  transition: all 0.15s ease;
}
.add:hover, .add.open { border-color: var(--accent); color: var(--accent-text); background: var(--surface-card); border-style: solid; }
.pop {
  position: absolute; left: 50%; bottom: calc(100% + 8px); transform: translateX(-50%);
  min-width: 460px; max-width: 100%; padding: 12px;
  border: 1px solid var(--border-subtle); border-radius: var(--radius-lg);
  background: var(--surface-card); box-shadow: var(--shadow-md); z-index: 5;
}
.pt { font: 500 12px var(--font-body); color: var(--text-faint); margin: 0 2px 10px; }
.cards { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 8px; }
.card {
  display: flex; flex-direction: column; align-items: center; gap: 7px; padding: 12px 8px;
  border: 1px dashed var(--border-default); border-radius: var(--radius-md);
  background: color-mix(in srgb, var(--surface-card) 60%, transparent);
  color: var(--text-body); cursor: pointer; transition: all 0.15s ease;
}
.card:disabled { opacity: .45; cursor: not-allowed; }
.card:not(:disabled):hover { border-color: var(--accent); border-style: solid; color: var(--accent-text); background: var(--accent-subtle); }
.ci { width: 30px; height: 30px; border-radius: 9px; display: flex; align-items: center; justify-content: center; background: var(--surface-sunken); color: inherit; transition: all 0.15s ease; }
.card:not(:disabled):hover .ci { background: var(--surface-card); }
.cn { font: 500 12px var(--font-body); }
@media (max-width: 768px) { .pop { min-width: 0; width: 100%; } .cards { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
</style>
