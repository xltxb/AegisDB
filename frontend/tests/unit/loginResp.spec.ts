import { test, expect } from '@playwright/test'

import { isUsableLoginResp } from '../../src/lib/loginResp'

// GitHub issue #79。
//
// 不校验形状就落盘的后果不是「存了个空值」:`localStorage.setItem(k, undefined)` 存进去
// 的是**字符串 "undefined"**,它是真值 —— 路由守卫的 `if (!token)` 放行、界面渲染出来,
// 而每个请求都带着 `Bearer undefined` 在 401。看着像登录成功,其实没登上。
//
// 走到这里的前提是信封 code 合法(否则 `ok()` 先抛)但 data 里没有 token —— 现实来源是
// 网关、反向代理或 CDN 在 200 里塞了自己的响应体。

const good = { token: 'jwt-abc', expiresAt: '2026-09-17T00:00:00Z', user: { id: 1, name: 'Lin Wei' } }

test('一份正常的登录响应可用', () => {
  expect(isUsableLoginResp(good)).toBe(true)
})

test('缺 token —— 正是 #79 那条路径', () => {
  const { token, ...noToken } = good
  expect(isUsableLoginResp(noToken)).toBe(false)
})

// 空串落盘之后守卫的 `if (!token)` 确实拦得住,但它同样不是一次成功的登录 ——
// 让它在这里就停下,好过让人先看见一个空壳再被弹回登录页。
test('token 是空串也不算可用', () => {
  expect(isUsableLoginResp({ ...good, token: '' })).toBe(false)
})

test('token 不是字符串不算可用', () => {
  expect(isUsableLoginResp({ ...good, token: 123 })).toBe(false)
})

// 没有 user 的话 `login()` 返回 undefined,而调用方拿它去算落地页。
test('缺 user 不算可用', () => {
  const { user, ...noUser } = good
  expect(isUsableLoginResp(noUser)).toBe(false)
})

// `{code:0, data:[]}` —— e2e 的兜底路由桩正是这个形状,当初 9 条用例全卡在登录跳转上,
// 排查到最后才发现根因在这里。数组也是 object,所以这一条不能靠 typeof 判掉。
test('data 是数组不算可用', () => {
  expect(isUsableLoginResp([])).toBe(false)
})

test('data 是 null / undefined / 字符串都不算可用', () => {
  expect(isUsableLoginResp(null)).toBe(false)
  expect(isUsableLoginResp(undefined)).toBe(false)
  expect(isUsableLoginResp('ok')).toBe(false)
})

// 只校验**真正被消费的**两个字段。`expiresAt` 全仓库没有一处读它,把它列进必填会让
// 一个本来能用的响应被拒掉 —— 校验该盯着消费方,不是盯着类型声明。
test('缺 expiresAt 仍然可用,多余字段也不影响', () => {
  const { expiresAt, ...noExp } = good
  expect(isUsableLoginResp(noExp)).toBe(true)
  expect(isUsableLoginResp({ ...good, somethingNew: 1 })).toBe(true)
})
