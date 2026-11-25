import { test, expect } from '@playwright/test'

import { srdLoginHref, justReturnedFromSrd } from '../../src/lib/srdLogin'

// 文档要求:redirect 整体 encodeURIComponent;回跳地址落在网关登录页(hash 路由)。
test('拼 SRD 登录地址:回跳到网关登录页,整体编码', () => {
  const href = srdLoginHref('https://srd.bssrvc66.com/auth/login', { origin: 'https://dbgw.bssrvc66.com', pathname: '/' })
  expect(href).toBe('https://srd.bssrvc66.com/auth/login?redirect=https%3A%2F%2Fdbgw.bssrvc66.com%2F%23%2Flogin')
  expect(new URL(href).searchParams.get('redirect')).toBe('https://dbgw.bssrvc66.com/#/login')
})

test('登录地址已带查询串时用 & 接上', () => {
  expect(srdLoginHref('https://srd.x.com/auth/login?t=1', { origin: 'https://a.x.com', pathname: '/gw/' }))
    .toBe('https://srd.x.com/auth/login?t=1&redirect=' + encodeURIComponent('https://a.x.com/gw/#/login'))
})

test('刚从 SRD 回来:几分钟内算,过久或没记过不算', () => {
  const now = 1_000_000_000
  expect(justReturnedFromSrd(String(now - 30_000), now)).toBe(true)
  expect(justReturnedFromSrd(String(now - 10 * 60_000), now)).toBe(false)
  expect(justReturnedFromSrd(null, now)).toBe(false)
  expect(justReturnedFromSrd('garbage', now)).toBe(false)
  expect(justReturnedFromSrd(String(now + 60_000), now)).toBe(false)
})
