import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import clsx from 'clsx'
import { FileCode2, ShieldAlert, CircleCheck, Loader2, TriangleAlert } from 'lucide-react'
import { Modal } from '@/components/common/Modal'
import { Button } from '@/components/common/Button'
import { Badge } from '@/components/common/Badge'
import { Loading, ErrorState } from '@/components/common/States'
import { connectionsQueryOptions } from '@/api/modules/connections'
import { useDbOptions } from '@/hooks/useDbOptions'
import { terminalApi } from '@/api/modules/terminal'
import { useUIStore } from '@/stores/ui'
import { CODE_OK, CODE_INTERCEPTED, CODE_MFA_REQUIRED } from '@/api/codes'
import type { Connection, ScannedStmt, ScriptScanResp } from '@/types'

/** 下发有界超时:遮罩必然散去,不会把人永久关在弹窗里。 */
const DISPATCH_TIMEOUT_MS = 120_000

interface Props {
  open: boolean
  /** 文件名。扫描还在路上时标题也要有东西可显示。 */
  filename: string
  /** 扫描结果;为空且没有 error 表示还在扫。 */
  scan: ScriptScanResp | null
  /** 扫描失败的原因。与"还在扫"必须分开说 —— 两者在界面上都是一个圈。 */
  error?: Error
  /** 上传后的脚本引用。执行时由网关重新读文件、校验哈希,前端不再回传正文。 */
  uploadId: number
  onClose: () => void
  /** 下发成功后回调,让调用方刷新自己的列表。 */
  onDispatched?: () => void
}

/**
 * 脚本扫描报告 + 下发。
 *
 * 这个弹窗最要紧的一条约束在遮罩上:**下发期间整块遮罩盖住,连关闭都不行**。
 * 按钮变灰只挡得住「再点一次执行」,挡不住「关掉重新扫一遍」—— 而那一下会让
 * 同一个脚本跑两遍。所以下发中不接受任何退出,只靠 120 秒的有界超时收场。
 */
export default function ScriptScanModal({ open, filename, scan, error, uploadId, onClose, onDispatched }: Props) {
  const { t } = useTranslation()
  const notify = useUIStore((s) => s.notify)
  const { data: conns } = useQuery(connectionsQueryOptions())
  const list = (conns ?? []) as Connection[]

  const [connId, setConnId] = useState(0)
  const [database, setDatabase] = useState('')
  const [busy, setBusy] = useState(false)
  const [submitted, setSubmitted] = useState('')

  const conn = list.find((c) => c.id === connId)
  const dbs = useDbOptions(conn)

  function reset() {
    setBusy(false)
    setSubmitted('')
  }

  async function dispatch() {
    if (!scan || !connId) return
    setBusy(true)
    try {
      const env = (await Promise.race([
        terminalApi.scriptExecute('', scan.filename, connId, '', uploadId, database),
        new Promise((_, rej) => setTimeout(() => rej(new Error(t('scanTimeout'))), DISPATCH_TIMEOUT_MS)),
      ])) as { code: number; msg: string; data?: { apNo?: string } }

      if (env.code === CODE_INTERCEPTED) {
        // 检出高危 → 整脚本转审批。弹窗不关,换成"已提交"的样子:人需要看到单号。
        setSubmitted(env.data?.apNo || t('scanSubmitted'))
      } else if (env.code === CODE_MFA_REQUIRED) {
        notify(t('termNeedMfa'), 'error')
      } else if (env.code === CODE_OK) {
        notify(t('scanDispatched'), 'ok')
        onDispatched?.()
        onClose()
        reset()
      } else {
        notify(env.msg, 'error')
      }
    } catch (e) {
      notify((e as Error).message, 'error')
    } finally {
      setBusy(false)
    }
  }

  if (!open) return null

  return (
    <Modal
      open={open}
      width={680}
      title={<span className="scan-title"><FileCode2 size={16} />{filename}</span>}
      sub={scan ? t('scanSub', { n: scan.total }) : error ? t('scanFailed') : t('scanScanning')}
      // 下发中不允许关闭 —— 见组件头部的说明。
      onClose={() => { if (!busy) { onClose(); reset() } }}
      footer={
        <>
          <Button variant="ghost" disabled={busy} onClick={() => { onClose(); reset() }}>
            {t('close')}
          </Button>
          <div className="fld" style={{ flex: 1, margin: 0 }}>
            <select value={connId} onChange={(e) => { setConnId(Number(e.target.value)); setDatabase('') }}>
              <option value={0}>{t('scanPickConn')}</option>
              {list.map((c) => <option key={c.id} value={c.id}>{c.env}-{c.name}</option>)}
            </select>
          </div>
          <div className="fld" style={{ width: 180, margin: 0 }}>
            {/* 库名必填:一台实例底下往往混着不同业务的库。 */}
            <select value={database} onChange={(e) => setDatabase(e.target.value)} disabled={!connId}>
              <option value="">{t('scanPickDb')}</option>
              {dbs.options.map((d) => <option key={d} value={d}>{d}</option>)}
            </select>
          </div>
          <Button
            variant={scan?.hasRisky ? 'secondary' : 'primary'}
            disabled={busy || !scan || !connId || !database || !!submitted}
            onClick={dispatch}
          >
            {busy && <Loader2 size={14} className="spin" />}
            {scan?.hasRisky ? t('scanSubmitForApproval') : t('scanRun')}
          </Button>
        </>
      }
    >
      {busy && <div className="scan-veil"><Loader2 size={22} className="spin" />{t('scanDispatching')}</div>}

      {!scan && !error && <Loading />}
      {error && <ErrorState error={error} />}

      {scan && (
        <>
          <div className="scan-nums">
            <div><b>{scan.total}</b><span>{t('scanTotal')}</span></div>
            <div className={scan.high ? 'danger' : ''}><b>{scan.high}</b><span>{t('scanHigh')}</span></div>
            <div className={scan.mid ? 'warn' : ''}><b>{scan.mid}</b><span>{t('scanMid')}</span></div>
            <div><b>{scan.safe}</b><span>{t('scanSafe')}</span></div>
          </div>

          <ol className="scan-list">
            {scan.statements.map((st: ScannedStmt) => (
              <li key={st.index} className={clsx(st.risk === 'high' && 'hi')}>
                <span className="scan-ix">{String(st.index).padStart(2, '0')}</span>
                <code>{st.sql}</code>
                <span className="scan-tags">
                  {st.command && (
                    <Badge tone={st.risk === 'high' ? 'danger' : st.risk === 'mid' ? 'warning' : 'neutral'}>
                      {st.command}
                    </Badge>
                  )}
                  {st.noWhere && <Badge tone="danger">{t('scanNoWhere')}</Badge>}
                </span>
              </li>
            ))}
          </ol>
        </>
      )}

      {submitted ? (
        <div className="notice"><CircleCheck size={15} />{t('scanSubmittedWith', { ap: submitted })}</div>
      ) : !scan ? null : scan.hasRisky ? (
        <div className="notice danger"><ShieldAlert size={15} />{t('scanRiskyBanner')}</div>
      ) : (
        <div className="notice"><CircleCheck size={15} />{t('scanSafeBanner')}</div>
      )}

      {scan?.hasRisky && !submitted && (
        <div className="scan-hint"><TriangleAlert size={13} />{t('scanRiskyHint')}</div>
      )}
    </Modal>
  )
}
