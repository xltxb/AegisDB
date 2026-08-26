// metaCommand — translate psql/MySQL/Oracle client meta-commands (\dt, \d, \l …)
// into a query the driver can run.
//
// Two things make this worth its own module. First, the argument is interpolated
// into the query as a string literal, so `ident()` is a security control, not
// formatting. Second, some of these commands answer a question that "zero rows"
// cannot express: describing a relation that does not exist returns no rows, and
// a bare empty grid is indistinguishable from "the relation exists but has no
// columns you can see" — real psql reports `Did not find any relation named "…"`.
// So a translation carries a NoticeRef for that case — an i18n id, never text.

/** A message to display, as an i18n id plus its parameters. This module holds no
 *  human-language text: it cannot know the active locale, and returning a literal
 *  made the terminal print Chinese after the UI was switched to English. */
export interface NoticeRef {
  id: string
  params?: Record<string, string>
}

export interface MetaTranslation {
  /** The query to execute (still subject to the gateway's normal judgement). */
  sql: string
  /** What an EMPTY result means for this command, if it means anything. Absent for
   *  listing commands, where "nothing" is a legitimate answer. */
  emptyNotice?: NoticeRef
}

/** ident keeps only characters valid in an identifier. The result is embedded in
 *  a SQL string literal, so quotes, semicolons and backslashes must not survive. */
const ident = (s: string) => s.replace(/["'`]/g, '').replace(/[^A-Za-z0-9_$.]/g, '')

const notFound = (name: string): NoticeRef => ({ id: 'termRelationNotFound', params: { name } })

export function translateMetaSql(cmd: string, engine: string): MetaTranslation | null {
  const m = cmd.trim().match(/^\\([a-z]+)\+?\s*(.*)$/i)
  if (!m) return null
  const verb = m[1].toLowerCase()
  const arg = m[2].trim().replace(/;$/, '')
  const isPG = /postgre|dws|gauss/i.test(engine)
  const isOra = /oracle/i.test(engine)
  const q = (sql: string): MetaTranslation => ({ sql })

  if (isPG) {
    const notSys = `NOT IN ('pg_catalog','information_schema')`
    switch (verb) {
      case 'l': case 'list': return q(`SELECT datname AS "Name" FROM pg_database WHERE datistemplate=false ORDER BY 1`)
      case 'dn': return q(`SELECT schema_name AS "Name" FROM information_schema.schemata WHERE schema_name ${notSys} ORDER BY 1`)
      case 'dt': return q(`SELECT table_schema AS "Schema", table_name AS "Name" FROM information_schema.tables WHERE table_type='BASE TABLE' AND table_schema ${notSys} ORDER BY 1,2`)
      case 'dv': return q(`SELECT table_schema AS "Schema", table_name AS "Name" FROM information_schema.views WHERE table_schema ${notSys} ORDER BY 1,2`)
      case 'di': return q(`SELECT schemaname AS "Schema", indexname AS "Name", tablename AS "Table" FROM pg_indexes WHERE schemaname ${notSys} ORDER BY 1,2`)
      case 'ds': return q(`SELECT sequence_schema AS "Schema", sequence_name AS "Name" FROM information_schema.sequences WHERE sequence_schema ${notSys} ORDER BY 1,2`)
      case 'df': return q(`SELECT routine_schema AS "Schema", routine_name AS "Name", data_type AS "Result" FROM information_schema.routines WHERE routine_schema ${notSys} ORDER BY 1,2`)
      case 'du': case 'dg': return q(`SELECT rolname AS "Role", rolsuper AS "Super", rolcanlogin AS "Login" FROM pg_roles ORDER BY 1`)
      case 'dp': case 'z': return q(`SELECT table_schema AS "Schema", table_name AS "Name", grantee AS "Grantee", privilege_type AS "Privilege" FROM information_schema.role_table_grants WHERE table_schema ${notSys} ORDER BY 1,2`)
      case 'conninfo': return q(`SELECT current_database() AS "Database", current_user AS "User", version() AS "Version"`)
      case 'd': {
        if (!arg) return q(`SELECT table_schema AS "Schema", table_name AS "Name" FROM information_schema.tables WHERE table_schema ${notSys} ORDER BY 1,2`)
        const parts = ident(arg).split('.')
        const tbl = parts.pop() || ''
        const sch = parts.length ? ` AND table_schema='${parts[0]}'` : ''
        return {
          sql: `SELECT column_name AS "Column", data_type AS "Type", is_nullable AS "Nullable", column_default AS "Default" FROM information_schema.columns WHERE table_name='${tbl}'${sch} ORDER BY ordinal_position`,
          emptyNotice: notFound(arg),
        }
      }
    }
    return null
  }

  if (isOra) {
    const notSys = `NOT IN ('SYS','SYSTEM','OUTLN','XDB','MDSYS','CTXSYS','DBSNMP','WMSYS','APPQOSSYS')`
    switch (verb) {
      case 'l': case 'list': case 'dn': return q(`SELECT username AS "Name" FROM all_users ORDER BY 1`)
      case 'dt': return q(`SELECT owner AS "Owner", table_name AS "Name" FROM all_tables WHERE owner ${notSys} ORDER BY 1,2`)
      case 'dv': return q(`SELECT owner AS "Owner", view_name AS "Name" FROM all_views WHERE owner ${notSys} ORDER BY 1,2`)
      case 'di': return q(`SELECT owner AS "Owner", index_name AS "Name", table_name AS "Table" FROM all_indexes WHERE owner ${notSys} ORDER BY 1,2`)
      case 'ds': return q(`SELECT sequence_owner AS "Owner", sequence_name AS "Name" FROM all_sequences WHERE sequence_owner ${notSys} ORDER BY 1,2`)
      case 'du': return q(`SELECT username AS "User", account_status AS "Status" FROM all_users ORDER BY 1`)
      case 'conninfo': return q(`SELECT SYS_CONTEXT('USERENV','DB_NAME') AS "Database", USER AS "User" FROM DUAL`)
      case 'd':
        if (!arg) return q(`SELECT owner AS "Owner", table_name AS "Name" FROM all_tables WHERE owner ${notSys} ORDER BY 1,2`)
        // 与 DESC 同一个描述实现:两种拼法回答同一个问题,不该给出两种答案。
        return describeSql(engine, arg)
    }
    return null
  }

  // MySQL / TiDB — SHOW COLUMNS / SHOW INDEX raise a proper error for a missing
  // table, so those need no notice of their own.
  switch (verb) {
    case 'l': case 'list': case 'dn': return q('SHOW DATABASES')
    case 'dt': return q(`SHOW FULL TABLES WHERE Table_type='BASE TABLE'`)
    case 'dv': return q(`SHOW FULL TABLES WHERE Table_type='VIEW'`)
    case 'du': case 'dg': return q('SELECT User, Host FROM mysql.user ORDER BY 1,2')
    case 'conninfo': return q('SELECT DATABASE() AS `Database`, CURRENT_USER() AS `User`, VERSION() AS `Version`')
    case 'di': return arg ? q(`SHOW INDEX FROM \`${ident(arg)}\``) : null
    case 'd': return arg ? q(`SHOW COLUMNS FROM \`${ident(arg)}\``) : q('SHOW TABLES')
  }
  return null
}

/** MySQL 家族把 DESC 当作合法 SQL,其余引擎不认。 */
const isMySQLFamily = (engine: string) => /mysql|tidb|mariadb|polardb/i.test(engine)

/**
 * translateDescribe — DESC / DESCRIBE。
 *
 * 它是 SQL*Plus 的**客户端命令**,不是 SQL:原样发给 Oracle 服务端只会得到
 * ORA-00900 invalid SQL statement。而它是每个 DBA 的肌肉记忆,报个错了事等于让
 * 人改习惯去迁就工具。这里把它翻成目录查询 —— 翻出来的 SQL 照常经过网关的三层
 * 判定与审计,和手敲那条查询没有任何区别。
 *
 * MySQL 家族返回 null:那里 DESC 本来就是合法语法,不去修一个没坏的东西。
 * 认不出的写法也返回 null —— 原样交给服务端拒绝,好过在这里猜。
 */
export function translateDescribe(stmt: string, engine: string): MetaTranslation | null {
  if (isMySQLFamily(engine)) return null
  // 必须是整条语句就是 `desc <对象>`(可带结尾分号)。ORDER BY … DESC 里的 DESC
  // 不在句首,拦错了会把一条正常查询变成目录查询。
  const m = stmt.trim().match(/^desc(?:ribe)?\s+([^\s;]+)\s*;?\s*$/i)
  if (!m) return null
  return describeSql(engine, m[1])
}

/**
 * describeSql builds the "what columns does this object have" query.
 *
 * Oracle 这条比看上去要绕,两处都是被真实用法逼出来的:
 *
 *  - **同义词**:v$session 是指向 SYS.V_$SESSION 的公共同义词,ALL_TAB_COLUMNS
 *    里根本没有叫 V$SESSION 的行。只按名字直查会返回零行 —— 用户看到一张空表格,
 *    以为这个视图没有列。所以要连 ALL_SYNONYMS 一起解。
 *  - **长度与精度**:SQL*Plus 的 DESCRIBE 给的是 VARCHAR2(30)。只说 VARCHAR2
 *    而不说多长,等于没描述。
 *
 * 不取 data_default:它在 ALL_TAB_COLUMNS 里是 LONG 类型,取它会给驱动添麻烦,
 * 而 SQL*Plus 的 DESCRIBE 本来也不显示默认值。
 */
function describeSql(engine: string, arg: string): MetaTranslation | null {
  const clean = ident(arg)
  if (!clean) return null
  const parts = clean.split('.').filter(Boolean)
  const name = parts.pop() || ''
  const owner = parts.pop() || ''
  if (!name) return null

  if (/oracle/i.test(engine)) {
    const NAME = name.toUpperCase()
    const OWNER = owner.toUpperCase()
    const ownerCol = OWNER ? ` AND owner = '${OWNER}'` : ''
    const synOwner = OWNER ? ` AND table_owner = '${OWNER}'` : ''
    const typeExpr =
      `data_type || CASE ` +
      `WHEN data_type IN ('VARCHAR2','NVARCHAR2','CHAR','NCHAR','RAW') THEN '(' || char_length || ')' ` +
      `WHEN data_type = 'NUMBER' AND data_precision IS NOT NULL THEN '(' || data_precision || ` +
      `CASE WHEN NVL(data_scale,0) > 0 THEN ',' || data_scale ELSE '' END || ')' ` +
      `ELSE '' END`
    return {
      sql:
        `SELECT column_name AS "Column", ${typeExpr} AS "Type", nullable AS "Nullable" ` +
        `FROM all_tab_columns WHERE (owner, table_name) IN (` +
        `SELECT owner, table_name FROM all_tab_columns WHERE table_name = '${NAME}'${ownerCol} ` +
        `UNION SELECT table_owner, table_name FROM all_synonyms ` +
        `WHERE synonym_name = '${NAME}'${synOwner} AND owner IN ('PUBLIC', USER)` +
        `) ORDER BY column_id`,
      emptyNotice: notFound(arg),
    }
  }

  if (/postgre|dws|gauss/i.test(engine)) {
    const sch = owner ? ` AND table_schema = '${owner}'` : ''
    return {
      sql:
        `SELECT column_name AS "Column", data_type AS "Type", is_nullable AS "Nullable", ` +
        `column_default AS "Default" FROM information_schema.columns ` +
        `WHERE table_name = '${name}'${sch} ORDER BY ordinal_position`,
      emptyNotice: notFound(arg),
    }
  }

  if (/sqlite/i.test(engine)) {
    // pragma_table_info(),不是 PRAGMA 语句:前者是 SELECT,会被判定层当作读;
    // 后者的动词认不出来,会被当成写而要求审批 —— 描述一张表不该走审批。
    return {
      sql: `SELECT name AS "Column", type AS "Type", CASE "notnull" WHEN 1 THEN 'N' ELSE 'Y' END AS "Nullable" FROM pragma_table_info('${name}')`,
      emptyNotice: notFound(arg),
    }
  }
  return null
}
