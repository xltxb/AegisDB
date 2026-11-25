import { test, expect } from '@playwright/test'

import { DEFAULT_BRAND, LOGO_MAX_BYTES, brand, brandNamed, brandText, checkLogoFile, docTitle, setBrand } from '../../src/lib/brand'
import { Transcript } from '../../src/lib/transcript'

// 系统名称与 LOGO 可配置(.scratch/branding)。没配置时各处保持原字样;配了任意一个名称就统一换掉。

test.afterEach(() => setBrand(null))

test('未配置:界面名称保持 DP DB GATEWAY,调用方给的默认值原样返回', () => {
  setBrand(null)
  expect(brandNamed()).toBe(false)
  expect(brandText('zh')).toBe(DEFAULT_BRAND)
  expect(brandText('en')).toBe('DP DB GATEWAY')
  expect(brandText('zh', 'Vela 数据库网关')).toBe('Vela 数据库网关')
})

test('中英名称按界面语言取,缺一个回落另一个', () => {
  setBrand({ name: ' 星河数据网关 ', nameEn: 'Galaxy DB', logo: '' })
  expect(brand.name).toBe('星河数据网关')
  expect(brandText('zh')).toBe('星河数据网关')
  expect(brandText('en')).toBe('Galaxy DB')

  setBrand({ name: '星河数据网关' })
  expect(brandText('en')).toBe('星河数据网关')
  setBrand({ nameEn: 'Galaxy DB' })
  expect(brandText('zh', 'Vela 数据库网关')).toBe('Galaxy DB')
})

test('浏览器标题:待办数做前缀,没有待办只显示名称', () => {
  expect(docTitle('DP DB GATEWAY', 0)).toBe('DP DB GATEWAY')
  expect(docTitle('星河', 3)).toBe('(3) 星河')
})

test('上传前检查类型与大小', () => {
  expect(checkLogoFile({ type: 'image/png', size: 1024 })).toBe('')
  expect(checkLogoFile({ type: 'image/svg+xml', size: LOGO_MAX_BYTES })).toBe('')
  expect(checkLogoFile({ type: 'image/bmp', size: 10 })).toBe('type')
  expect(checkLogoFile({ type: 'text/html', size: 10 })).toBe('type')
  expect(checkLogoFile({ type: 'image/png', size: LOGO_MAX_BYTES + 1 })).toBe('size')
})

test('终端会话日志抬头:未配置保持原文,配置后换成系统名称', () => {
  const at = new Date(2026, 7, 7, 14, 2, 15)
  const meta = { instance: 'x', database: '', user: '', exportedAt: at }
  const tr = new Transcript()
  tr.command('SELECT 1;', at)
  setBrand(null)
  expect(tr.render(meta)).toContain('# Vela 数据库网关 · 终端会话日志')
  setBrand({ name: '星河数据网关' })
  const out = tr.render(meta)
  expect(out).toContain('# 星河数据网关 · 终端会话日志')
  expect(out).not.toContain('Vela')
})
