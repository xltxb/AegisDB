<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted, onActivated, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { Upload, FileCode2, RotateCw, ShieldAlert, FolderCog, ListChecks, ChevronDown, Download, Command, ClipboardPaste } from 'lucide-vue-next'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import ApprovalModal from '@/components/modals/ApprovalModal.vue'
import ScriptScanModal from '@/components/modals/ScriptScanModal.vue'
import SnippetModal from '@/components/modals/SnippetModal.vue'
import api from '@/api'
import { CODE_OK, CODE_INTERCEPTED, CODE_MFA_REQUIRED, CODE_SCRIPT_PATH_UNSET } from '@/api/http'
import { classifyExecEnvelope, type ExecEnvelope } from '@/lib/execOutcome'
import { renderRule as renderRuleIn, renderOutText } from '@/lib/ruleText'
import type { RuleRef } from '@/types'
import { useAuthStore } from '@/stores/auth'
import { useEnvTierStore } from '@/stores/envtier'
import { useSnippetStore } from '@/stores/snippets'
import { useUIStore } from '@/stores/ui'
import { LineEditor } from '@/lib/lineEditor'
import { isCopyShortcut } from '@/lib/copyShortcut'
import { needsArming, slotFromEvent, slotLabel, snippetPreview, snippetSubmitText } from '@/lib/snippet'
import { countStatements } from '@/lib/sqlCount'
import { WsTerminal, type WsStatus } from '@/lib/wsTerminal'
import { translateMetaSql, translateDescribe, type NoticeRef } from '@/lib/metaCommand'
import { Transcript } from '@/lib/transcript'
import { ANSI, c, isSelect, synthTable, buildTable, renderTable, renderVertical } from '@/lib/sqlResult'
import { highlightSqlAnsi } from '@/lib/sqlHighlight'
import type { Connection, Member, ScriptScanResp, ScriptUpload } from '@/types'

// One fully-isolated terminal session bound to a single connection. Each tab
// mounts its own instance → its own xterm, readline history, websocket, and
// in-flight approval/MFA state. Nothing is shared between tabs.
const props = defineProps<{
  conn: Connection
  active: boolean
  conns: Connection[]
  chain: Member[]
  scriptEnabled: boolean
  scriptSavePath: string
  db?: string // target database chosen in the tree (defaults to the connection's)
  gridView?: boolean // when true, result sets go to the HTML grid; terminal prints only the summary
}>()
const emit = defineEmits<{ 'update:risk': ['idle' | 'safe' | 'high']; 'update:wsStatus': [WsStatus]; 'update:db': [string]; result: [{ columns: string[]; rows: string[][] }]; close: [] }>()

const { t, te } = useI18n()
// 规则名按界面语言渲染;服务端认不出的 code 回落到它给的中文原串。
const ruleI18n = { t: t as never, te: te as never }
const renderRule = (ref: RuleRef | undefined, canonical: string) => renderRuleIn(ref, canonical, ruleI18n)
// 服务端打进终端的那句话(执行结果、维护态提示)同样按语言渲染,认不出的 code 回落到
// 服务端的中文原串 —— 那是降级,不是出错(见 lib/ruleText.ts)。
const renderOut = (ref: RuleRef | undefined, canonical: string) => renderOutText(ref, canonical, ruleI18n)
const auth = useAuthStore()
const envtier = useEnvTierStore()
const router = useRouter()
// The caution banner's severity comes from the connection's tier. A failed load
// leaves every tier unresolved, which cautionLine treats as "unknown, warn".
envtier.load().then(() => { if (props.active) maybeWarnDanger() }).catch(() => {})

const risk = ref<'idle' | 'safe' | 'high'>('idle')
const wsStatus = ref<WsStatus>('connecting')
watch(risk, (v) => emit('update:risk', v))
watch(wsStatus, (v) => emit('update:wsStatus', v))

// ---- approval modal ----
const apOpen = ref(false)
const apCmd = ref('')
const apRisk = ref<'high' | 'mid'>('high')
const apAuditId = ref('AUD-pending')
const apRule = ref('')

// ---- script modal ----
const scOpen = ref(false)
const scScan = ref<ScriptScanResp | null>(null)
const scSubmitted = ref(false)
const enabled = ref(props.scriptEnabled)
watch(() => props.scriptEnabled, (v) => (enabled.value = v))
const fileRef = ref<HTMLInputElement>()
const pathPromptOpen = ref(false)

// ---- target database within the instance ----
const dbOptions = ref<string[]>([])
const targetDb = ref('')
async function loadDbs() {
  let names: string[] = []
  try { const sc = await api.connectionSchema(props.conn.id); names = sc.databases.map((d) => d.name) } catch { /* ignore */ }
  if (props.conn.database && !names.includes(props.conn.database)) names.unshift(props.conn.database)
  dbOptions.value = names
  const pref = props.db || props.conn.database || names[0] || ''
  targetDb.value = names.includes(pref) ? pref : (names[0] || pref)
}
watch(() => props.db, (v) => { if (v) targetDb.value = v })

// ---- xterm + readline + resilient websocket ----
const termEl = ref<HTMLElement>()
let term: Terminal
let fit: FitAddon
let editor: LineEditor
let ws: WsTerminal
let ro: ResizeObserver | null = null
const pendingSql = ref('')
// What an empty result MEANS for the command in flight, as an i18n ref so it is
// rendered in whatever language is active when it prints (see lib/metaCommand).
const pendingEmptyNotice = ref<NoticeRef | null>(null)
// psql 的 \d 是两段:列,然后索引。第二段等第一段渲染完再发 —— 和
// pendingEmptyNotice 一样,是"延后到出结果那一刻"才用得上的东西。
const pendingFollow = ref<{ titleKey: string; sql: string } | null>(null)
const pendingVertical = ref(false) // render the pending command's result MySQL \G-style
const expandedMode = ref(false)    // psql \x: persistent expanded (vertical) display

// MFA step-up
const mfaOpen = ref(false)
const mfaInput = ref('')
const mfaErr = ref('')
let lastExec = { sql: '', reason: '' }
let pendingMfa: { sql: string; reason: string } | null = null

// The session recording. Captured HERE rather than read back out of xterm: the
// renderer's scrollback is capped and carries no timestamps, and by this point
// the text still has its structure. See lib/transcript for the masking.
const transcript = new Transcript()
const canExportLog = ref(false)

const out = (line = '') => {
  transcript.output(line, new Date())
  canExportLog.value = true
  term.write(line + '\r\n')
}
const outLines = (arr: string[]) => arr.forEach((l) => out(l))

// truncHint is printed under a table whose cells did not fit the terminal width.
// The full values are already in hand — only the display was cut — so the line's
// job is to say so and name the command that shows them whole. Without it the
// trailing `…` reads as "this is all there is".
const truncHint = (n: number) => c(ANSI.gray, t('resultTruncatedHint', { n, bs: '\\' }))

// The duration is measured in whole milliseconds, so anything faster than one
// rounds to zero — which is true but reads as a broken timer. `<1` says the same
// thing without inviting that reading. It comes up constantly on a simulated
// connection, where no database is contacted at all.
const msLabel = (ms?: number) => (ms && ms > 0 ? String(ms) : '<1')

/**
 * Save the session log as a file.
 *
 * The file is built from what was already displayed, so there is nothing to
 * re-fetch and no gate to pass — but the export IS audited, because /export
 * records the same act and a terminal that wrote production output to disk
 * silently would be the gap between the two.
 */
async function exportLog() {
  if (transcript.isEmpty) return
  const meta = {
    instance: `${props.conn.env}-${props.conn.name}`,
    database: targetDb.value || props.conn.database || '',
    user: auth.me?.name || '',
    exportedAt: new Date(),
  }
  const name = transcript.filename(meta)
  // renderFile, not render: the file carries a byte order mark so the editor it
  // is opened in knows it is UTF-8 rather than guessing the ANSI code page and
  // showing every Chinese line as mojibake.
  const blob = new Blob([transcript.renderFile(meta)], { type: 'text/plain;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)

  // Audit after the download is handed to the browser: a failed audit call must
  // not cost the operator the file they asked for, but it must still be loud.
  try {
    await api.recordTranscriptExport({
      connectionId: props.conn.id,
      filename: name,
      lines: transcript.length,
      dropped: transcript.droppedCount,
      database: meta.database,
    })
  } catch (e) {
    ui.notifyError(e, t('logExportAuditFailed'))
  }
}
const cssVar = (name: string, fb: string) =>
  getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fb

const ui = useUIStore()

// xtermTheme follows the app theme. xterm's DEFAULT 16-colour ANSI ramp is tuned
// for dark backgrounds, so on the light theme the result table (cyan numbers, gray
// rules) was near-invisible; each mode gets an explicit ramp with the right
// contrast for its background. Surface/foreground read the live CSS tokens.
// xtermTheme 跟随应用主题。
//
// 曾经把它写死成深色,理由是"控制台就该是深的"。那是拿一个审美偏好去覆盖用户
// 明确选的主题 —— 挑了浅色主题的人,看到的却是一块黑框,而他并没有要求过它。
// 终端的边界由 .xterm-wrap 的边框和聚焦辉光负责,不需要靠底色去抢。
//
// xterm 的默认 16 色 ANSI 色阶是给深色底调的,浅色下结果表里的青色数字、灰色分隔
// 线几乎看不见,所以两套各有一份对比度合适的色阶。
function xtermTheme() {
  const light = ui.resolvedTheme() === 'light'
  // 终端有自己的底色,不直接借用页面底色:它被包在一块带边框的面板里(见
  // .xterm-wrap),底色和页面一样就没有"这是一块控制台"的边界,只是一片会滚动的
  // 页面背景。亮色用卡片白,暗色用最沉的那一档。
  const base = {
    background: cssVar(light ? '--surface-card' : '--surface-sunken', light ? '#ffffff' : '#0c0e17'),
    foreground: cssVar('--text-body', light ? '#232838' : '#d7dee8'),
    cursor: cssVar('--accent-text', light ? '#2553e0' : '#58a6ff'),
    cursorAccent: cssVar(light ? '--surface-card' : '--surface-sunken', light ? '#ffffff' : '#0c0e17'),
    selectionBackground: light ? 'rgba(59,110,246,0.20)' : 'rgba(88,166,255,0.32)',
    // 失去焦点后选区仍然看得见,但明显退一档 —— 复制粘贴要跨窗口,选完切出去
    // 再切回来,选区不该消失,也不该看着仍然是活动的。
    // 刻意不设 selectionForeground:设了会把选中的整段刷成同一个前景色,
    // 语法高亮当场消失,而选中一段 SQL 正是为了看清它。
    selectionInactiveBackground: light ? 'rgba(80,96,130,0.16)' : 'rgba(139,147,167,0.22)',
  }
  if (light) {
    return {
      ...base,
      black: '#232838', red: '#d42a21', green: '#0f9355', yellow: '#a96606',
      blue: '#2553e0', magenta: '#6438f0', cyan: '#0c8da8', white: '#4b5468',
      brightBlack: '#69748b', brightRed: '#ac1f18', brightGreen: '#0c7344',
      brightYellow: '#8a5a06', brightBlue: '#1c41b8', brightMagenta: '#5a2ad6',
      brightCyan: '#106f86', brightWhite: '#232838',
    }
  }
  return {
    ...base,
    black: '#2b313e', red: '#ff6b61', green: '#43d17f', yellow: '#f5b642',
    blue: '#60a5fa', magenta: '#c4b5fd', cyan: '#3fd0e6', white: '#d7dee8',
    brightBlack: '#8b93a7', brightRed: '#ff8f87', brightGreen: '#6ee7a0',
    brightYellow: '#fcd34d', brightBlue: '#93c5fd', brightMagenta: '#d8c9ff',
    brightCyan: '#8beef8', brightWhite: '#f4f7fc',
  }
}

// Repaint the live terminal when the app theme is switched.
watch(() => ui.theme, () => {
  if (term) {
    term.options.theme = xtermTheme()
    term.refresh(0, term.rows - 1)
  }
})

// The prompt shows the current database (instance/db ❯) so every command in the
// scrollback records which database it ran against.
function promptText() {
  const db = targetDb.value
  // 提示符用翠绿:它是这一屏里唯一"轮到你了"的标记,而青色和结果表里的数字、
  // 蓝色和语法高亮里的关键字都撞色 —— 一眼扫下去分不出哪一行是提示符。
  const head = db
    ? c(ANSI.green, props.conn.name) + c(ANSI.gray, '/') + c(ANSI.cyan, db)
    : c(ANSI.green, props.conn.name)
  return head + ' ' + ANSI.bold + c(ANSI.green, '❯') + ANSI.reset + ' '
}
function promptLen() { return props.conn.name.length + (targetDb.value ? targetDb.value.length + 1 : 0) + 3 }
function contPrompt() { return ' '.repeat(Math.max(0, promptLen() - 2)) + c(ANSI.gray, '· ') }

// ---- 进入生产实例的红色确认 ----
//
// 终端里那行红字是**被动**的:它印在屏幕上,人扫一眼就滑过去了。这个弹窗是主动的,
// 它挡在路中间,要求先确认再操作。两者不重复 —— 弹窗打断"手快",红字负责之后一直提醒。
//
// 触发条件同样是分层的 dangerBanner,不是名字叫不叫 prod:第二个生产环境和第一个一样
// 危险,按名字挑会让人最不熟悉的那些集群拿到最弱的警告(理由见 cautionLine)。
//
// 每个标签页确认一次。这个状态是组件内的 ref,所以关掉标签页再打开会重新确认 ——
// 那正是"重新进入"该有的意思;而在标签页之间来回切不会反复弹。
const dangerAck = ref(false)
const dangerOpen = ref(false)

const isDangerTier = computed(() => !!envtier.tierOf(props.conn.env)?.dangerBanner)

function maybeWarnDanger() {
  if (isDangerTier.value && !dangerAck.value) dangerOpen.value = true
}

function confirmDanger() {
  dangerAck.value = true
  dangerOpen.value = false
  nextTick(() => term?.focus())
}

// 取消 = 我不该在这里。直接把这个标签页关掉,而不是留着一个"确认过一半"的终端 ——
// 留着它,下一次点进来就不会再问了。
function cancelDanger() {
  dangerOpen.value = false
  emit('close')
}

// A prominent, tier-coloured "you are operating on X" caution printed into the
// terminal itself (bold; red with an explicit "proceed with caution" on a tier
// that carries the danger banner).
//
// 环境红字曾经打在这里(cautionLine)。它现在是终端上方一条常驻的横幅,由
// TerminalView 渲染 —— 判据仍是分层的 dangerBanner,而不是名字叫不叫 prod。
// 这个函数没有别的调用方了,所以随之删掉,免得留一份"看着还在用"的死代码。

// 开场白里**不再**打那行环境红字。
//
// 它现在是终端上方一条常驻的横幅(见 TerminalView 的 .safety):打在屏幕里的那行
// 只在会话开头出现一次,滚几屏就再也看不见了 —— 而"你正在生产库上"这件事,恰恰是
// 越往后越需要提醒的。同一句话不该说两遍,留下常驻的那一份。
function banner() {
  const cn = props.conn
  const me = auth.me
  out(c(ANSI.gray, t('termConnected', { conn: `${cn.env}-${cn.name}`, role: cn.defaultRole, policy: cn.policy, user: me?.name || '' })))
  out(c(ANSI.gray, t('termHelpLine', { bs: '\\' })))
}

function fitNow() { try { fit.fit() } catch { /* ignore */ } }

onMounted(() => {
  loadDbs()
  term = new Terminal({
    fontFamily: cssVar('--font-mono', 'ui-monospace, Menlo, Consolas, monospace'),
    fontSize: 14,
    fontWeight: 400,
    fontWeightBold: 600,
    // 行距放到 1.5:这块屏幕上主要是被人**读**的东西 —— 结果表格、报错、审计行,
    // 不是滚动的日志流。行挨得太紧,一列数字看串行的代价比省下的几行高。
    lineHeight: 1.5,
    // letterSpacing 保持 0。结果集是用制表符画的框(renderTable:┌┬┐ ├┼┤ └┴┘ ─ │),
    // 而这些框线在 DOM 渲染器下本来就已经是断的(见下面 customGlyphs 的说明);
    // 再加字距只会把缝拉得更宽。行距只推开行,不会拆散同一行里的连续字符,所以
    // 上面那个 1.5 是安全的。
    letterSpacing: 0,
    // customGlyphs(默认 true)本可以让 xterm 自己画框线、接得严丝合缝,但它
    // **对 DOM 渲染器无效** —— 这是 xterm 自己的说明。本项目只装了 fit 插件,
    // 没有 canvas/webgl 渲染器,所以框线完全交给字体,JetBrains Mono 的 ─ 字形
    // advance 比单元格窄一点,于是每两个之间留一道缝,横线看着像虚线。
    //
    // 根治要装 @xterm/addon-webgl(装上后这行 customGlyphs 才真正生效)。本机的
    // npm 源对该包返回 403,装不了,所以先记在这里,别再把这条虚线错怪到行距或
    // 字距头上 —— 已经分别验证过,两者都不是原因。
    customGlyphs: true,
    // 竖线光标配 SQL 提示符,比方块少挡一个字符;失焦时改成空心,因为这个终端
    // 经常被审批框、MFA 框抢走焦点,光标长什么样是"敲下去有没有用"的唯一提示。
    cursorBlink: true,
    cursorStyle: 'bar',
    cursorWidth: 2,
    cursorInactiveStyle: 'outline',
    // 加粗只加粗,不改颜色。语法高亮已经用颜色区分了关键字,再让粗体跳到
    // 亮色版本,同一个词会有两种色 —— 那不是强调,是噪声。
    drawBoldTextInBrightColors: false,
    // 兜底可读性:主题里那些低对比的 ANSI 色(目标库自己吐出来的转义序列也能
    // 用),自动提到 3:1 再画。设得更高会把刻意的柔和色也拉爆,3 只救真正看不清的。
    minimumContrastRatio: 3,
    // 一次导出前的排查经常要往回翻几百行,默认 1000 行不够。
    scrollback: 5000,
    smoothScrollDuration: 120,
    theme: xtermTheme(),
  })
  fit = new FitAddon()
  term.loadAddon(fit)
  term.open(termEl.value!)

  // Snippet hotkeys. The handler runs before xterm interprets the key: returning
  // false stops it being written to the shell as an escape sequence (Alt+1 would
  // otherwise arrive as `\x1b1`), and preventDefault keeps the browser out of it.
  // Only the FOCUSED terminal sees the event, so background tabs stay inert.
  term.attachCustomKeyEventHandler((e) => {
    if (e.type !== 'keydown') return true
    // 有选区时 Ctrl+C 是**复制**,不是中断 —— 见 lib/copyShortcut.ts。
    //
    // 复制完把选区清掉:下一次 Ctrl+C 就该是中断了。不清的话选区一直在,中断就永远
    // 按不出来 —— 这也正是 Windows Terminal 的做法。
    if (isCopyShortcut(e, term.hasSelection())) {
      const sel = term.getSelection()
      // 选中即复制那条路径已经写过一次剪贴板了,这里再写一次是兜底:它在非 HTTPS
      // 源上会静默失败,而那时用户按下的这一次才是他唯一一次明确的复制动作。
      if (sel && navigator.clipboard?.writeText) navigator.clipboard.writeText(sel).catch(() => {})
      term.clearSelection()
      e.preventDefault()
      return false
    }
    const slot = slotFromEvent(e)
    if (slot === null) return true
    e.preventDefault()
    runSlot(slot)
    return false
  })

  // Select-to-copy: mirror the terminal selection into the clipboard automatically,
  // like a native terminal. Mouse/keyboard selection is a user gesture, so
  // writeText is allowed; silently no-op when the clipboard API is unavailable
  // (e.g. a non-HTTPS origin). lastCopied avoids redundant writes while dragging.
  let lastCopied = ''
  term.onSelectionChange(() => {
    const sel = term.getSelection()
    if (!sel || sel === lastCopied) return
    lastCopied = sel
    if (navigator.clipboard?.writeText) navigator.clipboard.writeText(sel).catch(() => {})
  })

  editor = new LineEditor(term, {
    prompt: promptText,
    promptLen,
    contPrompt,
    contPromptLen: promptLen,
    onSubmit: handleSubmit,
    // Ctrl+C:请求取消正在执行的那条语句。
    //
    // 原先这里是个空函数,注释写着"结果照样会回来" —— 那句话没错,但它把"取消"降级成
    // 了"等着"。操作员按下 Ctrl+C 的意思是让那条语句停下来,而不是让终端安静地继续等。
    //
    // 服务端收到这一帧后取消那条语句的上下文;驱动发出去的是 KILL QUERY / cancel
    // request,所以**取消不等于没执行** —— 最终那句话由服务端说(execCancelled),
    // 这里只说"已请求",不替它下结论。
    //
    // 编辑器仍然保持 busy:释放它的只有那条回执。取消是否成功都会有回执 —— 成功是
    // 一条取消结果,失败(语句已经跑完)是正常结果。
    onInterrupt: () => {
      if (!editor?.isBusy()) return
      if (ws.send({ type: 'cancel' })) out(c(ANSI.yellow, t('termCancelSent')))
    },
    highlight: highlightSqlAnsi,
  })

  ws = new WsTerminal({
    url: () => {
      const proto = location.protocol === 'https:' ? 'wss' : 'ws'
      const base = import.meta.env.VITE_WS_BASE || `${proto}://${location.host}/api/v1`
      return `${base}/terminal/ws`
    },
    // Carry the JWT in the Sec-WebSocket-Protocol header (not the URL) so it
    // never appears in server access logs or Referer headers.
    protocols: () => ['vela-token', localStorage.getItem('vela_token') || ''],
    onMessage: onWsMessage,
    onStatus: (s) => {
      wsStatus.value = s
      // The editor stays busy from submit until a reply arrives, and the ONLY
      // things that release it are the reply handlers. If the socket dies while a
      // command is in flight that reply never comes, so the prompt never returns
      // and every subsequent keystroke is swallowed into the paste queue — the
      // terminal looks completely dead and even "reconnect session" can't revive
      // it (printAbove skips the redraw while busy). Reconnecting restores the
      // socket but not the editor, so release it here and be honest that the
      // command's outcome is unknown: it may well have committed on the server.
      if (s === 'closed' && editor?.running) {
        out(c(ANSI.yellow, '· ' + t('termLostWhileRunning')))
        editor.resume()
      }
    },
    // Repeated handshake failures MIGHT mean an expired session — but a plain
    // WebSocket close can't be told apart from a backend restart / network blip.
    // Probe a REST endpoint: a 401 confirms the session is gone (the http
    // interceptor then redirects to login); anything else is transient, so keep
    // retrying instead of logging the user out (M16/R7).
    onAuthError: async () => {
      try {
        await api.me()
        ws.connect() // session still valid → resume the terminal socket
      } catch (e: any) {
        if (e?.response?.status !== 401) ws.connect() // network/5xx → not an auth failure, keep trying
        // 401 → api/http.ts already cleared the token and redirected to /login
      }
    },
  })

  banner()
  editor.start()
  ws.connect()

  ro = new ResizeObserver(() => fitNow())
  ro.observe(termEl.value!)
  // 绑在容器上、capture 阶段:粘贴事件落在 xterm 自己的隐藏 textarea 上,而它由
  // xterm 创建和销毁 —— 绑容器就不用去追那个元素的生命周期,capture 让我们能在
  // xterm 处理之前决定要不要拦。
  termEl.value!.addEventListener('paste', onTermPaste, true)
  if (props.active) nextTick(() => { fitNow(); term.focus() })
})

// A hidden xterm can't measure itself; (re)fit and focus when this tab is shown.
watch(() => props.active, (a) => { if (a) nextTick(() => { fitNow(); term.focus() }) })

// Re-entering the terminal route (kept alive) re-inserts the DOM: refit the
// active session so xterm matches the restored container size.
onActivated(() => { if (props.active) nextTick(() => { fitNow(); term.focus() }) })
// 标签页之间切换时,第一次切到某个生产实例上同样算"进入"。已确认过的不会再弹
// (dangerAck 是每个标签页各自的)。
watch(() => props.active, (on) => { if (on) maybeWarnDanger() })

onUnmounted(() => {
  // 监听器绑在 termEl 上,而 termEl 随组件一起消失;显式摘掉是为了不依赖那个巧合。
  termEl.value?.removeEventListener('paste', onTermPaste, true)
  ro?.disconnect()
  ws?.close()
  term?.dispose()
  if (armTimer) clearTimeout(armTimer)
})

// ---- command dispatch ----
// parseUseDb recognises a `USE <db>` statement (optionally quoted) and returns the
// database name, or null. Backend execution targets the database per-command via
// the DSN, so a server-side USE would not persist across the pool — instead we
// switch the terminal's target database client-side (updating the prompt and every
// subsequent command).
function parseUseDb(sql: string): string | null {
  const m = /^\s*use\s+[`"']?([A-Za-z0-9_$-]+)[`"']?\s*$/i.exec(sql)
  return m ? m[1] : null
}

function switchDb(db: string) {
  const canon = dbOptions.value.find((d) => d.toLowerCase() === db.toLowerCase())
  if (dbOptions.value.length && !canon) {
    out(c(ANSI.yellow, t('termUnknownDb', { db })))
    risk.value = 'safe'
    return
  }
  targetDb.value = canon || db
  emit('update:db', targetDb.value) // mirror onto the tab so the tree highlights it
  out(c(ANSI.green, t('termSwitchedDb', { db: targetDb.value })))
  risk.value = 'safe'
}

async function handleSubmit(stmt: string) {
  // Record what the operator submitted, before any of the client-side rewriting
  // below — the log should show what they typed, not the normalised form.
  transcript.command(stmt.trim(), new Date())
  canExportLog.value = true
  // \G (vertical) / \g (horizontal) are MySQL client display terminators, not SQL —
  // strip them before sending and remember whether to render the result vertically.
  const trimmed = stmt.trim()
  pendingVertical.value = /\\G\s*$/.test(trimmed)
  // Reset per-command state up front: a leftover notice from an earlier \d would
  // otherwise be printed for the next query that legitimately returns no rows.
  pendingEmptyNotice.value = null
  pendingFollow.value = null
  const raw = trimmed.replace(/\\[gG]\s*$/, '').replace(/;+\s*$/, '').trim()
  // 两类客户端命令,一条通道:psql 风格的 \dt/\d/\dn,以及 SQL*Plus 的
  // DESC/DESCRIBE(它不是 SQL,原样发给 Oracle 只会得到 ORA-00900)。翻译出来的
  // 查询照常走网关判定与审计 —— 和手敲那条目录查询没有区别。
  const meta = raw.startsWith('\\')
    ? translateMetaSql(raw, props.conn.engine)
    : translateDescribe(raw, props.conn.engine)
  if (meta) {
    pendingEmptyNotice.value = meta.emptyNotice ?? null
    pendingFollow.value = meta.follow ?? null
    if (sendExec(meta.sql, '') === 'ws') return
    try { handleExecEnv(await execRest(meta.sql, ''), meta.sql, '') }
    catch { out(c(ANSI.red, t('termExecFail'))); editor.resume() }
    return
  }
  if (raw.startsWith('\\')) {
    // 不是可翻译的目录命令 → 本地终端命令(\?、\c 之类)。
    metaCommand(raw); editor.resume(); return
  }
  if (!raw) { editor.resume(); return }
  const useDb = parseUseDb(raw)
  if (useDb) { switchDb(useDb); editor.resume(); return }
  try {
    const r = await api.riskCheck(props.conn.id, raw, targetDb.value)
    if (r.action === 'deny') {
      out(c(ANSI.red, t('termDeniedCap')))
      risk.value = 'safe'
      abandonBatch()
      return
    }
    if (r.requiresApproval) {
      risk.value = 'high'
      apCmd.value = raw
      apRisk.value = r.risk === 'high' ? 'high' : 'mid'
      apRule.value = renderRule(r.matchedRuleRef, r.matchedRule)
      apAuditId.value = t('auditPending')
      apOpen.value = true
      out(c(ANSI.yellow, t('termHitRule', { rule: apRule.value || '-' })))
      return
    }
    risk.value = 'safe'
    if (sendExec(raw, '') === 'ws') return
    const env = await execRest(raw, '')
    handleExecEnv(env, raw, '')
  } catch {
    out(c(ANSI.red, t('termExecFail')))
    editor.resume()
  }
}

function metaCommand(raw: string) {
  const parts = raw.slice(1).trim().split(/\s+/)
  const cmd = (parts[0] || '').toLowerCase()
  const arg = parts.slice(1).join(' ').replace(/;$/, '').replace(/["'`]/g, '')
  if (cmd === '?' || cmd === 'h' || cmd === 'help') {
    printMetaHelp()
  } else if (cmd === 'conns' || cmd === 'connlist') {
    outLines(props.conns.map((cn) => `  ${String(cn.id).padStart(3)}  ${cn.env}-${cn.name}  ${c(ANSI.gray, cn.defaultRole)}`))
  } else if (cmd === 'clear') {
    term.clear()
  } else if (cmd === 'c' || cmd === 'connect' || cmd === 'u' || cmd === 'use') {
    // psql \c <db> / MySQL-client \u <db>: switch the target database; no arg = clear.
    if (arg) switchDb(arg)
    else term.clear()
  } else if (cmd === 'x') {
    // psql \x: toggle persistent expanded (vertical) display. \x on|off set it.
    const a = arg.toLowerCase()
    expandedMode.value = a === 'on' || a === 'auto' ? true : a === 'off' ? false : !expandedMode.value
    out(c(ANSI.gray, t(expandedMode.value ? 'metaExpandOn' : 'metaExpandOff')))
  } else {
    out(c(ANSI.gray, t('termUnknownCmd', { bs: '\\', cmd })))
  }
}

// printMetaHelp lists the client backslash commands, showing engine-specific ones
// only for the matching engine. Command tokens are built in JS (real backslashes)
// so they dodge vue-i18n's message escaping; only the descriptions are translated.
function printMetaHelp() {
  const engine = props.conn.engine
  const isPG = /postgre|dws|gauss/i.test(engine)
  const isOra = /oracle/i.test(engine)
  const pad = (s: string) => (s + '            ').slice(0, 12)
  const line = (token: string, descKey: string) => '  ' + c(ANSI.cyan, pad(token)) + c(ANSI.gray, t(descKey as any))
  const lines = [c(ANSI.bold, t('termMetaTitle'))]
  lines.push(line('\\?', 'metaHelpHelp'))
  lines.push(line('\\conns', 'metaHelpConns'))
  lines.push(line('\\c <db>', 'metaHelpUse'))
  lines.push(line('\\clear', 'metaHelpClear'))
  lines.push(line('\\l', 'metaHelpL'))
  lines.push(line('\\dt', 'metaHelpDt'))
  lines.push(line('\\dv', 'metaHelpDv'))
  lines.push(line('\\dn', 'metaHelpDn'))
  lines.push(line('\\di', 'metaHelpDi'))
  if (isPG || isOra) lines.push(line('\\du', 'metaHelpDu'))
  if (isPG || isOra) lines.push(line('\\ds', 'metaHelpDs'))
  if (isPG) lines.push(line('\\df', 'metaHelpDf'))
  lines.push(line('\\d <obj>', 'metaHelpD'))
  lines.push(line('\\x', 'metaHelpX'))
  lines.push(line('\\conninfo', 'metaHelpConninfo'))
  lines.push(c(ANSI.gray, t('termMetaSql', { bs: '\\' })))
  outLines(lines)
}

// execRest is the REST fallback used whenever the websocket is down. Every call
// MUST carry the target database: the backend only overrides the connection's
// default schema when `database` is non-empty, so omitting it silently redirects
// the statement to a DIFFERENT database than the prompt shows. api.exec takes
// database as a trailing defaulted parameter, so two of the four call sites had
// simply left it off and executed against the wrong schema — and the approval
// path recorded that wrong database on the ticket, freezing the mistake into
// what the gateway later executes on the approver's behalf (EF1). Funnelling
// every fallback through here removes the whole class.
function execRest(sql: string, reason: string, mfaCode = '') {
  return api.exec(props.conn.id, sql, reason, mfaCode, targetDb.value)
}

// runFollow 发出目录命令的第二段(psql 的 \d 先列列、再列索引)。
//
// 先把 pendingFollow 清掉再发:第二段自己也会走到渲染那一步,不清就会无限套下去。
// 它照常经过网关判定与审计 —— 确实跑了第二条目录查询,记两条是如实的。
async function runFollow() {
  const f = pendingFollow.value
  if (!f) return
  pendingFollow.value = null
  out('')
  out(c(ANSI.bold, t(f.titleKey as any)))
  if (sendExec(f.sql, '') === 'ws') return
  try { renderExecEnvelope(await execRest(f.sql, '')) }
  catch { out(c(ANSI.gray, t('termExecFail'))) }
}

function sendExec(sql: string, reason: string, mfaCode = ''): 'ws' | 'rest' {
  lastExec = { sql, reason }
  pendingSql.value = sql
  if (ws.send({ type: 'exec', connectionId: props.conn.id, sql, reason, mfaCode, database: targetDb.value })) return 'ws'
  return 'rest'
}

function handleExecEnv(env: ExecEnvelope, sql: string, reason: string) {
  if (classifyExecEnvelope(env).kind === 'mfa') { requestMfa(sql, reason); return }
  renderExecEnvelope(env)
  editor.resume()
}

function requestMfa(sql: string, reason: string) {
  pendingMfa = { sql, reason }
  mfaInput.value = ''
  mfaErr.value = ''
  mfaOpen.value = true
  // The caret goes into the code box via v-autofocus on the input itself.
}

// Closing the prompt removes the focused input, which drops focus on <body> —
// the user's next keystroke would go nowhere until they clicked the terminal.
// Hand it back to xterm, unless the prompt is about to reopen for a bad code.
function closeMfa() {
  mfaOpen.value = false
  nextTick(() => { if (!mfaOpen.value) term.focus() })
}

async function submitMfa() {
  const code = mfaInput.value.trim()
  if (code.length !== 6) { mfaErr.value = t('mfaStepDesc'); return }
  if (!pendingMfa) return
  const { sql, reason } = pendingMfa
  closeMfa()
  if (sendExec(sql, reason, code) === 'ws') return
  try {
    const env = await execRest(sql, reason, code)
    if (env.code === CODE_MFA_REQUIRED) { requestMfa(sql, reason); mfaErr.value = t('mfaBadCode'); return }
    renderExecEnvelope(env)
  } catch { out(c(ANSI.red, t('termExecFail'))) }
  editor.resume()
}

// abandonBatch drops whatever remains of a pasted multi-statement batch and
// tells the user, then hands control back to the editor. A cancelled approval,
// a cancelled MFA prompt and a hard denial all mean "stop this batch": without
// it resume() replayed the stashed remainder, so the terminal said "cancelled,
// not executed" and immediately ran the following statements (EF4).
function abandonBatch() {
  if (editor.discardQueued() > 0) out(c(ANSI.yellow, '· ' + t('termBatchAbandoned')))
  editor.resume()
}

function cancelMfa() {
  closeMfa()
  pendingMfa = null
  out(c(ANSI.gray, t('termCancelled')))
  abandonBatch()
}

function onWsMessage(m: any) {
  if (m.type === 'output') renderOutput(m)
  else if (m.type === 'intercept') { renderIntercept(m); auth.pendingCount++ }
  else if (m.type === 'mfa_required') { requestMfa(lastExec.sql, lastExec.reason); return }
  else if (m.type === 'error') out(c(ANSI.red, '· ' + (m.message || t('termExecFail'))))
  // The server sends this when the account is disabled, the token generation is
  // bumped, or the terminal menu is revoked mid-connection — then closes the
  // socket. Falling through the else meant the message was DISCARDED: the editor
  // was never released, so the terminal froze, and the client just kept
  // reconnecting (a menu revocation doesn't invalidate the token, so /auth/me
  // still answered 200 and the retry loop never ended) — EF6.
  else if (m.type === 'session_revoked') {
    out(c(ANSI.red, '· ' + (m.message || t('wsDisconnected'))))
    editor.resume()
    ws.close()
    auth.clearSession()
    router.push('/login')
    return
  }
  else return
  editor.resume()
}

function renderOutput(m: { text?: string; outputRef?: RuleRef; rows?: number; ms?: number; columns?: string[]; data?: string[][]; truncated?: boolean }) {
  const rows = m.rows || 0
  if (m.columns && m.columns.length) {
    // Real result set returned by the target DB.
    const data = m.data || []
    // Some commands ask a question that "zero rows" cannot answer: describing a
    // relation that does not exist returns an empty column list, and drawing the
    // empty grid reads as "it exists but has no columns" (psql instead says it
    // did not find the relation). translateMetaSql tells us when that applies.
    if (data.length === 0 && pendingEmptyNotice.value) {
      const n = pendingEmptyNotice.value
      out(c(ANSI.yellow, '· ' + t(n.id, n.params ?? {})))
      pendingEmptyNotice.value = null
      pendingFollow.value = null // 表都没找到,再查它的索引没有意义
      risk.value = 'safe'
      return
    }
    emit('result', { columns: m.columns, rows: data }) // feed the HTML grid panel
    // Grid mode shows the data in the HTML panel; the terminal keeps only the
    // summary. \G / \x are explicit terminal-display choices, still honoured.
    if (pendingVertical.value || expandedMode.value) outLines(renderVertical(m.columns, data))
    else if (!props.gridView) outLines(renderTable(buildTable(m.columns, data), term.cols, truncHint))
    const more = m.truncated ? t('termTruncated', { n: data.length }) : ''
    out(c(ANSI.gray, t('termRows', { n: data.length, more, ms: msLabel(m.ms) })))
    runFollow()
  } else if (isSelect(pendingSql.value) && rows > 0) {
    // Simulated connection (no credentials): synthesise a preview.
    const tb = synthTable(pendingSql.value, rows)
    emit('result', { columns: tb.columns, rows: tb.rows })
    if (pendingVertical.value || expandedMode.value) outLines(renderVertical(tb.columns, tb.rows))
    else if (!props.gridView) outLines(renderTable(tb, term.cols, truncHint))
    const shown = tb.rows.length
    const more = shown < rows ? t('termShownFirst', { n: shown }) : ''
    out(c(ANSI.gray, t('termRows', { n: rows, more, ms: msLabel(m.ms) })))
  } else if (m.text) {
    // A write reports through `text`, so the timing has to be appended here —
    // the reads above get it from termRows. A notice ("· 目标实例处于维护态") is
    // not an execution and gets no duration: nothing ran to be timed.
    //
    // 打出来的是**渲染后**的那句话:服务端的中文串是规范记录,而终端上显示的语言
    // 是读的人的事。提示与执行结果靠开头的 `·` 区分,所以两种语言的文案都保留了
    // 这个前缀(见 model 里 Out* 常量上方的说明)。
    const line = renderOut(m.outputRef, m.text)
    const notice = line.trimStart().startsWith('·')
    out(notice ? c(ANSI.yellow, line) : c(ANSI.green, '✓ ' + line) + c(ANSI.gray, t('termTook', { ms: msLabel(m.ms) })))
  } else {
    out(c(ANSI.green, t('termExecOk')) + c(ANSI.gray, t('termTook', { ms: msLabel(m.ms) })))
  }
  risk.value = 'safe'
}

function renderIntercept(m: { rule?: string; ruleRef?: RuleRef; approvalNo?: string }) {
  out(c(ANSI.red, t('termIntercepted')))
  const rule = renderRule(m.ruleRef, m.rule || '') || apRule.value
  out(c(ANSI.gray, t('termHitRuleLine', { rule: rule || '-' })))
  out(c(ANSI.gray, t('termApprovalLine', { no: m.approvalNo || '-' })))
}

// renderExecEnvelope prints whatever the server actually decided. The outcome is
// classified first (see lib/execOutcome): a rejection carries no payload, so
// rendering it as "a result with nothing in it" printed a green success line for
// a command that had been REFUSED (EF2).
function renderExecEnvelope(env: ExecEnvelope) {
  const outcome = classifyExecEnvelope(env)
  switch (outcome.kind) {
    case 'intercepted':
      renderIntercept({ rule: outcome.rule, ruleRef: outcome.ruleRef, approvalNo: outcome.approvalNo })
      auth.pendingCount++
      return
    case 'failed':
      out(c(ANSI.red, '· ' + (outcome.message || t('termExecFail'))))
      risk.value = 'safe'
      return
    case 'mfa':
      requestMfa(lastExec.sql, lastExec.reason)
      return
    default:
      renderOutput({ text: outcome.data.output, rows: outcome.data.rows, ms: outcome.data.ms,
        columns: outcome.data.columns, data: outcome.data.data, truncated: outcome.data.truncated })
  }
}

async function submitApproval(reason: string) {
  apOpen.value = false
  if (sendExec(apCmd.value, reason) === 'ws') return
  try {
    const env = await execRest(apCmd.value, reason)
    const outcome = classifyExecEnvelope(env)
    if (outcome.kind === 'mfa') { requestMfa(apCmd.value, reason); return }
    if (outcome.kind === 'intercepted') {
      renderIntercept({ rule: outcome.rule, ruleRef: outcome.ruleRef, approvalNo: outcome.approvalNo })
      auth.pendingCount++
    } else {
      // The command may not have been intercepted after all (the rule could have
      // been relaxed between the pre-check and the submit), in which case the
      // server just RAN it. Printing nothing left the operator with no evidence
      // that anything happened, and the bare catch hid outright failures too (EF7).
      renderExecEnvelope(env)
    }
  } catch { out(c(ANSI.red, t('termExecFail'))) }
  editor.resume()
}

function cancelApproval() {
  apOpen.value = false
  out(c(ANSI.gray, t('termCancelSubmit')))
  abandonBatch()
}

function refreshSession() {
  // 横幅一直挂在上面,这里只说重连本身。
  editor.printAbove([c(ANSI.gray, t('termReconnecting'))])
  ws.reconnect()
}

// ---- snippets on hotkeys Alt+1…9 ----
//
// The hotkey TYPES; it does not execute. The snippet's text is fed to the line
// editor exactly as a paste is, so it leaves through handleSubmit and gets the
// risk check, the approval prompt and the MFA step-up like anything else — judged
// against the connection this tab is on, at the moment the key is pressed. There
// is deliberately no server-side "run snippet" call: that would be a second way
// into the gateway, and the judgement lives on the first one.
const snippets = useSnippetStore()
snippets.load().catch(() => { /* the button still opens; the modal reloads */ })
const snipOpen = ref(false)

// Hand focus back to xterm on close, or the hotkeys stay dead until the operator
// clicks the terminal — the same trap the MFA prompt had.
function closeSnippets() {
  snipOpen.value = false
  nextTick(() => term?.focus())
}

// notice puts a line on screen from either state. printAbove rewinds over the
// input block to redraw it, which is right while the editor is idle and wrong
// while a statement is running — there the prompt is already gone and the rewind
// would erase output instead.
const notice = (line: string) => {
  if (editor.running) out(line)
  else editor.printAbove([line])
}

// A hotkey is fast and it is blind: the same key that is routine on dev is one
// keystroke away on prod, and muscle memory does not read the prompt. On a tier
// carrying the danger banner the first press therefore only ARMS the key and
// says what it would run, on which instance; the second press within the window
// fires it. Everywhere else it fires immediately.
//
// This is not the safety net — the gateway is, and it judges the statement
// either way. It is there for the case the gateway is right to allow: a snippet
// that is perfectly legal on prod and simply wasn't meant for prod.
const ARM_MS = 4000
let armedSlot = 0
let armTimer = 0
function disarm() {
  armedSlot = 0
  if (armTimer) { clearTimeout(armTimer); armTimer = 0 }
}

function runSlot(slot: number) {
  const s = snippets.bySlot[slot]
  if (!s) { notice(c(ANSI.gray, t('snipNoBinding', { key: slotLabel(slot) }))); return }
  // Firing while a statement runs would queue the text behind it and execute
  // seconds later, against whatever the session looks like by then. A paste does
  // that because the operator asked for it explicitly; a stray hotkey has not.
  if (editor.running) { notice(c(ANSI.yellow, '· ' + t('snipBusy'))); return }
  const text = snippetSubmitText(s.body)
  if (!text) return

  if (needsArming(envtier.tierOf(props.conn.env)) && armedSlot !== slot) {
    disarm()
    armedSlot = slot
    armTimer = window.setTimeout(() => { armedSlot = 0; armTimer = 0 }, ARM_MS)
    editor.printAbove([
      ANSI.bold + ANSI.yellow + t('snipArm', { key: slotLabel(slot), name: s.name, env: props.conn.env.toUpperCase(), inst: props.conn.name }) + ANSI.reset,
      c(ANSI.gray, '  ' + snippetPreview(s.body, 100)),
      c(ANSI.gray, t('snipArmHint', { key: slotLabel(slot) })),
    ])
    return
  }
  disarm()
  editor.printAbove([c(ANSI.cyan, `${slotLabel(slot)} · ${s.name}`)])
  editor.feed(text)
}

// ---- paste SQL through a dialog ----
//
// A textarea for SQL copied from elsewhere. Keyboard paste into xterm works, but
// it is invisible until it runs: a long batch scrolls past as it is echoed, and
// the operator has no chance to look at what they are about to hand to PROD.
// The dialog shows the text first; confirming feeds it to the line editor the
// same way a keyboard paste (or a snippet hotkey) does, so every statement
// leaves through handleSubmit — risk pre-check, approval prompt, MFA step-up —
// one after another. There is no separate "run this text" call to the server.
const pasteOpen = ref(false)
const pasteText = ref('')

function openPaste(initial = '') {
  // Feeding while a statement runs would queue the text behind it and execute
  // seconds later, against whatever the session looks like by then (see runSlot).
  if (editor.running) { notice(c(ANSI.yellow, '· ' + t('pasteBusy'))); return }
  pasteText.value = initial
  pasteOpen.value = true
}

// ---- 粘进来的多条语句改走弹窗 ----
//
// 键盘粘贴本来是直接进行编辑器的:第一条立刻执行,其余的排队,一条接一条回显着
// 滚过去。粘一句是这样最顺手,粘二十句就是**在看不清的情况下开始对生产下发** ——
// 想中途停下只能按 Ctrl+C,而那时前面几条已经跑完了。
//
// 所以:粘贴内容看着是多条时,拦下来交给「粘贴 SQL」弹窗,让人先看见全文再确认。
// 确认之后走的还是同一条路(editor.feed → handleSubmit),逐条过判定、该弹审批弹
// 审批 —— 这里改的只是"下发前先不先给人看一眼",不是任何一条语句怎么被判。
//
// 判断多条用的是 countStatements,它是个**界面用的启发式**:数错了两个方向都无害
// (见 lib/sqlCount 的注释),没有语句能因此绕过网关。
function onTermPaste(e: ClipboardEvent) {
  const text = e.clipboardData?.getData('text') ?? ''
  if (countStatements(text) <= 1) return // 单条照旧直接进行编辑器
  // 阻止 xterm 收到这次粘贴:否则文本会同时进编辑器和弹窗,确认后跑两遍。
  e.preventDefault()
  e.stopPropagation()
  if (editor.running) { notice(c(ANSI.yellow, '· ' + t('pasteBusy'))); return }
  openPaste(text)
  notice(c(ANSI.cyan, '· ' + t('pasteAutoOpened', { n: countStatements(text) })))
}

function closePaste() {
  pasteOpen.value = false
  nextTick(() => term?.focus())
}

function submitPaste() {
  // Same normalisation as a snippet: CRLF → LF, and a terminator appended when
  // the last statement has none, so the final line submits instead of sitting
  // in the editor waiting for Enter.
  const text = snippetSubmitText(pasteText.value)
  closePaste()
  if (!text) return
  // **整批一次提交**,不按 ; 拆开逐条送。
  //
  // 原先是 editor.feed():一条一条过判定,于是一批里只要有一条高危,前面安全的那几条
  // 已经跑完了,只有那一条去等审批 —— 审批人看到的是一条脱离上下文的语句,而库里
  // 已经变了一半;批准之后剩下的还要再跑一轮。一批粘进来的语句本来就是一件事。
  //
  // 整批提交之后,判定与执行仍然是逐条的,只是都发生在服务端:一次判定取最严裁决、
  // 一张审批单带整段命令,下发时一条一条给驱动(见 service.execCommand)。
  editor.submitBatch(text)
}

// ---- script upload/scan ----
function onUploadClick() {
  if (!enabled.value) { pathPromptOpen.value = true; return }
  fileRef.value?.click()
}
function onSampleClick() {
  if (!enabled.value) { pathPromptOpen.value = true; return }
  loadSample()
}
function goSettings() {
  pathPromptOpen.value = false
  router.push('/settings')
}

// uploadId > 0 means the script is already uploaded (dispatched from the
// "uploaded scripts" picker) — executing it must not save another copy.
const scUploadId = ref(0)
async function scanScript(text: string, filename: string, uploadId = 0) {
  scUploadId.value = uploadId
  try {
    // With an uploadId the server reads the file itself; sending `text` as well
    // would be the second copy that can disagree with it.
    scScan.value = await api.scriptScan(uploadId > 0 ? '' : text, filename, props.conn.id, uploadId)
    scSubmitted.value = false
    // 新的一次扫描 = 新的一张单子,重置执行记录。
    scDone.value = null
    scRunning.value = false
    scOpen.value = true
  } catch (e: any) {
    if (e?.code === CODE_SCRIPT_PATH_UNSET) { enabled.value = false; pathPromptOpen.value = true }
    else editor.printAbove([c(ANSI.red, t('termScanFail'))])
  }
}

function onFile(e: Event) {
  const file = (e.target as HTMLInputElement).files?.[0]
  if (!file) return
  const reader = new FileReader()
  reader.onload = () => { scanScript(String(reader.result || ''), file.name) }
  reader.readAsText(file)
  ;(e.target as HTMLInputElement).value = ''
}

// ---- dispatch an already-uploaded script from the terminal ----
const uploads = ref<ScriptUpload[]>([])
const pickOpen = ref(false)
const pickPos = ref({ top: 0, left: 0 })
async function togglePick(e: MouseEvent) {
  if (!pickOpen.value) {
    const r = (e.currentTarget as HTMLElement).getBoundingClientRect()
    pickPos.value = { top: r.bottom + 5, left: r.left }
    try { uploads.value = await api.scriptUploads() } catch { uploads.value = [] }
  }
  pickOpen.value = !pickOpen.value
}
async function runUploaded(u: ScriptUpload) {
  pickOpen.value = false
  try {
    // The file is on the server and the gateway reads it there. Fetching the body
    // only to post it straight back moved a 6MB script across the wire three
    // times — and the copy the client held was never what got judged anyway.
    await scanScript('', u.filename, u.id) // already uploaded → don't re-save on execute
  } catch { editor.printAbove([c(ANSI.red, t('termUploadLoadFail'))]) }
}

async function loadSample() {
  const sample = `-- migration_2026q3.sql
SELECT count(*) FROM orders WHERE status='paid';
UPDATE users SET tier='vip' WHERE id=88;
ALTER TABLE orders DROP COLUMN legacy_ref;
DELETE FROM sessions;
TRUNCATE TABLE orders_2024_q3;
INSERT INTO audit_log(evt) VALUES('migrate');`
  await scanScript(sample, 'migration_2026q3.sql')
}

/**
 * 这次弹窗里,这个脚本已经跑过了没有。
 *
 * 两道闸,挡的是两件不同的事:
 *
 *   scRunning  —— **并发**。原先没有任何保护:连点两下会并发发出两次
 *                 scriptExecute,两次完整执行同时落到库上。一个 INSERT 脚本
 *                 因此插两遍,而两次都会"成功"。
 *   scDone     —— **重复**。高危分支执行后弹窗是不关的(scSubmitted=true 之后
 *                 直接 return),按钮还活着 —— 再点一次就再生成一张审批单。
 *
 * 这是**客户端**的闸:刷新页面、或重新扫一遍同一个文件,它就不认识了。真正的
 * 一次性保证只在审批那条路上有(ClaimApprovalExecution 原子占位,批一次只换一次
 * 执行);安全脚本直接执行那条路服务端没有防重。这一点在 UI 上不隐瞒。
 */
const scRunning = ref(false)
const scDone = ref<{ at: string; by: string; kind: 'ran' | 'submitted' } | null>(null)

function markScriptDone(kind: 'ran' | 'submitted') {
  scDone.value = {
    at: new Date().toLocaleTimeString('sv').slice(0, 5),
    by: auth.me?.name || '',
    kind,
  }
}

async function runScript() {
  if (!scScan.value) return
  // 闸放在这里,而不是只靠按钮的 disabled:disabled 是渲染出来的状态,而这个函数
  // 也可能被别的路径调到 —— 真正管用的判断要和动作待在一起。
  if (scRunning.value || scDone.value) return
  scRunning.value = true
  try {
    await doRunScript()
  } finally {
    scRunning.value = false
  }
}

async function doRunScript() {
  if (!scScan.value) return
  // Reassembling the script out of the scan result only means anything while the
  // client is the one holding it. For an uploaded script the server re-reads the
  // file, so send nothing and let it.
  const content = scUploadId.value > 0
    ? ''
    : scScan.value.statements.map((s) => s.sql + ';').join('\n')
  try {
    const env = await api.scriptExecute(content, scScan.value.filename, props.conn.id, '', scUploadId.value, targetDb.value)
    if (env.code === CODE_SCRIPT_PATH_UNSET) {
      scOpen.value = false; enabled.value = false; pathPromptOpen.value = true; return
    }
    if (env.code === CODE_INTERCEPTED || env.data?.exec?.intercepted) {
      scSubmitted.value = true
      // 审批单已经生成 —— 再点一次只会多出一张一模一样的单子。
      markScriptDone('submitted')
      auth.pendingCount++
      if (env.data?.savedPath) editor.printAbove([c(ANSI.gray, t('termScriptSaved', { path: env.data.savedPath }))])
      return
    }
    if (env.code === CODE_OK) {
      const n = env.data?.statements ?? scScan.value.total
      const saved = env.data?.savedPath || ''
      editor.printAbove([
        c(ANSI.cyan, '\\i ' + scScan.value.filename),
        c(ANSI.green, t('termScriptOk', { n })),
        ...(saved ? [c(ANSI.gray, t('termSavedTo', { path: saved }))] : []),
      ])
      markScriptDone('ran')
      scOpen.value = false
      return
    }
    // 失败**不**打标:没跑成的脚本本来就该允许改完再来一次。
    scOpen.value = false
    editor.printAbove([c(ANSI.red, t('termScriptFailMsg', { msg: env.msg || '' }))])
  } catch {
    scOpen.value = false
    editor.printAbove([c(ANSI.red, t('termScriptFail'))])
  }
}
</script>

<template>
  <div class="session">
    <div class="actionbar">
      <div class="host"><span class="online" :class="{ off: wsStatus !== 'open' }" />{{ conn.name }}<span v-if="targetDb" class="curdb">· {{ targetDb }}</span> · SQL</div>
      <div class="actions">
        <div class="upload" :title="enabled ? '' : $t('scPathTitle')" @click="onUploadClick"><Upload :size="13" />{{ $t('uploadScript') }}</div>
        <input ref="fileRef" type="file" accept=".sql,text/plain" class="hidden-file" @change="onFile" />
        <div class="upload" @click="togglePick"><ListChecks :size="13" />{{ $t('uploadedScripts') }}<ChevronDown :size="12" /></div>
        <Teleport to="body">
          <template v-if="pickOpen">
            <div class="pick-mask" @click="pickOpen = false" />
            <div class="picker" :style="{ top: pickPos.top + 'px', left: pickPos.left + 'px' }">
              <div class="pick-head">{{ $t('uploadedScripts') }}</div>
              <div v-if="!uploads.length" class="pick-empty">{{ $t('uploadNone') }}</div>
              <div v-for="u in uploads" :key="u.id" class="pick-item" :title="u.path" @click="runUploaded(u)">
                <FileCode2 :size="12" /><span class="pn">{{ u.filename }}</span><span class="pm">{{ u.createdAt?.slice(5, 16) }}</span>
              </div>
            </div>
          </template>
        </Teleport>
        <div class="upload" :title="$t('pasteBtnTitle')" @click="openPaste()"><ClipboardPaste :size="13" />{{ $t('pasteBtn') }}</div>
        <div class="upload" :title="$t('snipBtnTitle')" @click="snipOpen = true"><Command :size="13" />{{ $t('snipBtn') }}</div>
        <div class="sample" @click="onSampleClick"><FileCode2 :size="13" />{{ $t('sampleScript') }}</div>
        <!-- Disabled until something has actually been printed: an empty file is
             not a useful thing to hand someone. -->
        <div class="upload" :class="{ off: !canExportLog }" :title="$t('logExportHint')" @click="canExportLog && exportLog()">
          <Download :size="13" />{{ $t('logExport') }}
        </div>
        <RotateCw class="refresh" :size="14" :title="$t('refreshSession')" @click="refreshSession" />
      </div>
    </div>

    <div class="xterm-wrap"><div ref="termEl" class="xterm-host" /></div>

    <!-- 底部状态栏。左边回答"此刻这条会话是什么",右边是快捷键 —— 后者原先只写在
         开场白里,滚两屏就再也找不到了,而它恰恰是要反复用的东西。 -->
    <div class="statusbar">
      <span class="ok" :class="{ warn: wsStatus !== 'open' }">
        {{ wsStatus === 'open' ? $t('connected') : wsStatus === 'connecting' ? $t('wsConnecting') : $t('wsDisconnected') }}
      </span>
      <span class="hl">{{ conn.engine.toUpperCase() }}</span>
      <span>{{ conn.defaultRole }}</span><span>{{ conn.policy }}</span>
      <span>UTF-8</span>
      <span class="right keyhint">
        <span><kbd>↑</kbd><kbd>↓</kbd> {{ $t('sbHistory') }}</span>
        <span><kbd>Ctrl</kbd><kbd>L</kbd> {{ $t('sbClear') }}</span>
        <span><kbd>Ctrl</kbd><kbd>C</kbd> {{ $t('sbCancel') }}</span>
      </span>
    </div>

    <ApprovalModal
      :open="apOpen" :command="apCmd" :instance="conn.name" :env="conn.env"
      :risk="apRisk" :audit-id="apAuditId" :chain="chain" :rule="apRule"
      @cancel="cancelApproval" @submit="submitApproval"
    />
    <ScriptScanModal
      :open="scOpen" :scan="scScan" :submitted="scSubmitted" :save-path="scriptSavePath"
      :instance="conn.name" :databases="dbOptions" :running="scRunning" :done="scDone"
      v-model:target-db="targetDb" @close="scOpen = false" @run="runScript"
    />
    <SnippetModal :open="snipOpen" @close="closeSnippets" />

    <div v-if="pasteOpen" class="mfa-overlay">
      <div class="mfa-mask" @click="closePaste" />
      <div class="mfa-modal paste-modal">
        <div class="mfa-top info" />
        <div class="mfa-pad">
          <div class="mfa-title acc"><ClipboardPaste :size="18" />{{ $t('pasteTitle') }}</div>
          <div class="mfa-desc">{{ $t('pasteDesc', { env: conn.env.toUpperCase(), inst: conn.name }) }}</div>
          <textarea
            v-autofocus
            v-model="pasteText"
            class="paste-box"
            spellcheck="false"
            :placeholder="$t('pastePh')"
            @keydown.ctrl.enter.prevent="submitPaste"
            @keydown.esc.prevent="closePaste"
          />
          <div class="paste-hint">{{ $t('pasteHint') }}</div>
          <div class="mfa-acts">
            <button class="mfa-btn ghost" @click="closePaste">{{ $t('btnCancel') }}</button>
            <button class="mfa-btn primary" :disabled="!pasteText.trim()" @click="submitPaste">{{ $t('pasteRun') }}</button>
          </div>
        </div>
      </div>
    </div>


    <!-- 进入生产实例的红色确认。挡在路中间,先确认再操作 —— 终端里那行红字是被动的,
         人扫一眼就滑过去了。 -->
    <div v-if="dangerOpen" class="mfa-overlay">
      <div class="mfa-mask" />
      <div class="mfa-modal danger-modal">
        <div class="mfa-top" />
        <div class="mfa-pad">
          <div class="mfa-title"><ShieldAlert :size="18" />{{ $t('prodWarnTitle') }}</div>
          <div class="danger-target">
            <span class="dt-env">{{ conn.env.toUpperCase() }}</span>
            <span class="dt-name">{{ conn.name }}</span>
            <span v-if="targetDb" class="dt-db">/ {{ targetDb }}</span>
          </div>
          <div class="mfa-desc">{{ $t('prodWarnDesc') }}</div>
          <div class="mfa-acts">
            <button class="mfa-btn ghost" @click="cancelDanger">{{ $t('prodWarnLeave') }}</button>
            <button class="mfa-btn danger" @click="confirmDanger">{{ $t('prodWarnGo') }}</button>
          </div>
        </div>
      </div>
    </div>

    <div v-if="pathPromptOpen" class="mfa-overlay">
      <div class="mfa-mask" @click="pathPromptOpen = false" />
      <div class="mfa-modal">
        <div class="mfa-top info" />
        <div class="mfa-pad">
          <div class="mfa-title acc"><FolderCog :size="18" />{{ $t('scPathTitle') }}</div>
          <div class="mfa-desc">{{ $t('scPathDesc') }}</div>
          <div class="mfa-acts">
            <button class="mfa-btn ghost" @click="pathPromptOpen = false">{{ $t('btnCancel') }}</button>
            <button class="mfa-btn primary" @click="goSettings">{{ $t('scPathGo') }}</button>
          </div>
        </div>
      </div>
    </div>

    <div v-if="mfaOpen" class="mfa-overlay">
      <div class="mfa-mask" @click="cancelMfa" />
      <div class="mfa-modal">
        <div class="mfa-top" />
        <div class="mfa-pad">
          <div class="mfa-title"><ShieldAlert :size="18" />{{ $t('mfaStepTitle') }}</div>
          <div class="mfa-desc">{{ $t('mfaStepDesc') }}</div>
          <input
            v-autofocus
            class="mfa-code" inputmode="numeric" autocomplete="one-time-code" placeholder="000000"
            :value="mfaInput"
            @input="mfaInput = ($event.target as HTMLInputElement).value.replace(/\D/g, '').slice(0, 6)"
            @keyup.enter="submitMfa"
          />
          <div v-if="mfaErr" class="mfa-err">{{ mfaErr }}</div>
          <div class="mfa-acts">
            <button class="mfa-btn ghost" @click="cancelMfa">{{ $t('btnCancel') }}</button>
            <button class="mfa-btn primary" @click="submitMfa">{{ $t('mfaVerify') }}</button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.session { flex: 1; min-height: 0; display: flex; flex-direction: column; background: var(--surface-page); }
.actionbar { display: flex; align-items: center; gap: 9px; height: 38px; padding: 0 14px; background: var(--surface-raised); border-bottom: 1px solid var(--border-subtle); min-width: 0; overflow: hidden; }
.host { flex: 1; min-width: 0; display: flex; align-items: center; gap: 7px; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; font: 600 12px var(--font-mono); color: var(--text-strong); }
.curdb { color: var(--accent-text); }
.online { width: 6px; height: 6px; border-radius: 50%; background: var(--success); flex-shrink: 0; transition: background var(--dur-fast) var(--ease-out); }
.online.off { background: var(--warning); }
/* 放不下时横向滚动,而不是被 .actionbar 的 overflow:hidden 一刀切掉。
   原先窄屏上最后一两个按钮(快捷脚本、导出日志)是**看不见也点不到**的,
   而且没有任何迹象表明它们存在。 */
.actions { flex: 0 1 auto; min-width: 0; display: flex; align-items: center; gap: 10px; overflow-x: auto; scrollbar-width: none; font: 500 11px var(--font-mono); color: var(--text-muted); }
.actions::-webkit-scrollbar { display: none; }
/* 实例名可以被压缩(它有省略号兜底),但不能压到没有。 */
.host { flex: 1 1 120px; }
.upload {
  display: inline-flex; align-items: center; gap: 6px; height: 28px; padding: 0 11px;
  border: 1px solid var(--accent-subtle-border); background: var(--accent-subtle); border-radius: 8px;
  color: var(--accent-text); font: 600 11px var(--font-mono); cursor: pointer;
}
/* Nothing printed yet — the control stays visible (so it is discoverable) but
   inert, rather than appearing and disappearing as the session starts. */
.upload.off { opacity: 0.45; cursor: not-allowed; }
.hidden-file { display: none; }
.sample { display: inline-flex; align-items: center; gap: 5px; height: 28px; padding: 0 10px; border: 1px solid var(--border-default); background: var(--surface-card); border-radius: 8px; color: var(--text-body); cursor: pointer; }
.sample:hover { color: var(--accent-text); border-color: var(--accent-subtle-border); }
.pick-mask { position: fixed; inset: 0; z-index: 900; }
.picker { position: fixed; z-index: 901; min-width: 260px; max-width: 420px; max-height: 320px; overflow-y: auto; padding: 5px; border: 1px solid var(--border-default); border-radius: 10px; background: var(--surface-overlay, var(--surface-card)); box-shadow: 0 12px 32px rgba(0, 0, 0, 0.4); }
.pick-head { padding: 6px 9px 7px; font: 600 10px var(--font-mono); color: var(--text-faint); text-transform: uppercase; letter-spacing: 0.04em; }
.pick-empty { padding: 14px; text-align: center; font: 500 11.5px var(--font-mono); color: var(--text-faint); }
.pick-item { display: flex; align-items: center; gap: 8px; padding: 8px 9px; border-radius: 7px; color: var(--text-body); cursor: pointer; }
.pick-item:hover { background: var(--accent-subtle); color: var(--accent-text); }
.pn { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font: 600 12px var(--font-mono); }
.pm { flex-shrink: 0; font: 500 10px var(--font-mono); color: var(--text-faint); }
.refresh { color: var(--text-muted); cursor: pointer; transition: color var(--dur-fast) var(--ease-out); }
.refresh:hover { color: var(--accent-text); }
/* ---- 终端面板 ----
   xterm 自己只画字符网格,周围的一切都要外面给。这块把它做成一块控制台面板:
   自己的底色、一圈边框、内圈留白,让它在页面上是一个"东西",而不是一片恰好
   有等宽字的区域。 */
/* 面板底色必须和 xterm 自己的底色是同一个来源,否则深色的终端会被一圈异色的边
   框在中间,像贴上去的一张图。两边都跟随主题,所以两边都用同一组 token。 */
.xterm-wrap {
  flex: 1; min-height: 0; overflow: hidden;
  margin: 10px 12px; padding: 12px 14px;
  background: var(--surface-card);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-xs);
  transition: border-color var(--dur-fast, 0.15s) var(--ease-out, ease),
              box-shadow var(--dur-fast, 0.15s) var(--ease-out, ease);
}
/* 暗色下换成最沉的一档,并去掉投影 —— 这套暗色主题靠边框和辉光分层,不用投影。 */
[data-theme='dark'] .xterm-wrap { background: var(--surface-sunken); box-shadow: none; }

/* 有焦点时描一圈强调色。这个终端经常被审批框、MFA 框、快捷脚本弹窗抢走焦点,
   而"我现在敲字会进到哪里"在一个能对生产库下命令的界面里不是装饰问题。
   xterm 的输入落在一个隐藏 textarea 上,所以 :focus-within 正好命中。 */
.xterm-wrap:focus-within {
  border-color: var(--accent-subtle-border, var(--accent));
  box-shadow: 0 0 0 3px var(--accent-subtle);
}

.xterm-host { width: 100%; height: 100%; }
.xterm-host :deep(.xterm) { height: 100%; }

/* 细滚动条。xterm 的视口默认用系统滚动条,在这套深色面板里是一条突兀的浅灰。
   只在悬停时提亮,不抢内容。 */
.xterm-host :deep(.xterm-viewport) {
  scrollbar-width: thin;
  scrollbar-color: var(--border-default) transparent;
  background-color: transparent !important;
}
.xterm-host :deep(.xterm-viewport)::-webkit-scrollbar { width: 10px; }
.xterm-host :deep(.xterm-viewport)::-webkit-scrollbar-track { background: transparent; }
.xterm-host :deep(.xterm-viewport)::-webkit-scrollbar-thumb {
  background: var(--border-default);
  border: 3px solid transparent;
  border-radius: var(--radius-full);
  background-clip: padding-box;
}
.xterm-host :deep(.xterm-viewport)::-webkit-scrollbar-thumb:hover { background: var(--text-faint); background-clip: padding-box; }
/* 底部状态栏保持 VS Code 那种窄条形态(28px、等宽小字、右侧快捷键),但**跟着
   主题走** —— 它贴着终端底边,终端是浅的时候压一条黑条,等于把控制台切成两半。 */
.statusbar { height: 28px; background: var(--surface-raised); border-top: 1px solid var(--border-subtle); display: flex; align-items: center; gap: 14px; padding: 0 14px; font: 500 10.5px var(--font-mono); color: var(--text-muted); }
.statusbar .ok { color: var(--success-text); }
.statusbar .ok.warn { color: var(--warning-text); }
.statusbar .hl { color: var(--text-body); }
.statusbar .right { margin-left: auto; }
.statusbar .keyhint { display: inline-flex; align-items: center; gap: 10px; color: var(--text-faint); }
.statusbar kbd { padding: 0 4px; border-radius: 4px; background: var(--surface-sunken); border: 1px solid var(--border-subtle); color: var(--text-body); font: 600 10px var(--font-mono); }

/* modals */
.mfa-overlay { position: fixed; inset: 0; z-index: 60; }
.mfa-mask { position: absolute; inset: 0; background: rgba(4, 6, 12, 0.7); backdrop-filter: blur(3px); }
.mfa-modal {
  position: absolute; left: 50%; top: 50%; transform: translate(-50%, -50%);
  width: 380px; max-width: 92vw; background: var(--surface-card);
  border: 1px solid var(--border-default); border-radius: 16px; overflow: hidden;
  box-shadow: 0 30px 80px -20px rgba(0, 0, 0, 0.7);
}
.mfa-top { height: 4px; background: linear-gradient(90deg, #f0473e, #f5a524); }
.mfa-top.info { background: linear-gradient(90deg, #3b6ef6, #2dcde6); }
.mfa-pad { padding: 20px 22px; }
.mfa-title { display: flex; align-items: center; gap: 9px; font: 700 16px var(--font-display); color: var(--text-strong); }
.mfa-title svg { color: var(--danger); }
.mfa-title.acc svg { color: var(--accent-text); }
.mfa-desc { margin-top: 6px; font: 400 12.5px/1.5 var(--font-body); color: var(--text-muted); }
.mfa-code {
  margin-top: 16px; width: 100%; height: 46px; border: 1px solid var(--border-default); border-radius: 10px;
  background: var(--surface-sunken); color: var(--text-strong); text-align: center;
  font: 700 22px var(--font-mono); letter-spacing: 10px; outline: none;
}
.mfa-code:focus { border-color: var(--accent-text); }
.danger-modal { width: 460px; }
/* 目标写在最显眼的位置:人要确认的是"我在哪台机器上",不是"我读没读这段话"。 */
.danger-target {
  margin-top: 14px; padding: 12px 14px; border-radius: 10px;
  background: var(--danger-subtle); border: 1px solid var(--danger);
  display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap;
}
.dt-env { font: 700 13px var(--font-mono); color: var(--danger-text); letter-spacing: 0.06em; }
.dt-name { font: 600 14px var(--font-mono); color: var(--text-strong); }
.dt-db { font: 500 13px var(--font-mono); color: var(--text-muted); }
.mfa-btn.danger { background: var(--danger); color: #fff; border-color: transparent; }
.mfa-btn.danger:hover { filter: brightness(1.08); }
.mfa-err { margin-top: 8px; font: 600 12px var(--font-body); color: var(--danger-text); }
.paste-modal { width: 640px; }
.paste-box {
  margin-top: 14px; width: 100%; height: 240px; resize: vertical; box-sizing: border-box;
  padding: 10px 12px; border: 1px solid var(--border-default); border-radius: 10px;
  background: var(--surface-sunken); color: var(--text-strong);
  font: 500 13px/1.5 var(--font-mono); outline: none; white-space: pre; overflow: auto;
}
.paste-box:focus { border-color: var(--accent-text); }
.paste-hint { margin-top: 6px; font: 400 11.5px var(--font-body); color: var(--text-faint); }
.mfa-btn:disabled { opacity: 0.45; cursor: not-allowed; }
.mfa-acts { margin-top: 16px; display: flex; justify-content: flex-end; gap: 10px; }
.mfa-btn { height: 36px; padding: 0 16px; border-radius: 9px; font: 600 12px var(--font-body); cursor: pointer; border: 1px solid var(--border-default); }
.mfa-btn.ghost { background: transparent; color: var(--text-body); }
.mfa-btn.ghost:hover { background: var(--surface-sunken); }
.mfa-btn.primary { background: var(--accent); color: #fff; border-color: transparent; }
.mfa-btn.primary:hover { background: var(--accent-hover); }
</style>
