<script setup lang="ts">
import { computed, ref } from 'vue'
import { X, Copy, Check, Table2 } from 'lucide-vue-next'

// more:后端还暂存着后续行(结果分批显示),表格底部给「加载更多」;loading 时按钮锁住
const props = defineProps<{ columns: string[]; rows: string[][]; more?: boolean }>()
const emit = defineEmits<{ close: []; more: [] }>()
const loadingMore = ref(false)
function onMore() {
  if (loadingMore.value) return
  loadingMore.value = true
  emit('more')
  // 加载完成后父组件会换掉 rows;兜底 3 秒后解锁,免得请求失败时按钮一直灰着
  setTimeout(() => { loadingMore.value = false }, 3000)
}

// A column is numeric (right-aligned) when every non-empty cell parses as a number.
const numeric = computed(() =>
  props.columns.map((_, i) => {
    let saw = false
    for (const r of props.rows) {
      const v = r[i]
      if (v == null || v === '') continue
      saw = true
      if (!/^-?\d+(\.\d+)?$/.test(String(v).trim())) return false
    }
    return saw
  }),
)

// 单元格只占一行:值里的换行显示成 ↵。DWS 的 pgxc_wlm_session_info 这类视图里有整段 SQL、执行
// 计划(几百行),按原样换行会把整行撑到几千像素高;单元格又是垂直居中,其它列的值全落在屏幕
// 下方很远处 —— 表头下面看起来一片空白(2026-10 生产反馈)。完整值在悬停提示里,复制 TSV 也是完整的。
const oneLine = (v: string) => v.replace(/\r\n|\r|\n/g, ' ↵ ').replace(/\t/g, ' ')

const copied = ref(false)
async function copyTsv() {
  const head = props.columns.join('\t')
  const body = props.rows.map((r) => r.map((v) => (v ?? '').replace(/[\t\r\n]+/g, ' ')).join('\t')).join('\n')
  try {
    await navigator.clipboard.writeText(head + '\n' + body)
    copied.value = true
    setTimeout(() => (copied.value = false), 1600)
  } catch { /* ignore */ }
}
</script>

<template>
  <div class="rg">
    <div class="rghead">
      <Table2 :size="14" color="var(--accent-text)" />
      <span class="rgt">{{ $t('gridResult') }}</span>
      <span class="rgn">{{ $t('gridRows', { n: rows.length }) }}</span>
      <div class="rgacts">
        <button class="rgbtn" :title="$t('gridCopyTsv')" @click="copyTsv"><component :is="copied ? Check : Copy" :size="13" />{{ copied ? $t('copied') : 'TSV' }}</button>
        <button class="rgbtn" :title="$t('gridClose')" @click="emit('close')"><X :size="14" /></button>
      </div>
    </div>
    <div class="rgscroll">
      <table class="rgtable">
        <thead>
          <tr>
            <th class="rgidx">#</th>
            <th v-for="(col, i) in columns" :key="i" :class="{ num: numeric[i] }">{{ col }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(r, ri) in rows" :key="ri">
            <td class="rgidx">{{ ri + 1 }}</td>
            <td v-for="(col, ci) in columns" :key="ci" :class="{ num: numeric[ci], null: r[ci] == null || r[ci] === '' }" :title="r[ci] || undefined">{{ r[ci] == null || r[ci] === '' ? '∅' : oneLine(r[ci]) }}</td>
          </tr>
          <tr v-if="!rows.length"><td :colspan="columns.length + 1" class="rgempty">{{ $t('gridEmpty') }}</td></tr>
        </tbody>
      </table>
      <div v-if="more" class="rgmore">
        <button class="rgbtn" :disabled="loadingMore" data-testid="grid-load-more" @click="onMore">{{ $t('gridLoadMore') }}</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.rg { display: flex; flex-direction: column; min-height: 0; border-top: 1px solid var(--border-default); background: var(--surface-card); }
.rghead { display: flex; align-items: center; gap: 8px; height: 34px; padding: 0 12px; border-bottom: 1px solid var(--border-subtle); background: var(--surface-sunken); flex-shrink: 0; }
.rgt { font: 600 12px var(--font-display); color: var(--text-strong); }
.rgn { font: 500 11px var(--font-mono); color: var(--text-muted); }
.rgacts { margin-left: auto; display: flex; gap: 6px; }
.rgbtn { display: inline-flex; align-items: center; gap: 5px; height: 24px; padding: 0 8px; border: 1px solid var(--border-default); border-radius: 7px; background: var(--surface-card); color: var(--text-muted); font: 600 11px var(--font-mono); cursor: pointer; }
.rgbtn:hover { border-color: var(--accent-text); color: var(--accent-text); }
.rgscroll { flex: 1; min-height: 0; overflow: auto; }
.rgmore { display: flex; justify-content: center; padding: 10px 0 14px; }
.rgtable { border-collapse: separate; border-spacing: 0; font: 500 12px var(--font-mono); color: var(--text-body); }
.rgtable th, .rgtable td { vertical-align: top; padding: 5px 12px; border-bottom: 1px solid var(--border-subtle); border-right: 1px solid var(--border-subtle); white-space: pre; text-align: left; max-width: 480px; overflow: hidden; text-overflow: ellipsis; }
.rgtable th { position: sticky; top: 0; z-index: 1; background: var(--surface-raised); color: var(--text-strong); font-weight: 600; border-bottom: 1px solid var(--border-default); }
.rgtable th.num, .rgtable td.num { text-align: right; }
.rgtable td.num { color: var(--cyan-400, #38bdf8); }
.rgtable td.null { color: var(--text-faint); }
.rgtable tbody tr:nth-child(even) td { background: var(--surface-sunken); }
.rgtable tbody tr:hover td { background: var(--accent-subtle); }
.rgidx { color: var(--text-faint); text-align: right; user-select: none; background: var(--surface-sunken); position: sticky; left: 0; }
.rgtable th.rgidx { z-index: 2; }
.rgempty { text-align: center; color: var(--text-faint); padding: 20px; }
</style>
