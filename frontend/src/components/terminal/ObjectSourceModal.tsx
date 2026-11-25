// 实例树里的源码查看器(对象定义 / 建表语句,只读)与 Oracle 的编译。从 DbTree 拆出来的,样式仍在 DbTree.scoped.css。
// 常驻挂着、自己管开合:父组件通过 ref 调 open()。
import { useImperativeHandle, useState, type Ref } from 'react'
import clsx from 'clsx'
import { X, Hammer, TriangleAlert, Copy, Check } from 'lucide-react'
import api from '@/api'
import { CODE_MFA_REQUIRED, localizeText } from '@/api/http'
import { highlightSqlHtml } from '@/lib/sqlHighlight'
import { copyText } from '@/lib/clipboard'
import { useI18n } from '@/hooks/useI18n'
import { useStateRef } from '@/hooks/useStateRef'
import type { Connection } from '@/types'
import './DbTree.scoped.css'

export interface ObjectSourceHandle {
  open: (cid: number, scope: string, type: string, name: string, database?: string) => Promise<void>
}
interface Props {
  connections: Connection[]
  /** 编译过一次:无效对象清单要重新清点。 */
  onCompiled: () => void
  ref?: Ref<ObjectSourceHandle>
}

// ---- 编译(Oracle 存储程序) ----
//
// 只有 Oracle 有编译单元。按钮的可见性跟着实例引擎走,而不是跟着对象类型猜:
// 其它引擎上的 "procedure" 没有 ALTER … COMPILE 这回事。
const COMPILABLE = ['package', 'procedure', 'function', 'trigger', 'type']

export default function ObjectSourceModal({ connections, onCompiled, ref }: Props) {
  const { t } = useI18n()

  // ---- source viewer ----
  // cid/scope/database are remembered so 编译 can target exactly what is on screen
  // rather than re-deriving it from the tree state.
  const [src, setSrc, srcRef] = useStateRef({ open: false, name: '', type: '', text: '', loading: false, err: '', cid: 0, scope: '', database: '' })
  const [compileState, setCompileState] = useState<{ busy: boolean; report: any | null; err: string }>({ busy: false, report: null, err: '' })

  // database: pass the browsed database for schema-level opens (PostgreSQL
  // family) — see ensureObjects; flat engines leave it empty.
  async function openSource(cid: number, scope: string, type: string, name: string, database = '') {
    setSrc({ open: true, name, type, text: '', loading: true, err: '', cid, scope, database })
    setCompileState({ busy: false, report: null, err: '' })
    try {
      const r = await api.objectSource(cid, scope, type, name, database)
      setSrc((s) => ({ ...s, text: r.source }))
    } catch (e: any) {
      setSrc((s) => ({ ...s, err: e?.message || t('objLoadFail') }))
    } finally {
      setSrc((s) => ({ ...s, loading: false }))
    }
  }
  useImperativeHandle(ref, () => ({ open: openSource }))
  function closeSource() { setSrc((s) => ({ ...s, open: false })) }

  // 复制建表/源码文本。复制的是**原始文本**,不是渲染后的高亮 DOM —— 后者会把
  // 语法着色的标签一起带走。成功与否如实反馈:复制这种事,谎报成功比失败更糟。
  const [srcCopied, setSrcCopied] = useState(false)
  const [srcCopyErr, setSrcCopyErr] = useState(false)
  const [copyTimer] = useState<{ h: ReturnType<typeof setTimeout> | null }>(() => ({ h: null }))
  async function copySource() {
    const ok = await copyText(srcRef.current.text)
    if (!ok) { setSrcCopyErr(true); setTimeout(() => setSrcCopyErr(false), 2000); return }
    setSrcCopied(true)
    if (copyTimer.h) clearTimeout(copyTimer.h)
    copyTimer.h = setTimeout(() => setSrcCopied(false), 1600)
  }

  // 生产实例上的编译要二次验证:编译会把库上的对象改成 VALID/INVALID,和在终端里执行
  // 一条 DDL 没有区别,所以它走同一道闸。后端答 42800 时把输码框亮出来,而不是把
  // "需要二次验证"当成一条普通错误甩给用户 —— 那样这个按钮在生产上就是个死按钮。
  const [cmpMfa, setCmpMfa] = useState(false)
  const [cmpCode, setCmpCode, cmpCodeRef] = useStateRef('')
  const srcConn = connections.find((x) => x.id === src.cid) || null
  const isOracleSrc = !!srcConn && /oracle/i.test(srcConn.engine)
  const canCompile = isOracleSrc && COMPILABLE.includes(src.type)

  // 连续问号 = 库里存的就是问号,不是这一页显示坏了。
  //
  // 成因是部署该对象时客户端 NLS_LANG 与库字符集不匹配:Oracle 在 CREATE 那一刻
  // 就把每个转换不了的字符换成 '?' 永久存下,ALL_SOURCE 里已经没有原文。看到的人
  // 第一反应总是"页面乱码",于是去查前端、查传输、查驱动 —— 这行提示就是为了把那
  // 段弯路省掉,并给出可自证的办法。
  const looksMojibake = isOracleSrc && /\?{2,}/.test(src.text)

  const report = compileState.report
  // 成功的判定跟后端一致:每个单元 VALID 且没有诊断。前端不另立标准。
  const cmpOK = !!report && !!(report.targets || []).length && !(report.errors || []).length
    && (report.targets || []).every((tg: any) => tg.status === 'VALID')

  async function compileObject() {
    const code = cmpCodeRef.current.trim()
    setCompileState({ busy: true, report: null, err: '' })
    try {
      const s = srcRef.current
      const env = await api.compileObject(s.cid, {
        scope: s.scope, type: s.type, name: s.name, database: s.database,
        mfaCode: code,
      })
      if (env.code === CODE_MFA_REQUIRED) {
        // 已经输过一次还回 42800,说明码不对 —— 换一句话说,而不是重复"需要二次验证"。
        setCompileState({ busy: false, report: null, err: code ? t('mfaBadCode') : '' })
        setCmpMfa(true)
        setCmpCode('')
        return
      }
      if (env.code !== 0) { setCompileState({ busy: false, report: null, err: env.msg }); return }
      setCmpMfa(false)
      setCmpCode('')
      setCompileState({ busy: false, report: env.data.report, err: '' })
      // 这个对象和它连带编过的那些,状态都变了 —— 无效对象清单重新取一次。
      onCompiled()
      // 编译改变了对象,屏幕上的源码可能已经不是库里的那份 —— 重新取一次。
      try {
        const n = srcRef.current
        const r = await api.objectSource(n.cid, n.scope, n.type, n.name, n.database)
        setSrc((x) => ({ ...x, text: r.source }))
      } catch { /* 源码刷新失败不掩盖编译结果 */ }
    } catch (e: any) {
      setCompileState({ busy: false, report: null, err: e?.message || t('objCompileFail') })
    }
  }

  if (!src.open) return null
  const CopyIcon = srcCopied ? Check : Copy
  return (
    <div className="src-overlay">
      <div className="src-mask" onClick={closeSource} />
      <div className="src-modal">
        <div className="src-hdr">
          <div className="src-title">{src.name}</div>
          <span className="src-type">{src.type}</span>
          {src.text && !src.loading && (
            <button className="src-copy" onClick={copySource}>
              <CopyIcon size={13} />{srcCopied ? t('copied') : t('copy')}
            </button>
          )}
          {canCompile && (
            <button className="src-compile" disabled={compileState.busy} onClick={compileObject}>
              <Hammer size={13} />{compileState.busy ? t('objCompiling') : t('objCompile')}
            </button>
          )}
          <div className="src-x" onClick={closeSource}><X size={16} /></div>
        </div>

        {/* 编译结果:状态按 Oracle 的裁决显示。ALTER … COMPILE 即使编译出错也
            返回成功,所以这里认的是 all_objects.status 与 all_errors。 */}
        {cmpMfa && (
          <div className="cmp-mfa">
            <span className="cmp-mfa-t">{t('mfaStepTitle')}</span>
            <span className="cmp-mfa-d">{t('mfaStepDesc')}</span>
            <input
              value={cmpCode} onChange={(e) => setCmpCode(e.target.value)} className="cmp-mfa-i" inputMode="numeric" maxLength={6}
              placeholder={t('mfaCodeLabel')} onKeyUp={(e) => { if (e.key === 'Enter') void compileObject() }}
            />
            <button className="cmp-mfa-b" disabled={compileState.busy || cmpCode.trim().length !== 6} onClick={compileObject}>
              {t('mfaVerify')}
            </button>
          </div>
        )}
        {compileState.err ? <div className="cmp err">{compileState.err}</div> : report ? (
          <div className={clsx('cmp', report.ok === false ? 'err' : 'ok')}>
            <div className="cmp-head">
              <span className={clsx('cmp-badge', cmpOK ? 'ok' : 'bad')}>{cmpOK ? t('objCompileOk') : t('objCompileBad')}</span>
              {(report.targets || []).map((tg: any) => (
                <span key={tg.type} className="cmp-unit">
                  {tg.type} · <b className={tg.status === 'VALID' ? 'ok' : 'bad'}>{tg.status}</b>
                </span>
              ))}
              <span className="cmp-ms">{report.ms}ms</span>
            </div>
            {(report.errors || []).map((d: any, i: number) => (
              <div key={i} className="cmp-diag">
                <span className="cmp-loc">{d.type} {d.line}:{d.position}</span>{d.text}
              </div>
            ))}
            {(report.warnings || []).map((w: any, i: number) => <div key={'w' + i} className="cmp-diag warn">{w}</div>)}
            {/* 连带编译:依赖它的 INVALID 对象,按依赖分层一并编过。每个对象照样按 Oracle
                回读的状态显示,编不过的给出第一条诊断。 */}
            {report.cascade && (
              <div className="cmp-casc">
                <div className="cmp-casc-h">
                  {t('objCascadeTitle', { n: report.cascade.total })}{' '}
                  <span className="ok">{t('invFixed', { n: report.cascade.fixed })}</span>
                  {!!report.cascade.failed && <span className="bad">{t('invFailed', { n: report.cascade.failed })}</span>}
                  {!!report.cascade.skipped && <span className="bad">{t('invSkipped', { n: report.cascade.skipped })}</span>}
                </div>
                {(report.cascade.items || []).map((it: any) => (
                  <div key={it.kind + it.name} className="cmp-casc-i">
                    <span className="cmp-casc-l">{t('invLevel', { n: it.level })}</span>
                    <b className={it.status === 'VALID' ? 'ok' : 'bad'}>{it.status}</b>
                    <span className="cmp-casc-n">{it.kind} {it.name}</span>
                    {(it.err || (it.errors && it.errors.length > 0)) && (
                      <span className="cmp-casc-e">
                        {it.err || `${it.errors[0].type} ${it.errors[0].line}:${it.errors[0].position} ${it.errors[0].text}`}
                      </span>
                    )}
                  </div>
                ))}
              </div>
            )}
            {report.cascadeNote && <div className="cmp-diag warn">{localizeText(report.cascadeNote, report.cascadeNoteRef)}</div>}
          </div>
        ) : null}

        {srcCopyErr && <div className="cmp err">{t('objCopyFail')}</div>}

        {/* 库里存的就是问号 —— 说清楚它不是显示问题,并给出自证方式 */}
        {looksMojibake && !src.loading && !src.err && (
          <div className="mojibake">
            <TriangleAlert size={14} />
            <div>
              <div className="mj-t">{t('objMojibakeTitle')}</div>
              <div className="mj-s">{t('objMojibakeSub')}</div>
              <code className="mj-q">SELECT line, dump(text, 1016) FROM all_source WHERE name = &apos;{src.name}&apos;</code>
            </div>
          </div>
        )}

        <div className="src-body scy">
          {src.loading ? <div className="shint">{t('treeLoading')}</div>
            : src.err ? <div className="shint err">{src.err}</div>
              /* highlightSqlHtml escapes every character before wrapping tokens
                 in classed spans, so inner HTML on server-returned DDL is safe here */
              : <pre dangerouslySetInnerHTML={{ __html: highlightSqlHtml(src.text ?? '') }} />}
        </div>
      </div>
    </div>
  )
}
