import { test, expect } from '@playwright/test'
import { playTodoChime, setChimeMuted, chimeMuted } from '../../src/lib/chime'

// 提示音真的排出去了没有。
//
// 这一组是补票的:上一版里 playTodoChime 先发起 ac.resume(),然后**立刻**判
// `ac.state !== 'running'` 就 return —— 而 resume() 是异步的,那一刻状态必然还是
// suspended,于是一个音都没排出去。表现是"提示弹出来了,声音永远不响"。
//
// 它当时之所以没被抓住,是因为验证用的是无头浏览器:那里 AudioContext 一创建就是
// running,永远走不到这条路。所以这里**从 suspended 开始**——真实浏览器的样子。

type Osc = { start: (t: number) => void; stop: (t: number) => void; connect: (n: any) => any; type: string; frequency: { value: number } }

/** 装一个假的 AudioContext,并把它挂到 window 上。返回排了多少个振荡器。 */
function installFakeAudio(opts: { state?: 'suspended' | 'running'; resumeFails?: boolean } = {}) {
  const counter = { oscillators: 0 }
  class FakeCtx {
    state: string = opts.state ?? 'suspended'
    currentTime = 0
    // **状态必须异步地翻**,这是这份替身的关键。
    //
    // 写成 `async resume() { this.state = 'running' }` 看着等价,其实不是:async 函数体
    // 在第一个 await 之前是同步跑的,于是 resume() 一返回状态就已经是 running —— 那份
    // 替身对"先发起 resume、再立刻同步判 state"的写法照样绿,而真实浏览器里那样写一个
    // 音都排不出去。替身必须模仿真东西的**时序**,不只是它的接口。
    resume() {
      return new Promise<void>((res, rej) => setTimeout(() => {
        if (opts.resumeFails) { rej(new Error('blocked by autoplay policy')); return }
        this.state = 'running'
        res()
      }, 0))
    }
    createOscillator(): Osc {
      counter.oscillators++
      return {
        type: 'sine', frequency: { value: 0 },
        connect: (n: any) => n, start: () => {}, stop: () => {},
      }
    }
    createGain() {
      const ramp = () => {}
      return { gain: { setValueAtTime: ramp, exponentialRampToValueAtTime: ramp }, connect: (n: any) => n }
    }
    destination = {}
  }
  const store: Record<string, string> = {}
  // 装替身之前先把原样存下来,用完还回去(见下面的 afterEach)。
  //
  // 这个 runner 是**同进程**跑所有 spec 的:把 globalThis.window 顶掉而不还,别的用例
  // 就会在一个残缺的 window 上跑 —— vue-i18n 见到 window 存在就去取
  // window.performance.now(),于是那边炸在一个跟它自己毫无关系的地方。这一条踩过一次。
  saved.push({ window: (globalThis as any).window, localStorage: (globalThis as any).localStorage })
  ;(globalThis as any).window = { ...(globalThis as any).window, AudioContext: FakeCtx }
  ;(globalThis as any).localStorage = {
    getItem: (k: string) => store[k] ?? null,
    setItem: (k: string, v: string) => { store[k] = v },
    removeItem: (k: string) => { delete store[k] },
  }
  return counter
}

const saved: { window: unknown; localStorage: unknown }[] = []
test.afterEach(() => {
  while (saved.length) {
    const s = saved.pop()!
    ;(globalThis as any).window = s.window
    ;(globalThis as any).localStorage = s.localStorage
  }
})

// 每个用例装的是一份**新的**假构造器,而 chime.ts 的缓存是跟着构造器走的 —— 换一份就
// 自动重建,不需要任何测试专用的复位钩子。
test('上下文一开始是 suspended —— resume 醒过来之后仍然要把音排出去', async () => {
  const c = installFakeAudio({ state: 'suspended' })
  playTodoChime(3)
  // resume() 是异步的:它 resolve 之后才排音,所以要让出一轮事件循环。
  await new Promise((r) => setTimeout(r, 5))
  expect(c.oscillators, '三响 = 3 组 × 每组两个音').toBe(6)
})

test('上下文已经是 running —— 同步排音', () => {
  const c = installFakeAudio({ state: 'running' })
  playTodoChime(3)
  expect(c.oscillators).toBe(6)
})

test('默认响一次', () => {
  const c = installFakeAudio({ state: 'running' })
  playTodoChime()
  expect(c.oscillators).toBe(2)
})

test('静音了就不响', () => {
  const c = installFakeAudio({ state: 'running' })
  setChimeMuted(true)
  expect(chimeMuted()).toBe(true)
  playTodoChime(3)
  expect(c.oscillators).toBe(0)
  setChimeMuted(false)
  playTodoChime(1)
  expect(c.oscillators).toBe(2)
})

test('浏览器拒绝恢复(没有用户手势)—— 安静放弃,不抛异常', async () => {
  const c = installFakeAudio({ state: 'suspended', resumeFails: true })
  expect(() => playTodoChime(3)).not.toThrow()
  await new Promise((r) => setTimeout(r, 5))
  expect(c.oscillators).toBe(0)
})

test('浏览器没有 WebAudio —— 安静放弃', () => {
  saved.push({ window: (globalThis as any).window, localStorage: (globalThis as any).localStorage })
  ;(globalThis as any).window = { ...(globalThis as any).window, AudioContext: undefined, webkitAudioContext: undefined }
  ;(globalThis as any).localStorage = { getItem: () => null, setItem: () => {}, removeItem: () => {} }
  expect(() => playTodoChime(3)).not.toThrow()
})
