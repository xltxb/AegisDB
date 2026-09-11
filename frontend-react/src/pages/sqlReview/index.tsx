import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import clsx from 'clsx'
import {
  SpellCheck, FlaskConical, Play, Plus, Search, Settings2, Trash2, X,
  CircleAlert, TriangleAlert, Info, CircleCheck,
} from 'lucide-react'
import {
  useReviewRules, useReviewCatalog, useSaveReviewRule, useDeleteReviewRule, useReviewCheck,
} from '@/hooks/useReview'
import { connectionsQueryOptions } from '@/api/modules/connections'
import { Card, CardHead } from '@/components/common/Card'
import { Badge, type BadgeTone } from '@/components/common/Badge'
import { Segmented } from '@/components/common/Segmented'
import { Switch } from '@/components/common/Switch'
import { Button } from '@/components/common/Button'
import { Modal } from '@/components/common/Modal'
import { Loading, ErrorState, Empty } from '@/components/common/States'
import type { Connection, ReviewCatalog, ReviewLevel, ReviewRule, ReviewSpec } from '@/types'

const LEVELS: ReviewLevel[] = ['error', 'warn', 'info']
/** 级别循环:阻断 → 告警 → 提示 → 阻断。 */
const NEXT_LEVEL: Record<ReviewLevel, ReviewLevel> = { error: 'warn', warn: 'info', info: 'error' }
const SPECS: ReviewSpec[] = ['critical', 'mandatory', 'recommended']

function specTone(s: string): BadgeTone {
  if (s === 'critical') return 'danger'
  if (s === 'mandatory') return 'warning'
  if (s === 'recommended') return 'accent'
  return 'neutral'
}
const LevelIcon = ({ level }: { level: string }) =>
  level === 'error' ? <CircleAlert size={14} />
    : level === 'warn' ? <TriangleAlert size={14} />
      : <Info size={14} />

const FALLBACK: ReviewCatalog = { dialects: ['all'], categories: [], levels: LEVELS, specs: SPECS }

export default function SqlReviewPage() {
  const { t } = useTranslation()
  const { data: rules, isLoading, error, refetch } = useReviewRules()
  const { data: cat } = useReviewCatalog()
  const save = useSaveReviewRule()
  const del = useDeleteReviewRule()

  const [dialect, setDialect] = useState('all')
  const [category, setCategory] = useState('')
  const [spec, setSpec] = useState('')
  const [q, setQ] = useState('')
  const [editing, setEditing] = useState<ReviewRule | null>(null)
  const [creating, setCreating] = useState(false)

  const catalog = cat ?? FALLBACK
  const list: ReviewRule[] = rules ?? []

  /*
    在客户端筛是对的:规则库一次性全量拉下来(接口不分页),所以筛出来的就是全部。
    这和审批页那个必须走服务端的搜索不是一回事 —— 那边列表分页,只筛当前页会对
    第三页的工单谎称"没有"。
  */
  const kw = q.trim().toLowerCase()
  const shown = list.filter((r) => {
    const dOk = dialect === 'all' || r.dialect === 'all' || r.dialect.split(',').includes(dialect)
    const cOk = !category || r.category === category
    const sOk = !spec || (spec === 'none' ? !r.spec : r.spec === spec)
    const kOk = !kw || r.name.toLowerCase().includes(kw) || r.code.toLowerCase().includes(kw)
    return dOk && cOk && sOk && kOk
  })
  const filtered = !!(kw || category || spec || dialect !== 'all')

  const specTabs = [...(catalog.specs ?? SPECS), 'none']
    .map((s) => ({ key: s, n: list.filter((r) => (s === 'none' ? !r.spec : r.spec === s)).length }))
    .filter((x) => x.n > 0)

  /** 级别与启用是两个立刻写回的决定,所以这里不设"保存"按钮 —— 少一步可忘的动作。 */
  function patch(r: ReviewRule, fields: Partial<ReviewRule>) {
    save.mutate({
      id: r.id,
      body: {
        name: r.name, dialect: r.dialect, category: r.category, level: r.level,
        enabled: r.enabled, params: r.params, message: r.message, sortOrder: r.sortOrder,
        spec: r.spec, specRef: r.specRef, ...fields,
      },
    })
  }

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>{t('srTitle')}</h1>
          <p>{t('srSub')}</p>
        </div>
        <div className="grow">
          <Button variant="primary" onClick={() => setCreating(true)}>
            <Plus size={15} />{t('srNewRule')}
          </Button>
        </div>
      </header>

      {isLoading && <Loading />}
      {error && <ErrorState error={error} retry={() => refetch()} />}

      {!isLoading && !error && (
        <div className="sr-wrap">
          <Card className="sr-lib">
            <CardHead icon={<SpellCheck size={17} />} title={t('srLibTitle')} sub={t('srLibSub')} />

            <div className="sr-filters">
              <div className="sr-segscroll">
                <Segmented
                  value={dialect}
                  options={catalog.dialects.map((d) => ({
                    value: d, label: d === 'all' ? t('srAllDialects') : d.toUpperCase(),
                  }))}
                  onChange={setDialect}
                />
              </div>
              <div className="sr-search">
                <Search size={14} />
                <input
                  value={q}
                  spellCheck={false}
                  placeholder={t('srSearchPh')}
                  onChange={(e) => setQ(e.target.value)}
                />
                {!!q && (
                  <button type="button" title={t('srClear')} onClick={() => setQ('')}><X size={13} /></button>
                )}
              </div>
            </div>

            {/*
              规范分级与规则分类挤在一行,中间留一道竖线:它们回答的是两个问题 ——
              「规范怎么定性」和「查的是哪一类写法」。不隔开就等于说它们是同一组开关。
            */}
            <div className="sr-tagbar">
              <button
                type="button"
                className={clsx('sr-tg', !spec && !category && 'on')}
                onClick={() => { setSpec(''); setCategory('') }}
              >
                {t('srAllRules')}
              </button>
              <span className="sr-tsep" />
              {specTabs.map((s) => (
                <button
                  key={s.key}
                  type="button"
                  className={clsx('sr-tg', spec === s.key && 'on')}
                  onClick={() => setSpec(spec === s.key ? '' : s.key)}
                >
                  {t(`srSpec_${s.key}`)}<b>{s.n}</b>
                </button>
              ))}
              {!!catalog.categories.length && <span className="sr-tsep" />}
              {catalog.categories.map((c) => (
                <button
                  key={c}
                  type="button"
                  className={clsx('sr-tg', category === c && 'on')}
                  onClick={() => setCategory(category === c ? '' : c)}
                >
                  {c}
                </button>
              ))}
            </div>

            <div className="sr-rules">
              {shown.map((r) => (
                <div key={r.id} className={clsx('sr-rule', !r.enabled && 'off')}>
                  <div className="sr-rmain">
                    <div className="sr-rtop">
                      <span className="sr-rname">{r.name}</span>
                      <span className="sr-rcode">{r.code}</span>
                      {r.kind === 'regex' && <Badge tone="accent">{t('srCustom')}</Badge>}
                      {/*
                        只有**限定方言**的规则挂徽标。每一行都写一遍 "ALL" 是 24 个
                        一模一样的小方块,而那几条真正只管某一种库的规则反而淹了。
                      */}
                      {r.dialect && r.dialect !== 'all' && r.dialect.split(',').map((d) => (
                        <span key={d} className="sr-rdia">{d.trim().toUpperCase()}</span>
                      ))}
                    </div>
                    <div className="sr-rmsg">{r.message || r.params || '—'}</div>
                    {/*
                      出处:被这条规则拦下来的人要能回去读原文。没有出处的写成
                      「平台内置」,免得有人拿平台的默认值当规范原文去引用。
                    */}
                    <div className="sr-rsrc">
                      <Badge tone={specTone(r.spec)}>{t(`srSpec_${r.spec || 'none'}`)}</Badge>
                      {r.specRef && <span className="sr-rref">{r.specRef}</span>}
                    </div>
                  </div>

                  <button
                    type="button"
                    className={clsx('sr-lvbtn', `lv-${r.level}`)}
                    title={t('srCycleHint')}
                    disabled={save.isPending}
                    onClick={() => patch(r, { level: NEXT_LEVEL[r.level] })}
                  >
                    <LevelIcon level={r.level} />
                    {t(`srLv_${r.level}`)}
                  </button>

                  <Switch
                    checked={r.enabled}
                    disabled={save.isPending}
                    onChange={(v) => patch(r, { enabled: v })}
                  />

                  <Button variant="ghost" title={t('srEdit')} onClick={() => setEditing(r)}>
                    <Settings2 size={14} />
                  </Button>
                  {r.kind === 'regex' && (
                    <Button
                      variant="ghost"
                      title={t('srDelete')}
                      disabled={del.isPending}
                      onClick={() => del.mutate(r.id)}
                    >
                      <Trash2 size={13} />
                    </Button>
                  )}
                </div>
              ))}

              {!shown.length && (
                <Empty hint={filtered ? t('srNoRulesFiltered') : t('srNoRules')} />
              )}
            </div>
          </Card>

          <SelfCheckCard catalog={catalog} />
        </div>
      )}

      {(creating || editing) && (
        <RuleModal
          rule={editing}
          catalog={catalog}
          busy={save.isPending}
          onClose={() => { setCreating(false); setEditing(null) }}
          onSubmit={(id, body) =>
            save.mutate({ id, body }, { onSuccess: () => { setCreating(false); setEditing(null) } })
          }
        />
      )}
    </div>
  )
}

/**
 * 上线前自查。
 *
 * 选了实例就以实例为准(它决定哪些规则可能适用),没选才退回方言 —— 两个都送过去
 * 时服务端也是这个优先级,这里跟它保持一致,免得界面上写着 mysql、实际按 oracle 判。
 */
function SelfCheckCard({ catalog }: { catalog: ReviewCatalog }) {
  const { t } = useTranslation()
  const { data: conns } = useQuery(connectionsQueryOptions())
  const check = useReviewCheck()
  const [connId, setConnId] = useState(0)
  const [dialect, setDialect] = useState('mysql')
  const [sql, setSql] = useState('')

  const list: Connection[] = conns ?? []
  const result = check.data?.result

  return (
    <Card className="sr-check">
      <CardHead icon={<FlaskConical size={16} />} title={t('srCheckTitle')} sub={t('srCheckSub')} />
      <div className="sr-checkbody">
        <div className="fld">
          <label>{t('srTarget')}</label>
          <select value={connId} onChange={(e) => setConnId(Number(e.target.value))}>
            <option value={0}>{t('srNoInstance')}</option>
            {list.map((c) => <option key={c.id} value={c.id}>{c.env}-{c.name}</option>)}
          </select>
        </div>

        {!connId && (
          <div className="fld">
            <label>{t('srDialect')}</label>
            <select value={dialect} onChange={(e) => setDialect(e.target.value)}>
              {catalog.dialects.filter((d) => d !== 'all').map((d) => (
                <option key={d} value={d}>{d.toUpperCase()}</option>
              ))}
            </select>
          </div>
        )}

        <div className="fld">
          <label>SQL</label>
          <textarea
            value={sql}
            spellCheck={false}
            placeholder={t('srSqlPh')}
            onChange={(e) => setSql(e.target.value)}
          />
        </div>

        <Button
          variant="primary"
          disabled={check.isPending || !sql.trim()}
          onClick={() => check.mutate({
            connectionId: connId || undefined,
            dialect: connId ? undefined : dialect,
            sql,
          })}
        >
          <Play size={14} />{check.isPending ? t('srChecking') : t('srRunCheck')}
        </Button>

        {!result && <div className="sr-resempty">{t('srResultIdle')}</div>}

        {result && (
          <div className="sr-res">
            {/*
              passed 的含义是「没有 error 级发现」,不是「一条都没查出来」——
              告警从不拦人,把两者混成一句会让人以为告警可以不看。
            */}
            <div className={clsx('sr-rsum', result.passed ? 'ok' : 'bad')}>
              {result.passed ? <CircleCheck size={15} /> : <CircleAlert size={15} />}
              <span className="sr-rst">{t(result.passed ? 'srPassed' : 'srBlocked')}</span>
              <span className="sr-rcnt">
                {t('srCounts', {
                  s: result.statements, e: result.errors, w: result.warnings, i: result.infos,
                })}
              </span>
            </div>
            {result.findings.map((f, i) => (
              <div key={i} className={clsx('sr-find', `lv-${f.level}`)}>
                <LevelIcon level={f.level} />
                <div className="sr-fbody">
                  <div className="sr-fname">
                    {f.name}
                    <span className="sr-floc">{t('srStmtAt', { n: f.stmt, line: f.line })}</span>
                  </div>
                  <div className="sr-fmsg">{f.message}</div>
                  <div className="sr-fsql">{f.sql}</div>
                </div>
              </div>
            ))}
            {!result.findings.length && <div className="sr-nofind">{t('srNoFindings')}</div>}
          </div>
        )}
      </div>
    </Card>
  )
}

/**
 * 新增 / 编辑一条规则。
 *
 * 内置规则的 code 与检查器绑死,所以编辑时不给改;自定义规则的 params 是它的正则
 * 本体,直接以文本编辑 —— 拆成一堆输入框只会掩盖它其实就是一段 JSON。
 */
function RuleModal({
  rule, catalog, busy, onClose, onSubmit,
}: {
  rule: ReviewRule | null
  catalog: ReviewCatalog
  busy: boolean
  onClose: () => void
  onSubmit: (id: number, body: Partial<ReviewRule>) => void
}) {
  const { t } = useTranslation()
  const [f, setF] = useState({
    code: rule?.code ?? '',
    name: rule?.name ?? '',
    dialect: rule?.dialect ?? 'all',
    category: rule?.category ?? catalog.categories[0] ?? 'dml',
    level: (rule?.level ?? 'warn') as ReviewLevel,
    message: rule?.message ?? '',
    params: rule?.params ?? '',
    spec: (rule?.spec ?? '') as ReviewSpec,
    specRef: rule?.specRef ?? '',
    /** 新建时填的是正则本体,提交前包成 params 的 JSON。 */
    pattern: '',
  })
  const set = (patch: Partial<typeof f>) => setF((p) => ({ ...p, ...patch }))
  const editing = !!rule

  function submit() {
    if (editing) {
      onSubmit(rule.id, {
        name: f.name.trim(), dialect: f.dialect, category: f.category, level: f.level,
        enabled: rule.enabled, params: f.params.trim(), message: f.message.trim(),
        sortOrder: rule.sortOrder, spec: f.spec, specRef: f.specRef.trim(),
      })
      return
    }
    onSubmit(0, {
      code: f.code.trim(), name: f.name.trim(), dialect: f.dialect, category: f.category,
      level: f.level, enabled: true, message: f.message.trim(),
      spec: f.spec, specRef: f.specRef.trim(),
      params: JSON.stringify({ pattern: f.pattern.trim(), mode: 'forbid' }),
    })
  }

  const incomplete = editing
    ? !f.name.trim()
    : !f.code.trim() || !f.name.trim() || !f.pattern.trim()

  return (
    <Modal
      open
      title={t(editing ? 'srEditRule' : 'srNewRule')}
      sub={t(editing ? 'srEditRuleSub' : 'srNewRuleSub')}
      width={620}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>{t('cancel')}</Button>
          <Button variant="primary" disabled={busy || incomplete} onClick={submit}>{t('save')}</Button>
        </>
      }
    >
      <div className="fld-2">
        <div className="fld">
          <label>{t('srCode')}</label>
          <input
            value={f.code}
            disabled={editing}
            placeholder="no-select-star"
            onChange={(e) => set({ code: e.target.value })}
          />
        </div>
        <div className="fld">
          <label>{t('srName')}</label>
          <input value={f.name} onChange={(e) => set({ name: e.target.value })} />
        </div>
      </div>

      <div className="fld-2">
        <div className="fld">
          <label>{t('srDialect')}</label>
          <select value={f.dialect} onChange={(e) => set({ dialect: e.target.value })}>
            {catalog.dialects.map((d) => (
              <option key={d} value={d}>{d === 'all' ? t('srAllDialects') : d.toUpperCase()}</option>
            ))}
          </select>
        </div>
        <div className="fld">
          <label>{t('srCategory')}</label>
          <select value={f.category} onChange={(e) => set({ category: e.target.value })}>
            {(catalog.categories.length ? catalog.categories : [f.category]).map((c) => (
              <option key={c} value={c}>{c}</option>
            ))}
          </select>
        </div>
      </div>

      <div className="fld">
        <label>{t('srLevel')}</label>
        <Segmented
          value={f.level}
          options={LEVELS.map((lv) => ({ value: lv, label: t(`srLv_${lv}`) }))}
          onChange={(lv) => set({ level: lv })}
        />
      </div>

      {editing ? (
        <div className="fld">
          <label>{t('srParams')}</label>
          <textarea value={f.params} spellCheck={false} onChange={(e) => set({ params: e.target.value })} />
        </div>
      ) : (
        <div className="fld">
          <label>{t('srPattern')}</label>
          <textarea
            value={f.pattern}
            spellCheck={false}
            placeholder="SELECT\\s+\\*"
            onChange={(e) => set({ pattern: e.target.value })}
          />
        </div>
      )}

      <div className="fld">
        <label>{t('srMessage')}</label>
        <input
          value={f.message}
          placeholder={t('srMessagePh')}
          onChange={(e) => set({ message: e.target.value })}
        />
      </div>

      <div className="fld-2">
        <div className="fld">
          {/* 规范分级和 level 是两件事:把某条降成告警,不等于改了公司规范怎么定性它。 */}
          <label>{t('srSpecLevel')}</label>
          <select value={f.spec} onChange={(e) => set({ spec: e.target.value as ReviewSpec })}>
            <option value="">{t('srSpec_none')}</option>
            {(catalog.specs ?? SPECS).map((s) => (
              <option key={s} value={s}>{t(`srSpec_${s}`)}</option>
            ))}
          </select>
        </div>
        <div className="fld">
          <label>{t('srSpecRef')}</label>
          <input
            value={f.specRef}
            placeholder={t('srSpecRefPh')}
            onChange={(e) => set({ specRef: e.target.value })}
          />
        </div>
      </div>
    </Modal>
  )
}
