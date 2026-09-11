import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Download, FileUp } from 'lucide-react'
import { IMPORT_TEMPLATE, parseConnectionImport } from '@/lib/connectionImport'
import { downloadCsv } from '@/lib/csv'
import { useImportConnections } from '@/hooks/useConnections'
import { Button } from '@/components/common/Button'
import { Modal } from '@/components/common/Modal'
import type { Environment } from '@/types'

/**
 * CSV 批量导入。
 *
 * 每一行都会变成一台网关真的会代理命令过去的实例,所以**整表先校验、再动手**
 * (解析与校验在 `lib/connectionImport` 里,是纯逻辑):一份做到一半失败的表格,
 * 会把实例清单留在一个谁都没要求过的状态里。
 *
 * 校验用的是**当前真实存在的环境列表**,不是那四个出厂环境 —— 一份指向刚建出来的
 * 集群的表格,应该在这里就被接受,而不是到服务端才失败。
 */
export function ImportModal({ envs, onClose }: { envs: Environment[]; onClose: () => void }) {
  const { t } = useTranslation()
  const [text, setText] = useState('')
  const [done, setDone] = useState(0)
  const [failures, setFailures] = useState<string[]>([])
  const fileRef = useRef<HTMLInputElement>(null)
  const run = useImportConnections()

  const parsed = parseConnectionImport(text, envs.map((e) => e.code))
  const busy = run.isPending

  async function pickFile(e: React.ChangeEvent<HTMLInputElement>) {
    const f = e.target.files?.[0]
    if (!f) return
    setText(await f.text())
    // 清掉 input 的值,同一个文件才能被再选一次(改完之后重选是常事)。
    e.target.value = ''
  }

  function start() {
    if (!parsed.rows.length) return
    setDone(0)
    setFailures([])
    run.mutate(
      { rows: parsed.rows, onProgress: setDone },
      {
        onSuccess: (r) => {
          setFailures(r.failures)
          // 全成才关。有失败时留在原地 —— 那张按行号点名的清单正是要拿去改表的。
          if (!r.failures.length) onClose()
        },
      },
    )
  }

  return (
    <Modal
      open
      title={t('impTitle')}
      sub={t('impSub')}
      width={680}
      onClose={() => !busy && onClose()}
      footer={
        <>
          <Button variant="ghost" disabled={busy} onClick={onClose}>{t('cancel')}</Button>
          <Button variant="primary" disabled={busy || !parsed.rows.length} onClick={start}>
            {busy ? t('impRunning', { done, total: parsed.rows.length }) : t('impRun')}
          </Button>
        </>
      }
    >
      <div className="conn-imp-bar">
        <Button variant="secondary" disabled={busy} onClick={() => fileRef.current?.click()}>
          <FileUp size={14} />{t('impPick')}
        </Button>
        <Button
          variant="secondary"
          onClick={() => downloadCsv('connections-template.csv', IMPORT_TEMPLATE)}
        >
          <Download size={14} />{t('impTemplate')}
        </Button>
        <input
          ref={fileRef}
          type="file"
          accept=".csv,text/csv,text/plain"
          hidden
          onChange={pickFile}
        />
      </div>

      <div className="fld">
        <label>{t('impPaste')}</label>
        <textarea
          value={text}
          spellCheck={false}
          placeholder={IMPORT_TEMPLATE}
          onChange={(e) => setText(e.target.value)}
        />
      </div>
      <p className="conn-imp-cols">{t('impCols')}</p>

      {!!parsed.rows.length && (
        <div className="notice">{t('impPreview', { n: parsed.rows.length })}</div>
      )}

      {/* 校验错误按行号列出来,一次改完整表 —— 一行一行地试是这件事最慢的做法。 */}
      {!!parsed.errors.length && (
        <div className="notice danger conn-imp-errs">
          <b>{t('impErrTitle', { n: parsed.errors.length })}</b>
          {parsed.errors.map((e) => <span key={e.line}>{e.message}</span>)}
        </div>
      )}

      {/* 服务端拒掉的那几行。它们和上面的校验错误不是一回事,所以分开列。 */}
      {!!failures.length && (
        <div className="notice danger conn-imp-errs">
          {failures.map((f, i) => <span key={i}>{f}</span>)}
        </div>
      )}
    </Modal>
  )
}
