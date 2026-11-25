// 变更发布页的「新建发布」弹窗。每次打开都是一个新的组件:表单自然从头来。
import { useMemo, useState } from 'react'
import clsx from 'clsx'
import { Rocket, ShieldCheck, X } from 'lucide-react'
import VButton from '@/components/common/VButton'
import VSelect from '@/components/common/VSelect'
import api from '@/api'
import { CODE_MFA_REQUIRED, envError, localizeText } from '@/api/http'
import { reviewGroups } from '@/lib/reviewGroups'
import { reviewRuleName } from '@/lib/builtinNames'
import { useUIStore } from '@/stores/ui'
import { useI18n } from '@/hooks/useI18n'
import { useMountEffect } from '@/hooks/effects'
import type { Connection, Pipeline, ReviewResult } from '@/types'
import './ReleasesView.scoped.css'

interface Props {
  conns: Connection[]
  pipelines: Pipeline[]
  onClose: () => void
  /** 已提交:关掉弹窗、重拉列表并摊开新单。等它做完才解除「提交中」。 */
  onCreated: (id: number | undefined) => Promise<void>
}

function pipeLabel(p: Pipeline) {
  return p.tierCode ? `${p.name} (${p.tierCode.toUpperCase()})` : p.name
}

export default function ReleaseForm({ conns, pipelines, onClose, onCreated }: Props) {
  const { t, te } = useI18n()
  const ui = useUIStore.getState() // 只用动作,不读状态
  const zhText = (k: string) => (te(k, 'zh') ? t(k, {}, { locale: 'zh' }) : undefined)
  const ruleName = (code: string, stored: string) => reviewRuleName(code, stored, t, zhText)

  // changeType: '' = 自动推断;声明了就必须与内容一致(后端强校验,DML/DDL 不得同单)
  const [f, setF] = useState(() => {
    const firstPipe = pipelines.find((p) => p.enabled && p.isDefault) || pipelines.find((p) => p.enabled)
    return {
      title: '', pipelineId: firstPipe?.id || 0, connectionId: conns[0]?.id || 0,
      database: '', sql: '', changeType: '', reason: '', mfaCode: '',
    }
  })
  const patch = (fields: Partial<typeof f>) => setF((v) => ({ ...v, ...fields }))
  const [needMfa, setNeedMfa] = useState(false)
  const [busy, setBusy] = useState(false)
  const [preview, setPreview] = useState<ReviewResult | null>(null)
  const previewGroups = useMemo(() => reviewGroups(preview), [preview])
  const [dbOptions, setDbOptions] = useState<string[]>([])

  const connLabels = conns.map((c) => `${c.env}-${c.name}`)
  const conn = conns.find((x) => x.id === f.connectionId)
  const connLabel = conn ? `${conn.env}-${conn.name}` : ''
  function setConnLabel(l: string) {
    const c = conns.find((x) => `${x.env}-${x.name}` === l)
    if (c) { patch({ connectionId: c.id }); void loadDbs(c.id) }
  }
  const pipeLabels = pipelines.filter((p) => p.enabled).map(pipeLabel)
  const pipe = pipelines.find((x) => x.id === f.pipelineId)
  const pipeLabelSel = pipe ? pipeLabel(pipe) : ''
  function setPipeLabel(l: string) {
    const p = pipelines.find((x) => pipeLabel(x) === l)
    if (p) patch({ pipelineId: p.id })
  }

  async function loadDbs(id: number) {
    patch({ database: '' })
    setDbOptions([])
    try {
      const sc = await api.connectionSchema(id)
      const names = sc.databases.map((d) => d.name)
      setDbOptions(names)
      const c = conns.find((x) => x.id === id)
      if (c?.database && names.includes(c.database)) patch({ database: c.database })
      else if (names.length) patch({ database: names[0] })
    } catch { /* the field stays free text */ }
  }
  useMountEffect(() => { if (f.connectionId) void loadDbs(f.connectionId) })

  // Pre-flight review: the same library the pipeline's review stage will run, so
  // the operator sees what would block them before a release number exists.
  async function preflight() {
    if (!f.sql.trim() || !f.connectionId) return
    try {
      const r = await api.reviewCheck({ connectionId: f.connectionId, sql: f.sql })
      setPreview(r.result)
    } catch (e) { ui.notifyError(e, t('actionFailed')) }
  }

  async function submit() {
    if (!f.title.trim() || !f.connectionId || !f.sql.trim()) {
      ui.notify(t('rlFormIncomplete'), 'error'); return
    }
    setBusy(true)
    try {
      const env = await api.createRelease({
        title: f.title.trim(), pipelineId: f.pipelineId, connectionId: f.connectionId,
        database: f.database, sql: f.sql, changeType: f.changeType, reason: f.reason.trim(), mfaCode: f.mfaCode.trim(),
      })
      if (env.code === CODE_MFA_REQUIRED) { setNeedMfa(true); ui.notify(t('rlNeedMfa'), 'error'); return }
      if (env.code !== 0) { ui.notifyError(envError(env), t('actionFailed')); return }
      await onCreated(env.data?.id)
    } catch (e) { ui.notifyError(e, t('actionFailed')) } finally { setBusy(false) }
  }

  return (
    <div className="overlay">
      <div className="mask" onClick={onClose} />
      <div className="modal">
        <div className="mhead">
          <div className="mic"><Rocket size={18} color="var(--accent-text)" /></div>
          <div><div className="mt">{t('rlNew')}</div><div className="ms">{t('rlNewSub')}</div></div>
          <X size={18} className="mx" onClick={onClose} />
        </div>
        <div className="mbody">
          <div><div className="fl">{t('rlTitle')}</div><input value={f.title} onChange={(e) => patch({ title: e.target.value })} className="in" placeholder={t('rlTitlePh')} /></div>
          <div className="grid2">
            <div><div className="fl">{t('mInst')}</div><VSelect value={connLabel} onChange={setConnLabel} options={connLabels} searchable /></div>
            <div>
              <div className="fl">{t('fDatabase')}</div>
              {dbOptions.length
                ? <VSelect value={f.database} onChange={(v) => patch({ database: v })} options={dbOptions} />
                : <input value={f.database} onChange={(e) => patch({ database: e.target.value })} className="in" />}
            </div>
          </div>
          <div><div className="fl">{t('rlPipeline')}</div><VSelect value={pipeLabelSel} onChange={setPipeLabel} options={pipeLabels} /></div>
          <div>
            <div className="fl">{t('rlChangeType')}</div>
            <div className="ctseg">
              <span className={clsx('ct', f.changeType === '' && 'on')} onClick={() => patch({ changeType: '' })}>{t('rlCtAuto')}</span>
              <span className={clsx('ct', f.changeType === 'dml' && 'on')} onClick={() => patch({ changeType: 'dml' })}>DML</span>
              <span className={clsx('ct', f.changeType === 'ddl' && 'on')} onClick={() => patch({ changeType: 'ddl' })}>DDL</span>
            </div>
            <div className="hint">{t('rlChangeTypeHint')}</div>
          </div>
          <div><div className="fl">{t('rlContent')}</div><textarea value={f.sql} onChange={(e) => patch({ sql: e.target.value })} className="sqlbox" spellCheck={false} placeholder={t('rlSqlPh')} /></div>
          <div><div className="fl">{t('reason')}</div><input value={f.reason} onChange={(e) => patch({ reason: e.target.value })} className="in" placeholder={t('rlReasonPh')} /></div>
          {needMfa && <div><div className="fl">{t('loginMfaLabel')}</div><input value={f.mfaCode} onChange={(e) => patch({ mfaCode: e.target.value })} className="in" placeholder={t('loginMfaPh')} /></div>}

          {preview && (
            <div className={clsx('pv', preview.passed ? 'ok' : 'bad')}>
              {preview.passed ? t('srPassed') : t('srBlocked')} ·{' '}
              {t('srCounts', { s: preview.statements, e: preview.errors, w: preview.warnings, i: preview.infos })}{' '}
              {previewGroups.slice(0, 8).map((g) => (
                <div key={g.code + g.level} className="pvline">
                  {' '}[{g.level.toUpperCase()}] {ruleName(g.code, g.name)} · {t('srRuleHits', { n: g.count })} — {localizeText(g.message, g.messageRef)}
                </div>
              ))}
            </div>
          )}
          <div className="hint">{t('rlSubmitHint')}</div>
        </div>
        <div className="mfoot">
          <VButton onClick={preflight}><ShieldCheck size={14} />{t('rlPreflight')}</VButton>
          <VButton variant="primary" disabled={busy} onClick={submit}><Rocket size={14} />{t('rlSubmit')}</VButton>
        </div>
      </div>
    </div>
  )
}
