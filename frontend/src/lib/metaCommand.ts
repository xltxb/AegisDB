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
  /** A SECOND section, rendered under the first.
   *
   *  psql 的 \d 不是一张表:它先列列,再列索引(主键、分区、表空间)。只回第一段
   *  就是把"这张表怎么建的索引"整块吞掉 —— 而那恰恰是 DBA 敲 \d 最常要看的东西。
   *
   *  两段是两次查询,因此也是两条审计。这是如实的:确实跑了两条目录查询。 */
  follow?: { titleKey: string; sql: string }
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
        const sch = parts.length ? parts[0] : ''
        // information_schema.columns 的 data_type 只给基础类型名:varchar(128) 会
        // 变成 "character varying",numeric(38,6) 会变成 "numeric"。**长度和精度
        // 丢了就不是"少显示一点",是给错信息** —— 同一个文件里 Oracle 分支早就为
        // 这件事拼过一段 CASE。PG 有现成的 format_type(),一个函数就给出 psql 那一列。
        //
        // 顺带补上 Collation:psql 有这一列,而且只在非默认排序规则时才显示内容。
        const relFilter =
          `c.relname='${tbl}'` + (sch ? ` AND n.nspname='${sch}'` : '')
        return {
          sql:
            `SELECT a.attname AS "Column", ` +
            `pg_catalog.format_type(a.atttypid, a.atttypmod) AS "Type", ` +
            `CASE WHEN cl.collname IS NULL OR cl.collname='default' THEN '' ELSE cl.collname END AS "Collation", ` +
            `CASE WHEN a.attnotnull THEN 'not null' ELSE '' END AS "Nullable", ` +
            `COALESCE(pg_catalog.pg_get_expr(d.adbin, d.adrelid), '') AS "Default" ` +
            `FROM pg_catalog.pg_attribute a ` +
            `JOIN pg_catalog.pg_class c ON c.oid = a.attrelid ` +
            `JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace ` +
            `LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum ` +
            `LEFT JOIN pg_catalog.pg_collation cl ON cl.oid = a.attcollation ` +
            `WHERE ${relFilter} AND a.attnum > 0 AND NOT a.attisdropped ORDER BY a.attnum`,
          emptyNotice: notFound(arg),
          follow: {
            titleKey: 'metaSectionIndexes',
            sql:
              `SELECT i.relname AS "Index", pg_catalog.pg_get_indexdef(x.indexrelid) AS "Definition" ` +
              `FROM pg_catalog.pg_index x ` +
              `JOIN pg_catalog.pg_class i ON i.oid = x.indexrelid ` +
              `JOIN pg_catalog.pg_class c ON c.oid = x.indrelid ` +
              `JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace ` +
              `WHERE ${relFilter} ORDER BY i.relname`,
          },
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
 * oracleDescTarget resolves the name to ONE (owner, table_name), the way SQL*Plus does.
 *
 * 从前是 `(owner, table_name) IN (所有叫这个名字的表 ∪ 同义词指向)`,不带 schema 时不限属主:
 * 同名表在两个 schema 各有一份(COLORGAME_UAT 和它的备份库),每列就出现两次,
 * 按 column_id 排出来正好是 GMTYPE、GMTYPE、GIFT_ID、GIFT_ID……
 *
 * 现在按优先级只取第一个:
 *  1. 当前 schema 下的表/视图 —— SYS_CONTEXT 而不是 USER:终端标签切过 schema
 *     (ALTER SESSION SET CURRENT_SCHEMA),desc 应当看的是切过去的那个;
 *  2. 当前 schema 下的私有同义词;
 *  3. 公共同义词(v$session → SYS.V_$SESSION);
 *  4. 以上都没有、而整个实例里**只有一个**看得见的属主有这张表 —— 只读账号靠授权
 *     读业务库、没建同义词时,从前 desc 能看到列,这里不让它变成"查无此表"。
 *     有两个以上属主就不猜,宁可报找不到也不拼出一张混着两张表的结果。
 * 带 schema(`desc owner.t`)时只查那个 schema:表,或那个 schema 的私有同义词。
 * 后端 oracle_sqlplus.go 的 oracleDescTarget 是同一份解析(脚本里的 DESC 走那条),改一处要改两处。
 */
function oracleDescTarget(OWNER: string, NAME: string): string {
  const own = OWNER ? `'${OWNER}'` : `SYS_CONTEXT('USERENV','CURRENT_SCHEMA')`
  const branches = [
    `SELECT owner, table_name, 1 AS pri FROM all_tab_columns WHERE owner = ${own} AND table_name = '${NAME}' AND ROWNUM = 1`,
    `SELECT table_owner, table_name, 2 FROM all_synonyms WHERE owner = ${own} AND synonym_name = '${NAME}' AND db_link IS NULL`,
  ]
  if (!OWNER) {
    branches.push(
      `SELECT table_owner, table_name, 3 FROM all_synonyms WHERE owner = 'PUBLIC' AND synonym_name = '${NAME}' AND db_link IS NULL`,
      `SELECT MIN(owner), table_name, 4 FROM all_tab_columns WHERE table_name = '${NAME}' GROUP BY table_name HAVING COUNT(DISTINCT owner) = 1`,
    )
  }
  // ORDER BY 不能直接挂在 UNION ALL 上:老版本 Oracle 在内联视图里认不出复合查询的别名
  // (现场报 ORA-00904: "PRI",23ai 不报)。先包成一层视图,再按它的列排 —— 经典的 top-N 写法。
  return `SELECT owner, table_name FROM (SELECT owner, table_name FROM (${branches.join(' UNION ALL ')}) ORDER BY pri) WHERE ROWNUM = 1`
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
    const typeExpr =
      `c.data_type || CASE ` +
      `WHEN c.data_type IN ('VARCHAR2','NVARCHAR2','CHAR','NCHAR','RAW') THEN '(' || c.char_length || ')' ` +
      `WHEN c.data_type = 'NUMBER' AND c.data_precision IS NOT NULL THEN '(' || c.data_precision || ` +
      `CASE WHEN NVL(c.data_scale,0) > 0 THEN ',' || c.data_scale ELSE '' END || ')' ` +
      `ELSE '' END`
    return {
      sql:
        `SELECT c.column_name AS "Column", ${typeExpr} AS "Type", c.nullable AS "Nullable" ` +
        `FROM all_tab_columns c, (${oracleDescTarget(owner.toUpperCase(), name.toUpperCase())}) t ` +
        `WHERE c.owner = t.owner AND c.table_name = t.table_name ORDER BY c.column_id`,
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
