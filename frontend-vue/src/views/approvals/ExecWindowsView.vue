<script setup lang="ts">
// 执行窗口(「班车」)—— 申请 → 审批 → 生效。
//
// 它从「分层」页里搬出来,有了自己的菜单键。原因不是那一页太挤,而是这两件事的
// 性质变了:分层是**规则**(这个环境按什么规矩办),而窗口是一次**申请**(请让我
// 在这段时间里,对这个库,不用逐条等人批)。规则由管理员改,申请由要执行的人提 ——
// 读者和权限都不是同一批。
//
// 提交之后窗口停在待审批,判定层一行都不放行(闸门在后端 ExecWindowsFor 的 status
// 条件上,不在这一页)。通过之后它才按自己的时间表开合。
import { ref, computed, watch, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Clock, Plus, Pencil, Trash2, X, ShieldCheck, Hourglass, Ban, Undo2, CalendarX } from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSelect from '@/components/common/VSelect.vue'
import VSwitch from '@/components/common/VSwitch.vue'
import VDateTime from '@/components/common/VDateTime.vue'
import api from '@/api'
import { useUIStore } from '@/stores/ui'
import { useAuthStore } from '@/stores/auth'
import type { Connection, ExecWindow } from '@/types'

const { t } = useI18n()
const ui = useUIStore()
const auth = useAuthStore()
const isAdmin = computed(() => auth.isAdmin)

const conns = ref<Connection[]>([])
const windows = ref<ExecWindow[]>([])
const busy = ref(false)
const winForm = ref(false)

// 星期缩写:1=周一 … 7=周日,与 ISO-8601 一致,也是 daysToIso 用的那套编号。
const dayLabels = computed(() => [1, 2, 3, 4, 5, 6, 7].map((n) => t(`dow${n}` as any)))
const kindOptions = computed(() => [t('ewKindRecurring'), t('ewKindOnce')])
const connOptions = computed(() => conns.value.map((c) => `${c.env}-${c.name}`))
const connLabel = (id: number) => {
  const c = conns.value.find((x) => x.id === id)
  return c ? `${c.env}-${c.name}` : `#${id}`
}

const blankWindow = () => ({
  id: 0, name: '', reason: '', database: '', enabled: true,
  connLabel: connOptions.value[0] || '', kindLabel: t('ewKindRecurring'),
  startsAt: '', endsAt: '', startHM: '02:00', endHM: '04:00',
  timezone: 'Asia/Shanghai', notAfter: '', days: [true, true, true, true, true, true, true],
})
const wf = ref(blankWindow())

// ---- 目标库下拉 ----
//
// 库名过去是手打的,而打错一个字的后果不是报错,是**窗口永远不开** —— 判定按
// (实例, 库) 精确匹配,拼写不上就是查无此行,而查无此行等于没有窗口。人却以为
// 配好了,直到那天夜里发现每条语句还在等审批。
//
// 所以改成从实例真实的库列表里选。探查失败时不静默留空:那时给回手工输入,并把
// 失败原因说出来 —— 连不上目标库是一个要人去处理的事实,不是一个空下拉框。
const dbOptions = ref<string[]>([])
const dbLoading = ref(false)
const dbErr = ref('')
/** 探查完成过一次(用来区分"还没查"和"查完了但一个库都没有")。 */
const dbProbed = ref(false)
const selectedConn = computed(() => conns.value.find((c) => `${c.env}-${c.name}` === wf.value.connLabel) || null)

async function loadDbOptions(keep = '') {
  const c = selectedConn.value
  dbOptions.value = []
  dbErr.value = ''
  dbProbed.value = false
  if (!c) return
  dbLoading.value = true
  try {
    const r = await api.connectionSchema(c.id)
    if (r.error) dbErr.value = r.error
    dbOptions.value = (r.databases || []).map((d) => d.name)
  } catch (e: any) {
    dbErr.value = e?.message || String(e)
  } finally {
    dbLoading.value = false
    dbProbed.value = true
  }
  // 编辑一张老窗口时,它的库可能已经不在列表里(改过名、或者现在连不上)。
  // 把它补回选项里,而不是悄悄换成第一个 —— 那会把这次编辑变成一次改库。
  if (keep && !dbOptions.value.includes(keep)) dbOptions.value = [keep, ...dbOptions.value]
  if (!dbOptions.value.includes(wf.value.database)) wf.value.database = dbOptions.value[0] || ''
}
// 换实例就换库列表:上一台实例的库名在这一台上多半不存在。
watch(() => wf.value.connLabel, () => { if (winForm.value) loadDbOptions() })

/**
 * 目标库对不上这台实例时的警告。
 *
 * 这是这个功能最容易悄悄配错的地方,而且**错了不会报错** —— 判定按 (实例, 库)
 * 精确匹配,对不上就是查无此行,而查无此行等于没有窗口。人以为配好了,直到那天
 * 夜里发现每条语句还在等审批。
 *
 * 真实发生过一次:窗口建在 hk-orders/db_orders 上,而 db_orders 其实是 hk-billing
 * 的库;hk-orders 探查不出任何库,于是那个下拉退回成了输入框,一句提示也没有。
 *
 * 两种情况分开说,因为要人做的事不同:
 *   列不出库  —— 仿真实例或没配凭据。库名只能手填,提醒他确认这个库属于这台实例。
 *   列得出但没有这一个 —— 几乎可以肯定是选错了实例,或者库名写错了。
 */
const dbWarn = computed(() => {
  if (!winForm.value || dbLoading.value || !dbProbed.value || dbErr.value) return ''
  const db = wf.value.database.trim()
  if (!dbOptions.value.length) return t('ewDbNoList')
  if (db && !dbOptions.value.includes(db)) return t('ewDbNotFound', { db })
  return ''
})

const hmToMin = (hm: string) => {
  const [h, m] = (hm || '0:0').split(':').map((x) => Number(x) || 0)
  return h * 60 + m
}
const minToHM = (n: number) => `${String(Math.floor(n / 60) % 24).padStart(2, '0')}:${String(n % 60).padStart(2, '0')}`

// whenLabel 只是显示。是否"进行中"由后端算(w.active),前端不重算 —— 跨午夜和时区
// 写两遍迟早分叉,而分叉的表现是界面与网关各说各话。
function whenLabel(w: ExecWindow): string {
  if (w.kind === 'once') {
    const f = (s?: string) => (s ? new Date(s).toLocaleString('sv').slice(0, 16) : '—')
    return `${f(w.startsAt)} → ${f(w.endsAt)}`
  }
  const days = w.weekdays.trim()
    ? w.weekdays.split(',').map((n) => dayLabels.value[Number(n) - 1] || n).join('')
    : t('ewEveryDay')
  const cross = w.endMin <= w.startMin ? ' (+1d)' : ''
  return `${days} ${minToHM(w.startMin)}-${minToHM(w.endMin)}${cross} ${w.timezone}`
}

// 班车的发车日。空串 = 每天(后端的约定,见 validWeekdays),1..7 对应周一到周日。
function daysOf(w: ExecWindow): boolean[] {
  const spec = (w.weekdays || '').trim()
  if (!spec) return [true, true, true, true, true, true, true]
  const set = new Set(spec.split(',').map((x) => Number(x.trim())))
  return [1, 2, 3, 4, 5, 6, 7].map((n) => set.has(n))
}

/**
 * 一行的状态。五种,顺序就是它们的优先级:
 *
 *   待审批 / 已驳回 —— 还没签字,或者签的是"不行"。这两种下面**没有**开不开的问题,
 *                     所以要先说,不能让一张待审批的窗口显示成"未到时间"。
 *   已停用          —— 签过字,但运维把它关了。
 *   进行中          —— 签过字、也启用了,而且此刻在时间表内。
 *   已到期 / 未到点  —— 都不在时间表内,但这两句话方向相反:一个是"再也不会开了",
 *                     另一个是"再等等就到了"。原先只有后者,于是一个上周就结束的
 *                     一次性窗口会永远显示成"未到点"。
 *
 * 到期与否由后端算(w.expired),和 active 同一个理由:时区与跨午夜写两遍会分叉。
 */
function stateOf(w: ExecWindow) {
  if (w.status === 'pending') return { cls: 'pending', text: 'ewStPending', icon: Hourglass }
  if (w.status === 'rejected') return { cls: 'rejected', text: 'ewStRejected', icon: Ban }
  // 撤回 ≠ 驳回:没有人看过并拒绝它,是申请人自己收回的。对判定层两者一样(都不是
  // approved),对读这一行的人不一样,所以用中性色而不是红色。
  if (w.status === 'cancelled') return { cls: 'off', text: 'ewStCancelled', icon: Undo2 }
  if (!w.enabled) return { cls: 'off', text: 'ewDisabled', icon: Ban }
  if (w.active) return { cls: 'open', text: 'ewOpen', icon: ShieldCheck }
  // 已到期用中性灰,不用红:它不是出了问题,只是这班车开完了。
  if (w.expired) return { cls: 'off', text: 'ewExpired', icon: CalendarX }
  return { cls: 'closed', text: 'ewClosed', icon: Clock }
}
/** 只有申请人自己或管理员能改/撤(后端也同样判,这里只是不摆一个必被拒的按钮)。 */
const canManage = (w: ExecWindow) => isAdmin.value || w.createdBy === (auth.me?.id ?? -1)

async function loadWindows() {
  try { windows.value = await api.execWindows() } catch { windows.value = [] }
}

function openWindowForm(w?: ExecWindow) {
  wf.value = blankWindow()
  if (w) {
    const local = (s?: string) => (s ? new Date(s).toLocaleString('sv').slice(0, 16).replace(' ', 'T') : '')
    const days = w.weekdays.trim()
      ? dayLabels.value.map((_, i) => w.weekdays.split(',').includes(String(i + 1)))
      : [true, true, true, true, true, true, true]
    wf.value = {
      id: w.id, name: w.name, reason: w.reason, database: w.database, enabled: w.enabled,
      connLabel: connLabel(w.connectionId),
      kindLabel: w.kind === 'once' ? t('ewKindOnce') : t('ewKindRecurring'),
      startsAt: local(w.startsAt), endsAt: local(w.endsAt),
      startHM: minToHM(w.startMin), endHM: minToHM(w.endMin),
      timezone: w.timezone || 'Asia/Shanghai', notAfter: local(w.notAfter), days,
    }
  }
  winForm.value = true
  loadDbOptions(w?.database || '')
}

async function saveWindow() {
  const f = wf.value
  const conn = selectedConn.value
  if (!conn) { ui.notifyError(new Error(t('ewPickInstance')), t('actionFailed')); return }
  const once = f.kindLabel === kindOptions.value[1]
  const iso = (v: string) => (v ? new Date(v).toISOString() : undefined)
  const body: Record<string, unknown> = {
    name: f.name.trim(), enabled: f.enabled, connectionId: conn.id,
    database: f.database.trim(), reason: f.reason.trim(),
    kind: once ? 'once' : 'recurring',
  }
  if (once) {
    body.startsAt = iso(f.startsAt)
    body.endsAt = iso(f.endsAt)
  } else {
    body.timezone = f.timezone.trim()
    body.startMin = hmToMin(f.startHM)
    body.endMin = hmToMin(f.endHM)
    // 全选等于"每天",送空串 —— 与后端 weekdayAllowed 的约定一致。
    body.weekdays = f.days.every(Boolean) ? '' : f.days.map((on, i) => (on ? i + 1 : 0)).filter(Boolean).join(',')
    body.notAfter = iso(f.notAfter)
  }
  busy.value = true
  try {
    const env = f.id ? await api.updateExecWindow(f.id, body) : await api.createExecWindow(body)
    if (env.code !== 0) { ui.notifyError(new Error(env.msg), t('actionFailed')); return }
    winForm.value = false
    await loadWindows()
    // 说清"提交了,还没生效" —— 一句"已保存"会让人以为今晚可以直接跑了。
    ui.notify(t(f.id ? 'ewResubmitted' : 'ewSubmitted', { ap: env.data?.apNo || '' }), 'success', 5000)
  } catch (e) { ui.notifyError(e, t('actionFailed')) } finally { busy.value = false }
}

async function removeWindow(w: ExecWindow) {
  if (!window.confirm(t('ewDelConfirm', { name: w.name }))) return
  busy.value = true
  try {
    const env = await api.deleteExecWindow(w.id)
    if (env.code !== 0) { ui.notifyError(new Error(env.msg), t('actionFailed')); return }
    await loadWindows()
  } catch (e) { ui.notifyError(e, t('actionFailed')) } finally { busy.value = false }
}

onMounted(async () => {
  try { conns.value = await api.connections() } catch { /* 实例下拉降级为空 */ }
  await loadWindows()
  ui.pageSub = { key: 'ewPageSub', params: { n: windows.value.length } }
})
</script>

<template>
  <div class="scy page">
    <div class="head">
      <div class="hleft">
        <div class="eyebrow">EXECUTION WINDOWS</div>
        <div class="sub">{{ $t('ewSub') }}</div>
      </div>
      <VButton variant="primary" @click="openWindowForm()"><Plus :size="15" />{{ $t('ewApply') }}</VButton>
    </div>

    <!-- 这一页最容易被误解的一句话:提交不等于生效。放在表格上面,不放在提交后的
         toast 里 —— toast 会消失,而这条约束一直成立。 -->
    <div class="banner">
      <ShieldCheck :size="16" />
      <span>{{ $t('ewApprovalNote') }}</span>
    </div>

    <div class="card">
      <div class="scx">
        <div class="wgrid">
          <div class="th">
            <span>{{ $t('ewColName') }}</span><span>{{ $t('ewColScope') }}</span>
            <span>{{ $t('ewColWhen') }}</span><span>{{ $t('ewColDays') }}</span>
            <span>{{ $t('ewColState') }}</span><span class="ctr">{{ $t('apActions') }}</span>
          </div>
          <div v-if="!windows.length" class="tr empty">{{ $t('ewEmpty') }}</div>
          <div v-for="w in windows" :key="w.id" class="tr">
            <div>
              <div class="cn">{{ w.name }}</div>
              <div class="cl2">{{ w.reason || $t('ewNoReason') }}</div>
              <!-- 单号是这扇门"凭什么开着"的凭据,列在名字下面,点得进审批页去看链路 -->
              <router-link v-if="w.apNo" class="apno" :to="`/approvals?q=${w.apNo}`">{{ w.apNo }}</router-link>
            </div>
            <div class="mono mute">{{ connLabel(w.connectionId) }} / {{ w.database }}</div>
            <div class="mono mute">{{ whenLabel(w) }}</div>
            <!-- 发车日用圆点徽章:七个字比一串 "1,3,5" 好认。一次性窗口没有"每周
                 哪几天"这回事,留空。 -->
            <div class="daycell">
              <template v-if="w.kind === 'recurring'">
                <span v-for="(on, i) in daysOf(w)" :key="i" class="daydot" :class="{ on }">{{ dayLabels[i] }}</span>
              </template>
              <span v-else class="dash">—</span>
            </div>
            <div>
              <span class="wst" :class="stateOf(w).cls">
                <component :is="stateOf(w).icon" :size="12" />{{ $t(stateOf(w).text as any) }}
              </span>
            </div>
            <div class="acts">
              <button v-if="canManage(w)" class="iconbtn" :disabled="busy" :title="$t('ewEdit')" @click="openWindowForm(w)"><Pencil :size="13" /></button>
              <button v-if="canManage(w)" class="iconbtn danger" :disabled="busy" :title="$t('etDelete')" @click="removeWindow(w)"><Trash2 :size="13" /></button>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- 居中弹窗,不再是右侧抽屉。抽屉宽 560px、贴着屏幕右缘,而这张表单里有日期
         选择器 —— 面板从贴边的字段上弹出来,一半会落到窗口外面去。 -->
    <Teleport to="body">
      <div v-if="winForm" class="ovl" @click.self="winForm = false">
      <div class="modal">
        <div class="dhead">
          <div class="dic"><Clock :size="16" color="var(--accent-text)" /></div>
          <div class="grow">
            <div class="dt">{{ wf.id ? $t('ewEdit') : $t('ewApply') }}</div>
            <div class="ds">{{ wf.id ? $t('ewEditNote') : $t('ewApprovalNote') }}</div>
          </div>
          <span class="x" @click="winForm = false"><X :size="17" /></span>
        </div>

        <div class="dbody scy">
          <div class="frow">
            <div><div class="fl">{{ $t('ewColName') }}</div><input v-model="wf.name" :placeholder="$t('ewNamePh')" /></div>
            <div><div class="fl">{{ $t('ewFReason') }}</div><input v-model="wf.reason" :placeholder="$t('ewReasonPh')" /></div>
          </div>
          <div class="frow">
            <div><div class="fl">{{ $t('ewFInstance') }}</div><VSelect v-model="wf.connLabel" :options="connOptions" /></div>
            <div>
              <div class="fl">{{ $t('ewFDatabase') }}</div>
              <!-- 库名从实例真实的库列表里选:打错一个字的后果不是报错,是窗口
                   永远不开(判定按 (实例, 库) 精确匹配)。 -->
              <VSelect v-if="dbOptions.length" v-model="wf.database" :options="dbOptions" />
              <input v-else v-model="wf.database" :placeholder="dbLoading ? $t('schemaLoading') : $t('ewDbPh')" />
              <div v-if="dbErr" class="dberr">{{ dbErr }}</div>
              <!-- 对不上不会报错,只会让窗口永远不开 —— 所以要在申请时就说出来。 -->
              <div v-else-if="dbWarn" class="dbwarn">{{ dbWarn }}</div>
            </div>
          </div>
          <div class="frow">
            <div><div class="fl">{{ $t('ewFKind') }}</div><VSelect v-model="wf.kindLabel" :options="kindOptions" /></div>
            <div><div class="fl">{{ $t('ewFEnabled') }}</div><label class="chk"><VSwitch v-model="wf.enabled" />{{ $t('ewEnabledHint') }}</label></div>
          </div>

          <!-- 一次性 -->
          <template v-if="wf.kindLabel === kindOptions[1]">
            <div class="frow">
              <div><div class="fl">{{ $t('ewFFrom') }}</div><VDateTime v-model="wf.startsAt" /></div>
              <div><div class="fl">{{ $t('ewFTo') }}</div><VDateTime v-model="wf.endsAt" /></div>
            </div>
            <div class="note">{{ $t('ewOnceNote') }}</div>
          </template>

          <!-- 周期班车 -->
          <template v-else>
            <div class="frow">
              <div><div class="fl">{{ $t('ewFStart') }}</div><input v-model="wf.startHM" type="time" /></div>
              <div><div class="fl">{{ $t('ewFEnd') }}</div><input v-model="wf.endHM" type="time" /></div>
            </div>
            <div class="frow">
              <div><div class="fl">{{ $t('ewFTz') }}</div><input v-model="wf.timezone" placeholder="Asia/Shanghai" /></div>
              <div><div class="fl">{{ $t('ewFNotAfter') }}</div><VDateTime v-model="wf.notAfter" /></div>
            </div>
            <div class="fl">{{ $t('ewFDays') }}</div>
            <div class="days">
              <label v-for="(d, i) in dayLabels" :key="i" class="day" :class="{ on: wf.days[i] }">
                <input v-model="wf.days[i]" type="checkbox" />{{ d }}
              </label>
            </div>
            <div class="note">{{ $t('ewRecurNote') }}</div>
          </template>
        </div>

        <div class="dfoot">
          <VButton variant="secondary" height="36px" @click="winForm = false">{{ $t('btnCancel') }}</VButton>
          <VButton variant="primary" height="36px" :disabled="busy || !wf.name || !wf.database" @click="saveWindow">
            {{ $t('ewSubmit') }}
          </VButton>
        </div>
      </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.page { flex: 1; min-height: 0; padding: var(--page-pad); max-width: var(--page-max); margin-inline: auto; width: 100%; }
.head { display: flex; align-items: flex-start; gap: 16px; margin-bottom: 14px; }
.hleft { min-width: 0; }
.head :deep(.vbtn) { margin-left: auto; display: inline-flex; align-items: center; gap: 6px; }
.eyebrow { font: 600 11px var(--font-mono); letter-spacing: 0.14em; color: var(--text-faint); }
.sub { margin-top: 6px; font: 500 12.5px var(--font-body); color: var(--text-muted); }
/* 「提交不等于生效」放在页面上而不是 toast 里 —— toast 会消失,这条约束一直成立。 */
.banner {
  display: flex; align-items: center; gap: 9px; margin-bottom: 14px;
  padding: 10px 14px; border-radius: var(--radius-md);
  background: var(--accent-subtle); border: 1px solid var(--accent-subtle-border);
  color: var(--accent-text); font: 500 12px/1.6 var(--font-body);
}
.card { border: 1px solid var(--border-subtle); border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-sm); overflow: hidden; }
.scx { overflow-x: auto; }
.wgrid { min-width: 960px; }
.th, .tr { display: grid; grid-template-columns: 1.5fr 1.4fr 1.6fr 1.3fr 0.9fr 80px; gap: 12px; align-items: center; }
.th { padding: 10px 16px; background: var(--surface-sunken); border-bottom: 1px solid var(--border-subtle); font: 600 10.5px var(--font-mono); letter-spacing: 0.07em; text-transform: uppercase; color: var(--text-faint); }
.tr { padding: 12px 16px; border-bottom: 1px solid var(--border-subtle); }
.tr:last-child { border-bottom: none; }
.tr:hover { background: var(--surface-sunken); }
.tr.empty { display: block; padding: 32px 16px; text-align: center; font: 500 12px var(--font-body); color: var(--text-faint); }
.ctr { text-align: right; }
.cn { font: 700 12.5px var(--font-body); color: var(--text-strong); }
.cl2 { margin-top: 2px; font: 500 11px var(--font-body); color: var(--text-faint); }
.apno { display: inline-block; margin-top: 4px; font: 600 10.5px var(--font-mono); color: var(--accent-text); text-decoration: none; }
.apno:hover { text-decoration: underline; }
.mono { font: 500 11.5px var(--font-mono); color: var(--text-body); }
.mono.mute { color: var(--text-muted); }
.daycell { display: flex; align-items: center; gap: 3px; flex-wrap: wrap; }
.daydot { display: inline-flex; align-items: center; justify-content: center; width: 18px; height: 18px; border-radius: 50%; background: var(--surface-sunken); color: var(--text-faint); font: 600 9.5px var(--font-body); }
.daydot.on { background: var(--accent-subtle); color: var(--accent-text); }
.dash { color: var(--text-faint); }
/* 状态:四种,各自一枚淡底徽章。待审批与已驳回排在"开没开"之前 —— 还没签字的
   窗口显示"未到时间"会让人以为只是时候未到。 */
.wst { display: inline-flex; align-items: center; gap: 5px; height: 22px; padding: 0 9px; border-radius: var(--radius-full); font: 600 10.5px var(--font-mono); }
.wst.pending { background: var(--warning-subtle); color: var(--warning-text); }
.wst.rejected { background: var(--danger-subtle); color: var(--danger-text); }
.wst.off { background: var(--surface-sunken); color: var(--text-faint); }
.wst.open { background: var(--success-subtle); color: var(--success-text); }
.wst.closed { background: var(--surface-sunken); color: var(--text-muted); }
.acts { display: flex; justify-content: flex-end; gap: 6px; }
.iconbtn { width: 26px; height: 26px; display: inline-flex; align-items: center; justify-content: center; border: 1px solid var(--border-default); border-radius: var(--radius-sm); background: var(--surface-card); color: var(--text-muted); cursor: pointer; }
.iconbtn:hover:not(:disabled) { color: var(--accent-text); border-color: var(--accent-text); }
.iconbtn.danger:hover:not(:disabled) { color: var(--danger-text); border-color: var(--danger-text); }
.iconbtn:disabled { opacity: 0.5; cursor: default; }
/* 居中弹窗 */
.ovl { position: fixed; inset: 0; z-index: 80; display: flex; align-items: center; justify-content: center; padding: 24px; background: var(--surface-overlay); backdrop-filter: blur(2px); }
/* 宽一点,并且**屏幕放得下就不滚**:表单本身只有五六行,先前被压在 640px 里,
   两列一挤就长出一条本不必要的滚动条。max-height 只是矮屏上的兜底。 */
.modal {
  width: min(760px, 96vw); max-height: min(92vh, 860px); display: flex; flex-direction: column;
  background: var(--surface-card); border: 1px solid var(--border-default);
  border-radius: var(--radius-lg); box-shadow: var(--shadow-xl); overflow: hidden;
}
.dhead { display: flex; align-items: flex-start; gap: 11px; padding: 16px 20px; border-bottom: 1px solid var(--border-subtle); }
.dic { width: 32px; height: 32px; border-radius: var(--radius-md); background: var(--accent-subtle); display: grid; place-items: center; flex-shrink: 0; }
.grow { flex: 1; min-width: 0; }
.dt { font: 700 14px var(--font-display); color: var(--text-strong); }
.ds { margin-top: 3px; font: 500 11.5px/1.6 var(--font-body); color: var(--text-muted); }
.x { color: var(--text-faint); cursor: pointer; display: flex; }
.x:hover { color: var(--text-strong); }
/* 只在真的放不下时才滚。日期面板已经 Teleport 到 body,不会再被这里裁掉。 */
.dbody { flex: 1 1 auto; min-height: 0; overflow-y: auto; padding: 18px 22px; }
.dfoot { display: flex; justify-content: flex-end; gap: 10px; padding: 12px 20px; border-top: 1px solid var(--border-subtle); background: var(--surface-raised); }
.frow { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; margin-bottom: 14px; }
/* 窄屏(或把窗口拖窄)时两列并一列:两个 datetime 字段并排挤到 150px 时,里面的
   `2026-09-20 00:00` 会被截断,而这正是要核对的东西。 */
@media (max-width: 620px) {
  .frow { grid-template-columns: 1fr; gap: 12px; }
  .ovl { padding: 12px; }
}
.fl { margin-bottom: 6px; font: 500 11px var(--font-body); color: var(--text-faint); }
/* :not(checkbox) —— 星期是复选框,套上输入框的高度会变成七个 38px 的方块。 */
.frow input:not([type='checkbox']), .dbody > input {
  width: 100%; box-sizing: border-box; height: 36px; padding: 0 11px;
  border: 1px solid var(--border-default); border-radius: var(--radius-md);
  background: var(--surface-sunken); color: var(--text-body); font: 500 12.5px var(--font-mono); outline: none;
}
.frow input:focus { border-color: var(--accent-text); box-shadow: 0 0 0 3px var(--focus-ring); }
.dberr { margin-top: 6px; font: 500 11px/1.5 var(--font-body); color: var(--danger-text); }
.dbwarn { margin-top: 6px; padding: 6px 9px; border-radius: var(--radius-sm); background: var(--warning-subtle); font: 500 11px/1.6 var(--font-body); color: var(--warning-text); }
.chk { display: flex; align-items: center; gap: 9px; height: 36px; font: 500 11.5px var(--font-body); color: var(--text-muted); }
.days { display: flex; gap: 6px; flex-wrap: wrap; margin-bottom: 12px; }
.day { display: inline-flex; align-items: center; justify-content: center; width: 34px; height: 30px; border-radius: var(--radius-sm); border: 1px solid var(--border-default); background: var(--surface-card); color: var(--text-muted); font: 600 11.5px var(--font-body); cursor: pointer; user-select: none; }
.day.on { background: var(--accent-text); border-color: var(--accent-text); color: var(--text-on-accent); }
.day input { display: none; }
.note { font: 500 11px/1.7 var(--font-body); color: var(--text-faint); border-left: 2px solid var(--border-default); padding-left: 9px; }
</style>
