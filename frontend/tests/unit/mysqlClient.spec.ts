import { test, expect } from '@playwright/test'
import { parsePager, parseClientCommand, mysqlClientComplete, splitPipeline, teeFilename } from '../../src/lib/mysqlClient'

// mysql 客户端命令:pager 用内置过滤器在浏览器里实现,不起 shell。

const MY = 'MySQL 8.0'
const run = (cmd: string, lines: string[]) => {
  const r = parsePager(cmd)
  if (!('pager' in r) || !r.pager) throw new Error('解析失败: ' + JSON.stringify(r))
  return r.pager.run(lines)
}
const errOf = (cmd: string) => {
  const r = parsePager(cmd)
  return 'error' in r ? r.error.id : null
}

// SHOW PROCESSLIST 的样子
const PROC = [
  '+----+------+-----------+---------+------+',
  '| Id | User | Host      | Command | Time |',
  '+----+------+-----------+---------+------+',
  '|  5 | app  | 10.0.0.1  | Sleep   |  120 |',
  '|  6 | app  | 10.0.0.2  | Query   |    3 |',
  '|  7 | etl  | 10.0.0.3  | Sleep   |   40 |',
  '|  8 | root | localhost | Query   |    0 |',
  '+----+------+-----------+---------+------+',
]

test('管道分词:引号内原样,引号外的 | 才是管道', () => {
  expect(splitPipeline(`grep -v "a | b" | wc -l`)).toEqual([['grep', '-v', 'a | b'], ['wc', '-l']])
  expect(splitPipeline(`grep 'it''s'`)).toEqual([['grep', 'its']])
  expect(splitPipeline(`grep ''`)).toEqual([['grep', '']])
  expect(splitPipeline(`grep "unclosed`)).toBeNull()
})

test('grep:过滤、反选、忽略大小写、计数', () => {
  expect(run('grep Sleep', PROC)).toHaveLength(2)
  expect(run('grep -v Sleep', PROC)).toHaveLength(6)
  expect(run('grep -ic sleep', PROC)).toEqual(['2'])
  // 最常见的一句:数非 Sleep 的连接(连表框一起数,和真 mysql 一样)
  expect(run('grep -v Sleep | wc -l', PROC)).toEqual(['6'])
  expect(run('grep -E "Sleep|Query" | grep -c etl', PROC)).toEqual(['1'])
})

test('grep 默认按基本正则:| 是字面量,\\| 才是"或"', () => {
  expect(run('grep "Sleep\\|root"', PROC)).toHaveLength(3)
  expect(run('grep "a|b"', ['a|b', 'a', 'b'])).toEqual(['a|b'])
  expect(run('grep -F "10.0.0.1"', ['10.0.0.1', '10a0b0c1'])).toEqual(['10.0.0.1'])
  expect(run('grep -w app', ['| app |', '| apple |'])).toEqual(['| app |'])
})

test('head / tail / sort / uniq / wc', () => {
  const nums = ['3', '1', '2', '2', '10']
  expect(run('head -2', nums)).toEqual(['3', '1'])
  expect(run('head -n 3', nums)).toEqual(['3', '1', '2'])
  expect(run('tail -n1', nums)).toEqual(['10'])
  expect(run('sort -n', nums)).toEqual(['1', '2', '2', '3', '10'])
  expect(run('sort -rn | head -1', nums)).toEqual(['10'])
  expect(run('sort | uniq -c', ['b', 'a', 'b'])).toEqual(['      1 a', '      2 b'])
  expect(run('sort -u', ['b', 'a', 'b'])).toEqual(['a', 'b'])
  expect(run('wc -l', nums)).toEqual(['5'])
  expect(run('less -S', PROC)).toEqual(PROC) // 分页器本身原样输出
  expect(run('/usr/bin/less -SFX', PROC)).toEqual(PROC)
})

test('认不出的命令、错误的参数、坏正则:说清楚,不猜', () => {
  expect(errOf('awk "{print $1}"')).toBe('pagerUnsupported')
  expect(errOf('md5sum')).toBe('pagerUnsupported')
  expect(errOf('grep')).toBe('pagerBadArgs')
  expect(errOf('grep -z x')).toBe('pagerBadArgs')
  expect(errOf('grep x file.txt')).toBe('pagerBadArgs') // 不读文件
  expect(errOf('grep -E "("')).toBe('pagerBadRegex')
  expect(errOf('head -x')).toBe('pagerBadArgs')
  expect(errOf('grep x |')).toBe('pagerBadPipe')
  expect(parsePager('   ')).toEqual({ pager: null }) // 空 = 回到原样输出
})

test('命令识别:单词写法不分大小写,回车即执行', () => {
  expect(parseClientCommand('pager grep -v Sleep', MY)).toMatchObject({ name: 'pager', arg: 'grep -v Sleep' })
  expect(parseClientCommand('PAGER less -S;', MY)).toMatchObject({ name: 'pager', arg: 'less -S' })
  expect(parseClientCommand('nopager', MY)).toMatchObject({ name: 'nopager' })
  expect(parseClientCommand('tee /tmp/out.log', MY)).toMatchObject({ name: 'tee', arg: '/tmp/out.log' })
  expect(parseClientCommand('notee;', MY)).toMatchObject({ name: 'notee' })
  expect(parseClientCommand('status', MY)).toMatchObject({ name: 'status' })
  expect(parseClientCommand('system ls', MY)).toMatchObject({ name: 'unsupported', word: 'system' })
  expect(mysqlClientComplete('pager less', MY)).toBe(true)
  expect(mysqlClientComplete('SELECT 1', MY)).toBeUndefined()
})

test('命令识别:反斜杠写法区分大小写,不抢 psql 风格的 \\dt / \\c / \\x', () => {
  expect(parseClientCommand('\\P grep x', MY)).toMatchObject({ name: 'pager', arg: 'grep x' })
  expect(parseClientCommand('\\n', MY)).toMatchObject({ name: 'nopager' })
  expect(parseClientCommand('\\T a.log', MY)).toMatchObject({ name: 'tee', arg: 'a.log' })
  expect(parseClientCommand('\\t', MY)).toMatchObject({ name: 'notee' })
  expect(parseClientCommand('\\s', MY)).toMatchObject({ name: 'status' })
  for (const s of ['\\dt', '\\d t_order', '\\c orders', '\\x', '\\l', '\\conninfo', '\\?']) {
    expect(parseClientCommand(s, MY), s).toBeNull()
  }
})

test('不是客户端命令的,一律不接', () => {
  expect(parseClientCommand('help SELECT', MY)).toBeNull() // 服务端的 HELP
  expect(parseClientCommand('status = 1', MY)).toBeNull()
  expect(parseClientCommand('pager less\nSELECT 1', MY)).toBeNull() // 多行
  expect(parseClientCommand('delimiter', MY)).toBeNull()
  expect(parseClientCommand('pager less', 'PostgreSQL 16')).toBeNull() // 只对 MySQL 系
  expect(parseClientCommand('pager less', 'Oracle 19c')).toBeNull()
})

test('tee 的文件名只留名字本身', () => {
  const now = new Date(2026, 8, 30, 9, 5, 7)
  expect(teeFilename('/tmp/../out.log', now)).toBe('out.log')
  expect(teeFilename('C:\\x\\结果.txt', now)).toBe('结果.txt')
  expect(teeFilename('', now)).toBe('vela-tee-20260930-090507.log')
})

test('交给 pager 的是 mysql 样式的 ASCII 表格:grep -v "+" 去掉边框,| wc -l 数出表头加数据行', async () => {
  const { renderTable, buildTable } = await import('../../src/lib/sqlResult')
  const { stripAnsi } = await import('../../src/lib/transcript')
  const { toMysqlAscii } = await import('../../src/lib/mysqlClient')
  const long = 'x'.repeat(200) + 'NEEDLE'
  const lines = renderTable(buildTable(['id', 'note'], [['1', 'a'], ['2', long]]), -1).map((l) => toMysqlAscii(stripAnsi(l)))
  expect(lines[0]).toMatch(/^\+-+\+-+\+$/)
  expect(lines[1]).toMatch(/^\| id +\| note +\|$/)
  expect(run('grep -v "+" | wc -l', lines)).toEqual(['3'])
  expect(run('grep -c NEEDLE', lines)).toEqual(['1']) // 长值不截断,grep 找得到
})
