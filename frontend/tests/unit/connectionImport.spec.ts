import { test, expect } from '@playwright/test'

import { parseConnectionImport, IMPORT_TEMPLATE } from '../../src/lib/connectionImport'

const HEADER = 'name,engine,env,host,policy,database,username,password'

test('parses a well-formed sheet into creatable rows', () => {
  const { rows, errors } = parseConnectionImport(
    [HEADER,
     'orders-primary,mysql,prod,10.20.3.12:3306,strict,orders,app_ro,s3cret',
     'analytics,postgres,dev,10.20.3.13:5432,audit-only,analytics,ro,pw',
    ].join('\n'))

  expect(errors).toEqual([])
  expect(rows).toHaveLength(2)
  expect(rows[0]).toMatchObject({
    name: 'orders-primary', engine: 'mysql', env: 'prod',
    host: '10.20.3.12:3306', policy: 'strict', database: 'orders',
    username: 'app_ro', password: 's3cret',
  })
})

// Columns are addressed BY NAME, not by position: a sheet exported from
// elsewhere will not have them in our order, and silently mapping host into env
// would create instances pointing at nonsense.
test('column order does not matter', () => {
  const { rows, errors } = parseConnectionImport(
    ['env,name,host,engine',
     'dev,probe,127.0.0.1:3306,mysql',
    ].join('\n'))
  expect(errors).toEqual([])
  expect(rows[0]).toMatchObject({ name: 'probe', env: 'dev', host: '127.0.0.1:3306', engine: 'mysql' })
})

// env decides which capability levels and dictionary rules apply. An unknown one
// is not a label — the backend refuses it, and a row that would be rejected must
// be caught here rather than after half the sheet has been created.
test('an unknown environment is rejected with its line number', () => {
  const { rows, errors } = parseConnectionImport(
    [HEADER,
     'good,mysql,prod,h:3306,strict,d,u,p',
     'bad,mysql,uat,h:3306,strict,d,u,p',
    ].join('\n'))
  expect(rows).toHaveLength(1)
  expect(errors).toHaveLength(1)
  expect(errors[0].line).toBe(3) // header is line 1
  expect(errors[0].message).toContain('uat')
})

test('an unknown gateway policy is rejected', () => {
  const { errors } = parseConnectionImport([HEADER, 'x,mysql,dev,h:3306,wide-open,d,u,p'].join('\n'))
  expect(errors).toHaveLength(1)
  expect(errors[0].message).toContain('wide-open')
})

test('the required columns must be present', () => {
  const { rows, errors } = parseConnectionImport(['name,engine', 'x,mysql'].join('\n'))
  expect(rows).toEqual([])
  expect(errors).toHaveLength(1)
  expect(errors[0].message).toMatch(/env|host/)
})

test('rows missing a required value are reported, not silently created', () => {
  const { rows, errors } = parseConnectionImport(
    [HEADER,
     ',mysql,dev,h:3306,strict,d,u,p',      // no name
     'x,mysql,dev,,strict,d,u,p',           // no host
    ].join('\n'))
  expect(rows).toEqual([])
  expect(errors).toHaveLength(2)
})

// Quoted fields are normal in exported sheets; a password may legitimately
// contain a comma.
test('quoted fields keep their commas', () => {
  const { rows, errors } = parseConnectionImport(
    [HEADER, 'x,mysql,dev,h:3306,strict,d,u,"pa,ss""word"'].join('\n'))
  expect(errors).toEqual([])
  expect(rows[0].password).toBe('pa,ss"word')
})

test('blank lines and a trailing newline are ignored', () => {
  const { rows, errors } = parseConnectionImport(
    [HEADER, 'a,mysql,dev,h:3306,strict,d,u,p', '', '   ', ''].join('\n'))
  expect(errors).toEqual([])
  expect(rows).toHaveLength(1)
})

test('empty input yields nothing rather than an error storm', () => {
  expect(parseConnectionImport('')).toEqual({ rows: [], errors: [] })
  expect(parseConnectionImport('   \n  ')).toEqual({ rows: [], errors: [] })
})

// The template offered in the UI must itself import cleanly — otherwise the
// first thing a user tries fails.
test('the bundled template parses without errors', () => {
  const { rows, errors } = parseConnectionImport(IMPORT_TEMPLATE)
  expect(errors).toEqual([])
  expect(rows.length).toBeGreaterThan(0)
})

// A row has to remember which line it came from: when creation fails server-side
// (a duplicate name, an unreachable host), the report has to point at the line in
// the user's sheet. Without it the failure can only name the instance, which is
// exactly the field a typo may have mangled.
test('each parsed row remembers its source line', () => {
  const { rows } = parseConnectionImport(
    [HEADER,
     'a,mysql,dev,h:3306,strict,d,u,p',
     '',
     'b,mysql,dev,h:3306,strict,d,u,p',
    ].join('\n'))
  expect(rows.map((r) => r.line)).toEqual([2, 4])
})
