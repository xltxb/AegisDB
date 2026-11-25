// SQL*Plus 的 SET 命令(Oracle)。
//
// `SET SERVEROUTPUT ON`、`SET LINESIZE 200`、`SET DEFINE OFF` 都不是 SQL,是说给 SQL*Plus 听的:
// 发给数据库只会得到 ORA-00922。终端在本地处理它们,和 `USE 库名` 一样不下发。
//
// 其中只有 SERVEROUTPUT 有实际效果:开着时每次执行把开关带给网关,网关在同一个会话上打开
// DBMS_OUTPUT、执行、读回输出。其余是显示设置,终端不用,明确说"已忽略"。
//
// Oracle 自己的 SET 语句只有 SET TRANSACTION / SET ROLE / SET CONSTRAINT(S),它们是 SQL,照常下发。
// 规则与后端 gateway.SQLPlusSet 一致。

import { sqlPlusLineComplete } from './sqlplus'

export interface SqlPlusSet {
  /** 选项名,大写(SERVEROUTPUT、LINESIZE…) */
  option: string
  /** 选项后面的部分(ON / OFF / 200 …),原样 */
  value: string
}

const ORACLE_SQL_SET = new Set(['TRANSACTION', 'ROLE', 'CONSTRAINT', 'CONSTRAINTS'])

const isOracle = (engine: string) => /oracle/i.test(engine)

// 去掉开头的注释(-- 行注释与 /* */ 块注释)
function stripLeadingComments(s: string): string {
  let t = s.trim()
  for (;;) {
    if (t.startsWith('--')) { const i = t.indexOf('\n'); t = i < 0 ? '' : t.slice(i + 1).trim(); continue }
    if (t.startsWith('/*')) { const i = t.indexOf('*/'); t = i < 0 ? '' : t.slice(i + 2).trim(); continue }
    return t
  }
}

export function parseSqlPlusSet(raw: string, engine: string): SqlPlusSet | null {
  if (!isOracle(engine)) return null
  // 只认单行。一段粘贴进来的脚本以 SET SERVEROUTPUT ON 开头时,从前整段被当成一条 SET 吞掉,
  // 后面的 PL/SQL 块根本没发出去、也没有任何提示(2026-10 sqlplus-compat 时发现)。多行的整段
  // 交给服务端逐条处理(它认得 SET,见 gateway.SQLPlusServerOutput)。
  const one = stripLeadingComments(raw)
  if (one.includes('\n')) return null
  const m = one.match(/^SET\s+([A-Za-z_]+)\b\s*(.*?)\s*;?\s*$/is)
  if (!m) return null
  const option = m[1].toUpperCase()
  if (ORACLE_SQL_SET.has(option)) return null
  return { option, value: m[2].trim() }
}

/** SET SERVEROUTPUT 的开关值:ON → true,OFF → false,其它(写错了)→ null。 */
export function serverOutputValue(set: SqlPlusSet): boolean | null {
  if (set.option !== 'SERVEROUTPUT' && set.option !== 'SERVEROUT') return null
  const w = set.value.split(/\s+/)[0]?.toUpperCase()
  if (w === 'ON') return true
  if (w === 'OFF') return false
  return null
}

/** SHOW SERVEROUTPUT —— 问的是终端自己的开关,本地回答。 */
export function isShowServerOutput(raw: string, engine: string): boolean {
  return isOracle(engine) && /^SHOW\s+SERVEROUT(PUT)?\s*;?\s*$/i.test(stripLeadingComments(raw))
}

/**
 * 一段脚本(粘贴的整段)里最后一次 SET SERVEROUTPUT ON / OFF 的值,没有则 null。
 * 网关在这一批里按顺序生效;批跑完之后,终端也要记住这个设置 —— 与 SQL*Plus 会话一致。
 */
export function lastServerOutputIn(script: string, engine: string): boolean | null {
  let last: boolean | null = null
  for (const line of script.split(/\r?\n/)) {
    const set = parseSqlPlusSet(line, engine)
    if (!set) continue
    const v = serverOutputValue(set)
    if (v !== null) last = v
  }
  return last
}

// ---------------------------------------------------------------- PL/SQL 块与语句结束

const PLSQL_HEAD = /^(?:begin|declare)\b|^create\s+(?:or\s+replace\s+)?(?:(?:non)?editionable\s+)?(?:procedure|function|package|trigger|type)\b/i

/** Oracle 的 PL/SQL 块(匿名块、存储程序)。它们必须以 END; 结尾 —— 发送前不能去掉那个分号。 */
export function isOraclePlsql(stmt: string, engine: string): boolean {
  return isOracle(engine) && PLSQL_HEAD.test(stripLeadingComments(stmt))
}

// 块结束:最外层的 END; 或 END 名字;。END LOOP / END IF / END CASE 是块里面的结构,不算。
const PLSQL_END = /\bend(?:\s+(?!loop\b|if\b|case\b)[\w$#"]+)?\s*;\s*$/i

/**
 * 行编辑器问"这一行敲完,语句结束了没有"时的 Oracle 规则(SQL*Plus 的习惯):
 *   - SET / SHOW SERVEROUTPUT / EXEC 以换行结束,不等分号;
 *   - PL/SQL 块要等到最外层的 END; 或单独一行的 /,块里每一行的分号都不算。
 * 返回 undefined 表示"按默认规则"(以分号结束)。
 */
export function oracleStatementComplete(full: string, engine: string): boolean | undefined {
  if (!isOracle(engine)) return undefined
  const s = full.trim()
  if (parseSqlPlusSet(s, engine) || isShowServerOutput(s, engine) || sqlPlusLineComplete(s, engine)) return true
  if (/^exec(?:ute)?\s+\S/i.test(stripLeadingComments(s)) && !s.includes('\n')) return true
  if (isOraclePlsql(s, engine)) {
    const lines = s.split(/\r?\n/)
    if (lines[lines.length - 1].trim() === '/') return true
    return PLSQL_END.test(s)
  }
  return undefined
}
