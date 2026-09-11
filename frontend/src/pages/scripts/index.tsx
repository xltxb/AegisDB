import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { FileCode2, ScanLine, Terminal, Trash2, Upload } from 'lucide-react'
import {
  useDeleteScriptUpload, useScriptContent, useScriptScan, useScriptScans, useScriptUploads,
  useUploadScript,
} from '@/hooks/useScripts'
import { meQueryOptions } from '@/api/modules/auth'
import { Badge } from '@/components/common/Badge'
import { Button } from '@/components/common/Button'
import { Card } from '@/components/common/Card'
import { Modal } from '@/components/common/Modal'
import ScriptScanModal from '@/components/script/ScriptScanModal'
import { Empty, ErrorState, Loading } from '@/components/common/States'
import type { ScriptScanResp, ScriptUpload } from '@/types'

const at = (s: string) => (s ? s.slice(5, 16).replace('T', ' ') : '—')
const size = (n: number) =>
  n < 1024 ? `${n} B` : n < 1048576 ? `${(n / 1024).toFixed(1)} KB` : `${(n / 1048576).toFixed(2)} MB`

export default function ScriptsPage() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { data, isLoading, error, refetch } = useScriptUploads()
  const { data: me } = useQuery(meQueryOptions())
  const upload = useUploadScript()
  const del = useDeleteScriptUpload()
  const fileRef = useRef<HTMLInputElement>(null)
  const [openId, setOpenId] = useState(0)
  // 要下发的那一份。与只读预览分开:预览只看,下发要选实例与库并真的跑。
  const [runId, setRunId] = useState(0)
  const [dragging, setDragging] = useState(false)
  /**
   * 扫过哪几份。
   *
   * 扫描按语句数线性,一份几 MB 的迁移脚本服务端要跑十几秒 —— 列表一进来就把每份
   * 都扫一遍,等于一次两分钟的加载。所以只扫人打开过的,其余卡片显示「未扫描」。
   */
  const [scanned, setScanned] = useState<number[]>([])

  const files = (data ?? []) as ScriptUpload[]
  const nameOf = (id: number) => files.find((f) => f.id === id)?.filename ?? ''
  const scans = useScriptScans(scanned, nameOf)

  function open(u: ScriptUpload) {
    setOpenId(u.id)
    setScanned((ids) => (ids.includes(u.id) ? ids : [...ids, u.id]))
  }

  /** 下发前必须先有扫描结果 —— 没扫过就不知道该给「执行」还是「提交审批」。 */
  function run(u: ScriptUpload) {
    setRunId(u.id)
    setScanned((ids) => (ids.includes(u.id) ? ids : [...ids, u.id]))
  }

  function pick(file: File | undefined) {
    if (file) upload.mutate(file)
  }

  const current = files.find((f) => f.id === openId)

  return (
    <div
      className="page"
      onDragOver={(e) => { e.preventDefault(); setDragging(true) }}
      onDragLeave={() => setDragging(false)}
      onDrop={(e) => { e.preventDefault(); setDragging(false); pick(e.dataTransfer.files?.[0]) }}
    >
      <header className="page-head">
        <div>
          <h1>{t('scriptsTitle')}</h1>
          <p>{t('scriptsSub')}</p>
        </div>
        <div className="grow">
          <Button variant="primary" disabled={upload.isPending} onClick={() => fileRef.current?.click()}>
            <Upload size={15} />{upload.isPending ? t('scriptsUploading') : t('scriptsUpload')}
          </Button>
        </div>
      </header>
      <input
        ref={fileRef}
        type="file"
        accept=".sql,text/plain"
        className="sc-file"
        onChange={(e) => { pick(e.target.files?.[0]); e.target.value = '' }}
      />

      {dragging && <div className="notice">{t('scriptsUploadHint')}</div>}

      {isLoading && <Loading />}
      {error && <ErrorState error={error} retry={() => refetch()} />}

      {!isLoading && !error && (
        files.length ? (
          <div className="sc-grid">
            {files.map((u) => (
              <ScriptCard
                key={u.id}
                file={u}
                uploader={me?.name ?? ''}
                scan={scans.get(u.id)}
                onView={() => open(u)}
                onRun={() => run(u)}
                onDispatch={() =>
                  // 终端才是执行脚本的地方 —— 这里只把"用哪一份"交过去,等同于在
                  // 终端里敲 `\i 文件名`。不另开一条执行路径,是因为网关的判定挂在
                  // 终端那条上。
                  navigate('/terminal', { state: { scriptUploadId: u.id, filename: u.filename } })
                }
                onDelete={() => {
                  if (window.confirm(t('scriptsDelConfirm', { name: u.filename }))) del.mutate(u.id)
                }}
              />
            ))}
          </div>
        ) : (
          <Card><Empty hint={t('scriptsEmpty')} /></Card>
        )
      )}

      <ViewModal file={current} onClose={() => setOpenId(0)} />

      {/* 下发走扫描弹窗:它同时负责"全安全直接跑"和"有高危转审批"两条路。 */}
      <ScriptScanModal
        open={!!runId}
        filename={files.find((f) => f.id === runId)?.filename ?? ''}
        scan={scans.get(runId) ?? null}
        error={scans.errorOf(runId)}
        uploadId={runId}
        onClose={() => setRunId(0)}
        onDispatched={() => refetch()}
      />
    </div>
  )
}

/** 三档计数 + 一个风险徽标。没扫过就说没扫过,不填零。 */
function riskBadge(scan: ScriptScanResp | undefined) {
  if (!scan) return { tone: 'neutral' as const, key: 'scriptsUnscanned' }
  if (scan.hasRisky || scan.high > 0) return { tone: 'danger' as const, key: 'scriptsRiskHigh' }
  if (scan.mid > 0) return { tone: 'warning' as const, key: 'scriptsRiskMid' }
  return { tone: 'success' as const, key: 'scriptsRiskSafe' }
}

function ScriptCard({
  file, uploader, scan, onView, onRun, onDispatch, onDelete,
}: {
  file: ScriptUpload
  uploader: string
  scan: ScriptScanResp | undefined
  onView: () => void
  onRun: () => void
  onDispatch: () => void
  onDelete: () => void
}) {
  const { t } = useTranslation()
  const r = riskBadge(scan)

  return (
    <Card className="sc-card">
      <div className="sc-head">
        <span className="sc-icon"><FileCode2 size={16} /></span>
        <span className="sc-name" title={file.path}>{file.filename}</span>
        <Badge tone={r.tone}>{t(r.key)}</Badge>
      </div>

      <div className="sc-meta">
        <span>{t('scriptsBy')} {uploader || '—'}</span>
        <span>{at(file.createdAt)}</span>
        <span>{size(file.size)}</span>
        <Badge tone={file.source === 'terminal' ? 'accent' : 'neutral'}>
          {t(file.source === 'terminal' ? 'scriptsFromTerm' : 'scriptsFromPage')}
        </Badge>
      </div>

      {scan ? (
        <div className="sc-counts">
          <span className="sc-total">{t('scriptsStmts', { n: scan.total })}</span>
          <span className="sc-c high">{t('scriptsHigh')} {scan.high}</span>
          <span className="sc-c mid">{t('scriptsMid')} {scan.mid}</span>
          <span className="sc-c safe">{t('scriptsSafe')} {scan.safe}</span>
        </div>
      ) : (
        <div className="sc-counts">
          <span className="sc-unscanned"><ScanLine size={13} />{t('scriptsScanHint')}</span>
        </div>
      )}

      <div className="sc-ops">
        <Button variant="secondary" onClick={onView}>{t('scriptsView')}</Button>
        <Button variant="primary" onClick={onRun}>
          <ScanLine size={13} />{t('scriptsRun')}
        </Button>
        <Button variant="ghost" title={t('scriptsDispatchTip')} onClick={onDispatch}>
          <Terminal size={13} />{t('scriptsDispatch')}
        </Button>
        <Button variant="ghost" title={t('scriptsDelete')} onClick={onDelete}>
          <Trash2 size={13} />
        </Button>
      </div>
    </Card>
  )
}

/**
 * 查看:扫描结论在上,正文在下。
 *
 * 扫描是打开这一份时才发的,所以卡片上的三档计数也是在这一刻才有 —— 反过来说,
 * 没打开过的卡片写「未扫描」是实话,不是还没加载完。
 */
function ViewModal({ file, onClose }: { file: ScriptUpload | undefined; onClose: () => void }) {
  const { t } = useTranslation()
  const id = file?.id ?? 0
  const scan = useScriptScan(id, file?.filename ?? '')
  const content = useScriptContent(id)

  return (
    <Modal
      open={!!file}
      width={720}
      title={file?.filename ?? ''}
      sub={file?.path}
      onClose={onClose}
      footer={<Button variant="ghost" onClick={onClose}>{t('cancel')}</Button>}
    >
      {scan.isLoading && <Loading />}
      {scan.error && <ErrorState error={scan.error} />}
      {scan.data && (
        <div className="sc-counts">
          <span className="sc-total">{t('scriptsStmts', { n: scan.data.total })}</span>
          <span className="sc-c high">{t('scriptsHigh')} {scan.data.high}</span>
          <span className="sc-c mid">{t('scriptsMid')} {scan.data.mid}</span>
          <span className="sc-c safe">{t('scriptsSafe')} {scan.data.safe}</span>
        </div>
      )}
      {scan.data && (
        <div className="sc-stmts">
          {scan.data.statements.map((s) => (
            <div key={s.index} className={`sc-stmt r-${s.risk}`}>
              <span className="sc-stno">{t('scriptsStmtNo', { n: s.index })}</span>
              <code>{s.sql}</code>
              {s.noWhere && <span className="sc-nowhere">{t('scriptsNoWhere')}</span>}
            </div>
          ))}
        </div>
      )}

      <div className="fld">
        <label>{t('scriptsContent')}</label>
        {content.isLoading && <Loading />}
        {content.error && <ErrorState error={content.error} />}
        {content.data && <pre className="sc-content">{content.data.content}</pre>}
      </div>
    </Modal>
  )
}
