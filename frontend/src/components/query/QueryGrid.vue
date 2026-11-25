<script setup lang="ts">
// 数据查询页的结果表格:粘性表头、按值推断的列类型、斑马纹与悬停高亮、单元格省略 + 一键复制、
// 前端分页。行数本来就被授权上限卡住(默认 1000),全在内存里,分页只是为了不一次画几千个格子。
//
// 底栏左边留给父组件放状态(成功 / 行数 / 耗时 / 截断 / 脱敏),右边是分页。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Hash, CalendarClock, ToggleLeft, Type, Copy, ChevronLeft, ChevronRight, ChevronsLeft, ChevronsRight } from 'lucide-vue-next'
import { inferColumnType, type ColumnType } from '@/lib/columnType'
import { copyText } from '@/lib/clipboard'
import { highlightSqlHtml } from '@/lib/sqlHighlight'
import { useUIStore } from '@/stores/ui'

// vertical:结尾写了 \G —— 像 mysql 客户端那样一行一块、每列一行(宽表看单行时用)。
// ddl:SHOW CREATE TABLE 的结果(见 lib/bizResultView)—— 画成一整块代码,不塞进单元格。
const props = defineProps<{ columns: string[]; rows: string[][]; vertical?: boolean; ddl?: { name: string; ddl: string } | null }>()
const ddlHtml = computed(() => (props.ddl ? highlightSqlHtml(props.ddl.ddl) : ''))
const { t } = useI18n()
const ui = useUIStore()

const types = computed<ColumnType[]>(() => props.columns.map((_, i) => inferColumnType(props.rows, i)))
const typeIcon = { num: Hash, date: CalendarClock, bool: ToggleLeft, text: Type } as const
const typeLabel = (k: ColumnType) => t(({ num: 'qTypeNum', date: 'qTypeDate', bool: 'qTypeBool', text: 'qTypeText' } as const)[k])

const PAGE_SIZES = [50, 100, 200, 500]
const pageSize = ref(100)
const page = ref(1)
const pages = computed(() => Math.max(1, Math.ceil(props.rows.length / pageSize.value)))
const start = computed(() => (page.value - 1) * pageSize.value)
const visible = computed(() => props.rows.slice(start.value, start.value + pageSize.value))
watch(() => props.rows, () => { page.value = 1 })
watch(pageSize, () => { page.value = 1 })
const go = (p: number) => { page.value = Math.min(pages.value, Math.max(1, p)) }

// 服务端把 NULL 与空字符串都送成 ''(cellString),这里分不出来:空串画 ∅ 并在提示里说明,不冒充 NULL
// (f7eec1d 起一律写成 NULL,把真正的空串也标成了 NULL —— 2026-10-26 回归排查)。
const empty = (v: string | null | undefined) => v == null || v === ''
// 单元格只占一行:换行显示成 ↵(完整值在悬停提示里、复制出去的也是完整值)
const oneLine = (v: string) => v.replace(/\r\n|\r|\n/g, ' ↵ ').replace(/\t/g, ' ')

// 竖排时列名右对齐成一栏(与 mysql 客户端一样),按最长的列名定宽,封顶免得一个超长列名把值挤没了。
const keyWidth = computed(() => `${Math.min(36, Math.max(4, ...props.columns.map((c) => c.length)) + 1)}ch`)

async function copyCell(v: string | null | undefined) {
  if (await copyText(v ?? '')) ui.notify(t('qCellCopied'), 'success')
}
</script>

<template>
  <div class="qg">
    <div v-if="ddl" class="qg-scroll scy scx qg-ddlwrap" data-testid="q-ddl-view">
      <div class="qg-ddlh">
        <span class="qg-ddln">{{ ddl.name }}</span>
        <button class="qg-ddlcopy" data-testid="q-ddl-copy" @click="copyCell(ddl.ddl)"><Copy :size="12" />{{ $t('qCopyDdl') }}</button>
      </div>
      <pre class="qg-ddl" v-html="ddlHtml" />
    </div>
    <div v-else-if="vertical" class="qg-scroll scy scx" data-testid="q-vertical">
      <div v-for="(r, ri) in visible" :key="start + ri" class="qg-rec">
        <div class="qg-rec-h">*************************** {{ start + ri + 1 }}. row ***************************</div>
        <div v-for="(col, ci) in columns" :key="ci" class="qg-kv">
          <span class="qg-k" :style="{ width: keyWidth }">{{ col }}:</span>
          <span class="qg-v" :class="{ null: empty(r[ci]) }">{{ r[ci] == null ? 'NULL' : r[ci] === '' ? '∅' : r[ci] }}</span>
          <button v-if="!empty(r[ci])" class="qg-copy qg-kv-copy" :title="$t('qCopyCell')" @click="copyCell(r[ci])"><Copy :size="11" /></button>
        </div>
      </div>
      <div v-if="!rows.length" class="qg-empty">{{ $t('gridEmpty') }}</div>
    </div>
    <div v-else class="qg-scroll scy scx">
      <table class="qg-table">
        <thead>
          <tr>
            <th class="qg-idx">#</th>
            <th v-for="(col, i) in columns" :key="i" :class="{ num: types[i] === 'num' }">
              <span class="qg-th">
                <component :is="typeIcon[types[i]]" :size="12" class="qg-type" :title="$t('qTypeInferred', { type: typeLabel(types[i]) })" />
                <span class="qg-col" :title="col">{{ col }}</span>
              </span>
            </th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(r, ri) in visible" :key="start + ri">
            <td class="qg-idx">{{ start + ri + 1 }}</td>
            <td v-for="(_, ci) in columns" :key="ci" :class="{ num: types[ci] === 'num', null: empty(r[ci]) }">
              <span class="qg-cell" :title="empty(r[ci]) ? t('gridEmptyTip') : r[ci]">{{ r[ci] == null ? 'NULL' : r[ci] === '' ? '∅' : oneLine(r[ci]) }}</span>
              <button v-if="!empty(r[ci])" class="qg-copy" :title="$t('qCopyCell')" @click="copyCell(r[ci])"><Copy :size="11" /></button>
            </td>
          </tr>
          <tr v-if="!rows.length"><td :colspan="columns.length + 1" class="qg-empty">{{ $t('gridEmpty') }}</td></tr>
        </tbody>
      </table>
    </div>
    <div class="qg-foot">
      <div class="qg-status"><slot name="status" /></div>
      <div v-if="!ddl && rows.length > PAGE_SIZES[0]" class="qg-pager" data-testid="q-pager">
        <select v-model.number="pageSize" class="qg-size" :aria-label="$t('qPageSizeLabel')">
          <option v-for="n in PAGE_SIZES" :key="n" :value="n">{{ $t('qPageSize', { n }) }}</option>
        </select>
        <button class="qg-pbtn" :disabled="page === 1" :aria-label="$t('qPageFirst')" @click="go(1)"><ChevronsLeft :size="14" /></button>
        <button class="qg-pbtn" :disabled="page === 1" :aria-label="$t('qPagePrev')" @click="go(page - 1)"><ChevronLeft :size="14" /></button>
        <span class="qg-pno">{{ $t('qPageOf', { p: page, total: pages }) }}</span>
        <button class="qg-pbtn" :disabled="page === pages" :aria-label="$t('qPageNext')" @click="go(page + 1)"><ChevronRight :size="14" /></button>
        <button class="qg-pbtn" :disabled="page === pages" :aria-label="$t('qPageLast')" @click="go(pages)"><ChevronsRight :size="14" /></button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.qg { display: flex; flex-direction: column; flex: 1; min-height: 0; font-variant-ligatures: none; font-feature-settings: "liga" 0, "calt" 0; }
.qg-scroll { flex: 1; min-height: 0; overflow: auto; }
.qg-table { border-collapse: separate; border-spacing: 0; min-width: 100%; font: 400 12.5px/1.4 var(--font-mono); color: var(--text-body); }
.qg-table th {
  position: sticky; top: 0; z-index: 1; text-align: left; white-space: nowrap; padding: 0 12px; height: 34px;
  background: color-mix(in oklch, var(--surface-sunken) 70%, var(--surface-card));
  border-bottom: 1px solid var(--border-default); border-right: 1px solid var(--border-subtle);
  font: 600 12px var(--font-body); color: var(--text-strong);
}
.qg-th { display: inline-flex; align-items: center; gap: 6px; max-width: 280px; }
.qg-type { flex-shrink: 0; color: var(--text-faint); }
.qg-col { overflow: hidden; text-overflow: ellipsis; }
.qg-table th.num .qg-th { flex-direction: row-reverse; }
.qg-table td {
  position: relative; max-width: 320px; height: 30px; padding: 0 12px; white-space: nowrap;
  border-bottom: 1px solid var(--border-subtle); border-right: 1px solid var(--border-subtle);
}
.qg-table tbody tr:nth-child(even) td { background: color-mix(in oklch, var(--surface-sunken) 45%, var(--surface-card)); }
.qg-table tbody tr:hover td { background: color-mix(in oklch, var(--accent) 8%, var(--surface-card)); }
.qg-table .num { text-align: right; font-variant-numeric: tabular-nums; }
.qg-cell { display: block; overflow: hidden; text-overflow: ellipsis; }
.qg-table td.null .qg-cell { color: var(--text-faint); font-style: italic; font-size: 11px; }
.qg-copy {
  position: absolute; right: 4px; top: 50%; transform: translateY(-50%); display: none; align-items: center; justify-content: center;
  width: 20px; height: 20px; border: 1px solid var(--border-subtle); border-radius: 5px;
  background: var(--surface-card); color: var(--text-muted); cursor: pointer;
}
.qg-table td:hover .qg-copy, .qg-copy:focus-visible { display: flex; }
.qg-copy:hover { color: var(--accent-text); border-color: var(--accent-text); }
.qg-idx { position: sticky; left: 0; z-index: 1; width: 44px; text-align: right; color: var(--text-faint); font-size: 11px;
  background: var(--surface-card); }
.qg-table th.qg-idx { z-index: 2; }
.qg-ddlwrap { padding: 10px 14px 14px; }
.qg-ddlh { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; }
.qg-ddln { font: 600 12.5px var(--font-mono); color: var(--text-strong); }
.qg-ddlcopy { display: inline-flex; align-items: center; gap: 5px; height: 26px; padding: 0 9px; margin-left: auto;
  border: 1px solid var(--border-default); border-radius: 6px; background: var(--surface-card); color: var(--text-muted);
  font: 500 12px var(--font-body); cursor: pointer; }
.qg-ddlcopy:hover { color: var(--accent-text); border-color: var(--accent-text); }
.qg-ddl { margin: 0; padding: 12px 14px; border: 1px solid var(--border-subtle); border-radius: 8px; background: var(--surface-sunken);
  font: 400 12.5px/1.6 var(--font-mono); color: var(--text-body); white-space: pre; overflow-x: auto; tab-size: 4; }
.qg-ddl :deep(.sqlh-kw) { color: var(--accent-text); font-weight: 600; }
.qg-ddl :deep(.sqlh-str) { color: var(--warning-text); }
.qg-ddl :deep(.sqlh-num) { color: #a855f7; }
.qg-ddl :deep(.sqlh-cmt) { color: var(--text-faint); font-style: italic; }
.qg-rec { padding: 6px 14px 10px; font: 400 12.5px/1.55 var(--font-mono); color: var(--text-body); }
.qg-rec + .qg-ddlwrap { padding: 10px 14px 14px; }
.qg-ddlh { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; }
.qg-ddln { font: 600 12.5px var(--font-mono); color: var(--text-strong); }
.qg-ddlcopy { display: inline-flex; align-items: center; gap: 5px; height: 26px; padding: 0 9px; margin-left: auto;
  border: 1px solid var(--border-default); border-radius: 6px; background: var(--surface-card); color: var(--text-muted);
  font: 500 12px var(--font-body); cursor: pointer; }
.qg-ddlcopy:hover { color: var(--accent-text); border-color: var(--accent-text); }
.qg-ddl { margin: 0; padding: 12px 14px; border: 1px solid var(--border-subtle); border-radius: 8px; background: var(--surface-sunken);
  font: 400 12.5px/1.6 var(--font-mono); color: var(--text-body); white-space: pre; overflow-x: auto; tab-size: 4; }
.qg-rec { border-top: 1px dashed var(--border-subtle); }
.qg-rec-h { color: var(--text-faint); white-space: nowrap; margin-bottom: 2px; }
.qg-kv { position: relative; display: flex; gap: 10px; padding: 1px 28px 1px 0; border-radius: 4px; }
.qg-kv:hover { background: color-mix(in oklch, var(--accent) 8%, var(--surface-card)); }
.qg-k { flex-shrink: 0; text-align: right; color: var(--text-muted); font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.qg-v { min-width: 0; white-space: pre-wrap; word-break: break-all; }
.qg-v.null { color: var(--text-faint); font-style: italic; font-size: 11px; }
.qg-kv:hover .qg-kv-copy { display: flex; }
.qg-empty { padding: 28px; text-align: center; color: var(--text-faint); font-family: var(--font-body); }
.qg-foot { display: flex; align-items: center; gap: 16px; min-height: 38px; padding: 0 8px 0 14px; flex-shrink: 0; flex-wrap: wrap;
  border-top: 1px solid var(--border-subtle); background: color-mix(in oklch, var(--surface-sunken) 40%, var(--surface-card)); }
.qg-status { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; min-width: 0; flex: 1; font: 500 12px var(--font-body); color: var(--text-muted); }
.qg-pager { display: flex; align-items: center; gap: 2px; font: 500 12px var(--font-body); color: var(--text-muted); }
.qg-size { height: 26px; margin-right: 6px; padding: 0 6px; border: 1px solid var(--border-default); border-radius: 6px;
  background: var(--surface-card); color: var(--text-body); font: inherit; }
.qg-pbtn { display: flex; align-items: center; justify-content: center; width: 26px; height: 26px; border: none; border-radius: 6px;
  background: transparent; color: var(--text-muted); cursor: pointer; }
.qg-pbtn:hover:not(:disabled) { background: var(--surface-sunken); color: var(--text-strong); }
.qg-pbtn:disabled { opacity: .35; cursor: default; }
.qg-pno { min-width: 64px; text-align: center; font-variant-numeric: tabular-nums; }
@media (max-width: 760px) {
  .qg-foot { padding: 6px 10px; gap: 6px; }
  .qg-status { flex: 1 1 100%; }
  .qg-pager { margin-left: auto; }
}
</style>
