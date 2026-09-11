import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery } from '@tanstack/react-query'
import {
  Ban, CircleAlert, GitBranch, Play, Plus, Rocket, ShieldCheck, Clock,
} from 'lucide-react'
import { CODE_OK } from '@/api/http'
import { connectionsQueryOptions } from '@/api/modules/connections'
import { reviewApi } from '@/api/modules/review'
import {
  useAbortRelease, useContinueStage, useCreateRelease, usePipelines, useRelease, useReleases,
  type CreateReleaseBody, type ReleaseScope,
} from '@/hooks/usePipeline'
import { StageStrip, StageTypeIcon, type StageNode } from '@/components/pipeline/StageStrip'
import { Badge } from '@/components/common/Badge'
import { Button } from '@/components/common/Button'
import { Modal } from '@/components/common/Modal'
import { Segmented } from '@/components/common/Segmented'
import { Empty, ErrorState, Loading } from '@/components/common/States'
import type {
  Connection, Pipeline, Release, ReleaseStage, ReviewResult,
} from '@/types'

/** 执行入口。后端只认「立即建单」,另外两条在别处,选中时由提示说清楚去哪。 */
type ExecMode = 'now' | 'scheduled' | 'git'

export default function ChangesPage() {
  const { t } = useTranslation()
  const [scope, setScope] = useState<ReleaseScope>('mine')
  const [openId, setOpenId] = useState(0)
  const [openStageId, setOpenStageId] = useState(0)
  const [formOpen, setFormOpen] = useState(false)
  const [abortAsk, setAbortAsk] = useState(false)

  const list = useReleases(scope)
  const items = list.data?.items ?? []
  // 列表回来之前 openId 还是 0;默认落在第一张单上,省掉"进来先点一下"。
  const currentId = openId || items[0]?.id || 0
  const detail = useRelease(currentId)
  const open = detail.data ?? null

  const advance = useContinueStage()
  const abort = useAbortRelease()

  const gate = open ? waitingGate(open) : null
  const stages: StageNode[] = (open?.stages ?? []).map((s) => ({
    key: s.id, name: s.name, type: s.type, status: s.status, meta: duration(s),
  }))
  const openStage = open?.stages.find((s) => s.id === openStageId) ?? null

  function select(r: Release) {
    setOpenId(r.id)
    setOpenStageId(0)
  }

  return (
    <div className="page chg">
      <header className="page-head">
        <div>
          <h1>{t('chgTitle')}</h1>
          <p>{t('chgSub')}</p>
        </div>
        <div className="grow">
          <Segmented<ReleaseScope>
            value={scope}
            options={[
              { value: 'mine', label: t('chgScopeMine') },
              { value: 'all', label: t('chgScopeAll') },
            ]}
            onChange={(v) => { setScope(v); setOpenId(0) }}
          />
          <Button variant="primary" onClick={() => setFormOpen(true)}>
            <Plus size={15} />{t('chgNew')}
          </Button>
        </div>
      </header>

      <div className="chg-wrap">
        <aside className="chg-list">
          {list.isLoading && <Loading />}
          {list.error && <ErrorState error={list.error} retry={() => list.refetch()} />}
          {!list.isLoading && !list.error && !items.length && <Empty hint={t('chgEmpty')} />}
          {items.map((r) => (
            <button
              key={r.id}
              type="button"
              className={`chg-item${r.id === currentId ? ' on' : ''}`}
              onClick={() => select(r)}
            >
              <span className="chg-item-top">
                <span className="chg-no">{r.relNo}</span>
                {r.changeType && <span className={`chg-ct ${r.changeType}`}>{r.changeType.toUpperCase()}</span>}
                <Badge tone={runTone(r.status)}>{t(`stRun_${r.status}`)}</Badge>
              </span>
              <span className="chg-item-title">{r.title}</span>
              <span className="chg-item-meta">
                <span className="chg-tag">{r.env}</span>
                <span>{r.instance}</span>
                {r.database && <span className="dim">/ {r.database}</span>}
              </span>
              <span className="chg-item-meta">
                <span className="dim">{r.pipelineName}</span>
                <span className="dim">·</span>
                <span className="dim">{r.creator}</span>
                <span className="dim chg-when">{fmt(r.createdAt)}</span>
              </span>
            </button>
          ))}
        </aside>

        {/* 换一张单时详情要重取:这一下要明说是在取,而不是退回"左边选一张"。 */}
        {!open && (
          <section className="chg-detail">
            {detail.isLoading && <Loading />}
            {detail.error && <ErrorState error={detail.error} retry={() => detail.refetch()} />}
            {!detail.isLoading && !detail.error && <Empty hint={t('chgPickRelease')} />}
          </section>
        )}
        {open && (
          <section className="chg-detail">
            <header className="chg-dhead">
              <span className="chg-dicon"><Rocket size={17} /></span>
              <div className="chg-dtitles">
                <div className="chg-dt">{open.relNo} · {open.title}</div>
                <div className="chg-ds">
                  {open.instance}{open.database ? ` / ${open.database}` : ''} · {open.pipelineName} · {fmt(open.createdAt)}
                  {open.source === 'api' && ` · ${t('chgFromApi', { name: open.clientName || 'API' })}`}
                </div>
              </div>
              <Badge tone={riskTone(open.risk)}>{t('chgRisk')} {(open.risk || 'low').toUpperCase()}</Badge>
              {/*
                「推进」只在服务端把某一道人工闸摆成 waiting 时才亮。不去用
                status / 是不是发起人 / 执行过没有 拼一个本地版本的"可以执行了" ——
                两份判断迟早分叉,分叉的样子就是一个亮着却点不动的按钮。
              */}
              {gate && (
                <Button
                  variant="primary"
                  disabled={advance.isPending}
                  onClick={() => advance.mutate({ releaseId: open.id, stageId: gate.id })}
                >
                  <Play size={14} />
                  {gate.type === 'execute' ? t('chgConfirmExec') : t('chgAdvance')}
                </Button>
              )}
              {(open.status === 'pending' || open.status === 'waiting' || open.status === 'running') && (
                <Button variant="danger" onClick={() => setAbortAsk(true)}>
                  <Ban size={14} />{t('chgAbort')}
                </Button>
              )}
            </header>

            <div className="chg-body">
              <div className="chg-main">
                <StageStrip
                  stages={stages}
                  selectedKey={openStageId || undefined}
                  onSelect={(s) => setOpenStageId(openStageId === s.key ? 0 : Number(s.key))}
                />

                {open.error && (
                  <div className="notice danger"><CircleAlert size={14} />{open.error}</div>
                )}

                {openStage
                  ? (
                    <StageBox
                      stage={openStage}
                      busy={advance.isPending}
                      onAdvance={() => advance.mutate({ releaseId: open.id, stageId: openStage.id })}
                    />
                  )
                  : <div className="chg-hint">{t('chgPickStage')}</div>}
              </div>

              <aside className="chg-side">
                <SidePanel release={open} />
              </aside>
            </div>
          </section>
        )}
      </div>

      <NewChangeModal
        open={formOpen}
        onClose={() => setFormOpen(false)}
        onCreated={(id) => { setFormOpen(false); setOpenId(id); setOpenStageId(0) }}
      />

      <Modal
        open={abortAsk}
        title={t('chgAbortTitle')}
        sub={t('chgAbortSub')}
        onClose={() => setAbortAsk(false)}
        footer={
          <>
            <Button variant="ghost" onClick={() => setAbortAsk(false)}>{t('cancel')}</Button>
            <Button
              variant="danger"
              disabled={abort.isPending}
              onClick={() => open && abort.mutate(open.id, { onSuccess: () => setAbortAsk(false) })}
            >
              {t('chgAbort')}
            </Button>
          </>
        }
      >
        <p className="chg-hint">{open?.relNo} · {open?.title}</p>
      </Modal>
    </div>
  )
}

/** 阶段详情:日志,以及规范审查阶段随身带着的那份审查结果。 */
function StageBox({
  stage, busy, onAdvance,
}: { stage: ReleaseStage; busy: boolean; onAdvance: () => void }) {
  const { t } = useTranslation()
  const findings = parseFindings(stage)
  const isGate = (stage.type === 'manual' || stage.type === 'execute') && stage.status === 'waiting'

  return (
    <div className="chg-stage">
      <header className="chg-stage-head">
        <StageTypeIcon type={stage.type} size={14} />
        <span className="chg-stage-name">{stage.name || t(`stType_${stage.type}`)}</span>
        <Badge tone={runTone(stage.status)}>{t(`stRun_${stage.status}`)}</Badge>
        {stage.approvalNo && <span className="chg-apno">{stage.approvalNo}</span>}
        {isGate && (
          <Button variant="primary" disabled={busy} onClick={onAdvance}>
            <Play size={13} />{stage.type === 'execute' ? t('chgConfirmExec') : t('chgAdvance')}
          </Button>
        )}
      </header>
      {stage.log ? <pre className="chg-log">{stage.log}</pre> : <div className="chg-hint">{t('chgNoLog')}</div>}
      {findings && <FindingList result={findings} />}
    </div>
  )
}

function FindingList({ result, max = 20 }: { result: ReviewResult; max?: number }) {
  const { t } = useTranslation()
  return (
    <>
      <div className="chg-counts">
        {t('chgCounts', {
          s: result.statements, e: result.errors, w: result.warnings, i: result.infos,
        })}
      </div>
      {result.findings.slice(0, max).map((f, i) => (
        <div key={i} className={`chg-find lv-${f.level}`}>
          <div className="chg-find-name">
            {f.name}
            <span className="chg-find-loc">{t('chgStmtAt', { n: f.stmt, line: f.line })}</span>
          </div>
          <div className="chg-find-msg">{f.message}</div>
          <div className="chg-find-sql">{f.sql}</div>
        </div>
      ))}
    </>
  )
}

/**
 * 右侧栏 —— 一张单最后要交代的四件事:改了什么、规范怎么说、谁签的字、出事能回到哪。
 *
 * 四块全部从阶段里读。没有那种阶段就明说"这条流程没有",而不是留一块空白 ——
 * "没配备份点"和"备份还没跑"是两回事,只有前者需要人去改流程。
 */
function SidePanel({ release }: { release: Release }) {
  const { t } = useTranslation()
  const reviewStage = release.stages.find((s) => s.type === 'review' && s.findings)
  const findings = reviewStage ? parseFindings(reviewStage) : null
  const approvals = release.stages.filter((s) => s.type === 'approve')
  const backups = release.stages.filter((s) => s.type === 'backup')

  return (
    <>
      <div className="chg-panel">
        <div className="chg-panel-head"><GitBranch size={13} />{t('chgDiff')}</div>
        <pre className="chg-log sm">{release.sql || t('chgScriptRef')}</pre>
        {release.reason && <div className="chg-panel-note">{t('chgFormReason')}: {release.reason}</div>}
      </div>

      <div className="chg-panel">
        <div className="chg-panel-head"><ShieldCheck size={13} />{t('chgLint')}</div>
        {!findings && <div className="chg-hint">{t('chgLintNone')}</div>}
        {findings && (
          <>
            <Badge tone={findings.passed ? 'success' : 'danger'}>
              {findings.passed ? t('chgLintPassed') : t('chgLintBlocked')}
            </Badge>
            <FindingList result={findings} max={4} />
          </>
        )}
      </div>

      <div className="chg-panel">
        <div className="chg-panel-head"><StageTypeIcon type="approve" />{t('chgSign')}</div>
        {!approvals.length && <div className="chg-hint">{t('chgSignNone')}</div>}
        {approvals.map((s) => (
          <div key={s.id} className="chg-panel-row">
            <span>{s.name || t('stType_approve')}</span>
            {s.approvalNo && <span className="chg-apno">{s.approvalNo}</span>}
            <Badge tone={runTone(s.status)}>{t(`stRun_${s.status}`)}</Badge>
          </div>
        ))}
      </div>

      <div className="chg-panel">
        <div className="chg-panel-head"><StageTypeIcon type="backup" />{t('chgBackup')}</div>
        {!backups.length && <div className="chg-hint">{t('chgBackupNone')}</div>}
        {backups.map((s) => (
          <div key={s.id}>
            <div className="chg-panel-row">
              <span>{s.name || t('stType_backup')}</span>
              <Badge tone={runTone(s.status)}>{t(`stRun_${s.status}`)}</Badge>
            </div>
            {cfgGet(s.config, 'sql') && <pre className="chg-log sm">{cfgGet(s.config, 'sql')}</pre>}
          </div>
        ))}
      </div>
    </>
  )
}

function NewChangeModal({
  open, onClose, onCreated,
}: { open: boolean; onClose: () => void; onCreated: (id: number) => void }) {
  const { t } = useTranslation()
  const { data: conns } = useQuery(connectionsQueryOptions())
  const { data: pipelines } = usePipelines()
  const create = useCreateRelease()
  const [mode, setMode] = useState<ExecMode>('now')
  const [needMfa, setNeedMfa] = useState(false)
  const [f, setF] = useState<CreateReleaseBody>({
    title: '', pipelineId: 0, connectionId: 0, database: '', sql: '', changeType: '', reason: '', mfaCode: '',
  })
  const set = (patch: Partial<CreateReleaseBody>) => setF((p) => ({ ...p, ...patch }))

  // 预检跑的是规范审查阶段将来会跑的那套库:在单号还不存在的时候,就先看见
  // 什么会把自己拦下来。
  const preflight = useMutation({
    mutationFn: () => reviewApi.reviewCheck({ connectionId: f.connectionId, sql: f.sql ?? '' }),
  })

  const usable = (pipelines ?? []).filter((p: Pipeline) => p.enabled)
  const connList = (conns ?? []) as Connection[]
  const ready = Boolean(f.title.trim() && f.connectionId && (f.sql ?? '').trim())

  function submit() {
    create.mutate(f, {
      onSuccess: (env) => {
        // 走到这里只剩两种码:0 建单成功,42800 要动态码(别的码在 hook 里已经抛了)。
        if (env.code !== CODE_OK) { setNeedMfa(true); return }
        onCreated(env.data.id)
      },
    })
  }

  return (
    <Modal
      open={open}
      title={t('chgNew')}
      sub={t('chgNewSub')}
      width={560}
      onClose={onClose}
      footer={
        <>
          <Button
            disabled={!f.connectionId || !(f.sql ?? '').trim() || preflight.isPending}
            onClick={() => preflight.mutate()}
          >
            <ShieldCheck size={14} />{t('chgPreflight')}
          </Button>
          <Button
            variant="primary"
            disabled={!ready || create.isPending || mode !== 'now'}
            onClick={submit}
          >
            <Rocket size={14} />{t('chgSubmit')}
          </Button>
        </>
      }
    >
      <div className="fld">
        <label>{t('chgFormTitle')}</label>
        <input value={f.title} onChange={(e) => set({ title: e.target.value })} />
      </div>
      <div className="fld-2">
        <div className="fld">
          <label>{t('chgFormInstance')}</label>
          <select
            value={f.connectionId}
            onChange={(e) => {
              const id = Number(e.target.value)
              const c = connList.find((x) => x.id === id)
              // 实例自带默认库时先填上 —— 大多数单子就是往那个库里改。
              set({ connectionId: id, database: c?.database ?? '' })
            }}
          >
            <option value={0}>—</option>
            {connList.map((c) => <option key={c.id} value={c.id}>{c.env}-{c.name}</option>)}
          </select>
        </div>
        <div className="fld">
          <label>{t('chgFormDatabase')}</label>
          <input value={f.database ?? ''} onChange={(e) => set({ database: e.target.value })} />
        </div>
      </div>
      <div className="fld-2">
        <div className="fld">
          <label>{t('chgFormPipeline')}</label>
          <select value={f.pipelineId} onChange={(e) => set({ pipelineId: Number(e.target.value) })}>
            <option value={0}>—</option>
            {usable.map((p) => (
              <option key={p.id} value={p.id}>
                {p.tierCode ? `${p.name} (${p.tierCode.toUpperCase()})` : p.name}
              </option>
            ))}
          </select>
        </div>
        <div className="fld">
          {/* 声明了类型就必须与内容一致(服务端强校验,DML / DDL 不得同单)。 */}
          <label>{t('chgFormType')}</label>
          <Segmented<string>
            value={f.changeType ?? ''}
            options={[
              { value: '', label: t('chgTypeAuto') },
              { value: 'dml', label: 'DML' },
              { value: 'ddl', label: 'DDL' },
            ]}
            onChange={(v) => set({ changeType: v })}
          />
        </div>
      </div>

      <div className="fld">
        <label>{t('chgMode')}</label>
        <Segmented<ExecMode>
          value={mode}
          options={[
            { value: 'now', label: t('chgModeNow') },
            { value: 'scheduled', label: t('chgModeScheduled') },
            { value: 'git', label: t('chgModeGit') },
          ]}
          onChange={setMode}
        />
        <div className={`notice${mode === 'now' ? '' : ' warn'} chg-mode-hint`}>
          {mode === 'now' && <Play size={13} />}
          {mode === 'scheduled' && <Clock size={13} />}
          {mode === 'git' && <GitBranch size={13} />}
          {t(mode === 'now' ? 'chgModeNowHint' : mode === 'scheduled' ? 'chgModeScheduledHint' : 'chgModeGitHint')}
        </div>
      </div>

      <div className="fld">
        <label>{t('chgFormSql')}</label>
        <textarea value={f.sql ?? ''} spellCheck={false} onChange={(e) => set({ sql: e.target.value })} />
      </div>
      <div className="fld">
        <label>{t('chgFormReason')}</label>
        <input value={f.reason ?? ''} onChange={(e) => set({ reason: e.target.value })} />
      </div>
      {needMfa && (
        <div className="fld">
          <label>{t('chgMfa')}</label>
          {/* 这一段是被动弹出来的:用户刚按下提交,手还在键盘上,接下来敲的六位数字
              必须有地方落。不聚焦的话它们会落进后面的表单里。 */}
          <input autoFocus inputMode="numeric" maxLength={6} autoComplete="one-time-code"
            value={f.mfaCode ?? ''} onChange={(e) => set({ mfaCode: e.target.value })} />
        </div>
      )}

      {preflight.data && (
        <div className={`notice${preflight.data.result.passed ? '' : ' danger'}`}>
          <span>
            {preflight.data.result.passed ? t('chgLintPassed') : t('chgLintBlocked')} ·{' '}
            {t('chgCounts', {
              s: preflight.data.result.statements, e: preflight.data.result.errors,
              w: preflight.data.result.warnings, i: preflight.data.result.infos,
            })}
          </span>
        </div>
      )}
      <div className="chg-hint">{t('chgSubmitHint')}</div>
    </Modal>
  )
}

// ---- 小工具 ----

/**
 * 正停着等人的那道闸。
 *
 * `execute` 是内置的执行闸:流程跑到落库这一步一定先停在这里,有人确认才下发。
 * `manual` 是流程里自己配的确认点。两者共用一个入口,文案区分语义。
 */
function waitingGate(r: Release): ReleaseStage | null {
  return r.stages.find(
    (s) => s.status === 'waiting' && (s.type === 'manual' || s.type === 'execute'),
  ) ?? null
}

function parseFindings(st: ReleaseStage): ReviewResult | null {
  if (!st.findings) return null
  // 审查结果是随阶段存下来的 JSON:规则库后来改了,这张被拦下的单也还讲得清
  // 当时是被哪一条拦的。
  try { return JSON.parse(st.findings) as ReviewResult } catch { return null }
}

function cfgGet(config: string, key: string): string {
  try { return (JSON.parse(config || '{}')[key] ?? '') as string } catch { return '' }
}

function duration(st: ReleaseStage): string {
  if (!st.startedAt) return ''
  const end = st.finishedAt ? new Date(st.finishedAt).getTime() : Date.now()
  const ms = end - new Date(st.startedAt).getTime()
  return ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}s`
}

function fmt(s: string | null): string {
  return s ? new Date(s).toLocaleString('en-GB') : '—'
}

function runTone(status: string) {
  switch (status) {
    case 'success': return 'success' as const
    case 'failed': return 'danger' as const
    case 'running': return 'accent' as const
    case 'waiting': case 'pending': return 'warning' as const
    default: return 'neutral' as const
  }
}

function riskTone(risk: string) {
  return risk === 'high' ? 'danger' as const : risk === 'mid' ? 'warning' as const : 'neutral' as const
}
