import { test, expect } from '@playwright/test'
import { ssoStartHref, ssoErrorKey } from '../../src/lib/ssoLogin'

test('发起地址按页面位置解析:网关在站点根', () => {
  expect(ssoStartHref('api/v1/auth/oidc/start', { origin: 'https://dbgw.example.com', pathname: '/' }))
    .toBe('https://dbgw.example.com/api/v1/auth/oidc/start')
})

test('发起地址按页面位置解析:网关挂在子路径下', () => {
  expect(ssoStartHref('api/v1/auth/oidc/start', { origin: 'https://a.example.com', pathname: '/gw/' }))
    .toBe('https://a.example.com/gw/api/v1/auth/oidc/start')
})

test('回调原因:认识的原样返回,不认识的按换取失败,没有就是 null', () => {
  expect(ssoErrorKey('denied')).toBe('denied')
  expect(ssoErrorKey('email')).toBe('email')
  expect(ssoErrorKey('<script>')).toBe('exchange')
  expect(ssoErrorKey(undefined)).toBeNull()
  expect(ssoErrorKey('')).toBeNull()
})
