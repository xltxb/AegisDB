// ---- bulk import ----
//
// Each row becomes a real instance the gateway proxies commands to, so the whole
// sheet is parsed and validated BEFORE anything is created (see lib/connectionImport):
// a half-applied sheet that then fails leaves the estate in a state nobody asked
// for. Rows are created through the ordinary connections API, so the server-side
// environment validation and credential encryption all still apply.
import { useImperativeHandle, useMemo, useRef, useState, type Ref } from 'react'
import { createPortal } from 'react-dom'
import VButton from '@/components/common/VButton'
import api from '@/api'
import { useI18n } from '@/hooks/useI18n'
import { parseConnectionImport, IMPORT_TEMPLATE, type ImportRow } from '@/lib/connectionImport'
import { UTF8_BOM } from '@/lib/transcript'
import { useEnvTierStore } from '@/stores/envtier'
import { useUIStore } from '@/stores/ui'
import { probe } from './connectionForm'
import './ConnectionsView.scoped.css'

export interface ImportModalHandle { open(): void }

function downloadTemplate() {
  // The template itself is ASCII, but it exists to be filled in — with Chinese
  // instance names, in Excel. Opened without a byte order mark Excel treats it as
  // ANSI and saves it back that way, and the importer then reads GBK bytes as
  // UTF-8. Shipping the mark makes the whole round trip UTF-8.
  const url = URL.createObjectURL(new Blob([UTF8_BOM + IMPORT_TEMPLATE], { type: 'text/csv;charset=utf-8' }))
  const a = document.createElement('a')
  a.href = url
  a.download = 'connections-template.csv'
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

export default function ConnectionImportModal({ ref, reload }: { ref: Ref<ImportModalHandle>; reload: () => Promise<void> }) {
  const { t } = useI18n()
  const notify = useUIStore((s) => s.notify)
  const environments = useEnvTierStore((s) => s.environments)

  const [open, setOpen] = useState(false)
  const [text, setText] = useState('')
  const file = useRef<HTMLInputElement>(null)
  const [busy, setBusy] = useState(false)
  const [done, setDone] = useState(0)
  const [failures, setFailures] = useState<string[]>([])
  // Validate against the environments that actually exist, so a sheet targeting a
  // newly created cluster is accepted here instead of only failing at the server.
  const parsed = useMemo(() => parseConnectionImport(text, environments.map((e) => e.code)), [text, environments])

  useImperativeHandle(ref, () => ({
    open() {
      setText('')
      setFailures([])
      setDone(0)
      setOpen(true)
    },
  }), [])

  async function pickFile() {
    const f = file.current?.files?.[0]
    if (!f) return
    setText(await f.text())
    if (file.current) file.current.value = '' // let the same file be chosen again
  }

  async function run() {
    const rows: ImportRow[] = parsed.rows
    if (!rows.length) { notify(t('impNothing'), 'info'); return }
    setBusy(true)
    setDone(0)
    const failed: string[] = []
    const fail = (msg: string) => { failed.push(msg); setFailures([...failed]) }
    setFailures([])
    let ok = 0
    let unreachable = 0 // 建成了、但当场连不上 —— 既不是失败(单子在),也不是干净的成功
    // Sequential on purpose: each create is a privileged write, and a per-row
    // outcome is far more useful than one aggregate failure from a parallel burst.
    for (const r of rows) {
      try {
        const created = await api.createConnection({
          name: r.name, engine: r.engine, host: r.host, env: r.env as any,
          username: r.username, password: r.password, database: r.database,
        })
        // 导入建成了,但连不上要记一笔 —— 一份"全部成功"的导入报告里躺着两台连不上的
        // 实例,比报告里多两行字糟得多。
        const err = await probe(created.id, t('connUnreachable'))
        if (err) {
          fail(t('impRowUnreachable', { line: r.line, name: r.name, msg: err }))
          unreachable++
        }
        ok++
      } catch (e: any) {
        fail(t('impRowFailed', { line: r.line, name: r.name, msg: e?.message || '' }))
      }
      setDone((n) => n + 1)
    }
    setBusy(false)
    await reload()
    // 三个数一起说:一句"成功 3 条,失败 0 条"配上一个红色提示条和两行失败明细,
    // 数字和列表互相矛盾,读的人不知道该信哪个。
    const bad = rows.length - ok
    notify(
      unreachable ? t('impDoneUnreachable', { ok, fail: bad, unreachable }) : t('impDone', { ok, fail: bad }),
      failed.length ? 'error' : 'success',
    )
    if (!failed.length) setOpen(false)
  }

  if (!open) return null
  return createPortal(
    <div className="ce-mask" onClick={(e) => { if (e.target === e.currentTarget && !busy) setOpen(false) }}>
      <div className="ce-card imp">
        <div className="ce-head">
          <div className="ce-title">{t('impTitle')}</div>
          <div className="ce-sub">{t('impSub')}</div>
        </div>
        <div className="ce-body">
          <div className="imp-bar">
            <VButton variant="secondary" onClick={() => file.current?.click()}>{t('impPick')}</VButton>
            <VButton variant="secondary" onClick={downloadTemplate}>{t('impTemplate')}</VButton>
            <input ref={file} type="file" accept=".csv,text/csv,text/plain" hidden onChange={pickFile} />
          </div>
          <div className="fl">{t('impPaste')}</div>
          <textarea value={text} onChange={(e) => setText(e.target.value)} className="imp-ta" spellCheck={false} placeholder={IMPORT_TEMPLATE} />
          <div className="imp-hint">{t('impCols')}</div>

          {parsed.rows.length > 0 && <div className="imp-ok">{t('impPreview', { n: parsed.rows.length })}</div>}
          {parsed.errors.length > 0 && (
            <div className="imp-bad">
              <div className="imp-bad-t">{t('impErrTitle', { n: parsed.errors.length })}</div>
              {parsed.errors.map((e, i) => <div key={i} className="imp-bad-l">{e.message}</div>)}
            </div>
          )}
          {failures.length > 0 && (
            <div className="imp-bad">
              {failures.map((f, i) => <div key={'f' + i} className="imp-bad-l">{f}</div>)}
            </div>
          )}
        </div>
        <div className="ce-foot">
          <VButton variant="secondary" disabled={busy} onClick={() => setOpen(false)}>{t('mCancel')}</VButton>
          <VButton variant="primary" disabled={busy || !parsed.rows.length} onClick={run}>
            {busy ? t('impRunning', { done, total: parsed.rows.length }) : t('impRun')}
          </VButton>
        </div>
      </div>
    </div>,
    document.body,
  )
}
