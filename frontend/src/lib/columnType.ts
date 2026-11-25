// 结果列的类型 —— 按返回值推断。
//
// 业务查询的接口只回列名和字符串化的值,不带库里的列类型。表头上那个小标识只是帮人一眼看出
// "这列是数还是字",所以按值猜:一列里非空的值全都像数 → num;全都像日期时间 → date;
// 全是 true/false/0/1 之外的布尔写法 → bool;其余 text。界面上要说明这是推断出来的。
export type ColumnType = 'num' | 'date' | 'bool' | 'text'

const NUM = /^-?\d+(\.\d+)?([eE][-+]?\d+)?$/
const DATE = /^\d{4}-\d{2}-\d{2}([ T]\d{2}:\d{2}(:\d{2}(\.\d+)?)?(Z|[+-]\d{2}:?\d{2})?)?$/
const BOOL = /^(true|false)$/i

export function inferColumnType(rows: string[][], col: number): ColumnType {
  let saw = false
  let num = true
  let date = true
  let bool = true
  for (const r of rows) {
    const v = r[col]
    if (v == null || v === '') continue
    saw = true
    const s = String(v).trim()
    if (num && !NUM.test(s)) num = false
    if (date && !DATE.test(s)) date = false
    if (bool && !BOOL.test(s)) bool = false
    if (!num && !date && !bool) return 'text'
  }
  if (!saw) return 'text'
  if (num) return 'num'
  if (date) return 'date'
  if (bool) return 'bool'
  return 'text'
}
