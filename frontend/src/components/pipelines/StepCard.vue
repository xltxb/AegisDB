<script setup lang="ts">
// 一张步骤卡。
//
// 页面把每个阶段画成一根长条表单的时候,看不出"这是一条按顺序执行的流水线"。这里
// 把它做成时间轴上的一个节点:左侧一条虚线把上下串起来,卡片自带序号环、类型图标与
// 类型徽标,头部是"这一步是什么",主体才是"这一步怎么配"。
//
// 数据所有权在页面:这里只读 stage,所有改动通过 emit 交回去 —— dirty 标记、审批人
// 成员的按需加载都是页面的事,卡片不该各自维护一份。
import { ref, computed } from 'vue'
import {
  ShieldCheck, ClipboardCheck, DatabaseBackup, Terminal, Zap, Search, Hourglass, Bell,
  ArrowUp, ArrowDown, Copy, Trash2, ChevronDown, Webhook, ListChecks,
} from 'lucide-vue-next'
import VSelect from '@/components/common/VSelect.vue'
import type { PipelineStage, StageType } from '@/types'

const props = defineProps<{
  stage: PipelineStage
  index: number
  total: number
  readonly: boolean
  typeLabels: string[]
  roleLabels: string[]
  confirmRoleLabels: string[]
  /** 审批/确认节点当前选中的角色显示名。 */
  approverLabel: string
  /** 这个节点将来会找谁 —— 由页面解析好;empty 表示解析出来是空的(要告警)。 */
  who: { names: string[]; empty: boolean }
  /** 自动执行阶段:当前选的适用分层不允许自动执行时,给出的提示(保存会被后端拒)。 */
  autoWarn?: string
}>()

const emit = defineEmits<{
  move: [dir: -1 | 1]
  remove: []
  duplicate: []
  changeType: [label: string]
  failure: [value: 'abort' | 'continue']
  cfg: [key: string, value: string]
  approver: [label: string]
}>()

const icon: Record<StageType, any> = {
  review: ShieldCheck, approve: ClipboardCheck, backup: DatabaseBackup,
  execute: Terminal, auto_execute: Zap, verify: Search, manual: Hourglass, notify: Bell,
}
// 徽标显示的是失败策略,不是类型:名字本身就是类型,再来一个同词的徽标不带任何信息。
// onFailure 是后端真读的字段(见 service/pipeline.go),而表单从没露出过它。
const stopsOnFail = computed(() => (props.stage.onFailure || 'abort') !== 'continue')

const collapsed = ref(false)

// 只读地取一个配置项;写入走 emit('cfg')。
function cfg(key: string, def = ''): string {
  try { return (JSON.parse(props.stage.config || '{}')[key] ?? def) as string } catch { return def }
}
const isFirst = computed(() => props.index === 0)
const isLast = computed(() => props.index === props.total - 1)
</script>

<template>
  <div class="step" :class="{ collapsed }">
    <!-- 时间轴:序号环 + 连到下一张卡的虚线 -->
    <div class="rail">
      <div class="ring">{{ index + 1 }}</div>
      <div v-if="!isLast" class="line" />
    </div>

    <div class="card">
      <div class="head">
        <button type="button" class="fold" :title="collapsed ? $t('plExpand') : $t('plCollapse')" @click="collapsed = !collapsed">
          <ChevronDown :size="14" :class="{ turned: collapsed }" />
        </button>
        <span class="ic"><component :is="icon[stage.type]" :size="14" /></span>
        <!-- 名字 = 类型。它会进记录(快照 / 通知 / 审计),存的那份由服务端按类型给出
             规范值,这里只按读者的语言显示;换类型走右侧的下拉,不在名字上打字。 -->
        <span class="name">{{ $t('plType_' + stage.type) }}</span>
        <button type="button" class="badge" :class="stopsOnFail ? 'stop' : 'go'" :disabled="readonly"
                :title="$t('plOnFailTip')" @click="emit('failure', stopsOnFail ? 'continue' : 'abort')">
          {{ stopsOnFail ? $t('plOnFailAbort') : $t('plOnFailContinue') }}
        </button>
        <span class="grow" />
        <VSelect
          v-if="!readonly"
          :model-value="$t('plType_' + stage.type)" :options="typeLabels" height="28px"
          @update:model-value="(v: string) => emit('changeType', v)"
        />
        <div v-if="!readonly" class="acts">
          <button type="button" class="ab" :disabled="isFirst" :title="$t('plMoveUp')" @click="emit('move', -1)"><ArrowUp :size="13" /></button>
          <button type="button" class="ab" :disabled="isLast" :title="$t('plMoveDown')" @click="emit('move', 1)"><ArrowDown :size="13" /></button>
          <button type="button" class="ab" :title="$t('plDuplicate')" @click="emit('duplicate')"><Copy :size="13" /></button>
          <button type="button" class="ab danger" :title="$t('delete')" @click="emit('remove')"><Trash2 :size="13" /></button>
        </div>
      </div>

      <div v-show="!collapsed" class="body">
        <!-- 规范审查:多严算不过 -->
        <div v-if="stage.type === 'review'" class="row">
          <span class="lbl">{{ $t('plFailOn') }}</span>
          <div class="seg">
            <button v-for="m in ['error', 'warn', 'none']" :key="m" type="button" class="si"
                    :class="{ active: cfg('failOn', 'error') === m }" :disabled="readonly"
                    @click="emit('cfg', 'failOn', m)">{{ $t('plFailOn_' + m) }}</button>
          </div>
        </div>

        <!-- 备份 / 校验:一段 SQL,等宽代码框 -->
        <template v-else-if="stage.type === 'backup' || stage.type === 'verify'">
          <div class="row top">
            <span class="lbl">SQL</span>
            <textarea
              class="code" rows="3" spellcheck="false" :disabled="readonly"
              :value="cfg('sql')" :placeholder="stage.type === 'backup' ? $t('plBackupPh') : $t('plVerifyPh')"
              @input="emit('cfg', 'sql', ($event.target as HTMLTextAreaElement).value)"
            />
          </div>
          <!-- 校验阶段一直读着 expect,表单却从没露出过它 —— 不知道这个键的人只能让校验
               永远"通过"。 -->
          <div v-if="stage.type === 'verify'" class="row">
            <span class="lbl">{{ $t('plExpect') }}</span>
            <div class="seg">
              <button v-for="m in ['', 'empty', 'nonempty']" :key="m" type="button" class="si"
                      :class="{ active: cfg('expect', '') === m }" :disabled="readonly"
                      @click="emit('cfg', 'expect', m)">{{ $t('plExpect_' + (m || 'any')) }}</button>
            </div>
          </div>
        </template>

        <!-- 人工审批 -->
        <template v-else-if="stage.type === 'approve'">
          <div class="row">
            <span class="lbl">{{ $t('plApprover') }}</span>
            <VSelect :model-value="approverLabel" :options="roleLabels" height="32px"
                     @update:model-value="(v: string) => !readonly && emit('approver', v)" />
          </div>
          <div class="who" :class="{ warn: who.empty }">
            <template v-if="who.empty">{{ $t('plApproverEmpty') }}</template>
            <template v-else>
              <span class="wl">{{ $t('plApproverWho', { n: who.names.length }) }}</span>
              <span v-for="nm in who.names.slice(0, 8)" :key="nm" class="pill">{{ nm }}</span>
              <span v-if="who.names.length > 8" class="more">+{{ who.names.length - 8 }}</span>
            </template>
          </div>
        </template>

        <!-- 人工确认 / 执行变更:都有一道"谁来点"的门 -->
        <template v-else-if="stage.type === 'manual' || stage.type === 'execute'">
          <div v-if="stage.type === 'manual'" class="row">
            <span class="lbl">{{ $t('plNote') }}</span>
            <input class="in" :disabled="readonly" :value="cfg('note')" :placeholder="$t('plNotePh')"
                   @input="emit('cfg', 'note', ($event.target as HTMLInputElement).value)" />
          </div>
          <div class="row">
            <span class="lbl">{{ $t('plConfirmer') }}</span>
            <VSelect :model-value="approverLabel" :options="confirmRoleLabels" height="32px"
                     @update:model-value="(v: string) => !readonly && emit('approver', v)" />
          </div>
          <div class="who" :class="{ warn: who.empty }">
            <template v-if="who.empty">{{ $t('plApproverEmpty') }}</template>
            <template v-else-if="!who.names.length">{{ $t('plConfirmDefaultHint') }}</template>
            <template v-else>
              <span class="wl">{{ $t('plConfirmWho', { n: who.names.length }) }}</span>
              <span v-for="nm in who.names.slice(0, 8)" :key="nm" class="pill">{{ nm }}</span>
              <span v-if="who.names.length > 8" class="more">+{{ who.names.length - 8 }}</span>
            </template>
          </div>
        </template>

        <!-- 自动执行:没有"谁来点"—— 前序阶段通过就下发。分层不允许时直接在卡片上说 -->
        <div v-else-if="stage.type === 'auto_execute' && autoWarn" class="who warn" data-testid="auto-exec-warn">{{ autoWarn }}</div>

        <!-- 结果通知:渠道由全局设置决定,这里只如实展示会走哪几条 -->
        <div v-else-if="stage.type === 'notify'" class="row">
          <span class="lbl">{{ $t('plChannels') }}</span>
          <span class="pill ch"><Webhook :size="11" />Webhook</span>
          <span class="pill ch"><ListChecks :size="11" />{{ $t('plChannelAudit') }}</span>
        </div>

        <div class="hint">{{ $t('plHint_' + stage.type) }}</div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.step { display: flex; gap: 12px; }
.rail { display: flex; flex-direction: column; align-items: center; width: 24px; flex-shrink: 0; }
.ring {
  width: 24px; height: 24px; border-radius: 50%; flex-shrink: 0; margin-top: 9px;
  display: flex; align-items: center; justify-content: center;
  border: 2px solid var(--accent); background: var(--surface-card); color: var(--accent-text);
  font: 700 11px var(--font-mono);
}
.line { flex: 1; width: 0; margin-top: 4px; border-left: 2px dashed var(--border-default); min-height: 18px; }

.card {
  flex: 1; min-width: 0; margin-bottom: 10px;
  border: 1px solid var(--border-subtle); border-radius: var(--radius-lg);
  background: var(--surface-card); box-shadow: var(--shadow-xs);
  transition: all 0.15s ease;
}
.card:hover { border-color: var(--border-default); box-shadow: var(--shadow-sm); }

.head { display: flex; align-items: center; gap: 8px; padding: 9px 10px 9px 8px; }
.collapsed .head { padding-bottom: 9px; }
.fold {
  width: 24px; height: 24px; border: none; border-radius: 6px; background: transparent;
  color: var(--text-faint); display: flex; align-items: center; justify-content: center; cursor: pointer;
  transition: all 0.15s ease;
}
.fold:hover { background: var(--surface-sunken); color: var(--text-body); }
.fold svg { transition: transform 0.15s ease; }
.fold svg.turned { transform: rotate(-90deg); }
.ic { width: 26px; height: 26px; border-radius: 7px; background: var(--accent-subtle); color: var(--accent-text); display: flex; align-items: center; justify-content: center; flex-shrink: 0; }
.name { font: 500 14px var(--font-body); color: var(--text-strong); white-space: nowrap; }
.badge { padding: 2px 8px; border: 1px solid transparent; border-radius: var(--radius-full); font: 600 11px var(--font-body); white-space: nowrap; cursor: pointer; transition: all 0.15s ease; }
.badge:disabled { cursor: default; }
.badge.stop { background: var(--warning-subtle); color: var(--warning-text); }
.badge.go { background: var(--surface-sunken); color: var(--text-muted); }
.badge:hover:not(:disabled) { border-color: currentColor; }
.grow { flex: 1; }
.head :deep(.vsel) { min-width: 132px; }
.acts { display: flex; gap: 2px; margin-left: 2px; }
.ab {
  width: 26px; height: 26px; border: 1px solid transparent; border-radius: 7px; background: transparent;
  color: var(--text-muted); display: flex; align-items: center; justify-content: center; cursor: pointer;
  transition: all 0.15s ease;
}
.ab:hover:not(:disabled) { background: var(--surface-sunken); color: var(--text-strong); border-color: var(--border-subtle); }
.ab:disabled { opacity: 0.35; cursor: default; }
.ab.danger:hover:not(:disabled) { background: var(--danger-subtle); color: var(--danger-text); }

.body { padding: 2px 12px 12px 42px; display: flex; flex-direction: column; gap: 9px; }
.row { display: flex; align-items: center; gap: 10px; }
.row.top { align-items: flex-start; }
.lbl { font: 500 12px var(--font-body); color: var(--text-muted); min-width: 64px; padding-top: 1px; }
.row :deep(.vsel) { min-width: 180px; }
.in {
  flex: 1; min-width: 0; height: 32px; box-sizing: border-box; padding: 0 10px;
  border: 1px solid var(--border-subtle); border-radius: 8px; background: var(--surface-card);
  font: 400 13px var(--font-body); color: var(--text-body); outline: none; transition: all 0.15s ease;
}
.in:focus, .code:focus { border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-subtle); }
.in:disabled, .code:disabled { opacity: 0.6; }
.code {
  flex: 1; min-width: 0; box-sizing: border-box; padding: 8px 10px; resize: vertical; min-height: 68px;
  border: 1px solid var(--border-subtle); border-radius: 8px; background: var(--surface-sunken);
  font: 400 12.5px/1.55 var(--font-mono); color: var(--text-body); outline: none; transition: all 0.15s ease;
}
.seg { display: inline-flex; border: 1px solid var(--border-subtle); border-radius: 8px; overflow: hidden; background: var(--surface-card); }
.si {
  display: flex; align-items: center; height: 30px; padding: 0 12px; border: none; background: transparent;
  font: 500 12px var(--font-body); color: var(--text-muted); border-left: 1px solid var(--border-subtle); cursor: pointer;
  transition: all 0.15s ease;
}
.si:first-child { border-left: none; }
.si:hover:not(:disabled) { color: var(--text-strong); background: var(--surface-sunken); }
.si.active { background: var(--accent-subtle); color: var(--accent-text); }
.si:disabled { cursor: default; }
.who { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; font: 400 12px var(--font-body); color: var(--text-muted); }
.who.warn { color: var(--warning-text); }
.wl { margin-right: 2px; }
.pill { display: inline-flex; align-items: center; gap: 4px; padding: 2px 8px; border-radius: var(--radius-full); background: var(--surface-sunken); border: 1px solid var(--border-subtle); font: 500 11.5px var(--font-body); color: var(--text-body); }
.pill.ch { background: var(--accent-subtle); border-color: transparent; color: var(--accent-text); }
.more { font: 500 11.5px var(--font-mono); color: var(--text-faint); }
.hint { padding: 8px 10px; border-radius: 8px; background: var(--surface-sunken); font: 400 12px/1.5 var(--font-body); color: var(--text-muted); }
</style>
