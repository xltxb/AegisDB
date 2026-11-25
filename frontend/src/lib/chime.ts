// 待办提示音。
//
// 为什么是**合成**出来的、而不是打包一个 mp3:这套网关最常见的部署环境是断网的内网机器,
// 整个前端也因此一个 CDN 都不走(Swagger UI 都是 vendored 进二进制的)。一个音频文件要么
// 进构建产物、要么走网络,而 WebAudio 两个振荡器就能给出一声干净的提示音,零字节、零请求。
//
// 音色刻意收着:两个短音(A5 → E6)、每组约 0.35 秒。它要能在有人盯着别的
// 窗口时被听见,而不是把整个办公室的头都转过来 —— 一个吓人的提示音,第二天就会被静音。

const MUTE_KEY = 'aegisdb_todo_chime_muted'

// 连响几次时,每次之间隔多久。0.45s 让三下听起来是"一串"而不是三次独立的提醒。
const REPEAT_GAP = 0.45

// 峰值音量。0.12 试下来在笔记本外放上偏轻,容易被当成没响;0.22 在安静的办公室里
// 听得清,又不至于让人吓一跳 —— 正弦波本来就比方波柔和得多。
const PEAK = 0.22

/** 提示音是不是被这台浏览器静音了。读不到 localStorage(隐私模式)就当没静音。 */
export function chimeMuted(): boolean {
  try { return localStorage.getItem(MUTE_KEY) === '1' } catch { return false }
}

export function setChimeMuted(v: boolean) {
  try { v ? localStorage.setItem(MUTE_KEY, '1') : localStorage.removeItem(MUTE_KEY) } catch { /* 无痕模式:这一轮有效就行 */ }
}

// 一个 AudioContext 反复用。每响一次新建一个,浏览器很快就会拒绝再开
// (Chrome 的上限在 6 个上下),而那之后提示音会静静地不再响。
let ctx: AudioContext | null = null
let ctxCtor: unknown = null
function audioCtx(): AudioContext | null {
  try {
    const AC = window.AudioContext || (window as any).webkitAudioContext
    if (!AC) return null
    // 缓存跟着**构造器**走。同一个构造器就复用同一个上下文;构造器换了,说明这个
    // window 已经不是刚才那个(测试里换一份替身,或者页面被整个换掉),缓存里那个
    // 属于一个不复存在的世界,继续用它只会往一个没人听的地方排音。
    if (!ctx || ctxCtor !== AC) {
      ctx = new AC()
      ctxCtor = AC
    }
    return ctx
  } catch { return null }
}

/**
 * 响一声。静音、浏览器不支持、或者**没有用户手势**时安静地什么都不做。
 *
 * 最后那一条是浏览器的自动播放策略,不是 bug:页面从未被点过就出声,会被直接掐掉。所以
 * 这个提示音挂在**登录之后** —— 登录那一下点击/回车本身就是手势,单页应用不重新加载文档,
 * 那次手势一直有效。反过来说:带着 token 刷新页面(F5)是没有手势的,那一次不会响,而这
 * 正是想要的 —— 刷新不是"刚上班",不该每次都叫一声。
 */
export function playTodoChime(times = 1) {
  if (chimeMuted()) return
  const ac = audioCtx()
  if (!ac) return
  // **resume() 是异步的。**
  //
  // 这里原先写的是「发起 resume,然后立刻判 ac.state !== 'running' 就 return」—— 而那一
  // 刻状态必然还是 suspended,于是一个音都没排出去:提示照弹,声音永远不响。无头浏览器
  // 里 AudioContext 一创建就是 running,所以"数振荡器调用"的那种测试根本碰不到这条路。
  //
  // 正确的做法是等它真的醒过来再排音。被拒绝(没有用户手势)时安静放弃 —— 控制台里一行
  // 未处理的 rejection 比听不到提示音更糟。
  if (ac.state === 'suspended') {
    ac.resume().then(() => schedule(ac, times)).catch(() => {})
    return
  }
  schedule(ac, times)
}

/** 把 times 组「叮-咚」排进 WebAudio 的时钟。 */
function schedule(ac: AudioContext, times: number) {
  if (ac.state !== 'running') return
  // 整段一次排完,不用 setTimeout 串:WebAudio 的时钟不受主线程卡顿影响,而一串定时器
  // 在页面正忙(登录后要拉一堆接口)的时候会把节奏抖成三下不齐的敲门声。
  const now = ac.currentTime
  for (let r = 0; r < Math.max(1, times); r++) {
    const base = now + r * REPEAT_GAP
    // A5 → E6,一升一落的两声,像门铃而不像警报。
    for (const [i, freq] of [880, 1318.5].entries()) {
      const osc = ac.createOscillator()
      const gain = ac.createGain()
      osc.type = 'sine'
      osc.frequency.value = freq
      const t0 = base + i * 0.12
      // 直接开关会"啪"一声(波形突变)。用一小段淡入淡出包住它。
      gain.gain.setValueAtTime(0.0001, t0)
      gain.gain.exponentialRampToValueAtTime(PEAK, t0 + 0.02)
      gain.gain.exponentialRampToValueAtTime(0.0001, t0 + 0.22)
      osc.connect(gain).connect(ac.destination)
      osc.start(t0)
      osc.stop(t0 + 0.24)
    }
  }
}
