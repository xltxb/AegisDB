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
        return {
          sql: `SELECT column_name AS "Column", data_type AS "Type", nullable AS "Nullable" FROM all_tab_columns WHERE table_name=UPPER('${ident(arg)}') ORDER BY column_id`,
          emptyNotice: notFound(arg),
        }
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
