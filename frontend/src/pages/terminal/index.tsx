import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { Database, ShieldAlert, RefreshCw, Plug, TriangleAlert } from 'lucide-react'
import clsx from 'clsx'
import { connectionsQueryOptions } from '@/api/modules/connections'
import { terminalApi } from '@/api/modules/terminal'
import { translateMetaSql, translateDescribe } from '@/lib/metaCommand'
import { useTerminalSession, type WsMessage } from '@/hooks/useTerminalSession'
import { Badge } from '@/components/common/Badge'
import { Button } from '@/components/common/Button'
import { Modal } from '@/components/common/Modal'
import { CODE_INTERCEPTED, CODE_MFA_REQUIRED } from '@/api/codes'
import { useEnvTier } from '@/hooks/useEnvTier'
import type { Connection } from '@/types'
import '@xterm/xterm/css/xterm.css'

const ANSI = { red: '\x1b[31m', yellow: '\x1b[33m', green: '\x1b[32m', dim: '\x1b[90m', off: '\x1b[0m' }

export default function TerminalPage() {
  const { t } = useTranslation()
  const { data: conns } = useQuery(connectionsQueryOptions())
  const envtier = useEnvTier()

  const [connId, setConnId] = useState(0)
  const [database, setDatabase] = useState('')
  const [intercept, setIntercept] = useState<{ sql: string; rule: string; apNo?: string } | null>(null)
  const [reason, setReason] = useState('')
  const pendingFollow = useRef<string | null>(null)

  const list = (conns ?? []) as Connection[]
  const conn = list.find((c) => c.id === connId) ?? null
  const tier = conn ? envtier.tierOf(conn.env) : undefined
  const danger = !!tier?.dangerBanner

  const session = useTerminalSession({
    /**
     * 提交前先过一次风险预检。
     *
     * 命中高危就**不下发**,而是开提交卡让人填原因 —— 这正是"拦截"的意思:
     * 命令没有跑,也不会因为界面上少点一步就偷偷跑掉。
     */
    onSubmit: async (sql) => {
      if (!connId) {
        write(`${ANSI.yellow}· ${t('termPickConn')}${ANSI.off}`)
        session.editor.current?.resume()
        return true
      }
      /*
       * psql / mysql 风格的元命令在这里翻译成目录查询,**翻译之后照常送进判定**。
       *
       * 顺序是刻意的:判定看到的是翻译后真正要跑的那条 SQL,而不是 `\dt` 这三个
       * 字符。反过来做(先判 `\dt` 再翻译)等于给自己开了一条不过闸的路 —— 任何
       * 能被翻译成 DDL 的写法都会绕过字典。
       *
       * `\d 表` 会产出两段(先列列、再列索引),那是两次查询,因此也是两条审计。
       * 如实记,不合并。
       */
      const engine = conn?.engine ?? ''
      const meta = translateMetaSql(sql, engine) ?? translateDescribe(sql, engine)
      const toRun = meta?.sql ?? sql
      const follow = meta?.follow?.sql

      try {
        const v = await terminalApi.riskCheck(connId, toRun, database)
        if (v.action === 'approve' || v.action === 'deny') {
          setIntercept({ sql, rule: v.matchedRule || '' })
          write(`${ANSI.red}· ${t('termIntercepted')}${ANSI.off}`)
          session.editor.current?.resume()
          return true
        }
      } catch {
        // 预检失败不等于放行:说出来,并把提示符还回去。
        write(`${ANSI.yellow}· ${t('termPrecheckFailed')}${ANSI.off}`)
        session.editor.current?.resume()
        return true
      }
      if (!session.send({ type: 'exec', connectionId: connId, sql: toRun, database })) {
        write(`${ANSI.yellow}· ${t('termOffline')}${ANSI.off}`)
        session.editor.current?.resume()
        return true
      }
      // 第二段(\d 的索引部分)等第一段回来之后再发,免得两条应答交织在一起。
      if (follow) pendingFollow.current = follow
      return true
    },
    onMessage: (m: WsMessage) => {
      const term = session.term.current
      if (!term) return
      if (m.type === 'output') {
        const rows = Number(m.rows ?? 0)
        const ms = Number(m.ms ?? 0)
        // 服务端已经给了一行人读的摘要时,不再自己重复行数 —— 否则是
        // 「+ 1 rows · 1 行 · 0ms」这种两遍。只补它没说的耗时。
        const text = typeof m.text === 'string' ? m.text : ''
        if (text) {
          term.writeln(text)
          term.writeln(`${ANSI.dim}· ${ms}ms${ANSI.off}`)
        } else {
          term.writeln(`${ANSI.dim}· ${rows} ${t('termRows')} · ${ms}ms${ANSI.off}`)
        }
      } else if (m.type === 'intercept') {
        setIntercept({ sql: String(m.sql ?? ''), rule: String(m.rule ?? ''), apNo: String(m.approvalNo ?? '') })
        term.writeln(`${ANSI.red}· ${t('termIntercepted')} ${m.approvalNo ?? ''}${ANSI.off}`)
      } else if (m.type === 'error') {
        term.writeln(`${ANSI.red}· ${String(m.message ?? t('termExecFail'))}${ANSI.off}`)
      } else if (m.type === 'session_revoked') {
        term.writeln(`${ANSI.red}· ${t('termRevoked')}${ANSI.off}`)
      }
      const next = pendingFollow.current
      pendingFollow.current = null
      if (next && m.type === 'output' && connId) {
        session.send({ type: 'exec', connectionId: connId, sql: next, database })
        return // 保持 busy,等第二段的应答
      }
      session.editor.current?.resume()
    },
    onCancel: () => {
      if (session.send({ type: 'cancel' })) write(`${ANSI.yellow}· ${t('termCancelSent')}${ANSI.off}`)
    },
  })

  function write(line: string) {
    session.term.current?.writeln(line)
  }

  async function submitApproval() {
    if (!intercept || !connId) return
    try {
      const r = await terminalApi.exec(connId, intercept.sql, reason, '', database)
      const code = (r as { code?: number })?.code
      if (code === CODE_INTERCEPTED) write(`${ANSI.yellow}· ${t('termTicketRaised')}${ANSI.off}`)
      else if (code === CODE_MFA_REQUIRED) write(`${ANSI.yellow}· ${t('termNeedMfa')}${ANSI.off}`)
    } catch (e) {
      write(`${ANSI.red}· ${(e as Error).message}${ANSI.off}`)
    }
    setIntercept(null)
    setReason('')
  }

  return (
    <div className="term-grid">
      <aside className="term-tree">
        <div className="term-panel-head"><Database size={14} />{t('termInstances')}</div>
        <div className="term-tree-list">
          {list.map((c) => (
            <button
              key={c.id}
              className={clsx('term-conn', c.id === connId && 'on')}
              onClick={() => { setConnId(c.id); setDatabase(c.database || '') }}
            >
              <span className="term-conn-name">{c.env}-{c.name}</span>
              <span className="term-conn-engine">{c.engine}</span>
            </button>
          ))}
        </div>
      </aside>

      <section className="term-main">
        <div className="term-tabs">
          <span className={clsx('term-dot', session.status)} />
          <span className="term-target">{conn ? `${conn.env}-${conn.name}` : t('termNoConn')}</span>
          {conn && (
            <input
              className="term-db"
              value={database}
              placeholder={t('termDatabase')}
              onChange={(e) => setDatabase(e.target.value)}
            />
          )}
          <div className="grow" />
          <Button variant="ghost" title={t('termReconnect')} onClick={() => session.reconnect()}>
            <RefreshCw size={14} />
          </Button>
        </div>

        {danger && (
          <div className="term-danger"><TriangleAlert size={14} />{t('termDangerBanner', { name: conn?.name })}</div>
        )}

        <div className="term-host" ref={session.hostRef} />

        <div className="term-status">
          <Plug size={12} />
          {t(`ws_${session.status}`)}
          <span className="grow" />
          <span className="dim">UTF-8 · {t('termAuditOn')}</span>
        </div>
      </section>

      <aside className="term-insp">
        <div className="term-panel-head"><ShieldAlert size={14} />{t('termInspector')}</div>
        <dl className="term-kv">
          <div><dt>{t('termTargetInst')}</dt><dd>{conn ? `${conn.env}-${conn.name}` : '—'}</dd></div>
          <div><dt>{t('termDatabase')}</dt><dd>{database || '—'}</dd></div>
          <div><dt>{t('termTier')}</dt><dd>{tier ? tier.displayName : '—'}</dd></div>
          <div><dt>{t('termPolicy')}</dt><dd>{conn?.policy ?? '—'}</dd></div>
        </dl>
        {danger && <Badge tone="danger">{t('termDangerTier')}</Badge>}
      </aside>

      <Modal
        open={!!intercept}
        title={t('termInterceptTitle')}
        sub={t('termInterceptSub')}
        onClose={() => { setIntercept(null); setReason('') }}
        footer={
          <>
            <Button variant="ghost" onClick={() => { setIntercept(null); setReason('') }}>{t('cancel')}</Button>
            <Button variant="primary" disabled={!reason.trim()} onClick={submitApproval}>
              {t('termSubmitApproval')}
            </Button>
          </>
        }
      >
        <pre className="cmd">{intercept?.sql}</pre>
        {intercept?.rule && <div className="notice danger"><ShieldAlert size={14} />{intercept.rule}</div>}
        <div className="fld">
          <label>{t('termReason')}</label>
          <textarea value={reason} onChange={(e) => setReason(e.target.value)} />
        </div>
      </Modal>
    </div>
  )
}
