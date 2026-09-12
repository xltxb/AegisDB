import { test, expect } from '@playwright/test'

import { ENGINES, engineFamily, engineLabels, isDrivable } from '../../src/lib/engines'

// The console must not offer an engine the gateway cannot actually execute
// against: such a connection saves fine and then silently behaves as a simulated
// one, so an operator believes an instance is attached when nothing is.
test('every offered engine maps to a wire protocol the gateway can drive', () => {
  for (const e of ENGINES) {
    expect(engineFamily(e.id), e.id).not.toBe('')
    expect(isDrivable(e.id), e.id).toBe(true)
  }
})

test('the catalogue covers the engines in use', () => {
  const ids = ENGINES.map((e) => e.id)
  for (const want of ['mysql', 'tidb', 'polardb', 'postgres', 'dws', 'oracle']) {
    expect(ids, want).toContain(want)
  }
})

// Families mirror the backend's engineFamily: the label a user picks is free
// text, so both ends have to agree on what protocol it means.
test('engine ids resolve to the same families the backend uses', () => {
  expect(engineFamily('mysql')).toBe('mysql')
  expect(engineFamily('mariadb')).toBe('mysql')
  expect(engineFamily('tidb')).toBe('mysql')
  expect(engineFamily('polardb')).toBe('mysql') // PolarDB is MySQL-compatible
  expect(engineFamily('postgres')).toBe('postgres')
  expect(engineFamily('dws')).toBe('postgres')
  expect(engineFamily('oracle')).toBe('oracle')
})

// Anything the gateway cannot speak must be reported as such rather than being
// quietly attached to the nearest driver.
test('engines the gateway cannot drive are not drivable', () => {
  for (const e of ['mongodb', 'redis', 'clickhouse', '']) {
    expect(engineFamily(e), e).toBe('')
    expect(isDrivable(e), e).toBe(false)
  }
})

// A stored connection may carry any historical label; grouping and driver choice
// must still work for those.
test('free-text labels are recognised, not just canonical ids', () => {
  expect(engineFamily('MySQL 8.0')).toBe('mysql')
  expect(engineFamily('GaussDB (DWS)')).toBe('postgres')
  expect(engineFamily('PolarDB-X')).toBe('mysql')
  expect(engineFamily('Oracle 19c')).toBe('oracle')
})

test('labels are offered for the dropdown in catalogue order', () => {
  const labels = engineLabels()
  expect(labels).toHaveLength(ENGINES.length)
  expect(labels[0]).toBe(ENGINES[0].label)
})

// PolarDB 有两个版本,MySQL 兼容版和 PostgreSQL 兼容版,**两个的引擎标签里都带
// polardb**。谁先匹配谁就赢,所以顺序是这里唯一要紧的事。
//
// 后端 realdb.go 把 postgre/dws/gauss 放在 MySQL 那一串**之前**,注释里写明了代价:
// 先匹配 polardb 会把 PolarDB for PostgreSQL 送去 MySQL 驱动,而它在那里根本连不上。
// 前端这份是同一张表的副本,判错的后果换了个样子 —— 元命令翻译成另一个方言的 SQL、
// 对象浏览按错的协议取库表,而连接本身是通的,于是看起来像"这台库怎么什么都查不到"。
test('PolarDB 的两个版本按标签里的 PostgreSQL 标记分开', () => {
  for (const label of ['PolarDB for PostgreSQL', 'PolarDB PostgreSQL 版', 'polardb postgres 14']) {
    expect(engineFamily(label), label).toBe('postgres')
  }
  for (const label of ['PolarDB for MySQL', 'polardb-mysql 8.0', 'PolarDB']) {
    expect(engineFamily(label), label).toBe('mysql')
  }
  // 认的是 "postgre" 这个词本身,所以 `polardb-pg` 这样的简写两端都认不出来,一律
  // 落回 MySQL。这里把它写下来不是认可,是**记录两端一致**:前端是后端那张表的副本,
  // 副本的职责是照抄,不是自作主张地多认一种写法 —— 多认了,判定层和驱动层就会对同
  // 一台实例给出两个答案。要认 `pg`,得两边一起认。
  expect(engineFamily('polardb-pg 14')).toBe('mysql')
})
