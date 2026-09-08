<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import {
  DatabaseZap, KeyRound, Download, Copy, Check, Eye, EyeOff, FolderCog, TriangleAlert,
  Loader, CircleCheck, CircleX, Clock, Trash2, RotateCw, ChevronDown, Inbox,
} from 'lucide-vue-next'
import VButton from '@/components/common/VButton.vue'
import VSelect from '@/components/common/VSelect.vue'
import api from '@/api'
import { CODE_OK, CODE_EXPORT_PATH_UNSET } from '@/api/http'
import type { Connection, ExportJob } from '@/types'

const router = useRouter()
const { t } = useI18n()
const conns = ref<Connection[]>([])
const connId = ref<number>(0)
const db = ref('')                       // target database within the instance ('' = connection default)
const dbOptions = ref<string[]>([])
const sql = ref('SELECT id, name, status, created_at FROM users')
const name = ref('')
const busy = ref(false)
const err = ref('')
const needPath = ref(false)
const jobs = ref<ExportJob[]>([])
const copiedId = ref(0)
// 首次拉取任务列表时为 true —— 空列表和"还没拉到"长得一样,而前者是一句结论、
// 后者只是还没问完。骨架屏存在的意义就是不让这两件事混在一起。
const loading = ref(true)
// 服务器上归档保留几天(0=永久)。来自 /export/config,不是前端猜的默认值 ——
// 提交页要说的保留期,必须和真正执行删除的那个设置是同一个数。
const retentionDays = ref(0)
let timer: ReturnType<typeof setInterval> | null = null

const activeConn = computed(() => conns.value.find((c) => c.id === connId.value) || null)
const anyActive = computed(() => jobs.value.some((j) => j.status === 'pending' || j.status === 'running'))

async function loadJobs() {
  try { jobs.value = await api.exportJobs() } catch { /* ignore */ }
}

// 手动刷新。轮询只在有任务真的在跑的时候才发请求(见下面的 timer),所以一张全是
// 历史记录的列表是不会自己动的 —— 那时候要看有没有新东西,得有个地方能点。
const refreshing = ref(false)
async function refresh() {
  refreshing.value = true
  try { await loadJobs() } finally { refreshing.value = false }
}

// Load the selectable databases for the chosen instance (same live introspection
// the terminal uses). Defaults to the connection's own database when it has one.
async function loadDbs(id: number) {
  db.value = ''
  dbOptions.value = []
  if (!id) return
  try {
    const sc = await api.connectionSchema(id)
    dbOptions.value = sc.databases.map((d) => d.name)
    const c = conns.value.find((x) => x.id === id)
    // Pre-select a real database so an export isn't submitted with no schema (which
    // fails on the target with "No database selected"): the connection's own
    // database if it has one, else the first introspected database.
    if (c?.database && dbOptions.value.includes(c.database)) db.value = c.database
    else if (dbOptions.value.length) db.value = dbOptions.value[0]
  } catch { /* leave on default */ }
}
// The instance picker is a searchable VSelect, which works on display labels, so
// the selection round-trips through `env-name` (the same label the terminal and
// the async-exec page use). A deployment can carry hundreds of instances; the
// label is what an operator actually types to find one.
const connLabel = (c: Connection) => `${c.env}-${c.name}`
const connLabels = computed(() => conns.value.map(connLabel))
const selectedLabel = computed({
  get: () => {
    const c = conns.value.find((x) => x.id === connId.value)
    return c ? connLabel(c) : ''
  },
  set: (l: string) => {
    const c = conns.value.find((x) => connLabel(x) === l)
    if (c) { connId.value = c.id; loadDbs(c.id) }
  },
})

onMounted(async () => {
  try {
    conns.value = await api.connections()
    const first = conns.value.find((c) => c.env === 'prod') ?? conns.value[0]
    if (first) { connId.value = first.id; await loadDbs(first.id) }
  } catch { /* ignore */ }
  try { retentionDays.value = (await api.exportConfig()).retentionDays ?? 0 } catch { /* 取不到就不提保留期,别编一个 */ }
  await loadJobs()
  loading.value = false
  timer = setInterval(() => { if (anyActive.value) loadJobs() }, 2000)
})
onUnmounted(() => { if (timer) clearInterval(timer) })

async function submit() {
  const q = sql.value.trim()
  if (!connId.value) { err.value = t('pickInstance'); return }
  if (!q) { err.value = t('enterExportSql'); return }
  busy.value = true
  err.value = ''
  needPath.value = false
  try {
    const env = await api.exportData(connId.value, q, name.value.trim(), db.value, includeSensitive.value)
    if (env.code === CODE_EXPORT_PATH_UNSET) { needPath.value = true; return }
    if (env.code === CODE_OK) {
      // 勾了原值的任务停在待审批，不会自己开始。不说这一句，人会一直等在下载那一栏。
      awaiting.value = includeSensitive.value
      await loadJobs()
      return
    }
    err.value = env.msg || t('submitFailed')
  } catch { err.value = t('submitFailed') }
  finally { busy.value = false }
}

/**
 * 把一条任务的参数填回左侧表单。
 *
 * 失败和过期的任务需要一个出口,而这里**没有**"重跑"这回事:一次导出跑的是提交
 * 时那条语句、那个库,重跑等于凭旧参数再建一个任务,而参数是不是还对(库还在吗、
 * 语句改过吗)只有人知道。所以填回表单、由人按下提交 —— 不新增接口,也不替人做
 * 那次决定。
 */
async function reuse(j: ExportJob) {
  const c = conns.value.find((x) => x.name === j.instance)
  if (c) {
    connId.value = c.id
    await loadDbs(c.id)
    if (j.database && dbOptions.value.includes(j.database)) db.value = j.database
  }
  sql.value = j.sql
  err.value = ''
  awaiting.value = false
  needPath.value = false
}

// Export passwords are masked by default and only shown on explicit reveal, so
// they don't sit in the DOM across the 2s job polling (L4).
const shownPw = ref<Set<number>>(new Set())
function togglePw(id: number) {
  const s = new Set(shownPw.value)
  s.has(id) ? s.delete(id) : s.add(id)
  shownPw.value = s
}

// 展开的错误。默认单行截断:一屏里三条失败任务各占五行报错,列表就没法看了;
// 但截断不能等于看不到 —— 报错原文是排查的起点,所以点一下能展开。
const openErr = ref<Set<number>>(new Set())
function toggleErr(id: number) {
  const s = new Set(openErr.value)
  s.has(id) ? s.delete(id) : s.add(id)
  openErr.value = s
}

async function copyPw(j: ExportJob) {
  try { await navigator.clipboard.writeText(j.password); copiedId.value = j.id; setTimeout(() => (copiedId.value = 0), 1600) } catch { /* ignore */ }
}
const partsOf = (j: ExportJob) => (j.files ? j.files.split('\n').map((f) => f.trim()).filter(Boolean) : [])

async function downloadOne(file: string) {
  const blob = await api.exportDownload(file)
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = file.split(/[\\/]/).pop() || 'export.zip'
  a.click()
  URL.revokeObjectURL(url)
}
// Download every part file of a finished job (sequentially to avoid popup blocks).
async function download(j: ExportJob) {
  try {
    for (const f of partsOf(j)) {
      await downloadOne(f)
      await new Promise((r) => setTimeout(r, 250))
    }
  } catch { err.value = t('downloadFailed') }
}
function goSettings() { router.push('/settings') }
const kb = (n: number) => (n < 1024 ? n + ' B' : n < 1048576 ? (n / 1024).toFixed(1) + ' KB' : (n / 1048576).toFixed(2) + ' MB')
const stMeta: Record<string, { t: string; icon: any; cls: string }> = {
  pending: { t: 'exportStPending', icon: Clock, cls: 'wait' },
  running: { t: 'exportStRunning', icon: Loader, cls: 'run' },
  done: { t: 'exportStDone', icon: CircleCheck, cls: 'ok' },
  failed: { t: 'exportStFailed', icon: CircleX, cls: 'bad' },
  // 归档已被保留策略清理:任务行还在,文件没了。
  expired: { t: 'exportStExpired', icon: Trash2, cls: 'gone' },
  // 含敏感字段的导出在批准之前停在这里，不进队列。借用"等待"的样式：它和排队中
  // 一样是"还没开始"，但等的是人不是机器。
  awaiting: { t: 'exportStAwaiting', icon: Clock, cls: 'wait' },
}
const meta = (s: string) => stMeta[s] || stMeta.pending

// 要不要敏感字段的原值。默认 false —— 打码是常态，放开才需要理由。
const includeSensitive = ref(false)
// 上一次提交是否停在了待审批（用于提交后的那句提示）。
const awaiting = ref(false)
</script>

<template>
  <div class="scy page">
    <div class="col">
      <!-- ------------------------------------------------ 左:新建导出 -->
      <!-- 表单是单列的:实例和目标库横着挤在一行时,两个都短得放不下完整的名字,
           而它们恰恰是最需要看清的两项。竖排之后每一项都拿到整个卡片的宽度。 -->
      <section class="card formcard">
        <div class="shead">
          <div class="sic"><DatabaseZap :size="18" color="var(--accent-text)" /></div>
          <div><div class="st">{{ $t('exportTitle') }}</div><div class="ss">{{ $t('exportSub') }}</div></div>
        </div>
        <div class="body">
          <div v-if="needPath" class="notice">
            <FolderCog :size="15" /><span>{{ $t('exportNoPath') }}</span>
            <VButton variant="secondary" height="30px" @click="goSettings">{{ $t('scPathGo') }}</VButton>
          </div>

          <div class="field">
            <div class="lbl">{{ $t('exportConn') }}</div>
            <VSelect v-model="selectedLabel" :options="connLabels" searchable height="38px" />
          </div>
          <div class="field">
            <div class="lbl">{{ $t('exportDb') }}</div>
            <select v-model="db" class="sel">
              <option value="">{{ $t('exportDbDefault') }}</option>
              <option v-for="d in dbOptions" :key="d" :value="d">{{ d }}</option>
            </select>
          </div>
          <div class="field">
            <div class="lbl">{{ $t('exportName') }}</div>
            <input v-model="name" class="nameinput" :placeholder="$t('exportNamePh')" />
          </div>
          <div v-if="activeConn?.env === 'prod'" class="prodwarn"><TriangleAlert :size="13" />{{ $t('opWarnPrefix') }} <b>PROD · {{ activeConn.name }}</b> · {{ $t('opWarnCaution') }}</div>

          <div class="field">
            <div class="lbl">{{ $t('exportSql') }}</div>
            <textarea
              v-model="sql" class="sqlarea" spellcheck="false"
              placeholder="SELECT id, name, created_at&#10;FROM users&#10;WHERE created_at >= '2026-01-01'"
            />
          </div>

          <label class="senschk">
            <input v-model="includeSensitive" type="checkbox" />
            <span class="senstxt">{{ $t('exportSensitive') }}</span>
          </label>
          <div v-if="includeSensitive" class="senshint"><TriangleAlert :size="13" />{{ $t('exportSensitiveHint') }}</div>
          <div v-if="awaiting" class="notice">{{ $t('exportAwaitingNotice') }}</div>
          <div v-if="err" class="err">{{ err }}</div>

          <VButton class="submit" variant="primary" :disabled="busy" @click="submit">
            {{ busy ? $t('exportRunning') : $t('exportSubmit') }}
          </VButton>
          <!-- 保留期写在提交按钮下面,而不是藏进帮助文档:导出成功后才知道"三天后
               文件没了",那三天就已经被当成永久的了。 -->
          <div class="subhint">
            <span>{{ $t('exportEncrypted') }}</span>
            <template v-if="retentionDays > 0"><span class="dot">·</span><span>{{ $t('exportRetentionNote', { d: retentionDays }) }}</span></template>
          </div>
        </div>
      </section>

      <!-- ------------------------------------------------ 右:导出任务 -->
      <section class="card">
        <div class="shead">
          <div class="sic"><Download :size="18" color="var(--accent-text)" /></div>
          <div class="grow"><div class="st">{{ $t('exportTasks') }}</div><div class="ss">{{ $t('exportTasksSub') }}</div></div>
          <!-- 轮询只在有任务真的在跑时才发请求,所以这里说的是"此刻会不会自己动",
               而不是一个长亮的转圈。 -->
          <span v-if="anyActive" class="poll"><Loader :size="13" class="spin" />{{ $t('exportPolling') }}</span>
          <button class="refresh" :disabled="refreshing" :title="$t('btnRefresh')" @click="refresh">
            <RotateCw :size="14" :class="{ spin: refreshing }" />{{ $t('btnRefresh') }}
          </button>
        </div>

        <div class="jhead">
          <span>{{ $t('exportColStatus') }}</span>
          <span>{{ $t('exportColJob') }}</span>
          <span class="right">{{ $t('exportColTime') }}</span>
          <span class="right">{{ $t('exportColAct') }}</span>
        </div>

        <!-- 加载中:骨架而不是空列表 —— "还没问完"和"确实没有"是两件事 -->
        <div v-if="loading" class="jobs">
          <div v-for="n in 3" :key="n" class="jrow skel">
            <span class="sk sk-badge" /><span class="sk sk-main" /><span class="sk sk-time" /><span class="sk sk-act" />
          </div>
        </div>

        <div v-else-if="!jobs.length" class="empty">
          <div class="eic"><Inbox :size="22" color="var(--text-faint)" /></div>
          <div class="et">{{ $t('exportNoTasks') }}</div>
          <div class="es">{{ $t('exportEmptySub') }}</div>
        </div>

        <div v-else class="jobs">
          <div v-for="j in jobs" :key="j.id" class="jrow">
            <!-- 状态 -->
            <span class="jbadge" :class="meta(j.status).cls">
              <component :is="meta(j.status).icon" :size="12" :class="{ spin: j.status === 'running' }" />{{ $t(meta(j.status).t) }}
            </span>

            <!-- 实例与语句 -->
            <div class="jmain">
              <div class="jinst">{{ j.instance }}<span v-if="j.database" class="jdb"> / {{ j.database }}</span></div>
              <div class="jsql" :title="j.sql">{{ j.sql }}</div>
            </div>

            <span class="jtime">#{{ j.id }}<br>{{ j.createdAt?.slice(5, 16).replace('T', ' ') }}</span>

            <!-- 操作 -->
            <span class="jact">
              <VButton v-if="j.status === 'done'" variant="secondary" height="30px" @click="download(j)">
                <Download :size="13" />{{ $t('exportDownload') }}{{ j.parts > 1 ? ` (${j.parts})` : '' }}
              </VButton>
              <VButton v-else-if="j.status === 'failed' || j.status === 'expired'" variant="secondary" height="30px" @click="reuse(j)">
                <RotateCw :size="13" />{{ $t('exportReuse') }}
              </VButton>
            </span>

            <!-- 执行详情 / 错误:占满第二行,因为语句和报错都长 -->
            <div v-if="j.status === 'done'" class="jdet">
              <span class="jstat">{{ $t('exportRowsN', { n: j.rows }) }}<span class="dot">·</span>{{ kb(j.bytes) }}<span class="dot">·</span>{{ j.parts }} {{ $t('exportParts') }}</span>
              <span class="jpw">
                <KeyRound :size="12" /><span class="pl">{{ $t('exportPassword') }}</span>
                <code class="pw">{{ shownPw.has(j.id) ? j.password : '••••••••••' }}</code>
                <button class="iconbtn" :title="$t('tipTogglePw')" @click="togglePw(j.id)"><component :is="shownPw.has(j.id) ? EyeOff : Eye" :size="14" /></button>
                <button class="iconbtn" :title="$t('copy')" @click="copyPw(j)"><component :is="copiedId === j.id ? Check : Copy" :size="14" /></button>
              </span>
            </div>
            <!-- 过期:文件被保留策略删了,但导出了什么仍然留在这里 —— 审计问的是这个,不是文件。 -->
            <div v-else-if="j.status === 'expired'" class="jdet">
              <span class="jstat">{{ $t('exportRowsN', { n: j.rows }) }}<span class="dot">·</span>{{ kb(j.bytes) }}<span class="dot">·</span>{{ j.parts }} {{ $t('exportParts') }}</span>
              <span class="gtip"><Trash2 :size="12" />{{ $t('exportExpiredHint', { d: retentionDays }) }}</span>
            </div>
            <div v-else-if="j.status === 'failed'" class="jdet">
              <div class="jerr" :class="{ open: openErr.has(j.id) }" @click="toggleErr(j.id)">
                <CircleX :size="13" class="ei" />
                <span class="etext">{{ j.error || $t('exportStFailed') }}</span>
                <ChevronDown :size="13" class="ec" :class="{ up: openErr.has(j.id) }" />
              </div>
            </div>
          </div>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.page { flex: 1; min-height: 0; padding: var(--page-pad); max-width: var(--page-max); margin-inline: auto; width: 100%; }
/* 左栏定宽、右栏吃掉剩下的全部宽度。左边是一张表单,它的宽度由内容决定(再宽也
   只是把输入框拉长);右边是列表,宽度越多越有用 —— 语句和报错都能少截一点。 */
.col { display: grid; grid-template-columns: minmax(400px, 440px) minmax(0, 1fr); gap: 24px; align-items: start; }
/* 表单跟着滚:任务多起来的时候,提交栏不该被滚出屏幕。 */
.col > .formcard { position: sticky; top: 0; }
@media (max-width: 1100px) {
  .col { display: flex; flex-direction: column; max-width: 760px; }
  .col > .formcard { position: static; }
}
.card { border: 1px solid var(--border-subtle); border-radius: var(--radius-lg); background: var(--surface-card); box-shadow: var(--shadow-sm); }
/* 表单卡片**不能** overflow:hidden —— 实例选择器的浮层要能从卡片里探出去。
   下拉被裁掉的时候看着像"菜单只有半截",而其实是卡片把它剪了。 */
.formcard { overflow: visible; }
.shead { display: flex; align-items: center; gap: 11px; padding: 16px 20px; border-bottom: 1px solid var(--border-subtle); }
.sic { width: 32px; height: 32px; border-radius: 9px; background: var(--accent-subtle); display: flex; align-items: center; justify-content: center; flex-shrink: 0; }
.grow { flex: 1; min-width: 0; }
.st { font: 600 14px var(--font-display); color: var(--text-strong); }
.ss { font: 500 12px var(--font-body); color: var(--text-muted); margin-top: 1px; }
.poll { display: inline-flex; align-items: center; gap: 6px; font: 600 11px var(--font-body); color: var(--accent-text); white-space: nowrap; }
.refresh { display: inline-flex; align-items: center; gap: 6px; height: 30px; padding: 0 11px; border: 1px solid var(--border-default); border-radius: 8px; background: var(--surface-card); color: var(--text-muted); font: 600 11.5px var(--font-body); cursor: pointer; white-space: nowrap; }
.refresh:hover:not(:disabled) { color: var(--accent-text); border-color: var(--accent-text); }
.refresh:disabled { opacity: .6; cursor: default; }

/* ---------------- 左侧表单 ---------------- */
.body { padding: 16px 20px 20px; }
.notice { display: flex; align-items: center; gap: 9px; padding: 11px 14px; margin-bottom: 12px; border-radius: 10px; background: var(--warning-subtle); color: var(--warning-text); font: 600 12px var(--font-body); }
.notice span { flex: 1; }
.field { margin-bottom: 14px; }
.lbl { margin-bottom: 7px; font: 600 11px var(--font-mono); color: var(--text-muted); }
.sel, .nameinput { width: 100%; box-sizing: border-box; height: 38px; padding: 0 12px; border: 1px solid var(--border-default); border-radius: 9px; background: var(--surface-sunken); color: var(--text-strong); font: 500 12.5px var(--font-mono); outline: none; }
.sel { font-weight: 600; cursor: pointer; }
.sel:focus, .nameinput:focus { border-color: var(--accent-text); }
.prodwarn { margin: -4px 0 14px; display: flex; align-items: center; gap: 6px; font: 700 11.5px var(--font-mono); color: var(--danger-text); }
/* 语句框给足高度:导出用的 SQL 通常带 WHERE 和几个字段,四行的框要一直滚。 */
.sqlarea { width: 100%; box-sizing: border-box; min-height: 180px; border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-sunken); color: var(--text-strong); padding: 11px 13px; resize: vertical; font: 500 12.5px/1.7 var(--font-mono); outline: none; }
.sqlarea::placeholder { color: var(--text-faint); }
.sqlarea:focus { border-color: var(--accent-text); }
.senschk { display: flex; align-items: center; gap: 8px; cursor: pointer; }
.senstxt { font: 500 12.5px var(--font-body); color: var(--text-body); }
.senshint { display: flex; align-items: flex-start; gap: 6px; margin-top: 6px; padding: 8px 10px;
  border-radius: 8px; background: var(--warning-subtle); color: var(--warning-text);
  font: 500 11.5px var(--font-body); line-height: 1.5; }
.err { margin-top: 10px; font: 600 12px var(--font-body); color: var(--danger-text); }
.submit { margin-top: 16px; width: 100%; }
.subhint { margin-top: 9px; display: flex; justify-content: center; flex-wrap: wrap; gap: 5px; font: 500 12px var(--font-body); color: var(--text-faint); text-align: center; }

/* ---------------- 右侧任务列表 ---------------- */
/* 一行四栏:状态 | 实例与语句 | 时间 | 操作。执行详情与报错另起一行占满 2..-1,
   因为语句和报错都长 —— 硬塞进第五栏只会把它们截得都读不出来。 */
.jhead, .jrow { display: grid; grid-template-columns: 104px minmax(0, 1fr) 104px 128px; gap: 12px; align-items: center; }
.jhead { padding: 10px 20px; background: var(--surface-page); border-bottom: 1px solid var(--border-subtle); font: 600 10.5px var(--font-body); color: var(--text-faint); text-transform: uppercase; letter-spacing: .06em; }
.jhead .right { text-align: right; }
.jobs { display: flex; flex-direction: column; }
.jrow { padding: 13px 20px; border-bottom: 1px solid var(--border-subtle); transition: background var(--dur-fast, .15s) var(--ease-out, ease); }
.jrow:last-child { border-bottom: none; }
.jrow:hover { background: var(--surface-sunken); }
.jbadge { justify-self: start; display: inline-flex; align-items: center; gap: 5px; height: 22px; padding: 0 9px; border-radius: 999px; font: 600 10px var(--font-mono); white-space: nowrap; }
.jbadge.wait { background: var(--surface-sunken); color: var(--text-muted); border: 1px solid var(--border-subtle); }
.jbadge.gone { background: var(--surface-sunken); color: var(--text-faint); border: 1px solid var(--border-subtle); }
.jbadge.run { background: var(--accent-subtle); color: var(--accent-text); }
.jbadge.ok { background: var(--success-subtle); color: var(--success-text); }
.jbadge.bad { background: var(--danger-subtle); color: var(--danger-text); }
.jmain { min-width: 0; }
.jinst { font: 600 13px var(--font-mono); color: var(--text-strong); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.jdb { color: var(--text-faint); font-weight: 500; }
.jsql { margin-top: 3px; font: 500 12px var(--font-mono); color: var(--text-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.jtime { text-align: right; font: 500 10.5px/1.6 var(--font-mono); color: var(--text-faint); white-space: nowrap; }
.jact { justify-self: end; }
.jdet { grid-column: 2 / -1; margin-top: 9px; display: flex; align-items: center; flex-wrap: wrap; gap: 12px; min-width: 0; }
.jstat { flex-shrink: 0; font: 500 11px var(--font-mono); color: var(--text-muted); }
.dot { margin: 0 5px; color: var(--text-faint); }
.jpw { display: flex; align-items: center; gap: 7px; min-width: 0; color: var(--text-faint); }
.jpw .pl { font: 600 10px var(--font-mono); color: var(--text-faint); white-space: nowrap; }
.pw { min-width: 0; max-width: 220px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; padding: 5px 10px; border-radius: 7px; background: var(--accent-subtle); color: var(--accent-text); font: 800 12.5px var(--font-mono); letter-spacing: 1px; }
.iconbtn { width: 30px; height: 28px; flex-shrink: 0; border: 1px solid var(--border-default); border-radius: 7px; background: var(--surface-card); color: var(--text-body); cursor: pointer; display: flex; align-items: center; justify-content: center; }
.iconbtn:hover { color: var(--accent-text); border-color: var(--accent-text); }
.gtip { display: inline-flex; align-items: center; gap: 6px; min-width: 0; font: 600 11px var(--font-body); color: var(--text-faint); }
/* 报错自成一块浅红区域,默认单行:三条失败任务各占五行,列表就没法看了。
   但截断不等于看不到 —— 报错原文是排查的起点,点一下展开。 */
.jerr { flex: 1; min-width: 0; display: flex; align-items: flex-start; gap: 7px; padding: 8px 11px; border-radius: 8px; background: var(--danger-subtle); color: var(--danger-text); font: 500 11.5px/1.6 var(--font-mono); cursor: pointer; }
.jerr .ei { flex-shrink: 0; margin-top: 1px; }
.jerr .etext { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.jerr.open .etext { white-space: pre-wrap; word-break: break-word; }
.jerr .ec { flex-shrink: 0; margin-top: 1px; transition: transform var(--dur-fast, .15s) var(--ease-out, ease); }
.jerr .ec.up { transform: rotate(180deg); }

/* ---------------- 骨架与空态 ---------------- */
.skel { pointer-events: none; }
.sk { height: 14px; border-radius: 6px; background: var(--surface-sunken); animation: pulse 1.4s ease-in-out infinite; }
.sk-badge { height: 22px; border-radius: 999px; }
.sk-main { height: 32px; }
.sk-act { height: 30px; border-radius: 8px; }
@keyframes pulse { 0%, 100% { opacity: 1; } 50% { opacity: .45; } }
.empty { padding: 46px 24px; display: flex; flex-direction: column; align-items: center; gap: 8px; }
.eic { width: 46px; height: 46px; border-radius: 14px; background: var(--surface-sunken); display: flex; align-items: center; justify-content: center; }
.et { font: 600 13px var(--font-body); color: var(--text-body); }
.es { font: 500 12px var(--font-body); color: var(--text-faint); text-align: center; }

.spin { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
