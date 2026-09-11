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
