import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import clsx from 'clsx'
import { Copy, Check, Hammer, TriangleAlert } from 'lucide-react'
import { connectionsApi } from '@/api/modules/connections'
import { highlightSqlHtml } from '@/lib/sqlHighlight'
import { copyText } from '@/lib/clipboard'
import { CODE_OK } from '@/api/codes'
import { Modal } from '@/components/common/Modal'
import { Loading } from '@/components/common/States'

/** 屏幕上正在看的那一个对象。scope 是 schema(有 schema 的引擎)或库/owner。 */
export interface SourceTarget {
  cid: number
  scope: string
  type: string
  name: string
  database?: string
  /** 这台实例是不是 Oracle —— 只有它有编译单元。 */
  oracle: boolean
}

/** 有 ALTER … COMPILE 这回事的对象类型。其它引擎上的同名类型没有。 */
const COMPILABLE = ['package', 'procedure', 'function', 'trigger', 'type']

interface CompileReport {
  ok?: boolean
  ms?: number
  targets?: { type: string; status: string }[]
  errors?: { type: string; line: number; position: number; text: string }[]
  warnings?: string[]
}

/**
 * 一个对象在查询缓存里的身份。
 *
 * 调用方拿同一个函数当组件的 `key`(见 `pages/terminal/index.tsx`),于是"换一个对象"
 * 就是"换一个组件实例" —— 上一个的编译结论和复制状态跟着旧实例一起走,不必在
 * effect 里一样样擦干净。两处必须用同一个函数:缓存的身份和组件的身份说的是同一
 * 件事,各写一份就会有"换了对象、编译结论还挂在屏幕上"这种谁也说不清的错位。
 */
export const sourceKey = (t: SourceTarget) =>
  ['object-source', t.cid, t.scope, t.type, t.name, t.database ?? ''] as const

/**
 * 一个对象的定义,只读。
 *
 * 表看的是建表 DDL,函数/存储过程/包/触发器看的是源码 —— 两者走的是同一个接口,
 * 区别只在 type。高亮用的是把每个字符都转义过再套 span 的 `highlightSqlHtml`,
 * 所以服务端返回的 DDL 走 dangerouslySetInnerHTML 是安全的。
 *
 * **由调用方在有对象时才挂载,并以 `sourceKey` 为 key**。"当前在看哪一个"因此是
 * 调用方的状态,不是这个组件要额外应付的一种空态。
 */
export function SourceViewer({ target, onClose }: { target: SourceTarget; onClose: () => void }) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const [copied, setCopied] = useState(false)
  const [copyErr, setCopyErr] = useState(false)
  const [report, setReport] = useState<CompileReport | null>(null)
  const [compileErr, setCompileErr] = useState('')

  const key = sourceKey(target)

  const q = useQuery({
    queryKey: key,
    queryFn: () => connectionsApi.objectSource(target.cid, target.scope, target.type, target.name, target.database ?? ''),
    retry: false,
    staleTime: 60_000,
  })

  const compile = useMutation({
    mutationFn: () => connectionsApi.compileObject(target.cid, {
      scope: target.scope, type: target.type, name: target.name, database: target.database ?? '',
    }),
    onSuccess: (env) => {
      if (env.code !== CODE_OK) { setCompileErr(env.msg || t('objCompileFail')); return }
      setReport(env.data.report as CompileReport)
      // 编译改变了对象,屏幕上这份源码可能已经不是库里的那份 —— 重新取一次。
      qc.invalidateQueries({ queryKey: key })
    },
    onError: (e: Error) => setCompileErr(e.message || t('objCompileFail')),
  })

  const text = q.data?.source ?? ''
  const canCompile = target.oracle && COMPILABLE.includes(target.type)
  // 连续问号 = 库里存的就是问号,不是这一页显示坏了(部署时 NLS_LANG 与库字符集
  // 不匹配,Oracle 在 CREATE 那一刻就把转换不了的字符换成了 '?')。
  const looksMojibake = target.oracle && /\?{2,}/.test(text)
  // 成功的判定跟后端一致:每个单元 VALID 且没有诊断。前端不另立标准。
  const cmpOK = !!report && !!(report.targets ?? []).length && !(report.errors ?? []).length
    && (report.targets ?? []).every((x) => x.status === 'VALID')

  async function copySource() {
    // 复制的是**原始文本**,不是渲染后的高亮 DOM —— 后者会把着色标签一起带走。
    if (!(await copyText(text))) { setCopyErr(true); setTimeout(() => setCopyErr(false), 2000); return }
    setCopied(true)
    setTimeout(() => setCopied(false), 1600)
  }

  return (
    <Modal
      open
      width={860}
      title={target.name}
      sub={target.type}
      onClose={onClose}
      footer={
        <>
          {canCompile && (
            <button className="c-btn v-secondary" disabled={compile.isPending} onClick={() => compile.mutate()}>
              <Hammer size={13} />{compile.isPending ? t('objCompiling') : t('objCompile')}
            </button>
          )}
          <button className="c-btn v-ghost" disabled={!text} onClick={copySource}>
            {copied ? <Check size={13} /> : <Copy size={13} />}{copied ? t('copied') : t('copy')}
          </button>
        </>
      }
    >
      {compileErr && <div className="notice danger">{compileErr}</div>}
      {copyErr && <div className="notice danger">{t('objCopyFail')}</div>}

      {/* 编译结果:状态按 Oracle 的裁决显示。ALTER … COMPILE 即使编译出错也返回
          成功,所以这里认的是 all_objects.status 与 all_errors,不是调用成没成。 */}
      {report && (
        <div className={clsx('cmp', cmpOK ? 'ok' : 'err')}>
          <div className="cmp-head">
            <span className={clsx('cmp-badge', cmpOK ? 'ok' : 'bad')}>{cmpOK ? t('objCompileOk') : t('objCompileBad')}</span>
            {(report.targets ?? []).map((tg) => (
              <span key={tg.type} className="cmp-unit">
                {tg.type} · <b className={tg.status === 'VALID' ? 'ok' : 'bad'}>{tg.status}</b>
              </span>
            ))}
            <span className="cmp-ms">{report.ms}ms</span>
          </div>
          {(report.errors ?? []).map((d, i) => (
            <div key={i} className="cmp-diag"><span className="cmp-loc">{d.type} {d.line}:{d.position}</span>{d.text}</div>
          ))}
          {(report.warnings ?? []).map((w, i) => <div key={'w' + i} className="cmp-diag warn">{w}</div>)}
        </div>
      )}

      {/* 库里存的就是问号 —— 说清它不是显示问题,并给出自证的办法。 */}
      {looksMojibake && !q.isLoading && !q.error && (
        <div className="mojibake">
          <TriangleAlert size={14} />
          <div>
            <div className="mj-t">{t('objMojibakeTitle')}</div>
            <div className="mj-s">{t('objMojibakeSub')}</div>
            <code className="mj-q">SELECT line, dump(text, 1016) FROM all_source WHERE name = '{target.name}'</code>
          </div>
        </div>
      )}

      <div className="src-body">
        {q.isLoading ? <Loading />
          : q.error ? <div className="tv-hint err">{(q.error as Error).message || t('objLoadFail')}</div>
            : <pre className="src-pre" dangerouslySetInnerHTML={{ __html: highlightSqlHtml(text) }} />}
      </div>
    </Modal>
  )
}
