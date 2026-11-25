import { useLayoutEffect, useRef, useState, type HTMLAttributes } from 'react'
import { createPortal } from 'react-dom'
import clsx from 'clsx'
import { Upload, FileCode2, RotateCw, ShieldAlert, FolderCog, ListChecks, ChevronDown, Download, Command, ClipboardPaste } from 'lucide-react'
import '@xterm/xterm/css/xterm.css'
import ApprovalModal from '@/components/modals/ApprovalModal'
import ScriptScanModal from '@/components/modals/ScriptScanModal'
import ScriptPrepModal from '@/components/modals/ScriptPrepModal'
import SnippetModal from '@/components/modals/SnippetModal'
import { useActivated } from '@/components/KeepAlive'
import { useMountEffect, useUpdateEffect } from '@/hooks/effects'
import { useI18n } from '@/hooks/useI18n'
import { useObservable } from '@/hooks/useObservable'
import { useAuthStore } from '@/stores/auth'
import { useUIStore } from '@/stores/ui'
import { createSession, type SessionProps } from './sessionController'
import './TerminalSession.scoped.css'

// 其余属性(父组件用来藏标签的 style、作用域属性等)落到根元素上
type Props = SessionProps & Omit<HTMLAttributes<HTMLDivElement>, keyof SessionProps>

/** 这个组件没有对外方法。 */
export type TerminalSessionHandle = Record<string, never>

export default function TerminalSession({
  conn, active, conns, chain, scriptEnabled, scriptSavePath, db, gridView,
  onUpdateRisk, onUpdateWsStatus, onUpdateDb, onResult, onClose, className, ...rest
}: Props) {
  const { t } = useI18n()
  const isAdmin = useAuthStore((st) => st.isAdmin)
  const theme = useUIStore((st) => st.theme)

  const props: SessionProps = {
    conn, active, conns, chain, scriptEnabled, scriptSavePath, db, gridView,
    onUpdateRisk, onUpdateWsStatus, onUpdateDb, onResult, onClose,
  }
  // 会话逻辑都在控制器里(见 sessionController):这里只渲染它的状态、把界面动作交给它
  const [s] = useState(() => createSession(props))
  useLayoutEffect(() => { s.setProps(props) })

  const termEl = useRef<HTMLDivElement>(null)
  const fileRef = useRef<HTMLInputElement>(null)
  const getFileRef = useRef<HTMLInputElement>(null)
  const startFileRef = useRef<HTMLInputElement>(null)

  useMountEffect(() => {
    s.mount({ term: termEl.current!, file: fileRef.current!, getFile: getFileRef.current!, startFiles: startFileRef.current! })
    return s.unmount
  })
  useActivated(s.activated)
  useUpdateEffect(s.activeChanged, [active])
  useUpdateEffect(s.scriptEnabledChanged, [scriptEnabled])
  useUpdateEffect(s.dbChanged, [db])
  // Repaint the live terminal when the app theme is switched.
  useUpdateEffect(s.themeChanged, [theme])

  const risk = useObservable(s.risk)
  const wsStatus = useObservable(s.wsStatus)
  useUpdateEffect(() => { onUpdateRisk?.(risk) }, [risk])
  useUpdateEffect(() => { onUpdateWsStatus?.(wsStatus) }, [wsStatus])

  const apOpen = useObservable(s.apOpen)
  const apCmd = useObservable(s.apCmd)
  const apRisk = useObservable(s.apRisk)
  const apAuditId = useObservable(s.apAuditId)
  const apRule = useObservable(s.apRule)
  const scOpen = useObservable(s.scOpen)
  const scScan = useObservable(s.scScan)
  const scSubmitted = useObservable(s.scSubmitted)
  const scRunning = useObservable(s.scRunning)
  const scDone = useObservable(s.scDone)
  const runProg = useObservable(s.runProg)
  const prep = useObservable(s.prep)
  const prepProg = useObservable(s.prepProg)
  const enabled = useObservable(s.enabled)
  const pathPromptOpen = useObservable(s.pathPromptOpen)
  const dbOptions = useObservable(s.dbOptions)
  const targetDb = useObservable(s.targetDb)
  const mfaOpen = useObservable(s.mfaOpen)
  const mfaInput = useObservable(s.mfaInput)
  const mfaErr = useObservable(s.mfaErr)
  const canExportLog = useObservable(s.canExportLog)
  const dangerOpen = useObservable(s.dangerOpen)
  const snipOpen = useObservable(s.snipOpen)
  const pasteOpen = useObservable(s.pasteOpen)
  const pasteText = useObservable(s.pasteText)
  const uploads = useObservable(s.uploads) ?? []
  const pickOpen = useObservable(s.pickOpen)
  const pickPos = useObservable(s.pickPos)

  return (
    <div {...rest} className={clsx('session', className)}>
      <div className="actionbar">
        <div className="host"><span className={clsx('online', wsStatus !== 'open' && 'off')} />{conn.name}{targetDb && <span className="curdb">· {targetDb}</span>} · SQL</div>
        <div className="actions">
          <div className="upload" title={enabled ? '' : t('scPathTitle')} onClick={s.onUploadClick}><Upload size={13} />{t('uploadScript')}</div>
          <input ref={fileRef} type="file" accept=".sql,text/plain" className="hidden-file" onChange={(e) => s.onFile(e.currentTarget)} />
          {/* SQL*Plus 的 @ / START:主脚本连同它引用的文件(可多选,或一个 zip) */}
          <input ref={getFileRef} type="file" accept=".sql,.txt,text/plain" className="get-file" onChange={(e) => { void s.onGetFile(e.currentTarget) }} />
          <input ref={startFileRef} type="file" multiple accept=".sql,.txt,.zip,text/plain,application/zip" className="start-files" onChange={(e) => { void s.onStartFiles(e.currentTarget) }} />
          <div className="upload" onClick={(e) => { void s.togglePick(e.currentTarget) }}><ListChecks size={13} />{t('uploadedScripts')}<ChevronDown size={12} /></div>
          {pickOpen && createPortal(
            <>
              <div className="pick-mask" onClick={() => { s.pickOpen.value = false }} />
              <div className="picker" style={{ top: pickPos.top + 'px', left: pickPos.left + 'px' }}>
                <div className="pick-head">{t('uploadedScripts')}</div>
                {!uploads.length && <div className="pick-empty">{t('uploadNone')}</div>}
                {uploads.map((u) => (
                  <div key={u.id} className="pick-item" title={u.path} onClick={() => { void s.runUploaded(u) }}>
                    <FileCode2 size={12} /><span className="pn">{u.filename}</span><span className="pm">{u.createdAt?.slice(5, 16)}</span>
                  </div>
                ))}
              </div>
            </>, document.body,
          )}
          <div className="upload" title={t('pasteBtnTitle')} onClick={() => s.openPaste()}><ClipboardPaste size={13} />{t('pasteBtn')}</div>
          <div className="upload" title={t('snipBtnTitle')} onClick={() => { s.snipOpen.value = true }}><Command size={13} />{t('snipBtn')}</div>
          <div className="sample" onClick={s.onSampleClick}><FileCode2 size={13} />{t('sampleScript')}</div>
          {/* Disabled until something has actually been printed: an empty file is
              not a useful thing to hand someone. */}
          <div className={clsx('upload', !canExportLog && 'off')} title={t('logExportHint')} onClick={() => { if (canExportLog) void s.exportLog() }}>
            <Download size={13} />{t('logExport')}
          </div>
          {/* title 不在图标的 props 类型里,但会原样落到 svg 上 */}
          <RotateCw className="refresh" size={14} {...{ title: t('refreshSession') }} onClick={s.refreshSession} />
        </div>
      </div>

      <div className="xterm-wrap"><div ref={termEl} className="xterm-host" /></div>

      {/* 底部状态栏。左边回答"此刻这条会话是什么",右边是快捷键 —— 后者原先只写在
          开场白里,滚两屏就再也找不到了,而它恰恰是要反复用的东西。 */}
      <div className="statusbar">
        <span className={clsx('ok', wsStatus !== 'open' && 'warn')}>
          {wsStatus === 'open' ? t('connected') : wsStatus === 'connecting' ? t('wsConnecting') : t('wsDisconnected')}
        </span>
        <span className="hl">{conn.engine?.toUpperCase()}</span>
        <span>{conn.defaultRole}</span>
        <span>UTF-8</span>
        <span className="right keyhint">
          <span><kbd>↑</kbd><kbd>↓</kbd> {t('sbHistory')}</span>
          <span><kbd>Ctrl</kbd><kbd>L</kbd> {t('sbClear')}</span>
          <span><kbd>Ctrl</kbd><kbd>C</kbd> {t('sbCancel')}</span>
        </span>
      </div>

      <ApprovalModal
        open={apOpen} command={apCmd} instance={conn.name} env={conn.env}
        risk={apRisk} auditId={apAuditId} chain={chain} rule={apRule}
        onCancel={s.cancelApproval} onSubmit={s.submitApproval}
      />
      <ScriptScanModal
        open={scOpen} scan={scScan} submitted={scSubmitted} savePath={scriptSavePath}
        instance={conn.name} databases={dbOptions} running={scRunning} done={scDone} progress={runProg}
        targetDb={targetDb} onUpdateTargetDb={(v) => { s.targetDb.value = v }}
        onClose={() => { s.scOpen.value = false }} onRun={s.runScript}
      />
      <ScriptPrepModal
        open={!!prep} filename={prep?.filename || ''} step={prep?.step || 'scan'} readPct={prep?.readPct || 0}
        uploadPct={prep?.uploadPct || 0} fileSteps={prep?.fileSteps || false} progress={prepProg}
        onCancel={s.cancelPrep}
      />
      <SnippetModal open={snipOpen} onClose={s.closeSnippets} onRun={s.runFromModal} />

      {pasteOpen && (
        <div className="mfa-overlay">
          <div className="mfa-mask" onClick={s.closePaste} />
          <div className="mfa-modal paste-modal">
            <div className="mfa-top info" />
            <div className="mfa-pad">
              <div className="mfa-title acc"><ClipboardPaste size={18} />{t('pasteTitle')}</div>
              <div className="mfa-desc">{t('pasteDesc', { env: conn.env?.toUpperCase(), inst: conn.name })}</div>
              <textarea
                autoFocus
                className="paste-box"
                spellCheck={false}
                placeholder={t('pastePh')}
                value={pasteText}
                onChange={(e) => { s.pasteText.value = e.target.value }}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && e.ctrlKey) { e.preventDefault(); s.submitPaste() }
                  else if (e.key === 'Escape') { e.preventDefault(); s.closePaste() }
                }}
              />
              <div className="paste-hint">{t('pasteHint')}</div>
              <div className="mfa-acts">
                <button className="mfa-btn ghost" onClick={s.closePaste}>{t('btnCancel')}</button>
                <button className="mfa-btn primary" disabled={!pasteText.trim()} onClick={s.submitPaste}>{t('pasteRun')}</button>
              </div>
            </div>
          </div>
        </div>
      )}

      {/* 进入生产实例的红色确认。挡在路中间,先确认再操作 —— 终端里那行红字是被动的,
          人扫一眼就滑过去了。 */}
      {dangerOpen && (
        <div className="mfa-overlay">
          <div className="mfa-mask" />
          <div className="mfa-modal danger-modal">
            <div className="mfa-top" />
            <div className="mfa-pad">
              <div className="mfa-title"><ShieldAlert size={18} />{t('prodWarnTitle')}</div>
              <div className="danger-target">
                <span className="dt-env">{conn.env?.toUpperCase()}</span>
                <span className="dt-name">{conn.name}</span>
                {targetDb && <span className="dt-db">/ {targetDb}</span>}
              </div>
              <div className="mfa-desc">{t('prodWarnDesc')}</div>
              <div className="mfa-acts">
                <button className="mfa-btn ghost" onClick={s.cancelDanger}>{t('prodWarnLeave')}</button>
                <button className="mfa-btn danger" onClick={s.confirmDanger}>{t('prodWarnGo')}</button>
              </div>
            </div>
          </div>
        </div>
      )}

      {pathPromptOpen && (
        <div className="mfa-overlay">
          <div className="mfa-mask" onClick={() => { s.pathPromptOpen.value = false }} />
          <div className="mfa-modal">
            <div className="mfa-top info" />
            <div className="mfa-pad">
              <div className="mfa-title acc"><FolderCog size={18} />{t('scPathTitle')}</div>
              {/* 系统设置只有平台管理员进得去:别人只说清该找谁,不给一个点了没反应的按钮(2026-10 审查) */}
              <div className="mfa-desc">{isAdmin ? t('scPathDesc') : t('scPathDescAskAdmin')}</div>
              <div className="mfa-acts">
                <button className="mfa-btn ghost" onClick={() => { s.pathPromptOpen.value = false }}>{isAdmin ? t('btnCancel') : t('scPathGotIt')}</button>
                {isAdmin && <button className="mfa-btn primary" onClick={s.goSettings}>{t('scPathGo')}</button>}
              </div>
            </div>
          </div>
        </div>
      )}

      {mfaOpen && (
        <div className="mfa-overlay">
          <div className="mfa-mask" onClick={s.cancelMfa} />
          <div className="mfa-modal">
            <div className="mfa-top" />
            <div className="mfa-pad">
              <div className="mfa-title"><ShieldAlert size={18} />{t('mfaStepTitle')}</div>
              <div className="mfa-desc">{t('mfaStepDesc')}</div>
              <input
                autoFocus
                className="mfa-code" inputMode="numeric" autoComplete="one-time-code" placeholder="000000"
                value={mfaInput}
                onChange={(e) => { s.mfaInput.value = e.target.value.replace(/\D/g, '').slice(0, 6) }}
                onKeyUp={(e) => { if (e.key === 'Enter') void s.submitMfa() }}
              />
              {mfaErr && <div className="mfa-err">{mfaErr}</div>}
              <div className="mfa-acts">
                <button className="mfa-btn ghost" onClick={s.cancelMfa}>{t('btnCancel')}</button>
                <button className="mfa-btn primary" onClick={() => { void s.submitMfa() }}>{t('mfaVerify')}</button>
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
